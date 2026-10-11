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
func TestSendControlResponseStatesTheReasonOfARefusal(t *testing.T) {
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
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			sends := 0
			svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }

			response, err := svc.respondToControlRequest(row, &leapmuxv1.SendControlResponseRequest{
				RequestId: "request", ClaimToken: "claim",
				Content: []byte(`{"id":"request","result":{"decision":"accept"}}`),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, response.GetError())
			assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.GetState(),
				"a refused answer leaves the request open for another answer")
			assert.Zero(t, sends, "a refused answer reaches no agent")
		})
	}
}

// The reason travels from the provider that refuses. Factory Droid refuses an
// option that its request did not offer, and the response states that option.
