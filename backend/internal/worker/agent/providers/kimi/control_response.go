package kimi

import (
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code's control answers.
//
// The browser sends the neutral approve/reject envelope every control surface
// produces, plus the fields a Kimi request needs beside the behavior: an approval
// scope, a plan or goal choice, a question's answers in the server's own shape.
// This file turns one of those into the exact body the server's REST route takes,
// so the server's vocabulary stays in the provider and the browser states a
// decision. The resolution is pure: the running agent posts the body
// (deliverControlResponse).

// kimiResolveControlResponse turns the browser's answer into the server's body.
func kimiResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		// No stored request: nothing states which route answers it, so the bytes
		// travel as they came.
		return result
	}
	var request kimiInteraction
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		slog.Warn("kimi stored control request does not decode", "request_id", ctx.RequestID, "error", err)
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	if request.Type != contracts.KimiFrameInteraction || request.Request == nil || request.Status != kimiInteractionPending {
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		slog.Warn("kimi control response carried no decision", "request_id", ctx.RequestID)
		result.Refuse(agent.RefusalNoDecision)
		return result
	}
	if requestID != "" && ctx.RequestID != "" && requestID != ctx.RequestID {
		slog.Warn("kimi control response addressed another request", "answered", requestID, "stored", ctx.RequestID)
		result.Refuse(agent.RefusalOtherRequest)
		return result
	}
	allow := behavior == agent.ControlBehaviorAllow
	var fields contracts.KimiDecisionFields
	if err := json.Unmarshal(kimiInnerResponse(ctx.ResponseContent), &fields); err != nil {
		result.Refuse(agent.RefusalUnreadableAnswer)
		return result
	}
	choice := agent.DecodeControlChoice(ctx.ResponseContent)

	var native any
	switch request.Kind {
	case contracts.KimiInteractionKindApproval:
		reply, refusal := kimiApprovalReply(request, allow, message, choice, fields.Scope)
		if refusal != "" {
			result.Refuse(refusal)
			return result
		}
		switch request.Request.ToolInputDisplay.Kind {
		case contracts.KimiDisplayPlanReview:
			if request.agent() != kimiMainAgentID {
				// A subagent's plan review is the subagent's own: the approval takes
				// the subagent out of plan mode, and the main agent's plan mode and
				// permission mode stay as they are. The answer carries the decision,
				// the label and the feedback alone.
				break
			}
			result.PlanModeControl = agent.PlanModeControlExit
			if allow && !ctx.PlanApproval.GetClearContext() {
				reply.PermissionMode = kimiPlanExitMode(ctx.PlanApproval.GetPermissionMode())
			}
		case contracts.KimiDisplayGoalStart:
			// The server switches to the chosen mode by itself when it takes the
			// label, and reports the switch on no event. The worker writes the
			// same mode first, so LeapMux's copy of it follows.
			if allow {
				reply.PermissionMode = reply.SelectedLabel
			}
		}
		native = reply
	case contracts.KimiInteractionKindQuestion:
		if !allow {
			// A refusal dismisses the question. Its text cannot ride the dismissal,
			// so it reaches the model as the user's next message.
			native = contracts.KimiQuestionReply{Kind: contracts.KimiInteractionKindQuestion, Dismissed: true}
			result.FollowUp = agent.FeedbackFollowUp(message)
			break
		}
		if !kimiAnswersFit(request, fields.Answers) {
			slog.Warn("kimi question answer does not fit its question", "request_id", ctx.RequestID)
			result.Refuse(agent.RefusalAnswersDoNotFit)
			return result
		}
		native = contracts.KimiQuestionReply{Kind: contracts.KimiInteractionKindQuestion, Answers: fields.Answers, Method: kimiQuestionMethod}
	default:
		slog.Warn("kimi stored control request is not an approval or a question", "request_id", ctx.RequestID, "type", request.Type)
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	content, err := kimiControlResponseContent(ctx.RequestID, native)
	if err != nil {
		result.Refuse(agent.RefusalUnencodableReply)
		return result
	}
	result.Content = content
	return result
}

