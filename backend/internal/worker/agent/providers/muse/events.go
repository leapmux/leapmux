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
		SessionID string          `json:"sessionId"`
		ModelID   json.RawMessage `json:"modelId"`
		Effort    json.RawMessage `json:"reasoningEffort"`
		Mode      json.RawMessage `json:"mode"`
		// Raw fields preserve absent, null, and future values without rejecting an unknown event.
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
		var raw json.RawMessage
		var optionID string
		switch line.Method {
		case methodSessionModelChanged:
			raw, optionID = event.ModelID, agent.OptionIDModel
		case methodSessionEffortChanged:
			raw, optionID = event.Effort, agent.OptionIDEffort
		case methodSessionApprovalChanged:
			raw, optionID = event.Mode, agent.OptionIDPermissionMode
		}
		value, valid := providerkit.RawString(raw)
		if !valid {
			return
		}
		a.stateMu.Lock()
		a.settings[optionID] = value
		a.stateMu.Unlock()
		state.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	case methodSessionGoalChanged:
		if !root {
			return
		}
		update, clear, valid := parseMuseGoalEvent(event.Goal)
		if !valid {
			// An invalid payload supplies no goal state. Keep the previous goal without an upsert or clear.
			return
		}
		sink, raw := state.sink, line.Raw
		// Capture the goal writer before dispatch releases.
		// A goal observer can enter HandleOutput again, so the callback must run without dispatcher.mu.
		// The captured writer refuses old goal data after a session replacement.
		captured := agent.CaptureTranscript(sink, agent.MessageContent{AgentSessionID: event.SessionID, Original: raw}, agent.SpanInfo{})
		writer, err := sink.GoalWriterFor(captured)
		if err != nil {
			slog.Warn("capture a Muse goal writer", "error", err)
			return
		}
		a.deferOutputAction(func() {
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
				// Preserve unknown status bytes as a message. A notification thread can hide an unreadable member.
				if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: event.SessionID}, agent.SpanInfo{}); err != nil {
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
	case contracts.MuseMethodTurnRetryScheduled:
		if current {
			a.holdRetryNotice(line, state)
		}
	case contracts.MuseMethodTodoListChanged, contracts.MuseMethodSessionModelRouteUnserved:
		_, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw})
		if err != nil {
			slog.Warn("persist a Muse notification", "error", err)
		}
	case methodViewGap:
		a.recoverView(line.Params, state)
	case methodSessionStatusChanged, methodSessionStarted, methodSessionClosed:
		// Native session bookkeeping changes no transcript row.
	case methodSessionBranchChanged:
		// The worker reads the branch directly.
	case methodSessionNameChanged:
		// LeapMux owns the title.
	case methodSessionPermissionProfileChanged:
		// LeapMux uses :ask-me and exposes no native profile axis.
	case methodSessionViewHealthChanged:
		slog.Warn("Muse reported a view health change", "session", event.SessionID)
	case methodTurnRetracted:
		// The paired cancelled turn/completed states the outcome.
	case methodTurnForegroundCompleted:
		// The foreground ended, but the native turn still owns work.
	case methodSkillChanged, methodAccountChanged, methodAccountLoginCompleted, methodMcpOAuthLoginCompleted, methodSessionListChanged:
		// LeapMux starts none of these native operations.
	case methodTurnUnqueued:
		turnID := providerkit.StringField(line.Params, "turnId")
		a.stateMu.Lock()
		matched := current && turnID != "" && state.turnID == turnID
		if matched {
			state.turnID = ""
		}
		a.stateMu.Unlock()
		if matched {
			a.Mu.Lock()
			seq := a.NextTurnSeq()
			a.Mu.Unlock()
			state.sink.SetTurnState(agent.TurnState{}, seq)
		}
	default:
		if !current {
			slog.Debug("ignore an unknown Muse event from a retired session", "session", event.SessionID, "method", line.Method)
			return
		}
		err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw, AgentSessionID: event.SessionID}, agent.SpanInfo{})
		if err != nil {
			slog.Warn("persist an unknown Muse notification", "error", err)
		}
	}
}

// holdRetryNotice keeps the failed attempt before the notice that describes its retry.
// Muse sends retryScheduled before it completes that attempt's text items.
func (a *Agent) holdRetryNotice(line *providerkit.ParsedLine, state *sessionState) {
	write := agent.CaptureTranscript(state.sink, agent.MessageContent{Original: line.Raw, AgentSessionID: providerkit.StringField(line.Params, "sessionId")}, agent.SpanInfo{})
	turnID := providerkit.StringField(line.Params, "turnId")
	a.stateMu.Lock()
	if turnID == "" || turnID != state.turnID {
		a.stateMu.Unlock()
		// A recovered notice stays visible, but later output cannot prove that its earlier turn retried successfully.
		if _, err := write.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
			slog.Error("persist a recovered Muse retry notice", "error", err)
		}
		return
	}
	if state.retryItems == nil {
		state.retryItems = make(map[string]bool)
	}
	for id, item := range state.items {
		if item.params.Item.TurnID != nil && *item.params.Item.TurnID == state.turnID &&
			item.params.Item.Status == contracts.MuseItemStatusInProgress &&
			(item.params.Item.Kind == contracts.MuseItemKindAgentMessage || item.params.Item.Kind == contracts.MuseItemKindReasoning) {
			state.retryItems[id] = true
		}
	}
	state.retryNotices = append(state.retryNotices, write)
	a.stateMu.Unlock()
	state.retry.NoteRetry()
	a.flushRetryNotices(state)
}

// flushRetryNotices submits notices after the text items of their failed attempt finish.
func (a *Agent) flushRetryNotices(state *sessionState) bool {
	a.stateMu.Lock()
	for id := range state.retryItems {
		if item := state.items[id]; item == nil || item.params.Item.Status != contracts.MuseItemStatusInProgress {
			delete(state.retryItems, id)
		}
	}
	if len(state.retryItems) != 0 {
		a.stateMu.Unlock()
		return false
	}
	notices := state.retryNotices
	state.retryNotices = nil
	a.stateMu.Unlock()
	for index, notice := range notices {
		if _, err := notice.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
			slog.Error("persist a Muse retry notice", "error", err)
			if !agent.OutputRetained(err) {
				a.stateMu.Lock()
				state.retryNotices = append(notices[index:], state.retryNotices...)
				a.stateMu.Unlock()
				return false
			}
		}
	}
	return true
}
