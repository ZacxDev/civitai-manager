package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// managerHarness is an httptest ComfyUI whose route set can be shaped per test.
// It mimics the ONE behaviour that makes probing hard: aiohttp answers a GET on a
// POST-only route with 404, so an absent route and a wrong-method route are
// indistinguishable (verified live).
type managerHarness struct {
	mu sync.Mutex
	// requests records every (method, path?query) the client issued, in order.
	requests []string
}

func (h *managerHarness) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := r.URL.Path
	if r.URL.RawQuery != "" {
		p += "?" + r.URL.RawQuery
	}
	h.requests = append(h.requests, r.Method+" "+p)
}

func (h *managerHarness) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests...)
}

func (h *managerHarness) sawPath(substr string) bool {
	for _, r := range h.seen() {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

// newManagerHarness serves handlers by exact path; everything else 404s.
func newManagerHarness(t *testing.T, handlers map[string]http.HandlerFunc) (*Client, *managerHarness) {
	t.Helper()
	h := &managerHarness{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.record(r)
		if fn, ok := handlers[r.URL.Path]; ok {
			fn(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, ""), h
}

func fixtureHandler(t *testing.T, name string) http.HandlerFunc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// v3Handlers is the route set of the REAL Manager V3.41 subset we drive.
func v3Handlers(t *testing.T) map[string]http.HandlerFunc {
	t.Helper()
	return map[string]http.HandlerFunc{
		"/api/manager/queue/status": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"total_count": 0, "done_count": 0, "in_progress_count": 0, "is_processing": false}`))
		},
		"/api/manager/version": func(w http.ResponseWriter, _ *http.Request) {
			// V3 answers PLAIN TEXT, not JSON — verified live.
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("V3.41"))
		},
		"/api/customnode/getmappings": func(w http.ResponseWriter, r *http.Request) {
			// The real handler bracket-accesses query["mode"] -> KeyError -> 500.
			if r.URL.Query().Get("mode") == "" {
				http.Error(w, "KeyError: 'mode'", http.StatusInternalServerError)
				return
			}
			fixtureHandler(t, "nodepack_getmappings.json")(w, r)
		},
		"/api/customnode/getlist": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("mode") == "" {
				http.Error(w, "KeyError: 'mode'", http.StatusInternalServerError)
				return
			}
			fixtureHandler(t, "nodepack_getlist.json")(w, r)
		},
		"/api/customnode/installed": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("mode") == "imported" {
				fixtureHandler(t, "nodepack_installed_imported.json")(w, r)
				return
			}
			fixtureHandler(t, "nodepack_installed.json")(w, r)
		},
		"/queue": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"queue_running": [], "queue_pending": []}`))
		},
	}
}

