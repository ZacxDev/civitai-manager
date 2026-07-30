package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// recordingOpener is the exec seam. It NEVER launches anything — it records the
// argv it was handed, which is exactly what the security assertions need. No test
// in this file spawns a file manager.
type recordingOpener struct {
	mu    sync.Mutex
	argvs [][]string
	err   error
}

func (o *recordingOpener) fn() func([]string) error {
	return func(argv []string) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.argvs = append(o.argvs, append([]string(nil), argv...))
		return o.err
	}
}

func (o *recordingOpener) calls() [][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([][]string(nil), o.argvs...)
}

// newRevealServer builds a loopback-bound server whose ONLY configured library
// root is root, with the exec seam recording instead of launching.
func newRevealServer(t *testing.T, root string) (*Server, *recordingOpener) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "127.0.0.1:8972", ModelRoot: root,
	}, nil)
	op := &recordingOpener{}
	srv.openerFn = op.fn()
	return srv, op
}

// seedFile writes a real file and indexes it, returning its local_files rowid.
// The file must really exist: containment resolves symlinks, and an unresolvable
// path is refused by design.
func seedFile(t *testing.T, srv *Server, path string) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.UpsertLocalFile(store.LocalFile{Path: path, SHA256: "aa", SizeBytes: 7}); err != nil {
		t.Fatal(err)
	}
	lf, err := srv.store.GetLocalFileByPath(path)
	if err != nil || lf == nil {
		t.Fatalf("seed lookup %s: %v", path, err)
	}
	return lf.ID
}

// wantOpener is the allowlisted opener for the platform the test runs on.
func wantOpener(t *testing.T) string {
	t.Helper()
	op, ok := fileManagerOpener()
	if !ok {
		t.Skipf("no allowlisted opener on %s", runtime.GOOS)
	}
	return op
}

// TestRevealOpensAContainedDirectory: the happy path, and the exact argv.
func TestRevealOpensAContainedDirectory(t *testing.T) {
	root := t.TempDir()
	srv, op := newRevealServer(t, root)
	id := seedFile(t, srv, filepath.Join(root, "loras", "a.safetensors"))

	rec := post(t, srv, "/library/files/"+strconv.FormatInt(id, 10)+"/reveal", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d, body=%s", rec.Code, rec.Body.String())
	}

	calls := op.calls()
	if len(calls) != 1 {
		t.Fatalf("opener called %d times, want 1", len(calls))
	}
	// The argv is EXACTLY the allowlisted opener plus the one resolved directory.
	// No flags, no shell, no extra token.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{wantOpener(t), filepath.Join(realRoot, "loras")}
	if len(calls[0]) != len(want) || calls[0][0] != want[0] || calls[0][1] != want[1] {
		t.Fatalf("argv = %#v, want %#v", calls[0], want)
	}
	// The response says WHERE the window opened — the server's machine.
	body := rec.Body.String()
	if !strings.Contains(body, "computer running civitai-manager") {
		t.Errorf("the outcome must name the machine the window opened on:\n%s", body)
	}
}

