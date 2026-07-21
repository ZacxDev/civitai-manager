-- Library management: incremental-scan cache + deletion-candidate flags on the
-- local file index, and the quarantine (soft-delete) ledger.
--
-- The scanner records/analyzes only; quarantine is the sole mover and only acts
-- on explicitly flagged candidates. Nothing here hard-deletes a user file.

-- mtime lets the scanner skip re-hashing a multi-GB file whose size AND
-- modification time are unchanged from the cached row (RFC3339Nano UTC).
ALTER TABLE local_files ADD COLUMN mtime TEXT;

-- status is the match state: 'matched', 'unmatched', 'unmatched-pending'
-- (rate-limited/transient — retry later, never a deletion candidate), or
-- 'broken' (a stray/partial/orphan non-model file). Empty means not yet scanned.
ALTER TABLE local_files ADD COLUMN status TEXT NOT NULL DEFAULT '';

-- candidate_reason is the deletion-candidate flag set by the analyzer:
-- 'superseded', 'duplicate', or 'broken'. Empty means not a candidate.
ALTER TABLE local_files ADD COLUMN candidate_reason TEXT NOT NULL DEFAULT '';

-- kind distinguishes a model-weight file ('model') from a tracked broken
-- non-model file ('sidecar': a stray .part, an empty .civitai.info, an orphan
-- preview) so the analyzer/quarantine treat them correctly.
ALTER TABLE local_files ADD COLUMN kind TEXT NOT NULL DEFAULT 'model';

CREATE INDEX idx_local_files_sha ON local_files (sha256) WHERE sha256 IS NOT NULL AND sha256 <> '';
CREATE INDEX idx_local_files_model ON local_files (model_id) WHERE model_id IS NOT NULL;
CREATE INDEX idx_local_files_candidate ON local_files (candidate_reason) WHERE candidate_reason <> '';

-- A quarantine batch: one soft-delete action moving one or more flagged files
-- (and their sidecars) into a timestamped trash dir with an undo manifest.
CREATE TABLE quarantine_batches (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT    NOT NULL,
    trash_dir   TEXT    NOT NULL,
    manifest    TEXT    NOT NULL,
    reason      TEXT    NOT NULL DEFAULT '',
    restored_at TEXT
);

-- Each moved file within a batch. original_path is the absolute source path the
-- restore returns the file to; trash_path is where it now lives.
CREATE TABLE quarantined_files (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id      INTEGER NOT NULL REFERENCES quarantine_batches (id) ON DELETE CASCADE,
    original_path TEXT    NOT NULL,
    trash_path    TEXT    NOT NULL,
    reason        TEXT    NOT NULL DEFAULT '',
    is_sidecar    INTEGER NOT NULL DEFAULT 0,
    sha256        TEXT,
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    moved_at      TEXT    NOT NULL,
    restored_at   TEXT
);

CREATE INDEX idx_quarantined_files_batch ON quarantined_files (batch_id);
