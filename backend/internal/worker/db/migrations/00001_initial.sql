-- +goose Up

-- Agents. No workspace_id: which workspace a tab lives in is CRDT state the
-- Hub owns, derived from the tab's tile at projection time. A copy here would
-- be a second source of truth that goes stale on every cross-workspace move,
-- and nothing on the Worker reads it -- every row here belongs to the Worker's
-- single registrant.
CREATE TABLE agents (
    id               TEXT PRIMARY KEY,
    working_dir      TEXT NOT NULL DEFAULT '',
    home_dir                 TEXT    NOT NULL DEFAULT '',
    plan_file_path           TEXT    NOT NULL DEFAULT '',
    plan_title               TEXT    NOT NULL DEFAULT '',
    title            TEXT NOT NULL DEFAULT '',
    -- 1 when nobody CHOSE `title`: the worker picked it from the shared pool,
    -- the model supplied it, or the client sent back the pre-filled suggestion
    -- untouched. Plan-mode auto-rename overwrites a title only while this is 1.
    --
    -- Recorded rather than re-derived. The answer used to come from a regex
    -- over the rendered string (`^Agent [A-Z][A-Za-z]+$`), which cannot tell a
    -- pooled name from one a user typed -- so a user who typed `Agent Bob`, or
    -- who edited the pre-filled `Agent Gabe` to another single word, was
    -- silently overwritten. It also forced every pooled name to keep that
    -- shape, a constraint the tab-names schema had to carry for a regex three
    -- layers away.
    title_auto_generated INTEGER NOT NULL DEFAULT 1,
    agent_session_id TEXT NOT NULL DEFAULT '',
    resumed          INTEGER NOT NULL DEFAULT 0,
    -- options: chosen option values keyed by option-group id (model, effort,
    -- permissionMode, and provider-specific axes), as a JSON object.
    options          TEXT NOT NULL DEFAULT '{}',
    -- option_groups: cached catalog of every configuration axis reported by the
    -- agent process (model/effort/permission/etc.), as a JSON array.
    option_groups    TEXT NOT NULL DEFAULT '[]',
    agent_provider   INTEGER NOT NULL DEFAULT 1,
    -- Subagent linkage. parent_agent_id is set ONLY for virtual child agents
    -- (subagent transcripts fed by the parent provider's process; they never
    -- own a process). spawn_span_id is the tool_use span in the PARENT
    -- transcript that spawned this child.
    parent_agent_id  TEXT REFERENCES agents(id) ON DELETE CASCADE,
    spawn_span_id    TEXT NOT NULL DEFAULT '',
    session_start_seq INTEGER NOT NULL DEFAULT 0,
    -- Per-agent monotonic message-seq high-water mark. Message seqs are allocated as
    -- (message_seq_hwm + 1) rather than (MAX(live seq) + 1), so a deleted tail seq is
    -- NEVER reused. That keeps the AFTER_CURSOR reconnect/replay cursor unambiguous: a
    -- reused seq would let a freshly-created message masquerade as the deleted row that
    -- previously held the seq, so an AFTER_CURSOR(cursor) resume (seq > cursor) would
    -- silently skip the new message. Maintained by the triggers on `messages` below.
    message_seq_hwm  INTEGER NOT NULL DEFAULT 0,
    startup_error    TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at        DATETIME
);
CREATE INDEX idx_agents_closed_at ON agents(closed_at) WHERE closed_at IS NOT NULL;
CREATE INDEX idx_agents_parent ON agents(parent_agent_id) WHERE parent_agent_id IS NOT NULL;
-- One child row per spawning tool_use: makes EnsureChildAgent replay
-- idempotency a constraint (worker restart mid-spawn), not a convention.
CREATE UNIQUE INDEX idx_agents_spawn_span ON agents(parent_agent_id, spawn_span_id)
    WHERE parent_agent_id IS NOT NULL AND spawn_span_id <> '';

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
    -- Scroll-rail jump-mark classifier (0=none, see proto MarkType). Set at write
    -- time so the rail can list marked seqs without decompressing content.
    mark_type           INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, seq)
);
-- Covers the (agent_id, span_id, source, seq) lookup the to-do extractor
-- uses to find a tool_result's paired tool_use, so SQLite serves the
-- ORDER BY seq ASC LIMIT 1 from the index rather than re-sorting matches.
CREATE INDEX idx_messages_span_id ON messages(agent_id, span_id, source, seq) WHERE span_id <> '';
-- Covering index for the scroll rail's ListMessageMarksByAgentID: SQLite serves
-- the (agent_id, ORDER BY seq ASC) scan of marked rows from the index alone.
-- Partial (only marked rows) so the far-more-numerous unmarked inserts skip it.
CREATE INDEX idx_messages_mark_type ON messages(agent_id, seq, mark_type) WHERE mark_type <> 0;

