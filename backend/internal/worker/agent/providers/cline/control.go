package cline

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Approvals, questions and the plan tool.
//
// The session asks the hub before each tool call: the worker creates it with
// `toolPolicies: {"*": {"autoApprove": false}}`, so Cline's own approval policy
// never runs and the worker decides every call from its CURRENT mode, as
// Cline's interactive CLI decides from its own auto-approve switch:
//
//   - Auto-approve answers every call at once.
//   - Plan and Act answer the calls that Cline's CLI calls safe at once, and
//     publish a control request for every other call.
//
// A question is a capability request: the worker owns the `askQuestion`
// executor, so the hub asks it to answer `ask_question`, and the worker
// publishes the question as a control request. The plan tool is the
// `switch_to_act_mode` custom tool that the worker contributes in Plan mode:
// the model calls it after the user approved the plan in a message, its call
// asks for approval first (the plan card), and the hub then asks the worker to
// run it. The worker answers it at once and rebuilds the session in Act mode
// when the turn ends (session_lifecycle.go).
//
// The worker keys every request by the hub's own id: the approval id, or the
// capability request id. The stored request payload is Cline's own event
// envelope, which the browser reads.

// safeAutoApproveTools are the tools that Plan and Act approve at once. They are
// the tools that Cline's interactive CLI approves by itself when its
// auto-approve switch is off (SAFE_AUTO_APPROVE_TOOL_NAMES in
// apps/cli/src/runtime/tool-policies.ts), less `fetch_web_content`. Each one
// reads, searches, asks the user or ends the run, and changes nothing. A fetch
// sends what the model chooses to a host that the model chooses: with a read
// that asks nothing, it could send any file to any page, so it asks.
var safeAutoApproveTools = map[string]bool{
	"ask_followup_question":        true,
	contracts.ClineToolAskQuestion: true,
	"read_files":                   true,
	"search_codebase":              true,
	"skills":                       true,
	"submit_and_exit":              true,
}

// controlKind is the kind of one published request.
type controlKind int

const (
	controlApproval controlKind = iota + 1
	controlQuestion
)

// pendingControl is one approval or question that LeapMux published and
// nothing resolved yet. Guarded by Agent.Mu.
type pendingControl struct {
	kind       controlKind
	sessionID  string
	toolName   string
	toolCallID string
}

