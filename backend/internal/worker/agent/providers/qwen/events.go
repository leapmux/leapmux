package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type qwenNotifyMethod struct {
	read   func(*Agent, json.RawMessage)
	reason string
	// A known mode update can imply the current session. Other notifications require its ID.
	sessionOptional bool
}

// This table lists each known notification and the reason that a consumed frame has no row.
var qwenNotifyMethods = map[string]qwenNotifyMethod{
	"qwen/notify/session/artifact-event":     {reason: "LeapMux does not display the native hook artifact catalog"},
	"qwen/notify/session/generation/event":   {reason: "LeapMux does not start native generation requests"},
	"qwen/notify/session/mcp-budget-event":   {reason: "LeapMux does not display native MCP budget telemetry"},
	"qwen/notify/session/pr-binding":         {reason: "the native pull request binding does not change the transcript"},
	"qwen/notify/session/sources-changed":    {reason: "the native source catalog does not change the transcript"},
	"qwen/notify/session/terminal-sequence":  {reason: "the browser transcript does not execute terminal escape sequences"},
	"qwen/notify/session/title-update":       {reason: "LeapMux uses its own tab titles"},
	"qwen/notify/workspace/generation/event": {reason: "LeapMux does not start native workspace generation requests"},
	"qwen/notify/session/model-update":       {reason: "the config-option update already states the model"},
	qwenModeUpdateMethod:                     {read: (*Agent).handleModeUpdate, sessionOptional: true},
	"qwen/notify/session/prompt-suggestion":  {read: (*Agent).handlePromptSuggestion},
	"qwen/notify/session/recording-degraded": {read: (*Agent).handleRecordingDegraded},
}

// Qwen's own methods, beside the extension method in the contract.
const (
	// qwenDrainMethod asks the client for input that the reader sent into the
	// running turn. Qwen asks after each tool batch. An error reply switches
	// steering off for the rest of the session, so LeapMux always answers.
	qwenDrainMethod = "craft/drainMidTurnQueue"
	// qwenStartTurnMethod opens a turn that Qwen starts by itself: a REQUEST for
	// the answer to a background subagent or command, which the client admits or
	// defers, and a NOTIFICATION for a round of a goal.
	qwenStartTurnMethod = "_qwencode/start_turn"
	// qwenModeUpdateMethod reports every change of the approval mode, including
	// the changes that no standard update states.
	qwenModeUpdateMethod = "qwen/notify/session/mode-update"
	// qwenNotifyPrefix marks Qwen's notifications of session metadata.
	qwenNotifyPrefix = "qwen/notify/"
	// qwenAuthenticateUpdateMethod reports a change of the login.
	qwenAuthenticateUpdateMethod = "authenticate/update"
)

// handleExtraMethod routes every Qwen method that the ACP base does not know.
// It answers true for a line that it handled.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case qwenDrainMethod:
		if !line.HasID() {
			return false
		}
		a.answerDrain(line)
		return true
	case qwenStartTurnMethod:
		a.handleStartTurn(line)
		return true
	case contracts.QwenMethodEndTurn:
		a.handleEndTurn(line)
		return true
	case qwenAuthenticateUpdateMethod:
		return !line.HasID()
	}
	if strings.HasPrefix(line.Method, qwenNotifyPrefix) && !line.HasID() {
		sessionID := providerkit.StringField(line.Params, "sessionId")
		method, known := qwenNotifyMethods[line.Method]
		if (sessionID == "" && !method.sessionOptional) || (sessionID != "" && !a.IsCurrentSession(sessionID)) {
			slog.Debug("ignore Qwen notification outside the current session", "agent_id", a.AgentID(), "method", line.Method)
			return true
		}
		if known {
			if method.read != nil {
				method.read(a, line.Params)
			} else {
				slog.Debug("consume Qwen notification", "agent_id", a.AgentID(), "method", line.Method, "reason", method.reason)
			}
			return true
		}
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		if err := a.Sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw}, agent.SpanInfo{}); err != nil {
			slog.Error("persist unknown Qwen notification", "agent_id", a.AgentID(), "method", line.Method, "error", err)
		}
		return true
	}
	return false
}

func (a *Agent) handlePromptSuggestion(params json.RawMessage) {
	text := providerkit.StringField(params, "suggestion")
	if strings.TrimSpace(text) == "" {
		return
	}
	a.Sink().BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyPromptSuggestion: map[string]any{contracts.PromptSuggestionFieldText: text},
	})
}

func (a *Agent) handleRecordingDegraded(params json.RawMessage) {
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	detail := ""
	if reason := strings.TrimSpace(providerkit.StringField(params, "reason")); reason != "" {
		detail = " (" + reason + ")"
	}
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("Qwen Code stopped writing its session file%s. A resume loses the turns after this point.", detail),
	})
}

