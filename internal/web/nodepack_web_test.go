package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeManager is an injectable managerClient for the node-pack tests. Every
// response is programmable, and the install/reboot calls are counted so a test
// can prove a gated path performed NO side effect.
type fakeManager struct {
	mu sync.Mutex

	info     *comfy.ManagerInfo
	probeErr error

	mappings json.RawMessage
	getlist  json.RawMessage

	installErr   error
	installCalls int
	installedTo  []comfy.Pack

	status    *comfy.ManagerQueueStatus
	statusErr error

	// diffs is consumed one entry per ManagerInstalledDiff call; the last entry
	// repeats once exhausted. That models "baseline, then after the install".
	diffs   [][]string
	diffIdx int
	diffErr error

	rebootErr   error
	rebootCalls int
}

func (f *fakeManager) ManagerProbe(context.Context) (*comfy.ManagerInfo, error) {
	return f.info, f.probeErr
}
func (f *fakeManager) ManagerMappings(context.Context) (json.RawMessage, error) {
	return f.mappings, nil
}
func (f *fakeManager) ManagerNodePacks(context.Context) (json.RawMessage, error) {
	return f.getlist, nil
}
func (f *fakeManager) ManagerInstall(_ context.Context, _ *comfy.ManagerInfo, p comfy.Pack) error {
	f.mu.Lock()
	f.installCalls++
	f.installedTo = append(f.installedTo, p)
	f.mu.Unlock()
	return f.installErr
}
func (f *fakeManager) ManagerQueueStatus(context.Context, *comfy.ManagerInfo) (*comfy.ManagerQueueStatus, error) {
	return f.status, f.statusErr
}
func (f *fakeManager) ManagerInstalledDiff(context.Context) ([]string, error) {
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.diffs) == 0 {
		return nil, nil
	}
	out := f.diffs[f.diffIdx]
	if f.diffIdx < len(f.diffs)-1 {
		f.diffIdx++
	}
	return out, nil
}
func (f *fakeManager) ManagerReboot(context.Context, *comfy.ManagerInfo) error {
	f.mu.Lock()
	f.rebootCalls++
	f.mu.Unlock()
	return f.rebootErr
}

func (f *fakeManager) calls() (install, reboot int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installCalls, f.rebootCalls
}

// managerPresent is the "V3.41 is answering and serves both indexes" info.
func managerPresent() *comfy.ManagerInfo {
	return &comfy.ManagerInfo{
		Present: true, Line: comfy.ManagerLineV3, Version: "V3.41",
		CanInstall: true, HasNodePackList: true, HasMappings: true,
	}
}

// installablePack / blockedPack mirror the two real-world shapes: a CNR-released
// pack Manager can install, and a nightly-only pack its default policy refuses
// (16% of live packs — the common blocked case, not an edge).
func installablePack() comfy.Pack {
	return comfy.Pack{
		ID: "comfy-mtb", Title: "comfy-mtb",
		Repository: "https://github.com/melmass/comfy_mtb",
		Version:    "0.4.1", Installable: true,
		Classes: []string{"Pick From Batch (mtb)"}, Source: comfy.SourceMap,
	}
}

func blockedPack() comfy.Pack {
	return comfy.Pack{
		ID: "ComfyUI_Comfyroll_CustomNodes", Title: "ComfyUI_Comfyroll_CustomNodes",
		Repository: "https://github.com/Suzie1/ComfyUI_Comfyroll_CustomNodes",
		Version:    "nightly", Installable: false,
		Reason:  "This pack ships only a nightly (git) build, not a Comfy Registry release.",
		Classes: []string{"CR Float To Integer"}, Source: comfy.SourceMap,
	}
}

// seedNodeAttrRun drives a run to a terminal failed-preflight state carrying attr,
// so the missing-nodes panel and the install endpoint both see a real snapshot.
func seedNodeAttrRun(t *testing.T, srv *Server, attr nodeAttribution, missing []string) string {
	t.Helper()
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{
			Preflight: &comfy.PreflightReport{MissingNodes: missing},
			NodeAttr:  attr,
		}, nil
	}
	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)
	return id
}

