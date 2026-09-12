package agent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

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
	stdin, stdout, stderr, err := setupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	process := newProcessBase(Options{AgentID: "bad-frame"}, "probe", cmd, stdin, ctx, cancel, "", "")
	cancelled := make(chan struct{})
	var once sync.Once
	process.cancel = func() {
		once.Do(func() { close(cancelled) })
		cancel()
	}
	require.NoError(t, process.startCmd(cmd, cancel))
	process.drainStderr(stderr)
	go process.readOutput(newCopilotScanner(stdout, "", 1024), func(*parsedLine) bool { return false }, func(*parsedLine) {})
	t.Cleanup(func() { cancel(); _ = process.Wait() })
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("The worker left the process alive after an invalid frame")
	}
	_ = process.Wait()
	require.Equal(t, MessageCompletionError, process.processExitCompletion())
}

func TestHelperInvalidAgentFraming(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_INVALID_AGENT_FRAMING") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, "Content-Length: -1\r\n\r\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
