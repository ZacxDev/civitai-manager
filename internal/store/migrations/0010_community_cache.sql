-- 0010_community_cache: persistent cache of the model page's community-image
-- feed, keyed by (model_id, version_id). raw is the exact SearchImages response
-- body (the /api/v1/images JSON), re-decoded on read. fetched_at drives the
-- caller's freshness/fail-open logic (serve fresh from cache, else fetch, else
-- fall back to the last cached entry). Mirrors model_cache (0007).
CREATE TABLE community_cache (
    model_id   INTEGER NOT NULL,
    version_id INTEGER NOT NULL,
    raw        BLOB    NOT NULL,
    fetched_at TEXT    NOT NULL,
    PRIMARY KEY (model_id, version_id)
);
