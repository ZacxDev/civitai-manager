package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newDisksServer builds a server bound to addr whose ModelRoot is a REAL
// directory (so the capacity probe has something to succeed on) plus a
// deliberately missing library path (so the unknown-capacity branch is exercised
// by the same request).
func newDisksServer(t *testing.T, addr string) (*Server, string, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	missing := filepath.Join(t.TempDir(), "not-mounted")
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		Addr: addr, ModelRoot: root, LibraryPaths: []string{missing},
		DefaultPollInterval: time.Hour,
	}, nil)
	return srv, root, missing
}

// TestDisksRendersCapacityOnLoopback is the happy path, and it asserts BOTH
// branches of the best-effort contract in one render: a real directory reports
// real figures, and a missing one reports "unknown" without erroring the page.
func TestDisksRendersCapacityOnLoopback(t *testing.T) {
	srv, root, missing := newDisksServer(t, "127.0.0.1:8787")

	rec := get(t, srv, "/disks")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /disks = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Both configured directories are listed.
	if !strings.Contains(body, root) {
		t.Errorf("the model root %q is missing from the page:\n%s", root, firstN(body, 2000))
	}
	if !strings.Contains(body, missing) {
		t.Errorf("the (missing) library path %q must still get a row:\n%s", missing, firstN(body, 2000))
	}

	// The KNOWN row renders a meter and real figures. "free of" is the figures
	// line's own wording, so its presence means the arithmetic branch ran rather
	// than the unknown branch.
	if !strings.Contains(body, `class="cm-meter"`) {
		t.Errorf("a stat-able directory must render a capacity meter:\n%s", firstN(body, 2000))
	}
	if !strings.Contains(body, "free of") {
		t.Errorf("a stat-able directory must render its used/free/total figures:\n%s", firstN(body, 2000))
	}

	// The UNKNOWN row renders the explanation, NOT a fabricated 0-byte disk.
	if !strings.Contains(body, "Capacity unknown") {
		t.Errorf("the missing directory must render as unknown capacity:\n%s", firstN(body, 2000))
	}
	if !strings.Contains(body, "does not exist or is not mounted") {
		t.Errorf("an unknown row must say WHY:\n%s", firstN(body, 2000))
	}
	// A 0-byte meter would read as a real, completely-empty disk. The unknown row
	// must not emit one at all.
	if n := strings.Count(body, `class="cm-meter"`); n != 1 {
		t.Errorf("expected exactly 1 meter (the one stat-able dir), got %d — the unknown row must not render a 0%% bar", n)
	}
}

// TestDisksHidesPathsOffLoopback is the gate. It asserts the REJECTION, not just
// that the page still renders: on a non-loopback bind no configured filesystem
// path may reach the response, and the standard gate wording must explain why.
func TestDisksHidesPathsOffLoopback(t *testing.T) {
	srv, root, missing := newDisksServer(t, "0.0.0.0:8787") // LAN-exposed

	// Sanity: the same fixture DOES leak these paths on a loopback bind, so the
	// absence below is caused by the gate rather than by the paths never being
	// renderable in the first place.
	loopback, lRoot, _ := newDisksServer(t, "127.0.0.1:8787")
	if !strings.Contains(get(t, loopback, "/disks").Body.String(), lRoot) {
		t.Fatal("fixture is wrong: /disks does not print the model root even on loopback, so the gate assertion below would be vacuous")
	}

	rec := get(t, srv, "/disks")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /disks = %d; the gated page must still render (with the notice)", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, gateMsg) {
		t.Errorf("the gated page must carry the standard non-loopback notice:\n%s", firstN(body, 2000))
	}
	for _, p := range []string{root, missing} {
		if strings.Contains(body, p) {
			t.Errorf("a non-loopback response leaked the filesystem path %q:\n%s", p, firstN(body, 2000))
		}
	}
	// No capacity at all — not even for a directory whose path happened not to
	// appear. The meter is the tell.
	if strings.Contains(body, `class="cm-meter"`) {
		t.Errorf("the gated page must render no capacity meter:\n%s", firstN(body, 2000))
	}
}

// countProbes swaps in a counting diskStatFn and returns the counter. The
// substitute answers with a REAL-LOOKING Usage rather than an error, so a
// probe-then-hide handler produces a fully populated page and is caught by the
// count rather than by some downstream symptom.
func countProbes(srv *Server) *atomic.Int64 {
	var n atomic.Int64
	srv.diskStatFn = func(string) (diskusage.Usage, error) {
		n.Add(1)
		return diskusage.Usage{Total: 1000, Free: 250, Used: 750}, nil
	}
	return &n
}

