-- 0016_generation_batches.sql
--
-- Batch identity on generations: which "Queue ×N" click a captured run belongs to.
--
-- N sequential runs produce N rows, and without a marker the outputs rail becomes
-- undifferentiated noise (twelve near-identical thumbnails from one click). These
-- columns are ALL NULL for an ordinary single run, so every existing row and every
-- existing query is unaffected.
--
--   batch_id    — opaque per-batch id (comfy.NewID()), NULL for a single run.
--                 A TEXT id and NOT a run_batches table: the batch job is an
--                 in-memory singleton that dies with the process, so a durable
--                 batch row could be left permanently "running" after a crash — a
--                 row that lies, which is the failure shape this repo already paid
--                 for with the comfyext zombie ping. The gallery needs identity and
--                 ordering only, and both live here.
--                 UNTRUSTED WHEN IT ARRIVES IN A URL: it is validated as a bare
--                 [A-Za-z0-9_-]{1,64} before reaching a query, and an unknown id
--                 returns zero rows rather than an error.
--   batch_index — 1-based position within the batch.
--   batch_total — N as requested at batch start, so "3 of 8" survives a halt: a
--                 batch that fails at item 3 never captures items 4..8, and
--                 counting the captured rows would then report the batch as "3 of
--                 3" and hide that five runs were never made.
--
-- No FK: a batch has no row of its own to reference (see above). The index is on
-- (batch_id, batch_index) so the batch view is one ordered range scan; NULL
-- batch_ids cost one index entry each and are never scanned by it (every batch
-- query binds a non-empty id).
ALTER TABLE generations ADD COLUMN batch_id    TEXT;
ALTER TABLE generations ADD COLUMN batch_index INTEGER;
ALTER TABLE generations ADD COLUMN batch_total INTEGER;

CREATE INDEX ix_generations_batch ON generations(batch_id, batch_index);
