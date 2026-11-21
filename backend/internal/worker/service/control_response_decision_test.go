package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// storedAnswerDecision reads the decision that the answer row of one request
// instance stores, or nil for a row that stores none.
func storedAnswerDecision(t *testing.T, svc *Service, requestID string) *leapmuxv1.ControlDecision {
	t.Helper()
	answer, err := svc.Queries.GetControlResponseAnswer(context.Background(), db.GetControlResponseAnswerParams{
		AgentID: "agent-1", RequestID: requestID, ClaimToken: "claim-1",
	})
	require.NoError(t, err)
	if len(answer.Decision) == 0 {
		return nil
	}
	decision := &leapmuxv1.ControlDecision{}
	require.NoError(t, proto.Unmarshal(answer.Decision, decision))
	return decision
}

// recordedControlDecision reads the decision that the one persisted response
// row of agent-1 states in its metadata, or nil for a row that states none.
func recordedControlDecision(t *testing.T, svc *Service) *leapmuxv1.ControlDecision {
	t.Helper()
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
	require.NoError(t, err)
	rows = controlResponseRows(rows)
	require.Len(t, rows, 1)
	supplement, err := msgcodec.Decompress(rows[0].SupplementalContent, rows[0].SupplementalContentCompression)
	require.NoError(t, err)
	var fields struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(supplement, &fields))
	encoded, ok := fields.Metadata["control_decision"]
	if !ok {
		return nil
	}
	decision := &leapmuxv1.ControlDecision{}
	require.NoError(t, protojson.Unmarshal(encoded, decision))
	return decision
}

// requireDecision fails unless got is the decision want.
func requireDecision(t *testing.T, want, got *leapmuxv1.ControlDecision) {
	t.Helper()
	require.Truef(t, proto.Equal(want, got), "want %v, got %v", want, got)
}

// A refusal whose runtime carries the feedback in the answer itself: the
// answer row and the transcript row both state the whole decision.
func TestAnswerControl_RecordsTheDecisionWithTheAnswer(t *testing.T) {
	t.Parallel()
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)

	decision := permissionDecisionOf("deny", "Edit the test file instead.")
	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: decision,
	})
	require.Empty(t, errs)
	require.Empty(t, response.GetError())
	require.Len(t, *delivered, 1)
	assert.Contains(t, string((*delivered)[0]), "Edit the test file instead.", "Claude carries the feedback in the answer")

	requireDecision(t, decision, storedAnswerDecision(t, svc, "req-1"))
	requireDecision(t, decision, recordedControlDecision(t, svc))
}

func TestAnswerControl_RecordsDroidChoicesInTheirDeliveredOrder(t *testing.T) {
	t.Parallel()
	const requestID = "droid-ask-query"
	payload := fmt.Sprintf(`{"type":%q,"requestId":"droid-ask-query","rpcId":"query","toolCallId":"query","questions":[{"index":1,"question":"Which colors?","options":["Blue","Green","Red"],"multiSelect":true}]}`, contracts.DroidRequestTypeAskUser)
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, requestID, payload)

	decision := agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red", "Blue"}, CustomText: "Purple"})
	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: requestID, ClaimToken: "claim-1", Decision: decision,
	})
	require.Empty(t, errs)
	require.Empty(t, response.GetError())
	require.Len(t, *delivered, 1)
	assert.Contains(t, string((*delivered)[0]), "Blue, Red, Purple")

	want := agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue", "Red"}, CustomText: "Purple"})
	requireDecision(t, want, storedAnswerDecision(t, svc, requestID))
	requireDecision(t, want, recordedControlDecision(t, svc))
	assert.Equal(t, []string{"Red", "Blue"}, decision.GetQuestion().GetAnswers()[0].GetSelected())
}

