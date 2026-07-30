package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// leakToken is the CivitAI token used by every render assertion below. It is long
// enough that config.RedactToken's last-4 tail ("****k4d2") cannot be mistaken for
// the whole value, and distinctive enough that a substring search for it is a
// meaningful leak check.
const leakToken = "cm-cloud-secret-DO-NOT-LEAK-9f3ak4d2"

// leakTokenRedacted is what MAY appear in markup.
const leakTokenRedacted = "****k4d2"

// postConnectRaw issues a form POST whose headers the caller can tamper with, so
// the CSRF cases can send a WRONG token as well as none at all.
func postConnectRaw(t *testing.T, srv *Server, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if mutate != nil {
		mutate(req)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// newConnectTestServer builds a loopback server with a workflow seeded and the
// cloud connect surface reachable. cfgCloud is the config-FILE value for
// comfy_cloud (nil = the key is absent, so the DB toggle governs).
func newConnectTestServer(t *testing.T, token string, cfgCloud *bool) (*Server, string) {
	t.Helper()
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.Token = token
	srv.cfg.ComfyCloud = cfgCloud
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	return srv, id
}

// --- Step 0: cloud auth reuses the ALREADY-CONFIGURED CivitAI token ----------

// TestCloudAuthReusesConfiguredToken pins the finding that made the "connect
// form" collapse into a toggle: the orchestration client is derived from
// cfg.Token and nothing else. Server.cloud() returns nil exactly when that token
// is blank, which is the only credential gate on the cloud path.
//
// The transport half — that the SAME string becomes `Authorization: Bearer …` on
// submit/poll/cancel — is pinned in internal/comfy/cloud_test.go.
func TestCloudAuthReusesConfiguredToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantNil bool
	}{
		{"no token → no cloud client", "", true},
		{"blank token → no cloud client", "   ", true},
		{"configured token → cloud client", leakToken, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newConnectTestServer(t, tc.token, boolp(true))
			// No cloudClientFn seam: exercise the production construction path.
			srv.cloudClientFn = nil
			got := srv.cloud()
			if tc.wantNil && got != nil {
				t.Errorf("cloud() = %v, want nil (no CivitAI token ⇒ no cloud auth)", got)
			}
			if !tc.wantNil && got == nil {
				t.Error("cloud() = nil, want a client built from the configured CivitAI token")
			}
		})
	}
}

// TestCloudConnectStoresNoCredential asserts the feature introduces no second
// secret: after exercising the whole connect flow, the ONLY settings row it wrote
// is the non-secret comfy_cloud toggle, and no stored value contains the token.
func TestCloudConnectStoresNoCredential(t *testing.T) {
	srv, id := newConnectTestServer(t, leakToken, nil)

	for _, val := range []string{"1", "0", "1"} {
		rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {val}}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("connect enabled=%s: status = %d", val, rec.Code)
		}
	}

	// The toggle key holds "1"/"0" and nothing else.
	got, ok, err := srv.store.GetSetting(comfyCloudSettingKey)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if !ok || got != "1" {
		t.Fatalf("comfy_cloud setting = %q, present=%v; want \"1\"", got, ok)
	}
	// No settings row anywhere holds the token. There is no cloud-credential key
	// to look up because the feature never invents one.
	for _, key := range []string{
		"comfy_cloud_token", "cloud_token", "civitai_token", "token",
		"comfy_cloud_secret", "orchestration_token",
	} {
		if v, present, err := srv.store.GetSetting(key); err != nil {
			t.Fatalf("get setting %q: %v", key, err)
		} else if present {
			t.Errorf("unexpected credential-shaped setting %q = %q — cloud must not store a secret", key, v)
		}
	}
	if got == leakToken || strings.Contains(got, leakToken) {
		t.Error("the CivitAI token leaked into the settings table")
	}
}

// TestCloudConnectDoesNotChangeDBFileMode documents the inverse of the "chmod the
// DB to 600 on the first secret write" requirement: because this feature stores
// NO secret, it must NOT silently re-permission the user's database. A secret
// write would need that hardening; a boolean toggle must leave the file alone.
func TestCloudConnectDoesNotChangeDBFileMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "civitai-manager.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "127.0.0.1:8787", Token: leakToken,
	}, nil)
	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)
	t.Cleanup(cancel)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("connect: status = %d", rec.Code)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if before.Mode() != after.Mode() {
		t.Errorf("DB mode changed %v → %v; the toggle stores no secret and must not re-permission the DB",
			before.Mode(), after.Mode())
	}
}

// --- render states -----------------------------------------------------------

