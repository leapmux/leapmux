package terminal

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// resolveShells is swapped out by tests to invalidate the cache between
// cases that mutate environment variables.
var resolveShells = newShellsResolver()

func newShellsResolver() func() (shells []string, defaultShell string) {
	return sync.OnceValues(computeShells)
}

func computeShells() (shells []string, defaultShell string) {
	defaultShell = resolveDefaultShellOnce()
	// Place the default shell first so it appears at the top of
	// the UI dropdown.
	if defaultShell != "" {
		shells = append(shells, defaultShell)
	}

	knownShells := []string{"sh", "bash", "zsh", "fish", "pwsh", "powershell"}
	defaultBase := strings.ToLower(strings.TrimSuffix(filepath.Base(defaultShell), ".exe"))
	for _, name := range knownShells {
		if name == defaultBase {
			continue
		}
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		shells = append(shells, path)
	}

	slog.Info("available shells resolved", "shells", shells, "default", defaultShell)
	return shells, defaultShell
}

// ResolveDefaultShell returns the user's default shell.  It checks the
// LEAPMUX_DEFAULT_SHELL environment variable first (accepting either a bare
// command name like "zsh" or an absolute path like "/bin/zsh"), then the SHELL
// environment variable, and finally falls back to platform-specific detection
// (e.g. dscl on macOS, /etc/passwd on Linux).
func ResolveDefaultShell() string {
	_, def := resolveShells()
	return def
}

func resolveDefaultShellOnce() string {
	if shell := resolveShellEnv("LEAPMUX_DEFAULT_SHELL"); shell != "" {
		slog.Info("default shell from LEAPMUX_DEFAULT_SHELL", "shell", shell)
		return shell
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		slog.Info("default shell from $SHELL", "shell", shell)
		return shell
	}
	shell := detectDefaultShell()
	slog.Info("default shell from platform detection", "shell", shell)
	return shell
}

// resolveShellEnv reads the named environment variable and, if it contains a
// bare command name (no path separator), resolves it to an absolute path via
// exec.LookPath.  Returns "" when the variable is unset/empty or lookup fails.
func resolveShellEnv(name string) string {
	val := os.Getenv(name)
	if val == "" {
		return ""
	}
	if filepath.IsAbs(val) {
		return val
	}
	// Bare command name – resolve via PATH.
	abs, err := exec.LookPath(val)
	if err != nil {
		slog.Info("failed to resolve shell env via LookPath", "env", name, "value", val, "error", err)
		return ""
	}
	return abs
}

// ListAvailableShells returns the shells available on the system and the
// default shell.  Results are cached after the first call.
//
// Each well-known shell name is resolved via exec.LookPath which respects the
// PATH environment variable.  Different names (e.g. "sh" vs "bash") are kept
// as separate entries even if the underlying binary is the same, because
// invoking as "sh" activates POSIX mode.
func ListAvailableShells() (shells []string, defaultShell string) {
	return resolveShells()
}
