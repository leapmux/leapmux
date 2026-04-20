//go:build !windows

package terminal

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveDefaultShell_Unix_PrefersLeapmuxEnv(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "/bin/test-leapmux-shell")
	t.Setenv("SHELL", "/bin/other-shell")
	resetShellCache()
	shell := ResolveDefaultShell()
	assert.Equal(t, "/bin/test-leapmux-shell", shell)
}

func TestResolveDefaultShell_Unix_LeapmuxEnvBareName(t *testing.T) {
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
