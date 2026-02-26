-- +goose Up

-- Organizations (tenants)
CREATE TABLE orgs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    is_personal INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Users
CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL REFERENCES orgs(id),
    username       TEXT NOT NULL UNIQUE,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    email          TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    is_admin       INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_users_org_id ON users(org_id);

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
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires_at ON user_sessions(expires_at);

-- Registered workers
CREATE TABLE workers (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    name          TEXT NOT NULL,
    hostname      TEXT NOT NULL DEFAULT '',
    os            TEXT NOT NULL DEFAULT '',
    arch          TEXT NOT NULL DEFAULT '',
    auth_token    TEXT NOT NULL UNIQUE,
    registered_by TEXT NOT NULL REFERENCES users(id),
    share_mode    INTEGER NOT NULL DEFAULT 1,
    status        INTEGER NOT NULL DEFAULT 1,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at  DATETIME,
    UNIQUE(org_id, name)
);
CREATE INDEX idx_workers_org_id ON workers(org_id);

-- Worker shares (for share_mode = 'members')
CREATE TABLE worker_shares (
    worker_id  TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (worker_id, user_id)
);
CREATE INDEX idx_worker_shares_user_id ON worker_shares(user_id);

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
    hostname    TEXT NOT NULL DEFAULT '',
    os          TEXT NOT NULL DEFAULT '',
    arch        TEXT NOT NULL DEFAULT '',
    version     TEXT NOT NULL DEFAULT '',
    status      INTEGER NOT NULL DEFAULT 1,
    worker_id   TEXT REFERENCES workers(id) ON DELETE SET NULL,
    approved_by TEXT REFERENCES users(id),
    expires_at  DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_worker_registrations_status ON worker_registrations(status);

-- Coding workspaces
CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    created_by  TEXT NOT NULL REFERENCES users(id),
    title       TEXT NOT NULL DEFAULT '',
    share_mode       INTEGER NOT NULL DEFAULT 1,
    is_deleted       INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_workspaces_org_id ON workspaces(org_id);
CREATE INDEX idx_workspaces_created_by ON workspaces(created_by);

-- Workspace shares (for share_mode = 'members')
CREATE TABLE workspace_shares (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX idx_workspace_shares_user_id ON workspace_shares(user_id);

-- Agents (1:N per workspace)
CREATE TABLE agents (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    worker_id        TEXT NOT NULL REFERENCES workers(id),
    working_dir      TEXT NOT NULL DEFAULT '',
    home_dir                 TEXT    NOT NULL DEFAULT '',
    plan_file_path           TEXT    NOT NULL DEFAULT '',
    plan_content             BLOB    NOT NULL DEFAULT '',
    plan_content_compression INTEGER NOT NULL DEFAULT 0,
    title            TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT 'opus',
    system_prompt    TEXT NOT NULL DEFAULT '',
    agent_session_id TEXT NOT NULL DEFAULT '',
    permission_mode  TEXT NOT NULL DEFAULT 'default',
    effort           TEXT NOT NULL DEFAULT 'high',
    status           INTEGER NOT NULL DEFAULT 1,
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at        DATETIME
);
CREATE INDEX idx_agents_workspace_id ON agents(workspace_id);
CREATE INDEX idx_agents_worker_id ON agents(worker_id);

-- Messages (verbatim storage, per agent)
CREATE TABLE messages (
    id                  TEXT PRIMARY KEY,
    agent_id            TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    seq                 INTEGER NOT NULL,
    role                INTEGER NOT NULL,
    content             BLOB NOT NULL,
    content_compression INTEGER NOT NULL,
    delivery_error      TEXT NOT NULL DEFAULT '',
    thread_id           TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          DATETIME,
    UNIQUE(agent_id, seq)
);
CREATE INDEX idx_messages_agent_id_seq ON messages(agent_id, seq);

-- Email verification tokens
CREATE TABLE email_verifications (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token      TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_email_verifications_token ON email_verifications(token);

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

-- Workspace tabs (unified ordering for agents + terminals)
CREATE TABLE workspace_tabs (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    position     TEXT NOT NULL,
    tile_id      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, tab_type, tab_id)
);
CREATE INDEX idx_workspace_tabs_workspace ON workspace_tabs(workspace_id);

-- Workspace tiling layouts (JSON tree per workspace)
CREATE TABLE workspace_layouts (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    layout_json  TEXT NOT NULL DEFAULT '{}',
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workspace_id)
);

