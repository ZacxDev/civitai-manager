package hf

import (
	"context"
	"net/url"
	"path"
	"strings"
)

// SourceNote identifies a Match resolved from a URL a WORKFLOW AUTHOR wrote in a
// Note / MarkdownNote node, rather than from the curated table or a repo search.
//
// 🔴 AutoDownloadEligible REFUSES a SourceNote match outright — see the explicit
// guard there, and read that comment before changing either. Fail-closed on
// purpose: the note path has its own, DIFFERENT trust argument (the user is
// looking at a specific file the author named, and approves it by clicking), and
// it must never launder a stranger's URL into the curated set's authority by
// leaking into a caller that asks AutoDownloadEligible.
//
// ⚠ THIS COMMENT USED TO CLAIM the exclusion fell out of the predicate's existing
// conditions — "satisfies NEITHER arm". That was FALSE: ResolveInRepo sets
// RecognizedOrg from the repo owner, so a note pointing at `stabilityai/sd-turbo`
// satisfied the second arm, and only the empty Subdir was holding the gate. The
// guard is now structural, which is why this comment can be trusted.
const SourceNote = "note"

// ParseResolveURL parses a HuggingFace download URL of the canonical form
//
//	https://huggingface.co/{owner}/{name}/resolve/{revision}/{path...}
//
// returning the repo id ("{owner}/{name}"), the revision as written (usually
// "main"), and the file path within the repo. ok is false for anything else —
// another host, another URL shape, a non-https scheme, an empty component.
//
// It is PURE and LOCAL: no network, no client. A renderer may call it to decide
// whether a note URL is even a candidate for auto-install before any egress
// happens.
//
// 🔴 The host test is EXACT `huggingface.co`, deliberately stricter than
// hostAllowed: the CDN hosts hostAllowed admits (*.hf.co) serve signed,
// short-lived object URLs, not /resolve/ paths, so accepting one here would mean
// accepting a shape this function cannot actually interpret. Strictness costs
// nothing — the origin is what an author writes in a note.
//
// The returned revision is NOT trusted to be immutable. "main" is a moving branch,
// so a caller that cares which bytes it gets must re-resolve the repo's current
// commit sha (ResolveInRepo does exactly that) rather than download this URL as
// written.
func ParseResolveURL(raw string) (repo, revision, filePath string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return "", "", "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host != "huggingface.co" {
		return "", "", "", false
	}
	// Split the ESCAPED path: a %2F inside a segment is a filename character, not a
	// separator, and url.URL.Path has already collapsed that distinction.
	segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	// {owner}/{name}/resolve/{revision}/{path…} — at least 5 segments.
	if len(segs) < 5 || segs[2] != "resolve" {
		return "", "", "", false
	}
	owner, name, rev := unescape(segs[0]), unescape(segs[1]), unescape(segs[3])
	fileSegs := make([]string, 0, len(segs)-4)
	for _, s := range segs[4:] {
		fileSegs = append(fileSegs, unescape(s))
	}
	filePath = strings.Join(fileSegs, "/")
	if owner == "" || name == "" || rev == "" || filePath == "" {
		return "", "", "", false
	}
	// A decoded component must not reintroduce path structure or a traversal — the
	// values are concatenated back into API URLs by ResolveInRepo.
	for _, s := range append([]string{owner, name, rev}, fileSegs...) {
		if s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
			return "", "", "", false
		}
	}
	return owner + "/" + name, rev, filePath, true
}

// unescape percent-decodes one path segment, leaving it as-is if it is malformed.
func unescape(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}

// ResolveInRepo resolves a KNOWN repo plus a file BASENAME to a downloadable
// Match: the repo's current commit sha, the file's path, its git-LFS oid (the
// content sha256) and the repo's gated/private state.
//
// It exists because the note path already knows WHICH repo — the author wrote it
// down — so the search half of Resolve (which indexes repo NAMES and therefore
// cannot find a file in a repo whose name shares nothing with it) is both
// unnecessary and useless here.
//
// Two things it buys beyond "we have a URL already":
//
//   - A PINNED REVISION. A note URL almost always says /resolve/main/…, and main
//     is a moving branch: downloading it as written fetches whatever is there now.
//     The returned URL is pinned to the commit sha the repo is at.
//   - A SHA256 TO VERIFY AGAINST, so the streamed bytes are checked before the
//     atomic rename instead of trusted.
//
// Source is SourceNote and Subdir is left EMPTY: the destination for a note
// install comes from the workflow's own inferred model type, not from this
// package's filename-family heuristics. ok is false when the repo has no file
// with that basename.
func (c *Client) ResolveInRepo(ctx context.Context, repo, basename string) (*Match, bool, error) {
	repo = strings.TrimSpace(repo)
	basename = path.Base(strings.TrimSpace(basename))
	if repo == "" || basename == "" || basename == "." || basename == ".." {
		return nil, false, nil
	}
	m, err := c.matchInRepo(ctx, repo, basename)
	if err != nil {
		return nil, false, err
	}
	if m == nil {
		return nil, false, nil
	}
	m.Source = SourceNote
	m.RecognizedOrg = orgRecognized(m.Repo)
	return m, true, nil
}
