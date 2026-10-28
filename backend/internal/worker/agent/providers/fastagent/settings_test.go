package fastagent

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func newFastAgentSettingsSubject(t *testing.T, bypass bool) (*Agent, func() []agenttest.RecordedRequest) {
	t.Helper()
	a, requests := acptest.NewAgentForRPC(t, func() *Agent { return &Agent{bypassPermissions: bypass} }, func(a *Agent) *acp.Base { return &a.Base })
	sink := agent.NewProviderServices(&agenttest.ControlSink{})
	a.SetSinkForTest(sink)
	*a.HooksForTest() = a.hooks("model-a", sink)
	a.SetModelForTest("model-a")
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "model-a"}, {Id: "model-b"}})
	return a, requests
}

func TestFastAgentBypassGroupReportsTheNativeLaunchChoice(t *testing.T) {
	t.Parallel()
	a, _ := newFastAgentSettingsSubject(t, false)
	group := optionids.GroupByID(a.OptionGroups(), "bypassPermissions")
	require.NotNil(t, group)
	assert.True(t, group.Mutable)
	assert.Equal(t, "false", group.CurrentValue)
	require.Len(t, group.Options, 2)
	assert.Equal(t, "false", group.Options[0].Id)
	assert.Equal(t, "true", group.Options[1].Id)
	assert.Equal(t, "false", a.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
}

func TestFastAgentBypassChangesRequireRestartBeforeOtherWrites(t *testing.T) {
	t.Parallel()
	a, requests := newFastAgentSettingsSubject(t, false)
	result := a.UpdateSettings(map[string]string{"bypassPermissions": "true", "model": "model-b"})
	assert.False(t, result.AppliedLive)
	assert.Empty(t, requests(), "no live change may precede a required restart")
	assert.Equal(t, "model-a", a.SettingsSnapshot().ConfirmedOptions()["model"])
	assert.Equal(t, "false", a.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
}

func TestFastAgentBypassOnSnapshotAndRestart(t *testing.T) {
	t.Parallel()
	a, requests := newFastAgentSettingsSubject(t, true)
	assert.Equal(t, "true", a.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
	result := a.UpdateSettings(map[string]string{"bypassPermissions": "false", "model": "model-b"})
	assert.False(t, result.AppliedLive)
	assert.Empty(t, requests())
	assert.Equal(t, "true", a.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
	assert.Equal(t, "model-a", a.SettingsSnapshot().ConfirmedOptions()["model"])
}

func TestFastAgentBypassInvalidLiveValuesKeepTheProcessSettings(t *testing.T) {
	t.Parallel()
	for _, bypass := range []bool{false, true} {
		for _, value := range []string{"yes", "1", "-1", "TRUE", " false "} {
			a, requests := newFastAgentSettingsSubject(t, bypass)
			before := a.SettingsSnapshot().ConfirmedOptions()
			result := a.UpdateSettings(map[string]string{"bypassPermissions": value, "model": "model-b"})
			assert.True(t, result.AppliedLive, "invalid input must not request restart")
			assert.Equal(t, before, result.ConfirmedOptions())
			assert.Empty(t, requests())
		}
	}
}

func TestFastAgentBypassUnchangedOrOmittedChoiceDelegatesOtherSettings(t *testing.T) {
	t.Parallel()
	for _, bypass := range []bool{false, true} {
		for _, value := range []string{"", strconv.FormatBool(bypass)} {
			a, requests := newFastAgentSettingsSubject(t, bypass)
			result := a.UpdateSettings(map[string]string{"bypassPermissions": value, "model": "model-b"})
			require.True(t, result.AppliedLive)
			assert.Equal(t, strconv.FormatBool(bypass), result.ConfirmedOptions()["bypassPermissions"])
			assert.Equal(t, "model-b", result.ConfirmedOptions()["model"])
			recorded := requests()
			require.Len(t, recorded, 1)
			assert.Equal(t, "model", recorded[0].Params["configId"])
			assert.Equal(t, "model-b", recorded[0].Params["value"])
		}
	}
}

func TestFastAgentNativeCommandsRequiresBypassBeforeAnyOtherWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bypass, native bool
		options        map[string]string
		restart        bool
	}{
		{options: map[string]string{"nativeCommands": "true", "model": "model-b"}},
		{bypass: true, options: map[string]string{"nativeCommands": "true"}, restart: true},
		{options: map[string]string{"nativeCommands": "true", "bypassPermissions": "true"}, restart: true},
		{bypass: true, native: true, options: map[string]string{"bypassPermissions": "false", "model": "model-b"}},
		{bypass: true, native: true, options: map[string]string{"bypassPermissions": "false", "nativeCommands": "false"}, restart: true},
		{bypass: true, native: true, options: map[string]string{"nativeCommands": "false"}, restart: true},
	} {
		a, requests := newFastAgentSettingsSubject(t, tc.bypass)
		a.nativeCommands = tc.native
		assert.Equal(t, !tc.native, optionids.GroupByID(a.OptionGroups(), "bypassPermissions").Mutable)
		before := a.SettingsSnapshot().ConfirmedOptions()
		result := a.UpdateSettings(tc.options)
		assert.Equal(t, !tc.restart, result.AppliedLive)
		assert.Equal(t, before, a.SettingsSnapshot().ConfirmedOptions())
		assert.Empty(t, requests())
	}
	for _, bypass := range []bool{false, true} {
		a, _ := newFastAgentSettingsSubject(t, bypass)
		group := optionids.GroupByID(a.OptionGroups(), OptionNativeCommands)
		require.NotNil(t, group)
		assert.Equal(t, bypass, group.Mutable)
		assert.Equal(t, "false", group.CurrentValue)
		assert.Equal(t, "false", nativeCommandsGroup.DefaultValue, "the live view cannot mutate the template")
		if !bypass {
			assert.Contains(t, group.ReadOnlyReason, "do not request approval")
		}
	}
}

func TestFastAgentNativeCommandsRejectsInvalidValuesBeforeOtherWrites(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"yes", "1", "-1", "TRUE", " false "} {
		a, requests := newFastAgentSettingsSubject(t, true)
		before := a.SettingsSnapshot().ConfirmedOptions()
		result := a.UpdateSettings(map[string]string{"nativeCommands": value, "model": "model-b", "bypassPermissions": "false"})
		assert.True(t, result.AppliedLive)
		assert.Equal(t, before, result.ConfirmedOptions())
		assert.Empty(t, requests())
	}
}
