package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode's control requests: a permission prompt, a plan approval, a question.
//
// Each request arrives on the event pipe and blocks the turn until an answer arrives.
// Bypass answers ordinary tool permissions with the offered one-time response.
// Questions and plan approvals remain manual.
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

// zcodeControlMCPElicitation is the tool name recorded for an MCP server's input
// form: ZCode's own name for the interaction (`toolName` of the request). It keeps
// the form off the question surface, because the browser draws it as a typed MCP
// form instead. See zcodeMCPElicitationResult.
const zcodeControlMCPElicitation = "McpElicitation"

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
func (a *Agent) handlePermissionRequest(id, params json.RawMessage) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	var req zcodePermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		slog.Warn("zcode permission request unmarshal failed", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not read the permission request")
		return
	}
	if !a.isCurrentZCodeSession(req.SessionID) {
		a.replyZCodePermission(id, req.Options, agent.ControlBehaviorDeny, "The session is no longer active.")
		slog.Debug("zcode declined a retired session permission request", "session_id", req.SessionID)
		return
	}
	requestID := req.RequestID
	if requestID == "" {
		// Without an id the answer cannot be routed back, and an unanswerable prompt
		// would stall the turn for good. Denying is the safe direction: it lets the
		// model report the refusal and continue.
		slog.Warn("zcode permission request carried no request id", "agent_id", a.AgentID(), "tool", req.ToolName)
		a.replyZCodePermission(id, req.Options, agent.ControlBehaviorDeny, "leapmux could not route this permission request")
		return
	}
	if _, err := a.supplementZCodeControlInput(req.ToolCallID, req.ToolName, req.Input); err != nil {
		slog.Warn("recover permission tool input", "agent_id", a.AgentID(), "tool_call_id", req.ToolCallID, "error", err)
	}

	// Check for a repeat BEFORE the marshal below. The app-server re-announces an
	// unanswered request every second, doubling to ten, for as long as the user takes to
	// answer, and the payload embeds the whole tool input each time. Republishing the
	// stored bytes skips that marshal, and it keeps a marshal failure on a REPEAT from
	// sending replyZCodeControlFailure, which would resolve the app-server's request and
	// destroy the prompt the user still holds.
	if stored := a.pendingZCodeControlPayload(requestID); stored != nil {
		slog.Debug("zcode re-announced permission request republished", "agent_id", a.AgentID(), "request_id", requestID)
		a.publishZCodeControlRequest(requestID)
		return
	}

	payload, err := json.Marshal(zcodeControlRequestPayload{
		Type:      "control_request",
		RequestID: requestID,
		WireID:    id,
		Method:    contracts.ZCodeMethodRequestPermission,
		Request: zcodeControlRequestHeader{
			ToolName:  req.ToolName,
			ToolUseID: req.ToolCallID,
			Input:     req.Input,
		},
		Params: params,
	})
	if err != nil {
		slog.Error("zcode marshal permission control request", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the permission request")
		return
	}

	a.rememberZCodeControlRequest(requestID, payload)
	a.publishZCodeControlRequest(requestID)
}

