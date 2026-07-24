package library

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// matchResult is the outcome of matching one file to a CivitAI version.
type matchResult struct {
	status    string
	modelID   *int
	versionID *int
	autov2    string
}

// resolveLocalMatch resolves a model file WITHOUT any network call. It prefers a
// valid local .civitai.info sidecar (Civitai-Helper compatible), then falls back
// to offline handling; when neither settles the file it reports needsRemote so
// the caller collects the file's SHA for the single batch by-hash lookup.
//
// It replaces the per-file matchFile's steps 1-2 (sidecar short-circuit + offline
// guard); step 3 — the by-hash network call — is no longer per-file: the whole
// library is resolved in one (chunked) POST via Scanner.batchMatch.
func (s *Scanner) resolveLocalMatch(path, sha string) (result matchResult, needsRemote bool) {
	// 1. Civitai-Helper sidecar short-circuit — but only when the sidecar's own
	// declared file hash matches THIS file's bytes. A sidecar whose declared
	// SHA256 does not match the file (a mislabeled/renamed/tampered sidecar), or
	// one that carries no file hash at all, is untrustworthy for adopting a
	// model/version id: we fall through to the authoritative by-hash lookup
	// (offline: recorded unmatched) rather than misclassify the wrong file.
	if info, ok := parseInfoSidecar(sidecarInfoPath(path, s.opts.Extensions)); ok {
		if av2, verified := info.verifyHash(sha); verified {
			return matchResult{
				status:    store.LocalStatusMatched,
				modelID:   ptrIfPos(info.modelID),
				versionID: ptrIfPos(info.versionID),
				autov2:    av2,
			}, false
		}
	}

	// 2. Offline mode: no API calls. Without a sidecar we cannot confirm a match.
	if s.opts.NoRemote || s.reader == nil {
		return matchResult{status: store.LocalStatusUnmatched}, false
	}

	// 3. Needs the authoritative remote lookup — done in one BATCH, not per file.
	return matchResult{}, true
}

// batchMatch resolves every needsRemote prepared file in ONE call:
// reader.GetModelVersionsByHashes (which chunks to <=10k hashes per request and
// merges). It returns a hash->match map for phase-3 resolution, plus any error.
//
// This is the batch replacement for the retired per-file by-hash GET loop and
// its shared 429-cooldown limiter: with a handful of POSTs instead of N GETs,
// pacing/cooldown is unnecessary — the SDK's own bounded retry/backoff handles a
// transient failure, and on a genuine failure the whole batch returns an error so
// the affected files are marked UnmatchedPending (never falsely flagged). A nil
// map with a nil error means "no remote lookup needed" (offline / nothing to
// resolve). ctx cancellation aborts promptly (the SDK honors ctx).
func (s *Scanner) batchMatch(ctx context.Context, prepared []*preparedFile) (map[string]civitai.HashMatch, error) {
	if s.opts.NoRemote || s.reader == nil {
		return nil, nil
	}
	var shas []string
	for _, pf := range prepared {
		if pf != nil && pf.needsRemote {
			shas = append(shas, pf.sha)
		}
	}
	if len(shas) == 0 {
		return nil, nil
	}
	matches, err := s.reader.GetModelVersionsByHashes(ctx, shas)
	if err != nil {
		s.log.Warn("batch by-hash lookup failed; affected files left pending",
			"hashes", len(shas), "err", err)
		return nil, err
	}
	return buildHashMatchMap(matches), nil
}

// resolveFromBatch turns a needsRemote file's SHA into its final match result,
// reading the prefetched batch map. A non-nil batchErr (the batch call failed
// after the SDK's own retries) leaves the file UnmatchedPending so nothing is
// ever falsely flagged; a hit is definitive matched; a miss is definitive
// unmatched (the batch replacement for the old ErrNotFound branch).
func resolveFromBatch(sha string, m map[string]civitai.HashMatch, batchErr error) matchResult {
	if batchErr != nil {
		return matchResult{status: store.LocalStatusUnmatchedPending}
	}
	if hm, ok := m[strings.ToUpper(strings.TrimSpace(sha))]; ok {
		return matchResult{
			status:    store.LocalStatusMatched,
			modelID:   ptrIfPos(hm.ModelID),
			versionID: ptrIfPos(hm.ModelVersionID),
			// AutoV2 is enrichment-only and NOT returned by the batch endpoint; it
			// is left unset for batch-matched files (the app already has the file's
			// SHA256 and never reconstructs AutoV2 from it).
		}
	}
	return matchResult{status: store.LocalStatusUnmatched}
}