// approvalRequest is the payload of approval.requested that the worker reads.
type approvalRequest struct {
	ApprovalID string `json:"approvalId"`
	SessionID  string `json:"sessionId"`
	AgentID    string `json:"agentId"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
}

// capabilityRequest is the payload of capability.requested that the worker
// reads.
type capabilityRequest struct {
	RequestID      string `json:"requestId"`
	TargetClientID string `json:"targetClientId"`
	CapabilityName string `json:"capabilityName"`
	Payload        struct {
		Args    []json.RawMessage `json:"args"`
		Context struct {
			ToolCallID string `json:"toolCallId"`
			AgentID    string `json:"agentId"`
		} `json:"context"`
	} `json:"payload"`
}

// approvalResolution is the payload of approval.resolved and
// capability.resolved that the worker reads.
type approvalResolution struct {
	ApprovalID string `json:"approvalId"`
	RequestID  string `json:"requestId"`
	Cancelled  bool   `json:"cancelled"`
}

// handleApprovalRequested decides one tool approval. The caller holds
// dispatchMu, and the event belongs to the current session.
func (a *Agent) handleApprovalRequested(event hubEvent) {
	var request approvalRequest
	if err := json.Unmarshal(event.Payload, &request); err != nil || request.ApprovalID == "" {
		slog.Warn("cline approval request cannot be read", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	_, known := a.controls[request.ApprovalID]
	mode := a.settings.permissionMode
	a.Mu.Unlock()
	if known {
		// The hub states each pending approval again to a client that
		// subscribes again after a reconnect.
		return
	}
	if mode == contracts.ClinePermissionModeAutoApprove || safeAutoApproveTools[request.ToolName] {
		// respondApproval logs a failed answer. The answer fails only with the
		// connection, and the hub states the approval again to the client that
		// subscribes after the reconnect, which answers it again.
		a.sendInBackground(func() error {
			return a.respondApproval(contracts.ClineApprovalReply{ApprovalId: request.ApprovalID, Approved: true})
		})
		return
	}
	if request.ToolName == contracts.ClineToolSwitchToActMode {
		// The plan the approval switches to is the lead's last answer. The service
		// keeps it, so an approval that also clears the context carries it into
		// the fresh session.
		if plan := a.lastPlanText(); plan != "" {
			a.sink.UpdatePlan([]byte(plan), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, providerkit.ExtractPlanTitle(plan))
		}
	}
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*pendingControl)
	}
	a.controls[request.ApprovalID] = &pendingControl{
		kind: controlApproval, sessionID: event.SessionID, toolName: request.ToolName, toolCallID: request.ToolCallID,
	}
	a.Mu.Unlock()
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: event.SessionID,
		RequestID:      request.ApprovalID,
		Payload:        event.Raw,
	}); err != nil {
		// Fail closed: a request that no user can see must not wait, and it must
		// not run.
		slog.Error("cline publish approval request", "agent_id", a.AgentID(), "tool", request.ToolName, "error", err)
		a.dropControl(request.ApprovalID)
		reason := fmt.Sprintf("LeapMux could not show the permission request: %v", err)
		a.sendInBackground(func() error {
			return a.respondApproval(contracts.ClineApprovalReply{ApprovalId: request.ApprovalID, Reason: reason})
		})
	}
}

// handleCapabilityRequested answers or publishes one capability request. The
// caller holds dispatchMu, and the event belongs to the current session.
func (a *Agent) handleCapabilityRequested(event hubEvent) {
	var request capabilityRequest
	if err := json.Unmarshal(event.Payload, &request); err != nil || request.RequestID == "" {
		slog.Warn("cline capability request cannot be read", "agent_id", a.AgentID(), "error", err)
		return
	}
	if request.TargetClientID != a.clientID {
		// Another client of the daemon owns it.
		return
	}
	switch request.CapabilityName {
	case contracts.ClineCapabilityAskQuestion:
		a.publishQuestion(event, request)
	case capabilitySwitchToActMode:
		a.answerSwitchToActMode(request)
	default:
		// respondCapability logs a failed answer, and the hub cancels the request
		// of a client whose connection ends.
		reply := contracts.ClineCapabilityReply{
			RequestId: request.RequestID,
			Error:     fmt.Sprintf("LeapMux does not provide the capability %s", request.CapabilityName),
		}
		a.sendInBackground(func() error { return a.respondCapability(reply) })
	}
}

// publishQuestion publishes one `ask_question` as a control request.
func (a *Agent) publishQuestion(event hubEvent, request capabilityRequest) {
	a.Mu.Lock()
	_, known := a.controls[request.RequestID]
	if !known {
		if a.controls == nil {
			a.controls = make(map[string]*pendingControl)
		}
		a.controls[request.RequestID] = &pendingControl{
			kind: controlQuestion, sessionID: event.SessionID,
			toolName: contracts.ClineToolAskQuestion, toolCallID: request.Payload.Context.ToolCallID,
		}
	}
	a.Mu.Unlock()
	if known {
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: event.SessionID,
		RequestID:      request.RequestID,
		Payload:        event.Raw,
		SourceSeq:      a.toolCallRow(request.Payload.Context.ToolCallID),
	}); err != nil {
		slog.Error("cline publish question", "agent_id", a.AgentID(), "error", err)
		a.dropControl(request.RequestID)
		reply := contracts.ClineCapabilityReply{
			RequestId: request.RequestID,
			Error:     fmt.Sprintf("LeapMux could not show the question: %v", err),
		}
		a.sendInBackground(func() error { return a.respondCapability(reply) })
	}
}

// answerSwitchToActMode runs the plan tool: the user approved its call a moment
// ago. The tool ends the run, and the session rebuilds in Act mode when the
// turn ends.
func (a *Agent) answerSwitchToActMode(request capabilityRequest) {
	a.Mu.Lock()
	inPlan := a.settings.permissionMode == contracts.ClinePermissionModePlan
	if inPlan && a.turn.active {
		a.turn.actModeApproved = true
	}
	a.Mu.Unlock()
	reply := contracts.ClineCapabilityReply{RequestId: request.RequestID, Ok: true}
	if inPlan {
		reply.Payload, _ = json.Marshal(map[string]string{contracts.ClineCapabilityReplyResult: switchToActModeResult})
	} else {
		// The tool exists only in Plan mode; Cline's own tool refuses a switch
		// from Act the same way.
		reply = contracts.ClineCapabilityReply{RequestId: request.RequestID, Error: "Already in act mode."}
	}
	// respondCapability logs a failed answer. The hub cancels the capability
	// requests of a client whose connection ends, so no run waits for it.
	a.sendInBackground(func() error { return a.respondCapability(reply) })
}

// toolCallRow returns the sequence number of the row that opened a tool call,
// or 0 when the worker cannot read the row. The banner then shows the call
// without a link to it.
func (a *Agent) toolCallRow(toolCallID string) int64 {
	if toolCallID == "" {
		return 0
	}
	sink := a.toolCallSink(toolCallID)
	stored, err := sink.ReadToolRequest(toolCallID)
	if err != nil || stored == nil {
		return 0
	}
	return stored.Seq
}

// handleControlResolved withdraws a request that the hub resolved without
// LeapMux's answer: a run.abort cancels every pending approval and question,
// and another client of the daemon can answer an approval.
func (a *Agent) handleControlResolved(event hubEvent) {
	var resolution approvalResolution
	if err := json.Unmarshal(event.Payload, &resolution); err != nil {
		return
	}
	id := resolution.ApprovalID
	if event.Event == eventCapabilityResolved {
		id = resolution.RequestID
	}
	if id == "" {
		return
	}
	if a.dropControl(id) {
		a.sink.CancelControlRequest(id)
	}
}

// dropControl forgets one request, and reports whether it was pending.
func (a *Agent) dropControl(id string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if _, ok := a.controls[id]; !ok {
		return false
	}
	delete(a.controls, id)
	return true
}

// withdrawAllControls withdraws every pending request. Their answers can no
// longer reach a run: the turn ended, the session changed, or the agent stops.
func (a *Agent) withdrawAllControls() {
	a.Mu.Lock()
	ids := make([]string, 0, len(a.controls))
	for id := range a.controls {
		ids = append(ids, id)
	}
	a.controls = nil
	a.Mu.Unlock()
	for _, id := range ids {
		a.sink.CancelControlRequest(id)
	}
}

// errUnknownControl refuses an answer to a request the agent no longer holds.
var errUnknownControl = errors.New("the Cline request no longer waits for an answer")

// SendRawInput delivers a control answer. The answer is the native reply that
// the provider's ResolveControlResponse built from the browser's decision:
// an approval reply or a capability reply.
func (a *Agent) SendRawInput(data []byte) error {
	if a.IsStopped() {
		return errAgentStopped
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("the Cline control answer is not JSON: %w", err)
	}
	if _, ok := probe[contracts.ClineApprovalReplyApprovalId]; ok {
		var reply contracts.ClineApprovalReply
		if err := json.Unmarshal(data, &reply); err != nil {
			return fmt.Errorf("read the Cline approval answer: %w", err)
		}
		return a.answerApproval(reply)
	}
	if _, ok := probe[contracts.ClineCapabilityReplyRequestId]; ok {
		var reply contracts.ClineCapabilityReply
		if err := json.Unmarshal(data, &reply); err != nil {
			return fmt.Errorf("read the Cline question answer: %w", err)
		}
		return a.answerCapability(reply)
	}
	return errors.New("the Cline control answer is neither an approval nor a question answer")
}

// answerApproval sends the user's decision on one approval. An approval of the
// plan tool that states a mode sets the mode the session rebuilds in when the
// turn ends.
func (a *Agent) answerApproval(reply contracts.ClineApprovalReply) error {
	a.Mu.Lock()
	control, ok := a.controls[reply.ApprovalId]
	if ok && control.kind == controlApproval {
		delete(a.controls, reply.ApprovalId)
	}
	current := ok && control.sessionID == a.sessionID
	a.Mu.Unlock()
	if !ok || control.kind != controlApproval {
		return errUnknownControl
	}
	if !current {
		return agent.ErrInputSessionChanged
	}
	if reply.Approved && control.toolName == contracts.ClineToolSwitchToActMode {
		a.setPlanExitMode(reply.PermissionMode)
	}
	return a.respondApproval(contracts.ClineApprovalReply{
		ApprovalId: reply.ApprovalId,
		Approved:   reply.Approved,
		Reason:     reply.Reason,
	})
}

// answerCapability sends the user's answer to one question.
func (a *Agent) answerCapability(reply contracts.ClineCapabilityReply) error {
	a.Mu.Lock()
	control, ok := a.controls[reply.RequestId]
	if ok && control.kind == controlQuestion {
		delete(a.controls, reply.RequestId)
	}
	current := ok && control.sessionID == a.sessionID
	a.Mu.Unlock()
	if !ok || control.kind != controlQuestion {
		return errUnknownControl
	}
	if !current {
		return agent.ErrInputSessionChanged
	}
	return a.respondCapability(reply)
}

// respondApproval sends approval.respond. The reply carries no LeapMux field.
func (a *Agent) respondApproval(reply contracts.ClineApprovalReply) error {
	reply.PermissionMode = ""
	ctx, cancel := a.requestContext()
	defer cancel()
	if _, err := a.hub.command(ctx, commandApprovalRespond, "", reply); err != nil {
		slog.Warn("cline approval answer failed", "agent_id", a.AgentID(), "approval_id", reply.ApprovalId, "error", err)
		return fmt.Errorf("answer the Cline approval: %w", err)
	}
	return nil
}

// respondCapability sends capability.respond.
func (a *Agent) respondCapability(reply contracts.ClineCapabilityReply) error {
	ctx, cancel := a.requestContext()
	defer cancel()
	if _, err := a.hub.command(ctx, commandCapabilityRespond, "", reply); err != nil {
		slog.Warn("cline capability answer failed", "agent_id", a.AgentID(), "request_id", reply.RequestId, "error", err)
		return fmt.Errorf("answer the Cline request: %w", err)
	}
	return nil
}

// declineForeignRequest refuses a request of a session that the agent does not
// drive, so the run of that session does not wait for an answer that cannot
// come. Two cases reach it:
//   - A request of a session that a context clear or a mode rebuild detached.
//     The clear aborts the old session's run, so the request raced the switch.
//   - A request that states no session. Cline states the session of each
//     request that it makes, so such a request belongs to no session of this
//     agent, and no mode answers it, Auto-approve included.
func (a *Agent) declineForeignRequest(event hubEvent, reason string) {
	switch event.Event {
	case contracts.ClineEventApprovalRequested:
		var request approvalRequest
		if json.Unmarshal(event.Payload, &request) == nil && request.ApprovalID != "" {
			a.sendInBackground(func() error {
				return a.respondApproval(contracts.ClineApprovalReply{ApprovalId: request.ApprovalID, Reason: reason})
			})
		}
	case contracts.ClineEventCapabilityRequested:
		var request capabilityRequest
		if json.Unmarshal(event.Payload, &request) == nil && request.RequestID != "" && request.TargetClientID == a.clientID {
			a.sendInBackground(func() error {
				return a.respondCapability(contracts.ClineCapabilityReply{RequestId: request.RequestID, Error: reason})
			})
		}
	}
}

// isRequestEvent reports whether an event asks this client for an answer.
func isRequestEvent(name string) bool {
	return name == contracts.ClineEventApprovalRequested || name == contracts.ClineEventCapabilityRequested
}

// sendInBackground sends one command that the dispatcher starts, an automatic
// answer or an abort, from its own goroutine. The dispatcher must not wait for
// the reply: when the event queue is full, the reader waits on the queue
// (hubQueueDepth) and cannot read that reply, so both would wait until the
// request's timeout, and the daemon would drop the connection for its missed
// pings. Each command logs its own failure. A stop waits for the goroutine
// after the dispatcher ends, so the goroutine never outlives the agent.
func (a *Agent) sendInBackground(send func() error) {
	a.background.Add(1)
	go func() {
		defer a.background.Done()
		_ = send()
	}()
}

// Control answers.
//
// The browser sends the neutral approve/reject envelope that every control
// surface produces, and for a question the answer beside it. The resolution
// turns that into Cline's own reply -- an approval.respond or a
// capability.respond payload -- which the service stores and SendRawInput
// sends. It is pure: the running agent sends the reply.

// storedControlRequest is the part of a stored control request -- Cline's own
// event envelope -- that the resolution reads.
type storedControlRequest struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// declinedToolReason is the reason of a refused tool call that the user gave no
// reason for. Cline shows the model this text as the call's error, and the browser
// words it as a plain refusal.
const declinedToolReason = contracts.ClineDeclineReasonTool

// declinedQuestionError is the error of a question that the user declined with no
// words of their own.
const declinedQuestionError = contracts.ClineDeclineReasonQuestion

// resolveControlResponse turns the browser's answer into Cline's reply.
func resolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	withhold := func(reason string) agent.ControlResponseResolution {
		slog.Warn("cline control response withheld", "request_id", ctx.RequestID, "reason", reason)
		result.Withhold = true
		return result
	}
	if len(ctx.RequestPayload) == 0 {
		return withhold("no stored request states which reply answers it")
	}
	var request storedControlRequest
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		return withhold("the stored request does not decode")
	}
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		return withhold("the response carries no decision")
	}
	if requestID != "" && ctx.RequestID != "" && requestID != ctx.RequestID {
		return withhold("the response answers another request")
	}
	allow := behavior == agent.ControlBehaviorAllow

	var native any
	switch request.Event {
	case contracts.ClineEventApprovalRequested:
		var approval approvalRequest
		if err := json.Unmarshal(request.Payload, &approval); err != nil || approval.ApprovalID == "" {
			return withhold("the stored approval states no id")
		}
		if ctx.RequestID != "" && approval.ApprovalID != ctx.RequestID {
			return withhold("the stored approval is keyed by another id")
		}
		reply := contracts.ClineApprovalReply{ApprovalId: approval.ApprovalID, Approved: allow}
		if !allow {
			reply.Reason = message
			if reply.Reason == "" {
				reply.Reason = declinedToolReason
			}
		}
		if approval.ToolName == contracts.ClineToolSwitchToActMode {
			result.PlanModeControl = clineProvider{}.PlanModeControl(approval.ToolName)
			if allow && !ctx.PlanApproval.GetClearContext() {
				reply.PermissionMode = planExitMode(ctx.PlanApproval.GetPermissionMode())
			}
		}
		native = reply
	case contracts.ClineEventCapabilityRequested:
		var capability capabilityRequest
		if err := json.Unmarshal(request.Payload, &capability); err != nil || capability.RequestID == "" {
			return withhold("the stored question states no id")
		}
		if capability.CapabilityName != contracts.ClineCapabilityAskQuestion {
			return withhold("the stored capability request is not a question")
		}
		if ctx.RequestID != "" && capability.RequestID != ctx.RequestID {
			return withhold("the stored question is keyed by another id")
		}
		if !allow {
			reason := message
			if reason == "" {
				reason = declinedQuestionError
			}
			native = contracts.ClineCapabilityReply{RequestId: capability.RequestID, Error: reason}
			break
		}
		answer, ok := questionAnswer(ctx.ResponseContent)
		if !ok {
			return withhold("the answer to the question is empty")
		}
		payload, err := json.Marshal(map[string]string{contracts.ClineCapabilityReplyResult: answer})
		if err != nil {
			return withhold("the answer does not encode")
		}
		native = contracts.ClineCapabilityReply{RequestId: capability.RequestID, Ok: true, Payload: payload}
	default:
		return withhold("the stored request is not an approval or a question")
	}
	content, err := json.Marshal(native)
	if err != nil {
		return withhold("the reply does not encode")
	}
	result.Content = content
	return result
}

// planExitMode is the mode an approved plan switches to: the one the user
// picked, or Act. Plan is not a mode that a plan approval can switch to -- the
// approval is what leaves it.
func planExitMode(picked string) string {
	if picked == contracts.ClinePermissionModeAct || picked == contracts.ClinePermissionModeAutoApprove {
		return picked
	}
	return contracts.ClinePermissionModeAct
}

// innerResponse returns the inner response object of the browser's envelope,
// or an empty object.
func innerResponse(content []byte) json.RawMessage {
	var envelope struct {
		Response struct {
			Response json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(content, &envelope) != nil || len(envelope.Response.Response) == 0 {
		return json.RawMessage(`{}`)
	}
	return envelope.Response.Response
}

// questionAnswer reads the answer to a question from the browser's inner
// response, and reports whether it states one.
func questionAnswer(content []byte) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(innerResponse(content), &fields) != nil {
		return "", false
	}
	var answer string
	if json.Unmarshal(fields[contracts.ClineQuestionAnswerAnswer], &answer) != nil || strings.TrimSpace(answer) == "" {
		return "", false
	}
	return answer, true
}
