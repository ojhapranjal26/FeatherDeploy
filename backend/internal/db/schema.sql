-- 001: users
CREATE TABLE IF NOT EXISTS users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    email               TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    name                TEXT    NOT NULL,
    password_hash       TEXT    NOT NULL,
    role                TEXT    NOT NULL DEFAULT 'user' CHECK(role IN ('superadmin','admin','user')),
    github_access_token TEXT    NOT NULL DEFAULT '',
    github_login        TEXT    NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 002: projects
CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL DEFAULT '',
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 003: project_members (per-project RBAC)
CREATE TABLE IF NOT EXISTS project_members (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    role       TEXT    NOT NULL DEFAULT 'viewer' CHECK(role IN ('owner','editor','viewer')),
    PRIMARY KEY (project_id, user_id)
);

-- 004: services
CREATE TABLE IF NOT EXISTS services (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT '',
    deploy_type   TEXT    NOT NULL CHECK(deploy_type IN ('git','artifact','dockerfile')),
    repo_url      TEXT    NOT NULL DEFAULT '',
    repo_branch   TEXT    NOT NULL DEFAULT 'main',
    framework     TEXT    NOT NULL DEFAULT '',
    build_command TEXT    NOT NULL DEFAULT '',
    start_command TEXT    NOT NULL DEFAULT '',
    app_port      INTEGER NOT NULL DEFAULT 8080 CHECK(app_port > 0 AND app_port <= 65535),
    host_port     INTEGER DEFAULT NULL CHECK(host_port IS NULL OR (host_port > 0 AND host_port <= 65535)),
    status        TEXT    NOT NULL DEFAULT 'inactive' CHECK(status IN ('inactive','deploying','running','error','stopped')),
    container_id  TEXT    NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(project_id, name)
);

-- 005: deployments
CREATE TABLE IF NOT EXISTS deployments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id    INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    triggered_by  INTEGER NOT NULL REFERENCES users(id)   ON DELETE RESTRICT,
    deploy_type   TEXT    NOT NULL CHECK(deploy_type IN ('git','artifact','dockerfile')),
    repo_url      TEXT    NOT NULL DEFAULT '',
    commit_sha    TEXT    NOT NULL DEFAULT '',
    artifact_path TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','success','failed')),
    error_message TEXT    NOT NULL DEFAULT '',
    started_at    DATETIME,
    finished_at   DATETIME,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 006: env_variables
CREATE TABLE IF NOT EXISTS env_variables (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key        TEXT    NOT NULL,
    value      TEXT    NOT NULL DEFAULT '',
    is_secret  INTEGER NOT NULL DEFAULT 0 CHECK(is_secret IN (0,1)),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(service_id, key)
);

-- 007: domains
CREATE TABLE IF NOT EXISTS domains (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    domain     TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    tls        INTEGER NOT NULL DEFAULT 0 CHECK(tls IN (0,1)),
    verified   INTEGER NOT NULL DEFAULT 0 CHECK(verified IN (0,1)),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 008: invitations (admin-only invite-based registration)
CREATE TABLE IF NOT EXISTS invitations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    email       TEXT    NOT NULL COLLATE NOCASE,
    token       TEXT    NOT NULL UNIQUE,
    role        TEXT    NOT NULL DEFAULT 'user' CHECK(role IN ('superadmin','admin','user')),
    invited_by  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  DATETIME NOT NULL,
    accepted_at DATETIME,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 009: ssh_keys — per-user SSH key pairs for git clone operations
CREATE TABLE IF NOT EXISTS ssh_keys (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                TEXT    NOT NULL,
    public_key          TEXT    NOT NULL,                     -- OpenSSH authorized_keys format
    encrypted_priv_key  TEXT    NOT NULL DEFAULT '',          -- AES-256-GCM encrypted PEM, empty for imported-only keys
    fingerprint         TEXT    NOT NULL,                     -- SHA256 fingerprint for display
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, fingerprint)
);

