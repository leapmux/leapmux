package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// archiveTabSet holds the tab ids one archive request touches, split by the
// table that stores them. It is a named record rather than two `[]string`
// results, because two same-typed slices can be swapped at a call site and the
// compiler cannot tell.
type archiveTabSet struct {
	agents    []string
	terminals []string
	// payloads holds the FILE and IMAGE tabs, as (owner, id) pairs.
	//
	// A PAIR, unlike the other two: a payload-backed tab id is minted
	// client-side and unique only within one account, so worker_tab_payloads is
	// keyed (user_id, tab_id) and the id alone would match another account's
	// row. Agent and terminal ids are server-minted and globally unique.
	//
	// Such a tab owns no process, so nothing pauses or stops for it. The flag
	// exists only so the quake reference count can tell a viewer the user can
	// still reach from one nobody can open the panel from.
	payloads []payloadTabRef
}

// payloadTabRef addresses one payload-backed tab. See archiveTabSet.payloads.
type payloadTabRef struct {
	userID string
	tabID  string
}

// ApplyTabArchiveState applies the Hub's authoritative archive state to this
// Worker's local rows. It returns the agents whose state changed to active, so
// the caller can schedule their resume.
//
// The orphan reconciler is the only caller. There is deliberately no RPC for
// this: the Hub nudges a Worker whenever a workspace's archive state or a tab's
// workspace membership changes, and the reconcile pass that follows reads the
// Hub's own answer. A client-driven variant would be a second delivery path
// for one effect, and it would apply only while that client was connected.
func (svc *Service) ApplyTabArchiveState(
	ctx context.Context,
	state leapmuxv1.WorkspaceArchiveState,
	tabs []*leapmuxv1.TabRef,
) ([]string, error) {
	flag, err := workspaceArchiveFlag(state)
	if err != nil {
		return nil, err
	}
	requested, err := archiveTabIDs(tabs)
	if err != nil {
		return nil, err
	}

	changed, err := svc.writeArchiveFlagsAndTearDown(ctx, state, flag, requested)
	if err != nil {
		return nil, err
	}

	// A QUAKE terminal takes no archive flag of its own, and this is where that
	// decision pays for itself.
	//
	// It is not a tab: the Hub never lists one, so it could only ever reach the
	// archive path by being expanded onto the request from the tabs around it.
	// That expansion has to answer "is this shell's workspace being archived?",
	// and a shell keyed on (worker, directory) can belong to tabs in SEVERAL
	// workspaces -- so the honest version of it was a conditional that included
	// the row only when every tab in its directory was already archived or in
	// this very request.
	//
	// The reference count answers the same question without knowing about
	// workspaces at all: a directory with no open, non-archived tab has nobody
	// who can reach its panel, so its shell ends. A directory that still has one
	// -- in ANY workspace -- keeps it, which is exactly what stops archiving one
	// workspace from stopping a shell another one is typing into.
	//
	// It CLOSES rather than archives, and that is not a shortcut: a quake
	// terminal has no restart contract, so there is no state an unarchive could
	// restore. The next open in that directory spawns a fresh shell, which is
	// what an unarchived workspace wants anyway.
	//
	// Scoped to the tabs whose flag actually MOVED, and skipped entirely when
	// none did. This pass runs on every reconcile that reports any archived or
	// any active tab, which is nearly every pass of a busy worker -- and an
	// unscoped sweep there closed a shell in a directory this request never
	// named, immediately, ahead of the grace window that reconcileQuakeTerminals
	// exists to give it.
	//
	// Released the locks first: the rows this sweep closes are OTHER tabs than
	// the ones the request names, so holding their locks through a PTY teardown
	// buys nothing.
	svc.closeUnusedQuakeTerminals(ctx, changed)

	if state == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED {
		return nil, nil
	}
	return changed.agents, nil
}

