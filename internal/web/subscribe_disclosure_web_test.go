package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ===========================================================================
// Guards for the subscribe panel's "what will this actually download?" copy.
// ===========================================================================
//
// Every one of these is an ANTI-LIE guard, so each carries an absence half: the
// failure mode is not "the disclosure is missing", it is "the disclosure states
// a confident, specific, wrong number". A size printed for a notify-only
// subscription, for a workflow post, or for a file the user's own size cap would
// skip is worse than no size at all.

// sizePattern matches the shape of a rendered byte size (humanBytes output:
// "6.5 GB", "512 B"). The absence checks assert on this rather than on a literal
// string, so a lie cannot slip through by being a DIFFERENT wrong number than
// the one a hard-coded assertion happened to name.
var sizePattern = regexp.MustCompile(`\d+(\.\d+)?\s(B|KB|MB|GB|TB)\b`)

// checkpointRaw is a two-version Checkpoint whose FEATURED version (index 0, the
// one the model page defaults to) is the OLDER one, and whose newest-by-
// publishedAt version is second in the array.
//
// That inversion is the whole point of the fixture: modelVersions[] is ordered by
// the creator's index, not by date, so a disclosure that reached for [0] would
// quote "1.0 GB / old.safetensors" and look perfectly reasonable. The test can
// only tell the two apart because the fixture makes them differ.
const checkpointRaw = `{
  "id": 4242, "name": "Nice Model", "type": "Checkpoint",
  "modelVersions": [
    {"id": 10, "name": "v1-featured", "baseModel": "SDXL 1.0",
     "publishedAt": "2024-01-01T00:00:00.000Z",
     "files": [{"id": 1, "name": "old.safetensors", "type": "Model", "sizeKB": 1048576, "primary": true}]},
    {"id": 11, "name": "v2-newest", "baseModel": "SDXL 1.0",
     "publishedAt": "2025-06-01T00:00:00.000Z",
     "files": [{"id": 2, "name": "new.safetensors", "type": "Model", "sizeKB": 6815744, "primary": true}]}
  ]
}`

// workflowRaw is the SAME payload with one field changed: type "Workflows". That
// is deliberately the only difference — a workflow post is shaped exactly like a
// checkpoint (a primary file with a real sizeKB), which is precisely why a naive
// implementation prints a size for bytes that will never be fetched.
const workflowRaw = `{
  "id": 4243, "name": "Nice Workflow Pack", "type": "Workflows",
  "modelVersions": [
    {"id": 20, "name": "v1", "baseModel": "SDXL 1.0",
     "publishedAt": "2025-06-01T00:00:00.000Z",
     "files": [{"id": 3, "name": "pack_AIO.zip", "type": "Archive", "sizeKB": 6815744, "primary": true}]}
  ]
}`

// newDisclosureServer builds a server over a FILE-backed store and a reader that
// FAILS every GetModel.
//
// Both halves are load-bearing:
//   - a file DB, because store.Open(":memory:") resolves to
//     file::memory:?cache=shared — every in-memory store in this package is the
//     SAME database, so two tests seeding model_cache would read each other's rows.
//   - a failing reader, so a seeded cache that is NOT read cannot be papered over
//     by a live fetch returning some other model. A broken fixture then produces
//     the "this model" fallback, which every assertion below notices.
func newDisclosureServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cm.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg.BaseURL = "https://civitai.com"
	cfg.DefaultPollInterval = time.Hour
	cfg.Addr = "127.0.0.1:8787"
	return NewServer(st, errModelReader{}, storeSubscriber{st: st}, cfg, nil)
}

// seedModelCache stores a raw model body so the options panel resolves it with no
// network call.
func seedModelCache(t *testing.T, srv *Server, id int, name, raw string) {
	t.Helper()
	if err := srv.store.PutModelCache(id, name, []byte(raw)); err != nil {
		t.Fatalf("seed model cache: %v", err)
	}
}

// disclosureLine returns the <p>…</p> of ONE mode's consequence line.
//
// It keys on the mode name inside its <span> label, NOT on the bare word: the
// word "Auto-download" also appears as the radio's own label a few nodes
// earlier, and slicing from there would return a paragraph that is not the
// disclosure at all — which for the scoped ABSENCE checks below would be a free
// pass. It t.Fatal's rather than returning an empty string for the same reason.
func disclosureLine(t *testing.T, body, mode string) string {
	t.Helper()
	marker := ">" + mode + "</span>"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no disclosure line for %q (expected a <span>%s</span> label):\n%s", mode, mode, body)
	}
	start := strings.LastIndex(body[:i], "<p>")
	end := strings.Index(body[i:], "</p>")
	if start < 0 || end < 0 {
		t.Fatalf("no <p> wrapping the %q disclosure line:\n%s", mode, body)
	}
	return body[start : i+end]
}

