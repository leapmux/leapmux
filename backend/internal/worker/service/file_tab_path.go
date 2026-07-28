package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// FileTabPathStore is the worker-local store of (user_id, tab_id) →
// (workspace_id, file_path). The hub never sees these rows; clients
// fetch them via WatchWorkspacePrivateEvents (which carries the path
// over the existing E2EE channel) or one-shot GetFileTabPath.
type FileTabPathStore struct {
	q      *db.Queries
	events *PrivateEventsBus
}

// NewFileTabPathStore returns a store bound to the worker's DB
// queries and the bus where snapshot/live events get published.
func NewFileTabPathStore(q *db.Queries, events *PrivateEventsBus) *FileTabPathStore {
	return &FileTabPathStore{q: q, events: events}
}

// Register persists a (user_id, tab_id, workspace_id, file_path)
// tuple and broadcasts FileTabPathRegistered on the matching
// workspace's private-event stream.
func (s *FileTabPathStore) Register(ctx context.Context, p RegisterFileTabPathParams) error {
	if p.UserID == "" || p.TabID == "" || p.WorkspaceID == "" || p.FilePath == "" {
		return fmt.Errorf("register file tab path: required field empty")
	}
	if err := s.q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		UserID:      p.UserID,
		TabID:       p.TabID,
		WorkspaceID: p.WorkspaceID,
		FilePath:    p.FilePath,
	}); err != nil {
		return fmt.Errorf("upsert worker_file_tab: %w", err)
	}
	// Link the file tab to its worktree (if any) BEFORE publishing the
	// FileTabPathRegistered event: consumers that react to the event
	// (orphan reconciler, sibling close paths calling CountWorktreeTabs)
	// would otherwise race the link insert and observe a temporarily-
	// unlinked file tab. CountWorktreeTabs underreports by one, the
	// last-tab close path decides "no siblings remain", and `git
	// worktree remove` runs while this file tab is still open — the
	// editor then ENOENTs on a dir that was just rm-rf'd. Mirror the
	// agent/terminal worktree-linkage path: probe the file's directory
	// once via `git rev-parse`, then exact-match the canonical top-
	// level against the tracked worktrees. Best-effort: a path outside
	// any tracked worktree leaves the file tab unbound, matching today's
	// behavior for non-worktree files.
	s.linkFileTabToWorktree(ctx, p.UserID, p.FilePath, p.TabID)
	if s.events != nil {
		s.events.PublishFileTabPathRegistered(p.WorkspaceID, p.TabID, p.FilePath)
	}
	return nil
}

// linkFileTabToWorktree associates a file tab with the worktree that
// contains its on-disk path, if one is tracked. Failure here is
// non-fatal — the file tab is still registered, it just won't count
// toward sibling-tab checks in the last-tab close path.
//
// userID is stamped onto the worktree_tabs row so worktree_tab_liveness can
// scope its FILE-tab join by user: file tab ids are only unique within a user
// (worker_file_tabs is keyed by (user_id, tab_id)), so without it a multi-user
// worker could match a different user's live file tab and mark a strand live.
func (s *FileTabPathStore) linkFileTabToWorktree(ctx context.Context, userID, filePath, tabID string) {
	info, err := queryGitPathInfo(ctx, filepath.Dir(filePath))
	if err != nil || info == nil || !info.IsWorktree {
		return
	}
	wt, err := s.q.GetWorktreeByPath(ctx, pathutil.Canonicalize(info.TopLevel))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("link file tab to worktree: lookup failed",
				"tab_id", tabID, "worktree_path", info.TopLevel, "error", err)
		}
		return
	}
	if err := s.q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: wt.ID,
		TabType:    leapmuxv1.TabType_TAB_TYPE_FILE,
		TabID:      tabID,
		UserID:     userID,
	}); err != nil {
		slog.Warn("link file tab to worktree: insert failed",
			"tab_id", tabID, "worktree_id", wt.ID, "error", err)
	}
}

