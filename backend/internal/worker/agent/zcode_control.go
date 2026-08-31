package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// ZCode's control requests: a permission prompt, a plan approval, a question.
//
// Each one arrives as a REQUEST on the same pipe the event stream uses, and the
// app-server blocks the turn until it is answered. LeapMux answers none of them
// itself: it persists a pending control request, the user decides, and the answer
// comes back through SendControlResponse -> ResolveControlResponse, which builds
// the app-server's reply frame.
//
// The stored payload is a HYBRID: ZCode's own params verbatim, plus the
// Claude-shaped `{request:{tool_name, tool_use_id, input}}` header. The header is
// what the SHARED control surfaces read -- the service resolves the tool name from
// it, and the shared AskUserQuestion control reads `request.input.questions` -- so
// ZCode reuses those surfaces instead of duplicating them.

// zcodeControlPlanApproval is the tool name recorded for a plan approval. It is not
// a ZCode tool: the app-server asks for plan approval through
// interaction/requestUserInput with a `plan_approval` schema, and LeapMux's plan
// machinery keys on a tool name. Spelled like ZCode's own plan tool so the frontend
// renders the plan-approval surface for it.
const zcodeControlPlanApproval = contracts.ZCodeToolNameExitPlanMode

// zcodeControlAskUserQuestion is the tool name recorded for a question, matching the
// name every provider's shared AskUserQuestion control recognizes.
const zcodeControlAskUserQuestion = contracts.ZCodeToolNameAskUserQuestion

// zcodePermissionRequest is the interaction/requestPermission params.
type zcodePermissionRequest struct {
	RequestID  string                  `json:"requestId"`
	SessionID  string                  `json:"sessionId"`
	ToolCallID string                  `json:"toolCallId"`
	ToolName   string                  `json:"toolName"`
	RiskLevel  string                  `json:"riskLevel"`
	Reason     string                  `json:"reason"`
	Input      json.RawMessage         `json:"input"`
	Options    []zcodePermissionOption `json:"options"`
}

// zcodePermissionOption is one offered answer.
//
// `response` is the load-bearing field: the app-server embeds the COMPLETE reply
// for the option, including the permission-rule updates a "always allow" choice
// carries. Echoing it back is both simpler and more correct than rebuilding a
// decision object, which would drop those updates.
type zcodePermissionOption struct {
	OptionID string          `json:"optionId"`
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// handlePermissionRequest persists a permission prompt and broadcasts it.
//
// It does NOT reply. The reply is built from the user's answer in
// ResolveControlResponse, and the app-server waits for it -- which is the whole
// point of a permission prompt.
func (a *zcodeAgent) handlePermissionRequest(id, params json.RawMessage) {
	var req zcodePermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		slog.Warn("zcode permission request unmarshal failed", "agent_id", a.agentID, "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not read the permission request")
		return
	}
	requestID := req.RequestID
	if requestID == "" {
		// Without an id the answer cannot be routed back, and an unanswerable prompt
		// would stall the turn for good. Denying is the safe direction: it lets the
		// model report the refusal and continue.
		slog.Warn("zcode permission request carried no request id", "agent_id", a.agentID, "tool", req.ToolName)
		a.replyZCodePermission(id, req.Options, ControlBehaviorDeny, "leapmux could not route this permission request")
		return
	}

	// Check for a repeat BEFORE the marshal below. The app-server re-announces an
	// unanswered request every second, doubling to ten, for as long as the user takes to
	// answer, and the payload embeds the whole tool input each time. Republishing the
	// stored bytes skips that marshal, and it keeps a marshal failure on a REPEAT from
	// sending replyZCodeControlFailure, which would resolve the app-server's request and
	// destroy the prompt the user still holds.
	if stored := a.pendingZCodeControlPayload(requestID); stored != nil {
		slog.Debug("zcode re-announced permission request republished", "agent_id", a.agentID, "request_id", requestID)
		a.publishZCodeControlRequest(requestID, stored)
		return
	}

	payload, err := json.Marshal(zcodeControlRequestPayload{
		Type:      "control_request",
		RequestID: requestID,
		WireID:    id,
		Method:    ZCodeMethodRequestPermission,
		Request: zcodeControlRequestHeader{
			ToolName:  req.ToolName,
			ToolUseID: req.ToolCallID,
			Input:     req.Input,
		},
		Params: params,
	})
	if err != nil {
		slog.Error("zcode marshal permission control request", "agent_id", a.agentID, "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the permission request")
		return
	}

	a.rememberZCodeControlRequest(requestID, payload)
	a.publishZCodeControlRequest(requestID, payload)
}