func TestCloudConnectRenderStates(t *testing.T) {
	tests := []struct {
		name string
		// token configured on the server ("" = none).
		token string
		// cfgCloud is the config-FILE comfy_cloud value (nil = key absent).
		cfgCloud *bool
		// dbSetting, when non-empty, is pre-written to the settings table.
		dbSetting string
		want      []string
		notWant   []string
	}{
		{
			name:  "no credential: disabled toggle, no field, says where a token comes from",
			token: "",
			want: []string{
				"(none)",
				"No CivitAI token configured",
				"CIVITAI_TOKEN",
				"--token",
				"disabled",
				`aria-disabled="true"`,
			},
			// No editable credential input, and a disabled control must not carry the
			// POST it cannot perform.
			notWant: []string{`hx-post="/workflows/`, "<input", "type=\"password\""},
		},
		{
			name:      "credential from config: read-only, redacted, no toggle POST",
			token:     leakToken,
			cfgCloud:  boolp(true),
			dbSetting: "0", // the DB disagrees; the config file must still win
			want: []string{
				leakTokenRedacted,
				"Set in your config file",
				"comfy_cloud: true",
				"config file wins",
			},
			notWant: []string{`hx-post="/workflows/`, "turn off", "turn on"},
		},
		{
			name:     "config explicitly false: read-only and OFF",
			token:    leakToken,
			cfgCloud: boolp(false),
			want: []string{
				"Set in your config file",
				"comfy_cloud: false",
				"turned off",
			},
			notWant: []string{`hx-post="/workflows/`},
		},
		{
			name:      "credential + DB toggle off: an enabled 'turn on' control",
			token:     leakToken,
			dbSetting: "0",
			want: []string{
				leakTokenRedacted,
				"Cloud run is off — turn on",
				`hx-post="/workflows/`,
				`aria-pressed="false"`,
				`&#34;enabled&#34;:&#34;1&#34;`,
			},
			notWant: []string{"disabled", "Set in your config file"},
		},
		{
			name:      "credential + DB toggle on: the control clears it again",
			token:     leakToken,
			dbSetting: "1",
			want: []string{
				"Cloud run is on — turn off",
				`aria-pressed="true"`,
				`&#34;enabled&#34;:&#34;0&#34;`,
			},
			notWant: []string{"disabled", "Set in your config file"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newConnectTestServer(t, tc.token, tc.cfgCloud)
			if tc.dbSetting != "" {
				if err := srv.store.SetSetting(comfyCloudSettingKey, tc.dbSetting); err != nil {
					t.Fatalf("seed setting: %v", err)
				}
			}
			rec := get(t, srv, "/workflows/"+id+"/cloud/connect")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q:\n%s", want, body)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(body, notWant) {
					t.Errorf("body should not contain %q:\n%s", notWant, body)
				}
			}
		})
	}
}

// --- the assertion that matters most: the secret never reaches markup ---------

// TestCloudTokenNeverAppearsInRenderedHTML sweeps every surface that knows about
// the token and asserts the raw value appears NOWHERE in the response — not as
// text, not in an attribute, not in an hx-vals JSON blob. Only the redacted form
// is allowed out.
func TestCloudTokenNeverAppearsInRenderedHTML(t *testing.T) {
	cases := []struct {
		name     string
		cfgCloud *bool
	}{
		{"config-absent (DB toggle governs)", nil},
		{"config on", boolp(true)},
		{"config off", boolp(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newConnectTestServer(t, leakToken, tc.cfgCloud)

			var bodies []string
			collect := func(what, body string) {
				bodies = append(bodies, body)
				if strings.Contains(body, leakToken) {
					t.Errorf("%s LEAKED the raw CivitAI token:\n%s", what, body)
				}
				// Also catch a partial leak of the distinctive middle of the secret,
				// which no redaction should ever emit.
				if strings.Contains(body, "DO-NOT-LEAK") {
					t.Errorf("%s leaked part of the raw CivitAI token:\n%s", what, body)
				}
			}

			collect("GET connect", get(t, srv, "/workflows/"+id+"/cloud/connect").Body.String())
			collect("GET cloud panel", get(t, srv, "/workflows/"+id+"/cloud").Body.String())
			collect("GET workflow detail", get(t, srv, "/workflows/"+id).Body.String())
			for _, val := range []string{"1", "0"} {
				collect("POST connect enabled="+val,
					post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {val}}, true).Body.String())
			}
			collect("POST whatif",
				post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"urn:air:x"}}, true).Body.String())

			// Sanity: at least one surface actually rendered the token's redacted form,
			// so the leak sweep above is exercising a page that knows the secret rather
			// than passing vacuously.
			var sawRedacted bool
			for _, b := range bodies {
				if strings.Contains(b, leakTokenRedacted) {
					sawRedacted = true
				}
			}
			if !sawRedacted {
				t.Error("no surface rendered the redacted token — the leak sweep may be vacuous")
			}
		})
	}
}

// --- precedence, clearing, and the panel it drives ---------------------------