// pollNodepackUntilDone polls the install-status endpoint until the fragment
// carries no poller (terminal).
func pollNodepackUntilDone(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec := get(t, srv, "/workflows/nodepacks/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("nodepack status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `id="nodepack-poll"`) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("install did not settle; last body:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestMissingNodesPanelStates covers every render state the panel must treat as
// first-class. All four occur routinely in reality — none is an edge case.
func TestMissingNodesPanelStates(t *testing.T) {
	patternPack := comfy.Pack{
		ID: "comfy-mtb", Title: "comfy-mtb",
		Repository: "https://github.com/melmass/comfy_mtb",
		Installable: true, Classes: []string{"Note Plus (mtb)"}, Source: comfy.SourcePattern,
	}

	cases := []struct {
		name    string
		attr    nodeAttribution
		missing []string
		want    []string
		absent  []string
	}{
		{
			name: "installable — a gated Install button",
			attr: nodeAttribution{
				ManagerPresent: true, Packs: []comfy.Pack{installablePack()},
			},
			missing: []string{"Pick From Batch (mtb)"},
			want: []string{
				"Missing custom nodes",
				"Install comfy-mtb",
				`hx-post="/workflows/9/nodepacks/install"`,
				`hx-target="#nodepack-status"`,
				"https://github.com/melmass/comfy_mtb",
				"Pick From Batch (mtb)",
				// The manual command is shown even when the pack IS installable.
				"git clone",
			},
		},
		{
			name: "blocked — the reason plus the manual command, no button",
			attr: nodeAttribution{
				ManagerPresent: true, Packs: []comfy.Pack{blockedPack()},
			},
			missing: []string{"CR Float To Integer"},
			want: []string{
				"nightly (git) build",
				"git clone &#39;https://github.com/Suzie1/ComfyUI_Comfyroll_CustomNodes&#39;",
			},
			absent: []string{"Install ComfyUI_Comfyroll_CustomNodes"},
		},
		{
			name: "unattributed — its own section with a search link",
			attr: nodeAttribution{
				ManagerPresent: true,
				Unattributed:   []string{"MMAudioSampler", "Note Plus (mtb)"},
			},
			missing: []string{"MMAudioSampler", "Note Plus (mtb)"},
			want: []string{
				"Not matched to a pack",
				"MMAudioSampler",
				"https://github.com/search?",
				"Search GitHub",
			},
		},
		{
			name: "manager absent — attribution renders, NO install affordance",
			attr: nodeAttribution{
				ManagerPresent: false,
				ManagerNote:    "ComfyUI-Manager's web API did not answer.",
				Packs:          []comfy.Pack{installablePack()},
			},
			missing: []string{"Pick From Batch (mtb)"},
			want: []string{
				"ComfyUI-Manager&#39;s web API did not answer.",
				"https://github.com/melmass/comfy_mtb",
				"git clone",
			},
			absent: []string{
				"/nodepacks/install",
				"/nodepacks/restart",
				"Install comfy-mtb",
			},
		},
		{
			name: "pattern rung reads as LIKELY, never as certain",
			attr: nodeAttribution{
				ManagerPresent: true, Packs: []comfy.Pack{patternPack},
			},
			missing: []string{"Note Plus (mtb)"},
			want: []string{
				"Likely provided by",
				"not a certainty",
			},
		},
		{
			name:    "no attribution at all degrades to naming every class",
			attr:    nodeAttribution{ManagerPresent: true},
			missing: []string{"SomeUnknownNode"},
			want:    []string{"Not matched to a pack", "SomeUnknownNode"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := renderString(t, missingNodesPanel(c.attr, c.missing, 9, "TOKEN", "/opt/ComfyUI"))
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q in:\n%s", w, body)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(body, a) {
					t.Errorf("must NOT contain %q in:\n%s", a, body)
				}
			}
		})
	}
}

