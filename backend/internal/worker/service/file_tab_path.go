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
// (file_path, working_dir). The hub never sees these rows; clients fetch them
// via WatchWorkerPrivateEvents (which carries both over the existing E2EE
// channel) or one-shot GetFileTabPath.
type FileTabPathStore struct {
	q      *db.Queries
	events *PrivateEventsBus
}

// NewFileTabPathStore returns a store bound to the worker's DB
// queries and the bus where snapshot/live events get published.
func NewFileTabPathStore(q *db.Queries, events *PrivateEventsBus) *FileTabPathStore {
	return &FileTabPathStore{q: q, events: events}
}

// Register persists a (user_id, tab_id, file_path, working_dir) tuple and
// broadcasts FileTabPathRegistered on the owner's private-event stream.
func (s *FileTabPathStore) Register(ctx context.Context, p RegisterFileTabPathParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok || p.TabID == "" || p.FilePath == "" {
		return fmt.Errorf("register file tab path: required field empty")
	}
	// Absolute, and refused here rather than normalized: a relative path has no
	// meaningful base on this side. The worker's own CWD is the only one
	// available and it is never the client's, so `filepath.Dir("notes.txt")`
	// would store "." and hand every reader below -- linkFileTabToWorktree,
	// getTabWorkingDir, the branch-sibling scan -- a `git -C .` that answers for
	// whatever repo the worker process happens to sit in. Both callers already
	// promise absolute (`leapmux remote tab open --path` documents it, and the
	// UI sends a tree path), so this refuses a caller that broke its contract
	// instead of silently binding the tab to the wrong repository.
	if !filepath.IsAbs(p.FilePath) {
		return fmt.Errorf("register file tab path: file_path must be absolute, got %q", p.FilePath)
	}
	workingDir, err := resolveFileTabWorkingDir(p.WorkingDir, p.FilePath)
	if err != nil {
		return fmt.Errorf("register file tab path: %w", err)
	}
	if err := s.q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		UserID:     owner.String(),
		TabID:      p.TabID,
		FilePath:   p.FilePath,
		WorkingDir: workingDir,
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
	// agent/terminal worktree-linkage path: probe the directory once
	// via `git rev-parse`, then exact-match the canonical top-level
	// against the tracked worktrees. Best-effort: a dir outside any
	// tracked worktree leaves the file tab unbound, matching today's
	// behavior for non-worktree files.
	s.linkFileTabToWorktree(ctx, owner.String(), filepath.Dir(p.FilePath), p.TabID)
	if s.events != nil {
		s.events.PublishFileTabPathRegistered(owner, p.TabID, p.FilePath, workingDir)
	}
	return nil
}

// resolveFileTabWorkingDir picks the working dir a file tab is stored with:
// the originating tab's, or the file's own directory when the caller has no
// originating tab to name. The UI always names one; `leapmux remote tab open
// --type=file` names the spawning tab's dir when it runs inside one
// ($LEAPMUX_REMOTE_WORKING_DIR) and nothing when run from a plain shell, which
// is the case the fallback exists for.
//
// Normalizing HERE, once at write time, is what lets every reader --
// getTabWorkingDir, linkFileTabToWorktree, the branch-sibling scan -- just read
// the column. A fallback evaluated per read is the same rule stated in three
// places, and three places is where they drift: the close path would then be
// able to answer for a different directory than the link that decides whether
// that close removes a worktree.
//
// It is literally the same normalizer agents.working_dir and terminals.working_dir
// go through at their own write points (OpenAgent, OpenTerminal) -- only the
// fallback differs, so that is the parameter. Without it a `--working-dir
// '~/proj'` -- which the CLI forwards raw -- is stored literally, and `git -C
// '~/proj'` cannot resolve it, dropping the tab straight back into the "not
// readable as a git repository" degraded close this column exists to eliminate.
// Three writers of the same fact have to agree on what it means, and sharing the
// function is what makes them agree rather than a comment asking them to.
func resolveFileTabWorkingDir(workingDir, filePath string) (string, error) {
	return normalizeWorkingDir(workingDir, filepath.Dir(filePath))
}

