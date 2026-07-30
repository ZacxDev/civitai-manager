package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/comfyext"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedUIWorkflow inserts a UI-format workflow (the "Open in ComfyUI" path only
// accepts UI graphs) and returns its id.
func seedUIWorkflow(t *testing.T, srv *Server, name, graph string) int64 {
	t.Helper()
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: name, Format: store.WorkflowFormatUI, Graph: graph, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed ui workflow: %v", err)
	}
	return id
}

// openComfyServer builds a loopback server with a ComfyUI configured and the given
// fake client wired in.
func openComfyServer(t *testing.T, fake *fakeComfy) *Server {
	t.Helper()
	srv := newWorkflowServer(t)
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"
	return srv
}

// liveHelper is a fake ComfyUI whose helper answers BOTH detection legs: the ping
// route AND the frontend script. Anything less is not usable — see
// TestOpenComfyZombieHelperIsNotUsable.
func liveHelper() *fakeComfy {
	return &fakeComfy{pingInfo: &comfy.ExtensionInfo{Tool: "civitai-manager", Version: "1"}}
}

// zombieHelper is the LIVE-CAUGHT failure this feature had to be hardened against:
// the helper directory was deleted, but ComfyUI registered its python routes at
// STARTUP and still holds the handlers in memory, so /civitai-manager/ping answers
// 200 with our exact body. The frontend script — the half that actually opens
// anything — is served from disk and 404s.
func zombieHelper() *fakeComfy {
	f := liveHelper()
	f.assetErr = comfy.ErrExtensionAbsent
	return f
}

// fakeComfyRoot builds a directory that looks like a ComfyUI install: custom_nodes/
// AND a ComfyUI fingerprint (custom_nodes/ alone is deliberately not enough — it
// matches the folder CONTAINING a ComfyUI install, which is a real mix-up).
func fakeComfyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, comfyext.CustomNodesDir), 0o755); err != nil {
		t.Fatalf("make custom_nodes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("# comfyui\n"), 0o644); err != nil {
		t.Fatalf("make main.py: %v", err)
	}
	return root
}

// deepLinkURL is the URL a usable helper must redirect the new tab to.
func deepLinkURL(rel string) string {
	return "http://127.0.0.1:8188/?" + openComfyParam + "=" + url.QueryEscape(rel)
}

// TestSanitizeWorkflowFilename proves the userdata filename is traversal-safe: no
// path separators, no "..", always "<safe>-<id>.json".
func TestSanitizeWorkflowFilename(t *testing.T) {
	cases := []string{
		"../../etc/passwd", `a\b/c`, "   ", "", "normal name!", "汉字 only",
		"..", "./.", "a/../../b", strings.Repeat("x", 200),
	}
	for _, name := range cases {
		got := sanitizeWorkflowFilename(name, 7)
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("name %q → %q contains a path separator", name, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("name %q → %q contains ..", name, got)
		}
		if !strings.HasSuffix(got, "-7.json") {
			t.Errorf("name %q → %q should end with -7.json", name, got)
		}
	}
}

// --- P1: the click must OPEN ComfyUI, not report about it ---

// TestOpenComfyControlIsAFormThatOpensANewTab pins the mechanism the "just open
// it" behaviour depends on. The control MUST be a real form POST with
// target="_blank": the browser opens the tab synchronously from the click gesture
// (so no popup blocker) and the handler can then redirect that tab into ComfyUI.
// An htmx button can only answer with markup, which is exactly how this used to
// render "we saved it — now click this OTHER link".
func TestOpenComfyControlIsAFormThatOpensANewTab(t *testing.T) {
	uiWF := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", true, comfyHelperView{}, workflowResolver{}))

	if !strings.Contains(got, `<form method="post" action="/workflows/3/open-in-comfyui" target="_blank"`) {
		t.Errorf("the open control must be a form POST that opens a new tab:\n%s", got)
	}
	if !strings.Contains(got, `name="csrf_token"`) {
		t.Errorf("the open form must carry the CSRF token as a field:\n%s", got)
	}
	if !strings.Contains(got, `type="submit"`) {
		t.Errorf("the open control must submit the form:\n%s", got)
	}
	if strings.Contains(got, `hx-post="/workflows/3/open-in-comfyui"`) {
		t.Errorf("the open control must NOT be an htmx POST (it cannot open a tab):\n%s", got)
	}
}

