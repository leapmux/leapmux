-- +goose Up

-- Binary collation is declared per-table (COLLATE=utf8mb4_bin) so every string
-- column -- ids and FK columns alike -- collates byte-wise (case-sensitive),
-- keeping cross-table FK collations consistent. SET NAMES alone cannot do this:
-- it sets the *session* collation, not the column collation the tables inherit.
-- Every CREATE TABLE here and in future migrations MUST carry
-- COLLATE=utf8mb4_bin: a table without it silently inherits the server or
-- database default (typically case-INsensitive), breaking the byte-wise id
-- tiebreak ordering and FK collation consistency. Enforced by
-- TestEveryCreateTableDeclaresBinaryCollation (schema_internal_test.go), which
-- scans every migration file in this directory, and by its live twin
-- TestMySQLBinaryCollationLive (-tags integration). See
-- https://github.com/leapmux/leapmux/issues/300. The database-level default
-- is intentionally left to the operator (the app connects to a pre-created
-- database and owns only its tables).
-- Username/email/display-name case-insensitivity is handled at the application
-- layer (NormalizeUsername/NormalizeEmail lowercases on write and lookup).
--
-- TEXT/BLOB columns intentionally carry NO DEFAULT, unlike their sqlite/
-- postgres twins: MySQL only allows the expression form -- DEFAULT ('') --
-- on TEXT/BLOB, and TiDB (which runs this same migration) rejects that
-- form outright. Every INSERT must therefore supply these columns
-- explicitly.
SET NAMES utf8mb4 COLLATE utf8mb4_bin;

-- Personal organizations: exactly one per user, created with the account,
-- soft-deleted with it. name mirrors the username (renamed together).
CREATE TABLE orgs (
    id          VARCHAR(255) PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at  DATETIME(3),
    -- Generated column for partial unique index emulation
    active_name VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN name ELSE NULL END) STORED
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_orgs_active_name ON orgs(active_name);
CREATE INDEX idx_orgs_deleted_at ON orgs(deleted_at);

-- Users
CREATE TABLE users (
    id             VARCHAR(255) PRIMARY KEY,
    org_id         VARCHAR(255) NOT NULL,
    username       VARCHAR(255) NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL,
    -- Unicode-casefolded (Go strings.ToLower) copy of display_name, maintained on
    -- every write, so admin SearchUsers matches non-ASCII names case-insensitively
    -- and identically across SQLite/Postgres/MySQL (SQLite folds only ASCII, so a
    -- plain LIKE on this pre-folded column keeps the three dialects in agreement).
    display_name_folded      VARCHAR(255) NOT NULL DEFAULT '',
    email                    VARCHAR(255) NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    pending_email            VARCHAR(255) NOT NULL DEFAULT '',
    -- Stored verification code in raw 6-char form (no hyphen), drawn from
    -- verifycode.Charset. Empty when no verification is pending.
    pending_email_token      VARCHAR(16) NOT NULL DEFAULT '',
    pending_email_expires_at DATETIME(3),
    -- Counts attempts against the active pending_email_token. Reset to 0
    -- whenever a new token is issued; force-expires the token at >5.
    pending_email_attempts   INT NOT NULL DEFAULT 0,
    password_set             BOOLEAN NOT NULL DEFAULT TRUE,
    is_admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    prefs          TEXT NOT NULL,
    created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    -- High-water mark bumped whenever this user's auth basis is
    -- bulk-revoked. Each bump also records a durable user-token
    -- revocation event so cookie channels and bearer caches die in
    -- lock-step with admin-CLI mutations that run in a separate process.
    tokens_revoked_at        DATETIME(3),
    -- Monotonic credential epoch. Sessions and bearer rows copy this
    -- value when issued; user-wide revocation increments it so stale
    -- credentials fail without depending on timestamp precision or
    -- cross-host clock agreement.
    auth_generation          BIGINT NOT NULL DEFAULT 0,
    deleted_at     DATETIME(3),
    -- Generated columns for partial unique index emulation
    active_username VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN username ELSE NULL END) STORED,
    active_email    VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL AND email != '' THEN email ELSE NULL END) STORED,
    FOREIGN KEY (org_id) REFERENCES orgs(id)
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_users_org_id ON users(org_id);
CREATE UNIQUE INDEX idx_users_active_username ON users(active_username);
CREATE UNIQUE INDEX idx_users_active_email ON users(active_email);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);
CREATE INDEX idx_users_created_at ON users(created_at DESC, id DESC);
-- Verification codes are looked up per-user (the session identifies who),
-- so no global token index is needed. Index expiry instead, for cleanup.
CREATE INDEX idx_users_pending_email_expires_at ON users(pending_email_expires_at);
-- GetFirstAdmin scans for the earliest non-deleted admin (bootstrap path).
-- MySQL has no partial indexes; the composite indexes deleted_at as a key
-- part (IS NULL is a ref-able key part in MySQL), so the ORDER BY created_at
-- + LIMIT 1 lands on the first live admin directly instead of walking past
-- soft-deleted admins as a residual filter.
CREATE INDEX idx_users_is_admin ON users(is_admin, deleted_at, created_at);