// TestManagerProbeDetectsLines pins line detection, including the 🔴 case that
// "present" must mean a LIVE ANSWER — a Manager loaded in CLI-only mode (web API
// disabled) answers nothing and must read as absent, not as an error.
func TestManagerProbeDetectsLines(t *testing.T) {
	tests := []struct {
		name        string
		routes      []string
		wantPresent bool
		wantLine    string
		wantInstall bool
	}{
		{
			name:        "no manager at all",
			routes:      nil,
			wantPresent: false,
			wantLine:    ManagerLineNone,
		},
		{
			name:        "CLI-only mode: package loaded, web API disabled",
			routes:      []string{"/queue"}, // ComfyUI answers; Manager does not
			wantPresent: false,
			wantLine:    ManagerLineNone,
		},
		{
			name:        "V3",
			routes:      []string{"/api/manager/queue/status"},
			wantPresent: true,
			wantLine:    ManagerLineV3,
			wantInstall: true,
		},
		{
			name:        "V4 default",
			routes:      []string{"/api/v2/manager/queue/status"},
			wantPresent: true,
			wantLine:    ManagerLineV4,
			wantInstall: true,
		},
		{
			name:        "V4 legacy UI",
			routes:      []string{"/api/v2/manager/queue/history"},
			wantPresent: true,
			wantLine:    ManagerLineV4Legacy,
			wantInstall: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handlers := map[string]http.HandlerFunc{}
			for _, r := range tc.routes {
				handlers[r] = func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{}`))
				}
			}
			c, _ := newManagerHarness(t, handlers)
			info, err := c.ManagerProbe(context.Background())
			if err != nil {
				t.Fatalf("ManagerProbe: %v", err)
			}
			if info.Present != tc.wantPresent {
				t.Fatalf("Present = %v, want %v", info.Present, tc.wantPresent)
			}
			if info.Line != tc.wantLine {
				t.Errorf("Line = %q, want %q", info.Line, tc.wantLine)
			}
			if info.CanInstall != tc.wantInstall {
				t.Errorf("CanInstall = %v, want %v", info.CanInstall, tc.wantInstall)
			}
			if !tc.wantPresent && info.Note == "" {
				t.Error("an absent Manager must carry a user-facing Note, not just a false")
			}
		})
	}
}

// TestManagerProbeNeverProbesAPOSTOnlyRoute: probing must use GET-able,
// side-effect-free routes only. A GET on /manager/queue/install always 404s
// (verified live), so probing it would be both useless AND — if it were a POST —
// an install nobody asked for.
func TestManagerProbeNeverProbesAPOSTOnlyRoute(t *testing.T) {
	c, h := newManagerHarness(t, v3Handlers(t))
	if _, err := c.ManagerProbe(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, req := range h.seen() {
		if strings.Contains(req, "queue/install") || strings.Contains(req, "queue/start") ||
			strings.Contains(req, "reboot") || strings.Contains(req, "queue/task") ||
			strings.Contains(req, "queue/batch") {
			t.Errorf("probe touched a state-mutating route: %s", req)
		}
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("probe issued a non-GET request: %s", req)
		}
	}
}

// TestManagerProbeFullV3 pins the fully-populated V3 result against the real
// route set and the real (plain-text) version body.
func TestManagerProbeFullV3(t *testing.T) {
	c, _ := newManagerHarness(t, v3Handlers(t))
	info, err := c.ManagerProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Present || info.Line != ManagerLineV3 {
		t.Fatalf("info = %+v", info)
	}
	if info.Version != "V3.41" {
		t.Errorf("Version = %q, want V3.41 (plain-text body)", info.Version)
	}
	if !info.HasMappings || !info.HasNodePackList {
		t.Errorf("HasMappings=%v HasNodePackList=%v, want both true", info.HasMappings, info.HasNodePackList)
	}
}

// TestManagerProbeWithoutGetlist: getlist is GONE in V4's default mode. The probe
// must report that as a degraded-but-usable state with an explanation.
func TestManagerProbeWithoutGetlist(t *testing.T) {
	handlers := v3Handlers(t)
	delete(handlers, "/api/customnode/getlist")
	c, _ := newManagerHarness(t, handlers)

	info, err := c.ManagerProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Present {
		t.Fatal("a missing getlist must not make Manager read as absent")
	}
	if info.HasNodePackList {
		t.Error("HasNodePackList must be false")
	}
	if !info.HasMappings {
		t.Error("HasMappings must still be true")
	}
	if info.Note == "" {
		t.Error("the degraded state must be explained")
	}
	// And the client must surface the absence as an error, not an empty document.
	if _, err := c.ManagerNodePacks(context.Background()); err == nil {
		t.Error("ManagerNodePacks must error when getlist is absent")
	}
}

// TestManagerIndexesAlwaysSendModeCache is the 🔴 KeyError guard: both index
// handlers bracket-access query["mode"], so omitting it is an HTTP 500. The
// harness reproduces that 500 exactly.
func TestManagerIndexesAlwaysSendModeCache(t *testing.T) {
	c, h := newManagerHarness(t, v3Handlers(t))
	ctx := context.Background()

	if _, err := c.ManagerMappings(ctx); err != nil {
		t.Fatalf("ManagerMappings: %v", err)
	}
	if _, err := c.ManagerNodePacks(ctx); err != nil {
		t.Fatalf("ManagerNodePacks: %v", err)
	}

	var mappings, getlist string
	for _, req := range h.seen() {
		switch {
		case strings.Contains(req, "getmappings"):
			mappings = req
		case strings.Contains(req, "getlist"):
			getlist = req
		}
	}
	if !strings.Contains(mappings, "mode=cache") {
		t.Errorf("getmappings request %q is missing mode=cache", mappings)
	}
	if !strings.Contains(getlist, "mode=cache") {
		t.Errorf("getlist request %q is missing mode=cache", getlist)
	}
	if !strings.Contains(getlist, "skip_update=true") {
		t.Errorf("getlist request %q is missing skip_update=true", getlist)
	}
}

// TestManagerIndexRejectsNonJSON: a 200 that is not JSON (a proxy login page, a
// truncated body) must fail HERE rather than silently becoming an empty index.
func TestManagerIndexRejectsNonJSON(t *testing.T) {
	c, _ := newManagerHarness(t, map[string]http.HandlerFunc{
		"/api/customnode/getmappings": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>login</html>"))
		},
	})
	if _, err := c.ManagerMappings(context.Background()); err == nil {
		t.Fatal("expected an error for a non-JSON 200")
	}
}

// TestManagerIndexesFeedTheAttributionIndex is the end-to-end join: what the
// client fetches must build an index that attributes the real classes.
func TestManagerIndexesFeedTheAttributionIndex(t *testing.T) {
	c, _ := newManagerHarness(t, v3Handlers(t))
	ctx := context.Background()

	mappings, err := c.ManagerMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	getlist, err := c.ManagerNodePacks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := BuildIndex(mappings, getlist)
	if err != nil {
		t.Fatal(err)
	}
	packs, unattributed := ix.Attribute([]string{"CR Float To Integer", "RIFEInterpolation", "MMAudioSampler"})
	if len(packs) != 2 {
		t.Fatalf("packs = %+v, want 2", packs)
	}
	if len(unattributed) != 1 || unattributed[0] != "MMAudioSampler" {
		t.Errorf("unattributed = %v, want [MMAudioSampler] (the Registry rung covers it, not Manager)", unattributed)
	}
}

// TestManagerInstalledDiff pins the restart-pending signal: disk minus imported.
func TestManagerInstalledDiff(t *testing.T) {
	c, h := newManagerHarness(t, v3Handlers(t))
	pending, err := c.ManagerInstalledDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The fixtures are a real 4-pack disk scan and a real 3-pack startup snapshot.
	if len(pending) != 1 || pending[0] != "comfyui_controlnet_aux" {
		t.Fatalf("pending = %v, want [comfyui_controlnet_aux]", pending)
	}
	if !h.sawPath("mode=imported") {
		t.Errorf("the imported snapshot was never requested: %v", h.seen())
	}
}

// TestManagerInstalledDiffEmptyIsSteadyState: identical sets mean nothing is
// pending — the correct steady state, not a failure.
func TestManagerInstalledDiffEmptyIsSteadyState(t *testing.T) {
	same := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"a":{"ver":"1"},"b":{"ver":"2"}}`))
	}
	c, _ := newManagerHarness(t, map[string]http.HandlerFunc{"/api/customnode/installed": same})
	pending, err := c.ManagerInstalledDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty", pending)
	}
}