// TestOpenComfyRedirectsTheNewTabIntoComfyUI is the P1 behaviour: with a usable
// helper the click saves the workflow, broadcasts the jump for an already-open
// tab, and 303-redirects the NEW tab straight to ?cm_open=<path> — no
// intermediate "here is what happened" page, no second link to click.
func TestOpenComfyRedirectsTheNewTabIntoComfyUI(t *testing.T) {
	fake := liveHelper()
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "portrait", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (the new tab must be redirected into ComfyUI):\n%s", rec.Code, rec.Body.String())
	}
	rel := "civitai-manager/portrait-" + itoa64(id) + ".json"
	if got := rec.Header().Get("Location"); got != deepLinkURL(rel) {
		t.Errorf("Location = %q, want %q", got, deepLinkURL(rel))
	}
	if !fake.saveCalled || fake.savedRelPath != rel {
		t.Errorf("the workflow must be saved first: called=%v path=%q", fake.saveCalled, fake.savedRelPath)
	}
	// The websocket broadcast still fires, so an ALREADY-open tab jumps too.
	if fake.openCalls != 1 || fake.openRelPath != rel {
		t.Errorf("open broadcast = %d call(s) for %q, want 1 for %q", fake.openCalls, fake.openRelPath, rel)
	}
	// The response is a redirect, so it can never carry a "click here" link.
	if strings.Contains(rec.Body.String(), "No ComfyUI tab open?") {
		t.Errorf("the redirect must not carry a second link to click:\n%s", rec.Body.String())
	}
}

// TestOpenComfyAbsentHelperRendersPathAndIssuesNoRedirect proves the absent case
// does NOT dump the user on a blank ComfyUI tab: it renders the exact
// Workflows-menu path, a copy button and the install offer, and redirects nowhere.
func TestOpenComfyAbsentHelperRendersPathAndIssuesNoRedirect(t *testing.T) {
	fake := &fakeComfy{}
	srv := openComfyServer(t, fake)
	srv.cfg.ComfyRoot = fakeComfyRoot(t)

	id := seedUIWorkflow(t, srv, "../evil name", "{\"nodes\":[]}")
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no helper → no redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("an absent helper must not redirect anywhere, got Location %q", loc)
	}
	if !fake.saveCalled {
		t.Fatal("SaveUserWorkflow was not called")
	}
	if !strings.HasPrefix(fake.savedRelPath, "civitai-manager/") {
		t.Errorf("saved path %q not namespaced under civitai-manager/", fake.savedRelPath)
	}
	if strings.Contains(fake.savedRelPath, "..") || strings.Contains(strings.TrimPrefix(fake.savedRelPath, "civitai-manager/"), "/") {
		t.Errorf("saved path %q must be traversal-safe", fake.savedRelPath)
	}
	if string(fake.savedGraph) != "{\"nodes\":[]}" {
		t.Errorf("saved graph mismatch: %s", fake.savedGraph)
	}
	body := rec.Body.String()
	// A standalone PAGE, not a bare fragment: the response landed in a new tab.
	if !strings.Contains(body, "<html") || !strings.Contains(body, "Open in ComfyUI · civitai-manager") {
		t.Errorf("the new tab must receive a full page, not a fragment:\n%s", body)
	}
	if !strings.Contains(body, "Workflows → civitai-manager → evil-name-"+itoa64(id)+".json") {
		t.Errorf("fallback must show the exact Workflows-menu path:\n%s", body)
	}
	if !strings.Contains(body, `data-copy="civitai-manager/evil-name-`+itoa64(id)+`.json"`) || !strings.Contains(body, "Copy path") {
		t.Errorf("fallback must offer a copy-path button:\n%s", body)
	}
	if !strings.Contains(body, "/comfy/extension/install") {
		t.Errorf("fallback must offer the one-click helper install:\n%s", body)
	}
	if !strings.Contains(body, `href="http://127.0.0.1:8188/"`) {
		t.Errorf("fallback should link the ComfyUI root:\n%s", body)
	}
	if strings.Contains(body, openComfyParam+"=") {
		t.Errorf("no helper detected → no %s deep link may be emitted:\n%s", openComfyParam, body)
	}
	if fake.openCalls != 0 {
		t.Error("no helper detected → the open broadcast must not be attempted")
	}
}

