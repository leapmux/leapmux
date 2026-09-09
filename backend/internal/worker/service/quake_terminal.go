// The LIFETIME of a quake terminal: the reference count that decides when one
// ends, and the reapers that share it.
//
// A quake terminal belongs to a (worker, working directory) pair rather than to
// a tab, so nothing owns it and nothing closes it explicitly. It ends when its
// directory has no live tab left, and THAT question is what this file owns --
// `dirHasLiveTab` is the one predicate, and the close paths, the archive sweep
// and the orphan reconciler all ask it here.
//
// The rest of the quake terminal's behaviour deliberately stays with the
// terminal handlers in `terminal.go`, beside the code it decides for: the
// open-time refusals and the adopt, the restart refusal, and the shell-exit
// hook. Splitting a helper from its only caller to group it by topic would
// trade one kind of locality for another.
package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// dirTabRef is one OPEN tab that works in some directory.
//
// A record rather than a bare id, because the reference count needs all of it:
// the directory to group by, the identity to exclude a tab that is closing
// right now, and the archive flag because an ARCHIVED tab is not a reference.
// A user cannot reach the panel from a tab in an archived workspace -- the
// frontend refuses a cold open there -- so a directory whose every tab is
// archived has nobody left to type into its shell.
//
// The identity is (Kind, ID) and NOT the id alone. An agent or terminal id is a
// server-minted nanoid and globally unique, but a payload-backed tab id is
// minted client-side and is unique only within ONE account -- the same reason
// worktree_tab_liveness and collectTabDirs both match on the pair. Keyed on the
// id alone, a viewer closing in one account would exclude a second account's
// still-open viewer of the same directory and take a shell it is using.
type dirTabRef struct {
	Kind              leapmuxv1.TabType
	ID                string
	WorkingDir        string
	WorkspaceArchived bool
}

// tabRefKey identifies one tab across the kinds whose id spaces can overlap.
// The separator is a NUL, which no tab id and no tab type can contain.
func tabRefKey(kind leapmuxv1.TabType, id string) string {
	return strconv.Itoa(int(kind)) + "\x00" + id
}

// key is this tab's identity. See dirTabRef for why the kind is part of it.
func (t dirTabRef) key() string { return tabRefKey(t.Kind, t.ID) }

// dirHasLiveTab reports whether any tab in `tabs` is a live reference: open,
// not archived, and not the one whose close is running right now.
//
// `excludeKey` is a tabRefKey, or "" when nothing is closing.
//
// THE rule, in one function. The synchronous close paths, the archive apply and
// the orphan reconciler all reap a quake terminal, and three spellings of "is
// this directory still in use?" is how they would come to disagree about a
// shell the user is typing into.
func dirHasLiveTab(tabs []dirTabRef, excludeKey string) bool {
	for _, tab := range tabs {
		if tab.key() != excludeKey && !tab.WorkspaceArchived {
			return true
		}
	}
	return false
}

