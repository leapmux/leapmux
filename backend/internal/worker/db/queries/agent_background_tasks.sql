-- ListAgentBackgroundTasksNewestFirst loads the cold-start seed for one owner's registry.
-- DESC is load-bearing: the registry is capped, and eviction drops the OLDEST
-- final row, so the rows worth holding are the NEWEST ones. An ASC LIMIT hands
-- back the oldest N instead, which for an owner past the cap means a restart
-- resurrects finished rows and hides the running subagents -- and derives
-- nextSeq from a truncated maximum, so the next insert collides with a
-- surviving seq under UNIQUE (owner_agent_id, seq). The caller reverses the
-- result to restore ascending seq order.
-- name: ListAgentBackgroundTasksNewestFirst :many
SELECT * FROM agent_background_tasks WHERE owner_agent_id = ? ORDER BY seq DESC LIMIT ?;

-- ListAgentBackgroundTasksByKindNewestFirst is the per-POOL seed. The registry
-- caps each kind independently, so one global window is the wrong shape: an
-- owner whose newest N rows are all shells seeds an EMPTY subagent pool, and the
-- subagent rows are the ones carrying a transcript worth reopening.
-- name: ListAgentBackgroundTasksByKindNewestFirst :many
SELECT * FROM agent_background_tasks
WHERE owner_agent_id = ? AND kind = ?
ORDER BY seq DESC LIMIT ?;

-- DeleteFinishedAgentBackgroundTasksBelowSeq reclaims the rows a pool's seed
-- window left behind.
--
-- The cap is SOFT: an upsert exceeds it rather than orphan a steerable child, so
-- a pool can hold more rows than its window admits. Eviction only ever deletes a
-- row the CACHE holds, so without this pass the surplus is never loaded and
-- never deleted -- invisible in the sidebar and growing without limit in the DB.
-- Only FINISHED rows are reclaimed: an active row still names a live child.
-- name: DeleteFinishedAgentBackgroundTasksBelowSeq :execrows
DELETE FROM agent_background_tasks
WHERE owner_agent_id = ? AND kind = ? AND seq < ?
  AND status IN ('completed','failed','stopped','interrupted');