// buildHashMatchMap indexes batch results by UPPER-cased hash for O(1) lookup.
//
// The endpoint may return MORE THAN ONE entry for a single hash (a hash shared
// across model versions). To stay deterministic regardless of response order we
// keep the entry with the LOWEST ModelVersionID; duplicate keys never panic.
func buildHashMatchMap(matches []civitai.HashMatch) map[string]civitai.HashMatch {
	m := make(map[string]civitai.HashMatch, len(matches))
	for _, hm := range matches {
		key := strings.ToUpper(strings.TrimSpace(hm.Hash))
		if key == "" {
			continue
		}
		if cur, ok := m[key]; !ok || hm.ModelVersionID < cur.ModelVersionID {
			m[key] = hm
		}
	}
	return m
}

// sidecarInfoPath returns the Civitai-Helper .civitai.info path for a model file
// (the file path with its model extension replaced by ".civitai.info").
func sidecarInfoPath(modelPath string, exts map[string]bool) string {
	ext := filepath.Ext(modelPath)
	if exts[strings.ToLower(ext)] {
		return strings.TrimSuffix(modelPath, ext) + sidecarInfo
	}
	return modelPath + sidecarInfo
}

// sidecarData is the parsed content of a .civitai.info sidecar: the model/version
// ids plus every per-file hash pair the sidecar declares (used to verify the
// sidecar actually describes the local file's bytes before trusting its ids).
type sidecarData struct {
	modelID   int
	versionID int
	files     []sidecarFileHash
}

// sidecarFileHash is one file's declared SHA256 (+ AutoV2) from the sidecar.
type sidecarFileHash struct {
	sha256 string
	autoV2 string
}

// verifyHash reports whether the file whose actual SHA256 is `sha` is one of the
// files this sidecar describes. It returns that file's AutoV2 (for enrichment)
// and verified=true only on a hash match. When the sidecar declares NO file
// SHA256 at all, or none match, it returns verified=false so the caller falls
// through to the authoritative by-hash lookup instead of trusting the ids.
func (d sidecarData) verifyHash(sha string) (autoV2 string, verified bool) {
	for _, f := range d.files {
		if hashutil.Equal(f.sha256, sha) {
			return f.autoV2, true
		}
	}
	return "", false
}

// parseInfoSidecar reads a .civitai.info sidecar and extracts its model/version
// ids and its declared per-file hashes. Civitai-Helper writes the model-version
// API JSON verbatim, so `id` is the version id, `modelId` the model id, and
// `files[].hashes` carries the SHA256/AutoV2 that identify each file. An empty,
// missing, or corrupt file yields ok=false (never a false match).
func parseInfoSidecar(infoPath string) (sidecarData, bool) {
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return sidecarData{}, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return sidecarData{}, false
	}
	var body struct {
		ID      int `json:"id"`
		ModelID int `json:"modelId"`
		Files   []struct {
			Hashes struct {
				SHA256 string `json:"SHA256"`
				AutoV2 string `json:"AutoV2"`
			} `json:"hashes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return sidecarData{}, false
	}
	if body.ID <= 0 && body.ModelID <= 0 {
		return sidecarData{}, false
	}
	out := sidecarData{modelID: body.ModelID, versionID: body.ID}
	for _, f := range body.Files {
		if strings.TrimSpace(f.Hashes.SHA256) != "" {
			out.files = append(out.files, sidecarFileHash{sha256: f.Hashes.SHA256, autoV2: f.Hashes.AutoV2})
		}
	}
	return out, true
}

func ptrIfPos(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}
