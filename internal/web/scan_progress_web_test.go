package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// pendingResult builds a streamed result in the unmatched-pending state (a
// rate-limited/transient lookup to retry) for the OnFile seam.
func pendingResult(name string) library.FileResult {
	return library.FileResult{
		Path: "/models/" + name, Name: name, SizeBytes: 10,
		SHA256: "sha-" + name, Status: store.LocalStatusUnmatchedPending,
	}
}

// nilScan is a trivially-completing scan seam (the noRemote value the handler
// computes is recorded on the job regardless of the seam).
func nilScan(context.Context, func(library.FileResult), func(int), func(int)) error { return nil }

// --- Change 1: default-on -----------------------------------------------------

// TestMatchRemoteDefaultsOn proves the reversed default: match_remote is ON when
// unset, an explicit "false" is respected (stays off), and "true" is on.
func TestMatchRemoteDefaultsOn(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	if !srv.matchRemoteEnabled() {
		t.Fatal("match_remote should default ON when the setting is unset")
	}
	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if srv.matchRemoteEnabled() {
		t.Fatal("an explicit match_remote=false must be respected (stays OFF)")
	}
	if err := srv.store.SetSetting(matchRemoteSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	if !srv.matchRemoteEnabled() {
		t.Fatal("an explicit match_remote=true must be ON")
	}
}

// TestScanDefaultRunsRemoteMatching proves a scan started with NO explicit setting
// runs with CivitAI matching ON (the job records noRemote=false — the exact value
// handleLibraryScan passes to startScan/newScanner), and an explicit "false"
// setting still yields an offline scan (noRemote=true). One server, sequential
// scans: the :memory: store uses cache=shared, so two live servers would share one
// DB and their settings would collide.
func TestScanDefaultRunsRemoteMatching(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.scanFn = nilScan

	// Default (unset) → matching ON.
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	pollScanUntilDone(t, srv)
	if srv.scanJobState().NoRemote {
		t.Fatal("a default scan (match_remote unset) must run with remote matching ON (noRemote=false)")
	}

	// Explicit false → OFFLINE. The prior job has settled, so a second POST starts
	// a fresh job carrying the new noRemote value.
	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	pollScanUntilDone(t, srv)
	if !srv.scanJobState().NoRemote {
		t.Fatal("an explicit match_remote=false scan must run offline (noRemote=true)")
	}
}

// TestModelScanFormChecksRemoteMatchByDefault proves the Tab B checkbox reflects
// the effective state: checked when matching is enabled, unchecked when disabled.
func TestModelScanFormChecksRemoteMatchByDefault(t *testing.T) {
	on := renderString(t, modelScanForm("csrf", true))
	if !strings.Contains(on, "checked") {
		t.Errorf("match_remote checkbox should be checked by default:\n%s", on)
	}
	off := renderString(t, modelScanForm("csrf", false))
	if strings.Contains(off, "checked") {
		t.Errorf("match_remote checkbox should be unchecked when matching is disabled:\n%s", off)
	}
}

// TestLibraryFilesTabChecksRemoteMatchByDefault proves the full wiring
// (matchRemoteEnabled → libraryPage → modelScanForm): GET the Model files tab with
// no setting renders the checkbox checked; with match_remote=false it is unchecked.
func TestLibraryFilesTabChecksRemoteMatchByDefault(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	if err := srv.store.AddScanDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	body := get(t, srv, "/library?tab=files").Body.String()
	if !strings.Contains(body, `name="match_remote"`) {
		t.Fatalf("Tab B should render the match_remote checkbox:\n%s", body)
	}
	if !strings.Contains(body, "checked") {
		t.Errorf("Tab B match_remote checkbox should default checked (matching ON):\n%s", body)
	}

	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	body = get(t, srv, "/library?tab=files").Body.String()
	if strings.Contains(body, "checked") {
		t.Errorf("Tab B match_remote checkbox should be unchecked when match_remote=false:\n%s", body)
	}
}

// TestScanTransparencyNoteRendered proves both scan entry points carry the
// hash-upload transparency note.
func TestScanTransparencyNoteRendered(t *testing.T) {
	const note = "sends file hashes to civitai.com"
	form := renderString(t, modelScanForm("csrf", true))
	if !strings.Contains(form, note) {
		t.Errorf("model scan form should carry the hash-upload transparency note:\n%s", form)
	}
	cta := renderString(t, scanForModelsCTA("csrf"))
	if !strings.Contains(cta, note) {
		t.Errorf("Tab A 'Scan for models' CTA should carry the hash-upload transparency note:\n%s", cta)
	}
}

// --- Change 2: progress counts + matching-off indicator -----------------------

// TestScanTalliesMatchedUnmatchedPending proves the job partitions streamed
// FileResults into matched / unmatched / pending (scanned == the sum) and the
// terminal wording shows the full breakdown.
func TestScanTalliesMatchedUnmatchedPending(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onDiscovered(4)                              // walk found 4 model files
		onFile(fileResult("a.safetensors", intp(1))) // matched
		onFile(fileResult("b.safetensors", nil))     // unmatched
		onFile(fileResult("c.safetensors", nil))     // unmatched
		onFile(pendingResult("d.safetensors"))       // pending
		return nil
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	term := pollScanUntilDone(t, srv)

	snap := srv.scanJobState()
	if snap.Scanned != 4 || snap.Matched != 1 || snap.Unmatched != 2 || snap.Pending != 1 {
		t.Fatalf("counts scanned=%d matched=%d unmatched=%d pending=%d; want 4/1/2/1",
			snap.Scanned, snap.Matched, snap.Unmatched, snap.Pending)
	}
	if snap.Discovered != 4 {
		t.Fatalf("job should capture the discovered total from the seam: got %d, want 4", snap.Discovered)
	}
	if !strings.Contains(term, "Scan complete — 4 discovered · 1 matched · 2 unmatched · 1 pending") {
		t.Errorf("terminal should show discovered total + matched/unmatched/pending breakdown:\n%s", term)
	}
}

// TestScanScanningRendersCounts proves the in-progress fragment renders progress
// against the discovered total ("N / D discovered") plus matched/unmatched/pending.
func TestScanScanningRendersCounts(t *testing.T) {
	frag := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true, Scanned: 120, Discovered: 454, Matched: 40, Unmatched: 78, Pending: 2,
	}, "csrf"))
	if !strings.Contains(frag, "120 / 454 discovered · matched 40 · unmatched 78 · pending 2") {
		t.Errorf("scanning progress should show N / total discovered + matched/unmatched/pending:\n%s", frag)
	}
	// With zero pending, the pending term is omitted (kept clean).
	frag = renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true, Scanned: 10, Discovered: 16, Matched: 4, Unmatched: 6,
	}, "csrf"))
	if !strings.Contains(frag, "10 / 16 discovered · matched 4 · unmatched 6") || strings.Contains(frag, "pending") {
		t.Errorf("scanning progress should omit the pending term when zero:\n%s", frag)
	}
	// Before the walk reports a total (discovered==0, nothing scanned), the line
	// reads "walking…" rather than "0 / 0 discovered".
	walking := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true,
	}, "csrf"))
	if !strings.Contains(walking, "walking…") {
		t.Errorf("scanning progress before the walk finishes should read 'walking…':\n%s", walking)
	}
}

