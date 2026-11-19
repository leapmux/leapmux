package kimi

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiStartupRestoresNativeConfigDiagnostics(t *testing.T) {
	t.Parallel()
	_, server := newFakeKap(t)
	program, err := os.Executable()
	require.NoError(t, err)
	sink := &agenttest.ControlSink{}
	opts := agent.Options{
		Shell:   terminal.ResolveDefaultShell(),
		AgentID: "config-diagnostics", WorkingDir: t.TempDir(),
		StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		ExtraEnv: []string{"LEAPMUX_KIMI_CONFIG_PEER=" + server.URL, "LEAPMUX_KIMI_CONFIG_VALUE=kept"},
	}
	spec := launch.Spec{Program: program, PrefixArgs: []string{"-test.run=^TestKimiConfigDiagnosticPeer$", "--"}}
	running, err := startServer(t.Context(), opts, agent.NewProviderServices(sink), spec, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { running.Stop(); _ = running.Wait() })
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "max_retries_per_step")
	assert.Contains(t, string(rows[0].Content), "max_attempts_per_step")
	running.(*Agent).HandleOutput([]byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"loop_control","message":"[loop_control] 'max_retries_per_step' is deprecated and no longer used; rename it to 'max_attempts_per_step'. Run /update-config to fix it."}]}}`))
	assert.Len(t, sink.PersistedNotifications(), 1, "a later native event does not repeat the startup diagnostic")
}

func TestKimiConfigDiagnosticPeer(t *testing.T) {
	address := os.Getenv("LEAPMUX_KIMI_CONFIG_PEER")
	if address == "" {
		return
	}
	if strings.HasSuffix(strings.Join(os.Args, " "), "doctor config") {
		if os.Getenv("LEAPMUX_KIMI_CONFIG_VALUE") != "kept" {
			os.Exit(2)
		}
		switch os.Getenv("LEAPMUX_KIMI_CONFIG_MODE") {
		case "missing":
			fmt.Print("Kimi doctor\n\nSKIP config.toml  missing-config.toml\n\nAll checked config files are valid.\n")
			return
		case "invalid":
			fmt.Fprintln(os.Stderr, "Kimi doctor found 1 issue.\nERROR config.toml synthetic-config.toml\n  Invalid configuration.")
			os.Exit(1)
		case "oversized":
			fmt.Print(strings.Repeat("x", 2<<20))
			return
		case "unrelated":
			fmt.Print("Kimi doctor\n\nOK tui.toml  synthetic-tui.toml\n  An unrelated warning.\n")
			return
		}
		fmt.Print("Kimi doctor\n\nOK config.toml  synthetic-config.toml\n  [loop_control] 'max_retries_per_step' is deprecated and no longer used; rename it to 'max_attempts_per_step'. Run /update-config to fix it.\n\nAll checked config files are valid.\n")
		return
	}
	fmt.Printf("Kimi server: %s/#token=%s\n", address, fakeKapToken)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestKimiStartupConfigDiagnosticsIgnoreUnusableResults(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "invalid", "oversized", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			program, err := os.Executable()
			require.NoError(t, err)
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			opts := agent.Options{
				Shell: terminal.ResolveDefaultShell(), WorkingDir: t.TempDir(), StartupTimeout: 30 * time.Second,
				ExtraEnv: []string{"LEAPMUX_KIMI_CONFIG_PEER=synthetic", "LEAPMUX_KIMI_CONFIG_VALUE=kept", "LEAPMUX_KIMI_CONFIG_MODE=" + mode},
			}
			spec := launch.Spec{Program: program, PrefixArgs: []string{"-test.run=^TestKimiConfigDiagnosticPeer$", "--"}}
			rig.agent.persistStartupConfigDiagnostics(t.Context(), opts, spec)
			assert.Empty(t, rig.sink.PersistedNotifications())
		})
	}
}