-- Advance agents.message_seq_hwm whenever a message is assigned a seq -- a fresh
-- insert (CreateMessage) or a notification reseq that moves a row to the tail
-- (UpdateNotificationThread). Guarded (message_seq_hwm < NEW.seq) so it can only
-- ever raise the mark, never lower it, and so a no-op write is cheap.
-- +goose StatementBegin
CREATE TRIGGER trg_messages_seq_hwm_insert AFTER INSERT ON messages
BEGIN
    UPDATE agents SET message_seq_hwm = NEW.seq
    WHERE id = NEW.agent_id AND message_seq_hwm < NEW.seq;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER trg_messages_seq_hwm_update AFTER UPDATE OF seq ON messages
BEGIN
    UPDATE agents SET message_seq_hwm = NEW.seq
    WHERE id = NEW.agent_id AND message_seq_hwm < NEW.seq;
END;
-- +goose StatementEnd

-- Pending control requests
CREATE TABLE control_requests (
    agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    request_id  TEXT NOT NULL,
    payload     BLOB NOT NULL,
    -- claim_token identifies this REQUEST INSTANCE. The worker mints a fresh token per
    -- PersistControlRequest (id.Generate nanoid), broadcasts it in AgentControlRequest, and the frontend
    -- echoes it in the answer so control_response_answers can dedup per instance rather than per
    -- reused request_id. '' for a row stored before the token existed (degrades to id-only dedup).
    claim_token TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (agent_id, request_id)
);

-- Idempotency claims for control-response answers (#258). A control answer is deduped by an atomic
-- INSERT here (ClaimControlResponseAnswer): the first answer for a (agent_id, request_id, claim_token)
-- wins the primary key, and a duplicate -- an RPC retry, or a second window answering the SAME request
-- instance (same echoed claim_token) -- loses. Being a durable row rather than in-memory state, the
-- claim survives BOTH a subprocess restart and a worker-PROCESS restart, so a duplicate straddling
-- either is still deduped instead of re-persisting a second answer row + scroll-rail dot.
--
-- claim_token is what makes the dedup INSTANCE-scoped: when a request_id is REUSED (a Codex/ACP
-- JSON-RPC counter that reset across a plan-exec restart, or a Claude follow-up), the new instance
-- carries a FRESH claim_token, so its genuine answer claims a distinct key and is never rejected as a
-- duplicate of the prior instance's -- while a stale duplicate of the PRIOR instance (carrying the old
-- token) still loses. No release-on-reissue is needed; rows are cleaned up in bulk with the agent via
-- ON DELETE CASCADE. '' claim_token (a pre-token or lookup-miss answer) degrades to id-only dedup.
CREATE TABLE control_response_answers (
    agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    request_id  TEXT NOT NULL,
    claim_token TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, request_id, claim_token)
);