// BackfillWorktreeLinks links any already-open FILE tabs that live under
// worktreePath to a freshly-created worktree row. FILE tabs only acquire
// their worktree_tabs link at registration time, and only when the
// worktree row already exists (see linkFileTabToWorktree). A worktree
// adopted AFTER a file under it was opened — e.g. created with `git
// worktree add` outside LeapMux, opened as a FILE tab first, then an
// agent/terminal opens inside it and creates the row — would otherwise
// leave that FILE tab unlinked. It then wouldn't count toward the
// worktree's ref-count, so a sibling AGENT/TERMINAL close could
// `git worktree remove` the dir while the editor is still mounted.
//
// Lexically pre-filter to files under worktreePath so we don't probe
// every file tab on the worker, then reuse linkFileTabToWorktree, which
// re-probes git and links only files that genuinely resolve to a tracked
// worktree (so a path in a nested submodule/worktree isn't mis-linked).
// Best-effort: errors are logged, never surfaced — an un-backfilled link
// degrades to today's behavior (the FILE tab just doesn't ref-count).
func (s *FileTabPathStore) BackfillWorktreeLinks(ctx context.Context, worktreePath string) {
	rows, err := s.q.ListAllWorkerFileTabs(ctx)
	if err != nil {
		slog.Warn("backfill worktree links: list file tabs", "worktree_path", worktreePath, "error", err)
		return
	}
	canonicalWorktree := pathutil.Canonicalize(worktreePath)
	for _, row := range rows {
		// Canonicalize both sides: worker_file_tabs stores the raw client
		// path, which may differ from the symlink-resolved worktree path.
		if !pathutil.HasPathPrefix(pathutil.Canonicalize(filepath.Dir(row.FilePath)), canonicalWorktree) {
			continue
		}
		s.linkFileTabToWorktree(ctx, row.UserID, row.FilePath, row.TabID)
	}
}

// Get returns the workspace_id and file_path for a tab, or
// ErrFileTabPathNotFound if absent.
//
// A blank userID is refused rather than bound: worker_file_tabs is keyed by
// (user_id, tab_id), so the owner is half the identity of the row being read.
// Register never writes a blank owner, so no legitimate row can be reached with
// one -- but binding it would turn a caller that lost track of the tenant into a
// silent lookup against whatever rows a future blank-owner write left behind.
// Mirrors Register's own required-field guard.
//
// The refusal goes through userid.New rather than a hand-written `== ""` so it
// is the SAME guard the hub dialects bind their owner columns through -- the
// repo-wide store-bind rule in internal/audit recognises a minted-or-refused id
// and nothing else. Callers hold the owner as a plain string here (the handler
// projects its userid.UserID, the reconciler reads a column), so New is the
// spelling of that guard on this side; owner.String() is the unwrap.
//
// The tabID check folds into the same disjunct: the audit rule walks `||`
// operands, so one refusal reads as one statement. (It did not always -- the
// rule once matched a bare `!ok` only, and this guard was split in two purely
// to stay visible to it.)
func (s *FileTabPathStore) Get(ctx context.Context, userID, tabID string) (workspaceID, filePath string, err error) {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return "", "", fmt.Errorf("get file tab path: required field empty")
	}
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrFileTabPathNotFound
		}
		return "", "", err
	}
	return row.WorkspaceID, row.FilePath, nil
}

// RevokeRow deletes the worker_file_tab row and broadcasts
// FileTabPathRevoked. It is the file-tab analog of the per-type DB
// close performed by Queries.CloseAgent / Queries.CloseTerminal — the
// worktree-association drop is intentionally NOT done here so the
// RevokeFileTabPath handler can drive the unified closeTabCommon flow
// that handles the worktree-tab link (and optional `git worktree
// remove`) consistently across AGENT, TERMINAL, and FILE.
//
// Returns ErrFileTabPathNotFound when no row exists. A blank userID is refused
// for the same reason as Get -- and here the stakes are higher, since the bound
// predicate drives a DELETE.
func (s *FileTabPathStore) RevokeRow(ctx context.Context, userID, tabID string) error {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return fmt.Errorf("revoke file tab path: required field empty")
	}
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFileTabPathNotFound
		}
		return err
	}
	if err := s.q.DeleteWorkerFileTab(ctx, db.DeleteWorkerFileTabParams{UserID: owner.String(), TabID: tabID}); err != nil {
		return fmt.Errorf("delete worker_file_tab: %w", err)
	}
	if s.events != nil {
		s.events.PublishFileTabPathRevoked(row.WorkspaceID, tabID)
	}
	return nil
}

