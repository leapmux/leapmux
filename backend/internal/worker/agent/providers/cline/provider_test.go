package cline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func notice(kind, phase string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"event":   contracts.ClineEventSessionNotice,
		"payload": map[string]any{"message": kind, "metadata": map[string]any{"kind": kind, "phase": phase}},
	})
	return data
}

func TestClassifyGroupsTheNoticesThatRepeat(t *testing.T) {
	t.Parallel()
	p := clineProvider{}
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want agent.NotificationClassification
	}{
		{"compaction start", notice(contracts.ClineNoticeKindAutoCompaction, contracts.ClineNoticePhaseStarted), agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: noticeKey}},
		{"compaction skip", notice(contracts.ClineNoticeKindManualCompaction, contracts.ClineNoticePhaseSkipped), agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: noticeKey}},
		{"compaction end", notice(contracts.ClineNoticeKindAutoCompaction, contracts.ClineNoticePhaseCompleted), agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: noticeKey}},
		{"overflow compaction end", notice(contracts.ClineNoticeKindOverflowRecoveryCompaction, contracts.ClineNoticePhaseCompleted), agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: noticeKey}},
		{"retry", notice(contracts.ClineNoticeKindProviderErrorRetry, contracts.ClineNoticePhaseStarted), agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "cline:retry"}},
		{"other status", notice(contracts.ClineNoticeKindMaxTokensRecovery, contracts.ClineNoticePhaseFailed), agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "cline:status"}},
		{"team run", json.RawMessage(`{"event":"team.progress","payload":{"lastEvent":{"runId":"r1"}}}`), agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: teamRowPrefix + "r1"}},
		{"team event with no run", json.RawMessage(`{"event":"team.progress","payload":{"lastEvent":{}}}`), agent.NotificationClassification{}},
		{"another row", json.RawMessage(`{"event":"assistant.finished","payload":{}}`), agent.NotificationClassification{}},
		{"not json", json.RawMessage(`nope`), agent.NotificationClassification{}},
	} {
		assert.Equal(t, tc.want, p.Classify(tc.raw), tc.name)
	}
}

func TestPlanModeWords(t *testing.T) {
	t.Parallel()
	p := clineProvider{}
	assert.Equal(t, agent.PlanModeControlExit, p.PlanModeControl(contracts.ClineToolSwitchToActMode))
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl("editor"))
	assert.Equal(t, contracts.ClinePermissionModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.ClinePermissionModeAct, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlPrompt))
}

func TestClineIsNoInterruptFrame(t *testing.T) {
	t.Parallel()
	assert.False(t, clineProvider{}.IsInterrupt(`{"command":"run.abort"}`), "a hub command interrupts Cline, never a frame")
}

func TestTheTurnEndStatesItsToolCount(t *testing.T) {
	t.Parallel()
	count, ok := clineProvider{}.TurnEndToolUses([]byte(`{"event":"run.completed","num_tool_uses":3}`))
	assert.True(t, ok)
	assert.EqualValues(t, 3, count)
	_, ok = clineProvider{}.TurnEndToolUses([]byte(`{"event":"run.completed"}`))
	assert.False(t, ok, "a row with no count states none")
}

func TestResumeHandlesAreTokens(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

func TestRegistration(t *testing.T) {
	t.Parallel()
	reg := Registration()
	assert.True(t, reg.Locator.Valid())
	assert.True(t, reg.FixedPermissionModes)
	assert.True(t, reg.ManagesEffort)
	assert.Equal(t, contracts.ClinePermissionModeAct, reg.PermissionDefaults.Fallback)
	assert.Equal(t, map[string]string{agent.OptionIDPermissionMode: contracts.ClinePermissionModeAct}, reg.PermissionDefaults.NewSession)
	assert.Equal(t, agent.DefaultModelSentinel, reg.DefaultModel())
	assert.Contains(t, reg.AdditionalOptionIDs, agent.OptionIDEffort)
	// The worker sweeps the directories of ended workers, and the hook of a
	// stale one ends the daemon that it records. Cline binds no socket there.
	require.NotNil(t, reg.AgentDir)
	assert.Equal(t, "cline", reg.AgentDir.Prefix)
	assert.Empty(t, reg.AgentDir.SocketName)
	assert.NotNil(t, reg.AgentDir.OnStale)
	require.NoError(t, reg.AgentDir.Validate())
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