// A runtime whose answer cannot carry the feedback receives it as the user's
// next message, which draws a row of its own. The answer row keeps the
// feedback, and the answer's transcript row states the decision without it.
func TestAnswerControl_LeavesTheFollowUpFeedbackOffTheTranscriptDecision(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}`
	svc, d, _ := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "jsonrpc:7", payload)

	decision := permissionDecisionOf("no", "Use the other file.")
	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "jsonrpc:7", ClaimToken: "claim-1", Decision: decision,
	})
	require.Empty(t, errs)
	require.Empty(t, response.GetError())

	requireDecision(t, decision, storedAnswerDecision(t, svc, "jsonrpc:7"))
	requireDecision(t, permissionDecisionOf("no", ""), recordedControlDecision(t, svc))
	assert.Equal(t, "Use the other file.", decision.GetPermission().GetFeedback(), "the transcript copy leaves the caller's decision alone")
}

// An answer that a Worker restart interrupts after delivery is recorded from
// its answer row (RecordControlResponse), so the transcript row states the
// decision that the row stores, and not one that a later call brings.
func TestRecordControlResponse_StatesTheStoredDecisionAfterARestart(t *testing.T) {
	t.Parallel()
	svc, _, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_answer BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
	require.NoError(t, err)

	decision := permissionDecisionOf("session", "")
	plugin := svc.Agents.Registry().Plugin(row.AgentProvider)
	request := agent.NewControlRequestInput([]byte(claudeEditRequest))
	built, err := plugin.AnswerControlRequest(agent.ControlAnswerInput{
		Request: request, RequestID: "req-1", Prompt: agent.ReadControlPrompt(plugin, request), Decision: decision,
	})
	require.NoError(t, err)
	err = svc.processControlAnswer(row, workerAnswer{claimToken: "claim-1", content: built.Content, decision: decision})
	require.ErrorContains(t, err, "answer storage failed")
	require.Len(t, *delivered, 1, "the agent received the answer before the transcript write failed")
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_answer")
	require.NoError(t, err)

	restored := New(svc.Config)
	t.Cleanup(restored.InputQueue.StopAndWait)
	recorded, err := restored.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{RequestId: "req-1", ClaimToken: "claim-1"})
	require.NoError(t, err)
	require.Empty(t, recorded.GetError())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, recorded.GetState())
	requireDecision(t, decision, recordedControlDecision(t, restored))
}

// An answer that the Worker recorded without a decision states none, and a
// stored decision that does not decode states none either: the answer
// reached the agent already, so the Worker still records it.
func TestFinalizeControlResponse_StatesNoDecisionThatTheAnswerRowLacks(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string][]byte{
		"no decision": nil,
		// A length-delimited field 1 whose length is missing.
		"undecodable": {0x0a},
		// Field 15, which ControlDecision does not declare, so the decision
		// decodes with no kind.
		"a decision of no kind": {0x78, 0x01},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "jsonrpc:1",
				`{"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`)
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_answer BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
			require.NoError(t, err)
			err = svc.processControlAnswer(row, workerAnswer{
				claimToken: "claim-1",
				content:    []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
				decision:   permissionDecisionOf("accept", ""),
			})
			require.ErrorContains(t, err, "answer storage failed")
			_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_answer")
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(t.Context(), `UPDATE control_response_payloads SET decision = COALESCE(?, X'')`, stored)
			require.NoError(t, err)

			recorded, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{RequestId: "jsonrpc:1", ClaimToken: "claim-1"})
			require.NoError(t, err)
			require.Empty(t, recorded.GetError())
			assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, recorded.GetState())
			assert.Nil(t, recordedControlDecision(t, svc))
		})
	}
}

func TestEncodeControlDecision(t *testing.T) {
	t.Parallel()
	for _, decision := range []*leapmuxv1.ControlDecision{nil, {}} {
		encoded, err := encodeControlDecision(decision)
		require.NoError(t, err)
		assert.Nil(t, encoded, "a decision of no kind stores as none")
	}

	decision := &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{
		Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"b", "a"}, CustomText: "note"}},
	}}}
	encoded, err := encodeControlDecision(decision)
	require.NoError(t, err)
	again, err := encodeControlDecision(decision)
	require.NoError(t, err)
	assert.Equal(t, encoded, again, "one decision has one stored form")
	requireDecision(t, decision, storedControlDecision(db.ControlResponseAnswer{Decision: encoded}))
}