// zcodeUserInputRequest is the interaction/requestUserInput params.
type zcodeUserInputRequest struct {
	RequestID  string `json:"requestId"`
	SessionID  string `json:"sessionId"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Prompt     string `json:"prompt"`
	Schema     struct {
		Interaction string               `json:"interaction"`
		Questions   []zcodeInputQuestion `json:"questions"`
	} `json:"schema"`
	Questions []zcodeInputQuestion `json:"questions"`
	Context   json.RawMessage      `json:"context"`
}

// zcodeInputQuestion is one question of a requestUserInput.
type zcodeInputQuestion struct {
	Question    string             `json:"question"`
	Header      string             `json:"header"`
	MultiSelect bool               `json:"multiSelect"`
	Options     []zcodeInputOption `json:"options"`
}

type zcodeInputOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// questions returns the question list from whichever field carried it. The
// app-server declares them under `schema.questions`, and a build that hoists them to
// the top level is accepted too, so one reader serves both.
func (r zcodeUserInputRequest) questions() []zcodeInputQuestion {
	if len(r.Schema.Questions) > 0 {
		return r.Schema.Questions
	}
	return r.Questions
}

// isPlanApproval reports whether this request is the plan-approval prompt rather
// than a question.
func (r zcodeUserInputRequest) isPlanApproval() bool {
	return r.Schema.Interaction == ZCodeInteractionPlanApproval
}

// planText returns the plan a plan approval asks about.
//
// The app-server puts the plan under `context.plan` and leaves `prompt` holding its own
// boilerplate ("Review this implementation plan."). The plan must therefore travel as its
// own field: the frontend reads the first question text, and the question a plan approval
// gets is synthesized here, so a plan left in `context` never reaches the banner.
//
// Falls back to `prompt` for a build that states the plan there and sends no context.
func (r zcodeUserInputRequest) planText() string {
	var ctx struct {
		Plan string `json:"plan"`
	}
	if len(r.Context) > 0 {
		// A context that does not decode is not an error here: the plan simply is not in it.
		_ = json.Unmarshal(r.Context, &ctx)
	}
	if strings.TrimSpace(ctx.Plan) != "" {
		return ctx.Plan
	}
	return r.Prompt
}

// handleUserInputRequest persists a plan approval or a question.
func (a *zcodeAgent) handleUserInputRequest(id, params json.RawMessage) {
	var req zcodeUserInputRequest
	if err := json.Unmarshal(params, &req); err != nil {
		slog.Warn("zcode user input request unmarshal failed", "agent_id", a.agentID, "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not read the user-input request")
		return
	}
	if req.RequestID == "" {
		slog.Warn("zcode user input request carried no request id", "agent_id", a.agentID)
		a.replyZCodeUserInput(id, ControlBehaviorDeny, "leapmux could not route this request",
			zcodeUserInputReply{PlanApproval: req.isPlanApproval()})
		return
	}

	// Check for a repeat BEFORE the two marshals below. The app-server re-announces an
	// unanswered request every second, doubling to ten, for as long as the user takes to
	// answer, and each repeat carries the whole tool input again. Republishing the stored
	// bytes skips both marshals, and it keeps a marshal failure on a REPEAT from sending
	// replyZCodeControlFailure, which would resolve the app-server's request and destroy
	// the prompt the user still holds.
	if stored := a.pendingZCodeControlPayload(req.RequestID); stored != nil {
		slog.Debug("zcode re-announced user-input request republished", "agent_id", a.agentID, "request_id", req.RequestID)
		a.publishZCodeControlRequest(req.RequestID, stored)
		return
	}

	toolName := zcodeControlAskUserQuestion
	if req.isPlanApproval() {
		toolName = zcodeControlPlanApproval
	}

	// The questions are re-encoded in the shape the SHARED AskUserQuestion control
	// reads -- {question, header, multiSelect, options:[{value,label}]} -- so the
	// answer it builds is keyed by question text, which is exactly what the
	// app-server's `content.answers` map expects. `plan` carries the plan text of a plan
	// approval, which the synthesized question cannot hold (see planText).
	input, err := json.Marshal(map[string]any{
		"questions": zcodeSharedQuestions(req),
		"prompt":    req.Prompt,
		"plan":      req.planText(),
	})
	if err != nil {
		slog.Error("zcode marshal user input questions", "agent_id", a.agentID, "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the user-input request")
		return
	}

	payload, err := json.Marshal(zcodeControlRequestPayload{
		Type:      "control_request",
		RequestID: req.RequestID,
		WireID:    id,
		Method:    ZCodeMethodRequestUserInput,
		Request: zcodeControlRequestHeader{
			ToolName:  toolName,
			ToolUseID: req.ToolCallID,
			Input:     input,
		},
		Params: params,
	})
	if err != nil {
		slog.Error("zcode marshal user input control request", "agent_id", a.agentID, "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the user-input request")
		return
	}

	a.rememberZCodeControlRequest(req.RequestID, payload)
	a.publishZCodeControlRequest(req.RequestID, payload)
}

// zcodeSharedQuestions projects ZCode's questions into the shared control's shape.
//
// A plan approval carries no questions of its own, so one is synthesized: the
// shared surface needs a question to key the answer by, and the app-server reads
// that key back out of `content.answers`.
func zcodeSharedQuestions(req zcodeUserInputRequest) []map[string]any {
	questions := req.questions()
	if len(questions) == 0 && req.isPlanApproval() {
		return []map[string]any{{
			"question":    ZCodePlanApprovalQuestion,
			"header":      "Plan",
			"multiSelect": false,
			"options": []map[string]any{
				{"value": ZCodePlanApproveSentinel, "label": ZCodePlanApproveSentinel},
			},
		}}
	}
	out := make([]map[string]any, 0, len(questions))
	for _, q := range questions {
		options := make([]map[string]any, 0, len(q.Options))
		for _, o := range q.Options {
			label := o.Label
			if label == "" {
				label = o.Value
			}
			value := o.Value
			if value == "" {
				value = o.Label
			}
			options = append(options, map[string]any{"value": value, "label": label})
		}
		out = append(out, map[string]any{
			"question":    q.Question,
			"header":      q.Header,
			"multiSelect": q.MultiSelect,
			"options":     options,
		})
	}
	return out
}

// zcodeControlRequestPayload is the stored control-request payload.
//
// It is the ONE place ZCode's request and the shared control surfaces meet, so it
// carries both spellings deliberately:
//   - `request` is the Claude-shaped header the service and the shared frontend
//     controls read (tool name, tool-use id, tool input / questions).
//   - `params` is ZCode's own request verbatim, which the answer path reads the
//     option list off.
//   - `id` is the WIRE id of the app-server's request, which the reply must echo.
//     Spelled `id` so ExtractJSONRPCID finds it, like every other provider's stored
//     request.
type zcodeControlRequestPayload struct {
	Type      string                    `json:"type"`
	RequestID string                    `json:"request_id"`
	WireID    json.RawMessage           `json:"id,omitempty"`
	Method    string                    `json:"method"`
	Request   zcodeControlRequestHeader `json:"request"`
	Params    json.RawMessage           `json:"params,omitempty"`
}

type zcodeControlRequestHeader struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// --- replies LeapMux sends without asking the user ---

// replyZCodeControlFailure answers a request LeapMux could not even read.
//
// An ERROR is the right answer here, unlike a denial: a denial is a decision, and
// LeapMux made none. The app-server reports the failure to the model, which can
// then say what went wrong instead of claiming the user refused.
func (a *zcodeAgent) replyZCodeControlFailure(id json.RawMessage, message string) {
	if err := a.sendZCodeErrorReply(id, ZCodeErrInternal, message); err != nil {
		slog.Warn("zcode control failure reply failed", "agent_id", a.agentID, "error", err)
	}
}

// replyZCodePermission answers a permission request directly, for the paths where
// no user decision is possible.
func (a *zcodeAgent) replyZCodePermission(id json.RawMessage, options []zcodePermissionOption, behavior, reason string) {
	result, err := zcodePermissionResult(options, behavior, reason)
	if err != nil {
		a.replyZCodeControlFailure(id, err.Error())
		return
	}
	if err := a.sendZCodeReply(id, result); err != nil {
		slog.Warn("zcode permission reply failed", "agent_id", a.agentID, "error", err)
	}
}

// replyZCodeUserInput answers a user-input request directly.
func (a *zcodeAgent) replyZCodeUserInput(id json.RawMessage, behavior, message string, reply zcodeUserInputReply) {
	if err := a.sendZCodeReply(id, zcodeUserInputResult(behavior, message, reply)); err != nil {
		slog.Warn("zcode user input reply failed", "agent_id", a.agentID, "error", err)
	}
}

// answerProviderRuntimeHeaders answers the app-server's request for freshly-minted
// provider credentials.
//
// LeapMux mints none: it reads ZCode's own configuration and forwards the API key it
// finds there, which the app-server already holds through the provider registry. So
// the honest answer is "no headers" -- with `authorized` set only when that inline
// key actually exists, because reporting an authorization LeapMux cannot back would
// make the app-server retry a request that can never succeed.
func (a *zcodeAgent) answerProviderRuntimeHeaders(id, params json.RawMessage) {
	var req struct {
		ProviderID string `json:"providerId"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		// Do NOT fall through. An undecodable request leaves ProviderID empty, and
		// hasInlineAPIKey reads an empty id as "any provider" -- so LeapMux would answer
		// `authorized: true` for a provider it holds no credential for, and the turn would
		// fail later with an authentication error from the model provider instead of here.
		slog.Warn("zcode provider runtime headers unmarshal failed", "agent_id", a.agentID, "error", err)
		if err := a.sendZCodeReply(id, map[string]any{
			"headers":    map[string]string{},
			"authorized": false,
			"error":      "leapmux could not read the provider runtime headers request",
		}); err != nil {
			slog.Warn("zcode provider runtime headers reply failed", "agent_id", a.agentID, "error", err)
		}
		return
	}
	authorized := a.catalog.hasInlineAPIKey(req.ProviderID)
	result := map[string]any{
		"headers":    map[string]string{},
		"authorized": authorized,
	}
	if !authorized {
		result["error"] = fmt.Sprintf("leapmux holds no credential for provider %q; add it to ZCode's configuration", req.ProviderID)
	}
	if err := a.sendZCodeReply(id, result); err != nil {
		slog.Warn("zcode provider runtime headers reply failed", "agent_id", a.agentID, "error", err)
	}
}

