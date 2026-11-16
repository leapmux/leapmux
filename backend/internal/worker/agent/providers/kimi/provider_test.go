package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiClassify(t *testing.T) {
	t.Parallel()

	for payload, want := range map[string]agent.NotificationClassification{
		`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","ops":[{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"running","retry":{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":0}}}]}}`: agent.APIRetryNotification(),
		`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","ops":[{"op":"marker.upsert","item":{"kind":"marker","markerId":"m1","marker":"compaction","payload":{"phase":"completed"}}}]}}`:                                                agent.CompactionBoundaryNotification(),
		`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","ops":[{"op":"marker.upsert","item":{"kind":"marker","markerId":"m1","marker":"compaction","payload":{"phase":"cancelled"}}}]}}`:                                                agent.CompactionBoundaryNotification(),
		`{"type":"transcript.ops","payload":{"ops":[{"op":"step.upsert","step":{"retry":{"nextAttempt":2}}}]}}`:                                                                                                                                                                                 {},
		`{"type":"turn.step.retrying","attempt":2}`: {},
		`{"type":"compaction.started"}`:             {},
		`{"type":"compaction.blocked"}`:             {},
		`{"type":"compaction.cancelled"}`:           {},
		`{"type":"compaction.completed"}`:           {},
		`{"type":"warning"}`:                        {},
		`{"type":"turn.started"}`:                   {},
		`{}`:                                        {},
		`not json`:                                  {},
	} {
		assert.Equal(t, want, kimiProvider{}.Classify([]byte(payload)), payload)
	}
}

func TestKimiIsInterrupt(t *testing.T) {
	t.Parallel()

	assert.True(t, kimiProvider{}.IsInterrupt(`{"action":"abort"}`))
	assert.False(t, kimiProvider{}.IsInterrupt(`{"type":"abort"}`))
	assert.False(t, kimiProvider{}.IsInterrupt(``))
}

func TestKimiPlanModeControl(t *testing.T) {
	t.Parallel()

	p := kimiProvider{}
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl("EnterPlanMode"), "the native runtime requires the user to enter Plan mode")
	assert.Equal(t, agent.PlanModeControlExit, p.PlanModeControl(contracts.KimiToolExitPlanMode))
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl(contracts.KimiToolBash))

	assert.Equal(t, contracts.KimiModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.KimiDefaultMode, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlPrompt))
}

func TestKimiProviderFacts(t *testing.T) {
	t.Parallel()

	assert.True(t, kimiProvider{}.ChildCapabilities(nil).AcceptsMessages, "a subagent's tab sends it messages")
	agenttest.AssertTokenResumeRule(t, kimiProvider{})

	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, registration.Provider)
	assert.IsType(t, kimiProvider{}, registration.Plugin)
	assert.NotNil(t, registration.Start)
	assert.Equal(t, kimiStaticOptionGroups, registration.OptionGroups, "the registration and a running agent state the same static axes")
	assert.Equal(t, []string{agent.OptionIDEffort}, registration.AdditionalOptionIDs,
		"the effort axis is read from the running server, so no static group states it")
	assert.Equal(t, kimiLocator, registration.Locator)
	assert.True(t, registration.ManagesEffort)
	assert.True(t, registration.FixedPermissionModes)
	assert.Equal(t, contracts.KimiDefaultMode, registration.PermissionDefaults.Fallback)
	assert.Equal(t, kimiSwarmOff, registration.ProviderOptionDefaults[kimiOptionSwarmMode])
	assert.Equal(t, "LEAPMUX_KIMI_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_KIMI_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.Nil(t, registration.DefaultModels, "the catalog is the user's own and read from the server")
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestKimiChildInterruptionRequiresAnUnavailableNativeTurnAddress(t *testing.T) {
	t.Parallel()
	assert.False(t, kimiProvider{}.ChildCapabilities(nil).AcceptsInterrupt, "task service addresses do not identify the current child turn")
	_, implements := any((*Agent)(nil)).(agent.ChildInterrupter)
	assert.False(t, implements, "the Worker must use its unsupported-operation path rather than guessing a coordinator")
}
