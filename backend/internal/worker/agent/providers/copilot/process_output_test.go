package copilot

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/require"
)

func TestInvalidAgentFramingCancelsTheProcess(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperInvalidAgentFraming$")
	procutil.DetachFromTerminal(cmd)
	cmd.Env = append(os.Environ(), "LEAPMUX_TEST_INVALID_AGENT_FRAMING=1")
	stdin, stdout, stderr, err := providerkit.SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	process := providerkit.NewProcess(agent.Options{AgentID: "bad-frame"}, "probe", cmd, stdin, ctx, cancel, "", "")
	cancelled := make(chan struct{})
	var once sync.Once
	process.SetCancelForTest(func() {
		once.Do(func() { close(cancelled) })
		cancel()
	})
	require.NoError(t, process.StartCmd(cmd, cancel))
	process.DrainStderr(stderr)
	go process.ReadOutput(newCopilotScanner(stdout, "", 1024), func(*providerkit.ParsedLine) bool { return false }, func(*providerkit.ParsedLine) {})
	t.Cleanup(func() { cancel(); _ = process.Wait() })
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("The worker left the process alive after an invalid frame")
	}
	_ = process.Wait()
	require.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
}

func TestHelperInvalidAgentFraming(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_INVALID_AGENT_FRAMING") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, "Content-Length: -1\r\n\r\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