// Relocate moves a file tab to a new workspace. Emits FileTabPathRevoked
// on the source workspace's private-event stream and
// FileTabPathRegistered on the destination workspace's stream — there
// is no "Relocated" event so destination workspace_id is never leaked
// to source-only watchers.
//
// A blank userID is refused for the same reason as Get -- the bound predicate
// drives an UPDATE that moves a row between workspaces.
func (s *FileTabPathStore) Relocate(ctx context.Context, userID, tabID, newWorkspaceID string) error {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return fmt.Errorf("relocate file tab path: required field empty")
	}
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFileTabPathNotFound
		}
		return err
	}
	if newWorkspaceID == "" {
		return fmt.Errorf("relocate file tab path: new_workspace_id is empty")
	}
	if row.WorkspaceID == newWorkspaceID {
		return nil
	}
	if err := s.q.UpdateWorkerFileTabWorkspace(ctx, db.UpdateWorkerFileTabWorkspaceParams{
		WorkspaceID: newWorkspaceID,
		UserID:      owner.String(),
		TabID:       tabID,
	}); err != nil {
		return fmt.Errorf("update worker_file_tab.workspace_id: %w", err)
	}
	if s.events != nil {
		s.events.PublishFileTabPathRevoked(row.WorkspaceID, tabID)
		s.events.PublishFileTabPathRegistered(newWorkspaceID, tabID, row.FilePath)
	}
	return nil
}

// SnapshotForWorkspace returns the FileTabPathRegistered events the
// private-event subscribe path replays before going live, so a
// late-joining client always sees the current path set.
//
// Binds BOTH the caller's owner and the workspace in SQL. workspace_id alone
// would be a real boundary -- it is unique across users and the caller has
// already been gated on access to it -- but binding only it meant walking every
// owner's rows on every subscribe and filtering in Go, and it left the read as
// the one tab-keyed path that did not name its owner. Every sibling (Get /
// RevokeRow / Relocate, the worktree_tabs deletes, and
// OrphanReconciler.reconcileFileTabs) binds the owner, because the
// (user_id, tab_id) key exists precisely for it: file tab ids are unique only
// within a user (see the worker_file_tabs / worktree_tabs DDL). Binding both
// also lets this seek idx_worker_file_tabs_workspace(user_id, workspace_id),
// which no query could use before.
func (s *FileTabPathStore) SnapshotForWorkspace(ctx context.Context, owner userid.UserID, workspaceID string) ([]*leapmuxv1.WorkspacePrivateEvent, error) {
	ownerID, ok := userid.OwnerFilter(owner)
	if !ok {
		// An unminted owner reaches no row. Answer an empty snapshot rather
		// than a whole-table read the caller would then have to filter.
		return nil, nil
	}
	rows, err := s.q.ListWorkerFileTabsByUserAndWorkspace(ctx, db.ListWorkerFileTabsByUserAndWorkspaceParams{
		UserID:      ownerID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.WorkspacePrivateEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, &leapmuxv1.WorkspacePrivateEvent{
			Event: &leapmuxv1.WorkspacePrivateEvent_FileTabPathRegistered{
				FileTabPathRegistered: &leapmuxv1.FileTabPathRegistered{
					TabId:       r.TabID,
					WorkspaceId: r.WorkspaceID,
					FilePath:    r.FilePath,
				},
			},
		})
	}
	return out, nil
}

// RegisterFileTabPathParams is the input shape for Register.
type RegisterFileTabPathParams struct {
	UserID      string
	TabID       string
	WorkspaceID string
	FilePath    string
}

// ErrFileTabPathNotFound is returned when the requested tab has no
// row in worker_file_tabs.
var ErrFileTabPathNotFound = errors.New("file_tab_path: not found")
