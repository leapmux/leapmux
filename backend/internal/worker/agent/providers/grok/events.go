package grok

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Grok's own notifications. Every extension method travels with a LEADING
// underscore: the ACP SDK that Grok is built on adds it on each send.
const (
	// grokSessionUpdateMethod carries the same updates as the live session
	// notification, for a session/load replay. LeapMux resumes with
	// session/resume, which replays nothing, so this arrives only if Grok
	// replays on its own.
	grokSessionUpdateMethod = "_x.ai/session/update"
	// grokQueueChangedMethod reports the prompt queue of one session, and the
	// prompt that runs now. It is the only signal that a turn started.
	grokQueueChangedMethod = "_x.ai/queue/changed"
	// grokTaskBackgroundedMethod and grokTaskCompletedMethod report a shell
	// command that runs in the background.
	grokTaskBackgroundedMethod = "_x.ai/task_backgrounded"
	grokTaskCompletedMethod    = "_x.ai/task_completed"
	// grokExtensionPrefix marks every Grok extension method.
	grokExtensionPrefix = "_x.ai/"
)

// The `sessionUpdate` words of a session notification that LeapMux reads.
// turn_completed is in the contract, because the browser reads it too.
const (
	grokUpdateResponseCompleted    = "response_completed"
	grokUpdateSubagentSpawned      = "subagent_spawned"
	grokUpdateSubagentFinished     = "subagent_finished"
	grokUpdateWorkflowUpdated      = "workflow_updated"
	grokUpdateGoalUpdated          = "goal_updated"
	grokUpdateAutoCompactStarted   = "auto_compact_started"
	grokUpdateAutoCompactCompleted = "auto_compact_completed"
	grokUpdateAutoCompactFailed    = "auto_compact_failed"
	grokUpdateRetryState           = "retry_state"
	grokUpdateInteractionResolved  = "interaction_resolved"
	grokUpdateTaskBackgrounded     = "task_backgrounded"
	grokUpdateTaskCompleted        = "task_completed"
)

// grokNotification is the envelope every Grok session notification shares:
// the session it belongs to, and one update tagged by `sessionUpdate`.
type grokNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// handleExtraMethod routes every Grok extension method that the ACP base does
// not know. It answers true for a line that it handled.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case contracts.GrokMethodAskUserQuestion,
		contracts.GrokMethodExitPlanMode,
		contracts.GrokMethodMcpElicit,
		contracts.GrokMethodFolderTrust:
		return a.publishGrokControlRequest(line)
	case contracts.GrokMethodSessionNotification, grokSessionUpdateMethod,
		grokTaskBackgroundedMethod, grokTaskCompletedMethod:
		a.handleSessionNotification(line)
		return true
	case grokQueueChangedMethod:
		a.handleQueueChanged(line.Params)
		return true
	}
	// Every other Grok notification is live chrome for Grok's own TUI: setup
	// phases, the session list, the MCP inventory, file-system and git
	// watches, an interjection echo, the prompt-complete twin of
	// turn_completed. None of them is conversation, so none reaches the
	// transcript. A REQUEST falls through, so the base refuses a method LeapMux
	// does not answer and stores the frame where the reader can see it.
	if strings.HasPrefix(line.Method, grokExtensionPrefix) && !line.HasID() {
		slog.Debug("grok notification not read", "agent_id", a.AgentID(), "method", line.Method)
		return true
	}
	return false
}

// handleSessionNotification dispatches one Grok session notification.
func (a *Agent) handleSessionNotification(line *providerkit.ParsedLine) {
	var notification grokNotification
	if err := json.Unmarshal(line.Params, &notification); err != nil {
		slog.Warn("grok session notification unmarshal failed", "agent_id", a.AgentID(), "method", line.Method, "error", err)
		return
	}
	var header struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(notification.Update, &header); err != nil {
		slog.Warn("grok session notification update unmarshal failed", "agent_id", a.AgentID(), "method", line.Method, "error", err)
		return
	}
	// A session that the agent does not serve -- the one a context clear
	// replaced, and each subagent of that session -- reaches nothing, as its
	// session updates reach no transcript. Its subagents, background commands
	// and workflow runs would otherwise open rows that nothing ever closes.
	if !a.ServesSession(notification.SessionID) {
		slog.Debug("grok notification of a session that it does not serve", "agent_id", a.AgentID(), "session_id", notification.SessionID, "update", header.SessionUpdate)
		return
	}
	main := notification.SessionID == "" || a.IsCurrentSession(notification.SessionID)
	switch header.SessionUpdate {
	case contracts.GrokNotificationTurnCompleted:
		a.handleTurnCompleted(notification, line.Raw, main)
	case grokUpdateInteractionResolved:
		a.handleInteractionResolved(notification.SessionID, notification.Update)
	case grokUpdateSubagentSpawned:
		a.handleSubagentSpawned(notification.Update)
	case grokUpdateSubagentFinished:
		a.handleSubagentFinished(notification.Update)
	case grokUpdateWorkflowUpdated:
		a.handleWorkflowUpdated(notification.Update)
	case grokUpdateTaskBackgrounded:
		a.handleTaskBackgrounded(notification.Update)
	case grokUpdateTaskCompleted:
		a.handleTaskCompleted(notification.Update)
	}
	// The rest of the handlers describe the main session: its goal, its usage
	// and its compaction. A child session reports the same updates about
	// ITSELF, which the reader of the parent must not see as its own.
	if !main {
		return
	}
	switch header.SessionUpdate {
	case grokUpdateGoalUpdated:
		a.handleGoalUpdated(notification.Update)
	case grokUpdateResponseCompleted:
		a.handleResponseCompleted(notification.Update)
	case grokUpdateAutoCompactStarted, grokUpdateAutoCompactCompleted, grokUpdateAutoCompactFailed:
		a.reportCompaction(header.SessionUpdate, notification.Update)
	case grokUpdateRetryState:
		a.reportRetry(notification.Update)
	}
}