// TestManagerInstallV3Wire pins the V3 body and the two-call sequence, plus the
// 🔴 Content-Type requirement (Manager 400s text/plain, Go's default).
func TestManagerInstallV3Wire(t *testing.T) {
	var installBody map[string]any
	var installCT, startCT, startRaw string

	handlers := v3Handlers(t)
	handlers["/api/manager/queue/install"] = func(w http.ResponseWriter, r *http.Request) {
		installCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&installBody)
		w.WriteHeader(http.StatusOK)
	}
	handlers["/api/manager/queue/start"] = func(w http.ResponseWriter, r *http.Request) {
		startCT = r.Header.Get("Content-Type")
		b := make([]byte, 16)
		n, _ := r.Body.Read(b)
		startRaw = string(b[:n])
		w.WriteHeader(http.StatusOK)
	}
	c, h := newManagerHarness(t, handlers)

	info, err := c.ManagerProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pack := Pack{ID: "comfy-mtb", Title: "comfy-mtb", Repository: "https://github.com/melMass/comfy_mtb", Version: "0.5.4", Installable: true}
	if err := c.ManagerInstall(context.Background(), info, pack); err != nil {
		t.Fatalf("ManagerInstall: %v", err)
	}

	if !strings.HasPrefix(installCT, "application/json") {
		t.Errorf("install Content-Type = %q, want application/json (text/plain is 400'd)", installCT)
	}
	if !strings.HasPrefix(startCT, "application/json") {
		t.Errorf("queue/start Content-Type = %q, want application/json", startCT)
	}
	if strings.TrimSpace(startRaw) != "{}" {
		t.Errorf("queue/start body = %q, want {} (a no-body POST is rejected on Content-Type)", startRaw)
	}
	// V3's handler bracket-accesses these keys — omitting any is a 500/400.
	for _, k := range []string{"id", "version", "selected_version", "channel", "mode", "repository", "ui_id"} {
		if _, ok := installBody[k]; !ok {
			t.Errorf("V3 install body is missing required key %q: %v", k, installBody)
		}
	}
	if installBody["id"] != "comfy-mtb" || installBody["selected_version"] != "0.5.4" {
		t.Errorf("install body = %v", installBody)
	}
	// V3 queues then starts — one call alone installs nothing.
	if !h.sawPath("POST /api/manager/queue/install") || !h.sawPath("POST /api/manager/queue/start") {
		t.Errorf("V3 install must be install THEN start: %v", h.seen())
	}
}