// TestRevealRefusesUncontainedPaths is the containment surface. Every case here
// must refuse WITHOUT executing anything.
func TestRevealRefusesUncontainedPaths(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the path to index, given the configured root and a
		// directory that is NOT configured as a root.
		setup func(t *testing.T, root, outside string) string
	}{
		{
			name: "a path in a directory that is not a configured root",
			setup: func(t *testing.T, _, outside string) string {
				p := filepath.Join(outside, "secret", "a.safetensors")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			name: "a SYMLINK inside the root pointing OUTSIDE it (resolve, then check)",
			setup: func(t *testing.T, root, outside string) string {
				target := filepath.Join(outside, "elsewhere")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "escape")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				// The path's DIRECTORY component is inside the root by NAME
				// (<root>/escape) and outside it after resolution. A check-then-resolve
				// implementation passes this; ours must not.
				return filepath.Join(link, "a.safetensors")
			},
		},
		{
			name: "a traversal out of the root",
			setup: func(t *testing.T, root, outside string) string {
				sib := filepath.Join(outside, "sibling")
				if err := os.MkdirAll(sib, 0o755); err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(root, sib)
				if err != nil {
					t.Fatal(err)
				}
				return filepath.Join(root, rel, "a.safetensors")
			},
		},
		{
			name: "a directory that does not exist cannot be proven contained",
			setup: func(t *testing.T, root, _ string) string {
				return filepath.Join(root, "no-such-dir", "a.safetensors")
			},
		},
		{
			name:  "a relative path is never accepted",
			setup: func(t *testing.T, _, _ string) string { return "loras/a.safetensors" },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, d := range []string{root, outside} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			srv, op := newRevealServer(t, root)

			path := tc.setup(t, root, outside)
			// Index the path directly (no file needs to exist for a refusal test).
			if err := srv.store.UpsertLocalFile(store.LocalFile{Path: path, SHA256: "aa", SizeBytes: 1}); err != nil {
				t.Fatal(err)
			}
			lf, err := srv.store.GetLocalFileByPath(path)
			if err != nil || lf == nil {
				t.Fatalf("index %s: %v", path, err)
			}

			rec := post(t, srv, "/library/files/"+strconv.FormatInt(lf.ID, 10)+"/reveal", url.Values{}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("reveal = %d", rec.Code)
			}
			if n := len(op.calls()); n != 0 {
				t.Fatalf("an uncontained path executed the opener %d times:\n%#v", n, op.calls())
			}
			if !strings.Contains(rec.Body.String(), "not inside one of your configured library folders") {
				t.Errorf("expected the containment refusal, got:\n%s", rec.Body.String())
			}
		})
	}
}

// TestRevealIgnoresARequestSuppliedPath: the endpoint takes an INTEGER id, so a
// forged POST cannot name a directory. Extra form fields are inert.
func TestRevealIgnoresARequestSuppliedPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	evil := filepath.Join(base, "evil")
	for _, d := range []string{root, evil} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv, op := newRevealServer(t, root)
	id := seedFile(t, srv, filepath.Join(root, "loras", "a.safetensors"))

	rec := post(t, srv, "/library/files/"+strconv.FormatInt(id, 10)+"/reveal", url.Values{
		"path": {evil}, "dir": {evil}, "file": {filepath.Join(evil, "x")},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d", rec.Code)
	}
	calls := op.calls()
	if len(calls) != 1 {
		t.Fatalf("opener called %d times, want 1", len(calls))
	}
	for _, arg := range calls[0] {
		if strings.Contains(arg, evil) {
			t.Fatalf("a request-supplied path reached the argv: %#v", calls[0])
		}
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	if calls[0][1] != filepath.Join(realRoot, "loras") {
		t.Fatalf("argv dir = %q, want the server-derived %q", calls[0][1], filepath.Join(realRoot, "loras"))
	}
}

// TestRevealRejectsForgedAndRemoteRequests: CSRF and the loopback gate, each
// proven to stop the exec.
func TestRevealRejectsForgedAndRemoteRequests(t *testing.T) {
	tests := []struct {
		name string
		// addr overrides the server bind (non-loopback disables the capability).
		addr     string
		csrf     string // "" = omit the token entirely
		withCSRF bool
		wantCode int
		wantBody string
	}{
		{name: "no CSRF token", wantCode: http.StatusForbidden},
		{name: "wrong CSRF token", csrf: "not-the-token", wantCode: http.StatusForbidden},
		{
			name: "non-loopback bind disables the capability", withCSRF: true,
			addr: "0.0.0.0:8972", wantCode: http.StatusOK,
			wantBody: "disabled when the server is bound to a non-loopback address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			srv, op := newRevealServer(t, root)
			if tc.addr != "" {
				srv.cfg.Addr = tc.addr
			}
			id := seedFile(t, srv, filepath.Join(root, "loras", "a.safetensors"))
			path := "/library/files/" + strconv.FormatInt(id, 10) + "/reveal"

			form := url.Values{}
			if tc.csrf != "" {
				form.Set("csrf_token", tc.csrf)
			}
			rec := post(t, srv, path, form, tc.withCSRF)
			if rec.Code != tc.wantCode {
				t.Fatalf("reveal = %d, want %d (body=%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("expected %q in:\n%s", tc.wantBody, rec.Body.String())
			}
			if n := len(op.calls()); n != 0 {
				t.Fatalf("a rejected request still executed the opener %d times", n)
			}
		})
	}
}

// TestRevealUnknownFileID: an id that names no indexed file yields no path and
// therefore no exec.
func TestRevealUnknownFileID(t *testing.T) {
	root := t.TempDir()
	srv, op := newRevealServer(t, root)

	for _, id := range []string{"999999", "0", "-1", "abc"} {
		rec := post(t, srv, "/library/files/"+id+"/reveal", url.Values{}, true)
		if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
			t.Fatalf("reveal id=%s = %d", id, rec.Code)
		}
		if n := len(op.calls()); n != 0 {
			t.Fatalf("id=%s executed the opener", id)
		}
	}
}

