package antigravity

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityTokenResumeRule(t *testing.T) {
	agenttest.AssertTokenResumeRule(t, antigravityProvider{})
}

func TestAntigravityPreservesResponseWithoutARequest(t *testing.T) {
	agenttest.AssertPreservesTheResponseWithoutARequest(t, antigravityProvider{})
}

func TestAntigravityWithholdsResponseForAMalformedRequest(t *testing.T) {
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, antigravityProvider{})
}

func TestAntigravityChildCapabilities(t *testing.T) {
	agenttest.AssertChildCapabilities(t, antigravityProvider{}, (*Agent)(nil))
}

func TestAntigravityPlanApprovalOptions(t *testing.T) {
	provider := antigravityProvider{}
	assert.Equal(t, map[string]string{agent.OptionIDPermissionMode: contracts.AntigravityModeDefault}, provider.PlanApprovalOptions(""))
	assert.Equal(t, map[string]string{agent.OptionIDPermissionMode: contracts.AntigravityModeAcceptEdits}, provider.PlanApprovalOptions(contracts.AntigravityModeAcceptEdits))
}

// The Worker raises the plan review itself, so no Antigravity request waits
// for its answer.
func TestAntigravityPlanControlIsLeapMuxOwned(t *testing.T) {
	provider := antigravityProvider{}
	payload := antigravityPlanReviewPayload(t, "# Plan")
	answer, err := agenttest.AnswerControl(provider, payload, "antigravity-plan:review-1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(provider, payload, "antigravity-plan:review-1", answer)
	assert.True(t, resolution.LeapMuxOwned)
	assert.Equal(t, agent.PlanModeControlPrompt, resolution.PlanModeControl)
}

func TestAntigravityControlResponseMatchesTheCascade(t *testing.T) {
	provider := antigravityProvider{}
	request := []byte(`{"cascadeId":"cascade"}`)
	response := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`)
	resolution := agenttest.ResolveAnswer(provider, request, "antigravity:cascade:trajectory:2", agent.ControlAnswer{Content: response})
	assert.False(t, resolution.Withhold)
	assert.Equal(t, response, resolution.Content)
	assert.Equal(t, "antigravity:cascade:trajectory:2", provider.ControlResponseRequestID(response))
	assert.Empty(t, provider.ControlResponseRequestID([]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"","stepIndex":2}}`)))
}

// A stored request whose cascade is not the cascade of its key disagrees with
// that key, so LeapMux cannot read the request that the answer is for.
func TestAntigravityRefusesARequestThatDisagreesWithItsKey(t *testing.T) {
	const requestID = "antigravity:cascade:trajectory:2"
	response, err := antigravityPermissionAnswer(requestID, true, "")
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(antigravityProvider{}, []byte(`{"cascadeId":"other"}`), requestID, agent.ControlAnswer{Content: response})
	assert.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableRequest)
}
