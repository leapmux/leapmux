-- +goose Up

-- Organizations (tenants)
CREATE TABLE orgs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    is_personal BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_orgs_name ON orgs(name) WHERE deleted_at IS NULL;
CREATE INDEX idx_orgs_deleted_at ON orgs(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orgs_created_at ON orgs(created_at DESC) WHERE deleted_at IS NULL;

-- Users
CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL REFERENCES orgs(id),
    username       TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    email                    TEXT NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    pending_email            TEXT NOT NULL DEFAULT '',
    -- Stored verification code in raw 6-char form (no hyphen), drawn from
    -- verifycode.Charset. Empty when no verification is pending.
    pending_email_token      VARCHAR(16) NOT NULL DEFAULT '',
    pending_email_expires_at TIMESTAMPTZ,
    -- Counts attempts against the active pending_email_token. Reset to 0
    -- whenever a new token is issued; force-expires the token at >5.
    pending_email_attempts   INTEGER NOT NULL DEFAULT 0,
    password_set             BOOLEAN NOT NULL DEFAULT TRUE,
    is_admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    prefs          TEXT NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- High-water mark bumped whenever this user's auth basis is
    -- bulk-revoked. Each bump also records a durable user-token
    -- revocation event so cookie channels and bearer caches die in
    -- lock-step with admin-CLI mutations that run in a separate process.
    tokens_revoked_at        TIMESTAMPTZ,
    -- Monotonic credential epoch. Sessions and bearer rows copy this
    -- value when issued; user-wide revocation increments it so stale
    -- credentials fail without depending on timestamp precision or
    -- cross-host clock agreement.
    auth_generation          BIGINT NOT NULL DEFAULT 0,
    deleted_at     TIMESTAMPTZ
);
CREATE INDEX idx_users_org_id ON users(org_id);
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email != '' AND deleted_at IS NULL;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_users_tokens_revoked_at ON users(tokens_revoked_at) WHERE tokens_revoked_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at DESC) WHERE deleted_at IS NULL;
-- Verification codes are looked up per-user (the session identifies who),
-- so no global token index is needed. Index expiry instead, for cleanup.
CREATE INDEX idx_users_pending_email_expires_at ON users(pending_email_expires_at) WHERE pending_email_expires_at IS NOT NULL;
-- GetFirstAdmin scans for the earliest non-deleted admin (bootstrap path).
-- Partial on (is_admin, deleted_at) keeps the index tiny; indexing on
-- created_at lets the ORDER BY + LIMIT 1 hit the first leaf directly.
CREATE INDEX idx_users_is_admin ON users(created_at) WHERE is_admin AND deleted_at IS NULL;

-- Multi-org membership (M:N junction)
CREATE TABLE org_members (
    org_id    TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role                INTEGER NOT NULL DEFAULT 1,
    joined_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX idx_org_members_user_id ON org_members(user_id);

-- Auth sessions
CREATE TABLE user_sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation BIGINT NOT NULL DEFAULT 0,
    user_agent      TEXT NOT NULL DEFAULT '',
    ip_address      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires_at_last_active ON user_sessions(expires_at, last_active_at);

-- Registered workers
CREATE TABLE workers (
    id            TEXT PRIMARY KEY,
    auth_token    TEXT NOT NULL UNIQUE,
    registered_by TEXT NOT NULL REFERENCES users(id),
    status        INTEGER NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ,
    public_key    BYTEA NOT NULL DEFAULT '',
    mlkem_public_key  BYTEA NOT NULL DEFAULT '',
    slhdsa_public_key BYTEA NOT NULL DEFAULT '',
    -- True for rows created by Server.RegisterWorker, the in-process
    -- bypass the solo launcher uses to bring up the co-located local
    -- worker. The deregister handler refuses these so the user can't
    -- accidentally tear down the bundled desktop worker -- it would just
    -- re-register on next launch and the running process would noisily
    -- exit with "invalid auth token" in between.
    auto_registered BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_workers_registered_by_status_created ON workers(registered_by, status, created_at DESC) WHERE deleted_at IS NULL;
-- Admin status-only listing (ListWorkersAdminByStatus) cannot use the
-- (registered_by, status, created_at) index because registered_by is the
-- leading column.
CREATE INDEX idx_workers_status_created ON workers(status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_workers_created_at ON workers(created_at DESC) WHERE deleted_at IS NULL;

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           TEXT PRIMARY KEY,
    worker_id    TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    type         INTEGER NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}',
    status       INTEGER NOT NULL DEFAULT 1,
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ
);
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
    id          TEXT PRIMARY KEY,
    created_by  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_worker_registration_keys_expires_at ON worker_registration_keys(expires_at);
CREATE INDEX idx_worker_registration_keys_created_by ON worker_registration_keys(created_by);
CREATE INDEX idx_worker_registration_keys_created_at ON worker_registration_keys(created_at);


-- Sidebar sections (per-user organization of sidebar panels)
CREATE TABLE workspace_sections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    position     TEXT NOT NULL,
    section_type INTEGER NOT NULL DEFAULT 1,
    sidebar      INTEGER NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);

