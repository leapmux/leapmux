package procutil

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterEnv_DropsMatchingKeys(t *testing.T) {
	got := FilterEnv(
		[]string{"FOO=1", "BAR=two", "BAZ=three"},
		"BAR",
	)
	assert.Equal(t, []string{"FOO=1", "BAZ=three"}, got)
}

func TestFilterEnv_CaseInsensitive(t *testing.T) {
	got := FilterEnv(
		[]string{"argv0=/foo", "ARGV0=/bar", "Path=/usr/bin"},
		"ARGV0",
	)
	assert.Equal(t, []string{"Path=/usr/bin"}, got, "lower- and mixed-case keys must both be dropped")
}

func TestFilterEnv_NoMatch_ReturnsCopy(t *testing.T) {
	in := []string{"FOO=1", "BAR=2"}
	got := FilterEnv(in, "ZZZ")
	assert.Equal(t, in, got)
	// Mutating the result must not touch the input.
	got[0] = "MUTATED=yes"
	assert.Equal(t, "FOO=1", in[0])
}

func TestFilterEnv_EmptyInputs(t *testing.T) {
	assert.Empty(t, FilterEnv(nil, "FOO"))
	assert.Equal(t, []string{}, FilterEnv([]string{}, "FOO"))
	// No keys → identity copy.
	assert.Equal(t, []string{"FOO=1"}, FilterEnv([]string{"FOO=1"}))
}

func TestFilterEnv_MalformedEntries(t *testing.T) {
	// strings.Cut on an entry without `=` returns the whole entry as the
	// name and an empty value — the filter should still match the name.
	got := FilterEnv(
		[]string{"NOEQUALS", "FOO=ok"},
		"NOEQUALS",
	)
	assert.Equal(t, []string{"FOO=ok"}, got)
}

// hasKey reports whether env contains an entry whose name (the part
// before '=') case-insensitively matches key.
func hasKey(env []string, key string) bool {
	for _, e := range env {
		if name, _, _ := strings.Cut(e, "="); strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// getValue returns the value of the first env entry whose name matches
// key (case-insensitive), or "", false if no such entry exists.
func getValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if name, val, ok := strings.Cut(e, "="); ok && strings.EqualFold(name, key) {
			return val, true
		}
	}
	return "", false
}