// TestDisksRowsAreNotEvenProbedOffLoopback proves the gate stops the SYSCALLS,
// not just the markup: off-loopback handleDisks must issue ZERO filesystem
// probes, because a version that probed and then hid the output would still stat
// every configured path on behalf of a remote caller.
//
// 🔴 IT DRIVES THE REAL HANDLER AND COUNTS AT THE SYSCALL. The first version of
// this test asserted only that extraPathsAllowed() was false and that diskRows()
// returned rows on loopback — it never called handleDisks at all, so rewriting
// the handler into exactly the shape this comment forbids
// (`rows := s.diskRows(); if gated { rows = nil }`) left it GREEN. The counter
// lives on Server.diskStatFn, one level BELOW diskRows, precisely so that shape
// cannot dodge it: whichever route the handler takes to a Usage, it is counted.
func TestDisksRowsAreNotEvenProbedOffLoopback(t *testing.T) {
	// --- the control: on loopback the SAME request probes every directory ------
	// Without this the "0 probes" assertion below would be satisfied by a page
	// that never probes anywhere, which proves nothing about the gate.
	loopback, _, _ := newDisksServer(t, "127.0.0.1:8787")
	lp := countProbes(loopback)
	if rec := get(t, loopback, "/disks"); rec.Code != http.StatusOK {
		t.Fatalf("loopback GET /disks = %d, want 200", rec.Code)
	}
	// The fixture configures a model root, a library path and (derived) a trash
	// dir. An exact count, not >0: it also pins that the gated case below is
	// suppressing a probe per directory rather than a single one.
	const wantProbes = 3
	if got := lp.Load(); got != wantProbes {
		t.Fatalf("fixture is wrong: a loopback GET /disks issued %d probes, want %d — "+
			"the gated assertion below would not be measuring anything", got, wantProbes)
	}

	// --- the gate: off-loopback, not one probe --------------------------------
	srv, _, _ := newDisksServer(t, "0.0.0.0:8787") // LAN-exposed
	if srv.extraPathsAllowed() {
		t.Fatal("fixture is wrong: 0.0.0.0 must not count as loopback")
	}
	gp := countProbes(srv)
	if rec := get(t, srv, "/disks"); rec.Code != http.StatusOK {
		t.Fatalf("gated GET /disks = %d; the page must still render", rec.Code)
	}
	if got := gp.Load(); got != 0 {
		t.Errorf("a non-loopback GET /disks issued %d filesystem probes, want 0 — "+
			"the gate must skip the collection entirely, not collect and then hide it", got)
	}
}

// TestDisksReportsTheDerivedTrashDir pins the DEFAULT-config case, which is the
// one that shipped broken: `trash_dir` is unset on essentially every install, so
// reading s.cfg.TrashDir raw contributed no row at all and /disks silently
// omitted the quarantine directory its own doc comment promises.
//
// The row must name the SAME directory quarantine actually writes to — the
// derivation is library.ResolveTrashDir, shared with library.NewScanner, and the
// expected path below is built from that shared helper rather than restated.
func TestDisksReportsTheDerivedTrashDir(t *testing.T) {
	srv, root, _ := newDisksServer(t, "127.0.0.1:8787")
	// The fixture must actually be in the default state, or this proves nothing.
	if srv.cfg.TrashDir != "" {
		t.Fatalf("fixture is wrong: TrashDir = %q, but the case under test is the UNSET default", srv.cfg.TrashDir)
	}
	want := library.ResolveTrashDir("", root)
	if want == filepath.Clean(root) {
		t.Fatalf("fixture is wrong: the derived trash dir %q is the model root, so a row for it "+
			"would be indistinguishable from the model root's own row", want)
	}

	var probed []string
	srv.diskStatFn = func(p string) (diskusage.Usage, error) {
		probed = append(probed, p)
		return diskusage.Usage{Total: 1000, Free: 250, Used: 750}, nil
	}
	body := get(t, srv, "/disks").Body.String()

	if !strings.Contains(body, want) {
		t.Errorf("/disks has no row for the derived trash dir %q:\n%s", want, firstN(body, 2000))
	}
	if !strings.Contains(body, ">Trash<") {
		t.Errorf("/disks has no row LABELLED Trash:\n%s", firstN(body, 2000))
	}
	// And it is a real probe target, not just a string in the markup.
	if !slices.Contains(probed, want) {
		t.Errorf("the trash dir %q was never probed; probed = %v", want, probed)
	}

	// An EXPLICIT trash_dir must still win over the derivation.
	explicit := filepath.Join(t.TempDir(), "elsewhere")
	srv2, _, _ := newDisksServer(t, "127.0.0.1:8787")
	srv2.cfg.TrashDir = explicit
	srv2.diskStatFn = func(string) (diskusage.Usage, error) {
		return diskusage.Usage{Total: 1000, Free: 250, Used: 750}, nil
	}
	body2 := get(t, srv2, "/disks").Body.String()
	if !strings.Contains(body2, explicit) {
		t.Errorf("an explicitly configured trash_dir %q must be the row's path:\n%s", explicit, firstN(body2, 2000))
	}
	if strings.Contains(body2, library.ResolveTrashDir("", srv2.cfg.ModelRoot)) {
		t.Errorf("the derived default leaked into the page even though trash_dir was set:\n%s", firstN(body2, 2000))
	}
}

