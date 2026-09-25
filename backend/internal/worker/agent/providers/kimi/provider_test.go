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
		`{"type":"turn.step.retrying","attempt":2}`: {Kind: agent.NotificationKindAPIRetry, Key: "kimi:retry"},
		`{"type":"compaction.started"}`:             {Kind: agent.NotificationKindStatus, Key: "kimi:compaction"},
		`{"type":"compaction.blocked"}`:             {Kind: agent.NotificationKindStatus, Key: "kimi:compaction"},
		`{"type":"compaction.cancelled"}`:           {Kind: agent.NotificationKindStatus, Key: "kimi:compaction"},
		`{"type":"compaction.completed"}`:           {Kind: agent.NotificationKindCompactionBoundary, Key: "kimi:compaction"},
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
	assert.Equal(t, agent.PlanModeControlEnter, p.PlanModeControl(contracts.KimiToolEnterPlanMode))
	assert.Equal(t, agent.PlanModeControlExit, p.PlanModeControl(contracts.KimiToolExitPlanMode))
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl(contracts.KimiToolBash))

	assert.Equal(t, contracts.KimiModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.KimiDefaultMode, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlPrompt))
}

func TestKimiProviderFacts(t *testing.T) {
	t.Parallel()

	assert.True(t, kimiProvider{}.SupportsChildSteering(), "a subagent's tab sends it messages")
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
