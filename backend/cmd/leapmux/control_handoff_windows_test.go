package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// Windows cannot replace a process, so execControlCLI waits for the target and
// returns its exit code, which main then exits with. Windows keeps an exit
// code of 32 bits, so a code above 255 stays as it is.
func TestExecControlCLIReturnsTheExitCodeOfTheTarget(t *testing.T) {
	shell, err := exec.LookPath(testutil.TestShell())
	require.NoError(t, err)
	for _, want := range []int{0, 7, 255, 4096} {
		code, err := execControlCLI(shell, []string{shell, "/d", "/c", "exit " + strconv.Itoa(want)}, os.Environ())

		require.NoError(t, err)
		assert.Equal(t, want, code)
	}
}

// A target that does not start returns the failure, and the caller runs the
// command itself: nothing of the target ran.
func TestExecControlCLIReportsATargetThatDoesNotStart(t *testing.T) {
	target := filepath.Join(t.TempDir(), "leapmux.exe")

	_, err := execControlCLI(target, []string{target, "control"}, os.Environ())

	assert.Error(t, err)
}