-- One row per subagent transcript whose closing divider has been written.
--
-- Two independent writers can end a subagent: the registry (whichever applier
-- moved its row into a final status) and the child's own turn end (a provider
-- that forwards its own closing envelope). Both must produce exactly ONE
-- divider, in either arrival order. Comparing the transcript's last message
-- cannot decide it -- both writers persist AFTER releasing the cache mutex, so
-- two goroutines can each read "not ended" and each write. This claim is the
-- decision: the INSERT picks a single winner, and being durable it also holds
-- across a worker restart, where a content probe would have to read and
-- decompress the last message to guess.
CREATE TABLE subagent_transcript_closes (
    child_agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE
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

-- Terminals. No workspace_id, for the same reason as agents above.
CREATE TABLE terminals (
    id            TEXT PRIMARY KEY,
    working_dir   TEXT NOT NULL DEFAULT '',
    home_dir      TEXT NOT NULL DEFAULT '',
    shell_start_dir TEXT NOT NULL DEFAULT '',
    shell         TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    cols          INTEGER NOT NULL DEFAULT 80,
    rows          INTEGER NOT NULL DEFAULT 25,
    screen        BLOB NOT NULL DEFAULT x'',
    exit_code     INTEGER NOT NULL DEFAULT 0,
    startup_error TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at     DATETIME
);
CREATE INDEX idx_terminals_closed_at ON terminals(closed_at) WHERE closed_at IS NOT NULL;

-- Junction: which tabs use which LeapMux-created worktree.
--
-- user_id scopes the FILE-tab liveness join (see worktree_tab_liveness): file
-- tab ids are only unique within a user (worker_file_tabs is keyed by
-- (user_id, tab_id)), so the liveness view needs the user to avoid matching a
-- different user's file tab. It is left '' for AGENT/TERMINAL links, whose ids
-- are globally unique, so their liveness legs never need it.
--
-- user_id is part of the PRIMARY KEY for the same reason. Without it, two
-- users' FILE links to the same worktree with the same tab_id collide: the
-- second insert is swallowed by AddWorktreeTab's ON CONFLICT DO NOTHING, and
-- then either user's tab-close deletes the single surviving row -- silently
-- unlinking the other user's still-open file tab and letting the worktree GC
-- reclaim a directory that is still mounted in an editor. FILE tab ids are
-- minted client-side as file-<epoch-ms>-<per-module-load counter>, so a
-- cross-user collision needs no adversary, just two clients starting in the
-- same millisecond. The three read/delete queries keyed by tab
-- (RemoveWorktreeTab, DeleteWorktreeTabsByTabID, GetWorktreeForTab) bind
-- user_id to match.
CREATE TABLE worktree_tabs (
    worktree_id  TEXT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (worktree_id, tab_type, tab_id, user_id)
);
-- Covers the by-tab lookups, which all bind (tab_type, tab_id, user_id).
CREATE INDEX idx_worktree_tabs_tab ON worktree_tabs(tab_type, tab_id, user_id);

-- Each worktree_tabs link annotated with whether its backing tab is still
-- live: an agent/terminal with closed_at IS NULL, or a still-present
-- worker_file_tabs row (file tabs are hard-deleted on close). A link to a
-- closed/deleted tab -- a startup-race strand -- has is_live = 0.
--
-- This view is the single definition of "is this link live?" so the two
-- consumers (CountLiveWorktreeRefs and ListOrphanCandidateWorktrees) cannot
-- drift apart: adding a new tab type means editing the predicate here once,
-- not in two queries that must agree or the GC reaps a live worktree.
--
-- The agent/terminal legs match on the globally-unique row id; the file leg
-- matches on (user_id, tab_id) because worker_file_tabs is keyed that way.
-- That key is about ID UNIQUENESS, not tenancy: agent and terminal ids are
-- minted server-side and are globally unique, while a file tab id is minted by
-- the client as `file-<millis>-<counter>` and is therefore unique only within
-- the client that made it. Matching tab_id alone would let one row stand in
-- for another that happens to share an id, marking a strand live and saving a
-- worktree that should be reaped. worktree_tabs.user_id carries the link's
-- user ('' for AGENT/TERMINAL links, whose ids never need it; '' never matches
-- a real worker_file_tabs row, which always has a non-empty user_id).
CREATE VIEW worktree_tab_liveness AS
SELECT
    t.worktree_id AS worktree_id,
    CASE WHEN
        EXISTS (SELECT 1 FROM agents a WHERE a.id = t.tab_id AND a.closed_at IS NULL)
        OR EXISTS (SELECT 1 FROM terminals te WHERE te.id = t.tab_id AND te.closed_at IS NULL)
        OR EXISTS (SELECT 1 FROM worker_file_tabs f WHERE f.tab_id = t.tab_id AND f.user_id = t.user_id)
    THEN 1 ELSE 0 END AS is_live
FROM worktree_tabs t;

-- File-tab paths kept E2EE on the worker. The hub never sees these
-- rows; clients fetch paths over WatchWorkerPrivateEvents and
-- GetFileTabPath. tab_id is unique within a user but not across users,
-- so the primary key includes user_id.
--
-- user_id therefore stays while workspace_id goes: it is an ID-UNIQUENESS
-- requirement, not a tenancy one (see worktree_tab_liveness above).
CREATE TABLE worker_file_tabs (
    -- CHECK (user_id <> ''): the worker's database has no users table, so the
    -- hub's REFERENCES users(id) floor cannot reach here. FileTabPathStore
    -- refuses a blank owner in Go, but a blank row that got in any other way
    -- would be permanently unclearable -- Get/RevokeRow both refuse an
    -- unminted owner, and the reconciler's scope check skips it -- while
    -- worktree_tab_liveness's FILE leg (which matches f.user_id = t.user_id and
    -- relies on '' never matching a real file-tab row) would start reading
    -- agent/terminal links as live and leak their worktrees.
    user_id      TEXT NOT NULL CHECK (user_id <> ''),
    tab_id       TEXT NOT NULL,
    file_path    TEXT NOT NULL,
    -- The tab's git working directory, and the reason a FILE tab is a
    -- first-class git citizen rather than a type shared code routes around.
    -- agents.working_dir / terminals.working_dir are what every branch-context
    -- operation resolves a tab through -- last-tab close inspection, the
    -- sibling-on-this-branch scan, push -- and this column is the FILE tab's
    -- entry in that same lookup (see getTabWorkingDir).
    --
    -- It is the ORIGINATING tab's working dir, forwarded by the client that
    -- opened the file, not something derived from file_path. A file is opened
    -- from an agent or terminal tab, and the branch group the file tab renders
    -- in is that tab's; deriving a dir from the path instead would answer for
    -- whatever repo the file happens to sit in, which is a different question
    -- and a different answer whenever the two diverge. Writers that have no
    -- originating tab (`leapmux control tab open --type=file`) fall back to the
    -- file's own directory, normalized once at write time in
    -- FileTabPathStore.Register so every reader can just read the column.
    working_dir  TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, tab_id)
);

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
    status      TEXT NOT NULL CHECK (status IN ('pending','in_progress','completed','deleted')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (agent_id, row_key),
    -- Matches messages.UNIQUE(agent_id, seq) so a `nextSeq` collision
    -- after restart-on-sparse-seqs (eviction creates holes) fails loudly
    -- instead of producing duplicate orderings. Also serves as the
    -- ORDER BY seq index for ListAgentTodos, so no separate index needed.
    UNIQUE (agent_id, seq)
);

