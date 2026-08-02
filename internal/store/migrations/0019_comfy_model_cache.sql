-- 0019_comfy_model_cache: cache the ComfyUI /object_info payload so the
-- workflow resource resolver can answer "is this file in ComfyUI's model dirs?"
-- without a live ComfyUI connection.
--
-- The table stores the FULL object_info JSON (the complete map of node types to
-- their input schemas). Loader node combo choices enumerate the files ComfyUI
-- has installed for each input — we derive per-basename presence from this data
-- at query time, not at insert time, so the cache stays future-proof if new
-- loader node types appear.
--
-- Populated when:
--   (a) a preflight or run fetches /object_info (the data is already in hand)
--   (b) the user clicks a manual "Refresh" button
-- Invalidated when a library scan runs (the local file set changes).

CREATE TABLE IF NOT EXISTS comfy_model_cache (
    id         TEXT PRIMARY KEY DEFAULT 'singleton',
    object_info_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
