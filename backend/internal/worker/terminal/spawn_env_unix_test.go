//go:build unix

package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// A terminal that the Worker spawns with remote control runs the Worker's own
// CLI for a bare `leapmux`, ahead of a `leapmux` that the user installed on the
// Worker's PATH.
//
// The shell is a script that reports its environment and exits. A real login
// shell would run the profile of this machine first, which can move the entry
// (macOS path_helper) or drop it (Debian's /etc/profile assigns PATH), so the
// result would depend on the machine. The script receives exactly what the
// PTY spawn gives.
func TestTerminal_ShellRunsTheWorkerCLIByName(t *testing.T) {
	installed := t.TempDir()
	testutil.WriteExecutable(t, filepath.Join(installed, "leapmux"), "#!/bin/sh\necho installed-cli\n")
	t.Setenv("PATH", installed+":"+os.Getenv("PATH"))
	cliDir := filepath.Join(t.TempDir(), "cli-1")
	require.NoError(t, os.Mkdir(cliDir, 0o700))
	cli := filepath.Join(cliDir, "leapmux")
	testutil.WriteExecutable(t, cli, "#!/bin/sh\necho worker-cli\n")
	shell := filepath.Join(t.TempDir(), "report-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\n"+
		"printf '<PATH>%s</PATH>\\n' \"$PATH\"\n"+
		"printf '<CLI>%s</CLI>\\n' \"$LEAPMUX_CONTROL_CLI\"\n"+
		"printf '<RAN>%s</RAN>\\n' \"$(leapmux)\"\n")

	var mu sync.Mutex
	var output strings.Builder
	term, err := Start(context.Background(), Options{
		ID:         "test-control-cli",
		Shell:      shell,
		WorkingDir: t.TempDir(),
		ExtraEnv:   []string{"LEAPMUX_CONTROL_SOCK=unix:/fresh/sock", controlenv.CLIVariable + "=" + cli},
	}, quartz.NewReal(), func(data []byte, _ int64, _ []Signal) {
		mu.Lock()
		defer mu.Unlock()
		output.Write(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(output.String(), "</RAN>")
	}, "the shell reports its environment")

	mu.Lock()
	got := output.String()
	mu.Unlock()
	assert.Contains(t, got, "<PATH>"+cliDir+":"+installed+":", "the Worker's CLI directory comes first, and the inherited PATH follows it")
	assert.Contains(t, got, "<CLI>"+cli+"</CLI>")
	assert.Contains(t, got, "<RAN>worker-cli</RAN>", "a bare `leapmux` runs the Worker's CLI, not the installed one")
}
