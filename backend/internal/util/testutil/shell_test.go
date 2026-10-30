package testutil

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellOutputCommandDistinguishesExecutionFromInputEcho(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"descendants_ready_marker", "natural_exit_marker", "natural_exit_last_write", strings.Repeat("m", 1000)} {
		line, err := TestShellOutputCommand(marker)
		require.NoError(t, err)
		assert.NotContains(t, line, marker, "input echo must not satisfy the output assertion")
		args := []string{"-c", line}
		if runtime.GOOS == "windows" {
			args = []string{"/D", "/Q", "/C", line}
		}
		// Command mode avoids sending ConPTY's Enter sequence through a pipe.
		cmd := exec.CommandContext(t.Context(), TestShell(), args...)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		assert.Contains(t, string(output), marker)
	}
}

func TestShellOutputCommandRejectsUnsafeOrAmbiguousMarkers(t *testing.T) {
	t.Parallel()
	ambiguous := "printf"
	if runtime.GOOS == "windows" {
		ambiguous = "echo"
	}
	for _, marker := range []string{"", " ", "a&b", "$HOME", "line\nbreak", "π", ambiguous} {
		line, err := TestShellOutputCommand(marker)
		assert.Error(t, err, "marker %q", marker)
		assert.Empty(t, line)
	}
}
