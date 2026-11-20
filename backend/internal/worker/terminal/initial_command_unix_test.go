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
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestInitialCommandCannotReachAForegroundStartupChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shell := filepath.Join(dir, "bash")
	capture := filepath.Join(dir, "startup-child-input")
	testutil.WriteExecutable(t, shell, `#!/bin/sh
(
  printf '\033[?2004h'
  if IFS= read -r input; then
    printf '%s' "$input" > "$LEAPMUX_TEST_STARTUP_CAPTURE"
  fi
)
`)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, startErr := Start(ctx, Options{
		ID: "startup", Shell: shell, WorkingDir: dir,
		InitialCommand: "printf RUN_IN_THE_ORIGINAL_SHELL",
		ExtraEnv:       []string{"LEAPMUX_TEST_STARTUP_CAPTURE=" + capture},
	}, quartz.NewReal(), func(Output) {})
	if term != nil {
		defer term.Stop()
		exited := make(chan struct{})
		go func() {
			term.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-ctx.Done():
			t.Fatal("the terminal reported successful startup while its child still waited")
		}
	}
	received, err := os.ReadFile(capture)
	assert.ErrorIs(t, err, os.ErrNotExist, "the startup child read %q from the PTY", received)
	require.Error(t, startErr, "the shell did not reach an execution point for the initial command")
}

func TestInitialCommandRunsInTheOriginalShellAndKeepsItsState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "shell-state")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	term, err := Start(ctx, Options{
		ID: "initial-state", Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "leapmux_example_state='chosen value with spaces'\nexport leapmux_example_state",
		ExtraEnv:       []string{"LEAPMUX_TEST_SHELL_STATE=" + out, "ENV="},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	require.NoError(t, term.SendInput([]byte(`printf '%s:%s' "$$" "$leapmux_example_state" > "$LEAPMUX_TEST_SHELL_STATE"`+"\r")))
	want := fmt.Sprintf("%d:chosen value with spaces", term.shellPID)
	testutil.RequireEventually(t, func() bool {
		data, err := os.ReadFile(out)
		return err == nil && string(data) == want
	}, "the original shell should retain the initial command's variable")
}

func TestInitialCommandClaimFailureDoesNotExposeTheCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claimFailure := errors.New("the creation is closed")
	term, err := Start(t.Context(), Options{
		ID: "failed-claim", Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand:        "printf release",
		ReleaseInitialCommand: func(context.Context, func() error) error { return claimFailure },
	}, quartz.NewReal(), func(Output) {})
	require.ErrorIs(t, err, claimFailure)
	assert.Nil(t, term)
	entries, err := os.ReadDir(filepath.Join(dir, initialCommandLaunchRoot))
	require.NoError(t, err)
	assert.Empty(t, entries, "failed admission leaves no launch directory or payload")
}

func TestInitialCommandReleaseSeesTheRegisteredTerminal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manager := NewManager()
	const terminalID = "registered-startup"
	var wasRegistered bool
	err := manager.StartTerminal(t.Context(), Options{
		ID: terminalID, Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "printf release",
		ExtraEnv:       []string{"ENV="},
		ReleaseInitialCommand: func(_ context.Context, expose func() error) error {
			wasRegistered = manager.HasTerminal(terminalID)
			files, err := os.ReadDir(filepath.Join(dir, initialCommandLaunchRoot))
			if err != nil {
				return err
			}
			require.Len(t, files, 1)
			assert.NoFileExists(t, filepath.Join(dir, initialCommandLaunchRoot, files[0].Name(), "command"))
			return expose()
		},
	}, func(Output) {}, nil)
	require.NoError(t, err)
	testutil.RegisterTerminalCleanup(t, manager, terminalID)
	assert.True(t, wasRegistered, "the initial command must be able to reach its own registered PTY")
}

