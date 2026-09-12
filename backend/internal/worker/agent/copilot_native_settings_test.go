//go:build unix

package agent

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestNativeCopilotRejectsAnUnavailableModel(t *testing.T) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary: "copilot", helperRun: "TestHelperCopilotNativeConnection",
		wantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
	})
	provider, err := startNativeCopilot(t.Context(), Options{
		AgentID: "native-model-validation", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    optionmap.Map{OptionIDModel: "probe-model"},
	}, &testSink{})
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	result := provider.UpdateSettings(optionmap.Map{OptionIDModel: "missing-model"})
	require.Equal(t, OptionSettlementUnresolved, result.Settlements[OptionIDModel].State)
	require.Equal(t, "probe-model", provider.SettingsSnapshot().SurfacedOptions[OptionIDModel])
	result = provider.UpdateSettings(optionmap.Map{OptionIDModel: "probe-model"})
	require.Equal(t, OptionSettlementConfirmed, result.Settlements[OptionIDModel].State)
}