-- Workspaces (hub-owned registry) -- must come before workspace_section_items
CREATE TABLE workspaces (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    title         TEXT NOT NULL DEFAULT '',
    is_deleted    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_workspaces_org_owner ON workspaces(org_id, owner_user_id) WHERE is_deleted = FALSE;
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at) WHERE deleted_at IS NOT NULL;

-- Workspace-to-section assignments (per-user)
CREATE TABLE workspace_section_items (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    section_id   TEXT NOT NULL REFERENCES workspace_sections(id) ON DELETE CASCADE,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, workspace_id)
);
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- Cross-user Worker access grants (for workspace sharing)
CREATE TABLE worker_access_grants (
    worker_id  TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (worker_id, user_id)
);
CREATE INDEX idx_worker_access_grants_user_id ON worker_access_grants(user_id);

-- Workspace read-only sharing ACL
CREATE TABLE workspace_access (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (workspace_id, user_id)
);
-- Point index for the cross-org "shared with me" lookup (the grant branch of
-- ListAllAccessibleWorkspaces), which keys on user_id. The PK leads with
-- workspace_id, so it cannot serve a user_id-only probe. (MySQL gets this index
-- automatically from the user_id foreign key; Postgres does not index the
-- referencing column of a foreign key.)
CREATE INDEX idx_workspace_access_user_id ON workspace_access(user_id);

-- See sqlite migration for full rationale on the CRDT schema (op
-- journal, materialized state blob, derived tab views, dedup table,
-- and lifecycle outbox).
CREATE TABLE org_op_batches (
    org_id        TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    physical_ms   BIGINT NOT NULL,
    logical       BIGINT NOT NULL,
    last_logical  BIGINT NOT NULL,
    origin_client TEXT NOT NULL,
    principal_id  TEXT NOT NULL,
    batch_id      TEXT NOT NULL,
    body_hash     BYTEA NOT NULL,
    batch_payload BYTEA NOT NULL,
    op_count      INTEGER NOT NULL CHECK (op_count > 0),
    epoch         BIGINT NOT NULL,
    committed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, physical_ms, logical, origin_client)
);
CREATE UNIQUE INDEX idx_org_op_batches_dedup ON org_op_batches(org_id, batch_id);

CREATE TABLE org_state (
    org_id           TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    state_payload    BYTEA NOT NULL,
    current_epoch    BIGINT NOT NULL DEFAULT 1,
    epoch_started_at TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE workspace_tab_owned (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    worker_id    TEXT NOT NULL,
    tile_id      TEXT NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id)
);
CREATE INDEX idx_workspace_tab_owned_worker    ON workspace_tab_owned(worker_id);
CREATE INDEX idx_workspace_tab_owned_workspace ON workspace_tab_owned(workspace_id);

CREATE TABLE workspace_tab_rendered (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    worker_id    TEXT NOT NULL,
    tile_id      TEXT NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id)
);
CREATE INDEX idx_workspace_tab_rendered_workspace ON workspace_tab_rendered(workspace_id);
-- LocateAccessibleRenderedTab filters on tab_id alone; the PK has tab_id
-- as the trailing column so it is not seekable.
CREATE INDEX idx_workspace_tab_rendered_tab_id ON workspace_tab_rendered(tab_id);

CREATE TABLE org_recent_batch_ids (
    org_id                TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    batch_id              TEXT NOT NULL,
    body_hash             BYTEA NOT NULL,
    principal_id          TEXT NOT NULL,
    canonical_physical_ms BIGINT NOT NULL,
    canonical_logical     BIGINT NOT NULL,
    canonical_client      TEXT NOT NULL,
    op_count              INTEGER NOT NULL CHECK (op_count > 0),
    epoch                 BIGINT NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, batch_id)
);
CREATE INDEX idx_org_recent_batch_ids_expires ON org_recent_batch_ids(expires_at);

CREATE TABLE lifecycle_outbox (
    id          BIGSERIAL PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    op_type     TEXT NOT NULL,
    payload     BYTEA NOT NULL,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at TIMESTAMPTZ
);
CREATE INDEX idx_lifecycle_outbox_pending ON lifecycle_outbox(org_id, id) WHERE consumed_at IS NULL;

CREATE TABLE revocation_events (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('session', 'api_token', 'api_token_rotation', 'delegation_token', 'user_tokens', 'user_info')),
    subject_id TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    revoked_at TIMESTAMPTZ NOT NULL,
    user_auth_generation BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seq BIGINT UNIQUE CHECK (seq IS NULL OR seq > 0),
    published_at TIMESTAMPTZ,
    CHECK ((seq IS NULL) = (published_at IS NULL))
);
CREATE INDEX idx_revocation_events_pending ON revocation_events(created_at, id) WHERE seq IS NULL;
CREATE INDEX idx_revocation_events_published ON revocation_events(published_at, seq) WHERE seq IS NOT NULL;

CREATE TABLE revocation_event_sequence (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    last_seq BIGINT NOT NULL CHECK (last_seq >= 0)
);
INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0);

