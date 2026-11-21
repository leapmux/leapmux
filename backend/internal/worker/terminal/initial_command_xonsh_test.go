//go:build !windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestXonshInitialCommandPreservesNativeProfileState(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("xonsh")
	if err != nil {
		t.Skip("Xonsh is unavailable")
	}
	dir := t.TempDir()
	rc := filepath.Join(dir, "login profile.xsh")
	rcDir := filepath.Join(dir, "profiles")
	receipt := filepath.Join(dir, "receipt")
	require.NoError(t, os.Mkdir(rcDir, 0o700))
	require.NoError(t, os.WriteFile(rc, []byte(`
assert $XONSH_LOGIN and $XONSH_INTERACTIVE
leapmux_seed = 6
@events.on_post_rc
def leapmux_test_loaded_files(**_):
    globals()["leapmux_profiles_loaded"] = len(__xonsh__.rc_files)
@events.on_pre_cmdloop
def leapmux_test_prepare_prompt(**_):
    globals()["leapmux_prompt_value"] = 1
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(rcDir, "01-extra.py"), []byte("leapmux_seed *= 7\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(rcDir, "02-extra.xsh"), []byte("leapmux_seed += 1\n"), 0o600))
	term, err := Start(testutil.DeadlineContext(t), Options{
		ID: "xonsh-startup", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		InitialCommand: `
leapmux_startup_snapshot = leapmux_seed + leapmux_prompt_value
def leapmux_test_startup_value():
    return leapmux_startup_snapshot
`,
		ExtraEnv: append(xonshStartupTestEnv(dir, rc, rcDir), "LEAPMUX_TEST_RECEIPT="+receipt),
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	require.NoError(t, term.SendInput([]byte(
		`import os; from pathlib import Path; `+
			`assert not any(key.startswith("LEAPMUX_INITIAL_COMMAND_") for key in tuple(os.environ) + tuple(__xonsh__.env)); `+
			`Path($LEAPMUX_TEST_RECEIPT + ".pending").write_text(f"{os.getpid()}:{leapmux_test_startup_value()}:{leapmux_profiles_loaded}"); `+
			`os.replace($LEAPMUX_TEST_RECEIPT + ".pending", $LEAPMUX_TEST_RECEIPT)`+"\r",
	)))
	testutil.RequireEventually(t, func() bool {
		_, err := os.Stat(receipt)
		return err == nil
	})
	result, err := os.ReadFile(receipt)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d:44:3", term.shellPID), string(result))
}

func TestXonshInitialCommandCanExitTheShell(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("xonsh")
	if err != nil {
		t.Skip("Xonsh is unavailable")
	}
	dir := t.TempDir()
	ctx := testutil.DeadlineContext(t)
	term, err := Start(ctx, Options{
		ID: "xonsh-exit", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		ExtraEnv:       xonshStartupTestEnv(dir, filepath.Join(dir, "no-rc"), filepath.Join(dir, "no-rc-dir")),
		InitialCommand: "exit 19",
	}, quartz.NewReal(), func(Output) {})
	require.NoError(t, err)
	t.Cleanup(term.Stop)
	select {
	case <-term.exitCh:
		assert.Equal(t, 19, term.Wait())
	case <-ctx.Done():
		t.Fatal("Xonsh did not exit")
	}
}

func TestXonshInitialCommandRefusalKeepsThePayloadPrivate(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("xonsh")
	if err != nil {
		t.Skip("Xonsh is unavailable")
	}
	dir := t.TempDir()
	effect := filepath.Join(dir, "effect")
	refused := errors.New("the terminal creation lost its owner")
	called := false
	term, err := Start(testutil.DeadlineContext(t), Options{
		ID: "xonsh-refused", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		ExtraEnv:       append(xonshStartupTestEnv(dir, filepath.Join(dir, "no-rc"), filepath.Join(dir, "no-rc-dir")), "LEAPMUX_TEST_EFFECT="+effect),
		InitialCommand: `from pathlib import Path; Path($LEAPMUX_TEST_EFFECT).touch()`,
		ReleaseInitialCommand: func(context.Context, func() error) error {
			called = true
			return refused
		},
	}, quartz.NewReal(), func(Output) {})
	require.ErrorIs(t, err, refused)
	assert.True(t, called)
	assert.Nil(t, term)
	assert.NoFileExists(t, effect)
	files, err := os.ReadDir(filepath.Join(dir, initialCommandLaunchRoot))
	require.NoError(t, err)
	assert.Empty(t, files)
}

func xonshStartupTestEnv(dir, rc, rcDir string) []string {
	return []string{
		"HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_DATA_HOME=" + dir, "XDG_CACHE_HOME=" + dir,
		"XONSHRC=" + rc, "XONSHRC_DIR=" + rcDir, "XONSH_HISTORY_BACKEND=dummy",
	}
}
