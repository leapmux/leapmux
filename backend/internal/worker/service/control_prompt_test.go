package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const claudeEditRequest = `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{"file_path":"/w/a.go"}}}`

// answerControlFixture is an agent of provider with one pending control
// request, and a seam that records what the Worker delivers to the agent.
func answerControlFixture(t *testing.T, provider leapmuxv1.AgentProvider, requestID, payload string) (*Service, *testServiceDispatcher, *[][]byte) {
	t.Helper()
	svc, d, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: provider,
	}))
	createTestControlRequest(t, context.Background(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: requestID, ClaimToken: "claim-1", Payload: []byte(payload),
	})
	delivered := &[][]byte{}
	svc.sendControlResponseFn = func(_ string, data []byte) error {
		*delivered = append(*delivered, append([]byte(nil), data...))
		return nil
	}
	return svc, d, delivered
}

// answerControl dispatches AnswerControl. It returns the response, or the
// errors that the handler sent instead.
func answerControl(t *testing.T, d *testServiceDispatcher, request *leapmuxv1.AnswerControlRequest) (*leapmuxv1.AnswerControlResponse, []testError) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, request, w)
	if len(w.errors) > 0 {
		return nil, w.errors
	}
	require.Len(t, w.responses, 1)
	var response leapmuxv1.AnswerControlResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &response))
	return &response, nil
}

func permissionDecisionOf(optionID, feedback string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{Permission: &leapmuxv1.PermissionDecision{OptionId: optionID, Feedback: feedback}}}
}

// recordedControlPrompt reads the prompt that the persisted response row
// states in its metadata.
func recordedControlPrompt(t *testing.T, svc *Service) *leapmuxv1.ControlPrompt {
	t.Helper()
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
	require.NoError(t, err)
	rows = controlResponseRows(rows)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, workerdb.StorageEnumValue(rows[0].MarkType))
	supplement, err := msgcodec.Decompress(rows[0].SupplementalContent, rows[0].SupplementalContentCompression)
	require.NoError(t, err)
	var fields struct {
		Metadata struct {
			Prompt json.RawMessage `json:"control_prompt"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(supplement, &fields))
	require.NotEmpty(t, fields.Metadata.Prompt, "the row states the prompt that the answer belongs to")
	var prompt leapmuxv1.ControlPrompt
	require.NoError(t, protojson.Unmarshal(fields.Metadata.Prompt, &prompt))
	return &prompt
}

func TestAnswerControl_DeliversTheProvidersAnswerAndRecordsThePrompt(t *testing.T) {
	t.Parallel()
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)

	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: permissionDecisionOf("once", ""),
	})
	require.Empty(t, errs)
	assert.Empty(t, response.GetError())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.GetState())
	require.Len(t, *delivered, 1, "the Worker delivers one answer to the agent")
	_, behavior, _, ok := agent.DecodeControlBehavior((*delivered)[0])
	require.True(t, ok)
	assert.Equal(t, contracts.ControlAnswerBehaviorAllow, behavior)

	prompt := recordedControlPrompt(t, svc)
	require.NotNil(t, prompt.GetPermission())
	assert.Equal(t, "Edit", prompt.GetPermission().GetTitle())
	_, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
	assert.Error(t, err, "the answered request is no longer pending")
}

func TestAnswerControl_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	t.Parallel()
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)

	_, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: permissionDecisionOf("forever", ""),
	})
	require.Len(t, errs, 1)
	assert.Equal(t, codeInvalidArgument, errs[0].code)
	assert.Contains(t, errs[0].message, `no option "forever"`)
	assert.Empty(t, *delivered)
	_, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
	require.NoError(t, err, "a refused decision leaves the request pending")

	_, errs = answerControl(t, d, &leapmuxv1.AnswerControlRequest{AgentId: "agent-1", ClaimToken: "claim-1", Decision: permissionDecisionOf("once", "")})
	require.Len(t, errs, 1, "an answer must identify its request")
	assert.Equal(t, codeInvalidArgument, errs[0].code)
}

// The claim token identifies the request instance. An answer from a card of
// a retired instance reports that instance's state and changes nothing, as
// RecordControlResponse does.
func TestAnswerControl_AnswersARetiredInstanceWithItsState(t *testing.T) {
	t.Parallel()
	_, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)

	for _, request := range []*leapmuxv1.AnswerControlRequest{
		{AgentId: "agent-1", RequestId: "req-1", ClaimToken: "old-claim", Decision: permissionDecisionOf("once", "")},
		{AgentId: "agent-1", RequestId: "gone", ClaimToken: "claim-1", Decision: permissionDecisionOf("once", "")},
	} {
		response, errs := answerControl(t, d, request)
		require.Empty(t, errs)
		assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, response.GetState(), request.GetRequestId())
	}
	assert.Empty(t, *delivered)
}

// The Agent Client Protocol selects an option, which carries no text, so the
// reason of a refusal follows the answer as a message: the Worker stores it
// with the answer and queues it when it finalizes the answer.
func TestAnswerControl_StoresTheFollowUpOfARefusal(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}`
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "jsonrpc:7", payload)

	response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "jsonrpc:7", ClaimToken: "claim-1", Decision: permissionDecisionOf("no", "Use the other file."),
	})
	require.Empty(t, errs)
	assert.Empty(t, response.GetError())
	require.Len(t, *delivered, 1)
	assert.Contains(t, string((*delivered)[0]), `"optionId":"no"`)

	answer, err := svc.Queries.GetControlResponseAnswer(context.Background(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "jsonrpc:7", ClaimToken: "claim-1"})
	require.NoError(t, err)
	assert.Equal(t, "Use the other file.", answer.FollowUp)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, answer.FollowUpKind)
}