// answerOfficialMcpAuthHeaders answers the request for credentials to ZCode's
// hosted MCP servers, which LeapMux does not hold.
//
// It is answered rather than ignored: the app-server blocks the tool behind it, and
// a declared unavailability lets it fall through to the servers it can reach.
func (a *zcodeAgent) answerOfficialMcpAuthHeaders(id json.RawMessage) {
	if err := a.sendZCodeReply(id, map[string]any{
		"status":  ZCodeOfficialAuthUnavailable,
		"headers": map[string]string{},
	}); err != nil {
		slog.Warn("zcode official mcp auth reply failed", "agent_id", a.agentID, "error", err)
	}
}

// --- reply construction, shared with ResolveControlResponse ---

// zcodePermissionResult builds the reply to a permission request.
//
// The chosen option's OWN embedded response wins, because it carries the
// permission-rule updates that an "always allow" option implies -- rebuilding a
// bare decision object would silently drop them and re-prompt next time.
//
// When no option matches, a bare decision is synthesized. For a DENIAL that is
// always correct. For an ALLOW it is the honest fallback, and it is why the
// option-matching is fail-safe: an option list that offers no allow at all cannot
// produce one.
func zcodePermissionResult(options []zcodePermissionOption, behavior, reason string) (json.RawMessage, error) {
	want := contracts.ZCodeDecisionAllow
	if behavior != ControlBehaviorAllow {
		want = contracts.ZCodeDecisionDeny
	}
	if option := zcodeOptionForDecision(options, want); option != nil {
		return zcodeOptionResponse(*option, want, reason)
	}
	if want == contracts.ZCodeDecisionAllow && len(options) > 0 {
		// The app-server offered options and none of them allows. Answering "allow"
		// anyway would grant something no offered option covers, so the request is
		// denied and the reason says why.
		return zcodeDecisionResult(contracts.ZCodeDecisionDeny, "no offered option allows this tool call")
	}
	return zcodeDecisionResult(want, reason)
}

