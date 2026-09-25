package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// toolCallWait limits how long a permission request waits for its tool call to
// appear on stdout.
//
// Amp prints the call's line before its executor leases the call, and the
// helper starts only after the lease, so the line is nearly always read first.
// The wait covers a reader that lags. The agent publishes a request that finds
// no call in time without a transcript link -- which is the right answer for a
// subagent's call, whose line Amp never prints.
const toolCallWait = time.Second

// decidePermission answers one helper request from the agent's CURRENT
// permission mode, so a mode change applies to the next call with no restart.
//
//   - Allow All answers at once, and no banner appears.
//   - Ask publishes a permission request and waits for the user's answer, or
//     for a cancellation: the helper went away, the turn ended, the process
//     exited, or the agent stopped.
func (a *Agent) decidePermission(ctx context.Context, request helperRequest) helperDecision {
	a.mu.Lock()
	mode := a.permissionMode
	stopped := a.stopped
	threadID := a.threadID
	a.mu.Unlock()
	if stopped {
		return rejectDecision("LeapMux stopped the agent")
	}
	if mode == contracts.AmpPermissionModeAllowAll {
		return allowDecision()
	}

	toolUseID, sourceSeq := a.matchToolCall(ctx, request.Tool, request.Input)
	if ctx.Err() != nil {
		return rejectDecision("LeapMux withdrew the permission request")
	}
	input := request.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	payload, err := json.Marshal(contracts.AmpPermissionRequest{
		Type:      contracts.AmpPermissionRequestTypeRequest,
		ToolName:  request.Tool,
		ToolUseID: toolUseID,
		Input:     input,
	})
	if err != nil {
		return rejectDecision(fmt.Sprintf("LeapMux could not read the tool input: %v", err))
	}

	requestID := a.bridge.newRequestID()
	pending, ok := a.bridge.register(requestID)
	if !ok {
		return rejectDecision("LeapMux stopped the agent")
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: threadID,
		RequestID:      requestID,
		Payload:        payload,
		SourceSeq:      sourceSeq,
	}); err != nil {
		a.bridge.unregister(requestID)
		slog.Error("amp publish permission request", "agent_id", a.agentID, "tool", request.Tool, "error", err)
		return rejectDecision(fmt.Sprintf("LeapMux could not show the permission request: %v", err))
	}
	if !a.bridge.markPublished(pending) {
		// The turn ended, or the agent stopped, while the banner was on its way.
		// That refusal found no banner to withdraw, so this withdraws it. The
		// refusal waits in pending.answer, and the select below returns it.
		a.sink.CancelControlRequest(requestID)
	}
	select {
	case decision := <-pending.answer:
		return decision
	case <-ctx.Done():
		// The helper went away, or the bridge closed. Either way no answer can
		// reach Amp through this request any more.
		a.bridge.unregister(requestID)
		a.sink.CancelControlRequest(requestID)
		return rejectDecision("LeapMux withdrew the permission request")
	}
}

// matchToolCall finds the tool call that one permission request is about, and
// the transcript row that opened it.
//
// The helper states no tool-use id: Amp starts it with an empty
// AGENT_TOOL_USE_ID. So a request claims the OLDEST open call that has the same
// tool name and the same input and that no earlier request claimed -- first in,
// first out. Two identical calls in parallel therefore map onto the two calls in
// the order they started, and each banner links a row of its own.
//
// It waits up to toolCallWait for the call to appear, and answers no call when
// none does.
func (a *Agent) matchToolCall(ctx context.Context, tool string, input json.RawMessage) (toolUseID string, sourceSeq int64) {
	timer := a.clock.NewTimer(toolCallWait, "amp", "permission-match")
	defer timer.Stop()
	for {
		// Read the signal BEFORE the search, so a call that appears between the
		// search and the wait still wakes the wait.
		signal := a.bridge.toolCallSignal()
		if id := a.claimToolCall(tool, input); id != "" {
			return id, a.toolCallRow(id)
		}
		select {
		case <-signal:
		case <-timer.C:
			return "", 0
		case <-ctx.Done():
			return "", 0
		}
	}
}

// claimToolCall marks the oldest unclaimed open call of tool with input as
// claimed, and returns its id, or "" when none matches.
func (a *Agent) claimToolCall(tool string, input json.RawMessage) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var oldest *openTool
	for _, candidate := range a.tools {
		if candidate.matched || candidate.name != tool || !agent.JSONCanonicalEqual(candidate.input, input) {
			continue
		}
		if oldest == nil || candidate.order < oldest.order {
			oldest = candidate
		}
	}
	if oldest == nil {
		return ""
	}
	oldest.matched = true
	return oldest.id
}

// toolCallRow returns the sequence number of the row that opened a tool call,
// or 0 when the row cannot be read. The banner then shows the call without
// linking it.
func (a *Agent) toolCallRow(toolUseID string) int64 {
	stored, err := a.sink.ReadToolRequest(toolUseID)
	if err != nil {
		slog.Warn("amp read the permission request's tool call", "agent_id", a.agentID, "tool_use_id", toolUseID, "error", err)
		return 0
	}
	if stored == nil {
		return 0
	}
	return stored.Seq
}

// SendRawInput delivers the user's answer to a permission request. A user line
// goes to Amp's stdin as it is.
//
// The answer is the neutral approve or reject envelope. A rejection with typed
// words reaches the model as the reason. A rejection with no words refuses in
// Amp's own wording.
//
// Amp's stdin takes user lines alone, and Amp ends the whole session at any
// other line. So SendRawInput refuses an answer whose behavior it cannot read,
// and every line that is not a user line, and sends nothing to Amp.
func (a *Agent) SendRawInput(data []byte) error {
	if requestID, behavior, message, ok := agent.DecodeControlBehavior(data); ok && requestID != "" {
		return a.answerPermission(requestID, behavior, message)
	}
	if !isUserLine(data) {
		return errors.New("the line is not a user message, which is the one line that Amp's input takes, so LeapMux did not send it")
	}
	a.mu.Lock()
	proc := a.proc
	stopped := a.stopped
	a.mu.Unlock()
	if stopped {
		return errAgentStopped
	}
	if proc == nil || proc.ending() {
		return errors.New("no Amp process runs to take the line")
	}
	return proc.SendRawInput(data)
}

// answerPermission delivers the user's decision on one permission request. It
// refuses a behavior other than allow and deny rather than guess a decision.
func (a *Agent) answerPermission(requestID, behavior, message string) error {
	var decision helperDecision
	switch behavior {
	case agent.ControlBehaviorAllow:
		decision = allowDecision()
	case agent.ControlBehaviorDeny:
		decision = rejectDecision(message)
	default:
		return fmt.Errorf("the answer to the Amp permission request %s states the behavior %q, and LeapMux knows only %q and %q",
			requestID, behavior, agent.ControlBehaviorAllow, agent.ControlBehaviorDeny)
	}
	if !a.bridge.answer(requestID, decision) {
		return fmt.Errorf("the Amp permission request %s no longer waits for an answer", requestID)
	}
	return nil
}

// isUserLine reports whether data is a JSON object of the user line type, the
// one line that Amp's stdin takes.
func isUserLine(data []byte) bool {
	var head struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &head) == nil && head.Type == contracts.AmpLineTypeUser
}
