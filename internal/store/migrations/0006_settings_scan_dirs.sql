-- Web-UI persistence added in v0.16:
--
--   settings   — a small key→value store for UI preferences that must survive a
--                restart (currently the NSFW image-display mode: hide|blur|show).
--   scan_dirs  — the set of extra library scan directories the user has SELECTED
--                (via the discovery flow or the server-side directory browser).
--                Persisting the selection means it pre-fills the Library page and
--                survives across scans, fixing the earlier "extra paths not
--                persisted" gap.
--
-- Neither table holds a secret; both are plain local UI state.

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE scan_dirs (
    path     TEXT PRIMARY KEY,
    added_at TEXT NOT NULL
);