-- 010: github_app_config — singleton row for GitHub App integration (superadmin-managed)
CREATE TABLE IF NOT EXISTS github_app_config (
    id              INTEGER PRIMARY KEY CHECK(id = 1),        -- enforces singleton
    app_id          TEXT    NOT NULL,
    app_name        TEXT    NOT NULL DEFAULT '',
    private_key_pem TEXT    NOT NULL,                         -- RSA PEM private key for JWT signing
    installation_id TEXT    NOT NULL DEFAULT '',              -- GitHub App installation ID
    webhook_secret  TEXT    NOT NULL DEFAULT '',
    client_id       TEXT    NOT NULL DEFAULT '',              -- optional: for OAuth flow via App
    client_secret   TEXT    NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_project_members_user    ON project_members(user_id);
CREATE INDEX IF NOT EXISTS idx_services_project        ON services(project_id);
CREATE INDEX IF NOT EXISTS idx_deployments_service     ON deployments(service_id);
CREATE INDEX IF NOT EXISTS idx_env_variables_service   ON env_variables(service_id);
CREATE INDEX IF NOT EXISTS idx_domains_service         ON domains(service_id);
CREATE INDEX IF NOT EXISTS idx_invitations_token       ON invitations(token);
CREATE INDEX IF NOT EXISTS idx_invitations_email       ON invitations(email);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_user           ON ssh_keys(user_id);

-- 011: nodes — worker nodes connected to this main server via mTLS
CREATE TABLE IF NOT EXISTS nodes (
    id               INTEGER  PRIMARY KEY AUTOINCREMENT,
    name             TEXT     NOT NULL UNIQUE COLLATE NOCASE,
    ip               TEXT     NOT NULL,
    port             INTEGER  NOT NULL DEFAULT 7443,           -- mTLS API port on the node
    status           TEXT     NOT NULL DEFAULT 'pending'
                              CHECK(status IN ('pending','connected','offline','error')),
    join_token       TEXT     UNIQUE,                          -- single-use registration token
    token_expires_at DATETIME,
    node_cert_pem    TEXT     NOT NULL DEFAULT '',             -- TLS cert signed by our CA
    rqlite_addr      TEXT     NOT NULL DEFAULT '',             -- host:port for rqlite Raft join
    last_seen        DATETIME,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 012: pki_ca — singleton row for the server CA certificate + key
CREATE TABLE IF NOT EXISTS pki_ca (
    id              INTEGER PRIMARY KEY CHECK(id = 1),
    cert_pem        TEXT    NOT NULL,                          -- PEM-encoded CA certificate
    key_pem         TEXT    NOT NULL,                          -- AES-256-GCM encrypted PEM key
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_nodes_join_token ON nodes(join_token);

-- 013: cluster_state — singleton row tracking which server is the brain (leader)
CREATE TABLE IF NOT EXISTS cluster_state (
    id              INTEGER PRIMARY KEY CHECK(id = 1),
    brain_id        TEXT    NOT NULL DEFAULT 'main',  -- hostname/id of current brain
    brain_addr      TEXT    NOT NULL DEFAULT '',       -- HTTP URL of brain e.g. http://IP:8080
    last_heartbeat  DATETIME,                          -- brain updates this every 10s
    -- brain resource stats (written by brain alongside heartbeat)
    brain_cpu       REAL    NOT NULL DEFAULT 0,        -- percent 0-100
    brain_ram_used  INTEGER NOT NULL DEFAULT 0,        -- bytes
    brain_ram_total INTEGER NOT NULL DEFAULT 0,        -- bytes
    brain_disk_used  INTEGER NOT NULL DEFAULT 0,       -- bytes
    brain_disk_total INTEGER NOT NULL DEFAULT 0,       -- bytes
    -- cluster SSH public key (installed on nodes during join for passwordless access)
    ssh_public_key  TEXT    NOT NULL DEFAULT '',
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
-- Ensure the singleton row always exists
INSERT OR IGNORE INTO cluster_state (id, brain_id, brain_addr) VALUES (1, 'main', '');

-- 014: node stats / SSH columns (ALTER TABLE for existing installs)
-- rqlite/SQLite ALTER TABLE ADD COLUMN is idempotent via error suppression in applySchema
ALTER TABLE nodes ADD COLUMN cpu_usage    REAL    NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN ram_used     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN ram_total    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN disk_used    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN disk_total   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_stats_at DATETIME;
ALTER TABLE nodes ADD COLUMN node_id      TEXT    NOT NULL DEFAULT '';  -- hostname used as election ID
