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

-- 011: service_stats — periodic container snapshots for historical analysis
CREATE TABLE IF NOT EXISTS service_stats (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id  INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    cpu_pct     REAL    NOT NULL DEFAULT 0,
    mem_used    INTEGER NOT NULL DEFAULT 0,
    mem_total   INTEGER NOT NULL DEFAULT 0,
    mem_pct     REAL    NOT NULL DEFAULT 0,
    net_in      INTEGER NOT NULL DEFAULT 0,
    net_out     INTEGER NOT NULL DEFAULT 0,
    blk_in      INTEGER NOT NULL DEFAULT 0,
    blk_out     INTEGER NOT NULL DEFAULT 0,
    pids        INTEGER NOT NULL DEFAULT 0,
    recorded_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 012: service_stats_monthly — hourly rollup per calendar month (one row per service/year/month/hour)
CREATE TABLE IF NOT EXISTS service_stats_monthly (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id   INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    year         INTEGER NOT NULL,
    month        INTEGER NOT NULL,   -- 1–12
    hour         INTEGER NOT NULL,   -- 0–23 UTC
    cpu_avg      REAL    NOT NULL DEFAULT 0,
    mem_avg      REAL    NOT NULL DEFAULT 0,
    net_in_avg   REAL    NOT NULL DEFAULT 0,
    net_out_avg  REAL    NOT NULL DEFAULT 0,
    blk_in_avg   REAL    NOT NULL DEFAULT 0,
    blk_out_avg  REAL    NOT NULL DEFAULT 0,
    samples      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(service_id, year, month, hour)
);

-- 013: qr_login_tokens — short-lived tokens enabling QR-code-based login on another device
-- Flow: login page calls /init (public) → QR shown → authenticated device opens URL → /approve
-- Tokens are 5-min TTL ephemeral data, safe to DROP and recreate on each startup so the
-- schema stays consistent across upgrades (avoids ALTER COLUMN limitations in SQLite).
DROP TABLE IF EXISTS qr_login_tokens;
CREATE TABLE IF NOT EXISTS qr_login_tokens (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    token         TEXT    NOT NULL UNIQUE,
    user_id       INTEGER REFERENCES users(id) ON DELETE CASCADE,  -- NULL until approved
    status        TEXT    NOT NULL DEFAULT 'pending',  -- pending | approved | expired
    session_token TEXT    NOT NULL DEFAULT '',          -- JWT issued on approval
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    qr_expires_at DATETIME NOT NULL                    -- QR code itself expires after ~5 min
);

-- 014: user_sessions — tracks active JWT sessions for device management
CREATE TABLE IF NOT EXISTS user_sessions (
    id          TEXT     PRIMARY KEY,  -- random hex = jti embedded in JWT
    user_id     INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_agent  TEXT     NOT NULL DEFAULT '',
    ip_address  TEXT     NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at  DATETIME NOT NULL,
    last_seen   DATETIME NOT NULL DEFAULT (datetime('now')),
    revoked     INTEGER  NOT NULL DEFAULT 0
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_project_members_user    ON project_members(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user           ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_service_stats_svc_time  ON service_stats(service_id, recorded_at);
CREATE INDEX IF NOT EXISTS idx_stats_monthly_svc       ON service_stats_monthly(service_id, year, month);
CREATE INDEX IF NOT EXISTS idx_qr_tokens               ON qr_login_tokens(token);
CREATE INDEX IF NOT EXISTS idx_qr_user                 ON qr_login_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_services_project        ON services(project_id);
CREATE INDEX IF NOT EXISTS idx_deployments_service     ON deployments(service_id);
CREATE INDEX IF NOT EXISTS idx_env_variables_service   ON env_variables(service_id);
CREATE INDEX IF NOT EXISTS idx_domains_service         ON domains(service_id);
CREATE INDEX IF NOT EXISTS idx_invitations_token       ON invitations(token);
CREATE INDEX IF NOT EXISTS idx_invitations_email       ON invitations(email);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_user           ON ssh_keys(user_id);

-- Additive column migrations (duplicate-column errors are suppressed by applySchema)
ALTER TABLE deployments ADD COLUMN deploy_log   TEXT    NOT NULL DEFAULT '';
ALTER TABLE services    ADD COLUMN last_image   TEXT    NOT NULL DEFAULT '';
ALTER TABLE services    ADD COLUMN repo_folder  TEXT    NOT NULL DEFAULT '';
ALTER TABLE services    ADD COLUMN auto_deploy  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN branch       TEXT    NOT NULL DEFAULT '';

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

-- 015: system_settings — key-value store for branding and panel-wide configuration
-- Note: column is named setting_key (not key) because key is reserved in rqlite.
CREATE TABLE IF NOT EXISTS system_settings (
    setting_key TEXT     PRIMARY KEY,
    value       TEXT     NOT NULL DEFAULT '',
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
-- Seed default branding keys so callers can always UPDATE instead of INSERT
INSERT OR IGNORE INTO system_settings (setting_key, value) VALUES ('company_name', '');
INSERT OR IGNORE INTO system_settings (setting_key, value) VALUES ('logo_url', '');

-- 016: add deploy_log column to deployments for real deployment output
ALTER TABLE deployments ADD COLUMN deploy_log TEXT NOT NULL DEFAULT '';

-- 017: app_settings — AES-256-GCM encrypted key-value store for sensitive
-- platform configuration (SMTP credentials, GitHub OAuth credentials, etc.)
-- Values are write-only from the API: GET endpoints return status/presence only,
-- never the decrypted value.
CREATE TABLE IF NOT EXISTS app_settings (
    key         TEXT     PRIMARY KEY,
    enc_value   TEXT     NOT NULL DEFAULT '',
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- 018: databases — managed databases per project
-- Postgres and MySQL run as named podman containers (fd-db-{id}) on the
-- project's isolated network (fd-proj-{project_id}). SQLite is provided as a
-- managed volume mounted into sibling service containers.
CREATE TABLE IF NOT EXISTS databases (
    id              INTEGER  PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER  NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT     NOT NULL,
    db_type         TEXT     NOT NULL DEFAULT 'postgres'
                             CHECK(db_type IN ('postgres','mysql','sqlite')),
    db_version      TEXT     NOT NULL DEFAULT 'latest',
    db_name         TEXT     NOT NULL DEFAULT '',
    db_user         TEXT     NOT NULL DEFAULT '',
    db_password     TEXT     NOT NULL DEFAULT '',   -- AES-256-GCM encrypted, fdenc: prefix
    host_port       INTEGER  DEFAULT NULL,          -- non-NULL only for public databases
    status          TEXT     NOT NULL DEFAULT 'stopped'
                             CHECK(status IN ('stopped','starting','running','error')),
    container_id    TEXT     NOT NULL DEFAULT '',
    network_public  INTEGER  NOT NULL DEFAULT 0 CHECK(network_public IN (0,1)),
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_databases_project ON databases(project_id);
