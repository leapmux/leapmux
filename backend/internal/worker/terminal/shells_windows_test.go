//go:build windows

package terminal

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Windows mirrors of the shell-env tests. On Windows, "absolute" paths
// require a drive letter, and exec.LookPath honours PATHEXT (so a bare
// "cmd" resolves to ...\cmd.exe).

func TestResolveDefaultShell_PrefersLeapmuxEnv(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", `C:\test-leapmux-shell.exe`)
	t.Setenv("SHELL", `C:\other-shell.exe`)
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, `C:\test-leapmux-shell.exe`, shell)
}

func TestResolveDefaultShell_LeapmuxEnvBareName(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "cmd")
	t.Setenv("SHELL", `C:\other-shell.exe`)
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.NotEmpty(t, shell, "bare name should be resolved")
	assert.True(t, filepath.IsAbs(shell), "resolved path should be absolute, got %q", shell)
	assert.True(t, strings.HasSuffix(strings.ToLower(shell), `\cmd.exe`), "resolved path should end with \\cmd.exe, got %q", shell)
}

func TestResolveDefaultShell_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", `C:\test-shell.exe`)
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, `C:\test-shell.exe`, shell, "ResolveDefaultShell should prefer $SHELL")
}

func TestResolveShellEnv_AbsolutePath(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", `C:\Windows\System32\cmd.exe`)
	assert.Equal(t, `C:\Windows\System32\cmd.exe`, resolveShellEnv("TEST_SHELL_ENV"))
}

func TestResolveShellEnv_BareNameResolved(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "cmd")
	result := resolveShellEnv("TEST_SHELL_ENV")
	assert.NotEmpty(t, result)
	assert.True(t, filepath.IsAbs(result), "expected absolute path, got %q", result)
	assert.True(t, strings.HasSuffix(strings.ToLower(result), `\cmd.exe`), "expected to end with \\cmd.exe, got %q", result)
}