-- Background-task registry rows. Populated incrementally by the worker output
-- handler from provider-neutral OutputSink primitives so the sidebar survives
-- page reloads and cross-machine opens. One registry per ROOT main agent
-- (owner_agent_id); rows for descendants at any depth live under the root.
-- row_key is the provider linkage key (Claude task_id, Codex thread id, ACP
-- toolCallId, etc.). child_agent_id is set only for subagent rows that own a
-- transcript (openable tab); deliberately NOT an FK so the registry row
-- survives its child row, and whole-tree deletion cascades through the owner.
-- Same security posture as agent_todos: content lives only in worker SQLite
-- and travels only over the E2EE WatchEvents channel.
CREATE TABLE agent_background_tasks (
    owner_agent_id  TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, -- ROOT main agent
    row_key         TEXT NOT NULL,  -- provider linkage key (tool_use id / thread id / session id / task id)
    seq             INTEGER NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('subagent','shell')),
    child_agent_id  TEXT NOT NULL DEFAULT '', -- set for subagent rows that own a transcript
    parent_agent_id TEXT NOT NULL DEFAULT '', -- immediate parent agent id ('' == the owner itself)
    group_key       TEXT NOT NULL DEFAULT '', -- workflow/phase grouping key
    group_label     TEXT NOT NULL DEFAULT '', -- human label for the group
    title           TEXT NOT NULL DEFAULT '',
    -- 1 when `title` is a verbatim shell command rather than prose. Only the
    -- provider that hands the command over itself can say so; see the
    -- title_is_command field on the BackgroundTaskItem proto.
    title_is_command INTEGER NOT NULL DEFAULT 0,
    description     TEXT NOT NULL DEFAULT '',
    active_form     TEXT NOT NULL DEFAULT '', -- live "what it does now" text
    status          TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','stopped','interrupted')),
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    ended_at        DATETIME,
    PRIMARY KEY (owner_agent_id, row_key),
    UNIQUE (owner_agent_id, seq)
);
CREATE INDEX idx_agent_background_tasks_child ON agent_background_tasks(child_agent_id) WHERE child_agent_id <> '';

