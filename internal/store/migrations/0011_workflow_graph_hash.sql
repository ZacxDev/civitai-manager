-- Add graph_hash to workflows for content-based dedup of imported workflows
-- (Discover D2 — import a CivitAI Workflows-type model into the local library).
--
-- graph_hash is a SHA-256 (hex) over the workflow's CANONICALIZED graph JSON
-- (unmarshal → re-marshal so map keys are sorted and whitespace is normalized),
-- so two workflows that differ only in formatting/key-order share one hash and a
-- re-import is idempotent. Nullable: existing rows stay NULL (no backfill); new
-- inserts across ALL paths (paste / PNG / scan / civitai) populate it going
-- forward so dedup is consistent.
--
-- Deliberately NO UNIQUE constraint: dedup is a lookup-based skip
-- (WorkflowExistsByGraphHash) scoped to the import flow, so a hash collision can
-- never break the existing paste/PNG/scan insert paths (which may legitimately
-- store the same graph twice). The index makes the existence lookup cheap.
ALTER TABLE workflows ADD COLUMN graph_hash TEXT;

CREATE INDEX ix_workflows_graph_hash ON workflows(graph_hash);
