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

-- Counts a worktree's tab links whose backing tab is still live (see the
-- worktree_tab_liveness view for the definition of "live"). A link to a
-- closed/deleted tab -- a startup-race strand -- has is_live = 0, so a
-- worktree whose links are all dead reports 0 and the orphan-worktree GC
-- can reclaim it.
-- name: CountLiveWorktreeRefs :one
SELECT COUNT(*) FROM worktree_tab_liveness WHERE worktree_id = ? AND is_live = 1;

-- ListOrphanCandidateWorktrees returns every tracked worktree that has at
-- least one tab link but no LIVE one -- i.e. all its links are
-- startup-race strands pointing at closed/deleted tabs (liveness defined by
-- the worktree_tab_liveness view). The has-at-least-one-link guard is
-- deliberate: a worktree with zero links is either mid-creation (the
-- agent/terminal row exists but its link has not been written yet) or
-- freshly adopted, and must never be reclaimed. Callers additionally
-- require a candidate to persist across two consecutive reconciler passes
-- before removing it, so a transient zero-live window during startup or
-- worktree reuse is never mistaken for an orphan.
-- name: ListOrphanCandidateWorktrees :many
SELECT w.* FROM worktrees w
WHERE w.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM worktree_tab_liveness l WHERE l.worktree_id = w.id)
  AND NOT EXISTS (
    SELECT 1 FROM worktree_tab_liveness l WHERE l.worktree_id = w.id AND l.is_live = 1
  );
