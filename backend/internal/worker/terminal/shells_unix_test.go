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
)

func TestListAvailableShellsDiscoversNushellAndElvish(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"sh", "elvish", "nu", "unlisted-shell"} {
		testutil.WriteExecutable(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
	}
	t.Setenv("PATH", dir)
	for _, name := range []string{"sh", "nu", "elvish"} {
		t.Run(name+" is default", func(t *testing.T) {
			t.Setenv("LEAPMUX_DEFAULT_SHELL", name)
			shells, defaultShell := newShellsResolver()()
			require.NotEmpty(t, shells)
			assert.Equal(t, filepath.Join(dir, name), defaultShell)
			assert.Equal(t, defaultShell, shells[0])
			assert.ElementsMatch(t, []string{
				filepath.Join(dir, "sh"), filepath.Join(dir, "nu"), filepath.Join(dir, "elvish"),
			}, shells)
		})
	}
}

func TestStartRunsNushellAndElvishInteractively(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ shell, config, profile, command, wantProfile string }{
		{
			"nu", "nushell/login.nu",
			`$env.config.show_banner = false
$env.PROMPT_COMMAND = {|| 'LEAPMUX_PROMPT' }
$env.LEAPMUX_TEST_PROFILE = 'nushell-login'
`,
			`$"($nu.pid) ($env.LEAPMUX_TEST_PROFILE)" | save --raw $env.LEAPMUX_TEST_RECEIPT`,
			"nushell-login",
		},
		{
			"elvish", "elvish/rc.elv",
			`set edit:prompt = { put 'LEAPMUX_PROMPT' }
var leapmux-test-profile = 'elvish-login'
`,
			`echo $pid $leapmux-test-profile > $E:LEAPMUX_TEST_RECEIPT`,
			"elvish-login",
		},
	} {
		t.Run(tt.shell, func(t *testing.T) {
			t.Parallel()
			shell, err := exec.LookPath(tt.shell)
			if err != nil {
				t.Skipf("%s is unavailable", tt.shell)
			}
			dir := t.TempDir()
			config := filepath.Join(dir, "config")
			profile := filepath.Join(config, tt.config)
			require.NoError(t, os.MkdirAll(filepath.Dir(profile), 0o700))
			require.NoError(t, os.WriteFile(profile, []byte(tt.profile), 0o600))
			receipt := filepath.Join(dir, "receipt")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var output testutil.LogBuffer
			term, err := Start(ctx, Options{
				ID: tt.shell, Shell: shell, WorkingDir: dir, Cols: 180, Rows: 24,
				ExtraEnv: []string{
					"HOME=" + dir, "XDG_CONFIG_HOME=" + config,
					"XDG_DATA_HOME=" + dir, "XDG_DATA_DIRS=" + dir, "XDG_STATE_HOME=" + dir,
					"TMPDIR=" + dir, "XDG_RUNTIME_DIR=" + dir,
					"LEAPMUX_TEST_RECEIPT=" + receipt,
				},
			}, quartz.NewReal(), func(chunk Output) { _, _ = output.Write(chunk.Data) })
			require.NoError(t, err)
			t.Cleanup(func() {
				term.Stop()
				term.Wait()
			})
			testutil.RequireEventually(t, func() bool {
				return strings.Contains(output.String(), "LEAPMUX_PROMPT") || term.IsExited()
			}, "the shell should reach the profile's prompt")
			require.False(t, term.IsExited(), "the shell exited before it could read a command: %s", output.String())
			require.NoError(t, term.SendInput([]byte(tt.command+"\r")))
			want := fmt.Sprintf("%d %s", term.shellPID, tt.wantProfile)
			testutil.RequireEventually(t, func() bool {
				got, err := os.ReadFile(receipt)
				return err == nil && strings.TrimSpace(string(got)) == want
			}, "the original shell evaluation should see the startup profile and its own PID")
		})
	}
}

func TestResolveDefaultShell_Unix_PrefersLeapMuxEnv(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "/bin/test-leapmux-shell")
	t.Setenv("SHELL", "/bin/other-shell")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, "/bin/test-leapmux-shell", shell)
}

func TestResolveDefaultShell_Unix_LeapMuxEnvBareName(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "sh")
	t.Setenv("SHELL", "/bin/other-shell")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.NotEmpty(t, shell, "bare name should be resolved")
	assert.True(t, strings.HasPrefix(shell, "/"), "resolved path should be absolute")
	assert.True(t, strings.HasSuffix(shell, "/sh"), "resolved path should end with /sh")
}

func TestResolveDefaultShell_Unix_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "/bin/test-shell")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, "/bin/test-shell", shell, "ResolveDefaultShell should prefer $SHELL")
}

func TestResolveShellEnv_Unix_AbsolutePath(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "/usr/bin/zsh")
	assert.Equal(t, "/usr/bin/zsh", resolveShellEnv("TEST_SHELL_ENV"))
}

func TestResolveShellEnv_Unix_BareNameResolved(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "sh")
	result := resolveShellEnv("TEST_SHELL_ENV")
	assert.NotEmpty(t, result)
	assert.True(t, filepath.IsAbs(result))
}