// TestOpenComfyNeverEmitsWorkflowParam is the regression pin for the bug this
// feature exists to fix: `?workflow=` is NOT a real ComfyUI parameter (verified
// across frontend 1.45.20/1.47.10/~1.49), so it silently dropped the user on
// whatever graph was open last. NO code path — response body OR redirect target —
// may emit it any more.
func TestOpenComfyNeverEmitsWorkflowParam(t *testing.T) {
	root := fakeComfyRoot(t)
	locationAndBody := func(srv *Server, id int64) string {
		rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
		return rec.Header().Get("Location") + "\n" + rec.Body.String()
	}
	variants := map[string]func(t *testing.T) string{
		"helper absent": func(t *testing.T) string {
			srv := openComfyServer(t, &fakeComfy{})
			return locationAndBody(srv, seedUIWorkflow(t, srv, "wf", "{}"))
		},
		"helper present": func(t *testing.T) string {
			srv := openComfyServer(t, liveHelper())
			return locationAndBody(srv, seedUIWorkflow(t, srv, "wf", "{}"))
		},
		"zombie helper (ping only)": func(t *testing.T) string {
			srv := openComfyServer(t, zombieHelper())
			return locationAndBody(srv, seedUIWorkflow(t, srv, "wf", "{}"))
		},
		"installed on disk but not yet active": func(t *testing.T) string {
			srv := openComfyServer(t, &fakeComfy{})
			srv.cfg.ComfyRoot = root
			if _, err := comfyext.Install(root); err != nil {
				t.Fatalf("install: %v", err)
			}
			return locationAndBody(srv, seedUIWorkflow(t, srv, "wf", "{}"))
		},
		"detail page": func(t *testing.T) string {
			wf := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
			return renderString(t, detailPageNode(wf, "csrf", "dark", "blur", true, comfyHelperView{}, workflowResolver{}))
		},
	}
	for name, render := range variants {
		t.Run(name, func(t *testing.T) {
			body := render(t)
			if strings.Contains(body, "?workflow=") || strings.Contains(body, "&workflow=") {
				t.Errorf("a dead ?workflow= ComfyUI link is back:\n%s", body)
			}
		})
	}
}

// TestOpenComfyBadComfyURLDoesNotRedirect proves a comfy_url that is not a safe
// http(s) address degrades to the honest page instead of becoming a bogus redirect
// target.
func TestOpenComfyBadComfyURLDoesNotRedirect(t *testing.T) {
	srv := openComfyServer(t, liveHelper())
	srv.cfg.ComfyURL = "javascript:alert(1)"
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("status = %d, Location = %q — an unsafe comfy_url must not become a redirect",
			rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "not a valid http(s) address") {
		t.Errorf("expected an explicit bad-URL note:\n%s", rec.Body.String())
	}
}

// --- P3: feature detection must not be fooled by a zombie helper ---

// TestOpenComfyZombieHelperIsNotUsable is the most important detection test, and
// it reproduces exactly what was observed on the live system after a user removed
// the helper:
//
//	custom_nodes/civitai-manager  → deleted
//	GET /civitai-manager/ping     → 200 {"tool":"civitai-manager","version":"1"}
//	GET /extensions/…/…js         → 404
//
// The python routes survive because ComfyUI registers them at startup and holds
// the handlers in memory; the script is served from disk and is gone. The app used
// to call that "helper present", claim it had asked a tab to jump, and NOTHING
// would happen.
func TestOpenComfyZombieHelperIsNotUsable(t *testing.T) {
	fake := zombieHelper()
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("a ping-only zombie must NOT count as usable: status=%d Location=%q",
			rec.Code, rec.Header().Get("Location"))
	}
	body := rec.Body.String()
	if strings.Contains(body, openComfyParam+"=") {
		t.Errorf("a zombie helper must not produce a %s link:\n%s", openComfyParam, body)
	}
	if !strings.Contains(body, "Copy path") {
		t.Errorf("a zombie helper must fall back to the copy-path UI:\n%s", body)
	}
	if !strings.Contains(body, "restart ComfyUI once") {
		t.Errorf("a zombie helper must tell the user the restart is what fixes it:\n%s", body)
	}
	if fake.openCalls != 0 {
		t.Error("a zombie helper must not be asked to broadcast an open")
	}
	if fake.assetCalls == 0 {
		t.Error("detection must actually probe the frontend asset")
	}
}

// TestOpenComfyUsabilityNeedsBothLegs walks the detection truth table: only
// ping-OK AND asset-OK is usable, and a ping that fails must not waste a probe on
// the asset.
func TestOpenComfyUsabilityNeedsBothLegs(t *testing.T) {
	cases := []struct {
		name       string
		fake       *fakeComfy
		wantUsable bool
		wantAsset  int
	}{
		{"ping ok + asset ok", liveHelper(), true, 1},
		{"ping ok + asset 404 (zombie)", zombieHelper(), false, 1},
		{"ping ok + asset timeout", func() *fakeComfy {
			f := liveHelper()
			f.assetErr = context.DeadlineExceeded
			return f
		}(), false, 1},
		{"ping ok + asset oversized", func() *fakeComfy {
			f := liveHelper()
			f.assetErr = errors.New("body exceeds the 262144-byte cap")
			return f
		}(), false, 1},
		{"ping ok + asset served by something else", func() *fakeComfy {
			f := liveHelper()
			f.assetErr = errors.New("the script served is not the civitai-manager helper")
			return f
		}(), false, 1},
		{"ping absent", &fakeComfy{}, false, 0},
		{"ping timeout", &fakeComfy{pingErr: context.DeadlineExceeded}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := openComfyServer(t, tc.fake)
			id := seedUIWorkflow(t, srv, "wf", "{}")
			rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)

			redirected := rec.Code == http.StatusSeeOther
			if redirected != tc.wantUsable {
				t.Errorf("redirected = %v, want %v (status %d)", redirected, tc.wantUsable, rec.Code)
			}
			if tc.fake.assetCalls != tc.wantAsset {
				t.Errorf("assetCalls = %d, want %d (a failed ping must not probe the asset)",
					tc.fake.assetCalls, tc.wantAsset)
			}
			if !tc.wantUsable {
				if body := rec.Body.String(); !strings.Contains(body, "Copy path") {
					t.Errorf("an unusable helper must fall back to the copy-path UI:\n%s", body)
				}
				if tc.fake.openCalls != 0 {
					t.Error("an unusable helper must not be asked to broadcast an open")
				}
			}
		})
	}
}

