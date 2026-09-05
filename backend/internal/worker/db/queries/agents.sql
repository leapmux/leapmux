-- CreateAgent records whether anybody CHOSE the title: see the
-- title_auto_generated column. The caller answers it from the request, because
-- only the client knows whether the user kept the suggestion it pre-filled.
-- name: CreateAgent :exec
INSERT INTO agents (id, working_dir, home_dir, title, title_auto_generated, options, agent_provider, resumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByID :one
SELECT * FROM agents WHERE id = ?;

-- GetAgentID is the existence probe behind requireAgentID: it answers
-- sql.ErrNoRows for an unknown id while reading two narrow columns, so a
-- handler that needs no agent fields (close, interrupt) does not deserialize
-- the options / option_groups JSON blobs SELECT * would.
--
-- workspace_archived rides along because the registrar refuses every write
-- handler for an archived workspace, and this probe is what a by-id write
-- handler loads. An INTEGER column costs nothing next to the blobs this query
-- exists to skip.
-- name: GetAgentID :one
SELECT id, workspace_archived FROM agents WHERE id = ?;

-- GetAgentTitle reads only the title column, for the RenameAgent reply on the
-- path that stores nothing: a request whose title cleans to empty keeps the
-- stored title, and the response has to report THAT title rather than the
-- empty string it refused to store. GetAgentByID would answer the same
-- question with a SELECT * that deserializes the options / option_groups JSON
-- blobs -- the cost registerAgentGatedByID exists to avoid on this handler.
-- name: GetAgentTitle :one
SELECT title FROM agents WHERE id = ?;

-- name: ListAllOpenAgentIDs :many
SELECT id FROM agents WHERE closed_at IS NULL;

-- name: ListAllAgentIDs :many
SELECT id FROM agents;

-- name: CloseAgent :execresult
-- closed_at IS NULL makes this idempotent, which the retention sweep depends
-- on. The orphan reconciler re-closes rows it finds absent from the hub's
-- list, and ListAllAgentIDs returns closed rows too -- so without this guard
-- every hourly pass re-stamps closed_at = now on every already-closed row,
-- the `closed_at < cutoff` retention delete never matches again, and the rows
-- (plus their cascaded messages) accumulate for the machine's lifetime.
--
-- :execresult, not :exec, because the affected-row count is the ONLY signal
-- that distinguishes "this close retired a live agent" from "the row was
-- already closed". closeTabCommon needs that to decide whether a
-- WORKTREE_ACTION_REMOVE is a user-confirmed delete or a stale client asking
-- to force-remove a directory that nobody looks at.
UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND closed_at IS NULL;

-- RenameAgent is a user typing a title, so it clears title_auto_generated and
-- plan-mode auto-rename stops overwriting the row from then on.
-- name: RenameAgent :execresult
UPDATE agents SET title = ?, title_auto_generated = 0 WHERE id = ?;

-- name: UpdateAgentSessionID :exec
UPDATE agents SET agent_session_id = ?, session_start_seq = (SELECT COALESCE(MAX(m.seq), 0) FROM messages m WHERE m.agent_id = agents.id) WHERE agents.id = ?;

-- name: ReopenAgent :exec
UPDATE agents SET closed_at = NULL WHERE id = ?;

-- name: SetAgentOptions :exec
UPDATE agents SET options = ? WHERE id = ?;

-- SetAgentOptionGroups persists only the provider-reported catalog (option_groups),
-- leaving the chosen option values untouched. Used when a running ACP provider discovers
-- its catalog (e.g. a dynamic model list reported only after the session/new handshake)
-- after the startup handoff already persisted a narrower one, so the post-exit offline
-- read surfaces the discovered options instead of a stale static fallback.
-- name: SetAgentOptionGroups :exec
UPDATE agents SET option_groups = ? WHERE id = ?;

-- SetAgentOptionsIfUnchanged is a compare-and-swap: it overwrites options only when
-- the column still equals expected_options -- the snapshot the new value was merged
-- from. Returns the number of rows changed (0 when the row moved on between the read
-- and the write), so a concurrent PersistSettingsRefresh can re-read, re-merge, and
-- retry instead of clobbering the other writer's keys with a stale full-map blob.
-- name: SetAgentOptionsIfUnchanged :execrows
UPDATE agents SET options = sqlc.arg(options)
WHERE id = sqlc.arg(id) AND options = sqlc.arg(expected_options);

-- SetAgentOptionGroupsIfUnchanged is the compare-and-swap form of SetAgentOptionGroups: it
-- overwrites the provider-reported catalog only while the column still equals
-- expected_option_groups -- the snapshot that the new catalog replaces. A running ACP provider
-- that discovers a richer catalog (e.g. a dynamic model list reported only after the
-- session/new handshake) and persists it via SetAgentOptionGroups on a separate, unsynchronized
-- path must not be clobbered by a (re)start handoff's narrower catalog: when the column moved on
-- (option_groups != expected_option_groups) this write is a no-op and the newer catalog is kept.
-- It is the standalone mirror of the option_groups CASE in
-- UpdateAgentConfirmedSettingsPreservingStartedSettings, for the synchronous
-- persistConfirmedAgentSettings path that writes the catalog separately from the options CAS.
-- Returns the number of rows changed (0 when the catalog moved on).
-- name: SetAgentOptionGroupsIfUnchanged :execrows
UPDATE agents SET option_groups = sqlc.arg(option_groups)
WHERE id = sqlc.arg(id) AND option_groups = sqlc.arg(expected_option_groups);

-- UpdateAgentConfirmedSettingsPreservingStartedSettings is used by
-- asynchronous startup. It persists the provider-reported catalog and the
-- confirmed option values via a compare-and-swap: the options column is only
-- overwritten when it still equals expected_options -- the row snapshot the
-- confirmed_options blob was derived from. confirmed_options already folds in
-- any setting the user changed during startup, so writing it when the row still
-- matches preserves those edits AND applies the provider's resolutions; a newer
-- change that landed after the snapshot (row != expected_options) is left
-- untouched.
-- The option_groups (catalog) column is CAS-guarded INDEPENDENTLY against
-- expected_option_groups: a running ACP provider that discovers its dynamic model
-- list AFTER this handoff and persists it via SetAgentOptionGroups must not be
-- clobbered by the (now narrower) startup catalog. Both writers touch only the
-- option_groups column with no shared lock, so without this guard a late-landing
-- handoff would overwrite the richer discovered catalog. When the catalog moved on
-- (option_groups != expected_option_groups) we keep the newer one.
-- name: UpdateAgentConfirmedSettingsPreservingStartedSettings :one
UPDATE agents SET
  options = CASE
    WHEN options = sqlc.arg(expected_options) THEN sqlc.arg(confirmed_options)
    ELSE options
  END,
  option_groups = CASE
    WHEN option_groups = sqlc.arg(expected_option_groups) THEN sqlc.arg(option_groups)
    ELSE option_groups
  END
WHERE id = sqlc.arg(id)
RETURNING *;

-- UpdateAgentConfirmedSettings atomically writes the confirmed options blob AND the provider
-- option-group catalog in ONE statement, for the synchronous persistConfirmedAgentSettings path, so
-- a concurrent options writer can't land BETWEEN two separate column writes and leave the row
-- showing this handoff's options beside a foreign catalog. The options column is a compare-and-swap
-- on expected_options (the snapshot the blob was merged from); the caller retries on a miss
-- (the returned options != options). The option_groups column is written ONLY on that SAME
-- successful options CAS (which requires options = expected_options) AND while it still equals
-- expected_option_groups, so the two columns move together-or-neither -- a richer catalog a running
-- provider discovered concurrently (option_groups != expected_option_groups) is preserved, and a
-- lost options CAS writes nothing (the caller re-merges and retries, keeping both atomic). Pass
-- option_groups = '' with expected_option_groups = '' to leave the catalog untouched (a no-op write,
-- e.g. when its marshal failed).
-- name: UpdateAgentConfirmedSettings :one
UPDATE agents SET
  options = CASE
    WHEN options = sqlc.arg(expected_options) THEN sqlc.arg(options)
    ELSE options
  END,
  option_groups = CASE
    WHEN options = sqlc.arg(expected_options) AND option_groups = sqlc.arg(expected_option_groups)
    THEN sqlc.arg(option_groups)
    ELSE option_groups
  END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetAgentStartupError :exec
UPDATE agents SET startup_error = ? WHERE id = ?;

-- SetAgentWorkspaceArchived changes only the cached workspace lifecycle state.
-- The row stays open, and all transcript and worktree data stays unchanged.
--
-- closed_at IS NULL matters, because the caller treats a changed row as a tab
-- to stop and broadcast for. A closed row stays in this table for the whole
-- 7-day cleanup retention, so without the predicate an archive that races a
-- close re-runs the full teardown for a tab that the worker already tore down,
-- and the matching unarchive spends a startup permit on it. This is the same
-- reason the orphan reconciler reads ListAllOpenRootAgentIDs.
-- name: SetAgentWorkspaceArchived :execrows
UPDATE agents SET workspace_archived = sqlc.arg(workspace_archived)
WHERE id = sqlc.arg(id) AND closed_at IS NULL
  AND workspace_archived <> sqlc.arg(workspace_archived);

-- GetAgentForInactiveBroadcast reads the three columns broadcastAgentInactive
-- needs. GetAgentByID would answer the same question, and it reads the options
-- and option_groups TEXT columns that this caller never looks at.
-- name: GetAgentForInactiveBroadcast :one
SELECT id, agent_session_id, agent_provider FROM agents WHERE id = ?;

-- name: UpdateAgentHomeDir :exec
UPDATE agents SET home_dir = ? WHERE id = ?;

-- name: UpdateAgentPlanFilePath :exec
UPDATE agents SET plan_file_path = ? WHERE id = ?;

-- name: UpdateAgentPlan :exec
UPDATE agents SET plan_file_path = ?, plan_title = ? WHERE id = ?;

-- UpdateAgentPlanAndTitle is the plan-mode auto-rename. The model chose this
-- title, not the user, so the row stays auto-generated and a later plan title
-- may replace it again.
-- name: UpdateAgentPlanAndTitle :exec
UPDATE agents SET plan_file_path = ?, plan_title = ?, title = ?, title_auto_generated = 1 WHERE id = ?;

-- UpdateAgentGoal writes the durable half of the session goal. The progress
-- counters are deliberately absent: they change after every tool call and ride
-- the ephemeral session-info broadcast, so keeping them here would make this
-- statement run hundreds of times per turn.
-- name: UpdateAgentGoal :exec
UPDATE agents
SET goal_objective = ?, goal_status = ?, goal_status_detail = ?, goal_created_at = ?, goal_updated_at = ?
WHERE id = ?;

-- ClearAgentGoal removes the goal. It is unconditional on purpose. Codex's
-- thread/resume pushes thread/goal/cleared to mean "this thread has no goal",
-- and a caller that skipped the write because its in-memory copy was already
-- empty would leave a goal from a previous process in the table forever.
-- name: ClearAgentGoal :exec
UPDATE agents
SET goal_objective = '', goal_status = '', goal_status_detail = '', goal_created_at = NULL, goal_updated_at = ?
WHERE id = ?;

-- ClearAllAgentGoalStatuses runs once at worker boot. A goal that survived a
-- restart is not being pursued by any process, so leaving its status set would
-- draw live Pause and Clear buttons for a goal nothing is running. The
-- objective text stays so the panel can still show what was being attempted;
-- the provider's own snapshot re-arms the status when a session resumes.
-- name: ClearAllAgentGoalStatuses :exec
UPDATE agents SET goal_status = '', goal_status_detail = '' WHERE goal_status <> '';

-- name: ListAgentsByIDs :many
SELECT * FROM agents WHERE id IN (sqlc.slice('ids')) AND closed_at IS NULL;

-- name: DeleteClosedAgentsBefore :execresult
-- Raw compare against a SQLiteNullTime cutoff (same canonical layout);
-- see DeleteClosedTerminalsBefore for the rationale.
DELETE FROM agents WHERE rowid IN (SELECT a.rowid FROM agents a WHERE a.closed_at < sqlc.arg(cutoff) LIMIT 1000);

-- ListAgentIDsWithPlanInDir returns the IDs of agents whose plan_file_path
-- begins with the provided literal byte sequence. Used by the plan-archive
-- task to skip year directories that still hold an active agent's plan.
-- instr() is used (not LIKE / GLOB) so data dirs containing wildcard
-- metacharacters cannot produce false positives or false negatives.
-- name: ListAgentIDsWithPlanInDir :many
SELECT id FROM agents WHERE instr(plan_file_path, ?) = 1;

-- CreateChildAgent inserts a virtual child agent (a subagent transcript fed by
-- the parent provider's process; it never owns a process). The caller copies
-- working_dir/home_dir/agent_provider from the parent row.
-- A child's title is the spawn's own description or a pooled name, never a
-- value a user typed, so it is auto-generated by construction.
-- name: CreateChildAgent :exec
INSERT INTO agents (id, parent_agent_id, spawn_span_id, working_dir, home_dir, title, title_auto_generated, agent_provider) VALUES (?, ?, ?, ?, ?, ?, 1, ?);

-- GetChildAgentBySpawnSpan is the worker-restart fallback behind
-- EnsureChildAgent: re-attach a child row when the registry upsert did not
-- land before the restart. The (parent_agent_id, spawn_span_id) pair is unique
-- among children (idx_agents_spawn_span), so this is at most one row.
-- name: GetChildAgentBySpawnSpan :one
SELECT * FROM agents WHERE parent_agent_id = ? AND spawn_span_id = ?;

-- GetRootAgentID walks parent_agent_id up to the root main agent. A root row
-- has parent_agent_id IS NULL.
-- name: GetRootAgentID :one
WITH RECURSIVE ancestors AS (
    SELECT a.id AS id, a.parent_agent_id AS parent_agent_id FROM agents a WHERE a.id = ?
    UNION ALL
    SELECT a.id AS id, a.parent_agent_id AS parent_agent_id FROM agents a
    JOIN ancestors ON a.id = ancestors.parent_agent_id
)
SELECT ancestors.id FROM ancestors WHERE ancestors.parent_agent_id IS NULL LIMIT 1;

-- ListDescendantAgentIDs walks parent_agent_id DOWN from a root, returning the
-- ids of every virtual child at any depth, in NO particular order (the CTE is
-- evaluated breadth-first, so a parent comes before its own children).
--
-- Used by the CHILD-tab close path, which runs no teardown and so has nothing
-- later to read: CloseAgent reports the list to the client as
-- descendant_agent_ids, and the client retires those tabs. A ROOT close does
-- not use this query -- it derives the same set from ListAgentTreeIDs below,
-- AFTER its teardown, so that a subagent spawned during the provider's drain is
-- included. See closeAgentTabCommon.
--
-- Deliberately unfiltered by closed_at: a row exists for every subagent the
-- provider ever spawned, so the client receives ids for subagents that were
-- never opened as tabs and for ones closed long ago. Each client MUST filter to
-- the tabs it actually holds before it tombstones anything.
-- name: ListDescendantAgentIDs :many
WITH RECURSIVE descendants AS (
    SELECT a.id AS id FROM agents a WHERE a.parent_agent_id = ?
    UNION ALL
    SELECT a.id AS id FROM agents a JOIN descendants ON a.parent_agent_id = descendants.id
)
SELECT descendants.id FROM descendants;

-- ListAgentTreeIDs returns the root id AND every descendant id (the whole
-- subtree rooted at the given agent). It is the ONE read a root-tab close
-- makes, and it serves three consumers: stamping closed_at on the root and all
-- its (virtual) children, freeing each child's per-agent maps, and reporting
-- the descendants to the client. The caller iterates this list and runs
-- CloseAgent per id (sqlc cannot parse a recursive CTE inside an UPDATE's
-- IN-subquery, so the close loop lives in Go, one statement per id rather than
-- one transaction). CloseAgent's `closed_at IS NULL` guard keeps each step
-- idempotent.
--
-- Read AFTER the teardown, so it includes a subagent the provider spawned while
-- it drained. Nothing else would ever reach such a row: the orphan reconciler
-- lists roots only.
-- name: ListAgentTreeIDs :many
WITH RECURSIVE tree AS (
    SELECT a.id AS id FROM agents a WHERE a.id = ?
    UNION ALL
    SELECT a.id AS id FROM agents a JOIN tree ON a.parent_agent_id = tree.id
)
SELECT tree.id FROM tree;

-- ListAllOpenRootAgentIDs is the orphan reconciler's view: only root main
-- agents (parent_agent_id IS NULL). Tabless children are their DEFAULT state
-- and must never be reaped, so they are excluded here; they disappear when
-- their root closes. ListAllOpenAgentIDs (above) still returns children so the
-- state sweep keeps seeing them.
-- name: ListAllOpenRootAgentIDs :many
SELECT id FROM agents WHERE closed_at IS NULL AND parent_agent_id IS NULL;

-- ListRootAgentIDsForResume excludes archived rows before the resume scheduler
-- dispatches them. The orphan reconciler uses ListAllOpenRootAgentIDs instead,
-- because it must still reap an archived tab that the Hub deleted.
-- name: ListRootAgentIDsForResume :many
SELECT id FROM agents
WHERE closed_at IS NULL AND parent_agent_id IS NULL AND workspace_archived = 0;


-- ListSessionsForResume lists the resume handles this worker recorded for one
-- provider, newest activity first: every row of the requested working
-- directory, plus every OPEN row of any directory.
--
-- It returns OPEN rows as well as closed ones, although only a closed session
-- can be offered for resume: an open handle identifies a session a live process
-- is already attached to, and the caller needs it as an EXCLUSION set -- the
-- provider's own store lists that session too, and resuming it into a second
-- tab would run two processes against one session store. The caller cannot
-- build that set from a second query without a race between the two reads.
--
-- The open rows are NOT restricted to the requested directory, and that is what
-- makes the exclusion set complete. A handle can be open under one directory
-- while a provider's store files it under another: the store records the
-- directory the session was CREATED in, and a user who resumes it elsewhere
-- leaves an open row that carries the second directory. Restricting both arms
-- to one directory hid exactly that row, and the store then offered the live
-- handle back.
--
-- No LIMIT, deliberately. A cut ordered by last activity would drop an open
-- handle that sits behind newer closed rows, which is the same failure by
-- another route. What limits this read instead is the provider filter, the
-- (agent_provider, working_dir) index, and the retention sweep that deletes
-- closed agents.
--
-- parent_agent_id IS NULL drops the virtual child rows that hold subagent
-- transcripts. They carry no session of their own to resume.
--
-- last_activity reads the newest message's time through the
-- messages(agent_id, seq) unique index rather than aggregating the agent's
-- whole transcript. The COALESCE is not only a fallback for a session that
-- never received a message: it makes the column NOT NULL, which is what the
-- generated scan needs -- sqlc types a bare correlated subquery as the
-- non-null SQLiteTime, whose Scan has no branch for a NULL and would fail the
-- whole query on the first message-less row.
-- name: ListSessionsForResume :many
SELECT a.agent_session_id,
       a.title,
       a.closed_at,
       COALESCE(
         (SELECT m.created_at FROM messages m
           WHERE m.agent_id = a.id ORDER BY m.seq DESC LIMIT 1),
         a.created_at
       ) AS last_activity
FROM agents a
WHERE a.agent_provider = ?
  AND (a.working_dir = ? OR a.closed_at IS NULL)
  AND a.agent_session_id <> ''
  AND a.parent_agent_id IS NULL
ORDER BY last_activity DESC;
