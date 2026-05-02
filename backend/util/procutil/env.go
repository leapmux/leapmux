package procutil

import (
	"os"
	"os/exec"
	"strings"
)

// AppImageEnvKeys are env vars set by the Linux AppImage runtime that must
// not leak into shells we spawn for the user. ARGV0 is the critical one:
// zsh reads it and uses it as argv[0] of every external command it execs
// (see AppImageKit#852), so when an rc-file activation of mise then execs
// e.g. `claude`, the mise shim sees argv[0] = the AppImage filename and
// bails with "<file>.AppImage is not a valid shim" (jdx/mise#3537). The
// others are scrubbed for hygiene — they expose AppImage internals to
// user-space tools that have no business knowing.
var AppImageEnvKeys = []string{"ARGV0", "APPIMAGE", "APPDIR", "OWD"}

// FilterEnv returns a copy of environ with entries matching any of the
// given key names removed. Keys are matched case-insensitively against
// the portion before the first '='.
func FilterEnv(environ []string, keys ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		skip := false
		for _, k := range keys {
			if strings.EqualFold(name, k) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// ScrubAppImageEnv strips AppImage-injected env vars from cmd's
// environment, but only when the parent process itself is running
// inside an AppImage (detected via APPIMAGE on os.Environ). Outside
// an AppImage the call is a no-op so cmd.Env keeps whatever value
// the caller had — including the conventional nil that tells exec to
// inherit the parent's environment verbatim.
//
// Operates on cmd.Environ(), which folds in any cmd.Env the caller has
// already set. Callers can therefore set cmd.Env first and then call
// ScrubAppImageEnv without losing their additions.
func ScrubAppImageEnv(cmd *exec.Cmd) {
	if os.Getenv("APPIMAGE") == "" {
		return
	}
	cmd.Env = FilterEnv(cmd.Environ(), AppImageEnvKeys...)
}

// ScrubAppImageEnvSlice is the slice-based variant of ScrubAppImageEnv
// for command types that don't expose an *exec.Cmd shape (e.g.
// go-pty's Cmd). When the parent process is itself inside an AppImage
// it returns env with AppImageEnvKeys removed; otherwise it returns
// env unchanged so callers can pass through their pre-seeded slice
// (or nil) without disturbing inherit-parent-env semantics.
func ScrubAppImageEnvSlice(env []string) []string {
	if os.Getenv("APPIMAGE") == "" {
		return env
	}
	return FilterEnv(env, AppImageEnvKeys...)
}
