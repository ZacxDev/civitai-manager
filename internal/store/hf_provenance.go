package store

import (
	"strings"
	"time"
)

// HFProvenance is one recorded statement that a file's BYTES came from a
// HuggingFace repo: this app downloaded them from {Repo}@{Revision}/{Path} and
// verified their sha256 against the repo's published LFS oid before the atomic
// rename.
//
// It is keyed by SHA256 rather than by an on-disk path (see migration 0015), so
// it survives a rename, covers every copy of the file, and can be written before
// any library scan has indexed the file into local_files.
//
// There is deliberately no confidence/tier field. A hash match against a remote
// index would prove a file is IDENTICAL to one published somewhere, not that it
// CAME FROM there — and a source link reads as origin. Only downloads we
// performed are recorded, so every row supports the same single claim.
type HFProvenance struct {
	// SHA256 is the file's content hash, lowercase hex (== its git-LFS oid).
	SHA256 string
	// Repo is the HuggingFace repo id, e.g. "Bingsu/adetailer". Untrusted for
	// rendering purposes (it comes from an API response) — escape it.
	Repo string
	// Path is the path WITHIN the repo (may contain subdirectories).
	Path string
	// Revision is the concrete commit sha the download was pinned to.
	Revision string
	// RecordedAt is when the download completed and verified.
	RecordedAt time.Time
}

// FileURL is the human HuggingFace page for this exact file at the PINNED
// revision. It is the only URL form that stays true: it keeps resolving to the
// bytes this row makes a claim about even after the repo's default branch moves
// or the file is replaced.
//
// It is deliberately the /blob/ (page) form and not /resolve/ (the raw download)
// — a chip whose click starts a multi-gigabyte download would be user-hostile.
// Verified live 2026-07-29: /blob/{40-hex commit sha}/{path} returns 200
// text/html with no redirect, for both a root-level and a nested path.
//
// Callers must still pass the result through the web layer's isSafeHTTPURL
// before using it as an href, and must escape the string when rendering.
func (p HFProvenance) FileURL() string {
	repo := strings.Trim(strings.TrimSpace(p.Repo), "/")
	rev := strings.TrimSpace(p.Revision)
	rel := strings.TrimPrefix(strings.TrimSpace(p.Path), "/")
	if repo == "" || rev == "" || rel == "" {
		return ""
	}
	return "https://huggingface.co/" + repo + "/blob/" + rev + "/" + rel
}

// RepoURL is the repo's landing page — the degrade used when FileURL cannot be
// built or does not pass the caller's URL safety check.
func (p HFProvenance) RepoURL() string {
	repo := strings.Trim(strings.TrimSpace(p.Repo), "/")
	if repo == "" {
		return ""
	}
	return "https://huggingface.co/" + repo
}

// UpsertHFProvenance records (or refreshes) one provenance statement. Re-running
// the same download updates recorded_at and the revision rather than duplicating
// the row; a DIFFERENT repo/path for the same bytes is a separate, equally true
// row (mirrors are real).
//
// Every field is required — a partial statement is not a statement. A blank
// sha256/repo/path/revision is refused as a no-op rather than persisting a row
// that could render a link to nowhere.
func (s *Store) UpsertHFProvenance(p HFProvenance) error {
	sha := strings.ToLower(strings.TrimSpace(p.SHA256))
	repo := strings.TrimSpace(p.Repo)
	rel := strings.TrimSpace(p.Path)
	rev := strings.TrimSpace(p.Revision)
	if sha == "" || repo == "" || rel == "" || rev == "" {
		return nil
	}
	at := p.RecordedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO hf_provenance (sha256, repo, path, revision, recorded_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (sha256, repo, path) DO UPDATE SET
			revision = excluded.revision,
			recorded_at = excluded.recorded_at`,
		sha, repo, rel, rev, at.UTC().Format(time.RFC3339))
	return err
}

// HFProvenanceBySHA256 returns every recorded provenance for a content hash,
// most-authoritative first: oldest recorded_at (the first time we fetched these
// bytes), then lexical repo/path so the order is total and stable. An unknown
// hash yields an empty slice and NO error — "we have no statement about these
// bytes" is an answer, not a failure.
func (s *Store) HFProvenanceBySHA256(sha256 string) ([]HFProvenance, error) {
	sha := strings.ToLower(strings.TrimSpace(sha256))
	if sha == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT sha256, repo, path, revision, recorded_at
		FROM hf_provenance WHERE sha256 = ?
		ORDER BY recorded_at ASC, repo ASC, path ASC`, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HFProvenance
	for rows.Next() {
		var (
			p        HFProvenance
			recorded string
		)
		if err := rows.Scan(&p.SHA256, &p.Repo, &p.Path, &p.Revision, &recorded); err != nil {
			return nil, err
		}
		p.RecordedAt = parseTime(recorded)
		out = append(out, p)
	}
	return out, rows.Err()
}

// HFProvenanceForFile returns the ONE provenance statement to render for a
// content hash, or (nil, nil) when there is none. Rendering several sources on
// one chip would read as uncertainty, which is the opposite of what these rows
// mean, so the pick is deterministic: the first row of HFProvenanceBySHA256's
// total ordering.
func (s *Store) HFProvenanceForFile(sha256 string) (*HFProvenance, error) {
	rows, err := s.HFProvenanceBySHA256(sha256)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	p := rows[0]
	return &p, nil
}
