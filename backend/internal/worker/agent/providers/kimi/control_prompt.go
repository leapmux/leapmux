package kimi

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's control requests as neutral prompts, and its answers.
//
// The server announces each request in an `interaction` frame, which the
// Worker stores unchanged. A question has its own interaction kind. An
// approval's `tool_input_display.kind` decides which prompt reads it:
//
//   - plan_review of the main agent is the plan approval, with the approaches
//     that the plan offers.
//   - plan_review of a subagent is a permission with the same answers,
//     because the plan approval acts on the MAIN agent: it leaves the main
//     agent's plan mode, can clear the main context, and switches the main
//     permission mode.
//   - goal_start is a permission whose options start the goal in a
//     permission mode.
//   - Any other display is a permission: approve once, approve for the
//     session, or reject.
//
// The answers ride the neutral envelope with the Kimi fields beside the
// behavior. ResolveControlResponse (control_response.go) turns them into the
// server's own body.

// The IDs of the permission options that LeapMux offers for Kimi Code. They
// are LeapMux's own: the answer carries a behavior, a scope or a label, and
// never the ID.
const (
	kimiControlOptionApprove           = "approve"
	kimiControlOptionApproveForSession = "approve_for_session"
	kimiControlOptionReject            = "reject"
	// kimiControlOptionGoalModePrefix starts an option that starts a goal in
	// one permission mode.
	kimiControlOptionGoalModePrefix = "goal_mode:"
	// kimiControlOptionPlanApprovePrefix starts an option that approves a
	// subagent's plan with one of the plan's own approaches.
	kimiControlOptionPlanApprovePrefix = "plan_approve:"
	// kimiControlOptionPlanRejectPrefix starts an option that refuses a
	// subagent's plan with a plan label: Revise, or Reject and Exit.
	kimiControlOptionPlanRejectPrefix = "plan_reject:"
)

// kimiObjects reads an array field and keeps its object elements. A value
// that is no array reads as none.
func kimiObjects(fields map[string]json.RawMessage, key string) []map[string]json.RawMessage {
	var elements []json.RawMessage
	if json.Unmarshal(fields[key], &elements) != nil {
		return nil
	}
	objects := make([]map[string]json.RawMessage, 0, len(elements))
	for _, element := range elements {
		if object := providerkit.RawFields(element); object != nil {
			objects = append(objects, object)
		}
	}
	return objects
}

// kimiControlFrame reads the nested request of a pending stored interaction
// frame of one interaction kind, and the frame itself. It returns false for
// any other payload, for a non-pending status, and for a frame whose request
// is no object.
func kimiControlFrame(payload []byte, kind string) (frame, request map[string]json.RawMessage, ok bool) {
	frame = providerkit.RawFields(payload)
	if providerkit.ObjectString(frame, "type") != contracts.KimiFrameInteraction ||
		providerkit.ObjectString(frame, "kind") != kind ||
		providerkit.ObjectString(frame, "status") != kimiInteractionPending {
		return nil, nil, false
	}
	request = providerkit.RawFields(frame["request"])
	if request == nil {
		return nil, nil, false
	}
	return frame, request, true
}

// ReadControlRequest reads Kimi Code's control requests: a question, LeapMux's
// workspace-trust question, and an approval. Any other payload reads as
// unrecognized: the server raises no other request, and the resolution
// refuses an answer to one.
func (kimiProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if _, request, ok := kimiControlFrame(input.Payload, contracts.KimiInteractionKindQuestion); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: kimiQuestions(request),
		}}}
	}
	if prompt, ok := providerkit.ReadWorkspaceTrustPrompt(input.Payload, leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE); ok {
		return prompt
	}
	frame, request, ok := kimiControlFrame(input.Payload, contracts.KimiInteractionKindApproval)
	if !ok {
		return agent.UnrecognizedControlPrompt()
	}
	return kimiApprovalPrompt(frame, request)
}

