package droid

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Factory Droid's control channel: the server asks the worker for permission
// (droid.request_permission) and for an answer to a question (droid.ask_user).
// Both are JSON-RPC requests that the worker must answer.

// droidPermissionRequest is the params of droid.request_permission.
type droidPermissionRequest struct {
	ToolUses []struct {
		ToolUse struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"toolUse"`
		ConfirmationType string          `json:"confirmationType"`
		Details          json.RawMessage `json:"details"`
	} `json:"toolUses"`
	Options []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"options"`
	AssociatedSessionIDs []string `json:"associatedSessionIds"`
}

// droidAskUserRequest is the params of droid.ask_user.
type droidAskUserRequest struct {
	ToolCallID string `json:"toolCallId"`
	Questions  []struct {
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		MultiSelect bool     `json:"multiSelect"`
	} `json:"questions"`
}

// handleServerRequest answers one server->client request.
func (a *Agent) handleServerRequest(line []byte, env *droidEnvelope) {
	switch env.Method {
	case droidMethodRequestPermission:
		a.onRequestPermission(env)
	case droidMethodAskUser:
		a.onAskUser(env)
	default:
		slog.Debug("droid: unknown server request", "agent_id", a.AgentID(), "method", env.Method)
		// Answer with an empty result so the CLI is not left waiting.
		a.respond(env.ID, map[string]any{})
	}
}

// onRequestPermission publishes a permission control request.
func (a *Agent) onRequestPermission(env *droidEnvelope) {
	var params droidPermissionRequest
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad permission params", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, map[string]any{"selectedOption": contracts.DroidPermissionOptionCancel})
		return
	}
	if len(params.ToolUses) == 0 {
		a.respond(env.ID, map[string]any{"selectedOption": contracts.DroidPermissionOptionProceedOnce})
		return
	}
	toolUse := params.ToolUses[0]
	requestID := "droid-perm-" + toolUse.ToolUse.ID
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*droidPendingControl)
	}
	a.controls[requestID] = &droidPendingControl{
		requestID: requestID,
		kind:      droidControlPermission,
		toolUseID: toolUse.ToolUse.ID,
	}
	a.Mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"type":             "permission_request",
		"requestId":        requestID,
		"rpcId":            env.ID,
		"toolUse":          toolUse.ToolUse,
		"confirmationType": toolUse.ConfirmationType,
		"details":          toolUse.Details,
		"options":          droidOptionValues(params.Options),
	})
	if err != nil {
		a.respond(env.ID, map[string]any{"selectedOption": contracts.DroidPermissionOptionCancel})
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: a.droidSession(),
		RequestID:      requestID,
		Payload:        payload,
	}); err != nil {
		slog.Debug("droid: publish permission failed", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, map[string]any{"selectedOption": contracts.DroidPermissionOptionCancel})
	}
}

// onAskUser publishes a question control request.
func (a *Agent) onAskUser(env *droidEnvelope) {
	var params droidAskUserRequest
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad ask_user params", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, map[string]any{"cancelled": true, "answers": map[string]any{}})
		return
	}
	requestID := "droid-ask-" + params.ToolCallID
	questionPayload := make([]map[string]any, 0, len(params.Questions))
	for _, q := range params.Questions {
		questionPayload = append(questionPayload, map[string]any{
			"question":    q.Question,
			"options":     q.Options,
			"multiSelect": q.MultiSelect,
		})
	}
	a.Mu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*droidPendingControl)
	}
	a.controls[requestID] = &droidPendingControl{
		requestID: requestID,
		kind:      droidControlAskUser,
		toolUseID: params.ToolCallID,
	}
	a.Mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"type":       "ask_user_request",
		"requestId":  requestID,
		"rpcId":      env.ID,
		"toolCallId": params.ToolCallID,
		"questions":  questionPayload,
	})
	if err != nil {
		a.respond(env.ID, map[string]any{"cancelled": true, "answers": map[string]any{}})
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID,
		Payload:   payload,
	}); err != nil {
		slog.Debug("droid: publish question failed", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, map[string]any{"cancelled": true, "answers": map[string]any{}})
	}
}