// TestOpenComfyProbeIsCachedAndNotRunOnRender proves feature detection (BOTH legs)
// is cached across clicks and is never run while rendering the workflow detail
// page — which now also renders the helper-management disclosure, so the
// no-probe-on-render rule has to survive that too.
func TestOpenComfyProbeIsCachedAndNotRunOnRender(t *testing.T) {
	fake := liveHelper()
	srv := openComfyServer(t, fake)
	srv.cfg.ComfyRoot = fakeComfyRoot(t)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	// Rendering the page must not probe — neither leg.
	rec := get(t, srv, "/workflows/"+itoa64(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail page status = %d", rec.Code)
	}
	if fake.pingCalls != 0 || fake.assetCalls != 0 {
		t.Fatalf("page render probed the helper (ping=%d asset=%d) — detection must never run on a render path",
			fake.pingCalls, fake.assetCalls)
	}
	// The management disclosure still rendered, from on-disk state alone.
	if !strings.Contains(rec.Body.String(), "ComfyUI helper (advanced)") {
		t.Errorf("the detail page must render the helper-management disclosure:\n%s", rec.Body.String())
	}

	for i := 0; i < 3; i++ {
		postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	}
	if fake.pingCalls != 1 || fake.assetCalls != 1 {
		t.Errorf("probe calls across 3 clicks = ping %d / asset %d, want 1 each (cached)",
			fake.pingCalls, fake.assetCalls)
	}

	// An install/uninstall must invalidate the cache — driven through the REAL
	// endpoint, not by poking the invalidator, so the wiring itself is covered.
	if rec := postForm(srv, "/comfy/extension/install", "", true); rec.Code != http.StatusOK {
		t.Fatalf("install status = %d", rec.Code)
	}
	postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if fake.pingCalls != 2 || fake.assetCalls != 2 {
		t.Errorf("after an install: ping %d / asset %d, want 2 each (the install must invalidate the cache)",
			fake.pingCalls, fake.assetCalls)
	}
	if rec := postForm(srv, "/comfy/extension/uninstall", "", true); rec.Code != http.StatusOK {
		t.Fatalf("uninstall status = %d", rec.Code)
	}
	postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if fake.pingCalls != 3 || fake.assetCalls != 3 {
		t.Errorf("after an uninstall: ping %d / asset %d, want 3 each (the uninstall must invalidate the cache)",
			fake.pingCalls, fake.assetCalls)
	}
}

// TestOpenComfyExtensionOpenGetsATightBudget proves the jump broadcast cannot hold
// the user's click for the whole save budget: the deadline handed to ExtensionOpen
// is the tight extOpenTimeout, not the enclosing 15s handler context.
func TestOpenComfyExtensionOpenGetsATightBudget(t *testing.T) {
	fake := liveHelper()
	var budget time.Duration
	fake.openFunc = func(ctx context.Context, _ string) error {
		if dl, ok := ctx.Deadline(); ok {
			budget = time.Until(dl)
		}
		return nil
	}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")
	postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)

	if budget <= 0 {
		t.Fatal("ExtensionOpen got no deadline at all")
	}
	if budget > extOpenTimeout {
		t.Errorf("ExtensionOpen budget = %s, want <= %s (a wedged helper must not hold the click)", budget, extOpenTimeout)
	}
}

