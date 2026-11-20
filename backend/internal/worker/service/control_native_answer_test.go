package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const rawClaudeAllow = `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"choice":"once","behavior":"allow"}}}`

func TestNativeControlAnswerDeliversTheMatchedPermissionOnce(t *testing.T) {
	t.Parallel()
	svc, dispatcher, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	request := &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1",
		NativeResponse: &leapmuxv1.NativeControlAnswer{Content: []byte(rawClaudeAllow)},
	}
	for range 2 {
		response, errs := answerControl(t, dispatcher, request)
		require.Empty(t, errs)
		assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.GetState())
		assert.Empty(t, response.GetError())
	}
	require.Len(t, *delivered, 1)
	assert.JSONEq(t, rawClaudeAllow, string((*delivered)[0]))
	assert.NotEqual(t, rawClaudeAllow, string((*delivered)[0]), "the Worker delivers the provider's encoding")

	saved, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{
		AgentID: request.AgentId, RequestID: request.RequestId, ClaimToken: request.ClaimToken,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(rawClaudeAllow), saved.ResponseContent)
	var decision leapmuxv1.ControlDecision
	require.NoError(t, proto.Unmarshal(saved.Decision, &decision))
	assert.Equal(t, "once", decision.GetPermission().GetOptionId())
}

func TestNativeControlAnswerRefusesUnverifiedBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		raw   string
		typed *leapmuxv1.ControlDecision
	}{
		{label: "empty"},
		{label: "malformed", raw: `{"response":`},
		{label: "wrong ID", raw: strings.Replace(rawClaudeAllow, "req-1", "req-2", 1)},
		{label: "unoffered choice", raw: strings.Replace(rawClaudeAllow, "once", "forever", 1)},
		{label: "matching ID on a request", raw: `{"jsonrpc":"2.0","id":"req-1","method":"session/prompt"}`},
		{label: "duplicate nested key", raw: strings.Replace(rawClaudeAllow, `"behavior":"allow"`, `"behavior":"deny","behavior":"allow"`, 1)},
		{label: "escaped duplicate key", raw: strings.Replace(rawClaudeAllow, `"choice":"once"`, `"cho\u0069ce":"deny","choice":"once"`, 1)},
		{label: "typed and native", raw: rawClaudeAllow, typed: permissionDecisionOf("once", "")},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
			request := &leapmuxv1.AnswerControlRequest{
				AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: tc.typed,
				NativeResponse: &leapmuxv1.NativeControlAnswer{Content: []byte(tc.raw)},
			}
			_, errs := answerControl(t, dispatcher, request)
			require.Len(t, errs, 1)
			assert.Equal(t, codeInvalidArgument, errs[0].code)
			assert.Empty(t, *delivered)
			_, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{
				AgentID: request.AgentId, RequestID: request.RequestId, ClaimToken: request.ClaimToken,
			})
			require.ErrorIs(t, err, sql.ErrNoRows)
			pending, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: request.AgentId, RequestID: request.RequestId})
			require.NoError(t, err)
			assert.Equal(t, request.ClaimToken, pending.ClaimToken)
		})
	}
}

func TestControlAnswerCannotClaimOrDeliverAChangedRequestSnapshot(t *testing.T) {
	t.Parallel()
	svc, _, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	ctx := t.Context()
	key := db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"}
	row, err := svc.Queries.GetControlRequest(ctx, key)
	require.NoError(t, err)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, key.AgentID)
	require.NoError(t, err)
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
	plan := resolveControlResponsePlan(plugin, storedControlRequestMetadata(row), []byte(rawClaudeAllow), nil)

	newPayload := []byte(strings.Replace(claudeEditRequest, "a.go", "b.go", 1))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: key.AgentID, RequestID: key.RequestID, ClaimToken: row.ClaimToken, Payload: newPayload,
	})
	claim, err := svc.Output.claimControlResponseAnswer(db.StoreControlResponseAnswerParams{
		AgentID: key.AgentID, RequestID: key.RequestID, ClaimToken: row.ClaimToken,
		RequestPayload: row.Payload, ResponseContent: []byte(rawClaudeAllow), AgentSessionID: row.AgentSessionID,
	})
	require.NoError(t, err)
	assert.Empty(t, claim, "the reservation must match the request payload")
	require.ErrorIs(t, svc.executeControlResponse(key.AgentID, dbAgent, plan), agent.ErrControlInstanceReplaced)
	assert.Empty(t, *delivered)

	claim, err = svc.Output.claimControlResponseAnswer(db.StoreControlResponseAnswerParams{
		AgentID: key.AgentID, RequestID: key.RequestID, ClaimToken: row.ClaimToken,
		RequestPayload: newPayload, ResponseContent: []byte(rawClaudeAllow), AgentSessionID: row.AgentSessionID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, claim, "the stale claim must leave the new snapshot available")
}