// TestDisksShowsQuarantineBatches proves /disks absorbed the /trash content: a
// seeded quarantine batch must appear with its restore control, targeting the
// SAME container id and endpoint the old page used.
func TestDisksShowsQuarantineBatches(t *testing.T) {
	srv, _, _ := newDisksServer(t, "127.0.0.1:8787")

	id, err := srv.store.CreateQuarantineBatch("/tmp/trash", "/tmp/trash/manifest.json", store.CandidateDuplicate)
	if err != nil {
		t.Fatalf("create quarantine batch: %v", err)
	}
	if _, err := srv.store.AddQuarantinedFile(store.QuarantinedFile{
		BatchID:      id,
		OriginalPath: "/models/a.safetensors",
		TrashPath:    "/tmp/trash/a.safetensors",
		Reason:       store.CandidateDuplicate,
	}); err != nil {
		t.Fatalf("add quarantined file: %v", err)
	}

	body := get(t, srv, "/disks").Body.String()

	// The fixture must reach the populated branch — an empty table renders the
	// empty state, in which case every assertion about the row would be vacuous.
	if strings.Contains(body, "Nothing in the trash") {
		t.Fatalf("the seeded batch did not reach the page; the row assertions would be vacuous:\n%s", firstN(body, 2000))
	}
	for _, want := range []string{
		`id="trash-content"`,         // the htmx swap target the restore button names
		`hx-post="/trash/1/restore"`, // the endpoint is UNMOVED
		"duplicate",                  // the batch's reason badge
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/disks quarantine section is missing %q:\n%s", want, firstN(body, 3000))
		}
	}
}

// TestDisksEmptyCapacityState covers the third capacity branch: nothing
// configured at all. It must render the guided empty state, not an empty div and
// not a crash.
func TestDisksEmptyCapacityState(t *testing.T) {
	out := renderString(t, disksCapacityCard(nil, false))
	if !strings.Contains(out, "No model directories configured") {
		t.Errorf("an unconfigured install must get a guided empty state:\n%s", out)
	}
	if !strings.Contains(out, libraryModelFilesHref) {
		t.Errorf("the empty state must offer a way to configure one:\n%s", out)
	}
}

// TestDiskRowUnknownRendersWithoutFiguresIsExhaustive drives diskRowNode
// directly across the three shapes it must survive: known, unknown-with-reason,
// and unknown-with-NO-reason (a probe that returned a zero Usage and a nil
// error). The last one is the crash-shaped case — it has nothing to print — and
// it must still produce a complete row.
func TestDiskRowRendersEveryUsageShape(t *testing.T) {
	cases := []struct {
		name    string
		row     diskRow
		want    []string
		notWant []string
	}{
		{
			"known", diskRow{Label: "Model root", Path: "/m", Usage: diskusage.Usage{Total: 1000, Free: 250, Used: 750}},
			[]string{"cm-meter-fill", `data-level="ok"`, "width:75.0%", "free of"},
			[]string{"Capacity unknown"},
		},
		{
			"unknown with reason", diskRow{Label: "Library path", Path: "/l", Err: "boom"},
			[]string{"Capacity unknown — boom", "/l"},
			[]string{"cm-meter"},
		},
		{
			"unknown with no reason", diskRow{Label: "Trash", Path: "/t"},
			[]string{"Capacity unknown — the filesystem did not report a size", "/t"},
			[]string{"cm-meter"},
		},
		{
			"nearly full tints", diskRow{Label: "Model root", Path: "/m", Usage: diskusage.Usage{Total: 1000, Free: 20, Used: 950}},
			[]string{`data-level="warn"`},
			[]string{`data-level="ok"`},
		},
		{
			"completely full tints", diskRow{Label: "Model root", Path: "/m", Usage: diskusage.Usage{Total: 1000, Free: 0, Used: 1000}},
			[]string{`data-level="full"`, "width:100.0%"},
			[]string{`data-level="warn"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderString(t, diskRowNode(c.row))
			// Every shape must produce a real row carrying the label — that is the
			// "renders without erroring" half.
			if !strings.Contains(out, c.row.Label) {
				t.Fatalf("row lost its label:\n%s", out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q:\n%s", w, out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(out, w) {
					t.Errorf("must not contain %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestDisksNeverBreaksTheRestorePost is the belt-and-braces half of the /trash
// migration: the GET moved, so this proves the POST did NOT. It goes through the
// real mux with a real CSRF token.
func TestDisksNeverBreaksTheRestorePost(t *testing.T) {
	srv, _, _ := newDisksServer(t, "127.0.0.1:8787")
	// A batch that does not exist is enough: the point is that the ROUTE is still
	// registered and reaches handleRestore, which answers with an error NOTE
	// (200 + text), not a 404/405 from the mux.
	rec := post(t, srv, "/trash/999/restore", url.Values{}, true)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST /trash/{id}/restore = %d — the redirect must not have swallowed the POST route", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Restore failed") {
		t.Errorf("expected handleRestore's own failure note, got:\n%s", rec.Body.String())
	}
}
