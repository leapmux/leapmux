package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ControlResponseContext is the pure provider input for interpreting a frontend
// control response. The service loads the stored control request once, extracts the
// shared metadata, and passes both payloads here; providers must not perform I/O.
type ControlResponseContext struct {
	RequestID       string
	RequestPayload  json.RawMessage
	ResponseContent []byte
	ToolName        string
	ToolUseID       string
}

// ControlResponseResolution is the provider-owned interpretation of a control
// response. The service executes side effects from this plan: it persists display
// rows, deletes control requests, mutates plan-mode settings, and forwards Content.
type ControlResponseResolution struct {
	Content         []byte
	DisplayText     string
	SelfDisplayed   bool
	PlanModeControl PlanModeControlKind
}

func defaultControlResponseResolution(ctx ControlResponseContext) ControlResponseResolution {
	return ControlResponseResolution{Content: ctx.ResponseContent}
}

func (noopProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	return defaultControlResponseResolution(ctx)
}

func (p codexProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)

	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "codex control response request") {
		return res
	}
	if req.Method == "item/tool/requestUserInput" {
		res.DisplayText = codexUserInputAnswersText(ctx.RequestPayload, ctx.ResponseContent)
		return res
	}
	if text := codexFeedbackMessageText(ctx.ResponseContent); text != "" {
		res.DisplayText = text
		return res
	}
	res.DisplayText = codexDecisionDisplayText(ctx.ResponseContent)
	return res
}

func (p claudeProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	res.SelfDisplayed = p.IsSelfDisplayingControlTool(ctx.ToolName)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	return res
}

func (p piProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "pi control response request") {
		return res
	}
	res.DisplayText = piExtensionUIResponseDisplayText(req.Method, ctx.ResponseContent)
	return res
}