-- Every OPEN tab and the directory its git questions are answered from, as one
-- relation.
--
-- "Where does a tab live" is asked by the branch-sibling scan, and it used to be
-- asked as three hand-written SELECTs fanned out across three goroutines --
-- three places to remember the liveness rule, three to remember the owner rule,
-- and a fourth tab type would have needed a fourth of each. Same reasoning as
-- worktree_tab_liveness above: one definition of the per-type predicate, not N
-- that must agree.
--
-- user_id is '' for agents and terminals and REAL for file tabs, and that
-- asymmetry is deliberate rather than a placeholder. It mirrors
-- worktree_tabs.user_id exactly (see worktree_tab_liveness): agent and terminal
-- ids are minted server-side and globally unique, so they need no owner to
-- disambiguate them, while a file tab id is minted client-side as
-- `file-<millis>-<counter>` and is unique only within the client that made it.
-- A reader scopes with `(user_id = '' OR user_id = ?)`, and worker_file_tabs'
-- own `CHECK (user_id <> '')` is what makes the '' arm provably unable to match
-- a file-tab row.
--
-- Openness differs by type and is encoded here so no reader restates it: agents
-- and terminals are closed by stamping closed_at, while a file tab is HARD
-- DELETED on close, so its presence IS its openness.
CREATE VIEW tab_locations AS
SELECT 1 AS tab_type, id AS tab_id, '' AS user_id, working_dir AS working_dir
FROM agents WHERE closed_at IS NULL AND parent_agent_id IS NULL
UNION ALL
SELECT 2, id, '', working_dir
FROM terminals WHERE closed_at IS NULL
UNION ALL
SELECT 3, tab_id, user_id, working_dir
FROM worker_file_tabs;

-- +goose Down
DROP VIEW IF EXISTS tab_locations;
DROP TABLE IF EXISTS agent_background_tasks;
DROP INDEX IF EXISTS idx_agents_spawn_span;
DROP INDEX IF EXISTS idx_agents_parent;
DROP TABLE IF EXISTS agent_todos;
DROP TABLE IF EXISTS worker_file_tabs;
DROP TABLE IF EXISTS terminals;
DROP VIEW IF EXISTS worktree_tab_liveness;
DROP TABLE IF EXISTS worktree_tabs;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS auto_continue_schedules;
DROP TABLE IF EXISTS control_response_answers;
DROP TABLE IF EXISTS control_requests;
DROP TRIGGER IF EXISTS trg_messages_seq_hwm_update;
DROP TRIGGER IF EXISTS trg_messages_seq_hwm_insert;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS agents;
