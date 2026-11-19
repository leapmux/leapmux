//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// tabMainEnv is the environment of main inside a tab whose Worker linked cli.
// It replaces each control variable that the test process inherited, so a run
// inside a real LeapMux tab tests the same thing.
func tabMainEnv(cli string) []string {
	return []string{
		controlenv.SocketVariable + "=unix:/nonexistent/leapmux-test.sock",
		controlenv.TokenVariable + "=token-1",
		controlenv.CLIVariable + "=" + cli,
		controlHandoffMarker + "=",
	}
}

// main replaces itself with the Worker's CLI: the CLI receives the arguments
// as typed and the marker, and its exit code is the command's.
func TestMainHandsAControlCommandToTheWorkerCLI(t *testing.T) {
	t.Parallel()
	cli := filepath.Join(t.TempDir(), "leapmux")
	testutil.WriteExecutable(t, cli, "#!/bin/sh\n"+
		"printf 'args=%s\\n' \"$*\"\n"+
		"printf 'marker=%s\\n' \"$"+controlHandoffMarker+"\"\n"+
		"exit 7\n")

	code, stdout, stderr := runMain(t, []string{"control", "tab", "list", "--a-flag-of-a-newer-release"}, tabMainEnv(cli)...)

	assert.Equal(t, 7, code)
	assert.Equal(t, "args=control tab list --a-flag-of-a-newer-release\nmarker=1\n", stdout)
	assert.Empty(t, stderr)
}

// A target that leads back to a `leapmux` with the hand-off runs once: the
// second process reads the marker and runs the command itself.
func TestMainStopsAHandOffThatLeadsBackToLeapmux(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	executable, err := os.Executable()
	require.NoError(t, err)
	cli := filepath.Join(dir, "leapmux")
	testutil.WriteExecutable(t, cli, "#!/bin/sh\n"+
		"echo run >> "+shellquote.POSIX.Quote(runs)+"\n"+
		"exec "+shellquote.POSIX.Quote(executable)+" \"$@\"\n")

	code, stdout, stderr := runMain(t, []string{"control", "--help"}, tabMainEnv(cli)...)

	assert.Zero(t, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "Control LeapMux from a script or from an agent.", "the second process prints the help")
	assert.Empty(t, stderr)
	body, err := os.ReadFile(runs)
	require.NoError(t, err)
	assert.Equal(t, "run\n", string(body), "the hand-off runs once")
}

// When the Worker's CLI is gone, this leapmux states why and runs the command.
func TestMainRunsTheCommandWhenTheWorkerCLIIsMissing(t *testing.T) {
	t.Parallel()
	cli := filepath.Join(t.TempDir(), "cli-1", "leapmux")

	code, stdout, stderr := runMain(t, []string{"control", "--help"}, tabMainEnv(cli)...)

	assert.Zero(t, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "Control LeapMux from a script or from an agent.")
	assert.Equal(t, "warning: the Worker's CLI "+cli+" does not exist. "+
		"This leapmux runs the command, and its version can differ from the Worker's.\n", stderr)
}

// A lifecycle command stays with the binary that the user ran, even inside a
// tab whose Worker linked another CLI.
func TestMainKeepsALifecycleCommandInsideATab(t *testing.T) {
	t.Parallel()
	cli := filepath.Join(t.TempDir(), "leapmux")
	testutil.WriteExecutable(t, cli, "#!/bin/sh\necho handed-off\nexit 7\n")

	code, stdout, stderr := runMain(t, []string{"worker", "--help"}, tabMainEnv(cli)...)

	assert.Zero(t, code, "stderr: %s", stderr)
	assert.False(t, strings.Contains(stdout, "handed-off"), "stdout: %s", stdout)
	assert.Contains(t, stdout, "leapmux worker")
	assert.Empty(t, stderr)
}
