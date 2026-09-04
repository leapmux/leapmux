package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// applyTabArchiveState applies both direct Hub fan-out and reconciled archive
// state through one lifecycle implementation. It returns agents whose state
// changed to active. The caller schedules them at its safe convergence point.
func (svc *Service) applyTabArchiveState(
	ctx context.Context,
	state leapmuxv1.WorkspaceArchiveState,
	tabs []*leapmuxv1.TabRef,
) ([]string, error) {
	flag, err := workspaceArchiveFlag(state)
	if err != nil {
		return nil, err
	}
	agentIDs, terminalIDs, err := archiveTabIDs(tabs)
	if err != nil {
		return nil, err
	}

	svc.archiveStateMu.Lock()
	defer svc.archiveStateMu.Unlock()

	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("start archive-state transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	queries := svc.Queries.WithTx(tx)
	changedAgents := make([]string, 0, len(agentIDs))
	changedTerminals := make([]string, 0, len(terminalIDs))
	for _, agentID := range agentIDs {
		rows, updateErr := queries.SetAgentWorkspaceArchived(ctx, db.SetAgentWorkspaceArchivedParams{
			WorkspaceArchived: flag,
			ID:                agentID,
		})
		if updateErr != nil {
			return nil, fmt.Errorf("set agent %s archive state: %w", agentID, updateErr)
		}
		if rows > 0 {
			changedAgents = append(changedAgents, agentID)
		}
	}
	for _, terminalID := range terminalIDs {
		rows, updateErr := queries.SetTerminalWorkspaceArchived(ctx, db.SetTerminalWorkspaceArchivedParams{
			WorkspaceArchived: flag,
			ID:                terminalID,
		})
		if updateErr != nil {
			return nil, fmt.Errorf("set terminal %s archive state: %w", terminalID, updateErr)
		}
		if rows > 0 {
			changedTerminals = append(changedTerminals, terminalID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit archive state: %w", err)
	}
	committed = true

	if state == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED {
		svc.stopArchivedTabs(changedAgents, changedTerminals)
		return nil, nil
	}
	return changedAgents, nil
}

// ApplyTabArchiveStateForReconcile exposes the shared lifecycle operation to
// bootstrap without exposing it as another RPC surface.
func (svc *Service) ApplyTabArchiveStateForReconcile(
	ctx context.Context,
	state leapmuxv1.WorkspaceArchiveState,
	tabs []*leapmuxv1.TabRef,
) ([]string, error) {
	return svc.applyTabArchiveState(ctx, state, tabs)
}

func workspaceArchiveFlag(state leapmuxv1.WorkspaceArchiveState) (int64, error) {
	switch state {
	case leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE:
		return 0, nil
	case leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED:
		return 1, nil
	default:
		return 0, fmt.Errorf("archive_state must be ACTIVE or ARCHIVED")
	}
}

func archiveTabIDs(tabs []*leapmuxv1.TabRef) ([]string, []string, error) {
	agentSeen := make(map[string]struct{})
	terminalSeen := make(map[string]struct{})
	var agentIDs, terminalIDs []string
	for _, tab := range tabs {
		if tab == nil || tab.GetTabId() == "" {
			continue
		}
		switch tab.GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			if _, exists := agentSeen[tab.GetTabId()]; !exists {
				agentSeen[tab.GetTabId()] = struct{}{}
				agentIDs = append(agentIDs, tab.GetTabId())
			}
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			if _, exists := terminalSeen[tab.GetTabId()]; !exists {
				terminalSeen[tab.GetTabId()] = struct{}{}
				terminalIDs = append(terminalIDs, tab.GetTabId())
			}
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
			// Payload-backed tabs own no process and have no archive-state row.
		case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			return nil, nil, fmt.Errorf("tab %q has an unspecified type", tab.GetTabId())
		default:
			return nil, nil, fmt.Errorf("tab %q has unsupported type %s", tab.GetTabId(), tab.GetTabType())
		}
	}
	return agentIDs, terminalIDs, nil
}

func (svc *Service) stopArchivedTabs(agentIDs, terminalIDs []string) {
	agentStarts := make(map[string]*startupEntry, len(agentIDs))
	for _, agentID := range agentIDs {
		agentStarts[agentID] = svc.AgentStartup.cancelForArchive(agentID)
	}
	terminalStarts := make(map[string]*startupEntry, len(terminalIDs))
	for _, terminalID := range terminalIDs {
		terminalStarts[terminalID] = svc.TerminalStartup.cancelForArchive(terminalID)
	}
	// Signal all running processes before any one process drain can block.
	for _, agentID := range agentIDs {
		svc.Agents.StopAgent(agentID)
	}
	for _, terminalID := range terminalIDs {
		svc.Terminals.StopTerminal(terminalID)
	}

	for agentID, startup := range agentStarts {
		svc.AgentStartup.waitForFinished(startup)
		unlock := svc.Agents.LockAgent(agentID)
		svc.Agents.StopAndWaitAgent(agentID)
		unlock()
		svc.Output.ClearAgentRuntimeState(agentID)
		svc.agentCleanups.retire(agentID)
		row, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if err == nil {
			svc.broadcastAgentInactive(&row)
		} else if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("archive: read agent for inactive broadcast", "agent_id", agentID, "error", err)
		}
	}

	for _, startup := range terminalStarts {
		svc.TerminalStartup.waitForFinished(startup)
	}
	for _, terminalID := range terminalIDs {
		svc.Terminals.WaitForExit(terminalID)
		svc.terminalCleanups.retire(terminalID)
		svc.clearTerminalBellCoalesce(terminalID)
		row, err := svc.Queries.GetTerminal(bgCtx(), terminalID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("archive: read terminal for exited broadcast", "terminal_id", terminalID, "error", err)
			}
			continue
		}
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{
				ExitCode: int32(row.ExitCode),
			}},
		})
	}
}
