-- +goose Up

-- Use binary collation for case-sensitive string comparison.
-- Username and email case-insensitivity is handled at the application layer.
SET NAMES utf8mb4 COLLATE utf8mb4_bin;

-- Organizations (tenants)
CREATE TABLE orgs (
    id          VARCHAR(255) PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    is_personal BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at  DATETIME(3),
    -- Generated column for partial unique index emulation
    active_name VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN name ELSE NULL END) STORED
);
CREATE UNIQUE INDEX idx_orgs_active_name ON orgs(active_name);
CREATE INDEX idx_orgs_deleted_at ON orgs(deleted_at);
CREATE INDEX idx_orgs_created_at ON orgs(created_at DESC);

-- Users
CREATE TABLE users (
    id             VARCHAR(255) PRIMARY KEY,
    org_id         VARCHAR(255) NOT NULL,
    username       VARCHAR(255) NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL,
    email                    VARCHAR(255) NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    pending_email            VARCHAR(255) NOT NULL DEFAULT '',
    pending_email_token      VARCHAR(255) NOT NULL DEFAULT '',
    pending_email_expires_at DATETIME(3),
    password_set             BOOLEAN NOT NULL DEFAULT TRUE,
    is_admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    prefs          TEXT NOT NULL,
    created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at     DATETIME(3),
    -- Generated columns for partial unique index emulation
    active_username VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN username ELSE NULL END) STORED,
    active_email    VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL AND email != '' THEN email ELSE NULL END) STORED,
    active_pending_email_token VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL AND pending_email_token != '' THEN pending_email_token ELSE NULL END) STORED,
    FOREIGN KEY (org_id) REFERENCES orgs(id)
);
CREATE INDEX idx_users_org_id ON users(org_id);
CREATE UNIQUE INDEX idx_users_active_username ON users(active_username);
CREATE UNIQUE INDEX idx_users_active_email ON users(active_email);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);
CREATE INDEX idx_users_created_at ON users(created_at DESC);
CREATE INDEX idx_users_active_pending_email_token ON users(active_pending_email_token);

-- Multi-org membership (M:N junction)
CREATE TABLE org_members (
    org_id    VARCHAR(255) NOT NULL,
    user_id   VARCHAR(255) NOT NULL,
    role      INT NOT NULL DEFAULT 1,
    joined_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (org_id, user_id),
    FOREIGN KEY (org_id) REFERENCES orgs(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_org_members_user_id ON org_members(user_id);

-- Auth sessions
CREATE TABLE user_sessions (
    id              VARCHAR(255) PRIMARY KEY,
    user_id         VARCHAR(255) NOT NULL,
    expires_at      DATETIME(3) NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_active_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    user_agent      TEXT NOT NULL,
    ip_address      VARCHAR(255) NOT NULL DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires_at_last_active ON user_sessions(expires_at, last_active_at);

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
    deleted_at    DATETIME(3),
    FOREIGN KEY (registered_by) REFERENCES users(id)
);
CREATE INDEX idx_workers_registered_by_status_created ON workers(registered_by, status, created_at DESC);
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at);
CREATE INDEX idx_workers_created_at ON workers(created_at DESC);

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
);
CREATE INDEX idx_worker_notifications_worker_status ON worker_notifications(worker_id, status);

-- Pending worker registrations
CREATE TABLE worker_registrations (
    id          VARCHAR(255) PRIMARY KEY,
    version     VARCHAR(255) NOT NULL DEFAULT '',
    public_key  BLOB NOT NULL,
    mlkem_public_key  BLOB NOT NULL,
    slhdsa_public_key BLOB NOT NULL,
    status      INT NOT NULL DEFAULT 1,
    worker_id   VARCHAR(255),
    approved_by VARCHAR(255),
    expires_at  DATETIME(3) NOT NULL,
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE SET NULL,
    FOREIGN KEY (approved_by) REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_worker_registrations_status ON worker_registrations(status);

-- Workspaces (hub-owned registry)
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
);
CREATE INDEX idx_workspaces_org_owner ON workspaces(org_id, owner_user_id);
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at);

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
);
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);

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
);
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- Cross-user Worker access grants (for workspace sharing)
CREATE TABLE worker_access_grants (
    worker_id  VARCHAR(255) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    granted_by VARCHAR(255) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (worker_id, user_id),
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_worker_access_grants_user_id ON worker_access_grants(user_id);

-- Workspace read-only sharing ACL
CREATE TABLE workspace_access (
    workspace_id VARCHAR(255) NOT NULL,
    user_id      VARCHAR(255) NOT NULL,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (workspace_id, user_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Workspace tabs (IDs + position/tile_id; paths stay on workers)
CREATE TABLE workspace_tabs (
    workspace_id VARCHAR(255) NOT NULL,
    worker_id    VARCHAR(255) NOT NULL,
    tab_type     INT NOT NULL,
    tab_id       VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    tile_id      VARCHAR(255) NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, tab_type, tab_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);
CREATE INDEX idx_workspace_tabs_workspace ON workspace_tabs(workspace_id);
CREATE INDEX idx_workspace_tabs_worker ON workspace_tabs(worker_id);

-- Workspace tiling layouts (JSON tree per workspace)
CREATE TABLE workspace_layouts (
    workspace_id VARCHAR(255) NOT NULL,
    layout_json  TEXT NOT NULL,
    updated_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (workspace_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

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
);

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          VARCHAR(255) NOT NULL,
    provider_id      VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, provider_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id) ON DELETE CASCADE
);
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
);
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
);

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
);

-- +goose Down
DROP TABLE IF EXISTS pending_oauth_signups;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_user_links;
DROP TABLE IF EXISTS oauth_providers;
DROP TABLE IF EXISTS workspace_layouts;
DROP TABLE IF EXISTS workspace_tabs;
DROP TABLE IF EXISTS workspace_access;
DROP TABLE IF EXISTS worker_access_grants;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registrations;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
