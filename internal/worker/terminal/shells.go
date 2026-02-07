package terminal

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var shellCache struct {
	once         sync.Once
	shells       []string
	defaultShell string
}

// resolveDefaultShell returns the user's default shell.  It checks the
// LEAPMUX_DEFAULT_SHELL environment variable first (accepting either a bare
// command name like "zsh" or an absolute path like "/bin/zsh"), then the SHELL
// environment variable, and finally falls back to platform-specific detection
// (e.g. dscl on macOS, /etc/passwd on Linux).
func resolveDefaultShell() string {
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
	shellCache.once.Do(func() {
		shellCache.defaultShell = resolveDefaultShell()

		// Place the default shell first so it appears at the top of
		// the UI dropdown.
		if shellCache.defaultShell != "" {
			shellCache.shells = append(shellCache.shells, shellCache.defaultShell)
		}

		knownShells := []string{"sh", "bash", "zsh", "fish"}
		for _, name := range knownShells {
			path, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			// Skip if it duplicates the default shell already at [0].
			if path == shellCache.defaultShell {
				continue
			}
			shellCache.shells = append(shellCache.shells, path)
		}

		slog.Info("available shells resolved", "shells", shellCache.shells, "default", shellCache.defaultShell)
	})

	return shellCache.shells, shellCache.defaultShell
}
