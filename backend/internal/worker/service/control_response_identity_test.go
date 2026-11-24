package service

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestControlResponsePreservesNativeIDsThroughStorageAndClaims(t *testing.T) {
	for _, wireID := range []string{`0`, `9007199254740993`, `"001"`, `"42"`} {
		t.Run(wireID, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`, wireID))
			// The provider publishes a JSON-RPC request under its canonical key.
			identity, valid := agent.JSONRPCControlRequestIdentity(request)
			require.True(t, valid)
			requestID := identity.Key
			sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: request}))
			stored := requireLookupControlRequest(t, t.Context(), svc.Output, "agent-1", requestID)
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"decision":"accept"}}`, requestID))
			content, forward, err := processControlAnswerForTest(svc, row, response, stored.ClaimToken)
			require.NoError(t, err)
			require.True(t, forward)
			var actual map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(content, &actual))
			require.Equal(t, wireID, string(actual["id"]))
			_, forward, err = processControlAnswerForTest(svc, row, response, stored.ClaimToken)
			require.NoError(t, err)
			require.False(t, forward)
		})
	}
}

func TestControlResponseKeepsConcurrentNumericAndStringRequestsSeparate(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	for _, nativeID := range []string{`7`, `"7"`} {
		identity, ok := agent.NewControlRequestIdentity(json.RawMessage(nativeID))
		requestID := identity.Key
		require.True(t, ok)
		request := []byte(fmt.Sprintf(`{"id":%s,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`, nativeID))
		require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: request}))
	}
	requests, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, requests, 2)
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	for index, request := range requests {
		response := []byte(fmt.Sprintf(`{"id":%q,"result":{"decision":"accept","count":0,"enabled":false}}`, request.RequestID))
		content, forward, err := processControlAnswerForTest(svc, row, response, request.ClaimToken)
		require.NoError(t, err)
		require.True(t, forward)
		var original, restored map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(request.Payload, &original))
		require.NoError(t, json.Unmarshal(content, &restored))
		require.Equal(t, original["id"], restored["id"])
		require.JSONEq(t, `{"decision":"accept","count":0,"enabled":false}`, string(restored["result"]))
		remaining, err := svc.Output.ListControlRequests(t.Context(), "agent-1")
		require.NoError(t, err)
		require.Len(t, remaining, len(requests)-index-1)
	}
}
