package service

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

func TestControlResponsePreservesNativeIDsThroughStorageAndClaims(t *testing.T) {
	for _, wireID := range []string{`0`, `9007199254740993`, `"001"`, `"42"`} {
		t.Run(wireID, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`, wireID))
			_, requestID, valid := agent.ExtractJSONRPCID(request)
			require.True(t, valid)
			sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: request}))
			stored, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: requestID})
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"decision":"accept"}}`, requestID))
			content, forward, err := processControlResponseForTest(svc, "agent-1", row, response, stored.ClaimToken)
			require.NoError(t, err)
			require.True(t, forward)
			var actual map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(content, &actual))
			require.Equal(t, wireID, string(actual["id"]))
			_, forward, err = processControlResponseForTest(svc, "agent-1", row, response, stored.ClaimToken)
			require.NoError(t, err)
			require.False(t, forward)
		})
	}
}

func TestControlResponseKeepsConcurrentNumericAndStringRequestsSeparate(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	for _, nativeID := range []string{`7`, `"7"`} {
		identity, ok := agent.NewControlRequestIdentity(json.RawMessage(nativeID))
		requestID := identity.Key
		require.True(t, ok)
		request := []byte(fmt.Sprintf(`{"id":%s,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`, nativeID))
		require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: request}))
	}
	requests, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 2)
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	for index, request := range requests {
		response := []byte(fmt.Sprintf(`{"id":%q,"result":{"decision":"accept","count":0,"enabled":false}}`, request.RequestID))
		content, forward, err := processControlResponseForTest(svc, "agent-1", row, response, request.ClaimToken)
		require.NoError(t, err)
		require.True(t, forward)
		var original, restored map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(request.Payload, &original))
		require.NoError(t, json.Unmarshal(content, &restored))
		require.Equal(t, original["id"], restored["id"])
		require.JSONEq(t, `{"decision":"accept","count":0,"enabled":false}`, string(restored["result"]))
		remaining, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
		require.NoError(t, err)
		require.Len(t, remaining, len(requests)-index-1)
	}
}