// TestMissingNodesPanelEscapesUntrustedStrings proves pack titles, repository
// URLs and class names — all third-party index data — are escaped, and that a
// non-http(s) repository never becomes an href.
func TestMissingNodesPanelEscapesUntrustedStrings(t *testing.T) {
	attr := nodeAttribution{
		ManagerPresent: true,
		Packs: []comfy.Pack{{
			ID:          `<script>alert(1)</script>`,
			Title:       `Evil "><img src=x onerror=alert(1)> Pack`,
			Repository:  `javascript:alert(document.domain)`,
			Installable: true,
			Classes:     []string{`<b>ClassOne</b>`, `"onmouseover="alert(2)`},
			Source:      comfy.SourceMap,
		}},
		Unattributed: []string{`</ul><script>alert(3)</script>`},
	}
	body := renderString(t, missingNodesPanel(attr, nil, 9, "TOKEN", ""))

	for _, raw := range []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<b>ClassOne</b>",
		"<script>alert(3)</script>",
	} {
		if strings.Contains(body, raw) {
			t.Errorf("unescaped %q leaked into the panel:\n%s", raw, body)
		}
	}
	// The escaped forms MUST be present — the strings are SHOWN, just safely.
	for _, esc := range []string{
		"Evil &#34;&gt;&lt;img src=x onerror=alert(1)&gt; Pack", // title, as text
		"&lt;b&gt;ClassOne&lt;/b&gt;",                           // class name, as text
		"&lt;script&gt;alert(3)&lt;/script&gt;",                 // unattributed class, as text
		// The pack id rides in the hx-vals JSON, where json.Marshal unicode-escapes
		// the angle brackets before the attribute escaping runs.
		`\u003cscript\u003ealert(1)\u003c/script\u003e`,
	} {
		if !strings.Contains(body, esc) {
			t.Errorf("expected the escaped form %q in:\n%s", esc, body)
		}
	}
	// A javascript: repository must never become an href, and must never be
	// interpolated into a shell command.
	if strings.Contains(body, `href="javascript:`) {
		t.Errorf("javascript: URL became an href:\n%s", body)
	}
	if strings.Contains(body, "git clone") {
		t.Errorf("an unsafe repository must not yield a manual command:\n%s", body)
	}
}

// TestManualInstallCommandRefusesShellUnsafeValues pins the injection guard: the
// command is pasted into a shell, so a repository URL carrying a quote or a
// space must drop the command entirely rather than be escaped into it.
func TestManualInstallCommandRefusesShellUnsafeValues(t *testing.T) {
	cases := []struct {
		name, repo, root, want string
		ok                     bool
	}{
		{
			name: "plain https repo with a configured root", ok: true,
			repo: "https://github.com/melmass/comfy_mtb", root: "/opt/ComfyUI",
			want: "cd '/opt/ComfyUI/custom_nodes' && git clone 'https://github.com/melmass/comfy_mtb'",
		},
		{
			name: "no configured root falls back to a placeholder", ok: true,
			repo: "https://github.com/a/b", root: "",
			want: "cd '" + comfyRootPlaceholder + "/custom_nodes' && git clone 'https://github.com/a/b'",
		},
		{
			name: "quote-injecting repo is refused outright", ok: false,
			repo: `https://github.com/a/b'; rm -rf ~ ;'`, root: "/opt/ComfyUI",
		},
		{
			name: "backtick repo is refused outright", ok: false,
			repo: "https://github.com/a/`whoami`", root: "/opt/ComfyUI",
		},
		{
			name: "non-http scheme is refused outright", ok: false,
			repo: "javascript:alert(1)", root: "/opt/ComfyUI",
		},
		{
			name: "unsafe comfy_root degrades to the placeholder, not to a bad command", ok: true,
			repo: "https://github.com/a/b", root: `/opt/'; rm -rf ~ ;'`,
			want: "cd '" + comfyRootPlaceholder + "/custom_nodes' && git clone 'https://github.com/a/b'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := manualInstallCommand(comfy.Pack{Repository: c.repo}, c.root)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, c.ok, got)
			}
			if c.ok && got != c.want {
				t.Errorf("command = %q, want %q", got, c.want)
			}
		})
	}
}

