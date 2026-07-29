package uxaudit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// auditloopSchemaSource records where the harness's hand-mirrored push structs were
// copied from. Bump the commit/date whenever you re-mirror auditloop's schema.
const auditloopSchemaSource = "auditloop internal/plugin/schema.go @ 0da3004 (2026-07-24)"

// expectedTags is the json tag set (per struct) that auditloop's server expects,
// copied verbatim from auditloopSchemaSource. TestPushSchemaTagsMatchAuditloop
// reflects over the harness's structs and asserts they still emit EXACTLY these
// tags — so renaming/adding/removing a json tag in push.go fails a test here rather
// than silently drifting from the server and surfacing as a remote 400.
//
// NOTE auditloop's PushPage additionally accepts OPTIONAL `perf`/`layout` blocks
// (omitempty); this harness deliberately does not emit them (it captures no
// perf/layout measurements), so they are intentionally ABSENT from PushPage below.
var expectedTags = map[string][]string{
	"PushPayload": {"label", "environment", "pages"},
	"PushPage": {
		"url", "viewport", "screenshot", "axe", "network",
		"axe_violations", "console_first_party", "console_third_party",
		"network_first_party", "network_third_party", "findings",
	},
	"PushFinding": {"type", "severity", "detail"},
}

// jsonTags returns the json tag NAMES (options like ",omitempty" stripped) for a
// struct type, in field order.
func jsonTags(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}

// TestPushSchemaTagsMatchAuditloop is the DRIFT guard: it reflects over the harness's
// hand-mirrored PushPayload/PushPage/PushFinding and asserts each emits exactly the
// json tags auditloop's server schema defines (expectedTags, copied from
// auditloopSchemaSource). Renaming a tag in push.go (e.g. `url`→`page_url`) makes the
// reflected set differ from expectedTags and FAILS this test — the self-referential
// marshal/unmarshal round-trip in TestUploadWireShape structurally cannot catch that.
func TestPushSchemaTagsMatchAuditloop(t *testing.T) {
	t.Logf("mirrored from %s", auditloopSchemaSource)
	cases := []struct {
		name string
		typ  reflect.Type
	}{
		{"PushPayload", reflect.TypeOf(PushPayload{})},
		{"PushPage", reflect.TypeOf(PushPage{})},
		{"PushFinding", reflect.TypeOf(PushFinding{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonTags(tc.typ)
			want := expectedTags[tc.name]
			gotSorted := append([]string(nil), got...)
			wantSorted := append([]string(nil), want...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if !reflect.DeepEqual(gotSorted, wantSorted) {
				t.Fatalf("%s json tags drifted from auditloop's schema:\n  got:  %v\n  want: %v\n(update push.go AND %q together, or re-mirror auditloop schema.go)",
					tc.name, got, want, auditloopSchemaSource)
			}
		})
	}
}

// keyPaths collects the dotted key paths of a decoded JSON value, using "[]" for any
// slice element so a one-element fixture matches an N-element payload. Object keys are
// sorted for determinism.
func keyPaths(prefix string, v interface{}, out map[string]bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "_") { // fixture-only annotations (e.g. _comment)
				continue
			}
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			keyPaths(p, t[k], out)
		}
	case []interface{}:
		for _, e := range t {
			keyPaths(prefix+"[]", e, out)
		}
	}
}

// TestBuildPayloadMatchesGoldenSchema builds a fully-populated payload via the
// harness's real structs, marshals it, and asserts its json key STRUCTURE equals the
// checked-in golden fixture (testdata/expected_metadata_schema.json). This is a second,
// JSON-level drift guard: renaming/adding/removing a json tag in push.go changes the
// produced key set and FAILS here. The representative payload populates every emitted
// field (incl. the omitempty ones + a finding) so all keys appear.
func TestBuildPayloadMatchesGoldenSchema(t *testing.T) {
	payload := PushPayload{
		Label:       "civitai-manager funnel",
		Environment: EnvLab,
		Pages: []PushPage{{
			URL:               "dashboard@desktop",
			Viewport:          ViewportDesktop,
			Screenshot:        "dashboard.desktop.png",
			Axe:               "dashboard.desktop.axe.json",
			Network:           "dashboard.desktop.network.json",
			AxeViolations:     2,
			ConsoleFirstParty: 1,
			ConsoleThirdParty: 0,
			NetworkFirstParty: 0,
			NetworkThirdParty: 3,
			Findings: []PushFinding{{
				Type:     "a11y",
				Severity: "serious",
				Detail:   "image-alt — Images must have alternate text",
			}},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "expected_metadata_schema.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var golden interface{}
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		t.Fatalf("golden fixture is not valid JSON: %v", err)
	}

	gotKeys := map[string]bool{}
	goldenKeys := map[string]bool{}
	keyPaths("", got, gotKeys)
	keyPaths("", golden, goldenKeys)

	for k := range goldenKeys {
		if !gotKeys[k] {
			t.Errorf("golden key %q is MISSING from the built payload (a json tag was renamed/removed in push.go, or the golden fixture is stale)", k)
		}
	}
	for k := range gotKeys {
		if !goldenKeys[k] {
			t.Errorf("built payload has key %q ABSENT from the golden fixture (a json tag was added/renamed in push.go — update the fixture + re-mirror from %s)", k, auditloopSchemaSource)
		}
	}
}

// TestValidateFilesSizeCaps asserts the client-side size self-check: a normal payload
// passes, a single file part over 16 MiB is rejected, and an aggregate over 64 MiB is
// rejected (mirroring auditloop's server-side per-file + total caps).
func TestValidateFilesSizeCaps(t *testing.T) {
	p := &PushPayload{}

	// Normal small payload passes.
	if err := p.ValidateFiles(map[string][]byte{"a.png": tinyPNG, "b.png": tinyPNG}); err != nil {
		t.Fatalf("small payload should pass, got %v", err)
	}

	// One oversized file part → rejected.
	over := make([]byte, MaxFileBytes+1)
	if err := p.ValidateFiles(map[string][]byte{"big.png": over}); err == nil {
		t.Fatal("expected a >16 MiB file part to be rejected")
	}

	// Aggregate over the total cap (each file under the per-file cap) → rejected.
	chunk := make([]byte, MaxFileBytes-1) // just under the per-file cap
	files := map[string][]byte{}
	for i := 0; i*len(chunk) <= MaxTotalBytes; i++ {
		files[string(rune('a'+i))+".png"] = chunk
	}
	if err := p.ValidateFiles(files); err == nil {
		t.Fatal("expected a >64 MiB total upload to be rejected")
	}
}

// TestUploadWireShape POSTs a built payload at an httptest fake and asserts the fake
// ACCEPTS our multipart: the metadata decodes with DisallowUnknownFields and there is
// exactly one file part per referenced artifact (form-name == filename). It proves the
// harness's OWN wire encoding round-trips through a strict decoder — it does NOT prove
// cross-repo compatibility with auditloop's real schema (the fake mirrors, not imports,
// the server). Schema drift is caught by TestPushSchemaTagsMatchAuditloop +
// TestBuildPayloadMatchesGoldenSchema instead.
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
