//go:build unix

package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCopilotRejectsAnUnavailableModel(t *testing.T) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
	})
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-model-validation", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    optionmap.Map{agent.OptionIDModel: "probe-model"},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	result := provider.UpdateSettings(optionmap.Map{agent.OptionIDModel: "missing-model"})
	require.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	require.Equal(t, "probe-model", provider.SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
	result = provider.UpdateSettings(optionmap.Map{agent.OptionIDModel: "probe-model"})
	require.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDModel].State)
}

// EffortAuto is LeapMux's sentinel for "send no effort at all".
//
// The runtime accepts the tiers of its own catalogue alone, so the sentinel must reach
// neither the `model.setReasoningEffort` request nor the session configuration. The
// runtime then keeps the tier it chose, and the settlement removes the stored effort
// rather than recording a word the runtime does not have.
func TestNativeCopilotEffortAutoSendsNoEffort(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-effort-auto", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    optionmap.Map{agent.OptionIDModel: "probe-model", agent.OptionIDEffort: agent.EffortAuto},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })

	result := provider.UpdateSettings(optionmap.Map{agent.OptionIDEffort: agent.EffortAuto})
	settlement := result.Settlements[agent.OptionIDEffort]
	require.Equal(t, agent.OptionSettlementConfirmed, settlement.State,
		"the sentinel asks for no change, so there is nothing the runtime can refuse")
	assert.Nil(t, settlement.Value, "a confirmed settlement with no value removes the stored effort")
	assert.NotEqual(t, agent.EffortAuto, provider.SettingsSnapshot().SurfacedOptions[agent.OptionIDEffort],
		"the snapshot reports the tier the runtime kept, never the sentinel")

	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	for decoder.More() {
		var request agenttest.RecordedRequest
		require.NoError(t, decoder.Decode(&request))
		assert.NotEqual(t, "session.model.setReasoningEffort", request.Method,
			"the sentinel starts no effort request")
		if request.Method == "session.create" {
			assert.NotContains(t, request.Params, "reasoningEffort",
				"the sentinel never reaches the session configuration")
		}
	}
}

// A model switch keeps the effort. The worker sends the model and the kept tier in ONE update,
// because the new model offers the tier. The update must switch the model first and then set the
// tier, so the tier lands on the new model whatever the runtime does with the tier at a switch.
// The settlement of each axis then states the value that the runtime reports.
func TestNativeCopilotModelSwitchSetsTheKeptEffortOnTheNewModel(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env: []string{
			"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath,
			"LEAPMUX_TEST_COPILOT_SECOND_MODEL=other-model",
		},
	})
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-model-switch-effort", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    optionmap.Map{agent.OptionIDModel: "probe-model", agent.OptionIDEffort: "high"},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })

	result := provider.UpdateSettings(optionmap.Map{agent.OptionIDModel: "other-model", agent.OptionIDEffort: "high"})
	require.True(t, result.AppliedLive)
	for key, want := range map[string]string{agent.OptionIDModel: "other-model", agent.OptionIDEffort: "high"} {
		settlement := result.Settlements[key]
		require.Equal(t, agent.OptionSettlementConfirmed, settlement.State, key)
		require.NotNil(t, settlement.Value, key)
		assert.Equal(t, want, *settlement.Value, key)
	}

	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	var methods []string
	for decoder.More() {
		var request agenttest.RecordedRequest
		require.NoError(t, decoder.Decode(&request))
		switch request.Method {
		case "session.model.switchTo":
			assert.Equal(t, "other-model", request.Params["modelId"])
			methods = append(methods, request.Method)
		case "session.model.setReasoningEffort":
			assert.Equal(t, "high", request.Params["reasoningEffort"])
			methods = append(methods, request.Method)
		}
	}
	assert.Equal(t, []string{"session.model.switchTo", "session.model.setReasoningEffort"}, methods,
		"the update switches the model, then sets the tier on it")
}
