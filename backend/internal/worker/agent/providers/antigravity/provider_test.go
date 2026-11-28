package antigravity

import (
	"encoding/json"
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
	request := []byte(`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":2}`)
	response := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`)
	resolution := agenttest.ResolveAnswer(provider, request, "antigravity:cascade:trajectory:2", agent.ControlAnswer{Content: response})
	assert.False(t, resolution.Withhold)
	assert.Equal(t, response, resolution.Content)
	assert.Equal(t, "antigravity:cascade:trajectory:2", provider.ControlResponseRequestID(response))
	// An answer that states no whole address addresses no request of a step.
	for _, content := range []string{
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"","stepIndex":2}}`,
		`{"cascadeId":"","interaction":{"trajectoryId":"trajectory","stepIndex":2}}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":-1}}`,
		`{"cascadeId":"cas:cade","interaction":{"trajectoryId":"trajectory","stepIndex":2}}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"traj:ectory","stepIndex":2}}`,
		`{"cascadeId":"cascade","interaction":null}`,
		`{"cascadeId":"cascade","interaction":"not-an-object"}`,
		`{"cascadeId":"cascade","interaction":[]}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":1.5}}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":"2"}}`,
	} {
		assert.Empty(t, provider.ControlResponseRequestID([]byte(content)), content)
	}
	// stepIndex 0 is valid and must be read back.
	stepZero := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":0,"permission":{"allow":true}}}`)
	assert.Equal(t, "antigravity:cascade:trajectory:0", provider.ControlResponseRequestID(stepZero))
}

// The key of a stored interaction is the address of its step, so a payload of
// another cascade, trajectory or step gives another key, and the Worker
// refuses the row before the provider reads it
// (agent.StoredControlRequestAgrees).
func TestAntigravityControlRequestKeyIsTheAddressOfTheStep(t *testing.T) {
	const requestID = "antigravity:cascade:trajectory:2"
	for payload, same := range map[string]bool{
		`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":2}`: true,
		`{"cascadeId":"other","trajectoryId":"trajectory","stepIndex":2}`:   false,
		`{"cascadeId":"cascade","trajectoryId":"other","stepIndex":2}`:      false,
		`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":3}`: false,
	} {
		key, ok := antigravityProvider{}.ControlRequestKey([]byte(payload), "cascade")
		require.True(t, ok, payload)
		assert.Equal(t, same, key == requestID, payload)
	}
	key, ok := antigravityProvider{}.ControlRequestKey(antigravityPlanReviewPayload(t, "# Plan"), "cascade")
	assert.True(t, ok)
	assert.Equal(t, antigravityPlanReviewKey, key, "the plan review states its own key")
}

// A stored request that states no whole address gives no key, so the Worker
// refuses every answer to it. A plan review that states no key gives none
// either.
func TestAntigravityControlRequestKeyOfARequestThatStatesNoAddress(t *testing.T) {
	for _, payload := range []string{
		`{}`, `{"request":{"tool_name":"run_command"}}`, `{"cascadeId":""}`, `{"cascadeId":7}`,
		`{"cascadeId":"cascade","trajectoryId":"trajectory"}`,
		`{"cascadeId":"cascade","trajectoryId":"","stepIndex":2}`,
		`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":-1}`,
		`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":1.5}`,
		`{"cascadeId":"cascade","trajectoryId":"trajectory","stepIndex":"2"}`,
		`{"request":{"tool_name":"AntigravityPlanReview"}}`,
	} {
		key, ok := antigravityProvider{}.ControlRequestKey([]byte(payload), "cascade")
		assert.False(t, ok, payload)
		assert.Empty(t, key, payload)
	}
}

// The language server reads only its own answer, so LeapMux refuses the
// shared envelope with the shared reason for an answer that it cannot read.
func TestAntigravityRefusesAnAnswerThatIsNotNative(t *testing.T) {
	const requestID = "antigravity:cascade:trajectory:4"
	payload := antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{}})
	for _, content := range [][]byte{agent.AllowResponse(requestID, json.RawMessage(`{}`)), agent.DenyResponse(requestID, "")} {
		resolution := agenttest.ResolveAnswer(antigravityProvider{}, payload, requestID, agent.ControlAnswer{Content: content})
		assert.True(t, resolution.Withhold, string(content))
		assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableAnswer, string(content))
	}
	malformed := [][]byte{
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":null,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":-1,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":1.5,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":"4","permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"","interaction":{"trajectoryId":"trajectory","stepIndex":4,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cas:cade","interaction":{"trajectoryId":"trajectory","stepIndex":4,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"","stepIndex":4,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"traj:ectory","stepIndex":4,"permission":{"allow":true}}}`),
		[]byte(`{"cascadeId":"cascade","interaction":null}`),
		[]byte(`{"cascadeId":"cascade","interaction":"not-an-object"}`),
		[]byte(`{"cascadeId":"cascade","interaction":[]}`),
		[]byte(`{"cascadeId":"cascade","interaction":42}`),
	}
	for _, content := range malformed {
		resolution := antigravityProvider{}.ResolveControlResponse(agenttest.ControlResponseContextOf(payload, requestID, agent.ControlAnswer{Content: content}))
		assert.True(t, resolution.Withhold, string(content))
		assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableAnswer, string(content))
	}
}