func (a *Agent) clearPromptSuggestion() {
	a.Sink().BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyPromptSuggestion: nil})
}

func (a *Agent) preparePrompt() {
	a.resetRequestRetries()
	a.stateMu.Lock()
	a.compressPending = nil
	a.stateMu.Unlock()
	a.clearPromptSuggestion()
}

// qwenDrainItem is one message of a drain reply.
type qwenDrainItem struct {
	Content     []map[string]any `json:"content"`
	DisplayText string           `json:"displayText"`
}

// answerDrain answers Qwen's request for the input queued for the running
// turn. A request of another session is answered empty: its turn is not this
// agent's.
func (a *Agent) answerDrain(line *providerkit.ParsedLine) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(line.Params, &params)
	var items []steerItem
	if params.SessionID == "" || a.IsCurrentSession(params.SessionID) {
		a.stateMu.Lock()
		items = a.steer.take(qwenMaxDrainItems)
		a.stateMu.Unlock()
	}
	reply := make([]qwenDrainItem, 0, len(items))
	for _, item := range items {
		blocks := acp.BuildPromptBlocks(item.content, agent.ClassifyAttachments(item.attachments))
		if len(blocks) == 0 {
			continue
		}
		reply = append(reply, qwenDrainItem{Content: blocks, DisplayText: item.content})
	}
	// `hasQueuedPrompt` serves Qwen's experimental guard that holds a turn open
	// for a queued prompt. LeapMux queues no prompt behind a steer, so it is
	// always false.
	a.SendResponseDetached(line.ID, map[string]any{"items": reply, "hasQueuedPrompt": false}, "qwen steer drain")
}

// qwenTurnSession reads the session that one of Qwen's turn frames states, and
// reports whether it is the current session. A frame that states no session
// counts as the current session's.
func (a *Agent) qwenTurnSession(line *providerkit.ParsedLine) bool {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(line.Params, &params)
	return params.SessionID == "" || a.IsCurrentSession(params.SessionID)
}

// handleStartTurn opens a turn that Qwen starts by itself. It comes in two
// forms:
//
//   - A REQUEST asks for admission BEFORE the turn starts: the answer to a
//     background subagent or command. LeapMux admits it only while no turn
//     runs, its own prompt included (AdmitAgentTurn). Qwen aborts a running
//     turn when a prompt arrives, so a turn admitted while a prompt runs could
//     start the instant before the worker's next prompt and be cut by it. A
//     refused turn waits: Qwen asks again after a backoff, and at once when
//     its own prompt ends.
//   - A NOTIFICATION states a goal round that already started. It cannot be
//     refused, so the base queues it behind a prompt that still runs
//     (BeginAgentTurn).
//
// A turn of another session is refused, and its notification opens nothing.
func (a *Agent) handleStartTurn(line *providerkit.ParsedLine) {
	current := a.qwenTurnSession(line)
	if !line.HasID() {
		if current {
			if a.BeginAgentTurn() {
				a.clearPromptSuggestion()
			}
		}
		return
	}
	admitted := current && a.AdmitAgentTurn()
	if admitted {
		a.clearPromptSuggestion()
	}
	a.SendResponseDetached(line.ID, map[string]any{"accepted": admitted}, "qwen turn admission")
}

// handleEndTurn ends a turn that Qwen started by itself. The agent's own frame
// is the turn-end row. The browser plugin reads its reason into the divider, as
// it reads the prompt response of a turn LeapMux started.
//
// An end of another session ends nothing. After a context clear, the old
// session's end would otherwise end the agent turn of the new session.
func (a *Agent) handleEndTurn(line *providerkit.ParsedLine) {
	if !a.qwenTurnSession(line) {
		slog.Debug("qwen end of a turn of another session ignored", "agent_id", a.AgentID())
		return
	}
	if a.AgentTurnActive() {
		a.resetRequestRetries()
	}
	a.EndAgentTurn(json.RawMessage(line.Raw))
}

// handleModeUpdate reads a change of the approval mode. Qwen sends the standard
// current_mode_update only when a plan approval leaves plan mode, and this
// notification for every change.
func (a *Agent) handleModeUpdate(params json.RawMessage) {
	var update struct {
		SessionID     string `json:"sessionId"`
		CurrentModeID string `json:"currentModeId"`
	}
	if err := json.Unmarshal(params, &update); err != nil || update.CurrentModeID == "" {
		return
	}
	if update.SessionID != "" && !a.IsCurrentSession(update.SessionID) {
		return
	}
	a.ObserveCurrentMode(update.CurrentModeID)
}