// TestFileManagerArgvIsAllowlistedAndShellFree pins the argv construction and
// proves no shell is involved: the *exec.Cmd is built (never started) and its
// Args are asserted to be exactly the opener + the directory.
func TestFileManagerArgvIsAllowlistedAndShellFree(t *testing.T) {
	opener := wantOpener(t)
	allowed := map[string]bool{"xdg-open": true, "open": true, "explorer": true}
	if !allowed[opener] {
		t.Fatalf("opener %q is not in the fixed allowlist", opener)
	}

	// A directory name full of shell metacharacters. With an argv there is nothing
	// to parse, so every one of these must survive as a single, literal argument.
	dir := `/lib/a b;rm -rf /$(id)|nc evil 1 &&echo 'x'`
	argv, ok := fileManagerArgv(dir)
	if !ok {
		t.Fatal("fileManagerArgv refused a non-empty dir")
	}
	if len(argv) != 2 || argv[0] != opener || argv[1] != dir {
		t.Fatalf("argv = %#v, want [%q %q]", argv, opener, dir)
	}

	cmd := openerCommand(context.Background(), argv)
	if len(cmd.Args) != 2 || cmd.Args[0] != opener || cmd.Args[1] != dir {
		t.Fatalf("cmd.Args = %#v, want [%q %q]", cmd.Args, opener, dir)
	}
	// No shell anywhere: not as the program, not as an argument.
	for _, a := range cmd.Args {
		if a == "-c" {
			t.Fatalf("a -c flag reached the argv: %#v", cmd.Args)
		}
	}
	for _, shell := range []string{"/bin/sh", "/bin/bash", "sh", "bash", "cmd.exe", "powershell"} {
		if cmd.Args[0] == shell || filepath.Base(cmd.Path) == shell {
			t.Fatalf("the opener resolved to a shell (%q / %q)", cmd.Args[0], cmd.Path)
		}
	}
	// The program is the allowlisted opener itself (resolved through PATH when
	// present, left as the bare name when not) — never a shell wrapper.
	if base := filepath.Base(cmd.Path); base != opener {
		t.Fatalf("cmd.Path = %q, want a path whose base is %q", cmd.Path, opener)
	}

	if _, ok := fileManagerArgv(""); ok {
		t.Fatal("fileManagerArgv must refuse an empty dir")
	}
}

// TestRevealRootsExcludeBlankAndRelative: an empty configured root would make the
// containment check pass for every path on the filesystem.
func TestRevealRootsExcludeBlankAndRelative(t *testing.T) {
	root := t.TempDir()
	srv, _ := newRevealServer(t, root)
	srv.cfg.ComfyModelPath = "   "
	srv.cfg.LibraryPaths = []string{"", "relative/path", root, "/abs/extra"}

	got := srv.revealRoots()
	for _, r := range got {
		if strings.TrimSpace(r) == "" || !filepath.IsAbs(r) {
			t.Fatalf("revealRoots returned a blank/relative root %q: %#v", r, got)
		}
	}
	// De-duplicated: root is configured twice (ModelRoot + LibraryPaths).
	seen := map[string]int{}
	for _, r := range got {
		seen[r]++
	}
	if seen[root] != 1 {
		t.Fatalf("root %q appears %d times: %#v", root, seen[root], got)
	}
	if seen["/abs/extra"] != 1 {
		t.Fatalf("an absolute extra library path should be a root: %#v", got)
	}
}

