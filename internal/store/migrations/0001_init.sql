-- Initial schema for civitai-manager.

CREATE TABLE subscriptions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    kind              TEXT    NOT NULL CHECK (kind IN ('model', 'creator')),
    model_id          INTEGER,
    username          TEXT,
    auto_download     INTEGER NOT NULL DEFAULT 1,
    notify_only       INTEGER NOT NULL DEFAULT 0,
    layout            TEXT    NOT NULL DEFAULT 'default',
    base_model_filter TEXT,
    file_type_pref    TEXT,
    poll_interval_secs INTEGER NOT NULL,
    last_polled_at    TEXT,
    created_at        TEXT    NOT NULL,
    -- A model subscription is keyed by model_id; a creator subscription by
    -- username. Enforce that the right key is present for each kind.
    CHECK ((kind = 'model' AND model_id IS NOT NULL) OR
           (kind = 'creator' AND username IS NOT NULL))
);

-- Prevent duplicate subscriptions to the same target.
CREATE UNIQUE INDEX idx_subscriptions_model
    ON subscriptions (model_id) WHERE model_id IS NOT NULL;
CREATE UNIQUE INDEX idx_subscriptions_creator
    ON subscriptions (username) WHERE username IS NOT NULL;

-- The diff ledger: which version ids have already been observed for a
-- subscription. A version id absent here (for a given subscription) is "new".
CREATE TABLE seen_versions (
    subscription_id INTEGER NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    version_id      INTEGER NOT NULL,
    published_at    TEXT,
    first_seen_at   TEXT    NOT NULL,
    PRIMARY KEY (subscription_id, version_id)
);

CREATE TABLE download_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id INTEGER REFERENCES subscriptions (id) ON DELETE SET NULL,
    model_id        INTEGER NOT NULL,
    version_id      INTEGER NOT NULL,
    file_id         INTEGER NOT NULL,
    file_name       TEXT    NOT NULL,
    download_url    TEXT    NOT NULL,
    dest_path       TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('queued', 'downloading', 'done', 'failed', 'skipped')),
    bytes_done      INTEGER NOT NULL DEFAULT 0,
    size_kb         REAL    NOT NULL DEFAULT 0,
    sha256_expected TEXT,
    sha256_actual   TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE INDEX idx_queue_status ON download_queue (status);
CREATE INDEX idx_queue_version_file ON download_queue (version_id, file_id);

-- Local library index. Populated by `scan` (post-MVP feature); the table and a
-- stub live here now so downloads can register their outputs.
CREATE TABLE local_files (
    path          TEXT PRIMARY KEY,
    sha256        TEXT,
    autov2        TEXT,
    model_id      INTEGER,
    version_id    INTEGER,
    size_bytes    INTEGER,
    is_superseded INTEGER NOT NULL DEFAULT 0,
    matched_at    TEXT
);

-- Activity feed shown in the web UI.
CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ts              TEXT    NOT NULL,
    level           TEXT    NOT NULL,
    kind            TEXT    NOT NULL,
    subscription_id INTEGER,
    model_id        INTEGER,
    version_id      INTEGER,
    message         TEXT    NOT NULL
);

CREATE INDEX idx_events_ts ON events (ts DESC);
