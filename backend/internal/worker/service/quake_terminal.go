package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// boolToInt64 spells a Go bool as the INTEGER SQLite stores. Every boolean
// column in this schema is an INTEGER, so the conversion lives in one place
// rather than as a conditional at each write site.
func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// dirTabRef is one OPEN tab that works in some directory.
//
// A record rather than a bare id, because the reference count needs all three
// fields: the directory to group by, the id to exclude a tab that is closing
// right now, and the archive flag because an ARCHIVED tab is not a reference.
// A user cannot reach the panel from a tab in an archived workspace -- the
// frontend refuses a cold open there -- so a directory whose every tab is
// archived has nobody left to type into its shell.
type dirTabRef struct {
	ID                string
	WorkingDir        string
	WorkspaceArchived bool
}

// dirHasLiveTab reports whether any tab in `tabs` is a live reference: open,
// not archived, and not the one whose close is running right now.
//
// THE rule, in one function. The synchronous close paths, the archive apply and
// the orphan reconciler all reap a quake terminal, and three spellings of "is
// this directory still in use?" is how they would come to disagree about a
// shell the user is typing into.
func dirHasLiveTab(tabs []dirTabRef, excludeTabID string) bool {
	for _, tab := range tabs {
		if tab.ID != excludeTabID && !tab.WorkspaceArchived {
			return true
		}
	}
	return false
}

// openTabsInWorkingDirs lists every open TAB that works in one of `dirs`.
//
// Two queries because the two tables are the two kinds of tab that own a
// working directory, and merging them here rather than in SQL keeps each query
// readable and each `sqlc.slice` unambiguous. Root agents only, and quake rows
// excluded -- see the note on each query for why a subagent and a quake
// terminal must not count as references.
//
// FILE and IMAGE tabs are deliberately absent. Their working directory lives in
// a worker-stored TabPayload the hub can never see, and they own no process, so
// a viewer left open in a directory is not a reason to keep a shell running
// there.
//
// A free function rather than a Service method, because the ORPHAN RECONCILER
// asks the same question and holds only `queries` -- see reconcileQuakeTerminals.
// One implementation is what stops the synchronous close path and the backstop
// pass disagreeing about which directory still has a tab.
func openTabsInWorkingDirs(ctx context.Context, q *db.Queries, dirs []string) ([]dirTabRef, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	agentRows, err := q.ListOpenRootAgentsByWorkingDirs(ctx, dirs)
	if err != nil {
		return nil, err
	}
	terminalRows, err := q.ListOpenTerminalTabsByWorkingDirs(ctx, dirs)
	if err != nil {
		return nil, err
	}
	out := make([]dirTabRef, 0, len(agentRows)+len(terminalRows))
	for _, row := range agentRows {
		out = append(out, dirTabRef{ID: row.ID, WorkingDir: row.WorkingDir, WorkspaceArchived: row.WorkspaceArchived != 0})
	}
	for _, row := range terminalRows {
		out = append(out, dirTabRef{ID: row.ID, WorkingDir: row.WorkingDir, WorkspaceArchived: row.WorkspaceArchived != 0})
	}
	return out, nil
}

// closeQuakeTerminalIfUnused closes the quake terminal of `workingDir` once no
// OPEN tab is left there besides `closingTabID`, the tab whose close is running
// right now.
//
// The exclusion is what makes this callable from a teardown: closeTabCommon
// runs the teardown BEFORE it stamps closed_at, so the closing tab is still
// open in the table and would otherwise count as a reference to its own
// directory and keep the shell alive for ever.
//
// Called from the ONE funnel every close passes through -- rootTeardown for an
// agent and closeTerminalTabCommon's teardown for a terminal tab -- so the
// online RPC, the orphan reap and the workspace cleanup cannot disagree. The
// orphan reconciler's own pass is the backstop for the close this never sees
// (a row the hub forgot, a worker that died mid-close).
//
// The action is pinned to UNSPECIFIED rather than forwarded: a quake terminal
// holds no worktree link of its own, so the user's choice about the closing
// tab's worktree is not a choice about the shell.
func (svc *Service) closeQuakeTerminalIfUnused(userID, workingDir, closingTabID string, linkPolicy worktreeLinkPolicy) {
	if workingDir == "" {
		return
	}
	quake, err := svc.Queries.GetOpenQuakeTerminalByWorkingDir(bgCtx(), workingDir)
	if err != nil {
		// sql.ErrNoRows is the ordinary "this directory has no quake terminal"
		// answer and stays silent. Every other failure would leave a live PTY
		// behind with its last tab gone, and only the orphan reconciler's next
		// pass reclaims it -- so it is logged rather than swallowed, the way
		// every other DB failure on the close paths is.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("failed to look up the quake terminal for a tab close", "working_dir", workingDir, "error", err)
		}
		return
	}
	if svc.workingDirStillHasTab(workingDir, closingTabID) {
		return
	}
	svc.closeTerminalTabCommon(userID, quake.ID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, linkPolicy)
}

// workingDirStillHasTab reports whether any LIVE tab other than `excludeTabID`
// works in `workingDir`. See dirHasLiveTab for what "live" means.
//
// A read failure answers TRUE, which keeps the shell. The alternative is to
// kill a terminal the user may be typing in because one query failed, and the
// orphan reconciler re-asks the same question on its next pass -- so the
// conservative answer costs a delay, and the other one costs work.
func (svc *Service) workingDirStillHasTab(workingDir, excludeTabID string) bool {
	tabs, err := openTabsInWorkingDirs(bgCtx(), svc.Queries, []string{workingDir})
	if err != nil {
		slog.Error("failed to list the tabs of a working directory", "working_dir", workingDir, "error", err)
		return true
	}
	return dirHasLiveTab(tabs, excludeTabID)
}

// closeUnusedQuakeTerminals closes every open quake terminal whose directory
// has no live tab left.
//
// The sweep the ARCHIVE path runs, and it takes no tab list: an archive changes
// which tabs count as references, not which directories exist, so asking the
// question of every open quake row is both simpler and complete. There is one
// row per directory a user opened a panel in, so the pass is small.
//
// `excludeTabID` is the tab whose close is running right now, or "" when
// nothing is closing -- see closeQuakeTerminalIfUnused for why the exclusion
// exists at all.
func (svc *Service) closeUnusedQuakeTerminals(ctx context.Context, excludeTabID string) {
	rows, err := svc.Queries.ListOpenQuakeTerminals(ctx)
	if err != nil {
		slog.Warn("failed to list the quake terminals for the unused sweep", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	dirs := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, dup := seen[row.WorkingDir]; dup {
			continue
		}
		seen[row.WorkingDir] = struct{}{}
		dirs = append(dirs, row.WorkingDir)
	}
	// ONE query for every directory at once: asking per row would run two
	// statements per open quake terminal, and the answer has the same shape
	// either way.
	tabs, err := openTabsInWorkingDirs(ctx, svc.Queries, dirs)
	if err != nil {
		slog.Warn("failed to list the tabs of the quake directories", "error", err)
		return
	}
	byDir := make(map[string][]dirTabRef, len(dirs))
	for _, tab := range tabs {
		byDir[tab.WorkingDir] = append(byDir[tab.WorkingDir], tab)
	}
	for _, row := range rows {
		if dirHasLiveTab(byDir[row.WorkingDir], excludeTabID) {
			continue
		}
		svc.closeTerminalTabCommon("", row.ID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	}
}
