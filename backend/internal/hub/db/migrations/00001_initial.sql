-- +goose Up

-- Organizations (tenants)
CREATE TABLE orgs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    is_personal INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    deleted_at  DATETIME
);
CREATE UNIQUE INDEX idx_orgs_name ON orgs(name) WHERE deleted_at IS NULL;
CREATE INDEX idx_orgs_deleted_at ON orgs(deleted_at) WHERE deleted_at IS NOT NULL;

-- Users
CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL REFERENCES orgs(id),
    username       TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    email                    TEXT NOT NULL DEFAULT '',
    email_verified           INTEGER NOT NULL DEFAULT 0,
    pending_email            TEXT NOT NULL DEFAULT '',
    pending_email_token      TEXT NOT NULL DEFAULT '',
    pending_email_expires_at DATETIME,
    password_set             INTEGER NOT NULL DEFAULT 1,
    is_admin                 INTEGER NOT NULL DEFAULT 0,
    prefs          TEXT NOT NULL DEFAULT '{}',
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    deleted_at     DATETIME
);
CREATE INDEX idx_users_org_id ON users(org_id);
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email != '' AND deleted_at IS NULL;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_users_pending_email_token ON users(pending_email_token) WHERE pending_email_token != '' AND deleted_at IS NULL;

-- Multi-org membership (M:N junction)
CREATE TABLE org_members (
    org_id    TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role                INTEGER NOT NULL DEFAULT 1,
    joined_at           DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX idx_org_members_user_id ON org_members(user_id);

-- Auth sessions
CREATE TABLE user_sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id),
    expires_at      DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_active_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
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
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at  DATETIME,
    public_key    BLOB NOT NULL DEFAULT '',
    mlkem_public_key  BLOB NOT NULL DEFAULT '',
    slhdsa_public_key BLOB NOT NULL DEFAULT '',
    deleted_at    DATETIME
);
CREATE INDEX idx_workers_registered_by ON workers(registered_by);
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at) WHERE deleted_at IS NOT NULL;

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           TEXT PRIMARY KEY,
    worker_id    TEXT NOT NULL REFERENCES workers(id),
    type         INTEGER NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}',
    status       INTEGER NOT NULL DEFAULT 1,
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    delivered_at DATETIME
);
CREATE INDEX idx_worker_notifications_worker_status ON worker_notifications(worker_id, status);

-- Pending worker registrations
CREATE TABLE worker_registrations (
    id          TEXT PRIMARY KEY,
    version     TEXT NOT NULL DEFAULT '',
    public_key  BLOB NOT NULL DEFAULT '',
    mlkem_public_key  BLOB NOT NULL DEFAULT '',
    slhdsa_public_key BLOB NOT NULL DEFAULT '',
    status      INTEGER NOT NULL DEFAULT 1,
    worker_id   TEXT REFERENCES workers(id) ON DELETE SET NULL,
    approved_by TEXT REFERENCES users(id),
    expires_at  DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_worker_registrations_status ON worker_registrations(status);


-- Sidebar sections (per-user organization of sidebar panels)
CREATE TABLE workspace_sections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    position     TEXT NOT NULL,
    section_type INTEGER NOT NULL DEFAULT 1,
    sidebar      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);

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
    granted_by TEXT NOT NULL REFERENCES users(id),
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (worker_id, user_id)
);
CREATE INDEX idx_worker_access_grants_user_id ON worker_access_grants(user_id);

-- Workspaces (hub-owned registry)
CREATE TABLE workspaces (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    title         TEXT NOT NULL DEFAULT '',
    is_deleted    INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    deleted_at    DATETIME
);
CREATE INDEX idx_workspaces_org_owner ON workspaces(org_id, owner_user_id) WHERE is_deleted = 0;
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at) WHERE deleted_at IS NOT NULL;

-- Workspace read-only sharing ACL
CREATE TABLE workspace_access (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id),
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workspace_id, user_id)
);

-- Workspace tabs (IDs + position/tile_id; paths stay on workers)
CREATE TABLE workspace_tabs (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    worker_id    TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    position     TEXT NOT NULL DEFAULT '',
    tile_id      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, tab_type, tab_id)
);
CREATE INDEX idx_workspace_tabs_workspace ON workspace_tabs(workspace_id);
CREATE INDEX idx_workspace_tabs_worker ON workspace_tabs(worker_id);

-- Workspace tiling layouts (JSON tree per workspace)
CREATE TABLE workspace_layouts (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    layout_json  TEXT NOT NULL DEFAULT '{}',
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workspace_id)
);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              TEXT PRIMARY KEY,
    provider_type   TEXT NOT NULL,  -- 'oidc' or 'github'
    name            TEXT NOT NULL,  -- display name
    issuer_url      TEXT NOT NULL DEFAULT '',  -- OIDC issuer (empty for GitHub)
    client_id       TEXT NOT NULL,
    client_secret   BLOB NOT NULL,  -- encrypted with encryption key, AAD: 'oauth_provider:' || id
    scopes          TEXT NOT NULL DEFAULT 'openid profile email',
    trust_email     INTEGER NOT NULL DEFAULT 1,  -- trust provider-reported email as verified
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    provider_subject TEXT NOT NULL,  -- sub claim (OIDC) or user ID (GitHub)
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, provider_id)
);
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id     TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    access_token    BLOB NOT NULL,   -- encrypted, AAD: 'access_token:' || user_id || ':' || provider_id
    refresh_token   BLOB NOT NULL,   -- encrypted, AAD: 'refresh_token:' || user_id || ':' || provider_id
    token_type      TEXT NOT NULL DEFAULT 'Bearer',
    expires_at      DATETIME NOT NULL,
    key_version     INTEGER NOT NULL DEFAULT 1,
    updated_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
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
    expires_at      DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            TEXT PRIMARY KEY,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id),
    provider_subject TEXT NOT NULL,
    email            TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    access_token     BLOB NOT NULL,
    refresh_token    BLOB NOT NULL,
    token_type       TEXT NOT NULL DEFAULT 'Bearer',
    token_expires_at DATETIME NOT NULL,
    key_version      INTEGER NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL DEFAULT '',
    expires_at       DATETIME NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
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
