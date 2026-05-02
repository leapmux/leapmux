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

func TestScrubAppImageEnv_InsideAppImage_DropsKeys(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_xxxxxx")
	t.Setenv("ARGV0", "leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("OWD", "/home/user")
	t.Setenv("PATH_SHOULD_SURVIVE", "/usr/bin:/bin")

	cmd := exec.CommandContext(context.Background(), "/bin/true")
	ScrubAppImageEnv(cmd)

	hasKey := func(env []string, key string) bool {
		for _, e := range env {
			if name, _, _ := strings.Cut(e, "="); strings.EqualFold(name, key) {
				return true
			}
		}
		return false
	}
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
	hasKey := func(env []string, key string) bool {
		for _, e := range env {
			if name, _, _ := strings.Cut(e, "="); strings.EqualFold(name, key) {
				return true
			}
		}
		return false
	}
	assert.True(t, hasEntry(cmd.Env, "TERM=xterm-256color"), "caller's pre-seeded env must survive the scrub")
	assert.False(t, hasKey(cmd.Env, "ARGV0"), "ARGV0 must be scrubbed")
}
