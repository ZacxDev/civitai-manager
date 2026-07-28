-- 0012_output_generations.sql
-- Durable capture of ComfyUI workflow-run outputs. One run = one `generations`
-- row grouping N `generation_images`. The source workflow may be deleted later,
-- so workflow_id is nullable (ON DELETE SET NULL) and workflow_name/base_model/
-- graph_hash are SNAPSHOTS taken at capture time so an orphaned generation is
-- still fully labeled and viewable forever.
CREATE TABLE generations (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id    INTEGER,                       -- civitai-manager workflow id, nullable
  workflow_name  TEXT NOT NULL DEFAULT '',       -- snapshot of the workflow name at run time
  prompt_id      TEXT NOT NULL,                  -- ComfyUI prompt id (also the on-disk dir name)
  base_model     TEXT,                           -- snapshot of wf.BaseModel (nullable)
  graph_hash     TEXT,                           -- snapshot of the workflow graph_hash (nullable)
  params         TEXT,                           -- JSON snapshot: applied run overrides + resources
  status         TEXT NOT NULL DEFAULT 'ready',  -- 'ready' | 'partial'
  image_count    INTEGER NOT NULL DEFAULT 0,     -- denormalized N for cheap grid rendering
  created_at     TEXT NOT NULL,                  -- RFC3339 UTC
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE SET NULL
);

CREATE INDEX ix_generations_created  ON generations(created_at DESC, id DESC);
CREATE INDEX ix_generations_workflow ON generations(workflow_id);
CREATE INDEX ix_generations_prompt   ON generations(prompt_id);

CREATE TABLE generation_images (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  generation_id  INTEGER NOT NULL,
  idx            INTEGER NOT NULL,               -- 0-based position within the generation
  rel_path       TEXT NOT NULL,                  -- path RELATIVE to the outputs root
  filename       TEXT NOT NULL,                  -- original ComfyUI basename (display/alt)
  content_type   TEXT NOT NULL DEFAULT 'image/png',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  sha256         TEXT,                           -- nullable; reserved for future dedup
  created_at     TEXT NOT NULL,
  FOREIGN KEY (generation_id) REFERENCES generations(id) ON DELETE CASCADE
);

CREATE INDEX ix_generation_images_gen ON generation_images(generation_id, idx);
