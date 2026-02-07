-- name: CreateWorktree :exec
INSERT INTO worktrees (id, worker_id, worktree_path, repo_root, branch_name) VALUES (?, ?, ?, ?, ?);

-- name: GetWorktreeByWorkerAndPath :one
SELECT * FROM worktrees WHERE worker_id = ? AND worktree_path = ?;

-- name: GetWorktreeByID :one
SELECT * FROM worktrees WHERE id = ?;

-- name: DeleteWorktree :exec
DELETE FROM worktrees WHERE id = ?;

-- name: AddWorktreeTab :exec
INSERT INTO worktree_tabs (worktree_id, tab_type, tab_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING;

-- name: RemoveWorktreeTab :exec
DELETE FROM worktree_tabs WHERE worktree_id = ? AND tab_type = ? AND tab_id = ?;

-- name: CountWorktreeTabs :one
SELECT COUNT(*) FROM worktree_tabs WHERE worktree_id = ?;

-- name: GetWorktreeForTab :one
SELECT w.* FROM worktrees w JOIN worktree_tabs wt ON wt.worktree_id = w.id WHERE wt.tab_type = ? AND wt.tab_id = ?;

-- name: DeleteWorktreeTabsByTabID :exec
DELETE FROM worktree_tabs WHERE tab_type = ? AND tab_id = ?;
