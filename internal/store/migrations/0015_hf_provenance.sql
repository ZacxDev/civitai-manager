-- 0015_hf_provenance: where a local file's BYTES came from on HuggingFace.
--
-- Written ONLY for files THIS APP downloaded through the HuggingFace fallback,
-- at the moment the download's sha256 verified against the repo's published LFS
-- oid and the atomic rename succeeded. A row therefore means exactly one thing:
-- "these bytes came from here". There is no weaker tier and no confidence
-- column — a hash MATCH would prove a file is *identical* to one published
-- somewhere, not that it *came from* there, and a source link reads as origin.
-- With that claim excluded there is one kind of row and nothing left for a
-- confidence column to express; an unprovable attribution is unrepresentable by
-- ABSENCE OF THE CONCEPT rather than by a CHECK constraint a later writer could
-- relax. Do not add 'guessed' or 'verified' in any form.
--
-- KEYED BY CONTENT HASH, NOT PATH — three reasons, all load-bearing:
--   1. The claim is about BYTES. Keying it by the bytes' hash makes it survive a
--      rename or a move, which a path key cannot.
--   2. One row covers EVERY copy of the same file, at any number of paths.
--   3. It must be writable at DOWNLOAD time. Nothing on the web install path
--      writes local_files at all (the only download-time writer is the CivitAI
--      queue, internal/queue/queue.go) — an HF-installed file first appears in
--      local_files on the NEXT library scan. A path-keyed column on local_files
--      would have no row to attach to when the provenance is actually known.
--
--   sha256      — the file's content sha256, lowercase hex. Identical to the
--                 git-LFS oid HuggingFace publishes for the file (verified
--                 end-to-end: bytes == tree lfs.oid == pointer "oid sha256:").
--                 NOT the tree's top-level `oid` (the git blob sha1 of the
--                 132-byte pointer) and NOT `xetHash` (a third, unrelated
--                 content-defined-chunking hash that is also 64 hex chars).
--   repo        — the HuggingFace repo id, e.g. "Bingsu/adetailer".
--   path        — path WITHIN the repo (may contain subdirectories).
--   revision    — the concrete commit sha the download URL was pinned to. The
--                 rendered link uses it, so the link keeps pointing at the bytes
--                 we are making a claim about even after the repo's main moves.
--   recorded_at — RFC3339 UTC.
--
-- PK is (sha256, repo, path), not sha256 alone: mirrors are real — the same
-- bytes are legitimately published in several repos, and each is a TRUE
-- statement. A bare sha256 PK would silently overwrite one true attribution with
-- another. Rendering picks one deterministically instead.
--
-- No url column: it is derivable from (repo, revision, path) and would be
-- duplicated state that can drift. No separate index on sha256: SQLite backs the
-- PK with an index whose leftmost column is already sha256.
--
-- Holds only public metadata — a public repo id, a path within it, a commit sha.
-- No token, no secret, no user data.
CREATE TABLE hf_provenance (
    sha256      TEXT NOT NULL,
    repo        TEXT NOT NULL,
    path        TEXT NOT NULL,
    revision    TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY (sha256, repo, path)
);