// openTabsInWorkingDirs lists every open TAB that works in one of `dirs`.
//
// ONE query, over the `tab_locations` view. That view already owns which tables
// hold tabs, what OPEN means for each of them, and which kinds carry an owner --
// and its own doc says it exists so no reader restates those rules. Three
// hand-written per-type SELECTs here had to agree with it by hand, and a fifth
// tab kind would have needed a fourth of them.
//
// FILE and IMAGE tabs COUNT, and that is what makes the worker agree with the
// panel the user sees. `quakeKeyForTab` in the browser accepts any tab that
// carries a worker and a working directory, so Ctrl+` opens the panel over a
// file viewer exactly as it does over an agent. A count that left those tabs
// out reported "nobody works here" for a directory the user is looking at, and
// the reap closed the shell under them.
//
// A free function rather than a Service method, because the ORPHAN RECONCILER
// asks the same question and holds only `queries` -- see reconcileQuakeTerminals.
// One implementation is what stops the synchronous close path and the backstop
// pass disagreeing about which directory still has a tab.
func openTabsInWorkingDirs(ctx context.Context, q *db.Queries, dirs []string) ([]dirTabRef, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	rows, err := q.ListOpenTabsByWorkingDirs(ctx, dirs)
	if err != nil {
		return nil, err
	}
	out := make([]dirTabRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, dirTabRef{
			Kind:              row.TabType,
			ID:                row.TabID,
			WorkingDir:        row.WorkingDir,
			WorkspaceArchived: row.WorkspaceArchived,
		})
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
// The action is pinned to UNSPECIFIED rather than forwarded: the user's choice
// about the CLOSING TAB's worktree is not a choice about the shell, so a REMOVE
// there must not reach this close.
//
// A quake terminal CAN hold a worktree link of its own, despite what that
// pinning might suggest. OpenTerminal refuses only a git-mode MUTATION, so a
// panel opened inside an existing linked worktree runs the use-current path,
// attaches to it, and gets a worktree_tabs row like any tab. `linkPolicy` is
// therefore still forwarded: it decides whether that row is dropped or left as
// a strand for the reconciler, and a deleted workspace wants the strand.
func (svc *Service) closeQuakeTerminalIfUnused(userID, workingDir string, closingKind leapmuxv1.TabType, closingTabID string, linkPolicy worktreeLinkPolicy) {
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
	if svc.workingDirStillHasTab(workingDir, tabRefKey(closingKind, closingTabID)) {
		return
	}
	svc.closeTerminalTabCommon(userID, quake.ID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, linkPolicy)
}

// workingDirStillHasTab reports whether any LIVE tab other than the one
// `excludeKey` identifies works in `workingDir`. See dirHasLiveTab for what
// "live" means, and dirTabRef for why the key is a (kind, id) pair.
//
// A read failure answers TRUE, which keeps the shell. The alternative is to
// kill a terminal the user may be typing in because one query failed, and the
// orphan reconciler re-asks the same question on its next pass -- so the
// conservative answer costs a delay, and the other one costs work.
func (svc *Service) workingDirStillHasTab(workingDir, excludeKey string) bool {
	tabs, err := openTabsInWorkingDirs(bgCtx(), svc.Queries, []string{workingDir})
	if err != nil {
		slog.Error("failed to list the tabs of a working directory", "working_dir", workingDir, "error", err)
		return true
	}
	return dirHasLiveTab(tabs, excludeKey)
}

// unusedQuakeTerminals returns every open quake terminal whose directory has no
// live tab left, and the tabs of each quake directory that it read on the way.
//
// THE sweep, in one function, for the same reason dirHasLiveTab is THE
// predicate: the archive path and the orphan reconciler both ask this, and two
// spellings of "which shells have nobody left?" is how they would come to reap
// different rows. Each caller keeps only what genuinely differs -- the archive
// path filters by what its request changed and closes at once, the reconciler
// waits out a grace window first.
func unusedQuakeTerminals(ctx context.Context, q *db.Queries) (rows []db.ListOpenQuakeTerminalsRow, byDir map[string][]dirTabRef, err error) {
	all, err := q.ListOpenQuakeTerminals(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	dirs := make([]string, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, row := range all {
		if _, dup := seen[row.WorkingDir]; dup {
			continue
		}
		seen[row.WorkingDir] = struct{}{}
		dirs = append(dirs, row.WorkingDir)
	}
	// ONE query for every directory at once: asking per row would run three
	// statements per open quake terminal, and the answer has the same shape
	// either way.
	tabs, err := openTabsInWorkingDirs(ctx, q, dirs)
	if err != nil {
		return nil, nil, err
	}
	byDir = make(map[string][]dirTabRef, len(dirs))
	for _, tab := range tabs {
		byDir[tab.WorkingDir] = append(byDir[tab.WorkingDir], tab)
	}
	unused := make([]db.ListOpenQuakeTerminalsRow, 0, len(all))
	for _, row := range all {
		if dirHasLiveTab(byDir[row.WorkingDir], "") {
			continue
		}
		unused = append(unused, row)
	}
	return unused, byDir, nil
}

// closeUnusedQuakeTerminals closes the quake terminal of every directory that
// `changed` emptied of live tabs.
//
// The sweep the ARCHIVE path runs. `changed` holds the tabs whose archive flag
// actually MOVED, and a row is closed only when one of them worked in its
// directory: an archive changes which tabs count as references, so a directory
// this request did not touch cannot have become unused because of it. Sweeping
// every open quake row instead closed a shell in an unrelated directory --
// immediately, ahead of the grace window that reconcileQuakeTerminals gives the
// same row, because a tab close and its quake reap arrive as two separate
// writes.
func (svc *Service) closeUnusedQuakeTerminals(ctx context.Context, changed archiveTabSet) {
	touched := make(map[string]struct{}, len(changed.agents)+len(changed.terminals)+len(changed.payloads))
	for _, id := range changed.agents {
		touched[tabRefKey(leapmuxv1.TabType_TAB_TYPE_AGENT, id)] = struct{}{}
	}
	for _, id := range changed.terminals {
		touched[tabRefKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, id)] = struct{}{}
	}
	// A payload row's kind comes back from the view as FILE or IMAGE, and the
	// archive set does not record which. Both spellings go in: the two share one
	// id space, so at most one of them can match a real row.
	for _, ref := range changed.payloads {
		touched[tabRefKey(leapmuxv1.TabType_TAB_TYPE_FILE, ref.tabID)] = struct{}{}
		touched[tabRefKey(leapmuxv1.TabType_TAB_TYPE_IMAGE, ref.tabID)] = struct{}{}
	}
	if len(touched) == 0 {
		return
	}
	rows, byDir, err := unusedQuakeTerminals(ctx, svc.Queries)
	if err != nil {
		slog.Warn("failed to list the quake terminals for the unused sweep", "error", err)
		return
	}
	for _, row := range rows {
		// byDir holds every tab of this directory, archived ones included, so a
		// tab this request just archived is still here to match. That is the
		// case the sweep exists for: the directory is unused BECAUSE of it.
		if !anyTabIn(byDir[row.WorkingDir], touched) {
			continue
		}
		svc.closeTerminalTabCommon("", row.ID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	}
}

// anyTabIn reports whether any tab of one directory is in `keys`, a set of
// tabRefKey values.
func anyTabIn(tabs []dirTabRef, keys map[string]struct{}) bool {
	for _, tab := range tabs {
		if _, hit := keys[tab.key()]; hit {
			return true
		}
	}
	return false
}
