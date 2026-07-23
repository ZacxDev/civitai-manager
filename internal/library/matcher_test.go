package library

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// matcherScanner builds a scanner wired for matcher unit tests with a fixed hash.
func matcherScanner(t *testing.T, root string, reader civitai.Reader, noRemote bool, fixedHash string) *Scanner {
	t.Helper()
	sc := NewScanner(newTestStore(t), reader, Options{ModelRoot: root, NoRemote: noRemote}, nil)
	sc.hashFn = func(string) (string, error) { return fixedHash, nil }
	return sc
}

// resolveLocalMatch is the network-free half of matching (sidecar short-circuit +
// offline handling); when it cannot settle a file it reports needsRemote so the
// file's SHA joins the single batch by-hash lookup. These tests pin that split.

func TestResolveLocalMatchValidSidecarShortCircuits(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	// Civitai-Helper sidecar: model-version JSON with id (version) + modelId AND
	// the file's declared SHA256, which must match this file's bytes.
	writeFile(t, filepath.Join(root, "m.civitai.info"),
		`{"id": 555, "modelId": 42, "files": [{"hashes": {"SHA256": "DEADBEEF", "AutoV2": "av2x"}}]}`)

	fr := &fakeReader{}
	sc := matcherScanner(t, root, fr, false, "deadbeef")

	res, needsRemote := sc.resolveLocalMatch(model, "deadbeef")
	if needsRemote {
		t.Fatal("a hash-verified sidecar must resolve locally, not need the remote batch")
	}
	if res.status != store.LocalStatusMatched {
		t.Fatalf("status = %q, want matched", res.status)
	}
	if res.modelID == nil || *res.modelID != 42 || res.versionID == nil || *res.versionID != 555 {
		t.Fatalf("ids = %v/%v, want 42/555", res.modelID, res.versionID)
	}
	if res.autov2 != "av2x" {
		t.Fatalf("autov2 = %q, want av2x enriched from the matching sidecar file", res.autov2)
	}
	if fr.calls != 0 || fr.batchCalls != 0 {
		t.Fatalf("API called (calls=%d batch=%d); a verified sidecar must short-circuit it", fr.calls, fr.batchCalls)
	}
}

// TestResolveLocalMatchSidecarHashMismatchNeedsRemote proves a sidecar whose
// declared SHA256 does NOT match the file's bytes is not trusted: matching falls
// through to the authoritative batch by-hash lookup (needsRemote), so a
// mislabeled/renamed sidecar can never misclassify the file.
func TestResolveLocalMatchSidecarHashMismatchNeedsRemote(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"),
		`{"id": 999, "modelId": 88, "files": [{"hashes": {"SHA256": "cafef00d"}}]}`)

	sc := matcherScanner(t, root, &fakeReader{}, false, "abc")
	res, needsRemote := sc.resolveLocalMatch(model, "abc")
	if !needsRemote {
		t.Fatal("mismatched sidecar must fall through to the remote batch")
	}
	if res.status != "" {
		t.Fatalf("a needsRemote result carries no local status yet, got %q", res.status)
	}
}

// TestResolveLocalMatchSidecarNoHashNeedsRemote proves a sidecar carrying ids but
// NO file hash cannot be trusted to adopt those ids: it needs the remote batch.
func TestResolveLocalMatchSidecarNoHashNeedsRemote(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), `{"id": 555, "modelId": 42}`)

	sc := matcherScanner(t, root, &fakeReader{}, false, "abc")
	if _, needsRemote := sc.resolveLocalMatch(model, "abc"); !needsRemote {
		t.Fatal("hashless sidecar must fall through to the remote batch")
	}
}

// TestResolveLocalMatchEmptySidecarNeedsRemote: an empty/whitespace sidecar is
// ignored and falls through to the remote batch.
func TestResolveLocalMatchEmptySidecarNeedsRemote(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), "   ")

	sc := matcherScanner(t, root, &fakeReader{}, false, "abc")
	if _, needsRemote := sc.resolveLocalMatch(model, "abc"); !needsRemote {
		t.Fatal("empty sidecar must fall through to the remote batch")
	}
}