// kimiQuestions reads the questions of a question request, in its order.
//
// Each question and each option carries an ID of the server's own (`q_0`,
// `opt_0_1`), and an answer states those IDs, so an option's value is its ID.
// A question with no ID or no text, and an option with no ID or no label,
// cannot be answered, so each is dropped. The header stands in for absent
// question text. Every question may be skipped: the server takes a `skipped`
// answer for any of them, so each permits an empty answer.
func kimiQuestions(request map[string]json.RawMessage) []*leapmuxv1.ControlQuestion {
	records := kimiObjects(request, "questions")
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(records))
	for _, record := range records {
		id := providerkit.ObjectString(record, "id")
		header := providerkit.ObjectString(record, "header")
		text := providerkit.ObjectString(record, "question")
		if text == "" {
			text = header
		}
		if id == "" || text == "" {
			continue
		}
		question := &leapmuxv1.ControlQuestion{
			Id:          id,
			Question:    text,
			Options:     kimiQuestionOptions(record),
			MultiSelect: providerkit.RawBool(record["multi_select"]),
			AllowEmpty:  true,
		}
		if header != text {
			question.Header = header
		}
		questions = append(questions, question)
	}
	return questions
}

// kimiQuestionOptions reads the options of one question, each with its ID as
// the value that an answer sends.
func kimiQuestionOptions(record map[string]json.RawMessage) []*leapmuxv1.QuestionOption {
	records := kimiObjects(record, "options")
	options := make([]*leapmuxv1.QuestionOption, 0, len(records))
	for _, option := range records {
		value := providerkit.ObjectString(option, "id")
		label := providerkit.ObjectString(option, "label")
		if value == "" || label == "" {
			continue
		}
		options = append(options, &leapmuxv1.QuestionOption{
			Value:       &value,
			Label:       label,
			Description: providerkit.ObjectString(option, "description"),
		})
	}
	return options
}

// kimiApprovalPrompt reads one approval: the main agent's plan review as the
// plan approval, and every other approval as a permission.
func kimiApprovalPrompt(frame, request map[string]json.RawMessage) *leapmuxv1.ControlPrompt {
	display := providerkit.RawFields(request["tool_input_display"])
	displayKind := providerkit.ObjectString(display, "kind")
	agentID := providerkit.ObjectString(frame, "agent_id")
	if displayKind == contracts.KimiDisplayPlanReview && (agentID == "" || agentID == kimiMainAgentID) {
		plan := &leapmuxv1.PlanPrompt{Choices: kimiPlanChoices(display)}
		if text := providerkit.ObjectString(display, "plan"); agent.TrimJavaScriptSpace(text) != "" {
			plan.Text = text
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: plan}}
	}
	permission := &leapmuxv1.PermissionPrompt{
		Title:            providerkit.ObjectString(request, "tool_name"),
		Reason:           providerkit.ObjectString(request, "action"),
		Command:          providerkit.ObjectString(display, "command"),
		WorkingDirectory: providerkit.ObjectString(display, "cwd"),
		Options:          kimiPermissionOptions(displayKind, display),
	}
	// The display is the whole statement of the call: the server sends no
	// arguments beside it. A subagent's plan review states its plan in prose,
	// which reads as text, and the arguments keep the rest of the display.
	args := request["tool_input_display"]
	if displayKind == contracts.KimiDisplayPlanReview {
		if text := providerkit.ObjectString(display, "plan"); agent.TrimJavaScriptSpace(text) != "" {
			permission.Text = text
			args = kimiWithout(display, "plan")
		}
	}
	if display != nil {
		permission.InputJson = string(args)
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// kimiWithout encodes fields without key.
func kimiWithout(fields map[string]json.RawMessage, key string) json.RawMessage {
	rest := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		if name != key {
			rest[name] = value
		}
	}
	encoded, err := json.Marshal(rest)
	if err != nil {
		// The values are raw JSON that decoding already accepted, so this
		// encoding cannot fail.
		panic("kimi: encode a display: " + err.Error())
	}
	return encoded
}