func (p acpProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}

	var req struct {
		Method string `json:"method"`
		Type   string `json:"type"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "acp control response request") {
		return res
	}

	// Cursor's question / create-plan flows are keyed on the JSON-RPC method and are unique to
	// Cursor's ACP variant, so they stay Cursor-specific (a per-provider enum check) rather than a
	// registration hook shared across providers.
	if p.provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR {
		switch req.Method {
		case CursorMethodAskQuestion:
			res.DisplayText = cursorQuestionAnswersText(ctx.RequestPayload, ctx.ResponseContent)
			return res
		case CursorMethodCreatePlan:
			if transformed, text, ok := transformCursorControlResponse(ctx.RequestPayload, ctx.ResponseContent); ok {
				res.Content = transformed
				res.DisplayText = text
				return res
			}
		}
	}

	// The OpenCode-protocol `question.asked` answer summary dispatches through a registration-time
	// hook (set only for the providers that speak that protocol, see init()) rather than a provider
	// enum allowlist -- so the membership lives at the single registration site, mirroring the
	// frontend's registerOpenCodeProtocolProvider, not a second source of truth that drifts.
	if p.questionAnswersText != nil && req.Type == "question.asked" {
		res.DisplayText = p.questionAnswersText(ctx.RequestPayload, ctx.ResponseContent)
		return res
	}

	res.DisplayText = acpPermissionResponseDisplayText(ctx.RequestPayload, ctx.ResponseContent)
	return res
}

// warnUnmarshal decodes JSON into v, logging a "<label> unmarshal failed" warning and returning
// false on error (the caller then returns its own zero result). The single home for the
// decode-and-warn preamble the control-response resolvers and answer summaries all repeat.
func warnUnmarshal(data []byte, v any, label string) bool {
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn(label+" unmarshal failed", "error", err)
		return false
	}
	return true
}

// firstNonEmpty returns the first argument that is non-empty after trimming surrounding
// whitespace, or "" when all are blank. Centralizes the "prefer this label, else fall back to
// that one" idiom the answer summaries repeat -- and, because it trims the fallback too, a
// display label chosen from a fallback never renders with stray whitespace.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// labeledAnswerLine formats one "label: v1, v2" answer line, trimming each value and dropping the
// empties; ok is false when no value survives (the caller skips the line). The single home for the
// per-line answer formatting the codex / opencode / cursor answer summaries all repeat, so a change
// to the separator or the empty-value rule happens once.
func labeledAnswerLine(label string, values []string) (string, bool) {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s: %s", label, strings.Join(parts, ", ")), true
}

func codexUserInputAnswersText(requestPayload, responseContent []byte) string {
	var req struct {
		Params struct {
			Questions []struct {
				ID     string `json:"id"`
				Header string `json:"header"`
			} `json:"questions"`
		} `json:"params"`
	}
	var resp struct {
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if !warnUnmarshal(requestPayload, &req, "codex user input request") {
		return ""
	}
	if !warnUnmarshal(responseContent, &resp, "codex user input response") {
		return ""
	}

	if len(resp.Result.Answers) == 0 {
		return ""
	}

	labels := make(map[string]string, len(req.Params.Questions))
	order := make([]string, 0, len(req.Params.Questions))
	for _, q := range req.Params.Questions {
		key := firstNonEmpty(q.ID, q.Header)
		if key == "" {
			continue
		}
		labels[key] = firstNonEmpty(q.Header, key)
		order = append(order, key)
	}

	lines := make([]string, 0, len(resp.Result.Answers))
	seen := make(map[string]bool, len(resp.Result.Answers))
	appendLine := func(key string) {
		answer, ok := resp.Result.Answers[key]
		if !ok || seen[key] {
			return
		}
		// seen is set ONLY when a non-empty line is emitted, so the empty-filter and the dedup
		// stay entangled -- an all-empty answer neither renders nor marks the key seen.
		if line, ok := labeledAnswerLine(labels[key], answer.Answers); ok {
			lines = append(lines, line)
			seen[key] = true
		}
	}

	for _, key := range order {
		appendLine(key)
	}
	// Any answer whose key wasn't in the request's questions is absent from `order`. Append
	// these in a STABLE (sorted) order rather than Go's randomized map-iteration order, so the
	// same control response always renders the same lines (a map range would reorder them
	// nondeterministically between renders).
	extraKeys := make([]string, 0, len(resp.Result.Answers))
	for key := range resp.Result.Answers {
		if !seen[key] {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		if _, ok := labels[key]; !ok {
			labels[key] = key
		}
		appendLine(key)
	}

	return strings.Join(lines, "\n")
}

// ControlBehaviorEnvelope is the frontend's neutral approve/reject control-response envelope --
// {response:{request_id, response:{behavior, message}}}. The SINGLE Go home for this wire shape:
// DecodeControlBehavior decodes it directly, and the service's plan-mode payload EMBEDS it (adding
// the permissionMode/clearContext siblings) rather than re-declaring the same nested struct, so a
// wire-field rename lands in exactly one place instead of drifting between two hand-copied shapes.
type ControlBehaviorEnvelope struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		} `json:"response"`
	} `json:"response"`
}

// DecodeControlBehavior decodes the frontend's neutral approve/reject control-response envelope
// (ControlBehaviorEnvelope), returning the trimmed request id, behavior, and rejection
// message. ok is false only when the bytes don't parse as JSON. The message is the user's typed
// rejection reason, with the ControlRejectedByUserMessage sentinel (an auto-filled placeholder,
// not a real reason) collapsed to "". The SINGLE home for the sentinel rule, shared by the Codex
// feedback path, the Cursor create-plan transform, and the service's request-id lookup, so a
// sentinel change lands in exactly one place.
func DecodeControlBehavior(content []byte) (requestID, behavior, message string, ok bool) {
	var cr ControlBehaviorEnvelope
	if err := json.Unmarshal(content, &cr); err != nil {
		return "", "", "", false
	}
	requestID = strings.TrimSpace(cr.Response.RequestID)
	behavior = strings.TrimSpace(cr.Response.Response.Behavior)
	message = NormalizeRejectionMessage(cr.Response.Response.Message)
	return requestID, behavior, message, true
}

// NormalizeRejectionMessage trims a control-response reject reason and collapses the
// ControlRejectedByUserMessage placeholder (the auto-filled "declined without a reason" text) to
// "". The SINGLE home for the deny-feedback rule, shared by DecodeControlBehavior (which decodes
// the raw wire bytes) and the service's controlResponsePlan.rejectionMessage accessor (which reads
// the already-decoded plan), so a sentinel-value change lands in exactly one place instead of two.
func NormalizeRejectionMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == ControlRejectedByUserMessage {
		return ""
	}
	return message
}

func codexFeedbackMessageText(responseContent []byte) string {
	_, _, message, _ := DecodeControlBehavior(responseContent)
	return message
}

func codexDecisionDisplayText(responseContent []byte) string {
	var resp struct {
		Result struct {
			Decision json.RawMessage `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseContent, &resp); err != nil || len(resp.Result.Decision) == 0 || string(resp.Result.Decision) == "null" {
		return ""
	}

	var decision string
	if err := json.Unmarshal(resp.Result.Decision, &decision); err == nil {
		switch strings.TrimSpace(decision) {
		case "accept":
			return "Allow"
		case "acceptForSession":
			return "Allow for Session"
		case "decline":
			return "Reject"
		case "cancel":
			return "Cancel"
		default:
			return strings.TrimSpace(decision)
		}
	}

	var objectDecision map[string]json.RawMessage
	if err := json.Unmarshal(resp.Result.Decision, &objectDecision); err != nil {
		return ""
	}
	if _, ok := objectDecision["acceptWithExecpolicyAmendment"]; ok {
		return "Allow & Remember"
	}
	if _, ok := objectDecision["applyNetworkPolicyAmendment"]; ok {
		return "Apply Network Policy"
	}
	if len(objectDecision) > 0 {
		return "Allow"
	}
	return ""
}

