package uxaudit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tinyPNG is a real 1x1 PNG (sniffs as image/png), so the wire-shape tests carry
// bytes that a strict server would accept as a screenshot.
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// synthCaptures builds a deterministic two-view (× two-viewport) capture set with no
// browser, for the push-shim mapping/validation/wire tests.
func synthCaptures() []CapturedView {
	var out []CapturedView
	for _, name := range []string{"dashboard", "run-missing-models"} {
		for _, vp := range Viewports {
			out = append(out, CapturedView{
				View:     View{Name: name},
				Viewport: vp,
				Capture: ViewCapture{
					ScreenshotPNG:     tinyPNG,
					AxeJSON:           []byte(`{"violations":[]}`),
					NetworkJSON:       []byte(`[]`),
					AxeViolations:     2,
					ConsoleFirstParty: 1,
					NetworkThirdParty: 3,
				},
			})
		}
	}
	return out
}

// TestBuildPayloadShape asserts the mapping produces a schema-valid payload: one page
// per (view,viewport), distinct stable urls, allowed viewports, environment "lab",
// and exactly one referenced file per artifact.
func TestBuildPayloadShape(t *testing.T) {
	caps := synthCaptures()
	payload, files := BuildPayload("civitai-manager funnel", caps)

	if payload.Environment != EnvLab {
		t.Fatalf("environment = %q, want %q", payload.Environment, EnvLab)
	}
	if len(payload.Pages) != len(caps) {
		t.Fatalf("pages = %d, want %d", len(payload.Pages), len(caps))
	}
	seenURL := map[string]bool{}
	for i, pg := range payload.Pages {
		if pg.Viewport != ViewportMobile && pg.Viewport != ViewportDesktop {
			t.Errorf("page %d viewport %q not in {mobile,desktop}", i, pg.Viewport)
		}
		if pg.URL == "" || seenURL[pg.URL] {
			t.Errorf("page %d url %q empty or duplicated (must be a distinct stable identity)", i, pg.URL)
		}
		seenURL[pg.URL] = true
		if pg.Screenshot == "" || files[pg.Screenshot] == nil {
			t.Errorf("page %d screenshot %q has no file part", i, pg.Screenshot)
		}
		if pg.Axe != "" && files[pg.Axe] == nil {
			t.Errorf("page %d axe %q has no file part", i, pg.Axe)
		}
	}
	// Every provided file must be referenced (no orphans) and vice-versa.
	if err := payload.Validate(setOf(files)); err != nil {
		t.Fatalf("built payload failed validation: %v", err)
	}
}