// TestNodepackEndpointsRequireCSRF proves both state-changing endpoints reject a
// request without a valid token, and perform NO Manager call.
func TestNodepackEndpointsRequireCSRF(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fm := &fakeManager{info: managerPresent(), diffs: [][]string{nil}}
	srv.managerClientFn = func() managerClient { return fm }
	id := seedNodeAttrRun(t, srv,
		nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{installablePack()}},
		[]string{"Pick From Batch (mtb)"})

	for _, c := range []struct{ name, path string }{
		{"install", "/workflows/" + id + "/nodepacks/install"},
		{"restart", "/workflows/nodepacks/restart"},
	} {
		rec := post(t, srv, c.path, installVals(), false)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without CSRF = %d, want 403", c.name, rec.Code)
		}
	}
	if install, reboot := fm.calls(); install != 0 || reboot != 0 {
		t.Errorf("a CSRF-rejected request must not reach Manager (install=%d reboot=%d)", install, reboot)
	}
}

// TestNodepackEndpointsLoopbackGated proves the endpoints are unavailable on a
// non-loopback bind even WITH a valid CSRF token (CSRF is not an auth boundary),
// and that they perform no Manager call.
func TestNodepackEndpointsLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	fm := &fakeManager{info: managerPresent()}
	srv.managerClientFn = func() managerClient { return fm }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	posts := []struct{ name, path string }{
		{"install", "/workflows/" + id + "/nodepacks/install"},
		{"restart", "/workflows/nodepacks/restart"},
	}
	for _, c := range posts {
		rec := post(t, srv, c.path, installVals(), true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), gateMsg) {
			t.Errorf("%s gated = %d, body:\n%s", c.name, rec.Code, rec.Body.String())
		}
	}
	if rec := get(t, srv, "/workflows/nodepacks/status"); !strings.Contains(rec.Body.String(), gateMsg) {
		t.Errorf("status must be gated too, got:\n%s", rec.Body.String())
	}
	if install, reboot := fm.calls(); install != 0 || reboot != 0 {
		t.Errorf("a gated request must not reach Manager (install=%d reboot=%d)", install, reboot)
	}
}

// TestNodepackInstallRequiresConfirmation proves the first click installs NOTHING
// and instead renders a confirmation naming the pack and its repository URL.
func TestNodepackInstallRequiresConfirmation(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fm := &fakeManager{info: managerPresent()}
	srv.managerClientFn = func() managerClient { return fm }
	id := seedNodeAttrRun(t, srv,
		nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{installablePack()}},
		[]string{"Pick From Batch (mtb)"})

	rec := post(t, srv, "/workflows/"+id+"/nodepacks/install", installVals(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("first click = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Install comfy-mtb?", "https://github.com/melmass/comfy_mtb", "Yes, install it", `&#34;confirm&#34;:&#34;1&#34;`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation missing %q:\n%s", want, body)
		}
	}
	if install, _ := fm.calls(); install != 0 {
		t.Fatalf("the first click must install nothing, got %d install call(s)", install)
	}
}

// TestNodepackInstallRefusesUnattributedPack proves the server never takes a
// repository URL from the request: a forged (id, repo) pair that is not in the
// current run's attribution is refused, and Manager is never called.
func TestNodepackInstallRefusesUnattributedPack(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fm := &fakeManager{info: managerPresent()}
	srv.managerClientFn = func() managerClient { return fm }
	id := seedNodeAttrRun(t, srv,
		nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{installablePack()}},
		[]string{"Pick From Batch (mtb)"})

	forged := url.Values{
		"pack_id":   {"evil"},
		"pack_repo": {"https://attacker.example/evil"},
		"confirm":   {"1"},
	}
	rec := post(t, srv, "/workflows/"+id+"/nodepacks/install", forged, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("forged install = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "out of date") {
		t.Errorf("expected a refusal, got:\n%s", rec.Body.String())
	}
	if install, _ := fm.calls(); install != 0 {
		t.Fatalf("a forged pack must never reach Manager, got %d install call(s)", install)
	}
}

