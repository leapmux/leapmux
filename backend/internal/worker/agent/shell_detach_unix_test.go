//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestBuildShellWrappedCommandStartsANewSession(t *testing.T) {
	for _, loginShell := range []bool{true, false} {
		t.Run("loginShell="+strconv.FormatBool(loginShell), func(t *testing.T) {
			cmd, _, _ := buildShellWrappedCommand(context.Background(), shellWrapSpec{
				Shell:      "/bin/bash",
				LoginShell: loginShell,
				BinaryName: "claude",
				WorkingDir: t.TempDir(),
			})
			require.NotNil(t, cmd.SysProcAttr, "LoginShell=%v: SysProcAttr must be set", loginShell)
			assert.True(t, cmd.SysProcAttr.Setsid, "LoginShell=%v: child must start in a new session", loginShell)
		})
	}
}

func TestBuildShellWrappedCommandTcshLoginUsesIc(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(context.Background(), shellWrapSpec{
		Shell:      "/bin/tcsh",
		LoginShell: true,
		BinaryName: "claude",
		WorkingDir: t.TempDir(),
	})
	require.GreaterOrEqual(t, len(cmd.Args), 2)
	assert.Equal(t, "-ic", cmd.Args[1], "tcsh rejects -l combined with -c")
}

func TestProbeBinaryRunsTheShellInItsOwnSession(t *testing.T) {
	for _, loginShell := range []bool{true, false} {
		t.Run("loginShell="+strconv.FormatBool(loginShell), func(t *testing.T) {
			stub, sidFile := writeSessionRecordingStub(t)
			t.Setenv("LEAPMUX_SID_FILE", sidFile)

			available, conclusive := probeBinary(context.Background(), stub, loginShell, "claude")
			require.True(t, conclusive, "stub shell must run to completion")
			assert.True(t, available, "stub prints the present marker, so the probe treats the binary as present")

			raw, err := os.ReadFile(sidFile)
			require.NoError(t, err, "stub must record its session id")
			childSid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			require.NoError(t, err, "stub session id must be an integer")

			parentSid, err := unix.Getsid(0)
			require.NoError(t, err)
			assert.NotEqual(t, parentSid, childSid,
				"probe shell must run in its own session, not the worker's")
		})
	}
}

// writeSessionRecordingStub writes an executable stub that records its
// own session id to $LEAPMUX_SID_FILE and exits 0, ignoring argv.
// probeBinary passes -i -l -c …; this stub is the process exec starts,
// so a missing Setsid still fails the session assertion.
//
// syscall.Getsid is not in the Linux syscall package, and GOOS=linux
// go vet typechecks this file, so the stub reads /proc/self/stat (Linux)
// or `ps -o sess=` (BSD/Darwin) instead of importing Getsid.
func writeSessionRecordingStub(t *testing.T) (bin, sidFile string) {
	t.Helper()
	dir := t.TempDir()
	sidFile = filepath.Join(dir, "sid")
	bin = filepath.Join(dir, "shell")
	script := `#!/bin/sh
if [ -r /proc/self/stat ]; then
	sid=$(sed 's/.*) //' /proc/self/stat | awk '{print $4}')
else
	sid=$(ps -p $$ -o sess=)
fi
printf '%s\n' "$sid" > "$LEAPMUX_SID_FILE"
printf '%s\n' '` + probeReachedPresent + `'
exit 0
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin, sidFile
}