// TestValidateRejectsBadPayloads is the negative case: a dangling artifact ref, a bad
// viewport, an orphan file, and an unknown finding type must all be rejected.
func TestValidateRejectsBadPayloads(t *testing.T) {
	cases := []struct {
		name     string
		payload  PushPayload
		provided map[string]bool
	}{
		{
			name: "dangling screenshot ref",
			payload: PushPayload{Environment: EnvLab, Pages: []PushPage{
				{URL: "a@mobile", Viewport: ViewportMobile, Screenshot: "missing.png"},
			}},
			provided: map[string]bool{}, // file not provided
		},
		{
			name: "bad viewport",
			payload: PushPayload{Environment: EnvLab, Pages: []PushPage{
				{URL: "a@tablet", Viewport: "tablet", Screenshot: "a.png"},
			}},
			provided: map[string]bool{"a.png": true},
		},
		{
			name: "orphan uploaded file",
			payload: PushPayload{Environment: EnvLab, Pages: []PushPage{
				{URL: "a@mobile", Viewport: ViewportMobile, Screenshot: "a.png"},
			}},
			provided: map[string]bool{"a.png": true, "orphan.png": true},
		},
		{
			name: "unknown finding type",
			payload: PushPayload{Environment: EnvLab, Pages: []PushPage{
				{URL: "a@mobile", Viewport: ViewportMobile, Screenshot: "a.png",
					Findings: []PushFinding{{Type: "bogus", Detail: "x"}}},
			}},
			provided: map[string]bool{"a.png": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.payload.Validate(tc.provided); err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

// TestUploadWireShape POSTs a built payload at an httptest fake mirroring auditloop's
// /api/plugins/runs and asserts the multipart decodes to the PushPayload shape and
// that there is exactly one file part per referenced artifact, form-name == filename.
func TestUploadWireShape(t *testing.T) {
	caps := synthCaptures()
	payload, files := BuildPayload("wire-shape", caps)
	metaJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var gotMeta PushPayload
	gotParts := map[string]bool{}
	var gotToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PushEndpoint {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		gotToken = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		// The metadata part must decode with DisallowUnknownFields (byte-compatible
		// with the server's strict decoder).
		metaVals := r.MultipartForm.Value["metadata"]
		if len(metaVals) != 1 {
			http.Error(w, "expected exactly one metadata part", http.StatusBadRequest)
			return
		}
		dec := json.NewDecoder(strings.NewReader(metaVals[0]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&gotMeta); err != nil {
			http.Error(w, "metadata decode: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Every file part: form-name must equal its filename; and every screenshot part
		// must sniff as PNG/JPEG (mirrors auditloop's server-side content check).
		for field, headers := range r.MultipartForm.File {
			for _, fh := range headers {
				if fh.Filename != field {
					http.Error(w, "form-name != filename", http.StatusBadRequest)
					return
				}
				if strings.HasSuffix(field, ".png") {
					f, _ := fh.Open()
					head := make([]byte, 512)
					n, _ := f.Read(head)
					f.Close()
					if ct := http.DetectContentType(head[:n]); ct != "image/png" && ct != "image/jpeg" {
						http.Error(w, "screenshot not PNG/JPEG: "+ct, http.StatusBadRequest)
						return
					}
				}
				gotParts[field] = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"r-1","url":"` + "http://auditloop.example/runs/r-1" + `"}`))
	}))
	defer srv.Close()

	res, err := Upload(context.Background(), srv.Client(), srv.URL, "secret-token", metaJSON, files)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.RunID != "r-1" {
		t.Errorf("run id = %q, want r-1", res.RunID)
	}
	if gotToken != "Bearer secret-token" {
		t.Errorf("auth header = %q, want bearer token", gotToken)
	}
	if gotMeta.Environment != EnvLab || len(gotMeta.Pages) != len(payload.Pages) {
		t.Errorf("decoded metadata mismatch: env=%q pages=%d", gotMeta.Environment, len(gotMeta.Pages))
	}
	// Exactly one part per referenced artifact — no missing, no orphan.
	refs := payload.ReferencedFiles()
	if len(gotParts) != len(refs) {
		t.Errorf("received %d file parts, want %d referenced artifacts", len(gotParts), len(refs))
	}
	for name := range refs {
		if !gotParts[name] {
			t.Errorf("referenced artifact %q was not uploaded as a part", name)
		}
	}
}

// TestPushConfigOptInGate asserts the opt-in gate: no env → no push config (the walk
// still runs + writes local artifacts, the push is skipped). Both token forms resolve.
func TestPushConfigOptInGate(t *testing.T) {
	// Unconfigured: nothing set.
	t.Setenv("AUDITLOOP_PUSH_URL", "")
	t.Setenv("AUDITLOOP_PUSH_TOKEN", "")
	t.Setenv("AUDITLOOP_PUSH_TOKENS", "")
	if _, _, ok := PushConfigFromEnv(); ok {
		t.Fatal("expected ok=false with no env configured")
	}

	// URL but no token → still not configured.
	t.Setenv("AUDITLOOP_PUSH_URL", "http://auditloop.example")
	if _, _, ok := PushConfigFromEnv(); ok {
		t.Fatal("expected ok=false with URL but no token")
	}

	// Bare token form.
	t.Setenv("AUDITLOOP_PUSH_TOKEN", "bare-token")
	baseURL, tok, ok := PushConfigFromEnv()
	if !ok || baseURL != "http://auditloop.example" || tok != "bare-token" {
		t.Fatalf("bare token: ok=%v base=%q tok=%q", ok, baseURL, tok)
	}

	// JSON-map token form keyed by the funnel target name.
	t.Setenv("AUDITLOOP_PUSH_TOKEN", "")
	t.Setenv("AUDITLOOP_PUSH_TOKENS", `{"`+PushTargetName+`":"map-token","other":"x"}`)
	_, tok, ok = PushConfigFromEnv()
	if !ok || tok != "map-token" {
		t.Fatalf("map token: ok=%v tok=%q", ok, tok)
	}

	// JSON map WITHOUT our key → not configured.
	t.Setenv("AUDITLOOP_PUSH_TOKENS", `{"someone-else":"x"}`)
	if _, _, ok := PushConfigFromEnv(); ok {
		t.Fatal("expected ok=false when the funnel key is absent")
	}
}

// TestMaybePushNoopWhenUnconfigured asserts MaybePush is a silent no-op (never POSTs)
// with no env — the non-fatal opt-in contract.
func TestMaybePushNoopWhenUnconfigured(t *testing.T) {
	t.Setenv("AUDITLOOP_PUSH_URL", "")
	t.Setenv("AUDITLOOP_PUSH_TOKEN", "")
	t.Setenv("AUDITLOOP_PUSH_TOKENS", "")
	url, attempted, err := MaybePush(context.Background(), &WalkResult{})
	if attempted || url != "" || err != nil {
		t.Fatalf("expected no-op, got attempted=%v url=%q err=%v", attempted, url, err)
	}
}
