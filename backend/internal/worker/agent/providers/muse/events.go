package muse

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) handleEvent(line *providerkit.ParsedLine, state *sessionState) {
	var event struct {
		SessionID string `json:"sessionId"`
		ModelID   string `json:"modelId"`
		Effort    string `json:"reasoningEffort"`
		Mode      string `json:"mode"`
		Goal      *struct {
			Objective string `json:"objective"`
			Status    string `json:"status"`
		} `json:"goal"`
	}
	if json.Unmarshal(line.Params, &event) != nil {
		return
	}
	a.stateMu.Lock()
	current := a.currentSessionState(event.SessionID, state)
	root := current && event.SessionID == a.sessionID
	a.stateMu.Unlock()
	switch line.Method {
	case methodSessionModelChanged, methodSessionEffortChanged, methodSessionApprovalChanged:
		if !root {
			return
		}
		a.stateMu.Lock()
		switch line.Method {
		case methodSessionModelChanged:
			a.settings[agent.OptionIDModel] = event.ModelID
		case methodSessionEffortChanged:
			a.settings[agent.OptionIDEffort] = event.Effort
		case methodSessionApprovalChanged:
			a.settings[agent.OptionIDPermissionMode] = event.Mode
		}
		a.stateMu.Unlock()
		state.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	case methodSessionGoalChanged:
		if !root {
			return
		}
		if event.Goal == nil {
			state.sink.ClearGoal(false)
			return
		}
		status := agent.GoalStatusActive
		switch event.Goal.Status {
		case "paused":
			status = agent.GoalStatusPaused
		case "completed", "satisfied":
			status = agent.GoalStatusDone
		case "blocked", "failed":
			status = agent.GoalStatusBlocked
		}
		state.sink.UpsertGoal(agent.GoalUpdate{Objective: event.Goal.Objective, Status: status, StatusDetail: event.Goal.Status})
	case contracts.MuseMethodContextUsage:
		a.handleContextUsage(line.Params, state)
	case contracts.MuseMethodTokenUsage:
		a.handleTokenUsage(line.Params, state)
	case contracts.MuseMethodUsageChanged:
		if current {
			a.handleUsage(line.Params, state.sink)
		}
	case contracts.MuseMethodTodoListChanged, contracts.MuseMethodTurnRetryScheduled:
		_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw)
		if err != nil {
			slog.Warn("persist a Muse notification", "error", err)
		}
	case methodViewGap:
		a.recoverView(line.Params, state)
	case methodSessionStatusChanged, methodSessionStarted, methodSessionClosed:
	default:
		// Preserve an unknown native notification for the browser's generic reader.
		_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw)
		if err != nil {
			slog.Warn("persist an unknown Muse notification", "error", err)
		}
	}
}