// zcodeUserInputRequest is the interaction/requestUserInput params.
type zcodeUserInputRequest struct {
	RequestID  string          `json:"requestId"`
	SessionID  string          `json:"sessionId"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Prompt     string          `json:"prompt"`
	Input      json.RawMessage `json:"input"`
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
	return r.Schema.Interaction == contracts.ZCodeInteractionPlanApproval
}

// isMCPElicitation reports whether this request is an MCP server's input form,
// which ZCode also states as questions.
func (r zcodeUserInputRequest) isMCPElicitation() bool {
	return r.Schema.Interaction == contracts.ZCodeInteractionMcpElicitation
}

// planText reads the native tool input first, then context, then the prompt fallback.
func (r zcodeUserInputRequest) planText() string {
	for _, data := range []json.RawMessage{r.Input, r.Context} {
		var content struct {
			Plan string `json:"plan"`
		}
		if json.Unmarshal(data, &content) == nil && strings.TrimSpace(content.Plan) != "" {
			return content.Plan
		}
	}
	return r.Prompt
}

// toolInput adds a missing plan field from the native context or prompt.
func (r zcodeUserInputRequest) toolInput() (json.RawMessage, error) {
	if !r.isPlanApproval() || strings.TrimSpace(r.planText()) == "" {
		return r.Input, nil
	}
	var fields map[string]json.RawMessage
	if !zcodeInputIsAbsent(r.Input) && json.Unmarshal(r.Input, &fields) != nil {
		return r.Input, nil
	}
	if _, exists := fields["plan"]; exists {
		return r.Input, nil
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	plan, err := json.Marshal(r.planText())
	if err != nil {
		return nil, err
	}
	fields["plan"] = plan
	return json.Marshal(fields)
}

// handleUserInputRequest persists a plan approval or a question.
func (a *Agent) handleUserInputRequest(id, params, original json.RawMessage) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	var req zcodeUserInputRequest
	if err := json.Unmarshal(params, &req); err != nil {
		slog.Warn("zcode user input request unmarshal failed", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not read the user-input request")
		return
	}
	if !a.isCurrentZCodeSession(req.SessionID) {
		a.replyZCodeUserInput(id, agent.ControlBehaviorDeny, zcodeUserInputReply{PlanApproval: req.isPlanApproval()})
		slog.Debug("zcode declined a retired session input request", "session_id", req.SessionID)
		return
	}
	if req.RequestID == "" {
		// Without an id the answer cannot be routed back, and an unanswerable prompt
		// would stall the turn. LeapMux denies it, so the turn continues.
		slog.Warn("leapmux denied a zcode user-input request with no request id", "agent_id", a.AgentID())
		a.replyZCodeUserInput(id, agent.ControlBehaviorDeny, zcodeUserInputReply{PlanApproval: req.isPlanApproval()})
		return
	}
	nativeInput, err := req.toolInput()
	if err != nil {
		slog.Warn("encode interaction tool input", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "LeapMux could not read the tool input.")
		return
	}
	carriesInput, sourceErr := a.supplementZCodeControlInput(req.ToolCallID, req.ToolName, nativeInput)
	if sourceErr != nil {
		slog.Warn("recover interaction tool input", "agent_id", a.AgentID(), "tool_call_id", req.ToolCallID, "error", sourceErr)
	}

	// Check for a repeat BEFORE the two marshals below. The app-server re-announces an
	// unanswered request every second, doubling to ten, for as long as the user takes to
	// answer, and each repeat carries the whole tool input again. Republishing the stored
	// bytes skips both marshals, and it keeps a marshal failure on a REPEAT from sending
	// replyZCodeControlFailure, which would resolve the app-server's request and destroy
	// the prompt the user still holds.
	if stored := a.pendingZCodeControlPayload(req.RequestID); stored != nil {
		slog.Debug("zcode re-announced user-input request republished", "agent_id", a.AgentID(), "request_id", req.RequestID)
		a.publishZCodeControlRequest(req.RequestID)
		return
	}
	// The transcript confirms no row that carries this plan, so this call writes one.
	// A read that FAILED confirms nothing either, and it is treated as "the row is
	// there": a second plan row over one already in the transcript draws it twice.
	if req.isPlanApproval() && !carriesInput && sourceErr == nil {
		if len(original) == 0 {
			a.replyZCodeControlFailure(id, "The original plan request is unavailable.")
			return
		}
		spanID := req.ToolCallID
		if spanID == "" {
			spanID = req.RequestID
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: spanID, SpanType: zcodeControlPlanApproval, NoSpan: true}); err != nil {
			slog.Error("persist plan control source", "agent_id", a.AgentID(), "request_id", req.RequestID, "error", err)
			a.replyZCodeControlFailure(id, "LeapMux could not store the plan.")
			return
		}
	}

	planText := req.planText()
	toolName := zcodeControlAskUserQuestion
	if req.isMCPElicitation() {
		toolName = zcodeControlMCPElicitation
	}
	if req.isPlanApproval() {
		toolName = zcodeControlPlanApproval
		if strings.TrimSpace(planText) != "" {
			// A context-clear approval reads this canonical copy after it replaces the native session.
			a.sink.UpdatePlan([]byte(planText), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, providerkit.ExtractPlanTitle(planText))
		}
	}

	// The questions are re-encoded in the shape the SHARED AskUserQuestion control
	// reads -- {question, header, multiSelect, options:[{value,label}]} -- so the
	// answer it builds is keyed by question text, which is exactly what the
	// app-server's `content.answers` map expects. `plan` carries the plan text of a plan
	// approval, which the synthesized question cannot hold (see planText).
	input, err := json.Marshal(map[string]any{
		"questions": zcodeSharedQuestions(req),
		"prompt":    req.Prompt,
		"plan":      planText,
	})
	if err != nil {
		slog.Error("zcode marshal user input questions", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the user-input request")
		return
	}

	payload, err := json.Marshal(zcodeControlRequestPayload{
		Type:      "control_request",
		RequestID: req.RequestID,
		WireID:    id,
		Method:    contracts.ZCodeMethodRequestUserInput,
		Request: zcodeControlRequestHeader{
			ToolName:  toolName,
			ToolUseID: req.ToolCallID,
			Input:     input,
		},
		Params: params,
	})
	if err != nil {
		slog.Error("zcode marshal user input control request", "agent_id", a.AgentID(), "error", err)
		a.replyZCodeControlFailure(id, "leapmux could not store the user-input request")
		return
	}

	a.rememberZCodeControlRequest(req.RequestID, payload)
	a.publishZCodeControlRequest(req.RequestID)
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
			"question":    contracts.ZCodePlanControlQuestion,
			"header":      "Plan",
			"multiSelect": false,
			"options": []map[string]any{
				{"value": contracts.ZCodePlanControlApprove, "label": contracts.ZCodePlanControlApprove},
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
func (a *Agent) replyZCodeControlFailure(id json.RawMessage, message string) {
	if err := a.sendZCodeErrorReply(id, ErrInternal, message); err != nil {
		slog.Warn("zcode control failure reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// replyZCodePermission answers a permission request directly, for the paths where
// no user decision is possible. No reader picked an option, so no choice rides
// the answer and the decision word alone selects the reply.
func (a *Agent) replyZCodePermission(id json.RawMessage, options []zcodePermissionOption, behavior, reason string) {
	result, err := zcodePermissionResult(options, behavior, reason, "")
	if err != nil {
		a.replyZCodeControlFailure(id, err.Error())
		return
	}
	if err := a.sendZCodeReply(id, result); err != nil {
		slog.Warn("zcode permission reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// replyZCodeUserInput answers a user-input request directly.
//
// The reply carries a behavior and nothing else. ZCode's user-input result has no
// field for a reason, unlike the permission result, so the caller logs its reason
// rather than passing one here.
func (a *Agent) replyZCodeUserInput(id json.RawMessage, behavior string, reply zcodeUserInputReply) {
	if err := a.sendZCodeReply(id, zcodeUserInputResult(behavior, reply)); err != nil {
		slog.Warn("zcode user input reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// answerProviderRuntimeHeaders answers the app-server's request for freshly-minted
// provider credentials.
//
// Legacy builds already hold the inline key from workspace/updateProviderRegistry,
// so their reply reports authorization only. ZCode 0.16.9 keeps account credentials
// in the host and requires the key in `requestAuth` for each model request, which
// accountRequestAuth resolves.
func (a *Agent) answerProviderRuntimeHeaders(id, params json.RawMessage) {
	a.Mu.Lock()
	accountConfig := a.account != nil
	a.Mu.Unlock()
	var req zcodeRuntimeHeadersRequest
	if err := json.Unmarshal(params, &req); err != nil {
		// Do NOT fall through. An undecodable request leaves ProviderID empty, and
		// hasInlineAPIKey reads an empty id as "any provider" -- so LeapMux would answer
		// `authorized: true` for a provider it holds no credential for, and the turn would
		// fail later with an authentication error from the model provider instead of here.
		slog.Warn("zcode provider runtime headers unmarshal failed", "agent_id", a.AgentID(), "error", err)
		var result any = map[string]any{
			"headers":    map[string]string{},
			"authorized": false,
			"error":      "leapmux could not read the provider runtime headers request",
		}
		if accountConfig {
			result = map[string]any{
				"headersApplied": false,
				"errorMessage":   "leapmux could not read the provider runtime headers request",
			}
		}
		if err := a.sendZCodeReply(id, result); err != nil {
			slog.Warn("zcode provider runtime headers reply failed", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	if accountConfig {
		// The caller sits on the event goroutine, and a coding plan's credential can
		// take a network call, so the answer goes on a goroutine of its own: the
		// reader must keep reading frames while the call runs.
		go a.answerAccountRuntimeHeaders(id, req)
		return
	}
	a.Mu.Lock()
	authorized := a.catalog.hasInlineAPIKey(req.ProviderID)
	a.Mu.Unlock()
	result := map[string]any{
		"headers":    map[string]string{},
		"authorized": authorized,
	}
	if !authorized {
		result["error"] = fmt.Sprintf("leapmux holds no credential for provider %q; add it to ZCode's configuration", req.ProviderID)
	}
	if err := a.sendZCodeReply(id, result); err != nil {
		slog.Warn("zcode provider runtime headers reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// answerAccountRuntimeHeaders answers a runtime headers request of an account
// build with the credential that accountRequestAuth resolves, or with the reason
// that it has none.
func (a *Agent) answerAccountRuntimeHeaders(id json.RawMessage, req zcodeRuntimeHeadersRequest) {
	ctx, cancel := context.WithTimeout(a.Context(), zcodeProjectTokenTimeout)
	auth, err := a.accountRequestAuth(ctx, req)
	cancel()
	var result any = map[string]any{"headersApplied": true, "requestAuth": auth}
	if err != nil {
		slog.Warn("zcode provider credential unavailable", "agent_id", a.AgentID(), "provider", req.ProviderID, "error", err)
		result = map[string]any{"headersApplied": false, "errorMessage": err.Error()}
	}
	if err := a.sendZCodeReply(id, result); err != nil {
		slog.Warn("zcode provider runtime headers reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// answerOfficialMcpAuthHeaders answers the request for credentials to ZCode's
// hosted MCP servers, which LeapMux does not hold.
//
// It is answered rather than ignored: the app-server blocks the tool behind it, and
// a declared unavailability lets it fall through to the servers it can reach.
func (a *Agent) answerOfficialMcpAuthHeaders(id json.RawMessage) {
	a.Mu.Lock()
	accountConfig := a.account != nil
	a.Mu.Unlock()
	if accountConfig {
		if err := a.sendZCodeReply(id, map[string]any{
			"ok":     false,
			"reason": OfficialAuthUnavailable,
		}); err != nil {
			slog.Warn("zcode official mcp auth reply failed", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	if err := a.sendZCodeReply(id, map[string]any{
		"status":  OfficialAuthUnavailable,
		"headers": map[string]string{},
	}); err != nil {
		slog.Warn("zcode official mcp auth reply failed", "agent_id", a.AgentID(), "error", err)
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
// zcodePermissionResult builds the app-server's reply from the reader's answer.
//
// The CHOICE names the option the reader picked in the pill group, and that
// option's embedded response is the exact native reply -- an "always allow"
// option carries permissionUpdates a bare decision would drop. The choice must
// name an OFFERED option: an id no option states changes nothing, so a stale
// pill cannot invent a grant. Without a choice, the decision word picks the
// first option whose response carries it.
func zcodePermissionResult(options []zcodePermissionOption, behavior, reason, choice string) (json.RawMessage, error) {
	want := contracts.ZCodeDecisionAllow
	if behavior != agent.ControlBehaviorAllow {
		want = contracts.ZCodeDecisionDeny
	}
	if choice != "" {
		for i := range options {
			if options[i].OptionID != choice {
				continue
			}
			var body struct {
				Decision string `json:"decision"`
			}
			if len(options[i].Response) == 0 || json.Unmarshal(options[i].Response, &body) != nil {
				break
			}
			if body.Decision != want {
				break
			}
			return zcodeOptionResponse(options[i], want, reason)
		}
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
// Rejection always uses decline. The native mapper discards rejection reasons.
// Plan answer text cannot carry rejection safely because the approval sentinel would approve the plan.
func zcodeUserInputResult(behavior string, reply zcodeUserInputReply) map[string]any {
	if behavior != agent.ControlBehaviorAllow {
		return map[string]any{contracts.ZCodeReplyFieldAction: contracts.ZCodeActionDecline}
	}

	if reply.PlanApproval {
		// The sentinel is the ONLY value the plan reader accepts as approval; any other
		// non-empty answer is read back as reviewer feedback, which is a denial.
		return map[string]any{
			contracts.ZCodeReplyFieldAction: contracts.ZCodeActionAccept,
			contracts.ZCodeReplyFieldContent: map[string]any{
				contracts.ZCodeAnswerFieldSingle: contracts.ZCodePlanControlApprove,
			},
		}
	}
	return map[string]any{
		contracts.ZCodeReplyFieldAction:  contracts.ZCodeActionAccept,
		contracts.ZCodeReplyFieldContent: zcodeAnswerContent(reply),
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
		content[fmt.Sprintf("%s%d", contracts.ZCodeAnswerFieldIndexedPrefix, i)] = answer
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
		content[contracts.ZCodeAnswerFieldMap] = keyed
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
func (a *Agent) pendingZCodeControlPayload(requestID string) json.RawMessage {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if record := a.pendingControls[requestID]; record != nil {
		return record.payload
	}
	return nil
}

// rememberZCodeControlRequest keeps the first wire identity until native settlement.
func (a *Agent) rememberZCodeControlRequest(requestID string, payload json.RawMessage) {
	record := newPendingControl(payload)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.pendingControls == nil {
		a.pendingControls = make(map[string]*pendingControl)
	}
	if a.pendingControls[requestID] != nil {
		return
	}
	record.ownerSessionID = a.sessionID
	if record.sessionID == "" {
		record.sessionID = a.sessionID
	}
	a.pendingControls[requestID] = record
}

// publishZCodeControlRequest reuses the first payload and its publication instance.
func (a *Agent) publishZCodeControlRequest(requestID string) {
	a.Mu.Lock()
	record := a.pendingControls[requestID]
	if record == nil || record.answering {
		a.Mu.Unlock()
		return
	}
	bypass := a.canBypassControlLocked(record)
	if bypass {
		record.ready = true
		record.publicationOnce.Do(func() { close(record.publicationDone) })
	}
	a.Mu.Unlock()
	if bypass {
		a.queuePermissionApprovals()
		return
	}
	a.publishRegisteredControl(requestID, record)
}

func (a *Agent) publishRegisteredControl(requestID string, record *pendingControl) {
	err := a.sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: record.ownerSessionID, RequestID: requestID, InstanceID: record.instanceID, Payload: record.payload})
	a.Mu.Lock()
	current := a.pendingControls[requestID] == record
	if err == nil && !record.answered {
		record.published = true
	}
	record.ready = true
	record.publicationOnce.Do(func() { close(record.publicationDone) })
	refuse := current && err != nil && !record.answering
	cancel := err == nil && (!current || record.answered) || refuse && record.published
	if refuse {
		delete(a.pendingControls, requestID)
	}
	bypass := current && err == nil && a.canBypassControlLocked(record)
	a.Mu.Unlock()
	if cancel {
		a.sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
	if bypass {
		a.queuePermissionApprovals()
	}
	if refuse {
		slog.Error("ZCode could not publish a control request", "request_id", requestID, "error", err)
		if len(record.wireID) > 0 {
			a.replyZCodeControlFailure(record.wireID, providerkit.ControlPublicationFailure)
		}
	}
}

// forgetZCodeControlRequest consumes native settlement and retires an undecided or automatic card.
func (a *Agent) forgetZCodeControlRequest(requestID string) bool {
	a.Mu.Lock()
	record := a.pendingControls[requestID]
	if record == nil {
		a.Mu.Unlock()
		return false
	}
	delete(a.pendingControls, requestID)
	record.publicationOnce.Do(func() { close(record.publicationDone) })
	cancel := record.published && (!record.answering || record.automatic)
	a.Mu.Unlock()
	if cancel {
		a.sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
	return true
}

// zcodeReplyForAnswer builds the `result` of the reply for one answered request.
func zcodeReplyForAnswer(stored zcodeControlRequestPayload, behavior, message string, responseContent []byte) (any, error) {
	switch stored.Method {
	case contracts.ZCodeMethodRequestPermission:
		var req zcodePermissionRequest
		if len(stored.Params) > 0 {
			if err := json.Unmarshal(stored.Params, &req); err != nil {
				return nil, fmt.Errorf("read stored permission request: %w", err)
			}
		}
		result, err := zcodePermissionResult(req.Options, behavior, message, agent.DecodeControlChoice(responseContent))
		if err != nil {
			return nil, err
		}
		return result, nil
	case contracts.ZCodeMethodRequestUserInput:
		return zcodeUserInputResult(behavior, zcodeUserInputReply{
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

// --- MCP input forms ---

// zcodeResolveMCPElicitation answers an MCP server's input form with the browser's
// MCP form answer. ZCode states the form as a user-input request, so the reply is
// the user-input reply that ZCode turns back into the form's content.
func zcodeResolveMCPElicitation(stored zcodeControlRequestPayload, responseContent []byte, res agent.ControlResponseResolution) agent.ControlResponseResolution {
	var response providerkit.MCPElicitationControlResponse
	if json.Unmarshal(responseContent, &response) != nil {
		res.Refuse(agent.RefusalUnreadableAnswer)
		return res
	}
	if response.Response.RequestID != stored.RequestID {
		res.Refuse(agent.RefusalOtherRequest)
		return res
	}
	if len(stored.WireID) == 0 {
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	answer := response.Response.Response
	if len(answer.Meta) > 0 {
		// ZCode's form offers no choice that an answer persists.
		res.Refuse("the answer persists a choice the request did not offer")
		return res
	}
	reply, err := zcodeMCPElicitationResult(stored.Params, answer.Action, answer.Content)
	if err != nil {
		slog.Warn("zcode build MCP form reply failed", "request_id", stored.RequestID, "error", err)
		res.Refuse(agent.RefusalUnreadableAnswer)
		return res
	}
	encoded, err := json.Marshal(zcodeReplyFrame{ID: stored.WireID, Result: reply})
	if err != nil {
		res.Refuse(agent.RefusalUnencodableReply)
		return res
	}
	res.Content = encoded
	return res
}

// zcodeMCPElicitationResult builds the user-input reply to an MCP form from the
// answer's action and content.
//
// ZCode reads an accepted form positionally: `answer_<i>` is the text of the i-th
// field that it carries (zcodeElicitationFields), and it types the text from the
// field (a boolean is "true", a number is parsed). So each value goes out as that
// text, and a value that ZCode cannot carry (null, an array, an object) is left
// out. Decline and cancel carry no content.
func zcodeMCPElicitationResult(params json.RawMessage, action string, content json.RawMessage) (map[string]any, error) {
	switch action {
	case contracts.MCPElicitationActionDecline:
		return map[string]any{contracts.ZCodeReplyFieldAction: contracts.ZCodeActionDecline}, nil
	case contracts.MCPElicitationActionCancel:
		return map[string]any{contracts.ZCodeReplyFieldAction: contracts.ZCodeActionCancel}, nil
	case contracts.MCPElicitationActionAccept:
	default:
		return nil, fmt.Errorf("the MCP form answer states the action %q", action)
	}
	values := map[string]json.RawMessage{}
	if len(content) > 0 && string(content) != "null" {
		if err := json.Unmarshal(content, &values); err != nil {
			return nil, fmt.Errorf("read the MCP form content: %w", err)
		}
	}
	var request struct {
		Schema struct {
			RequestedSchema json.RawMessage `json:"requestedSchema"`
		} `json:"schema"`
		Input struct {
			RequestedSchema json.RawMessage `json:"requestedSchema"`
		} `json:"input"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, fmt.Errorf("read the stored MCP form request: %w", err)
	}
	schema := request.Schema.RequestedSchema
	if len(schema) == 0 {
		schema = request.Input.RequestedSchema
	}
	answers := map[string]any{}
	for i, field := range zcodeElicitationFields(schema) {
		if text, ok := zcodeElicitationAnswerText(values[field]); ok {
			answers[fmt.Sprintf("%s%d", contracts.ZCodeAnswerFieldIndexedPrefix, i)] = text
		}
	}
	return map[string]any{
		contracts.ZCodeReplyFieldAction:  contracts.ZCodeActionAccept,
		contracts.ZCodeReplyFieldContent: answers,
	}, nil
}