// TestScanScanningRendersHashingProgress proves the phase-1 fix: DURING hashing
// (discovered known, hashed climbing, no card streamed yet) the scanning line shows
// a MOVING "Hashing files… N / D discovered" numerator — not a frozen "0 / total".
// It then shows "Matching…" once every file is hashed but before any card, and
// finally the streaming "N / D discovered · matched…" line once cards arrive.
func TestScanScanningRendersHashingProgress(t *testing.T) {
	// Phase 1 — hashing in progress (Scanned==0, Hashed < Discovered).
	hashing := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true, Discovered: 454, Hashed: 42,
	}, "csrf"))
	if !strings.Contains(hashing, "Hashing files… 42 / 454 discovered") {
		t.Errorf("mid-hash snapshot should show a moving 'Hashing files… N / D discovered' line:\n%s", hashing)
	}
	if strings.Contains(hashing, "matched") {
		t.Errorf("no cards have streamed yet; the hashing line must not show a match breakdown:\n%s", hashing)
	}

	// Phase 2 — every file hashed, batch by-hash lookup in flight (Scanned still 0).
	matching := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true, Discovered: 454, Hashed: 454,
	}, "csrf"))
	if !strings.Contains(matching, "Matching… (454 files hashed)") {
		t.Errorf("all-hashed snapshot should show the 'Matching…' batch line:\n%s", matching)
	}

	// Phase 3 — cards streaming: the existing match breakdown returns.
	streaming := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true, Discovered: 454, Hashed: 454, Scanned: 120, Matched: 40, Unmatched: 78,
	}, "csrf"))
	if !strings.Contains(streaming, "120 / 454 discovered · matched 40 · unmatched 78") {
		t.Errorf("phase-3 snapshot should show the streaming match breakdown:\n%s", streaming)
	}
}