// linkFileTabToWorktree associates a file tab with the worktree the directory it
// is given sits in, if one is tracked. Failure here is non-fatal — the file tab
// is still registered, it just won't count toward sibling-tab checks in the
// last-tab close path.
//
// Callers pass the FILE'S OWN directory, not the tab's working_dir, and the two
// answer genuinely different questions. working_dir is the tab's git IDENTITY:
// which branch group it renders in, which branch a push targets — inherited from
// the tab it was opened from, because that is the context the user opened it in.
// This link is a REF-COUNT on a directory that can be deleted: it decides whether
// `git worktree remove` may rm-rf a tree while an editor is still mounted inside
// it. Only physical containment can answer that. Keying it on working_dir instead
// silently unlinks every file opened from one checkout into another — an agent in
// the main repo opening a file inside a linked worktree — and CountWorktreeTabs
// then underreports, which is exactly the "editor ENOENTs on a dir that was just
// rm-rf'd" hazard Register's comment above says this linkage exists to prevent.
//
// userID is stamped onto the worktree_tabs row so worktree_tab_liveness can
// scope its FILE-tab join by user: file tab ids are only unique within a user
// (worker_file_tabs is keyed by (user_id, tab_id)), so without it a multi-user
// worker could match a different user's live file tab and mark a strand live.
func (s *FileTabPathStore) linkFileTabToWorktree(ctx context.Context, userID, fileDir, tabID string) {
	info, err := queryGitPathInfo(ctx, fileDir)
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
// Lexically pre-filter to tabs whose FILE sits under worktreePath so we
// don't probe every file tab on the worker, then reuse linkFileTabToWorktree,
// which re-probes git and links only dirs that genuinely resolve to a tracked
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
		// The FILE'S OWN directory on both the filter and the probe, matching
		// what Register links on -- this is a ref-count on a deletable directory,
		// so it follows where the file physically is, not the working_dir the tab
		// inherited from whatever opened it (see linkFileTabToWorktree).
		// Canonicalize both sides: worker_file_tabs stores the raw client path,
		// which may differ from the symlink-resolved worktree path.
		fileDir := filepath.Dir(row.FilePath)
		if !pathutil.HasPathPrefix(pathutil.Canonicalize(fileDir), canonicalWorktree) {
			continue
		}
		s.linkFileTabToWorktree(ctx, row.UserID, fileDir, row.TabID)
	}
}

// Get returns the stored row for a tab, or ErrFileTabPathNotFound if absent.
//
// Both columns come back together rather than through a path-only read plus a
// second working-dir read: they are one fact about one tab, and the two callers
// (the GetFileTabPath handler answering a client, getTabWorkingDir answering
// the git paths) would otherwise be able to observe them from different rows
// across a concurrent re-register.
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
func (s *FileTabPathStore) Get(ctx context.Context, userID, tabID string) (FileTabLocation, error) {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return FileTabLocation{}, fmt.Errorf("get file tab path: required field empty")
	}
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FileTabLocation{}, ErrFileTabPathNotFound
		}
		return FileTabLocation{}, err
	}
	return FileTabLocation{FilePath: row.FilePath, WorkingDir: row.WorkingDir}, nil
}

// FileTabLocation is where a file tab points: the file it shows, and the
// working dir it answers git questions from. See the working_dir column comment
// in the worker's initial migration for why the second is not derived from the
// first.
type FileTabLocation struct {
	FilePath   string
	WorkingDir string
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
	// One statement, and its affected-row count IS the existence answer. This used
	// to SELECT the row first, which was load-bearing only while the revoke event
	// carried the row's workspace_id; now it would be a second round trip whose
	// only job is a check the DELETE already reports -- and a TOCTOU window, since
	// a concurrent revoke between the two made this a no-op that still reported
	// success and published a duplicate FileTabPathRevoked.
	res, err := s.q.DeleteWorkerFileTab(ctx, db.DeleteWorkerFileTabParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		return fmt.Errorf("delete worker_file_tab: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete worker_file_tab: %w", err)
	}
	if affected == 0 {
		return ErrFileTabPathNotFound
	}
	if s.events != nil {
		s.events.PublishFileTabPathRevoked(owner, tabID)
	}
	return nil
}

// SnapshotForOwner returns the FileTabPathRegistered events the private-event
// subscribe path replays before going live, so a late-joining client always
// sees the current path set.
//
// The owner is the whole predicate, and it is bound in SQL rather than
// filtered in Go: every sibling (Get / RevokeRow, the worktree_tabs deletes,
// and OrphanReconciler.reconcileFileTabs) binds it, because the
// (user_id, tab_id) key exists precisely for it -- file tab ids are unique
// only within a user (see the worker_file_tabs / worktree_tabs DDL).
func (s *FileTabPathStore) SnapshotForOwner(ctx context.Context, owner userid.UserID) ([]*leapmuxv1.WorkerPrivateEvent, error) {
	ownerID, ok := userid.OwnerFilter(owner)
	if !ok {
		// An unminted owner reaches no row. Answer an empty snapshot rather
		// than a whole-table read the caller would then have to filter.
		return nil, nil
	}
	rows, err := s.q.ListWorkerFileTabsByUser(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.WorkerPrivateEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_FileTabPathRegistered{
				FileTabPathRegistered: &leapmuxv1.FileTabPathRegistered{
					TabId:      r.TabID,
					FilePath:   r.FilePath,
					WorkingDir: r.WorkingDir,
				},
			},
		})
	}
	return out, nil
}

// RegisterFileTabPathParams is the input shape for Register. WorkingDir is
// optional; see resolveFileTabWorkingDir for what an empty one resolves to.
type RegisterFileTabPathParams struct {
	UserID     string
	TabID      string
	FilePath   string
	WorkingDir string
}

// ErrFileTabPathNotFound is returned when the requested tab has no
// row in worker_file_tabs.
var ErrFileTabPathNotFound = errors.New("file_tab_path: not found")