// TestRevealContainmentUnit is the direct, table-driven pin on containedDir —
// the same cases as the HTTP tests plus the sibling-directory case a naive
// string-prefix check gets wrong.
func TestRevealContainmentUnit(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	sibling := base + "/rootlike" // shares the root's string prefix, is NOT inside it
	inner := filepath.Join(root, "loras")
	dotdot := filepath.Join(root, "..foo")
	for _, d := range []string{root, sibling, inner, dotdot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{root}

	tests := []struct {
		name    string
		path    string
		wantOK  bool
		wantDir string
	}{
		{"file directly in the root", filepath.Join(root, "a.pt"), true, realRoot},
		{"file in a subdirectory", filepath.Join(inner, "a.pt"), true, filepath.Join(realRoot, "loras")},
		{"a sibling sharing the root's string prefix is NOT contained", filepath.Join(sibling, "a.pt"), false, ""},
		{"a subdirectory literally named ..foo IS contained", filepath.Join(dotdot, "a.pt"), true, filepath.Join(realRoot, "..foo")},
		{"an empty path", "", false, ""},
		{"a relative path", "loras/a.pt", false, ""},
		{"a nonexistent directory", filepath.Join(root, "nope", "a.pt"), false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, ok := containedDir(tc.path, roots)
			if ok != tc.wantOK {
				t.Fatalf("containedDir(%q) ok = %v, want %v (dir=%q)", tc.path, ok, tc.wantOK, dir)
			}
			if ok && dir != tc.wantDir {
				t.Fatalf("containedDir(%q) = %q, want %q", tc.path, dir, tc.wantDir)
			}
		})
	}

	// No configured roots at all: nothing is contained.
	if _, ok := containedDir(filepath.Join(inner, "a.pt"), nil); ok {
		t.Fatal("with no configured roots, nothing may be contained")
	}
	// A blank root must not behave as "/".
	if _, ok := containedDir(filepath.Join(inner, "a.pt"), []string{""}); ok {
		t.Fatal("a blank root must not contain everything")
	}
}

// TestRevealSymlinkEscapeIsNotVacuous proves the symlink case actually TESTS
// something: it first demonstrates that the naive check-then-resolve
// implementation (string containment on the UNRESOLVED path) would ACCEPT the
// escape, and only then asserts that containedDir rejects it. Without this, the
// symlink test could pass for the wrong reason and nobody would notice.
func TestRevealSymlinkEscapeIsNotVacuous(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside", "elsewhere")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(link, "a.safetensors")

	// (1) The naive check-then-resolve: the UNRESOLVED directory is inside the
	// root by string, so a check-then-resolve implementation would allow it.
	naiveDir := filepath.Dir(path)
	rel, err := filepath.Rel(root, naiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(rel, "..") {
		t.Fatalf("the fixture is wrong: %q is not inside %q even before resolution", naiveDir, root)
	}

	// (2) After resolution it is demonstrably OUTSIDE the root.
	resolved, err := filepath.EvalSymlinks(naiveDir)
	if err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if relResolved, err := filepath.Rel(realRoot, resolved); err != nil || !strings.HasPrefix(relResolved, "..") {
		t.Fatalf("the fixture is wrong: resolved %q is still inside %q", resolved, realRoot)
	}

	// (3) So resolve-then-check must refuse it.
	if dir, ok := containedDir(path, []string{root}); ok {
		t.Fatalf("a symlink escaping the root was accepted (dir=%q)", dir)
	}
}

// TestWorkflowResolverOffersRevealOnlyForContainedFiles closes the loop through
// the PRODUCTION resolver: two real, indexed files — one inside the configured
// root, one outside it — and only the contained one gets a folder button.
func TestWorkflowResolverOffersRevealOnlyForContainedFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "loras"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv, _ := newRevealServer(t, root)
	inID := seedFile(t, srv, filepath.Join(root, "loras", "inside.safetensors"))
	outID := seedFile(t, srv, filepath.Join(outside, "outside.safetensors"))

	res := srv.workflowResolver()

	in := renderString(t, workflowResourceChip("inside.safetensors", res))
	if !strings.Contains(in, "/library/files/"+strconv.FormatInt(inID, 10)+"/reveal") {
		t.Fatalf("a contained file should offer the folder button:\n%s", in)
	}
	out := renderString(t, workflowResourceChip("outside.safetensors", res))
	if strings.Contains(out, "/reveal") {
		t.Fatalf("an uncontained file (id=%d) must NOT offer the folder button:\n%s", outID, out)
	}
}

