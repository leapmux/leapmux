package kimi

import (
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"

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

// kimiStoredRequest is the part of a stored control request the resolution
// reads: which event it is, which agent asks, and what it asks.
type kimiStoredRequest struct {
	Type string `json:"type"`
	// AgentID is the interaction's own agent tag, and EventAgentID is the agent
	// the event envelope states. The server fills both from the same tag.
	AgentID          string `json:"agent_id"`
	EventAgentID     string `json:"agentId"`
	ToolName         string `json:"tool_name"`
	ToolInputDisplay struct {
		Kind    string              `json:"kind"`
		Options []kimiLabeledOption `json:"options"`
	} `json:"tool_input_display"`
	Questions []struct {
		ID      string `json:"id"`
		Options []struct {
			ID string `json:"id"`
		} `json:"options"`
		MultiSelect bool `json:"multi_select"`
	} `json:"questions"`
}

// agent returns the agent the request belongs to. The server words an
// interaction with no agent tag as the main agent's (interactionAgentId), so
// a request that states no agent is the main agent's.
func (r kimiStoredRequest) agent() string {
	switch {
	case r.AgentID != "":
		return r.AgentID
	case r.EventAgentID != "":
		return r.EventAgentID
	default:
		return kimiMainAgentID
	}
}

// kimiLabeledOption is one option a plan review offers.
type kimiLabeledOption struct {
	Label string `json:"label"`
}

// kimiResolveControlResponse turns the browser's answer into the server's body.
func kimiResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		// No stored request: nothing states which route answers it, so the bytes
		// travel as they came.
		return result
	}
	var request kimiStoredRequest
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		slog.Warn("kimi stored control request does not decode", "request_id", ctx.RequestID, "error", err)
		result.Withhold = true
		return result
	}
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		slog.Warn("kimi control response carried no decision", "request_id", ctx.RequestID)
		result.Withhold = true
		return result
	}
	if requestID != "" && ctx.RequestID != "" && requestID != ctx.RequestID {
		slog.Warn("kimi control response addressed another request", "answered", requestID, "stored", ctx.RequestID)
		result.Withhold = true
		return result
	}
	allow := behavior == agent.ControlBehaviorAllow
	var fields contracts.KimiDecisionFields
	if err := json.Unmarshal(kimiInnerResponse(ctx.ResponseContent), &fields); err != nil {
		result.Withhold = true
		return result
	}
	choice := agent.DecodeControlChoice(ctx.ResponseContent)

	var native any
	switch request.Type {
	case contracts.KimiEventApprovalRequested:
		reply, ok := kimiApprovalReply(request, allow, message, choice, fields.Scope)
		if !ok {
			result.Withhold = true
			return result
		}
		switch request.ToolInputDisplay.Kind {
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
	case contracts.KimiEventQuestionRequested:
		if !allow {
			// A refusal dismisses the question. Its text cannot ride the dismissal,
			// so it reaches the model as the user's next message.
			native = contracts.KimiQuestionReply{Dismiss: true}
			result.Feedback = message
			break
		}
		if !kimiAnswersFit(request, fields.Answers) {
			slog.Warn("kimi question answer does not fit its question", "request_id", ctx.RequestID)
			result.Withhold = true
			return result
		}
		native = contracts.KimiQuestionReply{Answers: fields.Answers, Method: kimiQuestionMethod}
	default:
		slog.Warn("kimi stored control request is not an approval or a question", "request_id", ctx.RequestID, "type", request.Type)
		result.Withhold = true
		return result
	}
	content, err := kimiControlResponseContent(ctx.RequestID, native)
	if err != nil {
		result.Withhold = true
		return result
	}
	result.Content = content
	return result
}

// kimiApprovalReply builds the body of one approval decision.
//
// The choice is a label the request offered: a plan's own option, `Revise` or
// `Reject and Exit` for a plan, or a permission mode for a goal start. One the
// request did not offer is refused rather than sent, because the server would
// take an unknown label for a plain decision and do something the user did not
// pick.
func kimiApprovalReply(request kimiStoredRequest, allow bool, message, choice, scope string) (contracts.KimiApprovalReply, bool) {
	reply := contracts.KimiApprovalReply{Decision: contracts.KimiDecisionRejected}
	if allow {
		reply.Decision = contracts.KimiDecisionApproved
	} else if message != "" {
		reply.Feedback = message
	}
	if choice != "" {
		if !kimiChoiceOffered(request, allow, choice) {
			return contracts.KimiApprovalReply{}, false
		}
		reply.SelectedLabel = choice
	}
	if allow && scope != "" {
		if scope != contracts.KimiApprovalScopeSession || request.ToolInputDisplay.Kind == contracts.KimiDisplayPlanReview {
			return contracts.KimiApprovalReply{}, false
		}
		reply.Scope = scope
	}
	return reply, true
}

// kimiChoiceOffered reports whether a request offers choice for a decision of
// this polarity.
func kimiChoiceOffered(request kimiStoredRequest, allow bool, choice string) bool {
	switch request.ToolInputDisplay.Kind {
	case contracts.KimiDisplayPlanReview:
		if !allow {
			return choice == contracts.KimiPlanLabelRevise || choice == contracts.KimiPlanLabelRejectAndExit
		}
		return slices.ContainsFunc(request.ToolInputDisplay.Options, func(option kimiLabeledOption) bool {
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
func kimiAnswersFit(request kimiStoredRequest, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var answers map[string]kimiAnswerFit
	if err := json.Unmarshal(raw, &answers); err != nil || len(answers) == 0 {
		return false
	}
	options := make(map[string][]string, len(request.Questions))
	multi := make(map[string]bool, len(request.Questions))
	for _, question := range request.Questions {
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
