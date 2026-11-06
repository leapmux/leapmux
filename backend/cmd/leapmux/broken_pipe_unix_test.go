//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// A long-lived command must finish its shutdown even when its stderr peer has
// died: a lost stderr must return EPIPE from the log writes, not raise the
// runtime's fatal SIGPIPE. These tests close the stderr read end while the
// command runs, then signal it, and require a clean exit.

// childTimeout is the generous ceiling for one readiness wait. Only a failure
// path waits for it.
const childTimeout = 60 * time.Second

// lostStderrProcess is a running command whose stderr the test can revoke the
// way a dead pipe peer does.
type lostStderrProcess struct {
	cmd    *exec.Cmd
	stderr *os.File
	output testutil.LogBuffer
	exited chan error
	reaped chan struct{}
}

// uniqueListenEnv points LEAPMUX_HUB_LISTEN at a per-test local socket, so
// child processes of concurrent tests cannot collide on the platform default.
func uniqueListenEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LEAPMUX_HUB_LISTEN", locallistentest.UniqueListenURL(t, "leapmux-cmd-test"))
}

// startWithStderrPipe starts this test binary on args through runMainEnv, with
// stderr on a pipe the test holds the read end of.
func startWithStderrPipe(t *testing.T, args ...string) *lostStderrProcess {
	t.Helper()
	stderrR, stderrW, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = stderrR.Close(); _ = stderrW.Close() })
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = stderrW
	require.NoError(t, cmd.Start())
	// The child holds its inherited writer copy; drop ours.
	_ = stderrW.Close()
	p := &lostStderrProcess{
		cmd:    cmd,
		stderr: stderrR,
		exited: make(chan error, 1),
		reaped: make(chan struct{}),
	}
	reader := bufio.NewReader(stderrR)
	go func() {
		for {
			line, readErr := reader.ReadString('\n')
			_, _ = p.output.Write([]byte(line))
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		p.exited <- cmd.Wait()
		close(p.reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		// reaped, not exited: the exit value may already be consumed, and
		// cleanup must not wait for a second one.
		<-p.reaped
	})
	return p
}

// waitReadiness waits until stderr holds line, which marks the point where the
// command can run a graceful shutdown.
func (p *lostStderrProcess) waitReadiness(t *testing.T, line string) {
	t.Helper()
	timer := time.NewTimer(childTimeout)
	defer timer.Stop()
	for {
		if strings.Contains(p.output.String(), line) {
			return
		}
		select {
		case err := <-p.exited:
			t.Fatalf("the command exited before it became ready: %v\noutput:\n%s", err, p.output.String())
		case <-timer.C:
			t.Fatalf("the command never logged %q\noutput:\n%s", line, p.output.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// loseStderr closes the read end. The command's next log write reports EPIPE.
func (p *lostStderrProcess) loseStderr(t *testing.T) {
	t.Helper()
	require.NoError(t, p.stderr.Close())
}

// signalThenRequireCleanExit sends sig and requires exit 0 with no signal.
func (p *lostStderrProcess) signalThenRequireCleanExit(t *testing.T, sig os.Signal) {
	t.Helper()
	require.NoError(t, p.cmd.Process.Signal(sig))
	select {
	case err := <-p.exited:
		require.NoError(t, err, "the command must exit 0, not die by signal\noutput:\n%s", p.output.String())
	case <-time.After(childTimeout):
		t.Fatalf("the command did not exit after the signal\noutput:\n%s", p.output.String())
	}
}

// An earlier log line is not enough as the readiness mark: signal handling
// starts late on hub and worker, and a cancel during solo bring-up fails the
// start. solo's readiness line is exact, because "hub listening" prints
// earlier, during bring-up.
func TestCommandsSurviveALostStderrDuringShutdown(t *testing.T) {
	t.Run("solo", func(t *testing.T) {
		locallistentest.SandboxHome(t)
		uniqueListenEnv(t)
		p := startWithStderrPipe(t, "solo")
		p.waitReadiness(t, "leapmux solo listening")
		p.loseStderr(t)
		p.signalThenRequireCleanExit(t, syscall.SIGTERM)
	})

	t.Run("hub", func(t *testing.T) {
		locallistentest.SandboxHome(t)
		uniqueListenEnv(t)
		p := startWithStderrPipe(t, "hub")
		p.waitReadiness(t, "hub listening")
		p.loseStderr(t)
		p.signalThenRequireCleanExit(t, syscall.SIGTERM)
	})

	t.Run("worker", func(t *testing.T) {
		// The readiness mark is the fake Hub's first received frame, not a
		// log line: the worker installs its signal handling only after
		// registration, and a cancel before that fails the start.
		hubURL, connected := startReceivingHub(t)
		cfg := writeRegisteredState(t)
		p := startWithStderrPipe(t, "worker", "--hub", hubURL, "--data-dir", cfg.DataDir, "--log-level", "info")
		select {
		case <-connected:
		case err := <-p.exited:
			t.Fatalf("the worker exited before it connected: %v", err)
		case <-time.After(childTimeout):
			t.Fatalf("the worker never connected")
		}
		p.loseStderr(t)
		p.signalThenRequireCleanExit(t, syscall.SIGTERM)
	})
}
