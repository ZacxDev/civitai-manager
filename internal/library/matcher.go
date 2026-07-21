package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// matchFile resolves a model file to its CivitAI version. It prefers a valid
// local .civitai.info sidecar (Civitai-Helper compatible) to avoid an API call;
// otherwise it queries by hash, backing off and retrying on rate-limit/transient
// errors. A definitive "not found" is recorded unmatched (never a deletion
// candidate); an unresolved transient failure is recorded unmatched-pending so
// nothing is ever falsely flagged.
func (s *Scanner) matchFile(ctx context.Context, path, sha string) matchResult {
	// 1. Civitai-Helper sidecar short-circuit — but only when the sidecar's own
	// declared file hash matches THIS file's bytes. A sidecar whose declared
	// SHA256 does not match the file (a mislabeled/renamed/tampered sidecar), or
	// one that carries no file hash at all, is untrustworthy for adopting a
	// model/version id: we fall through to the authoritative by-hash lookup
	// (offline: recorded unmatched) rather than misclassify — and potentially
	// quarantine — the wrong file.
	if info, ok := parseInfoSidecar(sidecarInfoPath(path, s.opts.Extensions)); ok {
		if av2, verified := info.verifyHash(sha); verified {
			return matchResult{
				status:    store.LocalStatusMatched,
				modelID:   ptrIfPos(info.modelID),
				versionID: ptrIfPos(info.versionID),
				autov2:    av2,
			}
		}
	}

	// 2. Offline mode: no API calls. Without a sidecar we cannot confirm a match.
	if s.opts.NoRemote || s.reader == nil {
		return matchResult{status: store.LocalStatusUnmatched}
	}

	// 3. By-hash lookup with bounded backoff+retry.
	var backoff time.Duration
	for attempt := 0; attempt <= s.maxHashRetries; attempt++ {
		if ctx.Err() != nil {
			return matchResult{status: store.LocalStatusUnmatchedPending}
		}
		vd, _, err := s.reader.GetModelVersionByHash(ctx, sha)
		if err == nil {
			return matchResult{
				status:    store.LocalStatusMatched,
				modelID:   ptrIfPos(vd.ModelID),
				versionID: ptrIfPos(vd.ID),
				autov2:    autoV2ForHash(vd, sha),
			}
		}
		if errors.Is(err, civitai.ErrNotFound) {
			// Definitive: this file is not on CivitAI. Recorded, surfaced, and
			// NEVER treated as a deletion candidate.
			return matchResult{status: store.LocalStatusUnmatched}
		}
		// Rate-limited or transient network error: back off and retry. Anything
		// else that is not a clean 404 is also treated as transient so we never
		// falsely flag on an ambiguous failure.
		if attempt < s.maxHashRetries {
			backoff = nextBackoff(backoff)
			s.log.Warn("by-hash lookup failed; backing off",
				"path", path, "attempt", attempt+1, "backoff", backoff, "err", err)
			s.waitFn(ctx, backoff)
		}
	}
	// Still failing after retries: leave it pending, do not flag anything.
	return matchResult{status: store.LocalStatusUnmatchedPending}
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

// autoV2ForHash returns the AutoV2 hash of whichever version file matches sha,
// so a matched file can record its AutoV2 without a local recomputation.
func autoV2ForHash(vd *civitai.ModelVersionDetail, sha string) string {
	if vd == nil {
		return ""
	}
	for _, f := range vd.Files {
		if hashutil.Equal(f.Hashes.SHA256, sha) {
			return f.Hashes.AutoV2
		}
	}
	return ""
}

// nextBackoff escalates a transient-error backoff: 0 -> 2m, then doubling up to
// a 30m ceiling. It mirrors the poller's rate-limit policy.
func nextBackoff(cur time.Duration) time.Duration {
	if cur == 0 {
		return 2 * time.Minute
	}
	if cur < 30*time.Minute {
		return cur * 2
	}
	return cur
}

func ptrIfPos(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}