// TestSubscribeOptionsDisclosesWhatWillDownload is the positive guard: for a real
// auto-downloadable model the panel states, before Confirm, what a subscription
// would fetch and how big it is.
//
// It pins all four facts the copy has to get right at once, because they are
// separable and each has its own wrong answer:
//
//	nothing NOW            — a web subscribe seeds the ledger without downloading
//	a RATE, not a total    — per future version, not a one-off cost
//	ONE FILE               — SelectFile picks one, not the version's file list
//	the NEWEST version     — not modelVersions[0], which is the featured one
func TestSubscribeOptionsDisclosesWhatWillDownload(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)

	rec := get(t, srv, "/models/4242/subscribe-options")
	if rec.Code != 200 {
		t.Fatalf("options = %d", rec.Code)
	}
	body := rec.Body.String()

	// FIXTURE REACH. Without these the assertions below could all be about the
	// workflow-post arm (no radios) or about an unresolved model ("this model").
	if !strings.Contains(body, "Subscribe to Nice Model?") {
		t.Fatalf("the seeded cache was not read — every check below would be measuring the wrong panel:\n%s", body)
	}
	if !strings.Contains(body, `value="auto_download"`) {
		t.Fatalf("this is not the mode-chooser arm of the panel:\n%s", body)
	}

	// Fact 1: nothing is fetched at the moment of subscribing.
	if !strings.Contains(body, "Nothing is downloaded now") {
		t.Errorf("the panel must say that subscribing itself downloads nothing:\n%s", body)
	}
	// Facts 2 + 3: one file, per future version, with the real size and filename.
	auto := disclosureLine(t, body, "Auto-download")
	for _, want := range []string{
		"each new version published from now on",
		"one file per version",
		"6.5 GB",              // sizeKB 6815744 -> humanBytes
		"new.safetensors",     // the file SelectFile would pick
		"&#34;v2-newest&#34;", // named so the number is attributable (gomponents escapes the quotes)
	} {
		if !strings.Contains(auto, want) {
			t.Errorf("the auto-download line is missing %q:\n%s", want, auto)
		}
	}

	// Fact 4: the NEWEST version, not the featured one. Both halves — the older
	// version's size and its filename must appear nowhere on the panel, or the
	// disclosure is quoting a version the next download will not resemble.
	for _, wrong := range []string{"1.0 GB", "old.safetensors", "v1-featured"} {
		if strings.Contains(body, wrong) {
			t.Errorf("the disclosure quotes the FEATURED version (%q) instead of the newest — "+
				"modelVersions[] is index-ordered, not date-ordered:\n%s", wrong, body)
		}
	}
}

// TestSubscribeDisclosureClaimsNoDownloadForNotifyOnly is the notify-only half.
//
// 🔴 IT IS A STRUCTURAL ABSENCE, NOT A KEYWORD CHECK. The panel as a whole DOES
// carry a size (the auto-download arm legitimately quotes one), so "the body has
// no GB in it" would be false for the right reasons and unusable. The assertion
// is therefore scoped to the notify-only PARAGRAPH: whatever that line says, it
// may not contain a byte size, because notify-only fetches nothing at all.
func TestSubscribeDisclosureClaimsNoDownloadForNotifyOnly(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)
	body := get(t, srv, "/models/4242/subscribe-options").Body.String()

	// FIXTURE REACH: the panel really does print a size somewhere, so the scoped
	// absence below is discriminating rather than trivially true.
	if !sizePattern.MatchString(body) {
		t.Fatalf("the panel printed no size at all — the scoped absence check would prove nothing:\n%s", body)
	}

	notify := disclosureLine(t, body, "Notify only")
	if got := sizePattern.FindString(notify); got != "" {
		t.Errorf("the notify-only line states a download size (%q) — notify-only downloads NOTHING:\n%s", got, notify)
	}
	if !strings.Contains(notify, "nothing is ever downloaded") {
		t.Errorf("the notify-only line must say so plainly:\n%s", notify)
	}
}