// TestManagerInstallV4Wire pins the V4 envelope, which is a different shape on a
// different route — 4.2.1 consolidated the per-operation routes specifically
// breaking third-party callers.
func TestManagerInstallV4Wire(t *testing.T) {
	tests := []struct {
		name      string
		probePath string
		taskPath  string
		wantLine  string
	}{
		{"v4 default", "/api/v2/manager/queue/status", "/api/v2/manager/queue/task", ManagerLineV4},
		{"v4 legacy ui", "/api/v2/manager/queue/history", "/api/v2/manager/queue/batch", ManagerLineV4Legacy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			var ct string
			handlers := map[string]http.HandlerFunc{
				tc.probePath: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
				tc.taskPath: func(w http.ResponseWriter, r *http.Request) {
					ct = r.Header.Get("Content-Type")
					_ = json.NewDecoder(r.Body).Decode(&body)
					w.WriteHeader(http.StatusOK)
				},
			}
			c, h := newManagerHarness(t, handlers)
			info, err := c.ManagerProbe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.Line != tc.wantLine {
				t.Fatalf("Line = %q, want %q", info.Line, tc.wantLine)
			}
			pack := Pack{ID: "comfy-mtb", Repository: "https://github.com/melMass/comfy_mtb", Version: "0.5.4", Installable: true}
			if err := c.ManagerInstall(context.Background(), info, pack); err != nil {
				t.Fatalf("ManagerInstall: %v", err)
			}
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
			for _, k := range []string{"ui_id", "client_id", "kind", "params"} {
				if _, ok := body[k]; !ok {
					t.Errorf("V4 envelope is missing %q: %v", k, body)
				}
			}
			if body["kind"] != "install" {
				t.Errorf("kind = %v, want install", body["kind"])
			}
			params, _ := body["params"].(map[string]any)
			if params["id"] != "comfy-mtb" {
				t.Errorf("params = %v", params)
			}
			// V4 must NOT issue V3's separate start call.
			if h.sawPath("queue/start") {
				t.Errorf("V4 must not call V3's queue/start: %v", h.seen())
			}
		})
	}
}

// TestManagerInstallRefusesNonInstallable: a nightly-only pack routes Manager to
// its git-url path, which its default policy refuses. Offering it would be a
// button that always fails, so the client refuses before any request.
func TestManagerInstallRefusesNonInstallable(t *testing.T) {
	c, h := newManagerHarness(t, v3Handlers(t))
	info, err := c.ManagerProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := len(h.seen())
	pack := Pack{
		ID:          "ComfyUI_Comfyroll_CustomNodes",
		Title:       "Comfyroll Studio",
		Version:     "nightly",
		Installable: false,
		Reason:      "This pack ships only a nightly (git) build",
	}
	err = c.ManagerInstall(context.Background(), info, pack)
	if err == nil {
		t.Fatal("expected a refusal for a non-installable pack")
	}
	if !strings.Contains(err.Error(), "nightly") {
		t.Errorf("the refusal must repeat the pack's Reason, got %v", err)
	}
	if len(h.seen()) != before {
		t.Errorf("a refused install must issue NO request: %v", h.seen()[before:])
	}
}