// writeArchiveFlagsAndTearDown writes the flags and runs the per-tab teardown
// with this request's locks held, then releases them.
//
// One function because the pair must be atomic per tab: see archiveTabLocks. It
// exists so the quake sweep its caller runs next is OUTSIDE the locks, which it
// can be because that sweep acts on rows this request does not name.
func (svc *Service) writeArchiveFlagsAndTearDown(
	ctx context.Context,
	state leapmuxv1.WorkspaceArchiveState,
	flag bool,
	requested archiveTabSet,
) (archiveTabSet, error) {
	unlock := svc.lockArchiveTabs(requested)
	defer unlock()

	changed, err := svc.persistArchiveFlags(ctx, flag, requested)
	if err != nil {
		return archiveTabSet{}, err
	}
	if state == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED {
		// The Hub lists root tabs only. A subagent owns no tab, so the pause
		// must walk the subtree, exactly as the process-exit path does.
		for _, agentID := range requested.agents {
			for _, queueAgentID := range svc.agentSubtreeIDs(agentID) {
				if _, queueErr := svc.InputQueue.PauseForArchive(bgCtx(), queueAgentID); queueErr != nil {
					slog.Warn("archive: pause agent input queue", "agent_id", queueAgentID, "error", queueErr)
				}
			}
		}
		svc.stopArchivedTabs(changed)
		return changed, nil
	}
	for _, agentID := range requested.agents {
		for _, queueAgentID := range svc.agentSubtreeIDs(agentID) {
			if _, queueErr := svc.InputQueue.ResumeAfterArchive(bgCtx(), queueAgentID); queueErr != nil {
				slog.Warn("unarchive: resume agent input queue", "agent_id", queueAgentID, "error", queueErr)
			}
		}
	}
	return changed, nil
}