func TestInitialCommandPreservesExpandedPOSIXEnvironmentProfile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "profile")
	require.NoError(t, os.WriteFile(path, []byte("LEAPMUX_TEST_PROFILE_VALUE=loaded\n"), 0o600))
	receipt := filepath.Join(dir, "receipt")
	term, err := Start(t.Context(), Options{
		ID: "sh-profile", Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: `printf '%s' "$LEAPMUX_TEST_PROFILE_VALUE" > "$LEAPMUX_TEST_RECEIPT.pending" && mv "$LEAPMUX_TEST_RECEIPT.pending" "$LEAPMUX_TEST_RECEIPT"`,
		ExtraEnv: []string{
			"ENV=${LEAPMUX_TEST_PROFILE_DIR}/profile", "LEAPMUX_TEST_PROFILE_DIR=" + dir,
			"LEAPMUX_TEST_RECEIPT=" + receipt,
		},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	testutil.RequireEventually(t, func() bool {
		_, err := os.Stat(receipt)
		return err == nil
	}, "the shell should write its receipt")
	result, err := os.ReadFile(receipt)
	require.NoError(t, err)
	assert.Equal(t, "loaded", string(result))
}

func TestInitialCommandKeepsZshTypesetState(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	dir := t.TempDir()
	receipt := filepath.Join(dir, "zsh-state")
	term, err := Start(t.Context(), Options{
		ID: "zsh-state", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "typeset leapmux_probe_state=retained",
		ExtraEnv:       []string{"ZDOTDIR=" + dir, "LEAPMUX_TEST_RECEIPT=" + receipt},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	require.NoError(t, term.SendInput([]byte(`printf '%s' "$leapmux_probe_state" > "$LEAPMUX_TEST_RECEIPT.pending" && mv "$LEAPMUX_TEST_RECEIPT.pending" "$LEAPMUX_TEST_RECEIPT"`+"\r")))
	testutil.RequireEventually(t, func() bool {
		_, err := os.Stat(receipt)
		return err == nil
	}, "the shell should write its receipt")
	got, err := os.ReadFile(receipt)
	require.NoError(t, err)
	assert.Equal(t, "retained", string(got))
}

func TestInitialCommandPreservesBashProfilesAndPromptHooks(t *testing.T) {
	shells := []string{"/bin/bash"}
	if shell, err := exec.LookPath("bash"); err == nil && shell != "/bin/bash" {
		shells = append(shells, shell)
	}
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skip("Bash is unavailable")
			}
			t.Parallel()
			dir := t.TempDir()
			receipt := filepath.Join(dir, "bash-state")
			profile := `LEAPMUX_TEST_PROFILE_STATE=profile
if (( BASH_VERSINFO[0] >= 5 )); then
  PROMPT_COMMAND=('LEAPMUX_TEST_PROMPT_STATE=first' 'LEAPMUX_TEST_PROMPT_STATE=$LEAPMUX_TEST_PROMPT_STATE-second')
else
  PROMPT_COMMAND='LEAPMUX_TEST_PROMPT_STATE=first-second'
fi
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".bash_profile"), []byte(profile), 0o600))
			term, err := Start(t.Context(), Options{
				ID: "bash-profile", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
				InitialCommand: `declare LEAPMUX_TEST_COMMAND_STATE="$LEAPMUX_TEST_PROFILE_STATE-command"`,
				ExtraEnv:       []string{"HOME=" + dir, "LEAPMUX_TEST_RECEIPT=" + receipt},
			}, quartz.NewReal(), func(Output) {})
			require.NoError(t, err)
			t.Cleanup(func() {
				term.Stop()
				term.Wait()
			})
			require.NoError(t, term.SendInput([]byte(`printf '%s:%s' "$LEAPMUX_TEST_COMMAND_STATE" "$LEAPMUX_TEST_PROMPT_STATE" > "$LEAPMUX_TEST_RECEIPT"`+"\r")))
			testutil.RequireEventually(t, func() bool {
				got, err := os.ReadFile(receipt)
				return err == nil && string(got) == "profile-command:first-second"
			}, "the initial command must see the profile and leave the prompt hook intact")
		})
	}
}

func TestInitialCommandKeepsFishLocalState(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is unavailable")
	}
	dir := t.TempDir()
	receipt := filepath.Join(dir, "fish-state")
	const prompt = "LEAPMUX_FISH_READY"
	config := filepath.Join(dir, "fish")
	require.NoError(t, os.Mkdir(config, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.fish"), []byte("function fish_prompt; printf '"+prompt+"> '; end\n"), 0o600))
	term, err := Start(t.Context(), Options{
		ID: "fish-state", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "set -l leapmux_probe_state retained",
		ExtraEnv:       []string{"TERM=dumb", "XDG_CONFIG_HOME=" + dir, "LEAPMUX_TEST_RECEIPT=" + receipt},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	testutil.RequireEventually(t, func() bool {
		screen, _ := term.ScreenSnapshot()
		return strings.Contains(string(screen), prompt)
	}, "fish should finish configuring its first prompt")
	require.NoError(t, term.SendInput([]byte(`printf '%s' "$leapmux_probe_state" > "$LEAPMUX_TEST_RECEIPT"`+"\r")))
	testutil.RequireEventually(t, func() bool {
		got, err := os.ReadFile(receipt)
		return err == nil && string(got) == "retained"
	}, "the initial command's local assignment must live at the interactive level")
}

func TestInitialCommandPreservesQuotesInPOSIXEnvironmentPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	receipt := filepath.Join(dir, "receipt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, `quoted"profile`),
		[]byte("LEAPMUX_TEST_PROFILE_VALUE=quoted\n"), 0o600))
	term, err := Start(t.Context(), Options{
		ID: "quoted-env-profile", Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: `printf '%s' "$LEAPMUX_TEST_PROFILE_VALUE" > "$LEAPMUX_TEST_RECEIPT"`,
		ExtraEnv: []string{
			`ENV=${LEAPMUX_TEST_PROFILE_DIR}/quoted"profile`,
			"LEAPMUX_TEST_PROFILE_DIR=" + dir, "LEAPMUX_TEST_RECEIPT=" + receipt,
		},
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	testutil.RequireEventually(t, func() bool {
		result, err := os.ReadFile(receipt)
		return err == nil && string(result) == "quoted"
	}, "the original ENV path can contain a literal double quote")
}

func TestConcurrentInitialCommandsCannotReleaseForTheSameTerminal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	markers := filepath.Join(dir, "markers")
	require.NoError(t, os.Mkdir(markers, 0o700))
	gate := filepath.Join(dir, "continue")
	profile := filepath.Join(dir, "profile")
	require.NoError(t, os.WriteFile(profile, []byte(`
printf 'waiting\n' > "$LEAPMUX_TEST_MARKERS/$$"
while [ ! -f "$LEAPMUX_TEST_GATE" ]; do
  command sleep 0.02 || break
done
`), 0o600))

	manager := NewManager()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var releases atomic.Int32
	opts := Options{
		ID: "concurrent-command", Shell: "/bin/sh", WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: "exit",
		ExtraEnv: []string{
			"ENV=" + profile, "LEAPMUX_TEST_MARKERS=" + markers, "LEAPMUX_TEST_GATE=" + gate,
		},
		ReleaseInitialCommand: func(_ context.Context, expose func() error) error {
			releases.Add(1)
			return expose()
		},
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- manager.StartTerminal(ctx, opts, func(Output) {}, nil) }()
	}
	testutil.RequireEventually(t, func() bool {
		entries, err := os.ReadDir(markers)
		return err == nil && len(entries) == 2
	}, "both shells are waiting before either can register")
	require.NoError(t, os.WriteFile(gate, nil, 0o600))

	failures := 0
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				failures++
				assert.ErrorContains(t, err, "terminal already exists")
			}
		case <-ctx.Done():
			t.Fatal("the starts did not settle after the gate opened")
		}
	}
	testutil.RegisterTerminalCleanup(t, manager, opts.ID)
	assert.Equal(t, 1, failures)
	assert.Equal(t, int32(1), releases.Load())
}
