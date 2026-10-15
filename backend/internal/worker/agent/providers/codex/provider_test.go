package codex

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
	"github.com/stretchr/testify/assert"
)

func TestPlanApprovalOptions_PerProvider(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	// A mode that a permission preset owns selects that complete preset; every other
	// mode changes the permission mode alone.
	for _, test := range []struct {
		mode     string
		expected map[string]string
	}{
		{mode: "", expected: map[string]string{}},
		{mode: ModeAskForApproval, expected: map[string]string{agent.OptionIDPermissionMode: ModeAskForApproval}},
		{mode: ModeUnlessTrusted, expected: map[string]string{agent.OptionIDPermissionMode: ModeUnlessTrusted}},
		{mode: ModeApproveForMe, expected: map[string]string{
			agent.OptionIDPermissionMode:           ModeApproveForMe,
			contracts.CodexOptionPermissionProfile: ProfileWorkspace,
		}},
		{mode: ModeNever, expected: map[string]string{
			agent.OptionIDPermissionMode:           ModeNever,
			contracts.CodexOptionPermissionProfile: ProfileDangerFullAccess,
		}},
	} {
		test.expected[contracts.CodexOptionCollaborationMode] = CollaborationDefault
		assert.Equal(t, test.expected, provider.PlanApprovalOptions(test.mode), "permission mode %q", test.mode)
	}
	changed := provider.PlanApprovalOptions(ModeNever)
	changed[agent.OptionIDPermissionMode] = "changed"
	assert.Equal(t, ModeNever, provider.PlanApprovalOptions(ModeNever)[agent.OptionIDPermissionMode],
		"each call returns a new map")
}

// TestCodexPermissionPresetsUseOfferedValues holds each value of each permission
// preset to the static option groups, so a preset can never select a value that the
// picker does not offer.
func TestCodexPermissionPresetsUseOfferedValues(t *testing.T) {
	t.Parallel()
	for kind, preset := range contracts.CodexPermissionPresets() {
		for id, value := range preset {
			group := findStaticGroup(t, id)
			assert.Truef(t, slices.ContainsFunc(group.GetOptions(), func(o *leapmuxv1.AvailableOption) bool { return o.GetId() == value }),
				"the %s preset sets %s=%q, which the static group does not offer", kind, id, value)
		}
	}
	assert.Equal(t, ModeApproveForMe, contracts.CodexSmartOptions()[agent.OptionIDPermissionMode])
	assert.Equal(t, ModeNever, contracts.CodexBypassOptions()[agent.OptionIDPermissionMode])
}

func findStaticGroup(t *testing.T, id string) *leapmuxv1.AvailableOptionGroup {
	t.Helper()
	for _, group := range codexStaticOptionGroups {
		if group.GetId() == id {
			return group
		}
	}
	t.Fatalf("no static Codex option group %q", id)
	return nil
}

// TestSyntheticInterruptNotice pins the provider-delegated decision the raw-message
// handler consults instead of a hardcoded `== CODEX` switch, including the exact
// display text. Only Codex consumes its interrupt silently (turn/interrupt resolves
// internally with no transcript row), so only it returns the synthetic
// "[Request interrupted by user]" row text. TestOnlyCodexSynthesizesAnInterruptNotice
// pins that every other provider's interrupt surfaces in its own transcript.
func TestSyntheticInterruptNotice(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "[Request interrupted by user]", codexProvider{}.SyntheticInterruptNotice())
}

// TestCodexPlanModeControl pins Codex's reading of its own plan-mode tool name. The
// Claude name means nothing to Codex.
func TestCodexPlanModeControl(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.PlanModeControlPrompt, codexProvider{}.PlanModeControl(ToolNamePlanModePrompt))
	assert.Equal(t, agent.PlanModeControlNone, codexProvider{}.PlanModeControl(claude.ToolNameEnterPlanMode))
}

func TestProviderFor_CodexClassification(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	plugin := registry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	rateLimit := json.RawMessage(`{"method":"account/rateLimits/updated","params":{"foo":"bar"}}`)
	skillsChanged := json.RawMessage(`{"method":"skills/changed","params":{}}`)
	remoteControlStatus := json.RawMessage(`{"method":"remoteControl/status/changed","params":{"status":"disabled","environmentId":null}}`)
	startup := json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":"ready"}}`)
	contextCompactionStart := json.RawMessage(`{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"}}}`)
	commandExecutionStart := json.RawMessage(`{"method":"item/started","params":{"item":{"type":"commandExecution","id":"cmd-1"}}}`)

	assert.Equal(t, agent.NotificationClassification{
		Kind: agent.NotificationKindProviderScoped,
		Key:  "codex:account/rateLimits/updated",
	}, plugin.Classify(rateLimit))

	assert.False(t, plugin.Classify(skillsChanged).Consolidatable(),
		"skill discovery state never enters a notification thread")

	assert.False(t, plugin.Classify(remoteControlStatus).Consolidatable(),
		"remote-control state never enters a notification thread")

	assert.Equal(t, agent.NotificationClassification{
		Kind: agent.NotificationKindProviderScoped,
		Key:  "codex:mcpServer/startupStatus/updated:codex_apps",
	}, plugin.Classify(startup))

	assert.Equal(t, agent.NotificationClassification{
		Kind: agent.NotificationKindStatus,
		Key:  "codex:item/started:contextCompaction",
	}, plugin.Classify(contextCompactionStart),
		"item/started for a contextCompaction item is the in-progress compacting indicator")

	assert.False(t, plugin.Classify(commandExecutionStart).Consolidatable(),
		"item/started for non-contextCompaction items must NOT be classified as a notification — those go through PersistMessage as AGENT spans")

	contextCompactionEnd := json.RawMessage(`{"method":"item/completed","params":{"item":{"type":"contextCompaction","id":"compact-1"}}}`)
	agentMessageEnd := json.RawMessage(`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"msg-1"}}}`)
	threadCompacted := json.RawMessage(`{"method":"thread/compacted","params":{"threadId":"t1"}}`)

	assert.Equal(t, agent.NotificationClassification{
		Kind: agent.NotificationKindCompactionBoundary,
		Key:  "codex:item/completed:contextCompaction",
	}, plugin.Classify(contextCompactionEnd),
		"item/completed for a contextCompaction item is the boundary that ends the compacting indicator")

	assert.False(t, plugin.Classify(agentMessageEnd).Consolidatable(),
		"item/completed for every other item type goes through PersistMessage as an AGENT span")

	assert.False(t, plugin.Classify(threadCompacted).Consolidatable(),
		"thread/compacted persists as a plain threadable notification; item/completed is the boundary")
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
