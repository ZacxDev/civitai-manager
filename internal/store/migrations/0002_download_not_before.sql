-- Fleet anti-stampede: a per-instance "not before" gate on download rows.
--
-- When a popular model publishes, every user's poller detects the new version
-- inside roughly the same edge-cache window and they would all start the same
-- download at once. Auto-detected downloads get a random not_before offset so
-- the fleet de-synchronizes its download starts. A NULL value (legacy rows and
-- manual/backfill downloads) means immediately claimable.
ALTER TABLE download_queue ADD COLUMN not_before TEXT;
