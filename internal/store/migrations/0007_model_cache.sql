-- Model-detail cache added in v0.19:
--
--   model_cache — a per-model snapshot of the CivitAI GetModel response, keyed by
--                 model_id. The Library "Model files" results view lazy-loads an
--                 enriched card per matched model (name + showcase carousel +
--                 details); caching the raw API body means those cards render from
--                 the local snapshot instead of re-hitting civitai.com on every
--                 re-render or re-scan. Model detail is near-immutable, so the
--                 cache is served with a long TTL and only refetched when stale
--                 (see modelCacheTTL) or missing.
--
--   name       — denormalized model name for cheap listing without decoding raw.
--   raw        — the exact JSON body GetModel returned (re-decodable into a
--                civitai.ModelDetail and parseable for inline showcase images).
--   fetched_at — RFC3339 UTC fetch time; drives the staleness check.
--
-- Holds only public model metadata already shown on the model page; no secret.

CREATE TABLE model_cache (
    model_id   INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    raw        BLOB NOT NULL,
    fetched_at TEXT NOT NULL
);