-- UpsertAgentBackgroundTask inserts a new row or updates an existing one keyed
-- by (owner_agent_id, row_key). seq is set only on insert (never overwritten on
-- update); the CASE on child_agent_id means a later upsert that omits the id
-- can never blank one recorded earlier. created_at is bound by the caller
-- (sqltime floors to ms) only on insert via the excluded alias; updated_at is
-- always the caller's stamp. Binding both from one Go instant keeps the
-- in-memory cache and the persisted row byte-identical (no strftime-vs-time.Now
-- drift across a reconnect).
-- name: UpsertAgentBackgroundTask :exec
INSERT INTO agent_background_tasks (
    owner_agent_id, row_key, seq, kind, child_agent_id, parent_agent_id,
    group_key, group_label, title, title_is_command, description, active_form, status, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(owner_agent_id, row_key) DO UPDATE SET
    kind           = excluded.kind,
    child_agent_id = CASE WHEN excluded.child_agent_id <> '' THEN excluded.child_agent_id ELSE agent_background_tasks.child_agent_id END,
    parent_agent_id = excluded.parent_agent_id,
    group_key      = excluded.group_key,
    group_label    = excluded.group_label,
    title          = excluded.title,
    title_is_command = excluded.title_is_command,
    description    = excluded.description,
    active_form    = excluded.active_form,
    status         = excluded.status,
    updated_at     = excluded.updated_at;

-- name: UpdateAgentBackgroundTaskStatus :exec
UPDATE agent_background_tasks SET
    status     = ?,
    active_form = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND row_key = ?;

-- StampAgentBackgroundTaskEndedAt sets ended_at on a row that is already
-- final but still has a NULL ended_at. This covers the status-update path:
-- providers call UpdateBackgroundTaskStatus(a final status) then CloseBackgroundTask,
-- but the close early-returns on IsFinished(), so ended_at was never stamped.
-- The status filter makes this idempotent (only finished rows qualify), so a
-- transient failure here self-corrects on the next final-status write (or the boot
-- sweep): the status is already final in both DB and cache, and a NULL
-- ended_at reads back as the zero time, matching the cache's untouched value.
-- Kept as a separate query because sqlc cannot infer the type of a positional
-- parameter that appears only inside a CASE WHEN condition (a `? IN (...)`
-- predicate), so the atomic single-statement form is not generatable with this
-- toolchain version. The close path (CloseAgentBackgroundTask) sets both
-- status and ended_at in one statement because its WHERE guard
-- (status IN pending/running) is a literal, not a parameter.
-- name: StampAgentBackgroundTaskEndedAt :exec
UPDATE agent_background_tasks SET
    ended_at   = ?
WHERE owner_agent_id = ? AND row_key = ?
  AND ended_at IS NULL
  AND status IN ('completed','failed','stopped','interrupted');

-- CloseAgentBackgroundTask stamps the final status and ended_at. The
-- status-IN filter means a finished row can never be resurrected or re-closed
-- by a late/duplicate event.
-- name: CloseAgentBackgroundTask :exec
UPDATE agent_background_tasks SET
    status     = ?,
    ended_at   = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND row_key = ? AND status IN ('pending','running');

-- ReviveAgentBackgroundTask returns a FINISHED row to running and clears its
-- ended_at, for a subagent that its parent restarted by sending it a message.
--
-- This is the one write that undoes a final status, and it is deliberately its
-- own statement rather than a relaxation of the guards on the upsert and the
-- status update. Those guards drop a NON-final status against a final row, and
-- they must keep doing so: a replayed running upsert (a duplicate task_started,
-- a worker restart, a resumed session that re-announces every task it once ran)
-- would otherwise leave a row running that nothing ever closes, which pins the
-- parent's thinking indicator for good. A caller reaches this query only with
-- positive evidence that the provider restarted the task.
--
-- active_form and description are cleared with the status. Both describe the run
-- that ENDED -- the last activity text, and the output file its task_notification
-- named -- and the restarted run has reported neither yet. The row's activity
-- slot shows whichever is present, so leaving them pins the previous run's
-- output path under a subagent that is running again.
--
-- The status-IN filter makes the call idempotent -- an absent or still-active
-- row matches nothing -- and :execrows lets the caller tell a real revive from
-- a no-op, because only a real one owes the transcript-close release.
-- name: ReviveAgentBackgroundTask :execrows
UPDATE agent_background_tasks SET
    status      = 'running',
    active_form = '',
    description = '',
    ended_at    = NULL,
    updated_at  = ?
WHERE owner_agent_id = ? AND row_key = ?
  AND status IN ('completed','failed','stopped','interrupted');

-- name: DeleteAgentBackgroundTaskByRowKey :execresult
DELETE FROM agent_background_tasks WHERE owner_agent_id = ? AND row_key = ?;

-- RenameAgentBackgroundTask re-keys a row (owner_agent_id, old_row_key) to
-- new_row_key. The row_key is the provider linkage key; a provider that learns
-- the stable child id only on the final update (OpenCode: the row opens under
-- the toolCallId and the session id surfaces late) renames so a single row
-- tracks the whole lifecycle instead of a spawn row + a separately-keyed row.
-- No status-IN guard: a rename is key-only and applies to any row state.
-- name: RenameAgentBackgroundTask :execrows
UPDATE agent_background_tasks SET row_key = ? WHERE owner_agent_id = ? AND row_key = ?;

-- CountAgentBackgroundTasksByRowKey answers "is this key already taken?" for the
-- rename path. (owner_agent_id, row_key) is the PRIMARY KEY, so a rename onto an
-- occupied key fails the UPDATE outright rather than overwriting; the caller
-- checks first and drops the losing row instead.
-- name: CountAgentBackgroundTasksByRowKey :one
SELECT COUNT(*) FROM agent_background_tasks WHERE owner_agent_id = ? AND row_key = ?;

-- GetAgentBackgroundTaskByChildAgentID is the reverse lookup behind
-- send-to-subagent and interrupt routing: child agent id -> (owner, row_key).
-- name: GetAgentBackgroundTaskByChildAgentID :one
SELECT * FROM agent_background_tasks WHERE child_agent_id = ?;

-- MarkAgentBackgroundTasksEnded gives every still-active row owned by an agent a
-- final status (used on clean process exit). Returns the affected-row count so
-- the caller can skip the broadcast when nothing moved.
-- name: MarkAgentBackgroundTasksEnded :execrows
UPDATE agent_background_tasks SET
    status     = ?,
    ended_at   = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND status IN ('pending','running');

-- MarkAllActiveAgentBackgroundTasksInterrupted runs at worker boot before any
-- caches exist: every active row left over from the previous process is
-- honestly labeled "interrupted" (the worker restarted and cut the task off).
--
-- It RETURNS the child transcript of each row it ended, so the boot sweep closes
-- exactly the transcripts it interrupted. The write is the single source for
-- both facts: a separate read before the UPDATE could not be repeated after it
-- (the rows are no longer active), so a failed read followed by a successful
-- UPDATE would strand every one of those transcripts with no closing divider,
-- permanently and with no way for a later boot to find them.
-- name: MarkAllActiveAgentBackgroundTasksInterrupted :many
UPDATE agent_background_tasks SET
    status     = 'interrupted',
    ended_at   = ?,
    updated_at = ?
WHERE status IN ('pending','running')
RETURNING child_agent_id;
