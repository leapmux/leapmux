package terminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestInitialCommandPreservesThePowerShellRunspace(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is unavailable")
	}
	dir := t.TempDir()
	receipt := filepath.Join(dir, "powershell-state")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, Options{
		ID: "powershell-state", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "$leapmuxProbeState = 'retained in PowerShell'; function global:prompt { 'LEAPMUX_PS_READY> ' }",

		ExtraEnv: []string{"LEAPMUX_TEST_RECEIPT=" + receipt, "HOME=" + dir},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	var cursorReplies int
	replyToCursorQueries := func() string {
		screen, _ := term.ScreenSnapshot()
		// PSReadLine asks for the cursor position before printing its
		// prompt. This fixture owns the PTY without a terminal emulator.
		for queries := strings.Count(string(screen), "\x1b[6n"); cursorReplies < queries; cursorReplies++ {
			if err := term.SendInput([]byte("\x1b[1;1R")); err != nil {
				return ""
			}
		}
		return string(screen)
	}
	testutil.RequireEventually(t, func() bool {
		return strings.Contains(replyToCursorQueries(), "LEAPMUX_PS_READY")
	}, "the PowerShell runspace should reach its first prompt")
	require.NoError(t, term.SendInput([]byte("[IO.File]::WriteAllText($env:LEAPMUX_TEST_RECEIPT, $leapmuxProbeState)\r")))
	testutil.RequireEventually(t, func() bool {
		replyToCursorQueries()
		result, err := os.ReadFile(receipt)
		return err == nil && string(result) == "retained in PowerShell"
	}, "the next PowerShell command must see the original runspace's variable")
}

func TestInitialCommandCanExitPowerShell(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is unavailable")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, Options{
		ID: "powershell-exit", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "exit 7",
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	select {
	case <-term.exitCh:
		assert.Equal(t, 7, term.Wait())
	case <-ctx.Done():
		t.Fatal("PowerShell did not exit")
	}
}