// TestResolveLocalMatchOfflineHashlessSidecarUnmatched proves that in --no-remote
// mode a hashless (unverifiable) sidecar yields unmatched — never a blind match,
// never a remote call.
func TestResolveLocalMatchOfflineHashlessSidecarUnmatched(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")
	writeFile(t, filepath.Join(root, "m.civitai.info"), `{"id": 555, "modelId": 42}`)

	sc := matcherScanner(t, root, &panicReader{}, true, "abc") // NoRemote
	res, needsRemote := sc.resolveLocalMatch(model, "abc")
	if needsRemote {
		t.Fatal("offline mode must never need the remote batch")
	}
	if res.status != store.LocalStatusUnmatched {
		t.Fatalf("offline hashless sidecar should be unmatched, got %q", res.status)
	}
}

// TestResolveLocalMatchOfflineNoSidecarUnmatched: offline, no sidecar → unmatched,
// no remote.
func TestResolveLocalMatchOfflineNoSidecarUnmatched(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	sc := matcherScanner(t, root, &panicReader{}, true, "abc") // NoRemote
	res, needsRemote := sc.resolveLocalMatch(model, "abc")
	if needsRemote || res.status != store.LocalStatusUnmatched {
		t.Fatalf("offline no-sidecar = %q needsRemote=%v, want unmatched/false", res.status, needsRemote)
	}
}

// TestBuildHashMatchMapDedupLowestVersionID proves duplicate-hash handling: the
// endpoint may return MULTIPLE entries for one hash (a hash shared across
// versions); the map keeps the LOWEST ModelVersionID deterministically, keys
// case-insensitively, and never panics on the dup key.
func TestBuildHashMatchMapDedupLowestVersionID(t *testing.T) {
	matches := []civitai.HashMatch{
		{ModelVersionID: 20, ModelID: 2, Hash: "AABB"},
		{ModelVersionID: 10, ModelID: 2, Hash: "aabb"}, // same hash (lower case), lower version
		{ModelVersionID: 30, ModelID: 3, Hash: "CCDD"},
	}
	m := buildHashMatchMap(matches)
	if len(m) != 2 {
		t.Fatalf("map size = %d, want 2 (dup hash collapsed)", len(m))
	}
	if got := m["AABB"].ModelVersionID; got != 10 {
		t.Errorf("dup hash kept version %d, want the lowest (10)", got)
	}
	if got := m["CCDD"].ModelVersionID; got != 30 {
		t.Errorf("CCDD version = %d, want 30", got)
	}
}

// TestResolveFromBatch covers the three phase-3 outcomes: a hit is definitive
// matched (ids from the match, autov2 unset), a miss is definitive unmatched, and
// a failed batch leaves the file UnmatchedPending (never falsely flagged).
func TestResolveFromBatch(t *testing.T) {
	m := buildHashMatchMap([]civitai.HashMatch{{ModelVersionID: 7, ModelID: 3, Hash: "ABC"}})

	// Hit — lookup is case-insensitive (the SHA is lowercase hex on our side).
	hit := resolveFromBatch("abc", m, nil)
	if hit.status != store.LocalStatusMatched || hit.modelID == nil || *hit.modelID != 3 || hit.versionID == nil || *hit.versionID != 7 {
		t.Fatalf("hit = %+v, want matched 3/7", hit)
	}
	if hit.autov2 != "" {
		t.Errorf("batch match must leave autov2 unset, got %q", hit.autov2)
	}

	// Miss — definitive unmatched.
	if miss := resolveFromBatch("zzz", m, nil); miss.status != store.LocalStatusUnmatched {
		t.Errorf("miss status = %q, want unmatched", miss.status)
	}

	// Failed batch — pending regardless of map contents.
	if pend := resolveFromBatch("abc", m, errors.New("boom")); pend.status != store.LocalStatusUnmatchedPending {
		t.Errorf("failed-batch status = %q, want unmatched-pending", pend.status)
	}
}
