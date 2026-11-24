//go:build !windows

package terminal

import (
	"context"
	"errors"
	"fmt"
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

func nushellInitialCommandOptions(t *testing.T) Options {
	t.Helper()
	shell, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("Nushell is unavailable")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "nushell")
	require.NoError(t, os.MkdirAll(config, 0o700))
	dataDir := filepath.Join(dir, `private "data" π`)
	require.NoError(t, os.Mkdir(dataDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "login.nu"), []byte(`
$env.config.show_banner = false
$env.config.shell_integration = {osc133: false}
def leapmux_login_value [] { 'profile value' }
def save [] { error make {msg: 'profile overrides save'} }
$env.LEAPMUX_TEST_PROMPTS = 0
$env.config.hooks.pre_prompt = [{|| $env.LEAPMUX_TEST_PROMPTS = ($env.LEAPMUX_TEST_PROMPTS + 1) }]
'login ready' | %save --raw ($env.XDG_CONFIG_HOME | %path join 'profile-ready')
`), 0o600))
	return Options{
		ID: "nushell-startup", Shell: shell, WorkingDir: dir, InitialCommandDir: dataDir, Cols: 220, Rows: 24,
		ExtraEnv: []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_DATA_HOME=" + dir, "XDG_DATA_DIRS=" + dir},
	}
}

func TestInitialCommandPreservesNushellStateAndOutput(t *testing.T) {
	t.Parallel()
	opts := nushellInitialCommandOptions(t)
	releaseFile := filepath.Join(opts.WorkingDir, "startup-count")
	stateFile := filepath.Join(opts.WorkingDir, "startup-state")
	require.NoError(t, os.WriteFile(filepath.Join(opts.WorkingDir, "command"), []byte("exit 88"), 0o600))
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_TEST_RUNS="+releaseFile, "LEAPMUX_TEST_STATE="+stateFile)
	opts.InitialCommand = `
$env.LEAPMUX_TEST_COMMAND_STATE = (leapmux_login_value)
def leapmux_command_value [] { 'command value π' }
'run' | %save --raw --append $env.LEAPMUX_TEST_RUNS
'printed startup output'
`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, opts, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	testutil.RequireEventually(t, func() bool {
		screen, _ := term.ScreenSnapshot()
		return strings.Contains(string(screen), "printed startup output")
	}, "the initial command's returned value should appear in the terminal")
	require.NoError(t, term.SendInput([]byte(`$"($nu.pid):($env.LEAPMUX_TEST_COMMAND_STATE):(leapmux_command_value):($env.LEAPMUX_TEST_PROMPTS)" | %save --raw --force $env.LEAPMUX_TEST_STATE`+"\r")))
	wantState := fmt.Sprintf("%d:profile value:command value π:1", term.shellPID)
	testutil.RequireEventually(t, func() bool {
		state, err := os.ReadFile(stateFile)
		return err == nil && string(state) == wantState
	}, "the original Nushell should retain its startup state and the first prompt hook")
	require.NoError(t, term.SendInput([]byte(`$env.LEAPMUX_TEST_PROMPTS | into string | %save --raw --force $env.LEAPMUX_TEST_STATE`+"\r")))
	testutil.RequireEventually(t, func() bool {
		state, err := os.ReadFile(stateFile)
		return err == nil && string(state) == "2"
	}, "the next prompt should still run the user's hook")
	got, err := os.ReadFile(releaseFile)
	require.NoError(t, err)
	assert.Equal(t, "run", string(got))
}

func TestInitialCommandCanExitNushell(t *testing.T) {
	t.Parallel()
	opts := nushellInitialCommandOptions(t)
	opts.InitialCommand = "exit 7"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var output testutil.LogBuffer
	term, err := Start(ctx, opts, quartz.NewReal(), func(chunk Output) {
		_, _ = output.Write(chunk.Data)
	})
	if err != nil {
		t.Fatalf("Nushell startup failed: %v\n%s", err, output.String())
	}
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	select {
	case <-term.exitCh:
		assert.Equal(t, 7, term.Wait())
	case <-ctx.Done():
		t.Fatal("Nushell did not exit")
	}
}

func TestInitialCommandResolvesARelativeWorkerDirectoryForNushell(t *testing.T) {
	opts := nushellInitialCommandOptions(t)
	t.Chdir(opts.WorkingDir)
	opts.InitialCommandDir = filepath.Base(opts.InitialCommandDir)
	opts.WorkingDir = filepath.Join(opts.WorkingDir, "worktree")
	require.NoError(t, os.Mkdir(opts.WorkingDir, 0o700))
	opts.InitialCommand = "exit 19"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, opts, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	select {
	case <-term.exitCh:
		assert.Equal(t, 19, term.Wait())
	case <-ctx.Done():
		t.Fatal("Nushell did not exit")
	}
}

func TestInitialCommandDoesNotReachNushellWhenReleaseIsDenied(t *testing.T) {
	t.Parallel()
	opts := nushellInitialCommandOptions(t)
	witness := filepath.Join(opts.WorkingDir, "released")
	opts.InitialCommand = "'released' | %save --raw released"
	refusal := errors.New("the creation claim was refused")
	opts.ReleaseInitialCommand = func(context.Context, func() error) error {
		assert.FileExists(t, filepath.Join(opts.WorkingDir, "profile-ready"))
		root := filepath.Join(opts.InitialCommandDir, initialCommandLaunchRoot)
		entries, err := os.ReadDir(root)
		assert.NoError(t, err)
		if assert.Len(t, entries, 1) {
			assert.NoFileExists(t, filepath.Join(root, entries[0].Name(), "command"))
		}
		return refusal
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, opts, quartz.NewReal(), func(Output) {})
	if term != nil {
		defer term.Stop()
	}
	require.ErrorIs(t, err, refusal)
	assert.Nil(t, term)
	assert.NoFileExists(t, witness)
	entries, err := os.ReadDir(filepath.Join(opts.InitialCommandDir, initialCommandLaunchRoot))
	require.NoError(t, err)
	assert.Empty(t, entries)
}
