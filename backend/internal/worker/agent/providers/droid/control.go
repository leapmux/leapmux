package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
		// Index is Droid's own number for the question. Droid numbers its
		// questions from 1, and it identifies each answer by this number.
		Index       int      `json:"index"`
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		MultiSelect bool     `json:"multiSelect"`
	} `json:"questions"`
}

// droidPermissionResult is the result of Droid's reply to
// droid.request_permission. Droid's schema also accepts `comment` and
// `editedSpecContent`, and the worker writes neither: see
// droidResolveControlResponse for the comment.
type droidPermissionResult struct {
	SelectedOption string `json:"selectedOption"`
}

// droidAskUserResult is the result of Droid's reply to droid.ask_user. Droid's
// schema requires `answers` to be a list, even for a cancel.
type droidAskUserResult struct {
	Cancelled bool                 `json:"cancelled"`
	Answers   []droidAskUserAnswer `json:"answers"`
}

// droidAskUserAnswer is one answer of a droid.ask_user reply. Droid identifies
// the question by Index, and reports the answer to the model as
// "<index>. [question] <question>" and "[answer] <answer>".
type droidAskUserAnswer struct {
	Index    int    `json:"index"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// droidCancelledAskUser is the reply that cancels a droid.ask_user request: the
// reply that Droid's own client sends for a cancel.
func droidCancelledAskUser() droidAskUserResult {
	return droidAskUserResult{Cancelled: true, Answers: []droidAskUserAnswer{}}
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
	cancel := droidPermissionResult{SelectedOption: contracts.DroidPermissionOptionCancel}
	var params droidPermissionRequest
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad permission params", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, cancel)
		return
	}
	if len(params.ToolUses) == 0 {
		a.respond(env.ID, droidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce})
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
		"type":             contracts.DroidRequestTypePermission,
		"requestId":        requestID,
		"rpcId":            env.ID,
		"toolUse":          toolUse.ToolUse,
		"confirmationType": toolUse.ConfirmationType,
		"details":          toolUse.Details,
		"options":          droidOptionValues(params.Options),
	})
	if err != nil {
		a.respond(env.ID, cancel)
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: a.droidSession(),
		RequestID:      requestID,
		Payload:        payload,
	}); err != nil {
		slog.Debug("droid: publish permission failed", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, cancel)
	}
}

// onAskUser publishes a question control request.
func (a *Agent) onAskUser(env *droidEnvelope) {
	var params droidAskUserRequest
	if err := json.Unmarshal(env.Params, &params); err != nil {
		slog.Debug("droid: bad ask_user params", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, droidCancelledAskUser())
		return
	}
	requestID := "droid-ask-" + params.ToolCallID
	// The questions keep Droid's own order and Droid's own index: the reply
	// lists the answers in this order, and each answer states this index.
	questionPayload := make([]map[string]any, 0, len(params.Questions))
	for _, q := range params.Questions {
		questionPayload = append(questionPayload, map[string]any{
			contracts.DroidAskUserFieldIndex:       q.Index,
			contracts.DroidAskUserFieldQuestion:    q.Question,
			contracts.DroidAskUserFieldOptions:     q.Options,
			contracts.DroidAskUserFieldMultiSelect: q.MultiSelect,
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
		"type":                                contracts.DroidRequestTypeAskUser,
		"requestId":                           requestID,
		"rpcId":                               env.ID,
		contracts.DroidAskUserFieldToolCallID: params.ToolCallID,
		contracts.DroidAskUserFieldQuestions:  questionPayload,
	})
	if err != nil {
		a.respond(env.ID, droidCancelledAskUser())
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID,
		Payload:   payload,
	}); err != nil {
		slog.Debug("droid: publish question failed", "agent_id", a.AgentID(), "error", err)
		a.respond(env.ID, droidCancelledAskUser())
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

// droidStoredRequest is the part of a published control request that the reply
// reads. onRequestPermission and onAskUser write these fields.
type droidStoredRequest struct {
	Type    string `json:"type"`
	RPCID   string `json:"rpcId"`
	ToolUse struct {
		Name string `json:"name"`
	} `json:"toolUse"`
	// Options holds the option values that a permission request offers.
	Options []string `json:"options"`
	// Questions holds the questions of a question request, in Droid's order.
	Questions []droidStoredQuestion `json:"questions"`
}

// droidStoredQuestion is one question of a published question request.
type droidStoredQuestion struct {
	Index    int    `json:"index"`
	Question string `json:"question"`
}

// droidResolveControlResponse turns the browser's decision into the answer body
// of Droid. The shared suites pin two rules:
//
//   - An absent request leaves the response bytes alone.
//   - A malformed request withholds the response.
//
// The function also withholds a decision that Droid cannot take as stated. The
// refusal states why, so the reader sees the reason and can answer again. The
// function withholds these decisions:
//
//   - A behavior other than allow and deny.
//   - An option that the request did not offer, or that contradicts the behavior.
//   - Answers that do not match the questions one to one.
func droidResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) == 0 {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent}
	}
	var request droidStoredRequest
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		resolution := agent.ControlResponseResolution{Content: ctx.ResponseContent}
		resolution.Refuse(agent.RefusalUnreadableRequest)
		return resolution
	}

	_, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok || behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny {
		slog.Warn("droid control response states no decision", "request_id", ctx.RequestID, "behavior", behavior)
		var resolution agent.ControlResponseResolution
		resolution.Refuse(agent.RefusalNoDecision)
		return resolution
	}
	allow := behavior == agent.ControlBehaviorAllow
	decision := droidInnerResponse(ctx.ResponseContent)

	var resolution agent.ControlResponseResolution
	var result any
	var err error
	switch request.Type {
	case contracts.DroidRequestTypePermission:
		result, err = droidPermissionReply(request.Options, allow, decision)
		resolution.PlanModeControl = (droidProvider{}).PlanModeControl(request.ToolUse.Name)
	case contracts.DroidRequestTypeAskUser:
		result, err = droidAskUserReply(request.Questions, allow, decision)
	default:
		slog.Warn("droid stored control request has an unknown type", "request_id", ctx.RequestID, "type", request.Type)
		resolution.Refuse(agent.RefusalUnreadableRequest)
		return resolution
	}
	if err != nil {
		// The builder's error IS the reason the reader sees: each refusal of a
		// decision Droid cannot take is worded for the banner, and the same text
		// goes to the log.
		slog.Warn("droid control response refused", "request_id", ctx.RequestID, "error", err)
		resolution.Refuse(err.Error())
		return resolution
	}

	// The service forwards these bytes to the agent's stdin. Droid reads a
	// JSON-RPC RESPONSE envelope there, keyed by the id of the request it
	// answers -- a bare result body leaves the call hanging forever.
	raw, err := json.Marshal(droidResponseEnvelope(request.RPCID, result))
	if err != nil {
		resolution.Refuse(agent.RefusalUnencodableReply)
		return resolution
	}
	resolution.Content = raw
	if !allow {
		// The reason for a rejection cannot ride Droid's reply. The permission
		// reply accepts a `comment`, but Droid reads it only beside the approval
		// of a spec or of a mission proposal, and a cancel discards it for every
		// confirmation type. The question reply has no field for it. Droid's own
		// TUI also cancels with no comment and lets the user explain in the next
		// message. So the reason follows as the next user input. message is
		// empty for a bare rejection, which then queues nothing.
		resolution.Feedback = message
	}
	return resolution
}

// droidPermissionReply builds the reply to a permission request. Its error is
// the reason that the reader sees for a refusal.
//
// Allow becomes proceed_once and deny becomes cancel, unless the decision states
// its own `selectedOption`. The request must have offered any option other than
// cancel. Factory's SDK refuses to send an option that the request did not
// offer, and it cancels the request instead. A cancel needs no offer, because it
// is the reply that Droid's own client sends for every failure. The worker
// refuses proceed_edit even when the request offered it. Droid's reply schema
// requires `editedSpecContent` beside proceed_edit, and the worker sends no
// edited spec.
func droidPermissionReply(offered []string, allow bool, decision json.RawMessage) (droidPermissionResult, error) {
	var stated struct {
		SelectedOption string `json:"selectedOption"`
	}
	if err := json.Unmarshal(decision, &stated); err != nil {
		return droidPermissionResult{}, errors.New(agent.RefusalUnreadableAnswer)
	}
	option := stated.SelectedOption
	switch {
	case option == "" && allow:
		option = contracts.DroidPermissionOptionProceedOnce
	case option == "":
		option = contracts.DroidPermissionOptionCancel
	case (option == contracts.DroidPermissionOptionCancel) == allow:
		return droidPermissionResult{}, fmt.Errorf("the option %s contradicts the decision", option)
	case option == contracts.DroidPermissionOptionProceedEdit:
		return droidPermissionResult{}, errors.New("the option proceed_edit requires an edited spec, which LeapMux does not send")
	}
	if option != contracts.DroidPermissionOptionCancel && !slices.Contains(offered, option) {
		return droidPermissionResult{}, errors.New(agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, option))
	}
	return droidPermissionResult{SelectedOption: option}, nil
}

// droidAskUserReply builds the reply to a question request. A refusal is
// Droid's own cancel, which carries no answer.
func droidAskUserReply(questions []droidStoredQuestion, allow bool, decision json.RawMessage) (droidAskUserResult, error) {
	if !allow {
		return droidCancelledAskUser(), nil
	}
	answers, err := droidAnswersInQuestionOrder(questions, decision)
	if err != nil {
		return droidAskUserResult{}, err
	}
	return droidAskUserResult{Answers: answers}, nil
}

// droidDecidedAnswer is one answer of the browser's decision. Pointers tell an
// absent field from a zero value: an answer with no index or no text identifies
// no question.
type droidDecidedAnswer struct {
	Index  *int    `json:"index"`
	Answer *string `json:"answer"`
}

// droidAnswersInQuestionOrder pairs each answer of the browser's decision with
// its question by Droid's own index, and lists the answers in the order of the
// questions, with the index and the words of each question.
//
// The decision must answer every question exactly once, and nothing else.
// Droid's AskUser tool fails when the count of answers differs from the count
// of questions, and Droid's own client refuses an unknown or repeated index.
func droidAnswersInQuestionOrder(questions []droidStoredQuestion, decision json.RawMessage) ([]droidAskUserAnswer, error) {
	var stated struct {
		// A pointer tells an absent list from an empty one: an absent list
		// answers none.
		Answers *[]droidDecidedAnswer `json:"answers"`
	}
	if err := json.Unmarshal(decision, &stated); err != nil {
		return nil, errors.New(agent.RefusalUnreadableAnswer)
	}
	if stated.Answers == nil {
		return nil, errors.New("the decision holds no answer list")
	}
	byIndex := make(map[int]string, len(*stated.Answers))
	for _, answer := range *stated.Answers {
		if answer.Index == nil || answer.Answer == nil {
			return nil, errors.New("an answer states no index or no text")
		}
		if _, repeated := byIndex[*answer.Index]; repeated {
			return nil, fmt.Errorf("two answers state the index %d", *answer.Index)
		}
		byIndex[*answer.Index] = *answer.Answer
	}
	answers := make([]droidAskUserAnswer, 0, len(questions))
	asked := make(map[int]bool, len(questions))
	for _, question := range questions {
		if asked[question.Index] {
			return nil, fmt.Errorf("two questions state the index %d", question.Index)
		}
		asked[question.Index] = true
		text, answered := byIndex[question.Index]
		if !answered {
			return nil, fmt.Errorf("the question with the index %d has no answer", question.Index)
		}
		answers = append(answers, droidAskUserAnswer{Index: question.Index, Question: question.Question, Answer: text})
	}
	// Every question found its own answer, so a longer answer set holds an
	// index that no question states.
	if len(byIndex) != len(answers) {
		return nil, errors.New("an answer states an index that no question states")
	}
	return answers, nil
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
