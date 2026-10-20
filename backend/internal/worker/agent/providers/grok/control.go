package grok

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Grok raises four dialogs of its own as extension requests, beside the
// standard session/request_permission:
//
//   - `_x.ai/ask_user_question`: questions, answered with the chosen labels.
//   - `_x.ai/exit_plan_mode`: plan approval, answered approved, cancelled with
//     feedback, or abandoned.
//   - `_x.ai/mcp/elicit`: an MCP server's form or URL, answered accept,
//     decline or cancel under `outcome` rather than MCP's `action`.
//   - `_x.ai/folder_trust/request`: whether the repository's own
//     configuration may load, answered trust or reject.
//
// Grok never cancels one of them by a JSON-RPC notification. When it drops a
// request, because a stop cancelled the turn or stopped a subagent, it states
// only that the interaction of the tool call resolved. controlIndex maps each
// tool call to the request it raised, so that event can retire the card.

// controlKey identifies the tool call that raised a request. Grok states each
// resolution under the session that raised the interaction, and a subagent
// session can use a tool-call id that an open request of its parent uses too:
// a model backend can repeat ids across conversations. So the session is part
// of the key.
type controlKey struct {
	sessionID  string
	toolCallID string
}

// controlIndex maps the tool call of each open request to the LeapMux id of
// that request, and holds each tool call that carries a pending permission
// check. Guarded by Agent.stateMu.
type controlIndex struct {
	byToolCall map[controlKey]string
	checks     map[controlKey]struct{}
}

// rememberCheck records a tool call that published a permission-check badge.
func (c *controlIndex) rememberCheck(key controlKey) {
	if key.toolCallID == "" {
		return
	}
	if c.checks == nil {
		c.checks = make(map[controlKey]struct{})
	}
	c.checks[key] = struct{}{}
}

// takeCheck forgets a tool call's badge and reports whether one was open.
func (c *controlIndex) takeCheck(key controlKey) bool {
	_, ok := c.checks[key]
	delete(c.checks, key)
	return ok
}

// remember records the request that one tool call raised.
func (c *controlIndex) remember(key controlKey, requestID string) {
	if key.toolCallID == "" || requestID == "" {
		return
	}
	if c.byToolCall == nil {
		c.byToolCall = make(map[controlKey]string)
	}
	c.byToolCall[key] = requestID
}

// take returns and forgets the request that one tool call raised.
func (c *controlIndex) take(key controlKey) (string, bool) {
	requestID, ok := c.byToolCall[key]
	delete(c.byToolCall, key)
	return requestID, ok
}

// grokControlCancelAnswer is what LeapMux answers when it withdraws one of
// Grok's own requests without a reader decision, as it does on an interrupt.
//
// A question and a plan take `cancelled`, which Grok reads as no answer. An
// elicitation takes MCP's `cancel`. Folder trust takes NONE, and it is the one
// request that the provider publishes with none, which configure states to the
// base (Hooks.AnswerlessControlsOutliveTurns):
//
//   - The question asks about the repository, not about the turn, so a stop
//     or a context clear leaves it open for the reader.
//   - Grok reads every answer but `trust` as a rejection, and it keeps a
//     rejection for the rest of the process, so a cancel answer would stop it
//     from asking about that repository again until the agent restarts.
func grokControlCancelAnswer(method string) any {
	switch method {
	case contracts.GrokMethodAskUserQuestion:
		return map[string]any{contracts.GrokReplyFieldOutcome: contracts.GrokQuestionOutcomeCancelled}
	case contracts.GrokMethodExitPlanMode:
		return map[string]any{contracts.GrokReplyFieldOutcome: contracts.GrokPlanOutcomeCancelled}
	case contracts.GrokMethodMcpElicit:
		return map[string]any{contracts.GrokReplyFieldOutcome: contracts.MCPElicitationActionCancel}
	default:
		return nil
	}
}

// grokRequestParams is the part of one of Grok's requests that LeapMux reads
// before it publishes the request.
type grokRequestParams struct {
	SessionID     string  `json:"sessionId"`
	ToolCallID    string  `json:"toolCallId"`
	PlanContent   *string `json:"planContent"`
	ServerName    string  `json:"serverName"`
	ElicitationID string  `json:"elicitationId"`
	Mode          string  `json:"mode"`
	// ToolCall carries the tool-call id of a session/request_permission.
	ToolCall struct {
		ToolCallID string `json:"toolCallId"`
	} `json:"toolCall"`
}

// key returns the tool call that a request belongs to, whichever of the two
// shapes it takes.
func (p grokRequestParams) key() controlKey {
	toolCallID := p.ToolCallID
	if toolCallID == "" {
		toolCallID = p.ToolCall.ToolCallID
	}
	return controlKey{sessionID: p.SessionID, toolCallID: toolCallID}
}

// publishGrokControlRequest publishes one of Grok's own dialogs through the
// base, which refuses a dialog of a session that the agent does not serve. It
// answers true, because the publisher handles the line either way: it logs a
// request with no id, which is malformed.
func (a *Agent) publishGrokControlRequest(line *providerkit.ParsedLine) bool {
	a.PublishSessionControlRequest(line, grokControlCancelAnswer(line.Method))
	return true
}