CREATE TABLE hub_runtime_lease (
    singleton_id     SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
    holder_id        TEXT NOT NULL CHECK (holder_id <> ''),
    cursor_seq       BIGINT NOT NULL CHECK (cursor_seq >= 0),
    lease_expires_at TIMESTAMPTZ NOT NULL
);

-- See sqlite migration for full rationale on api_tokens.
CREATE TABLE api_tokens (
    id                            TEXT PRIMARY KEY,
    user_id                       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_type                   TEXT NOT NULL,
    client_name                   TEXT NOT NULL,
    secret_hash                   BYTEA NOT NULL,
    refresh_hash                  BYTEA,
    previous_refresh_hash         BYTEA,
    previous_refresh_expires_at   TIMESTAMPTZ,
    scope                         TEXT NOT NULL DEFAULT 'remote:*',
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  TIMESTAMPTZ,
    last_rotated_at               TIMESTAMPTZ,
    expires_at                    TIMESTAMPTZ,
    refresh_expires_at            TIMESTAMPTZ,
    revoked_at                    TIMESTAMPTZ
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id, client_type);
CREATE INDEX idx_api_tokens_expires_at ON api_tokens(expires_at);
CREATE INDEX idx_api_tokens_revoked_at ON api_tokens(revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE delegation_tokens (
    id                            TEXT PRIMARY KEY,
    user_id                       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    worker_id                     TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    workspace_id                  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id                      TEXT NOT NULL DEFAULT '',
    terminal_id                   TEXT NOT NULL DEFAULT '',
    issued_for_tab_id             TEXT NOT NULL DEFAULT '',
    issued_for_tab_type           INTEGER NOT NULL DEFAULT 0,
    secret_hash                   BYTEA NOT NULL,
    refresh_hash                  BYTEA,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  TIMESTAMPTZ,
    expires_at                    TIMESTAMPTZ NOT NULL,
    refresh_expires_at            TIMESTAMPTZ,
    revoked_at                    TIMESTAMPTZ
);
CREATE INDEX idx_delegation_tokens_user ON delegation_tokens(user_id);
CREATE INDEX idx_delegation_tokens_worker_agent ON delegation_tokens(worker_id, agent_id);
CREATE INDEX idx_delegation_tokens_workspace ON delegation_tokens(workspace_id);
CREATE INDEX idx_delegation_tokens_revoked_at ON delegation_tokens(revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE device_authorizations (
    device_code           TEXT PRIMARY KEY,
    user_code             TEXT NOT NULL UNIQUE,
    device_name           TEXT NOT NULL DEFAULT '',
    user_id               TEXT REFERENCES users(id) ON DELETE CASCADE,
    approved              INTEGER NOT NULL DEFAULT 0,
    last_polled_at        TIMESTAMPTZ,
    interval_seconds      INTEGER NOT NULL DEFAULT 5,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ
);
CREATE INDEX idx_device_authorizations_expires_at ON device_authorizations(expires_at);

CREATE TABLE cli_authorization_codes (
    code                  TEXT PRIMARY KEY,
    user_id               TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_challenge        TEXT NOT NULL,
    device_name           TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ
);
CREATE INDEX idx_cli_authorization_codes_expires_at ON cli_authorization_codes(expires_at);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              TEXT PRIMARY KEY,
    provider_type   TEXT NOT NULL,
    name            TEXT NOT NULL,
    issuer_url      TEXT NOT NULL DEFAULT '',
    client_id       TEXT NOT NULL,
    client_secret   BYTEA NOT NULL,
    scopes          TEXT NOT NULL DEFAULT 'openid profile email',
    trust_email     BOOLEAN NOT NULL DEFAULT TRUE,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    provider_subject TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, provider_id)
);
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id     TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    access_token    BYTEA NOT NULL,
    refresh_token   BYTEA NOT NULL,
    token_type      TEXT NOT NULL DEFAULT 'Bearer',
    expires_at      TIMESTAMPTZ NOT NULL,
    key_version     INTEGER NOT NULL DEFAULT 1,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, provider_id)
);
CREATE INDEX idx_oauth_tokens_provider_id ON oauth_tokens(provider_id);
CREATE INDEX idx_oauth_tokens_expires_at ON oauth_tokens(expires_at);
CREATE INDEX idx_oauth_tokens_key_version ON oauth_tokens(key_version);

-- Short-lived OAuth state for CSRF + PKCE during auth flow
CREATE TABLE oauth_states (
    state           TEXT PRIMARY KEY,
    provider_id     TEXT NOT NULL REFERENCES oauth_providers(id),
    pkce_verifier   TEXT NOT NULL,
    redirect_uri    TEXT NOT NULL DEFAULT '',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            TEXT PRIMARY KEY,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id),
    provider_subject TEXT NOT NULL,
    email            TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    access_token     BYTEA NOT NULL,
    refresh_token    BYTEA NOT NULL,
    token_type       TEXT NOT NULL DEFAULT 'Bearer',
    token_expires_at TIMESTAMPTZ NOT NULL,
    key_version      INTEGER NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL DEFAULT '',
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
DROP TABLE IF EXISTS workspace_access;
DROP TABLE IF EXISTS worker_access_grants;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registration_keys;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