// TestSubscribeDisclosureNeverClaimsADownloadForAWorkflowPost runs BOTH
// DIRECTIONS over payloads that differ in exactly one field.
//
// A Workflows-type post carries a real downloadUrl and a primary file with a real
// sizeKB, and the poller PERMANENTLY skips it (enqueueCandidate's type guard), so
// any arithmetic over its files produces a size for bytes that will never be
// fetched. The Checkpoint direction is what makes the Workflows direction mean
// something: without it, deleting the disclosure entirely would pass.
func TestSubscribeDisclosureNeverClaimsADownloadForAWorkflowPost(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)
	seedModelCache(t, srv, 4243, "Nice Workflow Pack", workflowRaw)

	// Direction 1 — a Checkpoint with these files DOES get a size.
	ck := get(t, srv, "/models/4242/subscribe-options").Body.String()
	if !sizePattern.MatchString(ck) {
		t.Fatalf("a Checkpoint must still disclose its download size — otherwise the workflow "+
			"assertion below is satisfied by the feature simply not existing:\n%s", ck)
	}

	// Direction 2 — the same file payload under type "Workflows" gets NONE.
	wf := get(t, srv, "/models/4243/subscribe-options").Body.String()
	if !strings.Contains(wf, "Notify me about Nice Workflow Pack?") {
		t.Fatalf("the workflow-post arm did not render — the checks below would be vacuous:\n%s", wf)
	}
	if got := sizePattern.FindString(wf); got != "" {
		t.Errorf("the workflow-post panel states a download size (%q); the poller permanently "+
			"skips a Workflows post, so nothing is ever fetched:\n%s", got, wf)
	}
	// And it must not have grown a mode chooser either: an Auto-download option
	// here would resolve to a permanent skip.
	if strings.Contains(wf, `value="auto_download"`) {
		t.Errorf("the workflow-post panel must not offer Auto-download:\n%s", wf)
	}
	// The pure-function half, so this cannot be defended only by the handler.
	if d := modelSubscribeDownload(decodeModelDetail([]byte(workflowRaw)), []byte(workflowRaw), 0); d.Known {
		t.Errorf("modelSubscribeDownload resolved a download for a Workflows post: %+v", d)
	}
	if d := modelSubscribeDownload(decodeModelDetail([]byte(checkpointRaw)), []byte(checkpointRaw), 0); !d.Known {
		t.Error("modelSubscribeDownload resolved nothing for a Checkpoint — the check above proves nothing")
	}
}

// TestSubscribeDisclosureInventsNoSizeWhenUnknown: an unresolvable model gets an
// honest "could not be read", never a plausible-looking figure.
func TestSubscribeDisclosureInventsNoSizeWhenUnknown(t *testing.T) {
	srv := newDisclosureServer(t, Config{}) // errModelReader, and nothing seeded
	body := get(t, srv, "/models/99/subscribe-options").Body.String()

	// FIXTURE REACH: we really are in the unknown case.
	if !strings.Contains(body, "Subscribe to this model?") {
		t.Fatalf("the model resolved after all — this is not the unknown case:\n%s", body)
	}
	if got := sizePattern.FindString(body); got != "" {
		t.Errorf("the panel invented a size (%q) for a model it could not resolve:\n%s", got, body)
	}
	if !strings.Contains(body, "could not be read") {
		t.Errorf("an unknown size must be stated as unknown:\n%s", body)
	}
}

// TestSubscribeDisclosureRespectsTheMaxFileSizeCap: when the configured
// max_file_size would make the poller permanently skip the file
// (BackfillFilteredSize), the panel must not advertise it as a download.
func TestSubscribeDisclosureRespectsTheMaxFileSizeCap(t *testing.T) {
	// 1 GB cap against a 6.5 GB primary file.
	srv := newDisclosureServer(t, Config{MaxFileSizeBytes: 1 << 30})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)
	body := get(t, srv, "/models/4242/subscribe-options").Body.String()

	auto := disclosureLine(t, body, "Auto-download")
	if !strings.Contains(auto, "nothing would actually download") {
		t.Errorf("an over-cap file must not be advertised as a download:\n%s", auto)
	}
	if !strings.Contains(auto, "max file size of 1.0 GB") {
		t.Errorf("the panel must name the limit that is blocking it:\n%s", auto)
	}
	// The promise the uncapped copy makes must be GONE, not merely joined by a
	// warning — "expect roughly that much each time" would still be a lie here.
	if strings.Contains(auto, "expect roughly that much") {
		t.Errorf("the over-cap line still promises a recurring download:\n%s", auto)
	}
}
