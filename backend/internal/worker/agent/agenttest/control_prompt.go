package agenttest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The helpers of a provider's control prompt test. Each provider reads its
// own control requests (Provider.ReadControlRequest) and answers them
// (Provider.AnswerControlRequest); these helpers drive both as the Worker
// does, so a test states only the payload, the decision, and what it wants.

// ControlPromptOf reads payload through provider, as the Worker reads a
// stored request that has no transcript row to load.
func ControlPromptOf(provider agent.Provider, payload []byte) *leapmuxv1.ControlPrompt {
	return provider.ReadControlRequest(agent.NewControlRequestInput(payload))
}

// AnswerControl answers payload, stored as requestID, through provider, with
// the prompt that provider reads from the same payload, as the Worker does.
func AnswerControl(provider agent.Provider, payload []byte, requestID string, decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
	input := agent.NewControlRequestInput(payload)
	return provider.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: requestID, Prompt: provider.ReadControlRequest(input), Decision: decision,
	})
}

// ResolveAnswer resolves answer to payload, stored as requestID, as the
// Worker does when it delivers the answer to the runtime, in the context that
// ControlResponseContextOf states.
//
// It panics when the answer addresses another request than requestID
// (Provider.ControlResponseRequestID), and when payload is a JSON object
// that gives another key than requestID, or none
// (agent.StoredControlRequestAgrees). The Worker refuses such an answer, and
// such a stored request, before it resolves the answer, so a test that pairs
// them states a context that no Worker path builds. A payload that is no JSON
// object, and an absent payload, stay for the suites that pin what a provider
// does with them (AssertWithholdsTheResponseForAMalformedRequest and
// AssertPreservesTheResponseWithoutARequest).
func ResolveAnswer(provider agent.Provider, payload []byte, requestID string, answer agent.ControlAnswer) agent.ControlResponseResolution {
	return ResolveAnswerInSession(provider, payload, "", requestID, answer)
}

// ResolveAnswerInSession is ResolveAnswer for a request that the Worker
// stores with the provider session agentSessionID, for a provider whose key
// reads that session (Provider.ControlRequestKey).
func ResolveAnswerInSession(provider agent.Provider, payload []byte, agentSessionID, requestID string, answer agent.ControlAnswer) agent.ControlResponseResolution {
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) == nil && object != nil &&
		!agent.StoredControlRequestAgrees(provider, requestID, payload, agentSessionID) {
		key, _ := provider.ControlRequestKey(payload, agentSessionID)
		panic(fmt.Sprintf("agenttest: the stored payload gives the key %q, not its key %q: %s", key, requestID, payload))
	}
	return ResolveAnswerPastTheKeyCheck(provider, payload, requestID, answer)
}

// ResolveAnswerPastTheKeyCheck is ResolveAnswer for a stored payload that
// gives another key than requestID, or none. No Worker path resolves that
// context: the Worker refuses the stored request before a provider reads it
// (agent.StoredControlRequestAgrees). A test resolves it to pin a provider's
// own guard against such a payload, so a later caller that skips the
// Worker's check still sends nothing that the provider cannot address.
//
// It panics when the answer addresses another request than requestID, as
// ResolveAnswer does.
func ResolveAnswerPastTheKeyCheck(provider agent.Provider, payload []byte, requestID string, answer agent.ControlAnswer) agent.ControlResponseResolution {
	if addressed := provider.ControlResponseRequestID(answer.Content); addressed != requestID {
		panic(fmt.Sprintf("agenttest: the answer addresses the request %q, not the stored request %q", addressed, requestID))
	}
	return provider.ResolveControlResponse(ControlResponseContextOf(payload, requestID, answer))
}

// ControlResponseContextOf is the context in which the Worker resolves answer
// to payload, stored as requestID. A test of a function that reads the
// context itself, rather than a provider, builds the context here. The
// context states:
//
//   - requestID, the key of the stored request. The Worker reads it from the
//     answer (Provider.ControlResponseRequestID).
//   - payload, the stored request. A test states a nil payload only to pin a
//     provider's own handling of a request whose row is gone
//     (AssertPreservesTheResponseWithoutARequest): the Worker refuses an
//     answer to such a request before a provider resolves it.
//   - The content of the answer.
//   - The tool name of the stored request (agent.ControlToolName), because a
//     provider's resolution reads it.
//   - The plan settings of the answer. The Worker states none when it rebuilds
//     a delivered answer from its stored row, and it reads the stored settings
//     from that row instead.
func ControlResponseContextOf(payload []byte, requestID string, answer agent.ControlAnswer) agent.ControlResponseContext {
	return agent.ControlResponseContext{
		RequestID:       requestID,
		RequestPayload:  json.RawMessage(payload),
		ResponseContent: answer.Content,
		ToolName:        agent.ControlToolName(payload),
		PlanApproval:    answer.PlanApproval,
	}
}

// RequireProto requires want and got to be equal messages, and prints both
// as JSON when they differ.
func RequireProto(t *testing.T, want, got proto.Message, msgAndArgs ...any) {
	t.Helper()
	if proto.Equal(want, got) {
		return
	}
	require.Fail(t, fmt.Sprintf("the messages differ\nwant %s\ngot  %s", protojson.Format(want), protojson.Format(got)), msgAndArgs...)
}

// PermissionPrompt wraps permission as a prompt.
func PermissionPrompt(permission *leapmuxv1.PermissionPrompt) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// PermissionDecision chooses optionID, with feedback for a refusing option.
func PermissionDecision(optionID, feedback string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{Permission: &leapmuxv1.PermissionDecision{OptionId: optionID, Feedback: feedback}}}
}

// PlanDecision wraps plan as a decision.
func PlanDecision(plan *leapmuxv1.PlanDecision) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Plan{Plan: plan}}
}

// QuestionDecision answers each question of a prompt with one answer, in
// order.
func QuestionDecision(answers ...*leapmuxv1.QuestionAnswer) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{Answers: answers}}}
}

// CancelQuestions refuses to answer the questions of a prompt, with the
// reason.
func CancelQuestions(feedback string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{Cancelled: true, Feedback: feedback}}}
}

// ElicitationDecision answers an MCP elicitation with action, and with the
// form's content as JSON text for an accept.
func ElicitationDecision(action leapmuxv1.ElicitationAction, content string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Elicitation{Elicitation: &leapmuxv1.ElicitationDecision{Action: action, ContentJson: content}}}
}

// ElicitationChoiceDecision accepts an MCP elicitation with choiceKey, one key
// of the prompt's accept choices, and with the form's content as JSON text.
// Only an accept carries an accept choice (agent.ElicitationDecisionOf), so
// the action is always accept.
func ElicitationChoiceDecision(content, choiceKey string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Elicitation{Elicitation: &leapmuxv1.ElicitationDecision{
		Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: content, AcceptChoiceKey: choiceKey,
	}}}
}

// DialogDecision answers an extension dialog with action, and with the text
// of a submit.
func DialogDecision(action leapmuxv1.DialogAction, value string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: &leapmuxv1.DialogDecision{Action: action, Value: value}}}
}