// TestCloudConfigWinsOverDBSetting pins the precedence in BEHAVIOUR, not just in
// copy: a DB toggle that disagrees with an explicit config value is ignored, in
// both directions, by the panel that actually gates the egress.
func TestCloudConfigWinsOverDBSetting(t *testing.T) {
	tests := []struct {
		name        string
		cfgCloud    *bool
		dbSetting   string
		wantEnabled bool
	}{
		{"config true beats DB off", boolp(true), "0", true},
		{"config false beats DB on", boolp(false), "1", false},
		{"config absent: DB on governs", nil, "1", true},
		{"config absent: DB off governs", nil, "0", false},
		{"config absent, DB unset: off by default", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newConnectTestServer(t, leakToken, tc.cfgCloud)
			if tc.dbSetting != "" {
				if err := srv.store.SetSetting(comfyCloudSettingKey, tc.dbSetting); err != nil {
					t.Fatalf("seed setting: %v", err)
				}
			}
			if got := srv.cloudEnabled(); got != tc.wantEnabled {
				t.Errorf("cloudEnabled() = %v, want %v", got, tc.wantEnabled)
			}
			body := get(t, srv, "/workflows/"+id+"/cloud").Body.String()
			off := strings.Contains(body, "Cloud run is off")
			if off == tc.wantEnabled {
				t.Errorf("panel off=%v but effective enabled=%v:\n%s", off, tc.wantEnabled, body)
			}
		})
	}
}

// TestCloudConnectToggleRoundTrip asserts turning cloud on persists, drives the
// panel, and that turning it back off CLEARS the stored enable.
func TestCloudConnectToggleRoundTrip(t *testing.T) {
	srv, id := newConnectTestServer(t, leakToken, nil)

	if srv.cloudEnabled() {
		t.Fatal("cloud should start off")
	}

	on := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true)
	if on.Code != http.StatusOK {
		t.Fatalf("turn on: status = %d", on.Code)
	}
	if !srv.cloudEnabled() {
		t.Error("cloud should be enabled after the toggle")
	}
	// The response re-renders the connect block AND asks the STABLE panel container
	// to reload — it never replaces the element that triggered the swap.
	onBody := on.Body.String()
	for _, want := range []string{
		"Cloud run is on — turn off",
		`hx-get="/workflows/` + id + `/cloud"`,
		`hx-target="#cloud-panel"`,
		`hx-swap="innerHTML"`,
	} {
		if !strings.Contains(onBody, want) {
			t.Errorf("toggle-on response missing %q:\n%s", want, onBody)
		}
	}
	if strings.Contains(onBody, `hx-target="#cloud-connect"`) && strings.Contains(onBody, `hx-swap="outerHTML"`) {
		t.Error("the connect fragment must not outerHTML-replace its own container")
	}
	if strings.Contains(get(t, srv, "/workflows/"+id+"/cloud").Body.String(), "Cloud run is off") {
		t.Error("panel still reports off after the toggle turned cloud on")
	}

	off := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"0"}}, true)
	if off.Code != http.StatusOK {
		t.Fatalf("turn off: status = %d", off.Code)
	}
	if srv.cloudEnabled() {
		t.Error("cloud should be disabled again after clearing the toggle")
	}
	if v, _, _ := srv.store.GetSetting(comfyCloudSettingKey); v != "0" {
		t.Errorf("cleared setting = %q, want \"0\"", v)
	}
	if !strings.Contains(get(t, srv, "/workflows/"+id+"/cloud").Body.String(), "Cloud run is off") {
		t.Error("panel should report off after the toggle was cleared")
	}
}

// TestCloudConnectRefusesEnableWithoutToken asserts the server refuses the exact
// transition its disabled button refuses, so a forged POST cannot store a state
// the user could never act on.
func TestCloudConnectRefusesEnableWithoutToken(t *testing.T) {
	srv, id := newConnectTestServer(t, "", nil)

	rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if srv.cloudEnabled() {
		t.Error("cloud must not be enabled without a CivitAI token")
	}
	if _, ok, _ := srv.store.GetSetting(comfyCloudSettingKey); ok {
		t.Error("the refused enable must not write the setting")
	}
	if !strings.Contains(rec.Body.String(), "no CivitAI token is configured") {
		t.Errorf("refusal should say why:\n%s", rec.Body.String())
	}
}

// TestCloudConnectConfigWinsRefusesWrite asserts a POST cannot override an
// explicit config-file value — the read-only UI and the server agree.
func TestCloudConnectConfigWinsRefusesWrite(t *testing.T) {
	srv, id := newConnectTestServer(t, leakToken, boolp(false))

	rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if srv.cloudEnabled() {
		t.Error("an explicit config-file comfy_cloud: false must not be overridable from the web")
	}
	if _, ok, _ := srv.store.GetSetting(comfyCloudSettingKey); ok {
		t.Error("a config-governed toggle must not write the DB setting")
	}
	if !strings.Contains(rec.Body.String(), "controlled by your config file") {
		t.Errorf("response should explain the precedence:\n%s", rec.Body.String())
	}
}

