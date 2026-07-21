-- Deduplicate the download queue: a (version_id, file_id) may have at most ONE
-- row in an ACTIVE status. This closes a race where two concurrent enqueues of
-- the same file both passed the non-atomic ActiveQueueItemExists check-then-insert
-- and inserted duplicate rows.
--
-- "Active" is the same set ActiveQueueItemExists guards on: 'queued',
-- 'downloading', 'done'. A 'failed' or 'skipped' row is NOT active and does not
-- block a fresh enqueue, so a retry after a terminal failure still works.

-- Step 1: drop pre-existing active-status duplicates BEFORE creating the unique
-- index (otherwise the CREATE would fail on a populated DB). Keep exactly one row
-- per (version_id, file_id) active group: prefer the most-progressed status
-- (done > downloading > queued), then the lowest id.
DELETE FROM download_queue
WHERE status IN ('queued', 'downloading', 'done')
  AND id NOT IN (
    SELECT id FROM (
      SELECT id,
             ROW_NUMBER() OVER (
               PARTITION BY version_id, file_id
               ORDER BY CASE status
                          WHEN 'done'        THEN 0
                          WHEN 'downloading' THEN 1
                          WHEN 'queued'      THEN 2
                          ELSE 3
                        END,
                        id
             ) AS rn
      FROM download_queue
      WHERE status IN ('queued', 'downloading', 'done')
    )
    WHERE rn = 1
  );

-- Step 2: enforce it going forward. The partial predicate MUST match the one the
-- Enqueue ON CONFLICT target uses.
CREATE UNIQUE INDEX IF NOT EXISTS ux_dlq_active
  ON download_queue (version_id, file_id)
  WHERE status IN ('queued', 'downloading', 'done');
