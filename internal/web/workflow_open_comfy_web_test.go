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

// fakeComfyRoot builds a directory that looks like a ComfyUI install.
func fakeComfyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, comfyext.CustomNodesDir), 0o755); err != nil {
		t.Fatalf("make custom_nodes: %v", err)
	}
	return root
}

// extForm builds the install/uninstall POST body.
func extForm(workflowID string) string {
	return url.Values{"workflow_id": {workflowID}}.Encode()
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

// TestOpenComfyWritesSanitizedNamespacedPath proves the endpoint writes the UI
// graph to ComfyUI userdata under a sanitized, namespaced path, and — with NO
// helper installed — renders the honest fallback: the exact Workflows-menu path
// with a copy button and a parameterless ComfyUI link.
func TestOpenComfyWritesSanitizedNamespacedPath(t *testing.T) {
	fake := &fakeComfy{}
	srv := openComfyServer(t, fake)

	id := seedUIWorkflow(t, srv, "../evil name", "{\"nodes\":[]}")
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
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
	// The exact menu path is spelled out, and copyable.
	if !strings.Contains(body, "Workflows → civitai-manager → evil-name-"+itoa64(id)+".json") {
		t.Errorf("fallback must show the exact Workflows-menu path:\n%s", body)
	}
	if !strings.Contains(body, `data-copy="civitai-manager/evil-name-`+itoa64(id)+`.json"`) || !strings.Contains(body, "Copy path") {
		t.Errorf("fallback must offer a copy-path button:\n%s", body)
	}
	// A parameterless ComfyUI link only — never a fabricated deep link.
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
// whatever graph was open last. NO code path may emit it any more.
func TestOpenComfyNeverEmitsWorkflowParam(t *testing.T) {
	root := fakeComfyRoot(t)
	variants := map[string]func(t *testing.T) string{
		"helper absent": func(t *testing.T) string {
			fake := &fakeComfy{}
			srv := openComfyServer(t, fake)
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
		"helper present": func(t *testing.T) string {
			fake := &fakeComfy{pingInfo: &comfy.ExtensionInfo{Tool: "civitai-manager", Version: "1"}}
			srv := openComfyServer(t, fake)
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
		"installed on disk but not yet active": func(t *testing.T) string {
			fake := &fakeComfy{}
			srv := openComfyServer(t, fake)
			srv.cfg.ComfyRoot = root
			if _, err := comfyext.Install(root); err != nil {
				t.Fatalf("install: %v", err)
			}
			id := seedUIWorkflow(t, srv, "wf", "{}")
			return postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
		},
		"detail page": func(t *testing.T) string {
			wf := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
			return renderString(t, workflowDetailPage(wf, "{}", "csrf", "dark", "blur", nil, true, workflowResolver{}))
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

// TestOpenComfyHelperDetectedJumpsAndDeepLinks proves that WITH the helper the
// endpoint asks it to jump an already-open tab and offers the ?cm_open= deep link
// (which works only because our helper implements it).
func TestOpenComfyHelperDetectedJumpsAndDeepLinks(t *testing.T) {
	fake := &fakeComfy{pingInfo: &comfy.ExtensionInfo{Tool: "civitai-manager", Version: "1"}}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "portrait", "{}")

	body := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
	rel := "civitai-manager/portrait-" + itoa64(id) + ".json"
	if fake.openCalls != 1 || fake.openRelPath != rel {
		t.Errorf("open broadcast = %d call(s) for %q, want 1 for %q", fake.openCalls, fake.openRelPath, rel)
	}
	want := "http://127.0.0.1:8188/?" + openComfyParam + "=" + url.QueryEscape(rel)
	if !strings.Contains(body, `href="`+want+`"`) {
		t.Errorf("expected the %s deep link %q:\n%s", openComfyParam, want, body)
	}
	if !strings.Contains(body, "jump to it") {
		t.Errorf("expected the jump confirmation:\n%s", body)
	}
	if !strings.Contains(body, "helper v1 is active") {
		t.Errorf("expected the active-helper version note:\n%s", body)
	}
}

// TestOpenComfyProbeIsCachedAndNotRunOnRender proves feature detection is cached
// across clicks (one network probe, not one per click) and is never run while
// rendering the workflow detail page.
func TestOpenComfyProbeIsCachedAndNotRunOnRender(t *testing.T) {
	fake := &fakeComfy{pingInfo: &comfy.ExtensionInfo{Tool: "civitai-manager", Version: "1"}}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	// Rendering the page must not probe.
	rec := get(t, srv, "/workflows/"+itoa64(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail page status = %d", rec.Code)
	}
	if fake.pingCalls != 0 {
		t.Fatalf("page render probed the helper %d time(s) — detection must never run on a render path", fake.pingCalls)
	}

	for i := 0; i < 3; i++ {
		postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	}
	if fake.pingCalls != 1 {
		t.Errorf("pingCalls = %d across 3 clicks, want 1 (cached)", fake.pingCalls)
	}

	// An install invalidates the cache, so the next click re-probes.
	srv.invalidateComfyExtensionProbe()
	postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if fake.pingCalls != 2 {
		t.Errorf("pingCalls = %d after invalidation, want 2", fake.pingCalls)
	}
}

// TestOpenComfyProbeFailuresFallBackHonestly proves a timeout, a transport error
// and a garbage/other-tool response all degrade to the copy-path fallback — never
// to a fabricated deep link.
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
			body := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
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
	fake := &fakeComfy{
		pingInfo: &comfy.ExtensionInfo{Tool: "civitai-manager", Version: "1"},
		openErr:  comfy.ErrExtensionAbsent,
	}
	srv := openComfyServer(t, fake)
	id := seedUIWorkflow(t, srv, "wf", "{}")

	body := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true).Body.String()
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

// TestWorkflowDetailOpenComfyButtonGating proves the button is shown ONLY for
// UI-format workflows with a configured comfy_url.
func TestWorkflowDetailOpenComfyButtonGating(t *testing.T) {
	uiWF := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	apiWF := &store.Workflow{ID: 4, Name: "api", Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported}

	// UI + comfy configured → button present.
	got := renderString(t, workflowDetailPage(uiWF, "{}", "csrf", "dark", "blur", nil, true, workflowResolver{}))
	if !strings.Contains(got, "/workflows/3/open-in-comfyui") || !strings.Contains(got, "Open in ComfyUI") {
		t.Errorf("UI + comfy should show the Open-in-ComfyUI button:\n%s", got)
	}
	// API + comfy configured → no button (API graphs don't load into the editor).
	gotAPI := renderString(t, workflowDetailPage(apiWF, "{}", "csrf", "dark", "blur", nil, true, workflowResolver{}))
	if strings.Contains(gotAPI, "open-in-comfyui") {
		t.Errorf("API-format detail must not show the Open-in-ComfyUI button:\n%s", gotAPI)
	}
	// UI + comfy NOT configured → no button.
	gotNoComfy := renderString(t, workflowDetailPage(uiWF, "{}", "csrf", "dark", "blur", nil, false, workflowResolver{}))
	if strings.Contains(gotNoComfy, "open-in-comfyui") {
		t.Errorf("no comfy_url should hide the Open-in-ComfyUI button:\n%s", gotNoComfy)
	}
}

// --- helper extension install / uninstall endpoints ---

// TestComfyExtensionInstallWritesTree proves the explicit install action writes the
// helper into the configured ComfyUI root and tells the user about the restart.
func TestComfyExtensionInstallWritesTree(t *testing.T) {
	root := fakeComfyRoot(t)
	srv := openComfyServer(t, &fakeComfy{})
	srv.cfg.ComfyRoot = root

	rec := postForm(srv, "/comfy/extension/install", extForm("7"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "RESTART ComfyUI once") {
		t.Errorf("install result must state the one-restart requirement:\n%s", body)
	}
	if !strings.Contains(body, `id="cm-comfy-ext-7"`) {
		t.Errorf("install result must swap its own container:\n%s", body)
	}
	dir := filepath.Join(root, comfyext.CustomNodesDir, comfyext.DirName)
	for _, rel := range []string{"__init__.py", filepath.Join("web", "civitai_manager.js"), comfyext.MarkerName} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("install did not write %s: %v", rel, err)
		}
	}

	// Uninstall removes it again.
	rec = postForm(srv, "/comfy/extension/uninstall", extForm("7"), true)
	if !strings.Contains(rec.Body.String(), "Removed the ComfyUI helper") {
		t.Errorf("uninstall result:\n%s", rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("uninstall must remove the helper directory")
	}
}

// TestComfyExtensionInstallRefusesForeignAndBadRoot proves the endpoint reports —
// rather than forces — a non-ComfyUI root and a directory it did not write.
func TestComfyExtensionInstallRefusesForeignAndBadRoot(t *testing.T) {
	t.Run("no root configured", func(t *testing.T) {
		srv := openComfyServer(t, &fakeComfy{})
		rec := postForm(srv, "/comfy/extension/install", extForm("7"), true)
		if !strings.Contains(rec.Body.String(), "comfy_root") {
			t.Errorf("expected a set-comfy_root message:\n%s", rec.Body.String())
		}
	})

	t.Run("not a ComfyUI install", func(t *testing.T) {
		bare := t.TempDir()
		srv := openComfyServer(t, &fakeComfy{})
		srv.cfg.ComfyRoot = bare
		rec := postForm(srv, "/comfy/extension/install", extForm("7"), true)
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

		rec := postForm(srv, "/comfy/extension/install", extForm("7"), true)
		if !strings.Contains(rec.Body.String(), "refusing to overwrite") {
			t.Errorf("expected a refuse-to-overwrite message:\n%s", rec.Body.String())
		}
		data, err := os.ReadFile(keep)
		if err != nil || string(data) != "# not ours" {
			t.Error("a foreign directory must be left exactly as it was")
		}
		rec = postForm(srv, "/comfy/extension/uninstall", extForm("7"), true)
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
		rec := postForm(srv, "/comfy/extension/uninstall", extForm("7"), true)
		if !strings.Contains(rec.Body.String(), "not installed") {
			t.Errorf("expected a nothing-to-remove message:\n%s", rec.Body.String())
		}
	})
}

// TestComfyExtensionEndpointsAreCSRFAndLoopbackGated proves the two write
// endpoints carry the same protection as every other path-taking action: a missing
// CSRF token is 403 and a non-loopback bind refuses — in both cases without
// touching the filesystem.
func TestComfyExtensionEndpointsAreCSRFAndLoopbackGated(t *testing.T) {
	for _, path := range []string{"/comfy/extension/install", "/comfy/extension/uninstall"} {
		t.Run("csrf "+path, func(t *testing.T) {
			root := fakeComfyRoot(t)
			srv := openComfyServer(t, &fakeComfy{})
			srv.cfg.ComfyRoot = root
			rec := postForm(srv, path, extForm("7"), false)
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
			rec := postForm(srv, path, extForm("7"), true)
			if !strings.Contains(rec.Body.String(), "non-loopback") {
				t.Errorf("expected loopback gating, got:\n%s", rec.Body.String())
			}
			if _, err := os.Stat(comfyext.Dir(root)); !os.IsNotExist(err) {
				t.Error("a gated request must not touch the ComfyUI install")
			}
		})
	}
}

// TestComfyExtensionRejectsBadWorkflowID proves the only request-supplied value
// (the container id) is strictly numeric — it can never reach the markup as text.
func TestComfyExtensionRejectsBadWorkflowID(t *testing.T) {
	srv := openComfyServer(t, &fakeComfy{})
	srv.cfg.ComfyRoot = fakeComfyRoot(t)
	for _, bad := range []string{"", "abc", `7"><script>`, "-1", "1.5"} {
		rec := postForm(srv, "/comfy/extension/install", extForm(bad), true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("workflow_id %q → status %d, want 400", bad, rec.Code)
		}
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
	if !strings.Contains(body, "Remove helper") {
		t.Errorf("the user must always be able to back the install out:\n%s", body)
	}
}
