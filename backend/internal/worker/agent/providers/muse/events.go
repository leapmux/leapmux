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
		// Goal stays raw: an absent goal, an explicit null goal, and a goal
		// object are three different native answers, and a typed pointer
		// collapses the first two into one.
		Goal json.RawMessage `json:"goal"`
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
		update, clear, valid := parseMuseGoalEvent(event.Goal)
		if !valid {
			// An invalid payload states nothing about the goal. The previous
			// goal survives untouched: no upsert that could blank it and no
			// clear that could drop it.
			return
		}
		sink, raw := state.sink, line.Raw
		// Capture the goal authority NOW, at the observation: the writes run
		// after dispatch releases (a goal observer can re-enter native output,
		// and the dispatch mutex must be free when it does), and a context
		// replacement in between must not hand old native goal data the new
		// session's authority. The captured writer refuses once that happens.
		captured := agent.CaptureTranscript(sink, agent.MessageContent{AgentSessionID: event.SessionID, Original: raw}, agent.SpanInfo{})
		writer, err := sink.GoalWriterFor(captured)
		if err != nil {
			slog.Warn("capture a Muse goal writer", "error", err)
			return
		}
		a.deferGoalAction(func() {
			if clear {
				if err := writer.ClearGoal(); err != nil {
					slog.Warn("clear the native Muse goal", "error", err)
				}
				return
			}
			if err := writer.UpsertGoal(update); err != nil {
				slog.Warn("upsert the native Muse goal", "error", err)
				return
			}
			if update.Status == agent.GoalStatusUnknown {
				// An unrecognized native status cannot project to the goal card
				// without inventing a state, so the raw native bytes stay in
				// the transcript for the browser's generic reader alongside
				// the normalized display detail.
				if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
					slog.Warn("persist an unknown Muse goal status", "error", err)
				}
			}
		})
	case contracts.MuseMethodContextUsage:
		a.handleContextUsage(line.Params, state)
	case contracts.MuseMethodTokenUsage:
		a.handleTokenUsage(line.Params, state)
	case contracts.MuseMethodUsageChanged:
		if current {
			a.handleUsage(line.Params, state.sink)
		}
	case contracts.MuseMethodTodoListChanged, contracts.MuseMethodTurnRetryScheduled:
		_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw})
		if err != nil {
			slog.Warn("persist a Muse notification", "error", err)
		}
	case methodViewGap:
		a.recoverView(line.Params, state)
	case methodSessionStatusChanged, methodSessionStarted, methodSessionClosed:
	default:
		// Preserve an unknown native notification for the browser's generic reader.
		_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw})
		if err != nil {
			slog.Warn("persist an unknown Muse notification", "error", err)
		}
	}
}