// observeControlRequest reads each control request that the base publishes,
// Grok's own dialogs and the base's permission requests alike, right before
// the publication (Hooks.ControlRequestObserver). It records the tool call of
// the request, and it stores the plan that a plan approval carries.
func (a *Agent) observeControlRequest(line *providerkit.ParsedLine) {
	var params grokRequestParams
	if json.Unmarshal(line.Params, &params) != nil {
		return
	}
	if line.Method == contracts.GrokMethodExitPlanMode {
		a.storePlan(params)
	}
	// An external URL elicitation keeps its card until Grok's own completion
	// notification arrives, so the flow is remembered under the pair that the
	// completion names again.
	if line.Method == contracts.GrokMethodMcpElicit && params.Mode == contracts.MCPElicitationModeURL && params.ElicitationID != "" {
		a.RememberURLElicitation(params.ServerName+"\x00"+params.ElicitationID, line)
	}
	// The request itself answers the question the badge asked, so the badge
	// ends here: in ask mode it lasts only the moments between the two frames.
	a.clearPermissionCheck(params.key())
	wireID, _, found := agent.ExtractJSONRPCID(line.Raw)
	if !found {
		return
	}
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !valid {
		return
	}
	a.stateMu.Lock()
	a.controls.remember(params.key(), identity.Key)
	a.stateMu.Unlock()
}

// handleInteractionResolved retires the card of a request that Grok resolved in
// sessionID.
//
// Grok sends this for EVERY resolution, the reader's own answer included. The
// withdrawal therefore cancels only a request that LeapMux still holds open,
// so a card the reader just decided never reads as cancelled.
func (a *Agent) handleInteractionResolved(sessionID string, update json.RawMessage) {
	var resolved struct {
		ToolCallID string `json:"tool_call_id"`
	}
	if json.Unmarshal(update, &resolved) != nil || resolved.ToolCallID == "" {
		return
	}
	key := controlKey{sessionID: sessionID, toolCallID: resolved.ToolCallID}
	a.clearPermissionCheck(key)
	a.stateMu.Lock()
	requestID, ok := a.controls.take(key)
	a.stateMu.Unlock()
	if !ok {
		return
	}
	if a.WithdrawOutstandingControlRequest(a.Sink(), requestID) {
		slog.Info("grok withdrew a control request", "agent_id", a.AgentID(), "session_id", sessionID, "tool_call_id", resolved.ToolCallID, "request_id", requestID)
	}
}

// storePlan records the plan that a plan approval carries, so LeapMux gives the
// plan its title and can show it again. Grok sends `null` for an empty plan. A
// plan that holds only whitespace states no plan either: stored, it would
// replace the plan of an earlier approval with a blank one.
func (a *Agent) storePlan(request grokRequestParams) {
	if request.PlanContent == nil || strings.TrimSpace(*request.PlanContent) == "" {
		return
	}
	compressed, compression := msgcodec.Compress([]byte(*request.PlanContent))
	a.Sink().UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(*request.PlanContent))
}

// handlePendingInteraction reads one `pending_interaction` notice. In auto
// mode Grok holds a tool call in its own classifier before it raises the real
// permission request, and the notice is the only live signal that the call
// still runs: it becomes the running row's permission-check badge on the
// transcript that owns the tool call. The badge in ask mode lasts only the
// moments between this notice and the request itself.
func (a *Agent) handlePendingInteraction(sessionID string, update json.RawMessage) {
	var pending struct {
		ToolCallID string `json:"tool_call_id"`
		Kind       string `json:"kind"`
	}
	if json.Unmarshal(update, &pending) != nil || pending.ToolCallID == "" || pending.Kind != "permission" {
		return
	}
	key := controlKey{sessionID: sessionID, toolCallID: pending.ToolCallID}
	owner, ok := a.SessionOwner(sessionID)
	if !ok {
		return
	}
	a.stateMu.Lock()
	a.controls.rememberCheck(key)
	a.stateMu.Unlock()
	providerkit.PublishPermissionCheck(owner, pending.ToolCallID, true)
}

// clearPermissionCheck ends the badge of one tool call when its check is open.
func (a *Agent) clearPermissionCheck(key controlKey) {
	a.stateMu.Lock()
	open := a.controls.takeCheck(key)
	a.stateMu.Unlock()
	if !open {
		return
	}
	if owner, ok := a.SessionOwner(key.sessionID); ok {
		providerkit.PublishPermissionCheck(owner, key.toolCallID, false)
	}
}

// clearPermissionChecks ends every open badge: the turn that raised them is
// over, a stop included.
func (a *Agent) clearPermissionChecks() {
	a.stateMu.Lock()
	keys := make([]controlKey, 0, len(a.controls.checks))
	for key := range a.controls.checks {
		keys = append(keys, key)
	}
	a.stateMu.Unlock()
	for _, key := range keys {
		a.clearPermissionCheck(key)
	}
}

// completeToolOutput ends the badge of a tool call that reached its final
// update: the call is over, whatever the classifier asked about it.
func (a *Agent) completeToolOutput(toolCallID string) {
	sessionID := a.CurrentSessionID()
	a.stateMu.Lock()
	key, open := controlKey{}, false
	for check := range a.controls.checks {
		if check.toolCallID == toolCallID && (check.sessionID == sessionID || check.sessionID == "") {
			key, open = check, true
			break
		}
	}
	a.stateMu.Unlock()
	if open {
		a.clearPermissionCheck(key)
	}
}
