package qwen

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

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
	case qwenModeUpdateMethod:
		a.handleModeUpdate(line.Params)
		return true
	case qwenAuthenticateUpdateMethod:
		return !line.HasID()
	}
	// The rest of Qwen's notifications state session metadata that LeapMux
	// keeps on its own: the model, the title, MCP budgets, artifacts and pull
	// requests. A REQUEST falls through, so the base refuses a method LeapMux
	// does not answer.
	if strings.HasPrefix(line.Method, qwenNotifyPrefix) && !line.HasID() {
		slog.Debug("qwen notification not read", "agent_id", a.AgentID(), "method", line.Method)
		return true
	}
	return false
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
			a.BeginAgentTurn()
		}
		return
	}
	admitted := current && a.AdmitAgentTurn()
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
