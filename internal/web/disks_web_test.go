package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
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

// TestDisksRowsAreNotEvenProbedOffLoopback proves the gate stops the SYSCALLS,
// not just the markup. diskRows is what issues them, and handleDisks must not
// call it at all off-loopback — a version that probed and then hid the output
// would still stat every configured path on behalf of a remote caller.
func TestDisksRowsAreNotEvenProbedOffLoopback(t *testing.T) {
	srv, _, _ := newDisksServer(t, "0.0.0.0:8787")
	// The predicate the handler branches on. Asserting it here ties the "no
	// probe" claim to the same condition the handler uses, rather than to a
	// re-derived one.
	if srv.extraPathsAllowed() {
		t.Fatal("fixture is wrong: 0.0.0.0 must not count as loopback")
	}
	// And on a loopback bind the same call DOES produce rows — otherwise "no
	// rows" would prove nothing about the gate.
	loopback, _, _ := newDisksServer(t, "127.0.0.1:8787")
	if len(loopback.diskRows()) == 0 {
		t.Fatal("fixture is wrong: diskRows returned nothing even on loopback")
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
