package antigravity

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
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

func TestAntigravityPlanControlIsLeapMuxOwned(t *testing.T) {
	provider := antigravityProvider{}
	resolution := provider.ResolveControlResponse(agent.ControlResponseContext{ToolName: contracts.AntigravityControlToolPlanReview})
	assert.True(t, resolution.LeapMuxOwned)
	assert.Equal(t, agent.PlanModeControlPrompt, resolution.PlanModeControl)
}

func TestAntigravityControlResponseMatchesTheCascade(t *testing.T) {
	provider := antigravityProvider{}
	request := []byte(`{"cascadeId":"cascade"}`)
	response := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`)
	resolution := provider.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: response})
	assert.False(t, resolution.Withhold)
	assert.Equal(t, response, resolution.Content)
	assert.Equal(t, "antigravity:cascade:trajectory:2", provider.ControlResponseRequestID(response))
	assert.Empty(t, provider.ControlResponseRequestID([]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"","stepIndex":2}}`)))
}
