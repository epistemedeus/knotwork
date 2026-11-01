CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    external_id  TEXT UNIQUE,
    scim_id      TEXT UNIQUE,
    username     TEXT NOT NULL,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'member',
    is_active    INTEGER NOT NULL DEFAULT 1,
    public_key   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (username),
    UNIQUE (email)
);

CREATE TABLE IF NOT EXISTS repositories (
    id                 TEXT PRIMARY KEY,
    owner_id           TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    path               TEXT NOT NULL UNIQUE,
    disk_path          TEXT NOT NULL UNIQUE,
    is_private         INTEGER NOT NULL DEFAULT 1,
    default_branch     TEXT NOT NULL DEFAULT 'main',
    protected_branches TEXT NOT NULL DEFAULT 'main,master',
    last_bundle_at     TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE IF NOT EXISTS repo_collaborators (
    repo_id TEXT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role    TEXT NOT NULL,
    PRIMARY KEY (repo_id, user_id)
);

CREATE TABLE IF NOT EXISTS merge_requests (
    id                  TEXT PRIMARY KEY,
    target_repo_id      TEXT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    number              INTEGER NOT NULL,
    author_id           TEXT REFERENCES users (id),
    source_repo_uri     TEXT NOT NULL DEFAULT '',
    source_branch       TEXT NOT NULL,
    target_branch       TEXT NOT NULL,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL DEFAULT 'open',
    is_federated        INTEGER NOT NULL DEFAULT 0,
    federated_actor_id  TEXT NOT NULL DEFAULT '',
    origin_domain       TEXT NOT NULL DEFAULT '',
    head_sha            TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE (target_repo_id, number)
);

CREATE TABLE IF NOT EXISTS review_comments (
    id         TEXT PRIMARY KEY,
    mr_id      TEXT NOT NULL REFERENCES merge_requests (id) ON DELETE CASCADE,
    author_id  TEXT REFERENCES users (id),
    path       TEXT NOT NULL,
    line       INTEGER NOT NULL,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pipelines (
    id                TEXT PRIMARY KEY,
    repo_id           TEXT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    mr_id             TEXT REFERENCES merge_requests (id) ON DELETE CASCADE,
    commit_sha        TEXT NOT NULL,
    ref_name          TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',
    requires_approval INTEGER NOT NULL DEFAULT 0,
    approved_by       TEXT REFERENCES users (id),
    approved_at       TEXT,
    created_at        TEXT NOT NULL,
    finished_at       TEXT
);

CREATE INDEX IF NOT EXISTS idx_pipelines_status ON pipelines (status);

CREATE TABLE IF NOT EXISTS federation_peers (
    id                 TEXT PRIMARY KEY,
    domain             TEXT NOT NULL UNIQUE,
    inbox_url          TEXT NOT NULL DEFAULT '',
    actor_url          TEXT NOT NULL DEFAULT '',
    public_key_pem     TEXT NOT NULL DEFAULT '',
    trust_status       TEXT NOT NULL DEFAULT 'pending',
    allow_federated_ci INTEGER NOT NULL DEFAULT 0,
    last_verified_at   TEXT,
    created_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS device_codes (
    device_code TEXT PRIMARY KEY,
    user_code   TEXT NOT NULL UNIQUE,
    user_id     TEXT REFERENCES users (id),
    status      TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS instance_state (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    setup_completed INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS app_manifests (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    public_key    TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL
);
