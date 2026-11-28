//go:build !windows

package terminal

import (
	"context"
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
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func elvishInitialCommandOptions(t *testing.T) (Options, string) {
	t.Helper()
	shell, err := exec.LookPath("elvish")
	if err != nil {
		t.Skip("Elvish is unavailable")
	}
	root := t.TempDir()
	config := filepath.Join(root, `profile "data" π`)
	worktree := filepath.Join(root, "worktree")
	dataDir := filepath.Join(root, "worker")
	profileDir := filepath.Join(config, "elvish")
	runtimeDir := filepath.Dir(strings.TrimPrefix(locallistentest.UniqueListenURL(t, "elvish"), "unix:"))
	for _, directory := range []string{worktree, dataDir, profileDir} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}
	// Elvish can reuse the host daemon when this directory does not exist.
	require.NoError(t, os.Mkdir(filepath.Join(runtimeDir, "elvish"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "state.elv"), []byte("var start = (num 5)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(config, "common.elv"), []byte("var label = 'relative import'\n"), 0o600))
	profile := filepath.Join(profileDir, "rc.elv")
	require.NoError(t, os.WriteFile(profile, []byte(`
use ./state
use ../common
var counter = $state:start
fn advance { set counter = (+ $counter 1) }
fn slurp { fail 'the profile shadows slurp' }
set edit:before-readline = [{ advance }]
set edit:prompt = { put 'ELVISH_STARTUP_PROMPT> ' }
echo ready > $E:LEAPMUX_TEST_PROFILE_READY
`), 0o600))
	return Options{
		ID: "elvish-startup", Shell: shell, WorkingDir: worktree, InitialCommandDir: dataDir, Cols: 220, Rows: 24,
		ExtraEnv: []string{
			"HOME=" + root, "XDG_CONFIG_HOME=" + config,
			"XDG_DATA_HOME=" + root, "XDG_DATA_DIRS=" + root, "XDG_STATE_HOME=" + root,
			"XDG_RUNTIME_DIR=" + runtimeDir, "TMPDIR=" + runtimeDir,
			"LEAPMUX_TEST_PROFILE_READY=" + filepath.Join(worktree, "profile-ready"),
		},
	}, profile
}

func TestInitialCommandPreservesElvishProfileAndShellState(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	profileBytes, err := os.ReadFile(profile)
	require.NoError(t, err)
	runs := filepath.Join(opts.WorkingDir, "runs")
	state := filepath.Join(opts.WorkingDir, "shell-state")
	history := filepath.Join(opts.WorkingDir, "history")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_TEST_RUNS="+runs, "LEAPMUX_TEST_STATE="+state, "LEAPMUX_TEST_HISTORY="+history)
	opts.InitialCommand = `advance
var value = 'initial-only-value π'
echo ran >> $E:LEAPMUX_TEST_RUNS
echo 'ELVISH_STARTUP_OUTPUT'
`

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var output testutil.LogBuffer
	term, err := Start(ctx, opts, quartz.NewReal(), func(chunk Output) { _, _ = output.Write(chunk.Data) })
	require.NoError(t, err, "%s", output.String())
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	testutil.RequireEventually(t, func() bool {
		screen, _ := term.ScreenSnapshot()
		return strings.Contains(string(screen), "ELVISH_STARTUP_OUTPUT") &&
			strings.Contains(string(screen), "ELVISH_STARTUP_PROMPT")
	}, "the original shell should execute the initial command and return to the profile prompt")
	require.NoError(t, term.SendInput([]byte(`echo $pid $counter $common:label $value (has-env LEAPMUX_INITIAL_COMMAND_FILE) > $E:LEAPMUX_TEST_STATE; edit:command-history &cmd-only | to-lines > $E:LEAPMUX_TEST_HISTORY`+"\r")))
	want := fmt.Sprintf("%d 8 relative import initial-only-value π $false", term.shellPID)
	testutil.RequireEventually(t, func() bool {
		got, err := os.ReadFile(state)
		return err == nil && strings.TrimSpace(string(got)) == want
	}, "the interactive shell should see the shared profile bindings and initial command declarations")
	testutil.RequireEventually(t, func() bool {
		got, err := os.ReadFile(history)
		return err == nil && strings.Contains(string(got), "LEAPMUX_TEST_HISTORY")
	}, "Elvish should be able to read its command history")
	recorded, err := os.ReadFile(history)
	require.NoError(t, err)
	assert.NotContains(t, string(recorded), "initial-only-value")
	released, err := os.ReadFile(runs)
	require.NoError(t, err)
	assert.Equal(t, "ran\n", string(released))
	after, err := os.ReadFile(profile)
	require.NoError(t, err)
	assert.Equal(t, profileBytes, after)
	profileEntries, err := os.ReadDir(filepath.Dir(profile))
	require.NoError(t, err)
	assert.Len(t, profileEntries, 2, "the profile directory should hold only its original files")
}

func TestInitialCommandCanExitElvishWithoutAProfile(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	require.NoError(t, os.RemoveAll(filepath.Dir(profile)))
	opts.InitialCommand = "exit 23"
	assertInitialCommandExits(t, opts, 23, "Elvish")
}

func TestInitialCommandDoesNotReachElvishWhenReleaseIsDenied(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	opts.InitialCommand = "echo released > released"
	assertInitialCommandRefusalKeepsPrivate(t, opts, filepath.Join(opts.WorkingDir, "released"), func() {
		assert.FileExists(t, filepath.Join(opts.WorkingDir, "profile-ready"))
		entries, err := os.ReadDir(filepath.Join(opts.InitialCommandDir, initialCommandLaunchRoot))
		assert.NoError(t, err)
		if assert.Len(t, entries, 1) {
			assert.NoFileExists(t, filepath.Join(opts.InitialCommandDir, initialCommandLaunchRoot, entries[0].Name(), "command"))
		}
	})
	entries, err := os.ReadDir(filepath.Dir(profile))
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestInitialCommandDoesNotWriteToAnElvishStartupChild(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	child := filepath.Join(opts.WorkingDir, "profile-child")
	entered := filepath.Join(opts.WorkingDir, "child-entered")
	captured := filepath.Join(opts.WorkingDir, "child-input")
	testutil.WriteExecutable(t, child, `#!/bin/sh
printf ready > "$LEAPMUX_TEST_ENTERED"
if IFS= read -r input; then
  printf '%s' "$input" > "$LEAPMUX_TEST_CAPTURED"
fi
`)
	require.NoError(t, os.WriteFile(profile, []byte("(external $E:LEAPMUX_TEST_CHILD)\n"), 0o600))
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_TEST_CHILD="+child, "LEAPMUX_TEST_ENTERED="+entered, "LEAPMUX_TEST_CAPTURED="+captured)
	opts.InitialCommand = "echo released > released"

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type result struct {
		term *Terminal
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		defer close(finished)
		term, err := Start(ctx, opts, quartz.NewReal(), func(Output) {})
		finished <- result{term, err}
	}()
	t.Cleanup(func() {
		select {
		case started, received := <-finished:
			if received && started.term != nil {
				started.term.Stop()
				started.term.Wait()
			}
		case <-time.After(30 * time.Second):
			t.Error("the canceled startup did not stop")
		}
	})
	testutil.RequireEventually(t, func() bool {
		data, err := os.ReadFile(entered)
		return err == nil && string(data) == "ready"
	}, "the foreground process should read from the terminal before the startup hook")
	cancel()
	select {
	case started := <-finished:
		if started.term != nil {
			started.term.Stop()
			started.term.Wait()
		}
		assert.Nil(t, started.term)
		require.ErrorIs(t, started.err, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("the canceled startup did not stop")
	}
	assert.NoFileExists(t, captured)
	assert.NoFileExists(t, filepath.Join(opts.WorkingDir, "released"))
}

func TestReapElvishStartupLinkPreservesAReplacement(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	abandoned, err := newInitialCommandBootstrap(opts.InitialCommandDir)
	require.NoError(t, err)
	t.Cleanup(abandoned.close)
	ownedLink, err := abandoned.elvishStartupSource(opts.WorkingDir, opts.ExtraEnv)
	require.NoError(t, err)
	replaced, err := newInitialCommandBootstrap(opts.InitialCommandDir)
	require.NoError(t, err)
	t.Cleanup(replaced.close)
	replacedLink, err := replaced.elvishStartupSource(opts.WorkingDir, opts.ExtraEnv)
	require.NoError(t, err)
	require.NoError(t, os.Remove(replacedLink))
	require.NoError(t, os.Symlink(profile, replacedLink))

	require.NoError(t, ReapInitialCommandLaunches(opts.InitialCommandDir))
	_, err = os.Lstat(ownedLink)
	assert.ErrorIs(t, err, os.ErrNotExist)
	target, err := os.Readlink(replacedLink)
	require.NoError(t, err)
	assert.Equal(t, profile, target)
	assert.FileExists(t, profile)
	assert.NoDirExists(t, abandoned.dir)
	assert.NoDirExists(t, replaced.dir)
}

func TestElvishProfilePathUsesTheShellWorkingDirectory(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	home := t.TempDir()
	profile, err := elvishProfilePath(worktree, []string{"HOME=" + home})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".config", "elvish", "rc.elv"), profile)

	profile, err = elvishProfilePath(worktree, []string{"HOME=" + home, "XDG_CONFIG_HOME=profile"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(worktree, "profile", "elvish", "rc.elv"), profile)

	profile, err = elvishProfilePath(worktree, []string{"HOME=/"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/", ".config", "elvish", "rc.elv"), profile)
}

func TestElvishStartupSourceRequiresARegularProfile(t *testing.T) {
	t.Parallel()
	opts, profile := elvishInitialCommandOptions(t)
	require.NoError(t, os.Remove(profile))
	require.NoError(t, os.Mkdir(profile, 0o700))
	bootstrap, err := newInitialCommandBootstrap(opts.InitialCommandDir)
	require.NoError(t, err)
	t.Cleanup(bootstrap.close)

	_, err = bootstrap.elvishStartupSource(opts.WorkingDir, opts.ExtraEnv)
	require.ErrorContains(t, err, "is not a regular file")
	assert.NoFileExists(t, filepath.Join(bootstrap.dir, elvishStartupFile))
	assert.DirExists(t, profile)
}