// kimiPlanChoices reads the answers that a plan review offers beside a plain
// approve and reject: each approach that the plan lists, a request for
// revisions, and a refusal that also leaves plan mode. An approach is
// identified by its label, which the answer carries back.
func kimiPlanChoices(display map[string]json.RawMessage) []*leapmuxv1.PlanChoice {
	var choices []*leapmuxv1.PlanChoice
	for _, option := range kimiObjects(display, "options") {
		label := providerkit.ObjectString(option, "label")
		if label == "" {
			continue
		}
		choices = append(choices, &leapmuxv1.PlanChoice{
			Id:          label,
			Label:       "Approve: " + label,
			Description: providerkit.ObjectString(option, "description"),
			Effect:      leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE,
		})
	}
	return append(choices,
		&leapmuxv1.PlanChoice{Id: contracts.KimiPlanLabelRevise, Label: "Request revisions", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
		&leapmuxv1.PlanChoice{Id: contracts.KimiPlanLabelRejectAndExit, Label: "Reject and exit plan mode", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
	)
}

// kimiPermissionOptions reads the options of an approval that reads as a
// permission, by its display kind.
func kimiPermissionOptions(displayKind string, display map[string]json.RawMessage) []*leapmuxv1.PermissionOption {
	allowOnce := leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	rejectOnce := leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	switch displayKind {
	case contracts.KimiDisplayPlanReview:
		// A subagent's plan: the plain approval, each approach, the plain
		// refusal, and the two refusals with a plan label. kimiPlanChoices
		// states the words, so the main agent's plan approval reads alike.
		choices := kimiPlanChoices(display)
		options := []*leapmuxv1.PermissionOption{{OptionId: kimiControlOptionApprove, Kind: allowOnce, Name: "Approve the plan"}}
		for _, choice := range choices {
			if choice.GetEffect() == leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE {
				options = append(options, &leapmuxv1.PermissionOption{OptionId: kimiControlOptionPlanApprovePrefix + choice.GetId(), Kind: allowOnce, Name: choice.GetLabel()})
			}
		}
		options = append(options, &leapmuxv1.PermissionOption{OptionId: kimiControlOptionReject, Kind: rejectOnce, Name: "Reject"})
		for _, choice := range choices {
			if choice.GetEffect() != leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE {
				options = append(options, &leapmuxv1.PermissionOption{OptionId: kimiControlOptionPlanRejectPrefix + choice.GetId(), Kind: rejectOnce, Name: choice.GetLabel()})
			}
		}
		return options
	case contracts.KimiDisplayGoalStart:
		// The permission modes that a goal can start in, with the names that
		// Kimi's own UI gives them.
		return []*leapmuxv1.PermissionOption{
			{OptionId: kimiControlOptionApprove, Kind: allowOnce, Name: "Start the goal"},
			{OptionId: kimiControlOptionGoalModePrefix + contracts.KimiGoalModeManual, Kind: allowOnce, Name: "Start in Always Ask"},
			{OptionId: kimiControlOptionGoalModePrefix + contracts.KimiGoalModeYolo, Kind: allowOnce, Name: "Start in Ask When Needed"},
			{OptionId: kimiControlOptionGoalModePrefix + contracts.KimiGoalModeAuto, Kind: allowOnce, Name: "Start in Never Ask"},
			{OptionId: kimiControlOptionReject, Kind: rejectOnce, Name: "Decline"},
		}
	default:
		return []*leapmuxv1.PermissionOption{
			{OptionId: kimiControlOptionApprove, Kind: allowOnce, Name: "Allow"},
			{OptionId: kimiControlOptionApproveForSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
			{OptionId: kimiControlOptionReject, Kind: rejectOnce, Name: "Deny"},
		}
	}
}

// AnswerControlRequest builds Kimi Code's answer in the neutral envelope,
// which ResolveControlResponse turns into the server's own body.
func (kimiProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return kimiQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		if providerkit.IsWorkspaceTrustQuestion(input.Request.Payload) {
			return providerkit.AnswerWorkspaceTrust(input)
		}
		if _, _, ok := kimiControlFrame(input.Request.Payload, contracts.KimiInteractionKindApproval); !ok {
			// Only an approval and the trust question read as a permission
			// (ReadControlRequest).
			return agent.ControlAnswer{}, agent.InvalidControlDecision("Kimi Code asks no permission for this request")
		}
		return kimiPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Kimi Code states no %T prompt", input.Prompt.GetKind())
	}
}

// kimiPermissionAnswer answers an approval that reads as a permission. Each
// option sends its own fields beside the behavior:
//
//   - approve allows.
//   - approve_for_session allows for the session.
//   - goal_mode:<mode> and plan_approve:<label> allow with the label as the
//     choice.
//   - plan_reject:<label> denies with the label as the choice.
//   - reject denies.
//
// A deny carries the user's feedback, or the placeholder message when the
// user typed none (agent.DenyResponse). The composer's typed refusal is
// the same deny with no label. A plan label keeps its place beside the
// feedback, because the server takes both, and a Revise with its reason
// states what to revise.
func kimiPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	id := option.GetOptionId()
	var content []byte
	switch {
	case id == kimiControlOptionApprove:
		content = agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
			Behavior: contracts.ControlAnswerBehaviorAllow,
		})
	case id == kimiControlOptionApproveForSession:
		content = agent.ControlResponseEnvelope(input.RequestID, map[string]any{
			"behavior":               contracts.ControlAnswerBehaviorAllow,
			contracts.KimiReplyScope: contracts.KimiApprovalScopeSession,
		})
	case strings.HasPrefix(id, kimiControlOptionGoalModePrefix):
		content = agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
			Behavior: contracts.ControlAnswerBehaviorAllow,
			Choice:   strings.TrimPrefix(id, kimiControlOptionGoalModePrefix),
		})
	case strings.HasPrefix(id, kimiControlOptionPlanApprovePrefix):
		content = agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
			Behavior: contracts.ControlAnswerBehaviorAllow,
			Choice:   strings.TrimPrefix(id, kimiControlOptionPlanApprovePrefix),
		})
	case strings.HasPrefix(id, kimiControlOptionPlanRejectPrefix):
		content = agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
			Behavior: contracts.ControlAnswerBehaviorDeny,
			Message:  agent.DenyMessage(feedback),
			Choice:   strings.TrimPrefix(id, kimiControlOptionPlanRejectPrefix),
		})
	default:
		content = agent.DenyResponse(input.RequestID, feedback)
	}
	return agent.ControlAnswer{Content: content}, nil
}

