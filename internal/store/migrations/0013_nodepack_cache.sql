-- 0013_nodepack_cache: persistent cache of the custom-node ATTRIBUTION indexes,
-- keyed by an opaque source key. raw is the exact upstream response body,
-- re-decoded on read. fetched_at drives the caller's freshness / fail-open logic
-- (serve fresh from cache, else fetch, else fall back to the last cached entry).
-- Mirrors model_cache (0007) and community_cache (0010).
--
--   source     — which index this row holds. Two shapes are stored:
--                  "extension-node-map"  — the static fallback index fetched
--                                          from raw.githubusercontent.com
--                  "registry:<class>"    — one Comfy Registry class lookup
--                                          (api.comfy.org has NO batch endpoint,
--                                          so a workflow costs one request per
--                                          missing class; caching is what keeps
--                                          that civil).
--   raw        — the exact response body. For a registry MISS this is the JSON
--                null literal, so a 404 is cached as a real answer instead of
--                being refetched on every render.
--   fetched_at — RFC3339 UTC fetch time; drives the staleness check.
--
-- Holds only public, read-only third-party index data — no secret, and nothing
-- derived from the user's library.

CREATE TABLE nodepack_cache (
    source     TEXT PRIMARY KEY,
    raw        BLOB NOT NULL,
    fetched_at TEXT NOT NULL
);
