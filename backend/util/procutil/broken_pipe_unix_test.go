//go:build !windows

package procutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenPipeTarget selects the descriptor a helper writes into after the test
// closed the pipe's read end: "stderr" or "stdout".
const brokenPipeTarget = "LEAPMUX_TEST_BROKEN_PIPE_TARGET"

// brokenPipeOptIn makes the helper call KeepRunningOnBrokenPipe before it writes.
const brokenPipeOptIn = "LEAPMUX_TEST_BROKEN_PIPE_OPT_IN"

// brokenPipeSpawn makes the helper run /bin/sh -c 'echo x; echo y' with the
// shell's stdout on a pipe whose read end is closed, and records the shell's
// wait status in the file that brokenPipeMarker gives.
const brokenPipeSpawn = "LEAPMUX_TEST_BROKEN_PIPE_SPAWN"

// brokenPipeMarker gives the file the helper writes its outcome to.
const brokenPipeMarker = "LEAPMUX_TEST_BROKEN_PIPE_MARKER"

// TestHelperBrokenPipeProcess is the process the broken-pipe tests start. It is
// inert in the test process itself.
func TestHelperBrokenPipeProcess(t *testing.T) {
	if os.Getenv(brokenPipeTarget) == "" && os.Getenv(brokenPipeSpawn) == "" {
		return
	}
	if os.Getenv(brokenPipeOptIn) == "1" {
		KeepRunningOnBrokenPipe()
	}
	if os.Getenv(brokenPipeSpawn) == "1" {
		spawnBrokenPipeGrandchild(t)
		return
	}
	target := os.Stderr
	if os.Getenv(brokenPipeTarget) == "stdout" {
		target = os.Stdout
	}
	// Write until a write fails. A write into a pipe with a live reader
	// succeeds (the buffer fills, then the write blocks), so the loop only
	// ends when the test closes the read end: the next write reports EPIPE,
	// or, under the default disposition, the first such write raises a fatal
	// SIGPIPE. Reaching the marker write below therefore proves the write
	// failed and the helper kept running.
	for {
		if _, err := fmt.Fprintln(target, "payload"); err != nil {
			break
		}
	}
	if marker := os.Getenv(brokenPipeMarker); marker != "" {
		if err := os.WriteFile(marker, []byte("survived\n"), 0o600); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

// spawnBrokenPipeGrandchild runs a shell whose stdout is a pipe with no
// reader. echo writes a few bytes, far below the pipe buffer's size, so the
// shell survives only while its SIGPIPE disposition is the default one.
func spawnBrokenPipeGrandchild(t *testing.T) {
	t.Helper()
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	// No reader may exist while the shell writes, or echo would succeed into
	// the pipe buffer and the shell would run to completion.
	require.NoError(t, pr.Close())
	sh := exec.Command("/bin/sh", "-c", "echo x; echo y")
	sh.Stdout = pw
	shErr := sh.Run()
	closeErr := pw.Close()
	// The outcome travels through the marker file, because os.Exit below must
	// not fold the shell's status into the helper's own exit code.
	outcome := fmt.Sprintf("closed=%v err=%v", closeErr == nil, shErr != nil)
	var exitErr *exec.ExitError
	if errors.As(shErr, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			outcome = fmt.Sprintf("signaled=%v signal=%v", ws.Signaled(), ws.Signal())
		}
	}
	if marker := os.Getenv(brokenPipeMarker); marker != "" {
		_ = os.WriteFile(marker, []byte(outcome+"\n"), 0o600)
	}
	os.Exit(0)
}

// startBrokenPipeHelper starts the helper with one descriptor on a pipe whose
// read end the caller holds. It returns the helper and the read end. The
// parent's writer copy is closed at start, so the helper is the pipe's only
// writer.
func startBrokenPipeHelper(t *testing.T, target string, env ...string) (*exec.Cmd, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pr.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBrokenPipeProcess$")
	cmd.Env = append(append(os.Environ(), brokenPipeTarget+"="+target), env...)
	if target == "stderr" {
		cmd.Stderr = pw
	} else {
		cmd.Stdout = pw
	}
	require.NoError(t, cmd.Start())
	// The helper holds its inherited copy of pw; drop the parent's so the
	// read end sees the helper as its only writer.
	require.NoError(t, pw.Close())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd, pr
}

// waitBrokenPipeHelper closes the read end, which makes the helper's next
// write to the descriptor under test report EPIPE, and waits for the exit.
func waitBrokenPipeHelper(t *testing.T, cmd *exec.Cmd, pr *os.File) (exitErr error, signaled bool) {
	t.Helper()
	require.NoError(t, pr.Close())
	err := cmd.Wait()
	if err == nil {
		return nil, false
	}
	var exitErrTyped *exec.ExitError
	require.ErrorAs(t, err, &exitErrTyped)
	ws, ok := exitErrTyped.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	return err, ws.Signaled() && ws.Signal() == syscall.SIGPIPE
}

// The control: a helper that did not opt in dies by SIGPIPE on its first write
// to the lost stderr. This proves the harness reproduces the runtime's fatal
// SIGPIPE before any test relies on opting out of it.
func TestBrokenPipeKillsAProcessThatDidNotOptIn(t *testing.T) {
	cmd, pr := startBrokenPipeHelper(t, "stderr")

	_, signaled := waitBrokenPipeHelper(t, cmd, pr)
	assert.True(t, signaled, "the helper must die by SIGPIPE")
}

func TestKeepRunningOnBrokenPipeSurvivesALostStderr(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	cmd, pr := startBrokenPipeHelper(t, "stderr", brokenPipeOptIn+"=1", brokenPipeMarker+"="+marker)

	err, signaled := waitBrokenPipeHelper(t, cmd, pr)
	require.NoError(t, err, "the helper must exit 0 after the write failed")
	assert.False(t, signaled)
	contents, readErr := os.ReadFile(marker)
	require.NoError(t, readErr, "the helper must write its marker after surviving")
	assert.Equal(t, "survived\n", string(contents))
}

func TestKeepRunningOnBrokenPipeSurvivesALostStdout(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	cmd, pr := startBrokenPipeHelper(t, "stdout", brokenPipeOptIn+"=1", brokenPipeMarker+"="+marker)

	err, signaled := waitBrokenPipeHelper(t, cmd, pr)
	require.NoError(t, err, "the helper must exit 0 after the write failed")
	assert.False(t, signaled)
	contents, readErr := os.ReadFile(marker)
	require.NoError(t, readErr, "the helper must write its marker after surviving")
	assert.Equal(t, "survived\n", string(contents))
}

// A Go-installed SIGPIPE handler resets to the default disposition across
// exec, so the helper's shell still dies by SIGPIPE. Under signal.Ignore the
// inherited SIG_IGN would let the shell run to completion.
func TestKeepRunningOnBrokenPipeLeavesChildrenTheDefault(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	cmd, pr := startBrokenPipeHelper(t, "stderr", brokenPipeOptIn+"=1", brokenPipeSpawn+"=1", brokenPipeMarker+"="+marker)

	err, signaled := waitBrokenPipeHelper(t, cmd, pr)
	require.NoError(t, err, "the helper must exit 0 after its own write failed")
	assert.False(t, signaled)
	contents, readErr := os.ReadFile(marker)
	require.NoError(t, readErr)
	assert.Equal(t, "signaled=true signal=broken pipe\n", string(contents),
		"the shell must die by SIGPIPE, not run to completion")
}