// TestScanAccumulatesHashed proves the web job accumulates the scanner's OnHashed
// increments under scanMu and that scanJobState's snapshot carries the total —
// the counter the "Hashing… N / D" line reads.
func TestScanAccumulatesHashed(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onDiscovered(3)
		onHashed(1) // phase 1 — three files hash, one increment each
		onHashed(1)
		onHashed(1)
		onFile(fileResult("a.safetensors", intp(1))) // phase 3 — cards stream
		onFile(fileResult("b.safetensors", nil))
		onFile(fileResult("c.safetensors", nil))
		return nil
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	pollScanUntilDone(t, srv)

	if snap := srv.scanJobState(); snap.Hashed != 3 {
		t.Fatalf("job should accumulate hashed increments: got %d, want 3", snap.Hashed)
	}
}

// TestMatchingOffIndicator proves the matching-off note appears (only) when the
// scan ran with matching disabled, in both the scanning and terminal fragments.
func TestMatchingOffIndicator(t *testing.T) {
	const note = "CivitAI matching is OFF"

	offScan := renderString(t, scanScanning(scanSnapshot{Started: true, Running: true, NoRemote: true}, "csrf"))
	if !strings.Contains(offScan, note) {
		t.Errorf("scanning fragment with matching off should show the note:\n%s", offScan)
	}
	onScan := renderString(t, scanScanning(scanSnapshot{Started: true, Running: true}, "csrf"))
	if strings.Contains(onScan, note) {
		t.Errorf("scanning fragment with matching on must NOT show the note:\n%s", onScan)
	}

	offTerm := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{Started: true, NoRemote: true}, "csrf"))
	if !strings.Contains(offTerm, note) {
		t.Errorf("terminal fragment with matching off should show the note:\n%s", offTerm)
	}
	onTerm := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{Started: true}, "csrf"))
	if strings.Contains(onTerm, note) {
		t.Errorf("terminal fragment with matching on must NOT show the note:\n%s", onTerm)
	}
}

// TestScanOfflineShowsMatchingOffNote proves the end-to-end wiring: a scan with
// match_remote=false carries the matching-off note into its terminal fragment,
// and a default (matching on) scan does not. One server, sequential scans (the
// :memory: store is cache=shared — see TestScanDefaultRunsRemoteMatching).
func TestScanOfflineShowsMatchingOffNote(t *testing.T) {
	const note = "CivitAI matching is OFF"
	srv := newLibraryTestServer(t, t.TempDir())

	// Offline scan → note present.
	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onFile(fileResult("a.safetensors", nil))
		return nil
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	if term := pollScanUntilDone(t, srv); !strings.Contains(term, note) {
		t.Errorf("an offline (match_remote=false) scan terminal should carry the matching-off note:\n%s", term)
	}

	// Re-enable matching → a fresh scan drops the note.
	if err := srv.store.SetSetting(matchRemoteSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onFile(fileResult("a.safetensors", intp(2)))
		return nil
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	if term := pollScanUntilDone(t, srv); strings.Contains(term, note) {
		t.Errorf("a default (matching on) scan must NOT show the matching-off note:\n%s", term)
	}
}