// TestOpenComfyProbeFailuresFallBackHonestly proves a timeout, a transport error
// and a garbage/other-tool response all degrade to the copy-path fallback — never
// to a redirect into a ComfyUI that cannot open anything.
func TestOpenComfyProbeFailuresFallBackHonestly(t *testing.T) {
	cases := map[string]*fakeComfy{
		"timeout":         {pingErr: context.DeadlineExceeded},
		"transport error": {pingErr: errors.New("connection refused")},
		"404 / absent":    {pingErr: comfy.ErrExtensionAbsent},
		"garbage body":    {pingErr: comfy.ErrExtensionAbsent},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			srv := openComfyServer(t, fake)
			id := seedUIWorkflow(t, srv, "wf", "{}")
			rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
			if rec.Code == http.StatusSeeOther {
				t.Errorf("a failed probe must not redirect into ComfyUI (Location %q)", rec.Header().Get("Location"))
			}
			body := rec.Body.String()
			if strings.Contains(body, openComfyParam+"=") {
				t.Errorf("a failed probe must not produce a deep link:\n%s", body)
			}
			if !strings.Contains(body, "Copy path") {
				t.Errorf("a failed probe must fall back to the copy-path UI:\n%s", body)
			}
			if fake.openCalls != 0 {
				t.Error("a failed probe must not broadcast an open")
			}
		})
	}
}

// TestOpenComfyHelperVanishedBetweenProbeAndOpen proves that if the helper
// disappears after a positive probe (a restart, an uninstall), the response is the
// honest fallback and the stale cache is dropped.
func TestOpenComfyHelperVanishedBetweenProbeAndOpen(t *testing.T) {
	fake := liveHelper()
	fake.openErr = comfy.ErrExtensionAbsent
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code == http.StatusSeeOther {
		t.Errorf("a vanished helper must not be redirected into (Location %q)", rec.Header().Get("Location"))
	}
	body := rec.Body.String()
	if strings.Contains(body, openComfyParam+"=") {
		t.Errorf("a vanished helper must not leave a deep link behind:\n%s", body)
	}
	if !strings.Contains(body, "Copy path") {
		t.Errorf("expected the honest fallback:\n%s", body)
	}
	srv.extProbeMu.Lock()
	cached := srv.extProbeVal
	srv.extProbeMu.Unlock()
	if cached != nil {
		t.Error("a vanished helper must invalidate the cached probe")
	}
}

// TestOpenComfyCSRFRejected proves a POST without a CSRF token is refused and does
// NOT reach ComfyUI.
func TestOpenComfyCSRFRejected(t *testing.T) {
	fake := &fakeComfy{}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if fake.saveCalled {
		t.Error("CSRF-rejected open must not reach ComfyUI")
	}
}

// TestOpenComfyLoopbackGated proves a non-loopback bind gates the endpoint (it
// never writes to ComfyUI).
func TestOpenComfyLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8972", ComfyURL: "http://127.0.0.1:8188"}, nil)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected loopback-gating note, got:\n%s", rec.Body.String())
	}
	if fake.saveCalled {
		t.Error("gated open must not reach ComfyUI")
	}
}

// TestOpenComfyAPIFormatRefused proves an API-format workflow is refused (it does
// not load into the editor) without reaching ComfyUI.
func TestOpenComfyAPIFormatRefused(t *testing.T) {
	fake := &fakeComfy{}
	srv := openComfyServer(t, fake)
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "api", Format: store.WorkflowFormatAPI, Graph: testAPIGraph, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if !strings.Contains(rec.Body.String(), "UI-format") {
		t.Errorf("API-format open should be refused with a reason:\n%s", rec.Body.String())
	}
	if fake.saveCalled {
		t.Error("API-format open must not reach ComfyUI")
	}
}

// TestOpenComfyUnreachable proves an unreachable ComfyUI degrades to a clear error
// (no crash, 200).
func TestOpenComfyUnreachable(t *testing.T) {
	fake := &fakeComfy{saveErr: errors.New("connection refused")}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Could not reach ComfyUI") {
		t.Errorf("unreachable ComfyUI should show a clear error:\n%s", rec.Body.String())
	}
}

// TestWorkflowDetailOpenComfyButtonGating proves the control is shown ONLY for
// UI-format workflows with a configured comfy_url.
func TestWorkflowDetailOpenComfyButtonGating(t *testing.T) {
	uiWF := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	apiWF := &store.Workflow{ID: 4, Name: "api", Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported}

	// UI + comfy configured → control present.
	got := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", true, comfyHelperView{}, workflowResolver{}))
	if !strings.Contains(got, "/workflows/3/open-in-comfyui") || !strings.Contains(got, "Open in ComfyUI") {
		t.Errorf("UI + comfy should show the Open-in-ComfyUI control:\n%s", got)
	}
	// API + comfy configured → no control (API graphs don't load into the editor).
	gotAPI := renderString(t, detailPageNode(apiWF, "csrf", "dark", "blur", true, comfyHelperView{}, workflowResolver{}))
	if strings.Contains(gotAPI, "open-in-comfyui") {
		t.Errorf("API-format detail must not show the Open-in-ComfyUI control:\n%s", gotAPI)
	}
	// UI + comfy NOT configured → no control.
	gotNoComfy := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", false, comfyHelperView{}, workflowResolver{}))
	if strings.Contains(gotNoComfy, "open-in-comfyui") {
		t.Errorf("no comfy_url should hide the Open-in-ComfyUI control:\n%s", gotNoComfy)
	}
}