// TestNodepackInstallRefusesBlockedPack proves a nightly-only pack is refused with
// its reason rather than sent to Manager (where it would always fail).
func TestNodepackInstallRefusesBlockedPack(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fm := &fakeManager{info: managerPresent()}
	srv.managerClientFn = func() managerClient { return fm }
	bp := blockedPack()
	id := seedNodeAttrRun(t, srv,
		nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{bp}},
		[]string{"CR Float To Integer"})

	vals := url.Values{"pack_id": {bp.ID}, "pack_repo": {bp.Repository}, "confirm": {"1"}}
	rec := post(t, srv, "/workflows/"+id+"/nodepacks/install", vals, true)
	if !strings.Contains(rec.Body.String(), "nightly (git) build") {
		t.Errorf("expected the pack's own reason, got:\n%s", rec.Body.String())
	}
	if install, _ := fm.calls(); install != 0 {
		t.Fatalf("a blocked pack must never reach Manager, got %d install call(s)", install)
	}
}

// TestNodepackInstallOutcomes is the honesty gate on the install job: success is
// claimed ONLY from ComfyUI-Manager's own installed-set diff, and every other
// outcome says so plainly and falls back to the manual command.
func TestNodepackInstallOutcomes(t *testing.T) {
	idle := &comfy.ManagerQueueStatus{TotalCount: 1, DoneCount: 1, IsProcessing: false}

	cases := []struct {
		name   string
		fm     *fakeManager
		want   []string
		absent []string
	}{
		{
			name: "diff confirms the pack — success, and a restart is required",
			fm: &fakeManager{
				info: managerPresent(), status: idle,
				diffs: [][]string{{}, {"comfy-mtb"}},
			},
			want: []string{
				"comfy-mtb installed",
				"ComfyUI must restart before this workflow can run",
				"/workflows/nodepacks/restart",
			},
		},
		{
			name: "diff does NOT show the pack — say so, do not claim success",
			fm: &fakeManager{
				info: managerPresent(), status: idle,
				diffs: [][]string{{}, {"some-other-pack"}},
			},
			want: []string{
				"does not show up as installed",
				"Nothing here can confirm it landed",
				"Install it by hand instead",
				"git clone",
			},
			absent: []string{"ComfyUI must restart before this workflow can run"},
		},
		{
			name: "Manager refuses with a 403 — explain the policy, offer the command",
			fm: &fakeManager{
				info: managerPresent(), status: idle, diffs: [][]string{{}},
				installErr: errors.New("manager install: unexpected status 403: forbidden"),
			},
			want: []string{
				"ComfyUI-Manager refused the install",
				"security policy",
				"git clone",
			},
			absent: []string{"installed —"},
		},
		{
			name: "already installed and pending a restart is reported as such",
			fm: &fakeManager{
				info: managerPresent(), status: idle,
				diffs: [][]string{{"comfy-mtb"}},
			},
			want: []string{"already installed and waiting for a restart"},
		},
		{
			name: "Manager is not answering — nothing was installed",
			fm: &fakeManager{
				info: &comfy.ManagerInfo{Present: false}, status: idle,
			},
			want:   []string{"did not answer, so nothing was installed"},
			absent: []string{"installed —"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			srv.cfg.ComfyRoot = "/opt/ComfyUI"
			srv.managerClientFn = func() managerClient { return c.fm }
			// Shorten the poll cadence and the never-became-busy grace period so the
			// honest "we could not confirm" path settles fast in a test.
			srv.nodepackPoll, srv.nodepackSettleWait = time.Millisecond, 10*time.Millisecond
			id := seedNodeAttrRun(t, srv,
				nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{installablePack()}, ComfyRoot: "/opt/ComfyUI"},
				[]string{"Pick From Batch (mtb)"})

			vals := installVals()
			vals.Set("confirm", "1")
			if rec := post(t, srv, "/workflows/"+id+"/nodepacks/install", vals, true); rec.Code != http.StatusOK {
				t.Fatalf("install = %d", rec.Code)
			}
			body := pollNodepackUntilDone(t, srv)
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("terminal body missing %q:\n%s", w, body)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(body, a) {
					t.Errorf("terminal body must NOT contain %q:\n%s", a, body)
				}
			}
		})
	}
}

