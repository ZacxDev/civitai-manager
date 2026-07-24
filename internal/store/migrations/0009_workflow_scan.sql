-- Workflow scanning (Slice A2): a workflow can now enter the library by being
-- DISCOVERED on disk (source='scanned'), keyed by its absolute source_path so a
-- re-scan upserts the same row instead of creating a duplicate. The size_bytes +
-- mtime pair is the incremental-scan cache key (mirrors local_files): an unchanged
-- file is skipped on re-scan without re-parsing its graph.
ALTER TABLE workflows ADD COLUMN source_path TEXT;
ALTER TABLE workflows ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflows ADD COLUMN mtime TEXT;

-- One row per on-disk workflow file. Partial so imported/PNG-extracted/authored
-- workflows (source_path NULL) are unconstrained; the pure-Go modernc driver
-- supports partial indexes (see 0008's ux_workflows_golden).
CREATE UNIQUE INDEX ux_workflows_source_path ON workflows(source_path) WHERE source_path IS NOT NULL;