func opencodeQuestionAnswersText(requestPayload, responseContent []byte) string {
	var req struct {
		Properties struct {
			Questions []struct {
				Header   string `json:"header"`
				Question string `json:"question"`
			} `json:"questions"`
		} `json:"properties"`
	}
	var resp struct {
		Result struct {
			Rejected bool       `json:"rejected"`
			Answers  [][]string `json:"answers"`
		} `json:"result"`
	}
	if !warnUnmarshal(requestPayload, &req, "opencode question request") {
		return ""
	}
	if !warnUnmarshal(responseContent, &resp, "opencode question response") {
		return ""
	}
	if resp.Result.Rejected {
		return "Reject"
	}

	lines := make([]string, 0, len(resp.Result.Answers))
	for i, answers := range resp.Result.Answers {
		label := fmt.Sprintf("Question %d", i+1)
		if i < len(req.Properties.Questions) {
			q := req.Properties.Questions[i]
			if v := firstNonEmpty(q.Header, q.Question); v != "" {
				label = v
			}
		}
		if line, ok := labeledAnswerLine(label, answers); ok {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func cursorQuestionAnswersText(requestPayload, responseContent []byte) string {
	var req struct {
		Params struct {
			Questions []struct {
				ID      string `json:"id"`
				Prompt  string `json:"prompt"`
				Options []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		} `json:"params"`
	}
	var resp struct {
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
				Answers []struct {
					QuestionID        string   `json:"questionId"`
					SelectedOptionIDs []string `json:"selectedOptionIds"`
				} `json:"answers"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if !warnUnmarshal(requestPayload, &req, "cursor question request") {
		return ""
	}
	if !warnUnmarshal(responseContent, &resp, "cursor question response") {
		return ""
	}

	switch resp.Result.Outcome.Outcome {
	case "answered":
		labels := make(map[string]string, len(req.Params.Questions))
		optionLabels := make(map[string]map[string]string, len(req.Params.Questions))
		order := make([]string, 0, len(req.Params.Questions))
		for _, q := range req.Params.Questions {
			if strings.TrimSpace(q.ID) == "" {
				continue
			}
			labels[q.ID] = strings.TrimSpace(q.Prompt)
			options := make(map[string]string, len(q.Options))
			for _, option := range q.Options {
				if strings.TrimSpace(option.ID) == "" {
					continue
				}
				options[option.ID] = firstNonEmpty(option.Label, option.ID)
			}
			optionLabels[q.ID] = options
			order = append(order, q.ID)
		}

		answerByQuestion := make(map[string][]string, len(resp.Result.Outcome.Answers))
		for _, answer := range resp.Result.Outcome.Answers {
			if strings.TrimSpace(answer.QuestionID) == "" {
				continue
			}
			mapped := make([]string, 0, len(answer.SelectedOptionIDs))
			for _, optionID := range answer.SelectedOptionIDs {
				optionID = strings.TrimSpace(optionID)
				if optionID == "" {
					continue
				}
				label := optionID
				if options := optionLabels[answer.QuestionID]; options != nil {
					if mappedLabel := strings.TrimSpace(options[optionID]); mappedLabel != "" {
						label = mappedLabel
					}
				}
				mapped = append(mapped, label)
			}
			if len(mapped) > 0 {
				answerByQuestion[answer.QuestionID] = mapped
			}
		}

		lines := make([]string, 0, len(answerByQuestion))
		for _, questionID := range order {
			mapped := answerByQuestion[questionID]
			if len(mapped) == 0 {
				continue
			}
			label := firstNonEmpty(labels[questionID], questionID)
			if line, ok := labeledAnswerLine(label, mapped); ok {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	case "cancelled", "skipped":
		return strings.TrimSpace(resp.Result.Outcome.Reason)
	default:
		return ""
	}
}

func cursorCreatePlanDisplayText(outcome, reason string) string {
	switch outcome {
	case "accepted":
		return "Accept"
	case "rejected":
		if reason := strings.TrimSpace(reason); reason != "" {
			return reason
		}
		return "Reject"
	case "cancelled":
		if reason := strings.TrimSpace(reason); reason != "" {
			return reason
		}
		return "Cancel"
	default:
		return ""
	}
}

func transformCursorControlResponse(requestPayload, responseContent []byte) ([]byte, string, bool) {
	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(requestPayload, &req, "cursor control response method") {
		return nil, "", false
	}
	if req.Method != CursorMethodCreatePlan {
		return nil, "", false
	}

	respRequestID, behavior, message, ok := DecodeControlBehavior(responseContent)
	if !ok {
		return nil, "", false
	}

	idRaw, requestID, ok := ExtractJSONRPCID(requestPayload)
	if !ok {
		return nil, "", false
	}

	if respRequestID == "" || respRequestID != requestID {
		return nil, "", false
	}

	outcome := "accepted"
	reason := ""
	switch behavior {
	case ControlBehaviorAllow:
	case ControlBehaviorDeny:
		outcome = "rejected"
		// message is already trimmed and the ControlRejectedByUserMessage placeholder collapsed
		// to "" by DecodeControlBehavior, so a bare rejection carries no reason.
		reason = message
	default:
		return nil, "", false
	}

	outcomeBody := map[string]interface{}{
		"outcome": outcome,
	}
	if reason != "" {
		outcomeBody["reason"] = reason
	}

	encoded, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idRaw),
		"result":  map[string]interface{}{"outcome": outcomeBody},
	})
	if err != nil {
		return nil, "", false
	}
	return encoded, cursorCreatePlanDisplayText(outcome, reason), true
}

// piExtensionUIResponseDisplayText extracts a human-readable summary of a Pi
// extension_ui_response so the user's choice shows up as a synthetic message
// in the transcript.
func piExtensionUIResponseDisplayText(method string, responseContent []byte) string {
	var resp struct {
		Cancelled bool   `json:"cancelled"`
		Confirmed *bool  `json:"confirmed"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal(responseContent, &resp); err != nil {
		return ""
	}

	if resp.Cancelled {
		return "Cancelled"
	}

	switch method {
	case PiDialogMethodConfirm:
		if resp.Confirmed != nil && *resp.Confirmed {
			return "Approve"
		}
		return "Deny"
	case PiDialogMethodSelect, PiDialogMethodInput, PiDialogMethodEditor:
		return strings.TrimSpace(resp.Value)
	default:
		return ""
	}
}

// acpPermissionResponseDisplayText extracts a human-readable display text from
// an ACP permission response by matching the selected optionId against the
// original request payload's option list.
func acpPermissionResponseDisplayText(requestPayload, responseContent []byte) string {
	var req struct {
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Name     string `json:"name"`
			} `json:"options"`
		} `json:"params"`
	}
	var resp struct {
		Result struct {
			Outcome struct {
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if !warnUnmarshal(requestPayload, &req, "acp permission request") {
		return ""
	}
	if !warnUnmarshal(responseContent, &resp, "acp permission response") {
		return ""
	}

	optionID := strings.TrimSpace(resp.Result.Outcome.OptionID)
	if optionID == "" {
		return ""
	}
	for _, option := range req.Params.Options {
		if strings.TrimSpace(option.OptionID) == optionID {
			if name := strings.TrimSpace(option.Name); name != "" {
				return name
			}
			break
		}
	}

	switch optionID {
	case "once", "proceed_once":
		return "Allow once"
	case "always", "proceed_always":
		return "Always allow"
	case "reject", "cancel":
		return "Reject"
	default:
		return optionID
	}
}