// --- P2: the destructive control must not sit in the per-click result ---

// TestOpenComfyResultCarriesNoRemovalControl is the direct pin for the reported
// problem: a "Remove helper" button used to render INLINE with the success text,
// and a user clicked it not knowing it disabled one-click open entirely. No
// per-click outcome may carry an uninstall control, in ANY of its shapes.
func TestOpenComfyResultCarriesNoRemovalControl(t *testing.T) {
	installedRoot := fakeComfyRoot(t)
	if _, err := comfyext.Install(installedRoot); err != nil {
		t.Fatalf("install: %v", err)
	}
	variants := map[string]func(t *testing.T) string{
		"helper absent": func(t *testing.T) string {
			srv := openComfyServer(t, &fakeComfy{})
			srv.cfg.ComfyRoot = fakeComfyRoot(t)
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
		"zombie helper": func(t *testing.T) string {
			srv := openComfyServer(t, zombieHelper())
			srv.cfg.ComfyRoot = installedRoot
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
		"installed on disk, ComfyUI not restarted": func(t *testing.T) string {
			srv := openComfyServer(t, &fakeComfy{})
			srv.cfg.ComfyRoot = installedRoot
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
	}
	for name, render := range variants {
		t.Run(name, func(t *testing.T) {
			body := render(t)
			if strings.Contains(body, "/comfy/extension/uninstall") {
				t.Errorf("a per-click result must never carry the uninstall action:\n%s", body)
			}
			for _, word := range []string{"Remove helper", "Uninstall"} {
				if strings.Contains(body, word) {
					t.Errorf("a per-click result must not offer %q:\n%s", word, body)
				}
			}
		})
	}
}

// TestOpenComfyInstalledButNotRestarted proves the specific, easy-to-misread state
// — helper on disk, ComfyUI not restarted — is called out explicitly instead of
// silently falling back.
func TestOpenComfyInstalledButNotRestarted(t *testing.T) {
	root := fakeComfyRoot(t)
	if _, err := comfyext.Install(root); err != nil {
		t.Fatalf("install: %v", err)
	}
	srv := openComfyServer(t, &fakeComfy{}) // ping absent: not restarted yet
	srv.cfg.ComfyRoot = root
	id := seedUIWorkflow(t, srv, "wf", "{}")

	body := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
	if !strings.Contains(body, "installed but not active yet") {
		t.Errorf("expected the restart-required note:\n%s", body)
	}
}

// TestComfyHelperDisclosureIsTheManagementSurface proves helper management lives
// in a deliberate, collapsed, clearly-labelled disclosure — and that the
// destructive action states its consequence BEFORE the button, not after.
func TestComfyHelperDisclosureIsTheManagementSurface(t *testing.T) {
	uiWF := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	root := fakeComfyRoot(t)

	t.Run("not installed: status + install only", func(t *testing.T) {
		hv := comfyHelperView{disk: comfyext.Inspect(root), rootSet: true, csrf: "csrf"}
		got := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", true, hv, workflowResolver{}))
		if !strings.Contains(got, "<summary") || !strings.Contains(got, "ComfyUI helper (advanced)") {
			t.Errorf("management must sit behind a labelled disclosure:\n%s", got)
		}
		if !strings.Contains(got, "Status: not installed") {
			t.Errorf("the disclosure must state the current status:\n%s", got)
		}
		if !strings.Contains(got, `hx-post="/comfy/extension/install"`) {
			t.Errorf("the disclosure must offer the install:\n%s", got)
		}
		if strings.Contains(got, "/comfy/extension/uninstall") {
			t.Errorf("nothing to uninstall → no uninstall control:\n%s", got)
		}
	})

	t.Run("installed: status + uninstall with its consequence", func(t *testing.T) {
		if _, err := comfyext.Install(root); err != nil {
			t.Fatalf("install: %v", err)
		}
		hv := comfyHelperView{disk: comfyext.Inspect(root), rootSet: true, csrf: "csrf"}
		got := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", true, hv, workflowResolver{}))
		if !strings.Contains(got, "Status: installed (v"+comfyext.ExtensionVersion+")") {
			t.Errorf("the disclosure must state the installed version:\n%s", got)
		}
		if !strings.Contains(got, `hx-post="/comfy/extension/uninstall"`) {
			t.Errorf("the disclosure must offer the uninstall:\n%s", got)
		}
		// The consequence is spelled out, and it precedes the button.
		consequence := "one-click open will stop working"
		if !strings.Contains(got, consequence) {
			t.Errorf("the uninstall must say what it costs:\n%s", got)
		}
		if strings.Index(got, consequence) > strings.Index(got, "/comfy/extension/uninstall") {
			t.Error("the consequence must be stated BEFORE the uninstall button, not after it")
		}
		if !strings.Contains(got, "hx-confirm=") {
			t.Errorf("the uninstall must require a confirmation:\n%s", got)
		}
	})

	t.Run("no comfy_root: explains why management is unavailable", func(t *testing.T) {
		got := renderString(t, detailPageNode(uiWF, "csrf", "dark", "blur", true, comfyHelperView{}, workflowResolver{}))
		if !strings.Contains(got, "no ComfyUI install directory configured") {
			t.Errorf("expected the unset-root status:\n%s", got)
		}
		if strings.Contains(got, "/comfy/extension/install") || strings.Contains(got, "/comfy/extension/uninstall") {
			t.Errorf("no root → no install/uninstall controls:\n%s", got)
		}
	})
}

// --- helper extension install / uninstall endpoints ---

// TestComfyExtensionEndpointsNeedNoWorkflow proves helper management is not tied to
// a workflow page: the endpoints take NOTHING from the request but the CSRF token,
// so they work from any surface — and a leftover extra field can neither break them
// nor reach the response.
func TestComfyExtensionEndpointsNeedNoWorkflow(t *testing.T) {
	root := fakeComfyRoot(t)
	srv := openComfyServer(t, &fakeComfy{})
	srv.cfg.ComfyRoot = root

	// No body at all — no workflow_id, no page context.
	rec := postForm(srv, "/comfy/extension/install", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install with an empty body → %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="`+comfyExtContainerID+`"`) {
		t.Errorf("the result must swap the constant helper container:\n%s", rec.Body.String())
	}

	// A hostile leftover field is simply ignored: it is not parsed, not validated
	// against a workflow, and not reflected.
	hostile := url.Values{"workflow_id": {`7"><script>alert(1)</script>`}}.Encode()
	rec = postForm(srv, "/comfy/extension/uninstall", hostile, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall with a junk workflow_id → %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Errorf("no request value may reach the markup:\n%s", rec.Body.String())
	}
}

// TestComfyExtensionInstallWritesTree proves the explicit install action writes the
// helper into the configured ComfyUI root and tells the user about the restart.
func TestComfyExtensionInstallWritesTree(t *testing.T) {
	root := fakeComfyRoot(t)
	srv := openComfyServer(t, &fakeComfy{})
	srv.cfg.ComfyRoot = root

	rec := postForm(srv, "/comfy/extension/install", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "RESTART ComfyUI once") {
		t.Errorf("install result must state the one-restart requirement:\n%s", body)
	}
	if !strings.Contains(body, `id="`+comfyExtContainerID+`"`) {
		t.Errorf("install result must swap its own container:\n%s", body)
	}
	// The action re-states the new status in place.
	if !strings.Contains(body, "Status: installed (v"+comfyext.ExtensionVersion+")") {
		t.Errorf("install result must re-render the updated status:\n%s", body)
	}
	dir := filepath.Join(root, comfyext.CustomNodesDir, comfyext.DirName)
	for _, rel := range []string{"__init__.py", filepath.Join("web", comfyext.AssetName), comfyext.MarkerName} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("install did not write %s: %v", rel, err)
		}
	}
}

// TestComfyExtensionUninstallIsHonestAboutTheRestart proves the removal result says
// what is actually true afterwards: the directory is gone AND civitai-manager
// already reports the helper as not installed, but ComfyUI keeps serving the
// helper's startup-registered routes until it is restarted.
func TestComfyExtensionUninstallIsHonestAboutTheRestart(t *testing.T) {
	root := fakeComfyRoot(t)
	srv := openComfyServer(t, &fakeComfy{})
	srv.cfg.ComfyRoot = root
	if _, err := comfyext.Install(root); err != nil {
		t.Fatalf("install: %v", err)
	}

	rec := postForm(srv, "/comfy/extension/uninstall", "", true)
	body := rec.Body.String()
	if !strings.Contains(body, "Removed the ComfyUI helper") {
		t.Fatalf("uninstall result:\n%s", body)
	}
	if _, err := os.Stat(comfyext.Dir(root)); !os.IsNotExist(err) {
		t.Error("uninstall must remove the helper directory")
	}
	if !strings.Contains(body, "RESTART ComfyUI once") || !strings.Contains(body, "stay live in memory") {
		t.Errorf("uninstall must say the routes survive until a restart:\n%s", body)
	}
	// The app itself must report "not installed" IMMEDIATELY.
	if !strings.Contains(body, "Status: not installed") {
		t.Errorf("after removal the status must read not-installed at once:\n%s", body)
	}
	// And detection agrees: a still-answering ping does not resurrect the helper.
	fake := zombieHelper()
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedUIWorkflow(t, srv, "wf", "{}")
	if rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true); rec.Code == http.StatusSeeOther {
		t.Error("after removal, a still-answering ping must not make the helper usable again")
	}
}

// TestComfyExtensionInstallRefusesForeignAndBadRoot proves the endpoint reports —
// rather than forces — a non-ComfyUI root and a directory it did not write.
func TestComfyExtensionInstallRefusesForeignAndBadRoot(t *testing.T) {
	t.Run("no root configured", func(t *testing.T) {
		srv := openComfyServer(t, &fakeComfy{})
		rec := postForm(srv, "/comfy/extension/install", "", true)
		if !strings.Contains(rec.Body.String(), "comfy_root") {
			t.Errorf("expected a set-comfy_root message:\n%s", rec.Body.String())
		}
	})

	t.Run("not a ComfyUI install", func(t *testing.T) {
		bare := t.TempDir()
		srv := openComfyServer(t, &fakeComfy{})
		srv.cfg.ComfyRoot = bare
		rec := postForm(srv, "/comfy/extension/install", "", true)
		if !strings.Contains(rec.Body.String(), comfyext.CustomNodesDir) {
			t.Errorf("expected a does-not-look-like-ComfyUI message:\n%s", rec.Body.String())
		}
		if entries, _ := os.ReadDir(bare); len(entries) != 0 {
			t.Error("a refused install must write nothing")
		}
	})

	t.Run("foreign directory is never clobbered", func(t *testing.T) {
		root := fakeComfyRoot(t)
		dir := filepath.Join(root, comfyext.CustomNodesDir, comfyext.DirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		keep := filepath.Join(dir, "someones_node.py")
		if err := os.WriteFile(keep, []byte("# not ours"), 0o644); err != nil {
			t.Fatal(err)
		}
		srv := openComfyServer(t, &fakeComfy{})
		srv.cfg.ComfyRoot = root

		rec := postForm(srv, "/comfy/extension/install", "", true)
		if !strings.Contains(rec.Body.String(), "refusing to overwrite") {
			t.Errorf("expected a refuse-to-overwrite message:\n%s", rec.Body.String())
		}
		data, err := os.ReadFile(keep)
		if err != nil || string(data) != "# not ours" {
			t.Error("a foreign directory must be left exactly as it was")
		}
		rec = postForm(srv, "/comfy/extension/uninstall", "", true)
		if !strings.Contains(rec.Body.String(), "refusing to remove") {
			t.Errorf("expected a refuse-to-remove message:\n%s", rec.Body.String())
		}
		if _, err := os.Stat(keep); err != nil {
			t.Error("a refused uninstall must not delete anything")
		}
	})

	t.Run("uninstall when nothing is installed", func(t *testing.T) {
		srv := openComfyServer(t, &fakeComfy{})
		srv.cfg.ComfyRoot = fakeComfyRoot(t)
		rec := postForm(srv, "/comfy/extension/uninstall", "", true)
		if !strings.Contains(rec.Body.String(), "not installed") {
			t.Errorf("expected a nothing-to-remove message:\n%s", rec.Body.String())
		}
	})
}

// TestComfyExtensionEndpointsAreCSRFAndLoopbackGated proves the two write
// endpoints carry the same protection as every other path-taking action — from
// their NEW home, with no workflow context: a missing CSRF token is 403 and a
// non-loopback bind refuses, in both cases without touching the filesystem.
func TestComfyExtensionEndpointsAreCSRFAndLoopbackGated(t *testing.T) {
	for _, path := range []string{"/comfy/extension/install", "/comfy/extension/uninstall"} {
		t.Run("csrf "+path, func(t *testing.T) {
			root := fakeComfyRoot(t)
			srv := openComfyServer(t, &fakeComfy{})
			srv.cfg.ComfyRoot = root
			rec := postForm(srv, path, "", false)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if _, err := os.Stat(comfyext.Dir(root)); !os.IsNotExist(err) {
				t.Error("a CSRF-rejected request must not touch the ComfyUI install")
			}
		})

		t.Run("loopback "+path, func(t *testing.T) {
			root := fakeComfyRoot(t)
			st, err := store.Open(":memory:")
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
				BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
				Addr: "0.0.0.0:8972", ComfyURL: "http://127.0.0.1:8188", ComfyRoot: root,
			}, nil)
			rec := postForm(srv, path, "", true)
			if !strings.Contains(rec.Body.String(), "non-loopback") {
				t.Errorf("expected loopback gating, got:\n%s", rec.Body.String())
			}
			if _, err := os.Stat(comfyext.Dir(root)); !os.IsNotExist(err) {
				t.Error("a gated request must not touch the ComfyUI install")
			}
		})
	}
}