// TestNodepackRestartOutcomes proves a busy-queue refusal surfaces as its OWN
// plain message (never a generic error), and that the other outcomes are honest.
func TestNodepackRestartOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		fm         *fakeManager
		want       string
		wantReboot int
	}{
		{
			name:       "queue busy — a plain, specific refusal",
			fm:         &fakeManager{info: managerPresent(), rebootErr: comfy.ErrQueueBusy},
			want:       "a generation is running or queued",
			wantReboot: 1,
		},
		{
			name:       "restart accepted",
			fm:         &fakeManager{info: managerPresent()},
			want:       "Restart requested",
			wantReboot: 1,
		},
		{
			name:       "Manager refused for another reason — say what it said",
			fm:         &fakeManager{info: managerPresent(), rebootErr: errors.New("manager reboot: unexpected status 403")},
			want:       "refused the restart",
			wantReboot: 1,
		},
		{
			name:       "Manager absent — never attempt a reboot",
			fm:         &fakeManager{info: &comfy.ManagerInfo{Present: false}},
			want:       "did not answer",
			wantReboot: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			srv.managerClientFn = func() managerClient { return c.fm }
			rec := post(t, srv, "/workflows/nodepacks/restart", url.Values{}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("restart = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("expected %q, got:\n%s", c.want, rec.Body.String())
			}
			if _, reboot := c.fm.calls(); reboot != c.wantReboot {
				t.Errorf("reboot calls = %d, want %d", reboot, c.wantReboot)
			}
		})
	}
}

// TestNodepackStatusPollerTargetsStableContainer pins the streaming invariant: the
// in-flight fragment's poller swaps the STABLE #nodepack-status container and
// never replaces itself, and the terminal fragment carries no poller at all.
func TestNodepackStatusPollerTargetsStableContainer(t *testing.T) {
	running := renderString(t, nodepackStatusFragment(nodepackSnapshot{
		Started: true, Running: true, Title: "comfy-mtb", Message: "Installing…",
		Lines: []string{"Queued with ComfyUI-Manager."},
	}, "TOKEN"))
	for _, want := range []string{
		`id="nodepack-poll"`,
		`hx-get="/workflows/nodepacks/status"`,
		`hx-target="#nodepack-status"`,
		`hx-swap="innerHTML"`,
	} {
		if !strings.Contains(running, want) {
			t.Errorf("running fragment missing %q:\n%s", want, running)
		}
	}
	terminal := renderString(t, nodepackStatusFragment(nodepackSnapshot{
		Started: true, Title: "comfy-mtb", Installed: true, Message: "comfy-mtb installed",
	}, "TOKEN"))
	if strings.Contains(terminal, `id="nodepack-poll"`) {
		t.Errorf("terminal fragment must carry no poller:\n%s", terminal)
	}
}

// TestMatchPackInDiff covers the two names a Manager install can land under: the
// pack id, and the repository's last path segment (the git clone directory).
func TestMatchPackInDiff(t *testing.T) {
	cases := []struct {
		name  string
		pack  comfy.Pack
		names []string
		want  bool
	}{
		{"by pack id", comfy.Pack{ID: "comfy-mtb"}, []string{"comfy-mtb"}, true},
		{"case-insensitively", comfy.Pack{ID: "Comfy-MTB"}, []string{"comfy-mtb"}, true},
		{"by repo last segment", comfy.Pack{Repository: "https://github.com/GACLove/ComfyUI-VFI"}, []string{"ComfyUI-VFI"}, true},
		{"repo .git suffix stripped", comfy.Pack{Repository: "https://github.com/a/b.git"}, []string{"b"}, true},
		{"unrelated entry is not a match", comfy.Pack{ID: "comfy-mtb"}, []string{"other"}, false},
		{"empty diff is not a match", comfy.Pack{ID: "comfy-mtb"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := matchPackInDiff(c.pack, c.names); ok != c.want {
				t.Errorf("matchPackInDiff = %v, want %v", ok, c.want)
			}
		})
	}
}

// installVals is the hx-vals payload the Install button issues for the
// installable fixture pack.
func installVals() url.Values {
	p := installablePack()
	return url.Values{"pack_id": {p.ID}, "pack_repo": {p.Repository}}
}