// zcodeOptionForDecision finds the offered option whose embedded response carries
// the wanted decision. Returns nil when none does.
func zcodeOptionForDecision(options []zcodePermissionOption, decision string) *zcodePermissionOption {
	for i := range options {
		var body struct {
			Decision string `json:"decision"`
		}
		if len(options[i].Response) == 0 {
			continue
		}
		if err := json.Unmarshal(options[i].Response, &body); err != nil {
			continue
		}
		if body.Decision == decision {
			return &options[i]
		}
	}
	return nil
}

// zcodeOptionResponse returns an option's embedded response, with the user's
// rejection reason attached when they typed one.
func zcodeOptionResponse(option zcodePermissionOption, decision, reason string) (json.RawMessage, error) {
	if reason == "" {
		return option.Response, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(option.Response, &body); err != nil || body == nil {
		return zcodeDecisionResult(decision, reason)
	}
	encoded, err := json.Marshal(reason)
	if err != nil {
		return option.Response, nil
	}
	body["reason"] = encoded
	out, err := json.Marshal(body)
	if err != nil {
		return option.Response, nil
	}
	return out, nil
}

// zcodeDecisionResult builds a bare decision object.
func zcodeDecisionResult(decision, reason string) (json.RawMessage, error) {
	body := map[string]any{"decision": decision}
	if reason != "" {
		body["reason"] = reason
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal permission decision: %w", err)
	}
	return encoded, nil
}

// zcodeUserInputReply is everything the reply to one requestUserInput depends on.
//
// The two interactions the app-server multiplexes over requestUserInput read their
// answer through DIFFERENT readers, so one reply shape cannot serve both -- see
// zcodeUserInputResult.
type zcodeUserInputReply struct {
	// PlanApproval selects the plan reader rather than the question reader.
	PlanApproval bool
	// Questions is the request's question text, IN ORDER. It is what the question
	// reader derives its `content.answers` keys from, so the reply's keys must be
	// these strings and the positional fallback must follow this order.
	Questions []string
	// Answers maps question text to the answer the user gave.
	Answers map[string]string
}

// zcodeUserInputResult builds the reply to a requestUserInput.
//
// The app-server has TWO readers for the `content` object, and which one runs is
// decided by the interaction, not by the reply:
//
//   - A QUESTION reads `content.answers[<question text>]`, falling back to
//     `content.answer_<index>` and then to `content.answer` for a single question.
//     The keys come from the REQUEST's questions, so an answer under any other key
//     is discarded.
//   - A PLAN APPROVAL reads `content.answers["Review this implementation plan."]`,
//     then `content.answer_0`, then `content.answer`. The default prompt uses that
//     question text, but a plan approval that carries a reason states the REASON as
//     its question text -- so only the positional forms are dependable.
//
// Both readers treat "no answer found" as a DENIAL, silently. That is why the reply
// carries the keyed map and the positional `answer_<index>` form together: the keyed
// map is exact when the question text survived the round trip, and the positional
// form is what answers a plan approval or a question whose text was empty.
//
// A REJECTION is asymmetric for the same reason. A question reads `reason` off a
// `decline`, so a decline carries it. The plan path does NOT -- its reply mapper
// drops every field but `action`, so a decline there loses the text. A rejection
// with feedback is therefore sent as an ACCEPT whose answer is the feedback, which
// the plan path resolves to a denial that carries the feedback to the model. A
// rejection with nothing typed has no text to preserve and declines outright.
func zcodeUserInputResult(behavior, message string, reply zcodeUserInputReply) map[string]any {
	if behavior != ControlBehaviorAllow {
		feedback := strings.TrimSpace(message)
		if reply.PlanApproval && feedback != "" {
			return map[string]any{
				ZCodeReplyActionField: ZCodeActionAccept,
				ZCodeReplyContentField: map[string]any{
					ZCodeAnswerField: feedback,
				},
			}
		}
		out := map[string]any{ZCodeReplyActionField: ZCodeActionDecline}
		if feedback != "" {
			out["reason"] = feedback
		}
		return out
	}

	if reply.PlanApproval {
		// The sentinel is the ONLY value the plan reader accepts as approval; any other
		// non-empty answer is read back as reviewer feedback, which is a denial.
		return map[string]any{
			ZCodeReplyActionField: ZCodeActionAccept,
			ZCodeReplyContentField: map[string]any{
				ZCodeAnswerField: ZCodePlanApproveSentinel,
			},
		}
	}
	return map[string]any{
		ZCodeReplyActionField:  ZCodeActionAccept,
		ZCodeReplyContentField: zcodeAnswerContent(reply),
	}
}

// zcodeAnswerContent builds the `content` object of an accepted question reply.
//
// The keyed map is emitted only for a question whose text is non-empty, because an
// empty key matches nothing the reader looks for. The positional form covers every
// question the request declared, in its order, so an answer still lands when the
// text did not survive. An answer that is blank after trimming is omitted from both:
// the reader discards a blank answer, and sending one would only make the map look
// answered.
//
// `answers` is omitted entirely when it would be empty. An EMPTY answers object is
// not the same as an absent one to the reader -- it short-circuits to "answered with
// nothing" and suppresses the positional fallback.
func zcodeAnswerContent(reply zcodeUserInputReply) map[string]any {
	content := map[string]any{}
	keyed := map[string]string{}
	for i, question := range reply.Questions {
		answer := strings.TrimSpace(reply.Answers[question])
		if answer == "" {
			continue
		}
		if trimmed := strings.TrimSpace(question); trimmed != "" {
			keyed[trimmed] = answer
		}
		content[fmt.Sprintf("%s%d", ZCodeAnswerIndexedPrefix, i)] = answer
	}
	// An answer whose question the request never declared cannot be placed
	// positionally, but its key can still match. Adding it is strictly better than
	// dropping the user's answer.
	for question, answer := range reply.Answers {
		trimmed, value := strings.TrimSpace(question), strings.TrimSpace(answer)
		if trimmed == "" || value == "" {
			continue
		}
		if _, ok := keyed[trimmed]; !ok {
			keyed[trimmed] = value
		}
	}
	if len(keyed) > 0 {
		content[ZCodeAnswerMapField] = keyed
	}
	return content
}

// --- permission bookkeeping ---

// pendingZCodeControlPayload returns the payload the FIRST announcement of requestID
// stored, or nil when no announcement of it is pending.
//
// The app-server re-announces an unanswered interaction request on a timer that starts
// at one second and doubles to ten, each time with a fresh WIRE id and the same
// `requestId`. Every announcement after the first repeats a prompt the user already
// holds, and publishing a freshly built payload would show a second banner: the frontend
// de-duplicates on the request id AND the payload, and the payload differs because the
// wire id is inside it.
//
// The caller therefore republishes THESE bytes rather than its own. Byte-identical bytes
// leave an open banner alone, and they restore one that is gone -- which is what keeps a
// prompt whose answer produced no reply reachable, instead of dropping every repeat and
// blocking the turn for good. Answering the FIRST wire id still resolves the request: the
// app-server registers every announced wire id against the same pending request.
//
// The record also makes the app-server's own permission.resolved for this request
// recognizable as the echo of the user's answer rather than as an automatic decision.
func (a *zcodeAgent) pendingZCodeControlPayload(requestID string) json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingControls[requestID]
}

