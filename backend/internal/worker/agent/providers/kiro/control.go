package kiro

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kiro raises two dialogs of its own as extension requests, beside the
// standard session/request_permission:
//
//   - `_kiro/userInput`: one question, with options or free text, answered
//     `answered` with the reader's answer or `dismissed`.
//   - `_kiro/mcp/elicitation`: an MCP server's form or URL, answered with
//     MCP's own `action` and `content`.
//
// Kiro never cancels one of them by a JSON-RPC notification. When it drops a
// request -- the turn was cancelled -- it states only that the interaction of
// the tool call resolved. controlIndex maps each tool call to the request it
// raised, so that update can retire the card.

// controlIndex maps a tool-call id to the LeapMux id of the control request
// that call raised. Guarded by Agent.stateMu.
type controlIndex struct {
	byToolCall map[string]string
}

// remember records the request that toolCallID raised.
func (c *controlIndex) remember(toolCallID, requestID string) {
	if toolCallID == "" || requestID == "" {
		return
	}
	if c.byToolCall == nil {
		c.byToolCall = make(map[string]string)
	}
	c.byToolCall[toolCallID] = requestID
}

// take returns and forgets the request that toolCallID raised.
func (c *controlIndex) take(toolCallID string) (string, bool) {
	requestID, ok := c.byToolCall[toolCallID]
	delete(c.byToolCall, toolCallID)
	return requestID, ok
}

// kiroControlCancelAnswer is what LeapMux answers when it withdraws one of
// Kiro's own requests without a reader decision, as it does on an interrupt.
// A question takes `dismissed`, which Kiro reads as no answer, and a form
// takes MCP's `cancel`.
func kiroControlCancelAnswer(method string) any {
	switch method {
	case contracts.KiroMethodUserInput:
		return map[string]any{"action": contracts.KiroUserInputActionDismissed}
	case contracts.KiroMethodMcpElicitation:
		return providerkit.MCPElicitationCancelAnswer()
	default:
		return nil
	}
}

// kiroRequestParams is the part of a control request that LeapMux reads
// before it publishes the request.
type kiroRequestParams struct {
	ToolCallID string `json:"toolCallId"`
	// ToolCall carries the tool-call id of a session/request_permission.
	ToolCall struct {
		ToolCallID string `json:"toolCallId"`
	} `json:"toolCall"`
}

// toolCallID returns the tool call a request belongs to, whichever of the two
// shapes it takes.
func (p kiroRequestParams) toolCallID() string {
	if p.ToolCallID != "" {
		return p.ToolCallID
	}
	return p.ToolCall.ToolCallID
}

// publishKiroControlRequest publishes one of Kiro's own dialogs through the
// session guard of the base. Kiro sends every session of its process down one
// connection, so a dialog of a session that the agent does not serve takes
// its cancel answer at once and never reaches the reader. The base calls
// observeControlRequest (Hooks.ControlRequestObserver) for each request that
// passes the guard.
func (a *Agent) publishKiroControlRequest(line *providerkit.ParsedLine) {
	a.PublishSessionControlRequest(line, kiroControlCancelAnswer(line.Method))
}

// observeControlRequest records the tool call of one control request that
// passed the session guard, for the base's publications as well as Kiro's own.
func (a *Agent) observeControlRequest(line *providerkit.ParsedLine) {
	var params kiroRequestParams
	if json.Unmarshal(line.Params, &params) != nil {
		return
	}
	wireID, _, found := agent.ExtractJSONRPCID(line.Raw)
	if !found {
		return
	}
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !valid {
		return
	}
	a.stateMu.Lock()
	a.controls.remember(params.toolCallID(), identity.Key)
	a.stateMu.Unlock()
}

// handleInteractionResolved retires the card of a request that Kiro resolved.
//
// Kiro sends this for EVERY resolution, the reader's own answer included. The
// withdrawal therefore cancels only a request that LeapMux still holds open,
// so a card the reader just decided never reads as cancelled.
func (a *Agent) handleInteractionResolved(raw json.RawMessage) {
	var resolved struct {
		InteractionResolved struct {
			ToolCallID string `json:"toolCallId"`
			Outcome    string `json:"outcome"`
		} `json:"interactionResolved"`
	}
	if json.Unmarshal(raw, &resolved) != nil || resolved.InteractionResolved.ToolCallID == "" {
		return
	}
	toolCallID := resolved.InteractionResolved.ToolCallID
	a.stateMu.Lock()
	requestID, ok := a.controls.take(toolCallID)
	a.stateMu.Unlock()
	if !ok {
		return
	}
	if a.WithdrawOutstandingControlRequest(a.Sink(), requestID) {
		slog.Info("kiro withdrew a control request", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "outcome", resolved.InteractionResolved.Outcome, "request_id", requestID)
	}
}