// lockArchiveTabs takes every lock this request needs and returns their
// release. It locks in SORTED order, which is what makes two overlapping
// requests over intersecting tab sets unable to deadlock: both walk the same
// global order, so neither can hold a lock the other wants next.
func (svc *Service) lockArchiveTabs(tabs archiveTabSet) func() {
	keys := make([]string, 0, len(tabs.agents)+len(tabs.terminals)+len(tabs.payloads))
	for _, id := range tabs.agents {
		keys = append(keys, "agent:"+id)
	}
	for _, id := range tabs.terminals {
		keys = append(keys, "terminal:"+id)
	}
	for _, ref := range tabs.payloads {
		keys = append(keys, "payload:"+ref.userID+"\x00"+ref.tabID)
	}
	sort.Strings(keys)
	locks := make([]*sync.Mutex, 0, len(keys))
	for _, key := range keys {
		v, _ := svc.archiveTabLocks.LoadOrStore(key, &sync.Mutex{})
		mu := v.(*sync.Mutex)
		mu.Lock()
		locks = append(locks, mu)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

// persistArchiveFlags writes the flag for every requested tab in one
// transaction, and reports the tabs whose stored value actually changed.
//
// The caller holds this request's per-tab locks, so no other archive operation
// can write these rows between the read and the write.
func (svc *Service) persistArchiveFlags(ctx context.Context, flag bool, requested archiveTabSet) (archiveTabSet, error) {
	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return archiveTabSet{}, fmt.Errorf("start archive-state transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	queries := svc.Queries.WithTx(tx)

	changedAgents, err := applyArchiveFlag(ctx, requested.agents, "agent", func(ctx context.Context, id string) (int64, error) {
		return queries.SetAgentWorkspaceArchived(ctx, db.SetAgentWorkspaceArchivedParams{
			WorkspaceArchived: flag,
			ID:                id,
		})
	})
	if err != nil {
		return archiveTabSet{}, err
	}
	changedTerminals, err := applyArchiveFlag(ctx, requested.terminals, "terminal", func(ctx context.Context, id string) (int64, error) {
		return queries.SetTerminalWorkspaceArchived(ctx, db.SetTerminalWorkspaceArchivedParams{
			WorkspaceArchived: flag,
			ID:                id,
		})
	})
	if err != nil {
		return archiveTabSet{}, err
	}
	changedPayloads := make([]payloadTabRef, 0, len(requested.payloads))
	for _, ref := range requested.payloads {
		// Through userid.New, with an early return on the miss: a zero id
		// unwraps to "", which MATCHES every blank-owner row instead of none.
		// archiveTabIDs already refuses an empty owner, so reaching this is a
		// caller that built an archiveTabSet by hand.
		owner, ok := userid.New(ref.userID)
		if !ok {
			return archiveTabSet{}, fmt.Errorf("payload tab %s has an unmintable owner", ref.tabID)
		}
		rows, setErr := queries.SetPayloadTabWorkspaceArchived(ctx, db.SetPayloadTabWorkspaceArchivedParams{
			WorkspaceArchived: flag,
			UserID:            owner.String(),
			TabID:             ref.tabID,
		})
		if setErr != nil {
			return archiveTabSet{}, fmt.Errorf("set payload tab %s archive state: %w", ref.tabID, setErr)
		}
		if rows > 0 {
			changedPayloads = append(changedPayloads, ref)
		}
	}
	if err := tx.Commit(); err != nil {
		return archiveTabSet{}, fmt.Errorf("commit archive state: %w", err)
	}
	committed = true
	return archiveTabSet{agents: changedAgents, terminals: changedTerminals, payloads: changedPayloads}, nil
}

// applyArchiveFlag runs one table's update loop and returns the ids whose row
// changed. The statements skip a closed row, so an id that names one is absent
// from the result and takes no teardown.
func applyArchiveFlag(ctx context.Context, ids []string, kind string, set func(context.Context, string) (int64, error)) ([]string, error) {
	changed := make([]string, 0, len(ids))
	for _, id := range ids {
		rows, err := set(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("set %s %s archive state: %w", kind, id, err)
		}
		if rows > 0 {
			changed = append(changed, id)
		}
	}
	return changed, nil
}

func workspaceArchiveFlag(state leapmuxv1.WorkspaceArchiveState) (bool, error) {
	switch state {
	case leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE:
		return false, nil
	case leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED:
		return true, nil
	default:
		return false, fmt.Errorf("archive_state must be ACTIVE or ARCHIVED")
	}
}

// archiveTabIDs sorts the requested tabs by the table that stores them, and
// REFUSES a tab whose type it cannot classify.
//
// The refusal is correct here and belongs to the RPC alone. A caller that
// reads the Hub's tab list instead — the orphan reconciler — must drop such a
// row and converge over the rest, because one row it cannot classify would
// otherwise stop every reap and every resume on this worker. The reconciler
// therefore filters before it calls, and this function stays strict.
func archiveTabIDs(tabs []*leapmuxv1.TabRef) (archiveTabSet, error) {
	agentSeen := make(map[string]struct{})
	terminalSeen := make(map[string]struct{})
	payloadSeen := make(map[payloadTabRef]struct{})
	var set archiveTabSet
	for _, tab := range tabs {
		if tab == nil || tab.GetTabId() == "" {
			continue
		}
		switch tab.GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			if _, exists := agentSeen[tab.GetTabId()]; !exists {
				agentSeen[tab.GetTabId()] = struct{}{}
				set.agents = append(set.agents, tab.GetTabId())
			}
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			if _, exists := terminalSeen[tab.GetTabId()]; !exists {
				terminalSeen[tab.GetTabId()] = struct{}{}
				set.terminals = append(set.terminals, tab.GetTabId())
			}
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
			// Payload-backed tabs own no process, so they take no teardown --
			// but they DO take the flag, because they are references for the
			// quake reference count and an archived tab is not a live one.
			//
			// REFUSED without an owner, rather than written with a blank one: a
			// blank user_id matches no row on a write and another account's row
			// on a read, so a sender that forgot the field must hear about it
			// rather than have its request half-applied.
			if tab.GetUserId() == "" {
				return archiveTabSet{}, fmt.Errorf("tab %q is payload-backed and needs a user_id", tab.GetTabId())
			}
			key := payloadTabRef{userID: tab.GetUserId(), tabID: tab.GetTabId()}
			if _, exists := payloadSeen[key]; !exists {
				payloadSeen[key] = struct{}{}
				set.payloads = append(set.payloads, key)
			}
		case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			return archiveTabSet{}, fmt.Errorf("tab %q has an unspecified type", tab.GetTabId())
		default:
			return archiveTabSet{}, fmt.Errorf("tab %q has unsupported type %s", tab.GetTabId(), tab.GetTabType())
		}
	}
	return set, nil
}

// archivableTabType reports whether archiveTabIDs accepts this type. The
// orphan reconciler asks before it builds a TabRef, so a row the Hub sends
// with a type this Worker does not know drops out of one pass instead of
// failing it.
func archivableTabType(tabType leapmuxv1.TabType) bool {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT,
		leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		leapmuxv1.TabType_TAB_TYPE_FILE,
		leapmuxv1.TabType_TAB_TYPE_IMAGE:
		return true
	case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
		return false
	default:
		return false
	}
}

func (svc *Service) stopArchivedTabs(changed archiveTabSet) {
	agentStarts := make(map[string]*startupEntry, len(changed.agents))
	for _, agentID := range changed.agents {
		agentStarts[agentID] = svc.AgentStartup.cancelForArchive(agentID)
	}
	terminalStarts := make(map[string]*startupEntry, len(changed.terminals))
	for _, terminalID := range changed.terminals {
		terminalStarts[terminalID] = svc.TerminalStartup.cancelForArchive(terminalID)
	}
	// Signal every process before any one drain can block. StopAgent waits for
	// the subprocess to answer its stop signal, so these run concurrently:
	// serially, N agents that ignore the signal cost N times that wait before
	// the first drain even starts.
	var signalled sync.WaitGroup
	for _, agentID := range changed.agents {
		signalled.Add(1)
		go func() {
			defer signalled.Done()
			svc.Agents.StopAgent(agentID)
		}()
	}
	for _, terminalID := range changed.terminals {
		signalled.Add(1)
		go func() {
			defer signalled.Done()
			svc.Terminals.StopTerminal(terminalID)
		}()
	}
	signalled.Wait()

	// Iterate the SLICE, not the map: the broadcasts below reach every watcher
	// in the order the caller listed the tabs, rather than in Go's randomized
	// map order.
	for _, agentID := range changed.agents {
		svc.AgentStartup.waitForFinished(agentStarts[agentID])
		unlock := svc.Agents.LockAgent(agentID)
		svc.Agents.StopAndWaitAgent(agentID)
		unlock()
		svc.Output.ClearAgentRuntimeState(agentID)
		svc.agentCleanups.retire(agentID)
		row, err := svc.Queries.GetAgentForInactiveBroadcast(bgCtx(), agentID)
		if err == nil {
			svc.broadcastAgentInactive(&db.Agent{
				ID:             row.ID,
				AgentSessionID: row.AgentSessionID,
				AgentProvider:  row.AgentProvider,
			})
		} else if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("archive: read agent for inactive broadcast", "agent_id", agentID, "error", err)
		}
	}

	for _, terminalID := range changed.terminals {
		svc.TerminalStartup.waitForFinished(terminalStarts[terminalID])
	}
	for _, terminalID := range changed.terminals {
		svc.Terminals.WaitForExit(terminalID)
		svc.terminalCleanups.retire(terminalID)
		svc.clearTerminalBellCoalesce(terminalID)
		exitCode, err := svc.Queries.GetTerminalExitCode(bgCtx(), terminalID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("archive: read terminal for exited broadcast", "terminal_id", terminalID, "error", err)
			}
			continue
		}
		// Broadcast even though a PTY that was RUNNING already broadcast the
		// same event from makeTerminalExitFn: a terminal that never spawned, or
		// that exited before the archive, fires no exit handler, and the client
		// has to learn that its shell is gone from something. WaitForExit above
		// returns only after that handler persisted the row, so the exit code
		// read here is the final one rather than a stale zero, and the repeat is
		// a repeat rather than a contradiction.
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{
				ExitCode: int32(exitCode),
			}},
		})
	}
}
