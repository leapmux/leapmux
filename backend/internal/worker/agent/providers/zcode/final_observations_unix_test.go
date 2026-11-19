//go:build unix

package zcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodePublishesFinalObservationsAfterNativeExit(t *testing.T) {
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(profile, []byte(`native_exit_fixture() {
  printf '%s\n' '{"method":"verification/gate"}'
  IFS= read -r release
  printf '%s\n' "$ZCODE_VERIFICATION_OBSERVATION"
  return 37
}
`), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	prepared, err := launch.Wrap(ctx, launch.WrapSpec{
		Shell: bash, Launch: launch.ShellCommands("native_exit_fixture"),
	})
	require.NoError(t, err)
	patch := `{"model":{"current":{"providerId":"new","modelId":"B"}},"runtime":{"stateRevision":7,"contextUsage":{"used":900}}}`
	prepared.Cmd.Env = envutil.PinEnv(prepared.Cmd.Environ(), "BASH_ENV="+profile, "ZCODE_VERIFICATION_OBSERVATION="+string(zcodeStateLine(t, contracts.ZCodeScopeSession, "model_changed", patch)))
	pipes, err := providerkit.SetupLaunchPipes(prepared, cancel)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipes.Close() })
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	clock := testutil.NewQuartzMock(t)
	drain := clock.Trap().NewTimer(providerkit.WrappedProcessDrainTimerTag)
	t.Cleanup(drain.Close)
	a.Process = providerkit.NewProcess(agent.Options{AgentID: "test-agent"}, providerkit.ProcessLaunch{
		ProviderName: "zcode", Clock: clock, PreambleDelimiter: prepared.Delimiter, PreambleMetaPrefix: prepared.MetaPrefix,
	}, pipes, ctx, cancel)
	require.NoError(t, a.StartCmd())
	a.DrainStderr(pipes.Stderr())
	entered, release := make(chan struct{}), make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})
	go a.ReadOutput(agent.NewStdoutScanner(pipes.Stdout()), a.interceptResponse, func(line *providerkit.ParsedLine) {
		if line.Method == "verification/gate" {
			close(entered)
			<-release
			return
		}
		a.handleOutput(line)
	})
	deadline := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-deadline.Done():
		t.Fatal("the preceding stdout handler did not enter")
	}
	require.NoError(t, a.WriteStdin([]byte("exit\n")))
	testutil.WaitForTimer(t, deadline, drain)
	require.True(t, a.ProcessExited())
	select {
	case <-a.ProcessDone():
		t.Fatal("the reader completed while its handler was held")
	default:
	}
	release <- struct{}{}
	select {
	case <-a.ProcessDone():
	case <-deadline.Done():
		t.Fatal("the process lifecycle did not finish")
	}
	var native *exec.ExitError
	require.ErrorAs(t, a.Wait(), &native)
	assert.Equal(t, 37, native.ExitCode())
	assert.Equal(t, agent.MessageCompletionError, a.ProcessExitCompletion())
	assert.Equal(t, "new/B", a.model, "the final patch reached the provider")
	assert.Equal(t, int64(900), a.latestContextUsage[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, 1, sink.SettingsRefreshCount(), "the final model must be persisted")
	assert.Equal(t, 1, sink.SessionInfoCount(), "the final context usage must be published")
}