// --- hostile input -----------------------------------------------------------

// TestCloudConnectRejectsHostileInput asserts the `enabled` field is a strict
// enum: anything else is rejected outright, stored nowhere, and echoed nowhere.
func TestCloudConnectRejectsHostileInput(t *testing.T) {
	hostile := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"true (not the enum)", "true"},
		{"yes", "yes"},
		{"script tag", `<script>alert(1)</script>`},
		{"quote break-out", `1" onload="alert(1)`},
		{"oversized", strings.Repeat("A", 64<<10)},
		{"null byte", "1\x00"},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newConnectTestServer(t, leakToken, nil)
			rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {tc.value}}, true)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for enabled=%q", rec.Code, tc.value)
			}
			if _, ok, _ := srv.store.GetSetting(comfyCloudSettingKey); ok {
				t.Error("a rejected value must not be stored")
			}
			// Nothing attacker-supplied is reflected back.
			if tc.value != "" && strings.Contains(rec.Body.String(), tc.value) {
				t.Errorf("response reflected the hostile value:\n%s", rec.Body.String())
			}
		})
	}
}

// TestCloudConnectBadWorkflowID asserts a non-numeric path segment is rejected
// rather than reflected.
func TestCloudConnectBadWorkflowID(t *testing.T) {
	srv, _ := newConnectTestServer(t, leakToken, nil)
	for _, path := range []string{
		"/workflows/<script>/cloud/connect",
		"/workflows/notanid/cloud/connect",
	} {
		if rec := get(t, srv, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400", path, rec.Code)
		}
	}
	rec := post(t, srv, "/workflows/notanid/cloud/connect", url.Values{"enabled": {"1"}}, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST bad id: status = %d, want 400", rec.Code)
	}
}

// --- CSRF + loopback gating --------------------------------------------------

// TestCloudConnectCSRF asserts the write endpoint rejects a missing AND a wrong
// token, and changes nothing in either case.
func TestCloudConnectCSRF(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(req *http.Request, srv *Server)
		wantMsg string
	}{
		{"missing token", func(*http.Request, *Server) {}, "invalid or missing CSRF token"},
		{"wrong token", func(req *http.Request, _ *Server) {
			req.Header.Set("X-CSRF-Token", "not-the-token")
		}, "invalid or missing CSRF token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newConnectTestServer(t, leakToken, nil)
			rec := postConnectRaw(t, srv, "/workflows/"+id+"/cloud/connect", "enabled=1", func(req *http.Request) {
				tc.mutate(req, srv)
			})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantMsg)
			}
			if _, ok, _ := srv.store.GetSetting(comfyCloudSettingKey); ok {
				t.Error("a CSRF-rejected POST must not change state")
			}
		})
	}

	// The happy path with a valid token still works, so the assertions above are
	// about CSRF and not about a broken route.
	srv, id := newConnectTestServer(t, leakToken, nil)
	if rec := post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("valid CSRF: status = %d", rec.Code)
	}
}

// TestCloudConnectLoopbackGated asserts both connect endpoints are gated off a
// non-loopback bind, consistent with the neighbouring cloud routes.
func TestCloudConnectLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "0.0.0.0:8787", Token: leakToken,
	}, nil)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"GET", ""},
		{"POST", "enabled=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			if tc.name == "GET" {
				body = get(t, srv, "/workflows/"+id+"/cloud/connect").Body.String()
			} else {
				body = post(t, srv, "/workflows/"+id+"/cloud/connect", url.Values{"enabled": {"1"}}, true).Body.String()
			}
			if !strings.Contains(body, "non-loopback") {
				t.Errorf("expected the gating note off-loopback:\n%s", body)
			}
			if strings.Contains(body, leakToken) {
				t.Error("the gating response leaked the token")
			}
		})
	}
	if _, ok, _ := srv.store.GetSetting(comfyCloudSettingKey); ok {
		t.Error("a loopback-gated POST must not change state")
	}
}

// TestCloudGenerateBlockLoadsConnectContainer asserts the detail page ships the
// stable connect container that lazy-loads the block, directly above the panel.
func TestCloudGenerateBlockLoadsConnectContainer(t *testing.T) {
	srv, id := newConnectTestServer(t, leakToken, nil)
	body := get(t, srv, "/workflows/"+id).Body.String()
	for _, want := range []string{
		`id="cloud-connect"`,
		`hx-get="/workflows/` + id + `/cloud/connect"`,
		`id="cloud-panel"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	if strings.Index(body, `id="cloud-connect"`) > strings.Index(body, `id="cloud-panel"`) {
		t.Error("the connect block must render ABOVE the cloud panel container")
	}
}