// TestResourceChipFolderButtonVisibility: the control appears ONLY for a
// concrete, resolved file on a loopback bind — never for an ambiguous basename
// (which resolves to no id and no path), never for a missing file, and never when
// the capability is gated off.
func TestResourceChipFolderButtonVisibility(t *testing.T) {
	tests := []struct {
		name       string
		info       resourceInfo
		have       bool
		openFolder bool
		wantButton bool
	}{
		{
			name: "resolved, CONTAINED local file on a loopback bind", have: true, openFolder: true,
			info:       resourceInfo{Path: "/models/loras/a.safetensors", FileID: 12, Contained: true},
			wantButton: true,
		},
		{
			// The click would be refused by the containment check, so offering the
			// control at all would be offering something that cannot work.
			name: "a resolved file OUTSIDE every configured root gets no button", have: true, openFolder: true,
			info:       resourceInfo{Path: "/elsewhere/a.safetensors", FileID: 12},
			wantButton: false,
		},
		{
			// HasLocalFileNamed says "present"; LocalFileByBasename refuses to resolve
			// the ambiguity, so there is no single folder to open and no button.
			name: "AMBIGUOUS basename: present, but no path and no id", have: true, openFolder: true,
			info:       resourceInfo{},
			wantButton: false,
		},
		{
			name: "an id with no path is not revealable", have: true, openFolder: true,
			info:       resourceInfo{FileID: 12, Contained: true},
			wantButton: false,
		},
		{
			name: "a path with no id is not revealable", have: true, openFolder: true,
			info:       resourceInfo{Path: "/models/loras/a.safetensors", Contained: true},
			wantButton: false,
		},
		{
			name: "not in the library", have: false, openFolder: true,
			info:       resourceInfo{},
			wantButton: false,
		},
		{
			// Everything else about this resource qualifies — only the bind gates it.
			name: "capability gated off (non-loopback bind)", have: true, openFolder: false,
			info:       resourceInfo{Path: "/models/loras/a.safetensors", FileID: 12, Contained: true},
			wantButton: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := provenanceResolver(tc.info, tc.have)
			res.openFolder = tc.openFolder
			res.csrf = "csrf-tok"
			got := renderString(t, workflowResourceChip("a.safetensors", res))

			hasButton := strings.Contains(got, `hx-post="/library/files/12/reveal"`)
			if hasButton != tc.wantButton {
				t.Fatalf("folder button present = %v, want %v:\n%s", hasButton, tc.wantButton, got)
			}
			if !tc.wantButton {
				if strings.Contains(got, "/reveal") || strings.Contains(got, "cm-res-open") {
					t.Fatalf("a reveal affordance leaked into a chip that must not have one:\n%s", got)
				}
				return
			}
			for _, want := range []string{
				// hx-vals is JSON inside an attribute, so the quotes are entity-escaped.
				`hx-vals="{&#34;csrf_token&#34;:&#34;csrf-tok&#34;}"`,
				"cm-res-open-btn",
				"computer running civitai-manager",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			// The button must NOT be nested inside the chip's anchor/span — it is a
			// sibling inside the .cm-res-item wrapper.
			if !strings.HasPrefix(got, `<span class="cm-res-item">`) {
				t.Errorf("expected the chip+button wrapper, got:\n%s", got)
			}
			// The request carries an id and a token, never a path.
			if strings.Contains(got, "/models/loras/a.safetensors\"") && strings.Contains(got, "hx-vals") {
				if strings.Contains(got, `"path"`) {
					t.Errorf("the reveal POST must not carry a path:\n%s", got)
				}
			}
		})
	}
}
