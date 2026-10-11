package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kiro's own notifications that LeapMux reads. Its two requests are in the
// contract, because the browser draws them too.
const (
	// kiroToolContentChunkMethod carries one piece of a running command's
	// output.
	kiroToolContentChunkMethod = "_kiro/tools/content_chunk"
	// kiroSessionNotifyMethod carries a message that a workflow step sends to
	// the session that started its workflow.
	kiroSessionNotifyMethod = "_kiro/session/notify"
	// kiroRateLimitMethod reports that the model service has no capacity or a
	// high load.
	kiroRateLimitMethod = "_kiro/error/rate_limit"
	// kiroSystemNotifyMethod reports a delay of the model service and its
	// recovery, for the whole process.
	kiroSystemNotifyMethod = "_kiro/system/notify"
	// kiroCustomAgentNotFoundMethod reports that the mode the client chose does
	// not exist, and that Kiro runs its default mode instead.
	kiroCustomAgentNotFoundMethod = "_kiro/customAgent/not_found"
	// kiroCustomAgentConfigErrorMethod reports a custom agent file that Kiro
	// could not read.
	kiroCustomAgentConfigErrorMethod = "_kiro/customAgent/config_error"
	// kiroPolicyErrorMethod reports a permission file that Kiro could not read.
	kiroPolicyErrorMethod = "_kiro/policy/error"
	// kiroExtensionPrefix marks every Kiro extension method.
	kiroExtensionPrefix = "_kiro/"
)

// handleExtraMethod routes every Kiro method that the ACP base does not know.
// It answers true for a line that it handled.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case contracts.KiroMethodUserInput, contracts.KiroMethodMcpElicitation:
		if !line.HasID() {
			return false
		}
		a.publishKiroControlRequest(line)
		return true
	case kiroToolContentChunkMethod:
		a.handleContentChunk(line.Params)
		return true
	case kiroWorkflowRunStartMethod, kiroWorkflowNodeStartMethod, kiroWorkflowNodeCompleteMethod,
		kiroWorkflowNodePausedMethod, kiroWorkflowLoopIterationMethod, kiroWorkflowPausedMethod,
		kiroWorkflowRunCompleteMethod:
		a.handleWorkflowNotification(line.Method, line.Params)
		return true
	case kiroSessionNotifyMethod:
		a.handleSessionNotify(line.Params)
		return true
	case kiroRateLimitMethod:
		a.handleSessionMessage(line.Params, "The model service is busy")
		return true
	case kiroSystemNotifyMethod:
		a.handleSystemNotify(line.Params)
		return true
	case kiroCustomAgentNotFoundMethod:
		a.handleCustomAgentNotFound(line.Params)
		return true
	case kiroCustomAgentConfigErrorMethod:
		a.handleCustomAgentConfigError(line.Params)
		return true
	case kiroPolicyErrorMethod:
		a.handlePolicyError(line.Params)
		return true
	}
	// Every other Kiro notification is live chrome for Kiro's own clients: the
	// governance state, the MCP inventory, powers, steering documents, tool
	// tags, the session list, hooks, knowledge indexing, spec progress. None
	// of them is conversation, so none reaches the transcript. A REQUEST falls
	// through, so the base refuses a method LeapMux does not answer and stores
	// the frame where the reader can see it.
	if strings.HasPrefix(line.Method, kiroExtensionPrefix) && !line.HasID() {
		slog.Debug("kiro notification not read", "agent_id", a.AgentID(), "method", line.Method)
		return true
	}
	return false
}

// Kiro sends every session of the process down one connection, workflow steps
// and sessions that it loads by itself included. So each handler below reads
// only a notification of the session that this agent serves.

// handleSessionNotify states a message that a workflow step sent to this
// session: a finding, a warning or an error of the step.
func (a *Agent) handleSessionNotify(params json.RawMessage) {
	var notify struct {
		SessionID  string `json:"sessionId"`
		Message    string `json:"message"`
		Severity   string `json:"severity"`
		AgentName  string `json:"agentName"`
		WorkflowID string `json:"workflowId"`
	}
	if err := json.Unmarshal(params, &notify); err != nil || !a.IsCurrentSession(notify.SessionID) {
		return
	}
	message := strings.TrimSpace(notify.Message)
	if message == "" {
		return
	}
	if notify.Severity == "error" {
		// An error of a goal step fails the goal's run, and the goal card
		// states it as the reason.
		a.noteGoalStepError(notify.WorkflowID, message)
	}
	if name := strings.TrimSpace(notify.AgentName); name != "" {
		message = name + ": " + message
	}
	if notify.Severity == "error" {
		a.persistAgentError(message)
		return
	}
	a.persistStatus(message)
}

// handleSessionMessage states the message of one notification of this
// session, or fallback when the notification states none.
func (a *Agent) handleSessionMessage(params json.RawMessage, fallback string) {
	var notice struct {
		SessionID string `json:"sessionId"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(params, &notice); err != nil || !a.IsCurrentSession(notice.SessionID) {
		return
	}
	message := strings.TrimSpace(notice.Message)
	if message == "" {
		message = fallback
	}
	a.persistStatus(message)
}

// handleSystemNotify states a delay of the model service, and its recovery.
// The notification belongs to the whole process, which serves this agent
// alone.
func (a *Agent) handleSystemNotify(params json.RawMessage) {
	var notice struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &notice); err != nil {
		return
	}
	if message := strings.TrimSpace(notice.Message); message != "" {
		a.persistStatus(message)
	}
}

// handleCustomAgentNotFound states that Kiro runs its default mode, because the
// mode the reader chose does not exist. Kiro answers the mode change itself
// with success, so this is the only sign of the fallback.
func (a *Agent) handleCustomAgentNotFound(params json.RawMessage) {
	var notFound struct {
		SessionID      string `json:"sessionId"`
		RequestedAgent string `json:"requestedAgent"`
		FallbackAgent  string `json:"fallbackAgent"`
	}
	if err := json.Unmarshal(params, &notFound); err != nil || !a.IsCurrentSession(notFound.SessionID) {
		return
	}
	fallback := notFound.FallbackAgent
	if fallback == "" {
		fallback = contracts.KiroModeDefault
	}
	a.persistStatus(fmt.Sprintf("Kiro has no mode %q, so it runs %q", notFound.RequestedAgent, fallback))
}

// handleCustomAgentConfigError states a custom agent file that Kiro could not
// read.
func (a *Agent) handleCustomAgentConfigError(params json.RawMessage) {
	var configError struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(params, &configError); err != nil || !a.IsCurrentSession(configError.SessionID) {
		return
	}
	a.persistAgentError(fmt.Sprintf("Kiro could not read the agent %s: %s", configError.Path, strings.TrimSpace(configError.Error)))
}

// handlePolicyError states a permission file that Kiro could not read. Kiro
// then runs without the rules of that file, which the reader must know.
func (a *Agent) handlePolicyError(params json.RawMessage) {
	var policyError struct {
		SessionID string `json:"sessionId"`
		Errors    []struct {
			Source  string `json:"source"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(params, &policyError); err != nil || !a.IsCurrentSession(policyError.SessionID) {
		return
	}
	for _, entry := range policyError.Errors {
		message := strings.TrimSpace(entry.Message)
		if message == "" {
			continue
		}
		if source := strings.TrimSpace(entry.Source); source != "" {
			message = source + ": " + message
		}
		a.persistAgentError("Kiro permission rules: " + message)
	}
}