// TestManagerInstallAbsentManager: with no Manager, install is ErrManagerAbsent
// and the caller degrades to the manual command.
func TestManagerInstallAbsentManager(t *testing.T) {
	c, _ := newManagerHarness(t, nil)
	info, err := c.ManagerProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = c.ManagerInstall(context.Background(), info, Pack{ID: "x", Installable: true})
	if !errors.Is(err, ErrManagerAbsent) {
		t.Fatalf("err = %v, want ErrManagerAbsent", err)
	}
	if err := c.ManagerInstall(context.Background(), nil, Pack{ID: "x", Installable: true}); !errors.Is(err, ErrManagerAbsent) {
		t.Fatalf("nil info: err = %v, want ErrManagerAbsent", err)
	}
}

// TestManagerInstallSurfacesManagerRefusal: Manager applies its own security
// policy and answers 403/404 with a terse text body; that reason must reach the
// user rather than being flattened.
func TestManagerInstallSurfacesManagerRefusal(t *testing.T) {
	handlers := v3Handlers(t)
	handlers["/api/manager/queue/install"] = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "A security error has occurred. Please check the terminal logs", http.StatusForbidden)
	}
	c, _ := newManagerHarness(t, handlers)
	info, _ := c.ManagerProbe(context.Background())
	err := c.ManagerInstall(context.Background(), info, Pack{ID: "x", Version: "1.0", Installable: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "security error") {
		t.Errorf("Manager's own reason was lost: %v", err)
	}
}