-- Auth sessions
CREATE TABLE user_sessions (
    id              VARCHAR(255) PRIMARY KEY,
    user_id         VARCHAR(255) NOT NULL,
    expires_at      DATETIME(3) NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_active_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    auth_generation BIGINT NOT NULL DEFAULT 0,
    user_agent      TEXT NOT NULL,
    ip_address      VARCHAR(255) NOT NULL DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
-- Serves the plain user_id lookups (prefix) AND the per-user keyset listing
-- ListUserSessionsByUserID (user_id =, ORDER BY last_active_at DESC, id DESC),
-- so that query both seeks and rides the index instead of sorting.
CREATE INDEX idx_user_sessions_user_last_active ON user_sessions(user_id, last_active_at DESC, id DESC);
CREATE INDEX idx_user_sessions_expires_at_last_active ON user_sessions(expires_at, last_active_at);
-- The active-session listing orders by (last_active_at DESC, id DESC) while
-- filtering expires_at with a range predicate. A range on the leading
-- expires_at column means the index above can never provide that order (the
-- engine would top-N sort every page), so the ORDER BY gets its own index and
-- the expiry check runs as a residual filter during the ordered scan.
CREATE INDEX idx_user_sessions_last_active ON user_sessions(last_active_at DESC, id DESC);

-- Registered workers
CREATE TABLE workers (
    id            VARCHAR(255) PRIMARY KEY,
    auth_token    VARCHAR(255) NOT NULL UNIQUE,
    registered_by VARCHAR(255) NOT NULL,
    status        INT NOT NULL DEFAULT 1,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_seen_at  DATETIME(3),
    public_key    BLOB NOT NULL,
    mlkem_public_key  BLOB NOT NULL,
    slhdsa_public_key BLOB NOT NULL,
    -- True for rows created by Server.RegisterWorker, the in-process
    -- bypass the solo launcher uses to bring up the co-located local
    -- worker. The deregister handler refuses these so the user can't
    -- accidentally tear down the bundled desktop worker -- it would just
    -- re-register on next launch and the running process would noisily
    -- exit with "invalid auth token" in between.
    auto_registered BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at    DATETIME(3),
    FOREIGN KEY (registered_by) REFERENCES users(id)
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workers_registered_by_status_created ON workers(registered_by, status, created_at DESC, id DESC);
-- Admin status-only listing (ListWorkersAdminByStatus) cannot use the
-- (registered_by, status, created_at) index because registered_by is the
-- leading column.
CREATE INDEX idx_workers_status_created ON workers(status, created_at DESC, id DESC);
-- Admin per-user listing (ListWorkersAdminByUser: registered_by=?, deleted_at IS
-- NULL, no status filter). The (registered_by, status, created_at, id) composite
-- above can't serve this query's ORDER BY because status sits between the
-- registered_by equality and the created_at sort key. MySQL has no partial
-- indexes, so deleted_at IS NULL is a residual filter rather than an index
-- predicate (mirrors idx_workers_created_at below).
CREATE INDEX idx_workers_registered_by_created ON workers(registered_by, created_at DESC, id DESC);
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at);
CREATE INDEX idx_workers_created_at ON workers(created_at DESC, id DESC);

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           VARCHAR(255) PRIMARY KEY,
    worker_id    VARCHAR(255) NOT NULL,
    type         INT NOT NULL,
    payload      TEXT NOT NULL,
    status       INT NOT NULL DEFAULT 1,
    attempts     INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    delivered_at DATETIME(3),
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_worker_notifications_worker_status ON worker_notifications(worker_id, status);

-- Active worker registration keys.
--
-- Created by an authenticated user via the frontend. The worker presents
-- the row's `id` as a bearer credential (Authorization: Bearer <key>) on
-- the WorkerConnectorService.Register RPC; the hub atomically consumes
-- the row and creates a workers row in one transaction.
--
-- Soft-delete is implemented by setting expires_at to a past instant.
-- The cleanup loop hard-deletes rows whose expires_at is older than the
-- retention cutoff.
CREATE TABLE worker_registration_keys (
    id          VARCHAR(255) PRIMARY KEY,
    created_by  VARCHAR(255) NOT NULL,
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at  DATETIME(3) NOT NULL,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_worker_registration_keys_expires_at ON worker_registration_keys(expires_at);
CREATE INDEX idx_worker_registration_keys_created_by ON worker_registration_keys(created_by);
CREATE INDEX idx_worker_registration_keys_created_at ON worker_registration_keys(created_at DESC, id DESC);

-- Sidebar sections (per-user organization of sidebar panels)
CREATE TABLE workspace_sections (
    id           VARCHAR(255) PRIMARY KEY,
    user_id      VARCHAR(255) NOT NULL,
    name         TEXT NOT NULL,
    position     TEXT NOT NULL,
    section_type INT NOT NULL DEFAULT 1,
    sidebar      INT NOT NULL DEFAULT 1,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);

-- Workspaces (hub-owned registry) -- must come before workspace_section_items
CREATE TABLE workspaces (
    id            VARCHAR(255) PRIMARY KEY,
    org_id        VARCHAR(255) NOT NULL,
    owner_user_id VARCHAR(255) NOT NULL,
    title         TEXT NOT NULL,
    is_deleted    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at    DATETIME(3),
    FOREIGN KEY (org_id) REFERENCES orgs(id),
    FOREIGN KEY (owner_user_id) REFERENCES users(id)
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspaces_org_owner ON workspaces(org_id, owner_user_id);
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at);

-- Workspace-to-section assignments (per-user)
CREATE TABLE workspace_section_items (
    user_id      VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    section_id   VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, workspace_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (section_id) REFERENCES workspace_sections(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- See sqlite migration for full rationale on the CRDT schema.
CREATE TABLE org_op_batches (
    org_id        VARCHAR(255) NOT NULL,
    physical_ms   BIGINT NOT NULL,
    logical       BIGINT NOT NULL,
    last_logical  BIGINT NOT NULL,
    origin_client VARCHAR(255) NOT NULL,
    principal_id  VARCHAR(255) NOT NULL,
    batch_id      VARCHAR(255) NOT NULL,
    body_hash     BLOB NOT NULL,
    batch_payload LONGBLOB NOT NULL,
    op_count      INT NOT NULL CHECK (op_count > 0),
    epoch         BIGINT NOT NULL,
    committed_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (org_id, physical_ms, logical, origin_client),
    FOREIGN KEY (org_id) REFERENCES orgs(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_org_op_batches_dedup ON org_op_batches(org_id, batch_id);

CREATE TABLE org_state (
    org_id           VARCHAR(255) NOT NULL,
    state_payload    LONGBLOB NOT NULL,
    current_epoch    BIGINT NOT NULL DEFAULT 1,
    epoch_started_at DATETIME(6) NOT NULL,
    updated_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (org_id),
    FOREIGN KEY (org_id) REFERENCES orgs(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;

CREATE TABLE workspace_tab_owned (
    org_id       VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    tab_type     INT NOT NULL,
    tab_id       VARCHAR(255) NOT NULL,
    worker_id    VARCHAR(255) NOT NULL,
    tile_id      VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_tab_owned_worker    ON workspace_tab_owned(worker_id);
CREATE INDEX idx_workspace_tab_owned_workspace ON workspace_tab_owned(workspace_id);

CREATE TABLE workspace_tab_rendered (
    org_id       VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    tab_type     INT NOT NULL,
    tab_id       VARCHAR(255) NOT NULL,
    worker_id    VARCHAR(255) NOT NULL,
    tile_id      VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_tab_rendered_workspace ON workspace_tab_rendered(workspace_id);
-- LocateAccessibleRenderedTab filters on tab_id alone; the PK has tab_id
-- as the trailing column so it is not seekable.
CREATE INDEX idx_workspace_tab_rendered_tab_id ON workspace_tab_rendered(tab_id);

CREATE TABLE org_recent_batch_ids (
    org_id                VARCHAR(255) NOT NULL,
    batch_id              VARCHAR(255) NOT NULL,
    body_hash             BLOB NOT NULL,
    principal_id          VARCHAR(255) NOT NULL,
    canonical_physical_ms BIGINT NOT NULL,
    canonical_logical     BIGINT NOT NULL,
    canonical_client      VARCHAR(255) NOT NULL,
    op_count              INT NOT NULL CHECK (op_count > 0),
    epoch                 BIGINT NOT NULL,
    expires_at            DATETIME(6) NOT NULL,
    PRIMARY KEY (org_id, batch_id),
    FOREIGN KEY (org_id) REFERENCES orgs(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_org_recent_batch_ids_expires ON org_recent_batch_ids(expires_at);

CREATE TABLE lifecycle_outbox (
    id          BIGINT PRIMARY KEY AUTO_INCREMENT,
    org_id      VARCHAR(255) NOT NULL,
    op_type     VARCHAR(16) NOT NULL,
    payload     LONGBLOB NOT NULL,
    enqueued_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    consumed_at DATETIME(6),
    FOREIGN KEY (org_id) REFERENCES orgs(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_lifecycle_outbox_pending ON lifecycle_outbox(org_id, id);

CREATE TABLE revocation_events (
    id         VARCHAR(255) PRIMARY KEY,
    kind       VARCHAR(32) NOT NULL CHECK (kind IN ('session', 'api_token', 'api_token_rotation', 'delegation_token', 'user_tokens', 'user_info')),
    subject_id VARCHAR(255) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    revoked_at DATETIME(3) NOT NULL,
    user_auth_generation BIGINT NOT NULL DEFAULT 0,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    seq BIGINT UNIQUE CHECK (seq IS NULL OR seq > 0),
    published_at DATETIME(6),
    CHECK ((seq IS NULL) = (published_at IS NULL))
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_revocation_events_pending ON revocation_events(seq, created_at, id);
CREATE INDEX idx_revocation_events_published ON revocation_events(published_at, seq);

CREATE TABLE revocation_event_sequence (
    id       INT PRIMARY KEY CHECK (id = 1),
    last_seq BIGINT NOT NULL CHECK (last_seq >= 0)
) COLLATE=utf8mb4_bin;
INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0);

CREATE TABLE hub_runtime_lease (
    singleton_id     TINYINT PRIMARY KEY CHECK (singleton_id = 1),
    holder_id        VARCHAR(64) NOT NULL CHECK (holder_id <> ''),
    cursor_seq       BIGINT NOT NULL CHECK (cursor_seq >= 0),
    lease_expires_at DATETIME(6) NOT NULL
) COLLATE=utf8mb4_bin;

-- See sqlite migration for full rationale on api_tokens.
CREATE TABLE api_tokens (
    id                            VARCHAR(255) PRIMARY KEY,
    user_id                       VARCHAR(255) NOT NULL,
    client_type                   VARCHAR(64) NOT NULL,
    client_name                   VARCHAR(255) NOT NULL,
    secret_hash                   VARBINARY(64) NOT NULL,
    refresh_hash                  VARBINARY(64),
    previous_refresh_hash         VARBINARY(64),
    previous_refresh_expires_at   DATETIME(3),
    scope                         VARCHAR(255) NOT NULL DEFAULT 'remote:*',
    created_at                    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME(3),
    last_rotated_at               DATETIME(3),
    expires_at                    DATETIME(3),
    refresh_expires_at            DATETIME(3),
    revoked_at                    DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_api_tokens_revoked_at ON api_tokens(revoked_at);
-- Keyset index for the admin ListAllAPITokens listing. MySQL has no partial
-- indexes, so revoked_at IS NULL stays a residual filter; the (created_at DESC,
-- id DESC) shape still lets the composite ORDER BY ride the index.
CREATE INDEX idx_api_tokens_created_at ON api_tokens(created_at DESC, id DESC);
-- Keyset index for the admin ListAllAPITokensByUser listing (the --user-id
-- path): the leading user_id equality seeks, and (created_at DESC, id DESC)
-- rides the composite ORDER BY. Plain (no partial indexes in MySQL);
-- revoked_at IS NULL stays a residual filter. Mirrors
-- idx_workers_registered_by_created. Its leftmost user_id prefix also
-- enforces the user_id FK (no separate user_id index).
CREATE INDEX idx_api_tokens_user_created ON api_tokens(user_id, created_at DESC, id DESC);

CREATE TABLE delegation_tokens (
    id                            VARCHAR(255) PRIMARY KEY,
    user_id                       VARCHAR(255) NOT NULL,
    worker_id                     VARCHAR(255) NOT NULL,
    workspace_id                  VARCHAR(255) NOT NULL,
    agent_id                      VARCHAR(255) NOT NULL DEFAULT '',
    terminal_id                   VARCHAR(255) NOT NULL DEFAULT '',
    issued_for_tab_id             VARCHAR(255) NOT NULL DEFAULT '',
    issued_for_tab_type           INT NOT NULL DEFAULT 0,
    secret_hash                   VARBINARY(64) NOT NULL,
    refresh_hash                  VARBINARY(64),
    created_at                    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME(3),
    expires_at                    DATETIME(3) NOT NULL,
    refresh_expires_at            DATETIME(3),
    revoked_at                    DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_delegation_tokens_worker_agent ON delegation_tokens(worker_id, agent_id);
CREATE INDEX idx_delegation_tokens_workspace ON delegation_tokens(workspace_id);
CREATE INDEX idx_delegation_tokens_revoked_at ON delegation_tokens(revoked_at);
-- Keyset index for the admin ListAllDelegationTokens listing (see
-- idx_api_tokens_created_at for the rationale).
CREATE INDEX idx_delegation_tokens_created_at ON delegation_tokens(created_at DESC, id DESC);
-- Per-user keyset twin (see idx_api_tokens_user_created). Its leftmost
-- user_id prefix also enforces the user_id FK (no separate user_id index).
CREATE INDEX idx_delegation_tokens_user_created ON delegation_tokens(user_id, created_at DESC, id DESC);
-- Serves the hourly DeleteExpiredDelegationTokensBefore sweep of this
-- high-churn table: seek the expired live rows instead of scanning every
-- live token. Plain (no partial indexes in MySQL); revoked_at IS NULL is a
-- residual filter.
CREATE INDEX idx_delegation_tokens_expires_at ON delegation_tokens(expires_at);

CREATE TABLE device_authorizations (
    device_code           VARCHAR(255) PRIMARY KEY,
    user_code             VARCHAR(64) NOT NULL UNIQUE,
    device_name           VARCHAR(255) NOT NULL DEFAULT '',
    user_id               VARCHAR(255),
    approved              INT NOT NULL DEFAULT 0,        -- 0 pending, 1 approved, 2 denied
    last_polled_at        DATETIME(3),
    interval_seconds      INT NOT NULL DEFAULT 5,
    created_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at            DATETIME(3) NOT NULL,
    consumed_at           DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_device_authorizations_expires_at ON device_authorizations(expires_at);

CREATE TABLE cli_authorization_codes (
    code                  VARCHAR(255) PRIMARY KEY,
    user_id               VARCHAR(255) NOT NULL,
    code_challenge        VARCHAR(255) NOT NULL,
    device_name           VARCHAR(255) NOT NULL DEFAULT '',
    created_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at            DATETIME(3) NOT NULL,
    consumed_at           DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_cli_authorization_codes_expires_at ON cli_authorization_codes(expires_at);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              VARCHAR(255) PRIMARY KEY,
    provider_type   VARCHAR(255) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    issuer_url      TEXT NOT NULL,
    client_id       VARCHAR(255) NOT NULL,
    client_secret   BLOB NOT NULL,
    scopes          TEXT NOT NULL,
    trust_email     BOOLEAN NOT NULL DEFAULT TRUE,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) COLLATE=utf8mb4_bin;

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          VARCHAR(255) NOT NULL,
    provider_id      VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, provider_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         VARCHAR(255) NOT NULL,
    provider_id     VARCHAR(255) NOT NULL,
    access_token    BLOB NOT NULL,
    refresh_token   BLOB NOT NULL,
    token_type      VARCHAR(255) NOT NULL DEFAULT 'Bearer',
    expires_at      DATETIME(3) NOT NULL,
    key_version     BIGINT NOT NULL DEFAULT 1,
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, provider_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_oauth_tokens_provider_id ON oauth_tokens(provider_id);
CREATE INDEX idx_oauth_tokens_expires_at ON oauth_tokens(expires_at);
CREATE INDEX idx_oauth_tokens_key_version ON oauth_tokens(key_version);

-- Short-lived OAuth state for CSRF + PKCE during auth flow
CREATE TABLE oauth_states (
    state           VARCHAR(255) PRIMARY KEY,
    provider_id     VARCHAR(255) NOT NULL,
    pkce_verifier   TEXT NOT NULL,
    redirect_uri    TEXT NOT NULL,
    expires_at      DATETIME(3) NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id)
) COLLATE=utf8mb4_bin;

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            VARCHAR(255) PRIMARY KEY,
    provider_id      VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    email            TEXT NOT NULL,
    display_name     TEXT NOT NULL,
    access_token     BLOB NOT NULL,
    refresh_token    BLOB NOT NULL,
    token_type       VARCHAR(255) NOT NULL DEFAULT 'Bearer',
    token_expires_at DATETIME(3) NOT NULL,
    key_version      BIGINT NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL,
    expires_at       DATETIME(3) NOT NULL,
    created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id)
) COLLATE=utf8mb4_bin;

-- +goose Down
DROP TABLE IF EXISTS cli_authorization_codes;
DROP TABLE IF EXISTS device_authorizations;
DROP TABLE IF EXISTS delegation_tokens;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS hub_runtime_lease;
DROP TABLE IF EXISTS revocation_events;
DROP TABLE IF EXISTS revocation_event_sequence;
DROP TABLE IF EXISTS pending_oauth_signups;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_user_links;
DROP TABLE IF EXISTS oauth_providers;
DROP TABLE IF EXISTS lifecycle_outbox;
DROP TABLE IF EXISTS org_recent_batch_ids;
DROP TABLE IF EXISTS workspace_tab_rendered;
DROP TABLE IF EXISTS workspace_tab_owned;
DROP TABLE IF EXISTS org_state;
DROP TABLE IF EXISTS org_op_batches;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registration_keys;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