func TestListControlRequests_ListsEachPendingRequestWithItsPrompt(t *testing.T) {
	t.Parallel()
	_, d, _ := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var listed leapmuxv1.ListControlRequestsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &listed))
	require.Len(t, listed.GetRequests(), 1)
	request := listed.GetRequests()[0]
	assert.Equal(t, "req-1", request.GetRequestId())
	assert.Equal(t, "claim-1", request.GetClaimToken())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, request.GetResponseState())
	assert.Equal(t, "Edit", request.GetPrompt().GetPermission().GetTitle(), "the list carries the prompt that the provider read")
}

// A payload that is not a JSON object reaches no provider: the list states
// its fault, and an answer is refused before anything reaches the agent.
func TestAnswerControl_RefusesARequestThatLeapMuxCannotRead(t *testing.T) {
	t.Parallel()
	svc, d, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", `not json`)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)
	var listed leapmuxv1.ListControlRequestsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &listed))
	require.Len(t, listed.GetRequests(), 1)
	assert.Equal(t, leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED, listed.GetRequests()[0].GetPrompt().GetUnreadable())

	_, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: permissionDecisionOf(contracts.ControlAnswerOptionAllow, ""),
	})
	require.Len(t, errs, 1)
	assert.Equal(t, codeInvalidArgument, errs[0].code)
	assert.Contains(t, errs[0].message, "cannot read this request")
	assert.Empty(t, *delivered)
	_, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
	require.NoError(t, err, "the provider still waits, so the request stays")
}

// A provider that returns no prompt still gives a client one to draw.
func TestControlRequestPrompt_FallsBackToTheGenericPrompt(t *testing.T) {
	t.Parallel()
	prompt := controlRequestPrompt(nil, emptyPromptProvider{}, "agent-1", agent.ControlRequest{Payload: []byte(`{}`)})
	assert.True(t, proto.Equal(agent.WithEditorPurpose(agent.GenericPermissionPrompt()), prompt), "the generic prompt, with its editor purpose stated")
	assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK, prompt.GetEditorPurpose())
}

type emptyPromptProvider struct{ agent.ProviderDefaults }

func (emptyPromptProvider) ReadControlRequest(agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{}
}