// respond sends a JSON-RPC response for a server->client request.
func (a *Agent) respond(id string, result any) {
	if id == "" {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	env := newDroidEnvelope(droidTypeResponse)
	env.ID = id
	env.Result = raw
	line, err := env.Marshal()
	if err != nil {
		return
	}
	if err := a.WriteStdin(append(line, '\n')); err != nil {
		slog.Debug("droid: respond failed", "agent_id", a.AgentID(), "error", err)
	}
}

// droidResolveControlResponse turns the browser's decision into Droid's own
// answer body. An absent request leaves the response bytes alone, and a
// malformed request withholds the response: the two rules the shared suites pin.
func droidResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) == 0 {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent}
	}
	var request struct {
		Type       string `json:"type"`
		RPCID      string `json:"rpcId"`
		RequestID  string `json:"requestId"`
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent, Withhold: true}
	}

	_, behavior, _, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		return agent.ControlResponseResolution{Withhold: true}
	}

	// The service forwards these bytes to the agent's stdin. Droid reads a
	// JSON-RPC RESPONSE envelope there, keyed by the id of the request it
	// answers -- a bare result body leaves the call hanging forever.
	result := map[string]any{}
	if request.Type == "ask_user_request" {
		result["cancelled"] = behavior != agent.ControlBehaviorAllow
		result["answers"] = extractAnswerList(ctx.ResponseContent)
	} else {
		option := contracts.DroidPermissionOptionCancel
		if behavior == agent.ControlBehaviorAllow {
			option = contracts.DroidPermissionOptionProceedOnce
		}
		if selected := extractSelectedOption(ctx.ResponseContent); selected != "" {
			option = selected
		}
		result["selectedOption"] = option
	}
	raw, err := json.Marshal(droidResponseEnvelope(request.RPCID, result))
	if err != nil {
		return agent.ControlResponseResolution{Withhold: true}
	}
	return agent.ControlResponseResolution{Content: raw}
}

// droidResponseEnvelope wraps a result in the JSON-RPC response Droid reads on
// its stdin. The id is the request it answers.
func droidResponseEnvelope(id string, result any) droidEnvelope {
	env := newDroidEnvelope(droidTypeResponse)
	env.ID = id
	raw, err := json.Marshal(result)
	if err != nil {
		raw = []byte("{}")
	}
	env.Result = raw
	return env
}

// droidInnerResponse returns the neutral inner response object, where a browser
// writes the fields a Droid answer takes beside the behavior.
func droidInnerResponse(content []byte) json.RawMessage {
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

// extractAnswers reads the ask_user answers from a browser decision.
func extractAnswers(content []byte) map[string]string {
	var d struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal(droidInnerResponse(content), &d); err != nil {
		return nil
	}
	return d.Answers
}

// extractSelectedOption reads a chosen permission option from a browser decision.
func extractSelectedOption(content []byte) string {
	var d struct {
		SelectedOption string `json:"selectedOption"`
	}
	if err := json.Unmarshal(droidInnerResponse(content), &d); err != nil {
		return ""
	}
	return d.SelectedOption
}

// droidOptionValues reads the `value` of each offered option. Droid's options
// are {label, value} objects; the response takes one `value`.
func droidOptionValues(options []struct {
	Label string `json:"label"`
	Value string `json:"value"`
}) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		if option.Value != "" {
			values = append(values, option.Value)
		}
	}
	return values
}

// droidSession reads the provider session id, which the control request must
// stamp so the service's delivery check matches it.
func (a *Agent) droidSession() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.sessionID
}

// extractAnswerList reads the ask_user answers as the ARRAY of
// {index, question, answer} Droid's own reply schema takes. The browser's
// decision carries a map keyed by question text; the array is the wire shape.
func extractAnswerList(content []byte) []map[string]any {
	answers := extractAnswers(content)
	list := make([]map[string]any, 0, len(answers))
	index := 0
	for question, answer := range answers {
		list = append(list, map[string]any{
			"index":    index,
			"question": question,
			"answer":   answer,
		})
		index++
	}
	return list
}
