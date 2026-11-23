package junie

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	junieSubagentSpawnedUpdate = "subagent_spawned"
	junieSubagentStateUpdate   = "subagent_state_update"
)

// Junie sends these updates only when the initialize request advertises
// nativeSubagentSessions. Its fallback tool card has no child session or task.
type junieSubagentUpdate struct {
	SubagentSessionID string `json:"subagentSessionId"`
	Name              string `json:"name"`
	Task              string `json:"task"`
	State             string `json:"state"`
}

// handleSessionMetadata folds Junie's goal and child-session updates into the
// Worker state. The base handles every other ACP update.
func (a *Agent) handleSessionMetadata(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if a.handleGoalMeta(updateType, metadata, update) {
		return true
	}
	switch updateType {
	case junieSubagentSpawnedUpdate, junieSubagentStateUpdate:
	default:
		return false
	}
	var event junieSubagentUpdate
	if err := json.Unmarshal(update, &event); err != nil {
		slog.Warn("junie child update is unreadable", "agent_id", a.AgentID(), "error", err)
		return true
	}
	childID := strings.TrimSpace(event.SubagentSessionID)
	if childID == "" || a.IsCurrentSession(childID) {
		slog.Warn("junie child update has an invalid session id", "agent_id", a.AgentID(), "child_session_id", childID)
		return true
	}
	if updateType == junieSubagentSpawnedUpdate {
		a.childTailMu.Lock()
		if _, finished := a.finishedChildren[childID]; finished {
			a.childTailMu.Unlock()
			return true
		}
		if a.openChildren == nil {
			a.openChildren = make(map[string]struct{})
		}
		a.openChildren[childID] = struct{}{}
		a.childTailMu.Unlock()

		title := strings.TrimSpace(event.Name)
		if title == "" {
			title = "Subagent"
		}
		a.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey: childID, ChildAgentKey: childID, Title: title,
			Prompt: event.Task, Status: bgtask.StatusRunning, Spawns: true,
		})
		a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: a.CurrentSessionID(), SessionID: childID, RowKey: childID})
		a.startChildTail(childID, title, event.Task)
		return true
	}
	status, ok := junieSubagentFinalStatus(event.State)
	if !ok {
		slog.Warn("junie child update has an unknown state", "agent_id", a.AgentID(), "child_session_id", childID, "state", event.State)
		return true
	}
	a.finishChildTail(childID)
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey: childID, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly,
	})
	return true
}

func (a *Agent) finishOpenChildren(err error, stopped bool) acp.PromptEnding {
	a.childTailMu.Lock()
	open := a.openChildren
	tails := a.childTails
	a.openChildren = nil
	a.childTails = nil
	if len(open) > 0 || len(tails) > 0 {
		if a.finishedChildren == nil {
			a.finishedChildren = make(map[string]struct{}, len(open)+len(tails))
		}
		for childID := range open {
			a.finishedChildren[childID] = struct{}{}
		}
		for childID := range tails {
			a.finishedChildren[childID] = struct{}{}
		}
	}
	a.childTailMu.Unlock()

	for _, tail := range tails {
		tail.stop()
		tail.poll()
	}
	status := bgtask.StatusStopped
	if err != nil && !stopped {
		status = bgtask.StatusFailed
	}
	childIDs := make([]string, 0, len(open)+len(tails))
	for childID := range open {
		childIDs = append(childIDs, childID)
	}
	for childID := range tails {
		if _, alreadyListed := open[childID]; alreadyListed {
			continue
		}
		childIDs = append(childIDs, childID)
	}
	slices.Sort(childIDs)
	for _, childID := range childIDs {
		a.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey: childID, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly,
		})
	}
	return acp.PromptEnding{}
}

func junieSubagentFinalStatus(state string) (bgtask.Status, bool) {
	switch state {
	case "completed":
		return bgtask.StatusSucceeded, true
	case "failed", "disconnected":
		return bgtask.StatusFailed, true
	case "cancelled":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusUnspecified, false
	}
}