// TestManagerRebootRefusesBusyQueue is the 🔴 destructive-call guard. Manager's
// reboot is os.execv with NO queue inspection: it does not decline while a
// generation runs, it destroys it. The refusal is ours.
func TestManagerRebootRefusesBusyQueue(t *testing.T) {
	tests := []struct {
		name       string
		queueBody  string
		wantRefuse bool
	}{
		{"idle queue reboots", `{"queue_running": [], "queue_pending": []}`, false},
		{"running generation refuses", `{"queue_running": [[1,"abc",{}]], "queue_pending": []}`, true},
		{"pending generation refuses", `{"queue_running": [], "queue_pending": [[2,"def",{}]]}`, true},
		{"both refuses", `{"queue_running": [[1,"a",{}]], "queue_pending": [[2,"b",{}]]}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rebooted := false
			handlers := v3Handlers(t)
			handlers["/queue"] = func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.queueBody))
			}
			handlers["/api/manager/reboot"] = func(w http.ResponseWriter, _ *http.Request) {
				rebooted = true
				w.WriteHeader(http.StatusOK)
			}
			c, _ := newManagerHarness(t, handlers)
			info, _ := c.ManagerProbe(context.Background())

			err := c.ManagerReboot(context.Background(), info)
			if tc.wantRefuse {
				if !errors.Is(err, ErrQueueBusy) {
					t.Fatalf("err = %v, want ErrQueueBusy", err)
				}
				if rebooted {
					t.Fatal("🔴 the reboot was sent anyway — this destroys a running generation")
				}
				return
			}
			if err != nil {
				t.Fatalf("idle queue: %v", err)
			}
			if !rebooted {
				t.Fatal("an idle queue must actually reboot")
			}
		})
	}
}

// TestManagerRebootFailsClosedOnUnreadableQueue: if we cannot PROVE the queue is
// idle, we do not reboot.
func TestManagerRebootFailsClosedOnUnreadableQueue(t *testing.T) {
	rebooted := false
	handlers := v3Handlers(t)
	handlers["/queue"] = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}
	handlers["/api/manager/reboot"] = func(w http.ResponseWriter, _ *http.Request) {
		rebooted = true
	}
	c, _ := newManagerHarness(t, handlers)
	info, _ := c.ManagerProbe(context.Background())

	err := c.ManagerReboot(context.Background(), info)
	if !errors.Is(err, ErrQueueBusy) {
		t.Fatalf("err = %v, want ErrQueueBusy (fail closed)", err)
	}
	if rebooted {
		t.Fatal("rebooted without being able to read the queue")
	}
}

// TestManagerRebootTransportErrorIsSuccess: os.execv replaces the process
// mid-request, so the client sees a connection reset. That is SUCCESS.
func TestManagerRebootTransportErrorIsSuccess(t *testing.T) {
	handlers := v3Handlers(t)
	handlers["/api/manager/reboot"] = func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close without answering: the transport-level EOF a real
		// os.execv produces.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Skip("no hijack support")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}
	c, _ := newManagerHarness(t, handlers)
	info, _ := c.ManagerProbe(context.Background())
	if err := c.ManagerReboot(context.Background(), info); err != nil {
		t.Fatalf("a transport failure after send is SUCCESS (the process was replaced), got %v", err)
	}
}

// TestManagerRebootSendsJSONBody: reboot reads no body, so it IS gated by
// _reject_simple_form_content_type — "{}" with the JSON content type.
func TestManagerRebootSendsJSONBody(t *testing.T) {
	var ct, body string
	handlers := v3Handlers(t)
	handlers["/api/manager/reboot"] = func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		b := make([]byte, 8)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.WriteHeader(http.StatusOK)
	}
	c, _ := newManagerHarness(t, handlers)
	info, _ := c.ManagerProbe(context.Background())
	if err := c.ManagerReboot(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if strings.TrimSpace(body) != "{}" {
		t.Errorf("body = %q, want {}", body)
	}
}

// TestManagerRebootAbsent: no Manager, no reboot.
func TestManagerRebootAbsent(t *testing.T) {
	c, _ := newManagerHarness(t, nil)
	if err := c.ManagerReboot(context.Background(), nil); !errors.Is(err, ErrManagerAbsent) {
		t.Fatalf("err = %v, want ErrManagerAbsent", err)
	}
}

// TestManagerQueueStatusIsNotACompletionSignal documents the trap: is_processing
// is ALREADY false before you start, so polling it alone reports success for work
// that never ran.
func TestManagerQueueStatusIsNotACompletionSignal(t *testing.T) {
	c, _ := newManagerHarness(t, v3Handlers(t))
	info, _ := c.ManagerProbe(context.Background())
	st, err := c.ManagerQueueStatus(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if st.IsProcessing {
		t.Fatal("fixture drift: the real steady state is is_processing=false")
	}
	if st.TotalCount != 0 || st.DoneCount != 0 {
		t.Errorf("status = %+v", st)
	}
}

// TestComfyQueueBusy pins the guard's own read.
func TestComfyQueueBusy(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantBusy    bool
		wantRunning int
		wantPending int
	}{
		{"idle", `{"queue_running": [], "queue_pending": []}`, false, 0, 0},
		{"running", `{"queue_running": [[1,"a",{}]], "queue_pending": []}`, true, 1, 0},
		{"pending", `{"queue_running": [], "queue_pending": [[1,"a",{}],[2,"b",{}]]}`, true, 0, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newManagerHarness(t, map[string]http.HandlerFunc{
				"/queue": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) },
			})
			busy, running, pending, err := c.ComfyQueueBusy(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if busy != tc.wantBusy || running != tc.wantRunning || pending != tc.wantPending {
				t.Errorf("busy=%v running=%d pending=%d", busy, running, pending)
			}
		})
	}
}

// TestManagerProbeSurfacesTransportFailure: an unreachable ComfyUI is a REAL
// error, distinct from "Manager is absent".
func TestManagerProbeSurfacesTransportFailure(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "") // nothing listens on port 1
	info, err := c.ManagerProbe(context.Background())
	if err == nil {
		t.Fatal("expected a transport error for an unreachable ComfyUI")
	}
	if info == nil || info.Present {
		t.Fatalf("info = %+v", info)
	}
}

// TestManagerInstalledDiffIsSorted keeps the pending list deterministic.
func TestManagerInstalledDiffIsSorted(t *testing.T) {
	c, _ := newManagerHarness(t, map[string]http.HandlerFunc{
		"/api/customnode/installed": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("mode") == "imported" {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{"zeta":{},"alpha":{},"mid":{}}`))
		},
	})
	for i := 0; i < 5; i++ {
		got, err := c.ManagerInstalledDiff(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("pending is not sorted: %v", got)
		}
		if len(got) != 3 {
			t.Fatalf("pending = %v", got)
		}
	}
}
