package procutil

import (
	"os"
	"os/exec"
	"strings"
)

// AppImageEnvKeys are env vars set by the Linux AppImage runtime (the
// linuxdeploy AppRun binary and its apprun-hooks) that are scrubbed
// wholesale — the entry is removed from the env, not rewritten.
//
// Two reasons a var goes here rather than in AppDirPathListVars:
//
//  1. It's not a colon-separated path list (identity vars like
//     ARGV0/APPIMAGE/APPDIR/OWD, scalar settings like
//     PYTHONDONTWRITEBYTECODE/GTK_THEME/GST_REGISTRY_FORK, single
//     paths like GTK_DATA_PREFIX/GTK_IM_MODULE_FILE/PYTHONHOME).
//
//  2. It's a path list, but an apprun-hook *replaces* it after AppRun
//     sets it (no `:$ORIG` tail), so the user's original value is
//     already gone by the time we see the env. Surgical-strip would
//     buy us nothing — examples: GSETTINGS_SCHEMA_DIR (GTK hook
//     replaces), GST_PLUGIN_SYSTEM_PATH_1_0 (GStreamer hook
//     replaces), GTK_PATH (GTK hook replaces). Wholesale-drop
//     leaves the spawned shell to fall back to system defaults
//     instead of inheriting the hook's hardcoded host paths.
//
// ARGV0 is the most consequential identity var: zsh reads it and uses
// it as argv[0] of every external command it execs (AppImageKit#852),
// which breaks mise activation in user rc files (jdx/mise#3537).
//
// LD_PRELOAD is intentionally NOT in this list — AppRun does not set
// it, so any LD_PRELOAD in the env is the user's own (e.g. for malloc
// debugging or library shimming). Scrubbing it would silently destroy
// that customization.
var AppImageEnvKeys = []string{
	// Identity / metadata.
	"ARGV0", "APPIMAGE", "APPDIR", "OWD",
	// Scalars + single-path vars from AppRun (no `:$ORIG` tail).
	"PYTHONHOME", "PYTHONDONTWRITEBYTECODE",
	// Path lists where hook overwrites destroy user's original — wholesale-drop is fine.
	"GSETTINGS_SCHEMA_DIR",
	"GST_PLUGIN_SYSTEM_PATH_1_0",
	"GTK_PATH",
	// Vars introduced by the GStreamer apprun-hook.
	"GST_REGISTRY_FORK",
	"GST_PLUGIN_PATH_1_0",
	"GST_PLUGIN_SCANNER_1_0",
	"GST_PTP_HELPER_1_0",
	// Vars introduced by the GTK apprun-hook (single paths or scalars).
	"GTK_DATA_PREFIX", "GTK_THEME", "GTK_EXE_PREFIX",
	"GTK_IM_MODULE_FILE", "GDK_PIXBUF_MODULE_FILE", "GIO_EXTRA_MODULES",
	"GDK_BACKEND",
}

// AppDirPathListVars are colon-separated path-list env vars that
// AppRun (and in some cases the apprun-hooks) prepend AppDir-rooted
// entries to, while preserving the user's original value at the tail
// via a `:$ORIG` snprintf format. We strip only the AppDir-rooted
// entries instead of dropping the whole var, so the user's
// customizations survive into shells we spawn.
//
// AppRun's snprintf for PATH is the canonical example:
//
//	PATH=$APPDIR/usr/bin/:$APPDIR/usr/sbin/:$APPDIR/usr/games/:$APPDIR/bin/:$APPDIR/sbin/:$ORIG_PATH
//
// LD_LIBRARY_PATH, PERLLIB, PYTHONPATH, QT_PLUGIN_PATH,
// XDG_DATA_DIRS, and GST_PLUGIN_SYSTEM_PATH all use the same
// `prepended-AppDir-dirs:$ORIG` shape. (XDG_DATA_DIRS is also
// prepended-to a second time by the GTK hook, but that hook
// preserves the existing value at the tail too, so the surgical
// strip still recovers the user's original.)
//
// If after stripping a var has no surviving non-empty entries — i.e.
// the user originally had no value and AppRun's prepends were the
// only content — we drop the entry rather than leave it set-to-
// empty-or-colons, which can have surprising semantics (e.g. an
// empty LD_LIBRARY_PATH or a `:` in PATH meaning "current dir").
var AppDirPathListVars = []string{
	"PATH",
	"LD_LIBRARY_PATH",
	"PERLLIB",
	"PYTHONPATH",
	"QT_PLUGIN_PATH",
	"XDG_DATA_DIRS",
	"GST_PLUGIN_SYSTEM_PATH",
}

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
	env := FilterEnv(cmd.Environ(), AppImageEnvKeys...)
	cmd.Env = stripAppDirFromPathLists(env, os.Getenv("APPDIR"), AppDirPathListVars)
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
	return stripAppDirFromPathLists(FilterEnv(env, AppImageEnvKeys...), os.Getenv("APPDIR"), AppDirPathListVars)
}

// stripAppDirFromPathLists rewrites each named colon-separated
// path-list var in env, removing components rooted at appDir. If a
// var has no surviving non-empty components after stripping, the
// entry is dropped from env entirely (see AppDirPathListVars docs
// for why empty-but-set is undesirable).
//
// If appDir is empty the env is returned unchanged: without it we
// have no reliable way to identify AppRun's prepends, and a user
// path-list value may itself contain entries we'd risk
// false-stripping.
func stripAppDirFromPathLists(env []string, appDir string, varNames []string) []string {
	if appDir == "" || len(varNames) == 0 {
		return env
	}
	prefix := appDir + "/"
	isPathListVar := func(name string) bool {
		for _, v := range varNames {
			if strings.EqualFold(name, v) {
				return true
			}
		}
		return false
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !isPathListVar(name) {
			out = append(out, entry)
			continue
		}
		parts := strings.Split(value, ":")
		kept := parts[:0]
		anyNonEmpty := false
		for _, p := range parts {
			if p == appDir || strings.HasPrefix(p, prefix) {
				continue
			}
			kept = append(kept, p)
			if p != "" {
				anyNonEmpty = true
			}
		}
		if !anyNonEmpty {
			// Var was effectively unset originally — AppRun's prepends
			// were the only content. Drop the entry so the spawned
			// shell sees the var as unset rather than empty/colons.
			continue
		}
		out = append(out, name+"="+strings.Join(kept, ":"))
	}
	return out
}
