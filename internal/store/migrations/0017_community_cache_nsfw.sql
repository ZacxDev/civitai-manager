-- 0017_community_cache_nsfw: put the CivitAI `nsfw` BROWSING LEVEL into the
-- community_cache key.
--
-- WHY THIS EXISTS AT ALL
-- ---------------------------------------------------------------------------
-- Until now handleModelCommunity fetched /api/v1/images with NO `nsfw` param.
-- Live-probed 2026-07-30 against modelVersionId=3112728 (limit=20):
--
--     (absent)      -> nsfwLevel None x20     <- SFW ONLY
--     nsfw=None     -> None x20
--     nsfw=Soft     -> Soft 16, None 4
--     nsfw=Mature   -> Mature 15, Soft 3, X 1, None 1
--     nsfw=X        -> X 17, Mature 3         <- drops SFW entirely
--     nsfw=true     -> X 17, Mature 3
--     nsfw=false    -> None x20
--     nsfw=bogus    -> HTTP 400               <- this one fails LOUDLY
--
-- So `nsfw` is a BROWSING LEVEL, not a ceiling, and omitting it is equivalent to
-- asking for SFW only. The community feed therefore never showed NSFW posts.
--
-- WHY THE CACHE HAD TO BE TOUCHED
-- ---------------------------------------------------------------------------
-- Every row already in community_cache is a response body captured under the OLD
-- (no-param) shape -- i.e. SFW-only JSON. The cache is served first and, on a
-- fetch failure, is served STALE forever. Left alone, a previously-visited model
-- would keep rendering the old SFW-only feed after the fix, and the fix would
-- look like it did not work.
--
-- Both halves are handled here, structurally rather than by a one-shot cleanup:
--
--   1. INVALIDATE -- the table is dropped and recreated, so not one pre-fix body
--      survives. This is a fail-open image-feed cache, never a system of record
--      (see 0010), so discarding it costs at most one refetch per model version.
--
--   2. RE-KEY -- `nsfw` joins the primary key. A body fetched at one browsing
--      level can no longer be served for another, so changing the level in code
--      is SELF-INVALIDATING and never needs a migration like this again. That is
--      the whole reason this is not just a DELETE.
DROP TABLE IF EXISTS community_cache;

CREATE TABLE community_cache (
    model_id   INTEGER NOT NULL,
    version_id INTEGER NOT NULL,
    -- The exact CivitAI `nsfw` query-param value the body was fetched under
    -- (today always the compile-time constant web.communityImagesNSFWLevel).
    nsfw       TEXT    NOT NULL,
    raw        BLOB    NOT NULL,
    fetched_at TEXT    NOT NULL,
    PRIMARY KEY (model_id, version_id, nsfw)
);
