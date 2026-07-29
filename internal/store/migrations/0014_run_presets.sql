-- 0014_run_presets.sql
--
-- Saved per-workflow RUN PRESETS (the run panel's tabs) + preset attribution on
-- generations.
--
-- A preset is a DRAFT the user edits, not a historical record. It stores the full
-- run-parameter set for ONE workflow — the same JSON shape generations.params
-- already uses (runParamsSnapshot), extended with mode_selection and with
-- per-entry drift metadata (see the reconciliation rule in
-- claudedocs/RUN-PRESETS-AND-BATCH-DESIGN.md).
--
--   workflow_id — owning workflow. ON DELETE CASCADE, DELIBERATELY UNLIKE
--                 generations' ON DELETE SET NULL. A generation is a durable
--                 artifact (images on disk) that stays viewable forever behind
--                 snapshot labels; a preset is a POINTER INTO A SPECIFIC GRAPH
--                 (positional widget keys + mode keys) that means nothing without
--                 that graph. An orphaned preset could never be reconciled and
--                 never be run — it could only render as an error.
--   name        — user-authored tab label. UNTRUSTED: escaped at render time.
--                 Blank renders as "Preset N".
--   position    — tab order (0-based, dense but not enforced; ties break by id).
--   graph_hash  — SNAPSHOT of workflows.graph_hash AT SAVE TIME. This is THE
--                 reconciliation key: equal ⇒ the stored positional keys still
--                 mean what they meant. Blank is treated as "cannot prove equal",
--                 exactly like runOptionsFromParams. It is re-stamped ONLY on an
--                 explicit "adopt current graph" save, never silently on a
--                 successful read — silent re-stamping would erase the evidence
--                 of drift for the next open.
--   params      — JSON runParamsSnapshot. NOT NULL, defaults to '{}' so a
--                 corrupt/absent blob degrades to "no stored values" rather than
--                 an error, mirroring parseRunParams.
CREATE TABLE run_presets (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id INTEGER NOT NULL,
  name        TEXT    NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  graph_hash  TEXT    NOT NULL DEFAULT '',
  params      TEXT    NOT NULL DEFAULT '{}',
  created_at  TEXT    NOT NULL,             -- RFC3339 UTC
  updated_at  TEXT    NOT NULL,
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

CREATE INDEX ix_run_presets_workflow ON run_presets(workflow_id, position, id);

-- Preset attribution on generations. Both columns are NULL for a run that did not
-- come from a preset, so every existing row and every existing query is
-- unaffected.
--
--   preset_id   — the preset this run came from. ON DELETE SET NULL, mirroring
--                 workflow_id: deleting a preset must never delete images.
--   preset_name — SNAPSHOT of the preset label at run time, exactly the
--                 workflow_name idiom, so a deleted preset's runs stay labeled.
--
-- SQLite rule: ALTER TABLE ADD COLUMN with a REFERENCES clause is permitted only
-- when the column's default is NULL. It is here (no DEFAULT given). The FK is not
-- applied retroactively to existing rows, which is fine — they are all NULL.
ALTER TABLE generations ADD COLUMN preset_id   INTEGER REFERENCES run_presets(id) ON DELETE SET NULL;
ALTER TABLE generations ADD COLUMN preset_name TEXT;