// rememberZCodeControlRequest stores the first announcement's payload for requestID.
func (a *zcodeAgent) rememberZCodeControlRequest(requestID string, payload json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingControls == nil {
		a.pendingControls = map[string]json.RawMessage{}
	}
	a.pendingControls[requestID] = payload
}

// publishZCodeControlRequest persists a control prompt and broadcasts it to every window.
// Shared by the first announcement and by each republished repeat, so the two can never
// store one payload and show another.
func (a *zcodeAgent) publishZCodeControlRequest(requestID string, payload json.RawMessage) {
	claimToken := a.sink.PersistControlRequest(requestID, payload)
	a.sink.BroadcastControlRequest(requestID, payload, claimToken)
}

// forgetZCodeControlRequest reports whether requestID was a prompt LeapMux
// forwarded, and drops it. Consumed by permission.resolved and userInput.resolved,
// which is what re-arms the de-duplication for a reused request id.
func (a *zcodeAgent) forgetZCodeControlRequest(requestID string) bool {
	if requestID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingControls[requestID] == nil {
		return false
	}
	delete(a.pendingControls, requestID)
	return true
}

// zcodeReplyForAnswer builds the `result` of the reply for one answered request.
func zcodeReplyForAnswer(stored zcodeControlRequestPayload, behavior, message string, responseContent []byte) (any, error) {
	switch stored.Method {
	case ZCodeMethodRequestPermission:
		var req zcodePermissionRequest
		if len(stored.Params) > 0 {
			if err := json.Unmarshal(stored.Params, &req); err != nil {
				return nil, fmt.Errorf("read stored permission request: %w", err)
			}
		}
		result, err := zcodePermissionResult(req.Options, behavior, message)
		if err != nil {
			return nil, err
		}
		return result, nil
	case ZCodeMethodRequestUserInput:
		return zcodeUserInputResult(behavior, message, zcodeUserInputReply{
			PlanApproval: stored.Request.ToolName == zcodeControlPlanApproval,
			Questions:    zcodeStoredQuestionTexts(stored.Request.Input),
			Answers:      zcodeAnswersFromResponse(responseContent),
		}), nil
	default:
		return nil, fmt.Errorf("stored control request has no reply shape: method %q", stored.Method)
	}
}