// zcodeElicitationFields lists the fields of a form schema that ZCode carries, in
// the order of `properties`: a property with an `enum` array, or of the type
// number, integer, boolean or string. ZCode skips every other property, and its
// positional answers count only these fields (readMcpElicitationFields in
// zcode.cjs 3.16.3). A property that the object states twice keeps its first
// place and its last definition, as a JavaScript object does.
func zcodeElicitationFields(schema json.RawMessage) []string {
	var root struct {
		Properties json.RawMessage `json:"properties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || len(root.Properties) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(root.Properties))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	var order []string
	carried := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		name, _ := token.(string)
		var property json.RawMessage
		if err := decoder.Decode(&property); err != nil {
			return nil
		}
		if _, seen := carried[name]; !seen {
			order = append(order, name)
		}
		carried[name] = zcodeElicitationCarries(property)
	}
	var fields []string
	for _, name := range order {
		if carried[name] {
			fields = append(fields, name)
		}
	}
	return fields
}

// zcodeElicitationCarries reports whether ZCode carries a value for one property
// of a form schema.
func zcodeElicitationCarries(property json.RawMessage) bool {
	var definition map[string]json.RawMessage
	if json.Unmarshal(property, &definition) != nil || definition == nil {
		return false
	}
	if enum := bytes.TrimSpace(definition["enum"]); len(enum) > 0 && enum[0] == '[' {
		return true
	}
	var kind string
	if json.Unmarshal(definition["type"], &kind) != nil {
		return false
	}
	switch kind {
	case "number", "integer", "boolean", "string":
		return true
	default:
		return false
	}
}

// zcodeElicitationAnswerText is the text that ZCode reads back as the value: a
// string as it is, a number as its JSON text, and a boolean as "true" or
// "false". Any other value cannot pass through ZCode's text answer.
func zcodeElicitationAnswerText(value json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if len(bytes.TrimSpace(value)) == 0 || decoder.Decode(&decoded) != nil {
		return "", false
	}
	switch typed := decoded.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}