// kimiApprovalReply builds the body of one approval decision. The second result
// is the reason the reader sees for a refusal, empty when the reply stands.
//
// The choice is a label the request offered: a plan's own option, `Revise` or
// `Reject and Exit` for a plan, or a permission mode for a goal start. One the
// request did not offer is refused rather than sent, because the server would
// take an unknown label for a plain decision and do something the user did not
// pick.
func kimiApprovalReply(request kimiInteraction, allow bool, message, choice, scope string) (contracts.KimiApprovalReply, string) {
	reply := contracts.KimiApprovalReply{Kind: contracts.KimiInteractionKindApproval, Decision: contracts.KimiDecisionRejected}
	if allow {
		reply.Decision = contracts.KimiDecisionApproved
	} else if message != "" {
		reply.Feedback = message
	}
	if choice != "" {
		if !kimiChoiceOffered(request, allow, choice) {
			return contracts.KimiApprovalReply{}, agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, choice)
		}
		reply.SelectedLabel = choice
	}
	if allow && scope != "" {
		if scope != contracts.KimiApprovalScopeSession {
			return contracts.KimiApprovalReply{}, agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, scope)
		}
		if request.Request.ToolInputDisplay.Kind == contracts.KimiDisplayPlanReview {
			return contracts.KimiApprovalReply{}, "a plan review takes no session scope"
		}
		reply.Scope = scope
	}
	return reply, ""
}

// kimiChoiceOffered reports whether a request offers choice for a decision of
// this polarity.
func kimiChoiceOffered(request kimiInteraction, allow bool, choice string) bool {
	switch request.Request.ToolInputDisplay.Kind {
	case contracts.KimiDisplayPlanReview:
		if !allow {
			return choice == contracts.KimiPlanLabelRevise || choice == contracts.KimiPlanLabelRejectAndExit
		}
		return slices.ContainsFunc(request.Request.ToolInputDisplay.Options, func(option kimiLabeledOption) bool {
			return option.Label == choice
		})
	case contracts.KimiDisplayGoalStart:
		return allow && (choice == contracts.KimiGoalModeManual || choice == contracts.KimiGoalModeYolo || choice == contracts.KimiGoalModeAuto)
	default:
		return false
	}
}

// kimiPlanExitMode is the permission mode an approved plan switches to: the one
// the user picked in the banner, or Kimi's own default. `plan` is not a mode a
// plan approval can switch to -- the approval is what leaves it.
func kimiPlanExitMode(picked string) string {
	switch picked {
	case contracts.KimiModeManual, contracts.KimiModeYolo, contracts.KimiModeAuto:
		return picked
	default:
		return contracts.KimiDefaultMode
	}
}

// kimiAnswerFit is the part of one question answer the check reads.
type kimiAnswerFit struct {
	Kind      string   `json:"kind"`
	OptionID  string   `json:"option_id"`
	OptionIDs []string `json:"option_ids"`
	Text      *string  `json:"text"`
	OtherText *string  `json:"other_text"`
}

// kimiAnswersFit reports whether answers answer the stored questions: every key
// is a question of the request, every option id is one of that question's
// options, and each kind carries the fields the server requires of it.
func kimiAnswersFit(request kimiInteraction, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var answers map[string]kimiAnswerFit
	if err := json.Unmarshal(raw, &answers); err != nil || len(answers) == 0 {
		return false
	}
	options := make(map[string][]string, len(request.Request.Questions))
	multi := make(map[string]bool, len(request.Request.Questions))
	for _, question := range request.Request.Questions {
		ids := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			ids = append(ids, option.ID)
		}
		options[question.ID] = ids
		multi[question.ID] = question.MultiSelect
	}
	for questionID, answer := range answers {
		offered, known := options[questionID]
		if !known {
			return false
		}
		switch answer.Kind {
		case contracts.KimiAnswerKindSingle:
			if !slices.Contains(offered, answer.OptionID) {
				return false
			}
		case contracts.KimiAnswerKindMulti:
			if !multi[questionID] || len(answer.OptionIDs) == 0 || !kimiAllOffered(offered, answer.OptionIDs) {
				return false
			}
		case contracts.KimiAnswerKindMultiWithOther:
			if !multi[questionID] || answer.OtherText == nil || !kimiAllOffered(offered, answer.OptionIDs) {
				return false
			}
		case contracts.KimiAnswerKindOther:
			if answer.Text == nil {
				return false
			}
		case contracts.KimiAnswerKindSkipped:
		default:
			return false
		}
	}
	return true
}

func kimiAllOffered(offered, picked []string) bool {
	for _, id := range picked {
		if !slices.Contains(offered, id) {
			return false
		}
	}
	return true
}

// kimiInnerResponse returns the inner response object of the browser's envelope,
// or an empty object.
func kimiInnerResponse(content []byte) json.RawMessage {
	var envelope kimiControlEnvelope
	if json.Unmarshal(content, &envelope) != nil || len(envelope.Response.Response) == 0 {
		return json.RawMessage(`{}`)
	}
	return envelope.Response.Response
}

// kimiControlResponseContent wraps the server's body in the shared envelope,
// which is what the service stores and SendRawInput reads back.
func kimiControlResponseContent(requestID string, native any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   native,
		},
	})
}
