-- name: CreateWorktree :one
-- Upsert, because two callers can reach here for the same path at once:
-- idx_worktrees_path is UNIQUE over the live rows, and opening two agents on
-- one worktree (the "use existing worktree" flow) runs two async startups that
-- both miss GetWorktreeByPath before either inserts. Without conflict handling
-- the loser surfaced a raw "UNIQUE constraint failed" to the user.
--
-- The conflict target names idx_worktrees_path exactly, so a collision on the
-- PRIMARY KEY is still raised rather than swallowed by an untargeted clause,
-- and the no-op DO UPDATE exists only so RETURNING yields the WINNER's row.
-- Winning the insert is not the same as owning the id, and returning the row
-- makes that mechanical: a loser that assumed its own id would hand back an id
-- no row carries, and every later ref-count and close keyed on it would miss.
INSERT INTO worktrees (id, worktree_path, repo_root, branch_name) VALUES (?, ?, ?, ?)
ON CONFLICT(worktree_path) WHERE deleted_at IS NULL
DO UPDATE SET worktree_path = excluded.worktree_path
RETURNING *;

-- name: GetWorktreeByPath :one
SELECT * FROM worktrees WHERE worktree_path = ? AND deleted_at IS NULL;

-- name: GetWorktreeByID :one
SELECT * FROM worktrees WHERE id = ?;

-- name: DeleteWorktree :exec
UPDATE worktrees SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorktreeBranchName :exec
UPDATE worktrees SET branch_name = ? WHERE id = ?;

-- name: HardDeleteWorktreesBefore :execresult
-- Raw compare against a SQLiteNullTime cutoff (same canonical layout);
-- see DeleteClosedTerminalsBefore for the rationale.
DELETE FROM worktrees WHERE rowid IN (SELECT w.rowid FROM worktrees w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < sqlc.arg(cutoff) LIMIT 1000);

-- name: AddWorktreeTab :exec
-- user_id is set for every payload-backed link, FILE and IMAGE alike (it scopes
-- the worktree_tab_liveness join against worker_tab_payloads, which is keyed by
-- (user_id, tab_id)); AGENT/TERMINAL links pass '' since their ids are globally
-- unique.
INSERT INTO worktree_tabs (worktree_id, tab_type, tab_id, user_id) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING;

-- name: RemoveWorktreeTab :exec
-- user_id is part of the key: payload-backed links carry the owning user (see
-- the worktree_tabs DDL), so an unbound user_id would delete a different user's
-- link to the same worktree with the same tab_id. AGENT/TERMINAL callers pass
-- '' -- exactly what AddWorktreeTab wrote for them.
DELETE FROM worktree_tabs WHERE worktree_id = ? AND tab_type = ? AND tab_id = ? AND user_id = ?;

-- name: CountWorktreeTabs :one
SELECT COUNT(*) FROM worktree_tabs WHERE worktree_id = ?;

-- name: GetWorktreeForTab :one
-- Bound by user_id for the same reason as RemoveWorktreeTab: without it a
-- FILE-tab close could resolve -- and then remove -- the worktree behind a
-- different user's identically-named tab.
SELECT w.* FROM worktrees w JOIN worktree_tabs wt ON wt.worktree_id = w.id WHERE wt.tab_type = ? AND wt.tab_id = ? AND wt.user_id = ? AND w.deleted_at IS NULL;

-- name: DeleteWorktreeTabsByTabID :exec
-- Bound by user_id for the same reason as RemoveWorktreeTab.
DELETE FROM worktree_tabs WHERE tab_type = ? AND tab_id = ? AND user_id = ?;

-- name: DeleteWorktreeTabsByWorktreeID :exec
DELETE FROM worktree_tabs WHERE worktree_id = ?;

-- Count links whose backing tabs remain live. Zero does not authorize Git deletion.
-- name: CountLiveWorktreeRefs :one
SELECT COUNT(*) FROM worktree_tab_liveness WHERE worktree_id = ? AND is_live = 1;

-- Select tracked worktrees with dead links and no live links.
-- The reconciler deletes only links that remain dead when cleanup runs.
-- A worktree with zero links requires no link cleanup. Its tracking row remains.
-- name: ListOrphanCandidateWorktrees :many
SELECT w.* FROM worktrees w
WHERE w.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM worktree_tab_liveness l WHERE l.worktree_id = w.id)
  AND NOT EXISTS (
    SELECT 1 FROM worktree_tab_liveness l WHERE l.worktree_id = w.id AND l.is_live = 1
  );

-- name: ListLiveTabWorkingDirs :many
SELECT l.tab_type, l.tab_id, l.user_id, l.working_dir, p.payload
FROM tab_locations l
LEFT JOIN worker_tab_payloads p
  ON p.tab_type = l.tab_type AND p.tab_id = l.tab_id AND p.user_id = l.user_id
WHERE l.is_quake = 0;

-- name: DeleteDeadWorktreeTabs :exec
-- sqlc requires this qualifier because the correlated view exposes worktree_id too.
DELETE FROM worktree_tabs
WHERE worktree_tabs.worktree_id = ?
  AND EXISTS (
    SELECT 1
    FROM worktree_tab_liveness l
    WHERE l.worktree_id = worktree_tabs.worktree_id
      AND l.tab_type = worktree_tabs.tab_type
      AND l.tab_id = worktree_tabs.tab_id
      AND l.user_id = worktree_tabs.user_id
      AND l.is_live = 0
  );