func TestScrubAppImageEnv_InsideAppImage_DropsKeys(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_xxxxxx")
	t.Setenv("ARGV0", "leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("OWD", "/home/user")
	t.Setenv("PATH_SHOULD_SURVIVE", "/usr/bin:/bin")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	require.NotNil(t, cmd.Env, "ScrubAppImageEnv must materialize cmd.Env when APPIMAGE is set")
	for _, key := range AppImageEnvKeys {
		assert.False(t, hasKey(cmd.Env, key), "env var %q should be scrubbed inside an AppImage", key)
	}
	assert.True(t, hasKey(cmd.Env, "PATH_SHOULD_SURVIVE"), "non-AppImage env vars must not be touched")
}

func TestScrubAppImageEnv_OutsideAppImage_PreservesEnv(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	t.Setenv("ARGV0", "should-survive-outside-appimage")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	// cmd.Env stays nil so exec inherits os.Environ() verbatim — no
	// blanket scrubbing when we're not inside an AppImage. This matches
	// the agent shell's pre-existing behavior (commit 5d430a3) so .deb
	// installs and dev runs are unaffected.
	assert.Nil(t, cmd.Env)
}

func TestScrubAppImageEnvSlice_InsideAppImage_DropsKeys(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	in := []string{
		"ARGV0=fake.AppImage",
		"APPIMAGE=/path/to/fake.AppImage",
		"APPDIR=/tmp/.mount_x",
		"OWD=/home/user",
		"TERM=xterm-256color",
		"PATH=/usr/bin:/bin",
	}
	got := ScrubAppImageEnvSlice(in)
	assert.Equal(t, []string{"TERM=xterm-256color", "PATH=/usr/bin:/bin"}, got)
}

func TestScrubAppImageEnvSlice_OutsideAppImage_ReturnsInputUnchanged(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	in := []string{"ARGV0=should-survive", "TERM=xterm-256color"}
	got := ScrubAppImageEnvSlice(in)
	// Outside an AppImage we want a pass-through: no allocation, no
	// scrub. Pointer equality (well, slice header equality) is the
	// strongest assertion go offers here.
	require.Len(t, got, len(in))
	for i := range in {
		assert.Same(t, &in[i], &got[i], "expected pass-through (no allocation)")
	}
}

func TestScrubAppImageEnvSlice_NilInput(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	assert.Nil(t, ScrubAppImageEnvSlice(nil))
}

// TestScrubAppImageEnv_DropsAppRunWholesaleVars asserts by name (not by
// iterating AppImageEnvKeys, which would tautologically self-validate)
// that the wholesale-drop variables set by AppRun and its apprun-hooks
// are removed. These are vars where surgical-strip wouldn't help —
// either they're not colon-separated path lists, or a hook overwrites
// them after AppRun, destroying the user's original value before we
// can recover it.
func TestScrubAppImageEnv_DropsAppRunWholesaleVars(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_x")

	// Identity / metadata vars.
	t.Setenv("ARGV0", "fake.AppImage")
	t.Setenv("OWD", "/home/user")
	// Scalars / single paths from AppRun.
	t.Setenv("PYTHONHOME", "/tmp/.mount_x/usr")
	t.Setenv("PYTHONDONTWRITEBYTECODE", "1")
	// Path lists where hooks overwrite after AppRun (user value is
	// already lost — wholesale-drop is correct).
	t.Setenv("GSETTINGS_SCHEMA_DIR", "/tmp/.mount_x/usr/share/glib-2.0/schemas")
	t.Setenv("GST_PLUGIN_SYSTEM_PATH_1_0", "/tmp/.mount_x/usr/lib/gstreamer-1.0")
	t.Setenv("GTK_PATH", "/tmp/.mount_x/usr/lib/gtk-3.0:/usr/lib64/gtk-3.0")
	// Vars introduced by the GStreamer apprun-hook.
	t.Setenv("GST_REGISTRY_FORK", "no")
	t.Setenv("GST_PLUGIN_PATH_1_0", "/tmp/.mount_x/usr/lib/gstreamer-1.0")
	t.Setenv("GST_PLUGIN_SCANNER_1_0", "/tmp/.mount_x/usr/lib/gstreamer-1.0/gst-plugin-scanner")
	t.Setenv("GST_PTP_HELPER_1_0", "/tmp/.mount_x/usr/lib/gstreamer-1.0/gst-ptp-helper")
	// Vars introduced by the GTK apprun-hook.
	t.Setenv("GTK_DATA_PREFIX", "/tmp/.mount_x")
	t.Setenv("GTK_THEME", "Adwaita:dark")
	t.Setenv("GTK_EXE_PREFIX", "/tmp/.mount_x/usr")
	t.Setenv("GTK_IM_MODULE_FILE", "/tmp/.mount_x/usr/lib/gtk-3.0/3.0.0/immodules.cache")
	t.Setenv("GDK_PIXBUF_MODULE_FILE", "/tmp/.mount_x/usr/lib/gdk-pixbuf-2.0/2.10.0/loaders.cache")
	t.Setenv("GIO_EXTRA_MODULES", "/tmp/.mount_x/usr/lib/gio/modules")
	t.Setenv("GDK_BACKEND", "x11")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	for _, key := range []string{
		"ARGV0", "APPIMAGE", "APPDIR", "OWD",
		"PYTHONHOME", "PYTHONDONTWRITEBYTECODE",
		"GSETTINGS_SCHEMA_DIR", "GST_PLUGIN_SYSTEM_PATH_1_0", "GTK_PATH",
		"GST_REGISTRY_FORK", "GST_PLUGIN_PATH_1_0",
		"GST_PLUGIN_SCANNER_1_0", "GST_PTP_HELPER_1_0",
		"GTK_DATA_PREFIX", "GTK_THEME", "GTK_EXE_PREFIX",
		"GTK_IM_MODULE_FILE", "GDK_PIXBUF_MODULE_FILE", "GIO_EXTRA_MODULES",
		"GDK_BACKEND",
	} {
		assert.False(t, hasKey(cmd.Env, key), "env var %q must be wholesale-dropped inside an AppImage", key)
	}
}

// TestScrubAppImageEnv_PreservesUserLDPreload locks in that we don't
// scrub LD_PRELOAD: AppRun does not set it, so any value present came
// from the user (e.g. malloc debugging) and must propagate to spawned
// shells unchanged. Regression guard against re-adding it to the
// wholesale-drop list "defensively".
func TestScrubAppImageEnv_PreservesUserLDPreload(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_x")
	t.Setenv("LD_PRELOAD", "/usr/lib/libtcmalloc.so")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	got, ok := getValue(cmd.Env, "LD_PRELOAD")
	require.True(t, ok, "user-set LD_PRELOAD must survive scrubbing")
	assert.Equal(t, "/usr/lib/libtcmalloc.so", got)
}

// TestScrubAppImageEnv_StripsAppDirFromPathLists verifies the surgical
// strip on every Category-A path-list var: AppDir-rooted entries are
// removed but the user's original value at the tail is preserved.
// Regression test for the readline symbol-lookup crash (caused by
// LD_LIBRARY_PATH pointing at the bundled libreadline.so.8) and
// against silently destroying user customizations of these vars.
func TestScrubAppImageEnv_StripsAppDirFromPathLists(t *testing.T) {
	const appDir = "/tmp/.mount_xxxxxx"
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("APPDIR", appDir)

	cases := []struct {
		name string
		set  string
		want string
	}{
		{
			name: "PATH",
			// AppRun's snprintf shape, with user's original at the tail.
			set: appDir + "/usr/bin/:" + appDir + "/usr/sbin/:" + appDir + "/usr/games/:" +
				appDir + "/bin/:" + appDir + "/sbin/:" +
				"/usr/local/bin:/usr/bin:/bin:/home/user/.local/bin",
			want: "/usr/local/bin:/usr/bin:/bin:/home/user/.local/bin",
		},
		{
			name: "LD_LIBRARY_PATH",
			set: appDir + "/usr/lib/:" + appDir + "/usr/lib/x86_64-linux-gnu/:" + appDir + "/lib64/:" +
				"/opt/cuda/lib64:/home/user/.local/lib",
			want: "/opt/cuda/lib64:/home/user/.local/lib",
		},
		{
			name: "PERLLIB",
			set:  appDir + "/usr/share/perl5/:" + appDir + "/usr/lib/perl5/:" + "/home/user/perl5/lib/perl5",
			want: "/home/user/perl5/lib/perl5",
		},
		{
			name: "PYTHONPATH",
			set:  appDir + "/usr/share/pyshared/:" + "/home/user/project/src",
			want: "/home/user/project/src",
		},
		{
			name: "QT_PLUGIN_PATH",
			set: appDir + "/usr/lib/qt5/plugins/:" + appDir + "/usr/lib/x86_64-linux-gnu/qt5/plugins/:" +
				"/usr/lib/qt6/plugins",
			want: "/usr/lib/qt6/plugins",
		},
		{
			name: "XDG_DATA_DIRS",
			// AppRun + GTK hook combined: hook's hardcoded /usr/share
			// survives along with the user's original tail. Spawned
			// shell ends up with a sane combined value.
			set:  appDir + "/usr/share:/usr/share:" + appDir + "/usr/share/:" + "/home/user/.local/share",
			want: "/usr/share:/home/user/.local/share",
		},
		{
			name: "GST_PLUGIN_SYSTEM_PATH",
			set:  appDir + "/usr/lib/gstreamer:" + "/usr/local/lib/gstreamer-1.0",
			want: "/usr/local/lib/gstreamer-1.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.name, tc.set)

			cmd := exec.CommandContext(context.Background(), "/bin/true")
			ScrubAppImageEnv(cmd)

			got, ok := getValue(cmd.Env, tc.name)
			require.True(t, ok, "%s must remain present after scrubbing (user had a non-empty original)", tc.name)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestScrubAppImageEnv_DropsPathListsWithNoUserOriginal asserts that
// when AppRun's prepends are the only content of a path-list var
// (the user had nothing set originally), the var is dropped from the
// env entirely rather than left set-to-empty or set-to-bare-colons.
//
// This is what fixes the readline crash in the common case: most
// users have no LD_LIBRARY_PATH set, so AppRun's value is purely
// AppDir-rooted, and after stripping the var disappears, letting
// ld.so use the system default search path.
func TestScrubAppImageEnv_DropsPathListsWithNoUserOriginal(t *testing.T) {
	const appDir = "/tmp/.mount_xxxxxx"
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("APPDIR", appDir)
	// Trailing colon is what AppRun's snprintf produces when the user
	// had no LD_LIBRARY_PATH (the `:%s` tail substitutes empty).
	t.Setenv("LD_LIBRARY_PATH",
		appDir+"/usr/lib/:"+appDir+"/usr/lib/x86_64-linux-gnu/:"+appDir+"/lib64/:")
	t.Setenv("PYTHONPATH", appDir+"/usr/share/pyshared/:")
	t.Setenv("PERLLIB", appDir+"/usr/share/perl5/:"+appDir+"/usr/lib/perl5/:")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	for _, key := range []string{"LD_LIBRARY_PATH", "PYTHONPATH", "PERLLIB"} {
		assert.False(t, hasKey(cmd.Env, key),
			"%s must be dropped when only AppDir entries remain (rather than set-to-empty)", key)
	}
}

// TestScrubAppImageEnv_PathLists_HandlesMissingAppDir checks the
// graceful fallback when APPDIR is not set: path-list vars must be
// left alone (without APPDIR we can't tell which entries are
// AppRun's prepends, and the user's original PATH may itself
// contain entries we'd risk false-stripping).
func TestScrubAppImageEnv_PathLists_HandlesMissingAppDir(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	// Empty APPDIR is the same code path as unset for
	// stripAppDirFromPathLists (both yield os.Getenv("APPDIR") == "")
	// and t.Setenv handles cleanup.
	t.Setenv("APPDIR", "")
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("LD_LIBRARY_PATH", "/opt/cuda/lib64")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	pathVal, ok := getValue(cmd.Env, "PATH")
	require.True(t, ok)
	assert.Equal(t, "/usr/local/bin:/usr/bin:/bin", pathVal, "PATH must be untouched when APPDIR is unset")
	ldVal, ok := getValue(cmd.Env, "LD_LIBRARY_PATH")
	require.True(t, ok)
	assert.Equal(t, "/opt/cuda/lib64", ldVal, "LD_LIBRARY_PATH must be untouched when APPDIR is unset")
}

// TestScrubAppImageEnvSlice_StripsAppDirFromPathLists is the
// slice-variant counterpart to the cmd-based surgical-strip test —
// terminal.go uses ScrubAppImageEnvSlice and is subject to the same
// path-list manipulation.
func TestScrubAppImageEnvSlice_StripsAppDirFromPathLists(t *testing.T) {
	const appDir = "/tmp/.mount_xxxxxx"
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("APPDIR", appDir)

	in := []string{
		"TERM=xterm-256color",
		"PATH=" + appDir + "/usr/bin/:" + appDir + "/bin/:/usr/bin:/bin",
		"LD_LIBRARY_PATH=" + appDir + "/usr/lib/:/opt/cuda/lib64",
		// User had no PYTHONPATH originally — only AppDir prepends survived.
		// This entry should be dropped entirely.
		"PYTHONPATH=" + appDir + "/usr/share/pyshared/:",
	}
	got := ScrubAppImageEnvSlice(in)

	pathVal, ok := getValue(got, "PATH")
	require.True(t, ok)
	assert.Equal(t, "/usr/bin:/bin", pathVal)
	ldVal, ok := getValue(got, "LD_LIBRARY_PATH")
	require.True(t, ok)
	assert.Equal(t, "/opt/cuda/lib64", ldVal)
	assert.False(t, hasKey(got, "PYTHONPATH"),
		"PYTHONPATH must be dropped when only AppDir entries remain")
}

func TestScrubAppImageEnv_PreservesCallerSetEnv(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/fake.AppImage")
	t.Setenv("ARGV0", "fake.AppImage")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	// Mimic terminal.go's pattern: pre-seed cmd.Env with a curated
	// addition (TERM) before scrubbing. The addition must survive.
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
	ScrubAppImageEnv(cmd)

	hasEntry := func(env []string, want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}
	assert.True(t, hasEntry(cmd.Env, "TERM=xterm-256color"), "caller's pre-seeded env must survive the scrub")
	assert.False(t, hasKey(cmd.Env, "ARGV0"), "ARGV0 must be scrubbed")
}