func TestTypedControlAnswerRejectsAChangedPayloadUnderTheSameClaim(t *testing.T) {
	t.Parallel()
	svc, dispatcher, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	queries := svc.Queries
	var requestReads int
	var replaceErr error
	newPayload := []byte(strings.Replace(claudeEditRequest, "a.go", "b.go", 1))
	svc.Queries = db.New(observedControlRead{DBTX: svc.DB, read: func(ctx context.Context, query string, args ...any) *sql.Row {
		if strings.HasPrefix(query, "-- name: GetControlRequest :one\n") {
			requestReads++
			if requestReads == 2 {
				_, replaceErr = queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "req-1", ClaimToken: "claim-1", Payload: newPayload,
				})
			}
		}
		return svc.DB.QueryRowContext(ctx, query, args...)
	}})
	response, errs := answerControl(t, dispatcher, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1", ClaimToken: "claim-1", Decision: permissionDecisionOf("once", ""),
	})
	require.GreaterOrEqual(t, requestReads, 2)
	require.NoError(t, replaceErr)
	require.Empty(t, errs)
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.GetState())
	assert.Contains(t, response.GetError(), agent.ErrControlInstanceReplaced.Error())
	assert.Empty(t, *delivered)
	row, err := queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
	require.NoError(t, err)
	assert.Equal(t, newPayload, row.Payload)
}

func TestNativeControlAnswerRequiresTheClaimToken(t *testing.T) {
	t.Parallel()
	_, dispatcher, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	_, errs := answerControl(t, dispatcher, &leapmuxv1.AnswerControlRequest{
		AgentId: "agent-1", RequestId: "req-1",
		NativeResponse: &leapmuxv1.NativeControlAnswer{Content: []byte(rawClaudeAllow)},
	})
	require.Len(t, errs, 1)
	assert.Equal(t, codeInvalidArgument, errs[0].code)
	assert.Contains(t, errs[0].message, "claim token")
	assert.Empty(t, *delivered)
}

func TestNativeControlAnswerRestoresTheExactJSONRPCID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{`9007199254740993`, `"9007199254740993"`} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			key := "jsonrpc:" + id
			requestPayload := `{"jsonrpc":"2.0","id":` + id + `,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":[{"optionId":"once","kind":"allow_once"}]}}`
			_, dispatcher, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, key, requestPayload)
			response, errs := answerControl(t, dispatcher, &leapmuxv1.AnswerControlRequest{
				AgentId: "agent-1", RequestId: key, ClaimToken: "claim-1",
				NativeResponse: &leapmuxv1.NativeControlAnswer{Content: agent.SelectedOptionResult(key, "once")},
			})
			require.Empty(t, errs)
			assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.GetState())
			require.Len(t, *delivered, 1)
			wireID, _, ok := agent.ExtractJSONRPCID((*delivered)[0])
			require.True(t, ok)
			assert.Equal(t, id, string(wireID))
		})
	}
}

func TestControlPublicationRefusesAReusedInstanceIDWithChangedPayload(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "root-control-instance")
	request := agent.ControlRequest{RequestID: "req-1", InstanceID: "claim-1", Payload: []byte(claudeEditRequest)}
	require.NoError(t, sink.PublishControlRequest(request))
	query := db.GetControlRequestParams{AgentID: "root-control-instance", RequestID: request.RequestID}
	before, err := svc.Queries.GetControlRequest(t.Context(), query)
	require.NoError(t, err)
	request.Payload = []byte(strings.Replace(claudeEditRequest, "a.go", "b.go", 1))
	require.ErrorContains(t, sink.PublishControlRequest(request), "control request instance changed")
	after, err := svc.Queries.GetControlRequest(t.Context(), query)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestDeliveredControlAnswerKeepsAChangedPendingPayload(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1", claudeEditRequest)
	query := db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"}
	newPayload := []byte(strings.Replace(claudeEditRequest, "a.go", "b.go", 1))
	svc.sendControlResponseFn = func(_ string, _ agent.RawInput) error {
		_, err := svc.Queries.StoreControlRequest(t.Context(), db.StoreControlRequestParams{
			AgentID: query.AgentID, RequestID: query.RequestID, ClaimToken: "claim-1", Payload: newPayload,
		})
		return err
	}
	response, errs := answerControl(t, dispatcher, &leapmuxv1.AnswerControlRequest{
		AgentId: query.AgentID, RequestId: query.RequestID, ClaimToken: "claim-1", Decision: permissionDecisionOf("once", ""),
	})
	require.Empty(t, errs)
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.GetState())
	pending, err := svc.Queries.GetControlRequest(t.Context(), query)
	require.NoError(t, err)
	assert.Equal(t, newPayload, pending.Payload)
}