// kimiQuestionAnswer answers every question of a question request, by its
// ID, in the server's own answer shape, beside the neutral allow. A cancelled
// decision denies with its feedback: the server's dismissal cannot carry the
// text, so the resolution sends it as the user's next message.
func kimiQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	answers := map[string]map[string]any{}
	for i, question := range questions {
		// The resolution checks each answer against the stored request by
		// its ID, so a question with no ID has no answer that it can carry.
		if question.GetId() == "" {
			continue
		}
		answers[question.GetId()] = kimiAnswer(question, agent.QuestionAnswerAt(decision, i))
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior":                 contracts.ControlAnswerBehaviorAllow,
		contracts.KimiReplyAnswers: answers,
	})}, nil
}

// kimiAnswer is the server's answer to one question: its answer values
// (agent.QuestionAnswerValues) in the server's own shape. QuestionDecisionOf
// already verified every selected option against the question. The values list
// the selected option IDs first, and the typed text last where the answer keeps
// it, so the two parts split at the count of the selection:
//
//   - A multiple-choice question takes options and text together, which
//     the server spells `multi_with_other`.
//   - A single-choice question takes an option or the text. Its values never
//     keep the text beside an option.
//   - A question with neither is skipped.
func kimiAnswer(question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer) map[string]any {
	values := agent.QuestionAnswerValues(question, answer)
	selected := values[:len(answer.GetSelected())]
	var text string
	if len(values) > len(selected) {
		text = values[len(selected)]
	}
	switch {
	case question.GetMultiSelect() && len(selected) > 0 && text != "":
		return map[string]any{"kind": contracts.KimiAnswerKindMultiWithOther, "option_ids": selected, "other_text": text}
	case question.GetMultiSelect() && len(selected) > 0:
		return map[string]any{"kind": contracts.KimiAnswerKindMulti, "option_ids": selected}
	case !question.GetMultiSelect() && len(selected) > 0:
		return map[string]any{"kind": contracts.KimiAnswerKindSingle, "option_id": selected[0]}
	case text != "":
		return map[string]any{"kind": contracts.KimiAnswerKindOther, "text": text}
	default:
		return map[string]any{"kind": contracts.KimiAnswerKindSkipped}
	}
}
