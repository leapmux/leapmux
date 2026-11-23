//go:build darwin || linux

package amp

import (
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAmpProcessAliveReturnsFalseForZombie verifies that an exited child process
// waiting to be reaped (zombie state) is reported as not alive.
func TestAmpProcessAliveReturnsFalseForZombie(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Wait()
	})

	// Waiting for stdout EOF ensures the child has entered exit teardown.
	_, err = io.ReadAll(stdout)
	require.NoError(t, err)

	// Wait until the kernel marks the unreaped child as a zombie before probing.
	require.Eventually(t, func() bool {
		out, psErr := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
		if psErr != nil {
			return false
		}
		return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
	}, 30*time.Second, 5*time.Millisecond, "child process %d did not enter zombie state", pid)

	assert.False(t, ampProcessAlive(pid), "an unreaped zombie process must not be reported alive")
}
