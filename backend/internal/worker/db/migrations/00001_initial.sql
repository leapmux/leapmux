-- +goose Up

-- Agents (1:N per workspace; workspace_id is a hub-owned ID, no local FK)
CREATE TABLE agents (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL,
    working_dir      TEXT NOT NULL DEFAULT '',
    home_dir                 TEXT    NOT NULL DEFAULT '',
    plan_file_path           TEXT    NOT NULL DEFAULT '',
    plan_title               TEXT    NOT NULL DEFAULT '',
    title            TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT 'opus',
    system_prompt    TEXT NOT NULL DEFAULT '',
    agent_session_id TEXT NOT NULL DEFAULT '',
    resumed          INTEGER NOT NULL DEFAULT 0,
    permission_mode  TEXT NOT NULL DEFAULT 'default',
    effort           TEXT NOT NULL DEFAULT 'high',
    extra_settings   TEXT NOT NULL DEFAULT '{}',
    available_models         TEXT NOT NULL DEFAULT '[]',
    available_option_groups  TEXT NOT NULL DEFAULT '[]',
    agent_provider   INTEGER NOT NULL DEFAULT 1,
    session_start_seq INTEGER NOT NULL DEFAULT 0,
    startup_error    TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at        DATETIME
);
CREATE INDEX idx_agents_workspace_id ON agents(workspace_id);
CREATE INDEX idx_agents_closed_at ON agents(closed_at) WHERE closed_at IS NOT NULL;

-- Messages (verbatim storage, per agent)
CREATE TABLE messages (
    id                  TEXT PRIMARY KEY,
    agent_id            TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    seq                 INTEGER NOT NULL,
    source              INTEGER NOT NULL,
    content             BLOB NOT NULL,
    content_compression INTEGER NOT NULL,
    depth               INTEGER NOT NULL DEFAULT 0,
    span_id             TEXT NOT NULL DEFAULT '',
    parent_span_id      TEXT NOT NULL DEFAULT '',
    span_type           TEXT NOT NULL DEFAULT '',
    span_lines          TEXT NOT NULL DEFAULT '[]',
    span_color          INTEGER NOT NULL DEFAULT 0,
    delivery_error      TEXT NOT NULL DEFAULT '',
    agent_provider      INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, seq)
);
-- Covers the (agent_id, span_id, source, seq) lookup the to-do extractor
-- uses to find a tool_result's paired tool_use, so SQLite serves the
-- ORDER BY seq ASC LIMIT 1 from the index rather than re-sorting matches.
CREATE INDEX idx_messages_span_id ON messages(agent_id, span_id, source, seq) WHERE span_id <> '';

-- Pending control requests
CREATE TABLE control_requests (
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    payload    BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (agent_id, request_id)
);

-- Scheduled synthetic auto-continue messages
CREATE TABLE auto_continue_schedules (
    agent_id        TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    reason          TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT 'Continue.',
    due_at          DATETIME NOT NULL,
    jitter_ms       INTEGER NOT NULL DEFAULT 0,
    next_backoff_ms INTEGER NOT NULL DEFAULT 0,
    state           TEXT NOT NULL DEFAULT 'active',
    source_payload  BLOB NOT NULL DEFAULT x'',
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (agent_id, reason)
);
CREATE INDEX idx_auto_continue_schedules_state_due_at ON auto_continue_schedules(state, due_at);

-- Worktrees created by LeapMux (for lifecycle tracking)
CREATE TABLE worktrees (
    id              TEXT PRIMARY KEY,
    worktree_path   TEXT NOT NULL,
    repo_root       TEXT NOT NULL,
    branch_name     TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    deleted_at      DATETIME
);
CREATE UNIQUE INDEX idx_worktrees_path ON worktrees(worktree_path) WHERE deleted_at IS NULL;
CREATE INDEX idx_worktrees_deleted_at ON worktrees(deleted_at) WHERE deleted_at IS NOT NULL;

-- Terminals (1:N per workspace; workspace_id is a hub-owned ID, no local FK)
CREATE TABLE terminals (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    working_dir   TEXT NOT NULL DEFAULT '',
    home_dir      TEXT NOT NULL DEFAULT '',
    shell_start_dir TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    cols          INTEGER NOT NULL DEFAULT 80,
    rows          INTEGER NOT NULL DEFAULT 24,
    screen        BLOB NOT NULL DEFAULT x'',
    exit_code     INTEGER NOT NULL DEFAULT 0,
    startup_error TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at     DATETIME
);
CREATE INDEX idx_terminals_workspace_id ON terminals(workspace_id);
CREATE INDEX idx_terminals_closed_at ON terminals(closed_at) WHERE closed_at IS NOT NULL;

-- Junction: which tabs use which LeapMux-created worktree
CREATE TABLE worktree_tabs (
    worktree_id  TEXT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    PRIMARY KEY (worktree_id, tab_type, tab_id)
);
CREATE INDEX idx_worktree_tabs_tab ON worktree_tabs(tab_type, tab_id);

-- File-tab paths kept E2EE on the worker. The hub never sees these
-- rows; clients fetch paths over WatchWorkspacePrivateEvents and
-- GetFileTabPath. tab_id is unique within an org but not across orgs,
-- so the primary key includes org_id.
CREATE TABLE worker_file_tabs (
    org_id       TEXT NOT NULL,
    tab_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    file_path    TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (org_id, tab_id)
);
CREATE INDEX idx_worker_file_tabs_workspace ON worker_file_tabs(org_id, workspace_id);

-- Provider-neutral to-do rows. Populated incrementally by the worker
-- output handler in response to Claude TodoWrite/Task*, Codex
-- turn/plan/updated, and ACP sessionUpdate=plan events so the sidebar
-- survives page reloads and cross-machine opens. row_key is the
-- task_id for Claude Task* (which addresses rows by id) and a synthetic
-- "snap-<seq>" for snapshot-only providers; snapshot replacements
-- delete-all-then-insert-all in a single transaction.
CREATE TABLE agent_todos (
    agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    row_key     TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    task_id     TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL,
    active_form TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status IN ('pending','in_progress','completed')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (agent_id, row_key)
);
CREATE INDEX idx_agent_todos_seq ON agent_todos(agent_id, seq);

-- +goose Down
DROP TABLE IF EXISTS agent_todos;
DROP TABLE IF EXISTS worker_file_tabs;
DROP TABLE IF EXISTS terminals;
DROP TABLE IF EXISTS worktree_tabs;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS auto_continue_schedules;
DROP TABLE IF EXISTS control_requests;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS agents;
