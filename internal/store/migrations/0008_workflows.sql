-- Workflow library: locally-curated ComfyUI workflows (Slice A).
--
-- CivitAI does NOT host a per-version workflow .json, so a "golden workflow" is
-- something WE curate and store locally. A workflow is imported (pasted JSON /
-- extracted from a PNG's tEXt chunks) or authored, and may be linked to a civitai
-- model/version by the same nullable integer-id convention local_files uses.
CREATE TABLE workflows (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL,              -- 'api' | 'ui'   (only 'api' is runnable)
  graph TEXT NOT NULL,               -- the workflow JSON as stored
  source TEXT NOT NULL,              -- 'imported' | 'extracted-png' | 'authored'
  model_id INTEGER,                  -- civitai linkage, nullable
  version_id INTEGER,
  base_model TEXT,
  is_golden INTEGER NOT NULL DEFAULT 0,
  resources TEXT,                    -- JSON array of referenced model filenames (may be null)
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX ix_workflows_version ON workflows(version_id);

-- At most ONE golden workflow per version_id (partial unique index).
CREATE UNIQUE INDEX ux_workflows_golden ON workflows(version_id) WHERE is_golden = 1;