func TestTranscriptControlDecision(t *testing.T) {
	t.Parallel()
	feedback := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK
	questionAnswer := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER
	plan := func(feedback string) *leapmuxv1.ControlDecision {
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Plan{Plan: &leapmuxv1.PlanDecision{ChoiceId: "revise", Feedback: feedback}}}
	}
	question := func(feedback string) *leapmuxv1.ControlDecision {
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{Cancelled: true, Feedback: feedback}}}
	}
	elicitation := &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Elicitation{Elicitation: &leapmuxv1.ElicitationDecision{
		Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{"a":1}`,
	}}}
	dialog := &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: &leapmuxv1.DialogDecision{
		Action: leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, Value: "text",
	}}}
	for _, tc := range []struct {
		name     string
		decision *leapmuxv1.ControlDecision
		followUp leapmuxv1.AgentInputKind
		want     *leapmuxv1.ControlDecision
	}{
		{"no decision", nil, feedback, nil},
		{"a permission whose answer carries the feedback", permissionDecisionOf("deny", "why"), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, permissionDecisionOf("deny", "why")},
		{"a permission whose feedback follows", permissionDecisionOf("deny", "why"), feedback, permissionDecisionOf("deny", "")},
		{"a plan whose feedback follows", plan("why"), feedback, plan("")},
		{"a question whose feedback follows", question("why"), feedback, question("")},
		{"a question whose answer follows", question("why"), questionAnswer, question("why")},
		{"a form", elicitation, feedback, elicitation},
		{"a dialog", dialog, feedback, dialog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var original *leapmuxv1.ControlDecision
			if tc.decision != nil {
				original = proto.CloneOf(tc.decision)
			}
			plan := controlResponsePlan{controlDecision: tc.decision}
			plan.resolution.FollowUp = agent.ControlFollowUp{Kind: tc.followUp, Text: "why"}
			requireDecision(t, tc.want, plan.transcriptControlDecision())
			requireDecision(t, original, tc.decision)
		})
	}
}

// openCodeQuestionForm is a stored OpenCode question form of one question.
var openCodeQuestionForm = `{"type":"` + contracts.OpenCodeNativeEventFormCreated + `","data":{"form":{"id":"frm_1","sessionID":"ses_1","metadata":{"kind":"question"},"fields":[{"key":"task","type":"string","title":"Pick a task","options":[{"value":"Build","label":"Build"}]}]}}}`

// A runtime that takes no reason with an answer gets none from its provider.
// OpenCode dismisses a question with no reason, so the answer row stores the
// dismissal without the reason, and the transcript row states none.
func TestAnswerControl_StoresNoFeedbackThatTheRuntimeDoesNotReceive(t *testing.T) {
	t.Parallel()
	const requestID = "opencode:form:ses_1:frm_1"
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, requestID, openCodeQuestionForm)

	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: requestID, ClaimToken: "claim-1", Decision: agenttest.CancelQuestions("User stopped"),
	})
	require.Empty(t, errs)
	require.Empty(t, response.GetError())
	require.Len(t, *delivered, 1)
	assert.NotContains(t, string((*delivered)[0]), "User stopped", "OpenCode's dismissal carries no reason")

	requireDecision(t, agenttest.CancelQuestions(""), storedAnswerDecision(t, svc, requestID))
	requireDecision(t, agenttest.CancelQuestions(""), recordedControlDecision(t, svc))
}

// A runtime that takes the reason in the answer receives it, so the answer row
// and the transcript row keep it.
func TestAnswerControl_StoresTheFeedbackThatTheAnswerCarries(t *testing.T) {
	t.Parallel()
	payload := `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -r build"}}}`
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", payload)

	decision := permissionDecisionOf("deny", "Keep the build directory.")
	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: decision,
	})
	require.Empty(t, errs)
	require.Empty(t, response.GetError())
	require.Len(t, *delivered, 1)
	assert.Contains(t, string((*delivered)[0]), "Keep the build directory.")
	requireDecision(t, decision, storedAnswerDecision(t, svc, "req-1"))
}

// scriptedAnswerProvider builds an answer that carries the feedback only when
// carry says so, and states a follow-up only when followUp says so. Its
// resolution passes the answer's bytes through, or drops every reason when
// dropOnResolve says so.
type scriptedAnswerProvider struct {
	agent.ProviderDefaults
	carry         bool
	followUp      bool
	dropOnResolve bool
	failBare      bool
}

func (p scriptedAnswerProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	feedback := controlFeedback(input.Decision)
	if p.failBare && feedback == "" {
		return agent.ControlAnswer{}, errors.New("the runtime needs a reason")
	}
	answer := agent.ControlAnswer{Content: []byte(`{"answer":"no"}`)}
	if p.carry {
		content, err := json.Marshal(map[string]string{"answer": "no", "reason": feedback})
		if err != nil {
			return agent.ControlAnswer{}, err
		}
		answer.Content = content
	}
	if p.followUp {
		answer.FollowUp = feedback
	}
	return answer, nil
}

func (p scriptedAnswerProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if p.dropOnResolve {
		return agent.ControlResponseResolution{Content: []byte(`{"answer":"no"}`)}
	}
	return agent.ControlResponseResolution{Content: ctx.ResponseContent}
}

func TestDeliveredControlDecision(t *testing.T) {
	t.Parallel()
	refusal := permissionDecisionOf("no", "Use the other file.")
	for _, tc := range []struct {
		name     string
		provider scriptedAnswerProvider
		decision *leapmuxv1.ControlDecision
		want     *leapmuxv1.ControlDecision
	}{
		{"a decision with no feedback", scriptedAnswerProvider{}, permissionDecisionOf("once", ""), permissionDecisionOf("once", "")},
		{"an answer that carries the feedback", scriptedAnswerProvider{carry: true}, refusal, refusal},
		{"a follow-up that carries the feedback", scriptedAnswerProvider{followUp: true}, refusal, refusal},
		{"an answer that leaves the feedback out", scriptedAnswerProvider{}, refusal, permissionDecisionOf("no", "")},
		{"a resolution that drops the feedback", scriptedAnswerProvider{carry: true, dropOnResolve: true}, refusal, permissionDecisionOf("no", "")},
		{"a second answer that fails", scriptedAnswerProvider{failBare: true}, refusal, refusal},
		{"a dismissed question that leaves its reason out", scriptedAnswerProvider{}, agenttest.CancelQuestions("User stopped"), agenttest.CancelQuestions("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := proto.CloneOf(tc.decision)
			input := agent.ControlAnswerInput{Request: agent.NewControlRequestInput([]byte(`{}`)), RequestID: "r", Decision: tc.decision}
			answer, err := tc.provider.AnswerControlRequest(input)
			require.NoError(t, err)
			meta := storedControlRequestMetadata(db.ControlRequest{RequestID: "r", Payload: []byte(`{}`)})
			requireDecision(t, tc.want, deliveredControlDecision(tc.provider, meta, input, answer))
			requireDecision(t, original, tc.decision)
		})
	}
}