// zcodeStoredQuestionTexts reads the question texts, IN ORDER, out of the stored
// control request's input.
//
// The order is what makes the positional `answer_<index>` fallback line up with the
// app-server's own question list, so it must be the order the request declared and
// not a map iteration.
func zcodeStoredQuestionTexts(input json.RawMessage) []string {
	if len(input) == 0 {
		return nil
	}
	var body struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &body); err != nil {
		return nil
	}
	out := make([]string, 0, len(body.Questions))
	for _, q := range body.Questions {
		out = append(out, q.Question)
	}
	return out
}

// zcodeAnswersFromResponse reads the AskUserQuestion answers the shared control
// attached to its allow envelope: `response.response.updatedInput.answers`, a map of
// question text to the joined labels the user picked.
func zcodeAnswersFromResponse(content []byte) map[string]string {
	var env struct {
		Response struct {
			Response struct {
				UpdatedInput struct {
					Answers map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &env); err != nil {
		return nil
	}
	return env.Response.Response.UpdatedInput.Answers
}

// zcodeControlRequestContext prunes a stored ZCode control request to what the
// frontend needs to render the ANSWER after the pending request is deleted: the
// tool name, and the option or question labels the answer refers to.
func zcodeControlRequestContext(stored zcodeControlRequestPayload) json.RawMessage {
	type option struct {
		OptionID string `json:"optionId,omitempty"`
		Name     string `json:"name,omitempty"`
	}
	type question struct {
		Question string `json:"question,omitempty"`
		Header   string `json:"header,omitempty"`
	}
	ctx := struct {
		Method  string `json:"method,omitempty"`
		Request struct {
			ToolName string `json:"tool_name,omitempty"`
		} `json:"request"`
		Options   []option   `json:"options,omitempty"`
		Questions []question `json:"questions,omitempty"`
	}{Method: stored.Method}
	ctx.Request.ToolName = stored.Request.ToolName

	switch stored.Method {
	case ZCodeMethodRequestPermission:
		var req zcodePermissionRequest
		if len(stored.Params) > 0 && json.Unmarshal(stored.Params, &req) == nil {
			for _, o := range req.Options {
				ctx.Options = append(ctx.Options, option{OptionID: o.OptionID, Name: o.Name})
			}
		}
	case ZCodeMethodRequestUserInput:
		var input struct {
			Questions []question `json:"questions"`
		}
		if len(stored.Request.Input) > 0 && json.Unmarshal(stored.Request.Input, &input) == nil {
			ctx.Questions = input.Questions
		}
	}
	return marshalControlRequestContext(ctx)
}