-- Per-tile active tab tracking
CREATE TABLE workspace_tile_active_tabs (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tile_id      TEXT NOT NULL,
    tab_type     INTEGER NOT NULL DEFAULT 0,
    tab_id       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, tile_id)
);

-- Worktrees created by LeapMux (for lifecycle tracking)
CREATE TABLE worktrees (
    id              TEXT PRIMARY KEY,
    worker_id       TEXT NOT NULL REFERENCES workers(id),
    worktree_path   TEXT NOT NULL,
    repo_root       TEXT NOT NULL,
    branch_name     TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX idx_worktrees_worker_path ON worktrees(worker_id, worktree_path);

-- Junction: which tabs use which LeapMux-created worktree
CREATE TABLE worktree_tabs (
    worktree_id  TEXT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    PRIMARY KEY (worktree_id, tab_type, tab_id)
);
CREATE INDEX idx_worktree_tabs_tab ON worktree_tabs(tab_type, tab_id);

-- User preferences
CREATE TABLE user_preferences (
    user_id                  TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    theme                    TEXT NOT NULL DEFAULT '',
    terminal_theme           TEXT NOT NULL DEFAULT '',
    ui_font_custom_enabled   INTEGER NOT NULL DEFAULT 0,
    mono_font_custom_enabled INTEGER NOT NULL DEFAULT 0,
    ui_fonts                 TEXT NOT NULL DEFAULT '[]',
    mono_fonts               TEXT NOT NULL DEFAULT '[]',
    diff_view                INTEGER NOT NULL DEFAULT 0,
    turn_end_sound           INTEGER NOT NULL DEFAULT 0,
    turn_end_sound_volume    INTEGER NOT NULL DEFAULT 100,
    updated_at               DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- System settings (single-row)
CREATE TABLE system_settings (
    id                              INTEGER PRIMARY KEY CHECK (id = 1),
    signup_enabled                  INTEGER NOT NULL DEFAULT 0,
    email_verification_required     INTEGER NOT NULL DEFAULT 0,
    smtp_host                       TEXT NOT NULL DEFAULT '',
    smtp_port                       INTEGER NOT NULL DEFAULT 587,
    smtp_username                   TEXT NOT NULL DEFAULT '',
    smtp_password                   TEXT NOT NULL DEFAULT '',
    smtp_from_address               TEXT NOT NULL DEFAULT '',
    smtp_use_tls                    INTEGER NOT NULL DEFAULT 1,
    api_timeout_seconds             INTEGER NOT NULL DEFAULT 10,
    agent_startup_timeout_seconds   INTEGER NOT NULL DEFAULT 30,
    worktree_create_timeout_seconds INTEGER NOT NULL DEFAULT 60,
    updated_at                      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Pending control requests (survive hub restarts)
CREATE TABLE control_requests (
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    payload    BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (agent_id, request_id)
);

-- Insert default system settings row
INSERT INTO system_settings (id) VALUES (1);

-- +goose Down
DROP TABLE IF EXISTS control_requests;
DROP TABLE IF EXISTS system_settings;
DROP TABLE IF EXISTS user_preferences;
DROP TABLE IF EXISTS worktree_tabs;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS email_verifications;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS workspace_tile_active_tabs;
DROP TABLE IF EXISTS workspace_layouts;
DROP TABLE IF EXISTS workspace_tabs;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspace_shares;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registrations;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS worker_shares;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
