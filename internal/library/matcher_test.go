package library

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// matcherScanner builds a scanner wired for matcher tests: a fixed hash, no real
// sleeps, and a small retry budget.
func matcherScanner(t *testing.T, root string, reader civitai.Reader, noRemote bool, fixedHash string) *Scanner {
	t.Helper()
	sc := NewScanner(newTestStore(t), reader, Options{ModelRoot: root, NoRemote: noRemote}, nil)
	sc.hashFn = func(string) (string, error) { return fixedHash, nil }
	sc.waitFn = func(context.Context, time.Duration) {} // never actually sleep
	sc.maxHashRetries = 3
	return sc
}

func TestMatcherValidSidecarShortCircuitsAPI(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	// Civitai-Helper sidecar: model-version JSON with id (version) + modelId AND
	// the file's declared SHA256, which must match this file's bytes for the
	// short-circuit to be trusted.
	writeFile(t, filepath.Join(root, "m.civitai.info"),
		`{"id": 555, "modelId": 42, "files": [{"hashes": {"SHA256": "DEADBEEF", "AutoV2": "av2x"}}]}`)

	fr := &fakeReader{}
	sc := matcherScanner(t, root, fr, false, "deadbeef")

	res := sc.matchFile(context.Background(), model, "deadbeef")
	if res.status != store.LocalStatusMatched {
		t.Fatalf("status = %q, want matched", res.status)
	}
	if res.modelID == nil || *res.modelID != 42 || res.versionID == nil || *res.versionID != 555 {
		t.Fatalf("ids = %v/%v, want 42/555", res.modelID, res.versionID)
	}
	if res.autov2 != "av2x" {
		t.Fatalf("autov2 = %q, want av2x enriched from the matching sidecar file", res.autov2)
	}
	if fr.calls != 0 {
		t.Fatalf("API called %d times; a hash-verified sidecar must short-circuit it", fr.calls)
	}
}

// TestMatcherSidecarHashMismatchFallsThrough proves a sidecar whose declared
// SHA256 does NOT match the file's actual bytes is not trusted: it is ignored and
// matching falls through to the authoritative by-hash lookup (which here resolves
// the real ids), so a mislabeled/renamed sidecar can never misclassify the file.
func TestMatcherSidecarHashMismatchFallsThrough(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	// Sidecar claims ids 999/88 for a DIFFERENT file (hash "cafef00d"), but this
	// file actually hashes to "abc".
	writeFile(t, filepath.Join(root, "m.civitai.info"),
		`{"id": 999, "modelId": 88, "files": [{"hashes": {"SHA256": "cafef00d"}}]}`)

	fr := &fakeReader{byHash: versionMap("abc", version(10, 100, "abc"))}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched || fr.calls != 1 {
		t.Fatalf("mismatched sidecar must fall through to one API call: status=%q calls=%d", res.status, fr.calls)
	}
	if res.modelID == nil || *res.modelID != 100 || res.versionID == nil || *res.versionID != 10 {
		t.Fatalf("ids = %v/%v, want 100/10 from the by-hash path (not the bogus sidecar 88/999)", res.modelID, res.versionID)
	}
}

// TestMatcherSidecarNoHashFallsThrough proves a sidecar carrying ids but NO file
// hash cannot be trusted to adopt those ids: it falls through to by-hash.
func TestMatcherSidecarNoHashFallsThrough(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), `{"id": 555, "modelId": 42}`) // no files/hashes

	fr := &fakeReader{byHash: versionMap("abc", version(10, 100, "abc"))}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched || fr.calls != 1 {
		t.Fatalf("hashless sidecar must fall through to one API call: status=%q calls=%d", res.status, fr.calls)
	}
	if res.modelID == nil || *res.modelID != 100 {
		t.Fatalf("ids should come from by-hash (100), got %v", res.modelID)
	}
}

// TestMatcherSidecarNoHashOfflineUnmatched proves that in --no-remote mode a
// hashless (unverifiable) sidecar yields unmatched rather than a blind match.
func TestMatcherSidecarNoHashOfflineUnmatched(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), `{"id": 555, "modelId": 42}`) // no hash

	sc := matcherScanner(t, root, &panicReader{}, true, "abc") // NoRemote
	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusUnmatched {
		t.Fatalf("offline hashless sidecar should be unmatched, got %q", res.status)
	}
}

func TestMatcherEmptyOrCorruptSidecarFallsThrough(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), "   ") // empty/whitespace guard

	fr := &fakeReader{byHash: versionMap("abc", version(9, 3, "abc"))}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched || fr.calls != 1 {
		t.Fatalf("corrupt sidecar should fall through to one API call and match: status=%q calls=%d", res.status, fr.calls)
	}
}

func TestMatcherByHashFoundEnriches(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	fr := &fakeReader{byHash: versionMap("abc", version(10, 100, "abc"))}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched || *res.modelID != 100 || *res.versionID != 10 {
		t.Fatalf("expected matched 100/10, got %q %v/%v", res.status, res.modelID, res.versionID)
	}
	if res.autov2 != "av2-abc" {
		t.Fatalf("autov2 = %q, want enriched from the matching file", res.autov2)
	}
}

func TestMatcherNotFoundIsUnmatched(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	fr := &fakeReader{} // no entries -> ErrNotFound
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusUnmatched {
		t.Fatalf("status = %q, want unmatched", res.status)
	}
	if res.modelID != nil || res.versionID != nil {
		t.Fatalf("unmatched must carry no ids")
	}
}

func TestMatcherRateLimitedBacksOffThenRetries(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	// Rate-limited twice, then serves the match.
	fr := &fakeReader{byHash: versionMap("abc", version(10, 100, "abc")), failN: 2}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched {
		t.Fatalf("status = %q, want matched after backoff+retry", res.status)
	}
	if fr.calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 rate-limited + 1 success)", fr.calls)
	}
}

func TestMatcherPersistentRateLimitLeavesPendingNoFalseFlag(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	// Always rate-limited (failN exceeds the retry budget).
	fr := &fakeReader{failN: 100}
	sc := matcherScanner(t, root, fr, false, "abc")

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusUnmatchedPending {
		t.Fatalf("status = %q, want unmatched-pending", res.status)
	}
	// A pending file is never a candidate: a full scan confirms zero candidates.
	sc2 := NewScanner(sc.store, fr, Options{ModelRoot: root}, nil)
	sc2.hashFn = func(string) (string, error) { return "abc", nil }
	sc2.waitFn = func(context.Context, time.Duration) {}
	sc2.maxHashRetries = 1
	report, err := sc2.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 0 {
		t.Fatalf("pending file must not be flagged, got %d candidates", len(report.Candidates))
	}
}

func TestMatcherNoRemoteSkipsAPIEntirely(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	pr := &panicReader{}
	sc := matcherScanner(t, root, pr, true, "abc") // NoRemote

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusUnmatched {
		t.Fatalf("offline match status = %q, want unmatched", res.status)
	}
}
