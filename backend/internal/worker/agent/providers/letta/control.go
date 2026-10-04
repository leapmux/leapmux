package letta

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Letta Code's control channel: the server asks the worker to approve a tool
// call (`can_use_tool`) which also carries an AskUserQuestion. The answer is a
// FLAT `approval_response` payload: `kind`, `request_id` and `decision` sit at
// the top level, never under a `response` key.

// lettaControlRequest is the body of a control_request message. The frame
// names it in `request`, the discriminator is `subtype`, and the body sits
// FLAT: `tool_name`, `input` and the rest are direct members of `request`,
// never under a `payload` key. The request id is the frame's own
// `request_id`.
type lettaControlRequest struct {
	Subtype               string                      `json:"subtype"`
	ToolName              string                      `json:"tool_name"`
	ToolCallID            string                      `json:"tool_call_id"`
	Input                 json.RawMessage             `json:"input"`
	PermissionSuggestions []lettaPermissionSuggestion `json:"permission_suggestions"`
}

// lettaPermissionSuggestion is one offer a permission request carries.
type lettaPermissionSuggestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// onControlRequest publishes one control request. `body` is the frame's
// `request` object and `requestID` the frame's `request_id`.
func (a *Agent) onControlRequest(line []byte, body []byte, requestID string) {
	var req lettaControlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Debug("letta: bad control request", "agent_id", a.AgentID(), "error", err)
		return
	}
	if req.Subtype != "can_use_tool" {
		slog.Debug("letta: unknown control request", "agent_id", a.AgentID(), "subtype", req.Subtype)
		return
	}

	if requestID == "" {
		requestID = "letta-perm-" + req.ToolCallID
	}
	kind := lettaControlPermission
	if req.ToolName == contracts.LettaToolAskUserQuestion {
		kind = lettaControlAskUser
	}
	slog.Info("letta: control request",
		"agent_id", a.AgentID(), "request_id", requestID, "kind", kind, "tool", req.ToolName)
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*lettaPendingControl)
	}
	a.controls[requestID] = &lettaPendingControl{
		requestID:  requestID,
		kind:       kind,
		toolCallID: req.ToolCallID,
	}
	a.Mu.Unlock()

	// The tool fields take the CONTRACT names (`tool_name`, `tool_call_id`,
	// `tool_input`): the browser plugin reads them under those names, the same
	// ones a stream_delta carries. A payload that spelled them differently drew
	// a banner titled "Tool" with no command and a question with no options.
	payloadBytes, err := json.Marshal(map[string]any{
		"type":                              string(kind),
		"requestId":                         requestID,
		contracts.LettaDeltaFieldToolName:   req.ToolName,
		contracts.LettaDeltaFieldToolCallID: req.ToolCallID,
		contracts.LettaDeltaFieldToolInput:  req.Input,
		"suggestions":                       req.PermissionSuggestions,
	})
	if err != nil {
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: a.lettaSession(),
		RequestID:      requestID,
		Payload:        payloadBytes,
	}); err != nil {
		slog.Debug("letta: publish control failed", "agent_id", a.AgentID(), "error", err)
	}
}

// lettaResolveControlResponse turns the browser's decision into the flat
// approval_response payload Letta reads. An absent request leaves the response
// bytes alone, and a malformed request withholds the response: the two rules
// the shared suites pin.
func lettaResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) == 0 {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent}
	}
	var request struct {
		Type       string          `json:"type"`
		RequestID  string          `json:"requestId"`
		ToolName   string          `json:"tool_name"`
		ToolCallID string          `json:"tool_call_id"`
		Input      json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent, Withhold: true}
	}

	_, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		return agent.ControlResponseResolution{Withhold: true}
	}

	behaviorWord := "deny"
	if behavior == agent.ControlBehaviorAllow {
		behaviorWord = "allow"
	}

	decision := decisionBody{Behavior: behaviorWord, Message: message}
	if behaviorWord == "allow" {
		if updated := extractUpdatedInput(ctx.ResponseContent, request.Input); updated != nil {
			decision.UpdatedInput = updated
		}
	}

	decisionRaw, err := json.Marshal(decision)
	if err != nil {
		return agent.ControlResponseResolution{Withhold: true}
	}
	response := approvalResponsePayload{
		Kind:      "approval_response",
		RequestID: request.RequestID,
		Decision:  decisionRaw,
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return agent.ControlResponseResolution{Withhold: true}
	}
	return agent.ControlResponseResolution{Content: raw}
}

// extractUpdatedInput builds the question-answer payload a question tool takes.
// The working form is `updated_input: {questions: <original>, answers: {<question text>: <label>}}`.
func extractUpdatedInput(content []byte, original json.RawMessage) json.RawMessage {
	answers := extractLettaAnswers(content)
	if answers == nil {
		return nil
	}
	updated := map[string]any{"answers": answers}
	if len(original) > 0 {
		var input map[string]any
		if err := json.Unmarshal(original, &input); err == nil {
			if questions, ok := input["questions"]; ok {
				updated["questions"] = questions
			}
		}
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return nil
	}
	return raw
}

// lettaInnerResponse returns the neutral inner response object, where a browser
// writes the fields a Letta answer takes beside the behavior.
func lettaInnerResponse(content []byte) json.RawMessage {
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

// extractLettaAnswers reads the question answers from a browser decision.
func extractLettaAnswers(content []byte) map[string]string {
	var d struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal(lettaInnerResponse(content), &d); err != nil {
		return nil
	}
	return d.Answers
}

// lettaSession reads the provider session id, which the control request must
// stamp so the service's delivery check matches it.
func (a *Agent) lettaSession() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.conversationID
}

// SendRawInput delivers one control answer to the App Server.
//
// The answer is the flat `approval_response` payload that
// lettaResolveControlResponse built. It travels as the PAYLOAD of an `input`
// command, addressed to the runtime. The base implementation writes to the
// process's STDIN, which `letta server` does not read for protocol_v2: the
// approval then never arrives and the tool call hangs for the rest of the turn.
func (a *Agent) SendRawInput(data []byte) error {
	if a.IsStopped() {
		return errAgentStopped
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("the Letta control answer is not JSON: %w", err)
	}
	if payload["kind"] != "approval_response" {
		return fmt.Errorf("the Letta control answer is not an approval_response")
	}
	cmd := newLettaCommand("input", a.nextRequestID())
	scope := a.runtime()
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the Letta runtime identity is not known yet; the answer cannot be addressed")
	}
	cmd.Runtime = &scope
	cmd.Payload = payload
	slog.Info("letta: send control answer",
		"agent_id", a.AgentID(), "request_id", cmd.RequestID,
		"runtime_conversation_id", scope.ConversationID)
	return a.sendCommand(cmd)
}
