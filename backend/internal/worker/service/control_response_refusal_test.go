package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// refusingProvider wraps one provider and refuses every answer with reason.
type refusingProvider struct {
	agent.Provider
	reason string
}

func (p refusingProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := p.Provider.ResolveControlResponse(ctx)
	result.Refuse(p.reason)
	return result
}

// A refused answer states why in the error of the response, which the browser
// shows after "The response was not sent: ". A provider that gives no reason
// states the generic text, which only the agent package spells.
func TestDeliverControlAnswerStatesTheReasonOfARefusal(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{"a reason", "the test refuses this answer", "the test refuses this answer"},
		{"no reason", "", agent.ControlResponseRefusedText},
		{"a blank reason", "   ", agent.ControlResponseRefusedText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
			plugin := refusingProvider{Provider: testRegistry.Plugin(provider), reason: tc.reason}
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, provider, plugin)))
			createClaimTestAgent(t, svc, "agent-1")
			createTestControlRequest(t, t.Context(), svc.Output, testControlRequestParams{
				AgentID: "agent-1", RequestID: "jsonrpc:1", ClaimToken: "claim",
				Payload: []byte(`{"id":1,"method":"item/commandExecution/requestApproval"}`),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			sends := 0
			svc.sendControlResponseFn = func(string, agent.RawInput) error { sends++; return nil }

			state, errText, err := svc.deliverControlAnswer(row, "jsonrpc:1", workerAnswer{
				claimToken: "claim",
				content:    []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, errText)
			assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, state,
				"a refused answer leaves the request open for another answer")
			assert.Zero(t, sends, "a refused answer reaches no agent")
		})
	}
}

// The reason travels from the provider that refuses. Factory Droid refuses an
// option that its request did not offer, and the response states that option.
func TestDeliverControlAnswerStatesTheReasonOfAProviderRefusal(t *testing.T) {
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID,
	}))
	createTestControlRequest(t, t.Context(), svc.Output, testControlRequestParams{
		AgentID: "agent-1", RequestID: "droid-perm-1", ClaimToken: "claim",
		Payload: []byte(`{"type":"permission_request","requestId":"droid-perm-1","rpcId":"rpc-1",` +
			`"toolUse":{"type":"tool_use","id":"1","name":"Edit","input":{}},"confirmationType":"edit",` +
			`"options":["proceed_once","cancel"]}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	sends := 0
	svc.sendControlResponseFn = func(string, agent.RawInput) error { sends++; return nil }

	state, errText, err := svc.deliverControlAnswer(row, "droid-perm-1", workerAnswer{
		claimToken: "claim",
		content:    []byte(`{"response":{"request_id":"droid-perm-1","response":{"behavior":"allow","selectedOption":"proceed_always"}}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "Factory Droid did not offer proceed_always", errText)
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, state)
	assert.Zero(t, sends)
}
