package terminal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// A worker inherits TERM from whatever shell started it, and inherits
// GIT_OPTIONAL_LOCKS too whenever that shell was itself a LeapMux terminal.
// Appending over those hands the child two entries per key: harmless to the
// child (exec resolves duplicates last-wins) but it makes the environment we
// hand out disagree with the environment we can assert on -- and the pin that
// silently stops being the only value is the one nobody notices.
func TestSpawnEnv_PinsOverInheritedValues(t *testing.T) {
	t.Parallel()

	env := spawnEnv([]string{
		"PATH=/usr/bin",
		"TERM=dumb",
		"GIT_OPTIONAL_LOCKS=1",
	}, nil)

	for key, want := range map[string]string{
		"TERM":               "xterm-256color",
		"GIT_OPTIONAL_LOCKS": "0",
	} {
		values := envutil.ValuesFor(env, key)
		require.Len(t, values, 1, "%s must be pinned exactly once, not layered over the inherited value", key)
		assert.Equal(t, want, values[0], "%s must be the value the terminal spawn pins", key)
	}
	assert.Equal(t, "GIT_OPTIONAL_LOCKS=0", gitutil.GitOptionalLocksOff,
		"the exported constant is what every spawn path shares")
}

// The shell needs the rest of the environment -- PATH, HOME, credential and
// proxy settings -- so the pin must displace only what it assigns.
func TestSpawnEnv_KeepsUnrelatedVariables(t *testing.T) {
	t.Parallel()

	env := spawnEnv([]string{"PATH=/usr/bin", "HOME=/home/u", "TERM=dumb"}, nil)

	assert.Contains(t, env, "PATH=/usr/bin")
	assert.Contains(t, env, "HOME=/home/u")
}

// ExtraEnv carries the canonical LEAPMUX_REMOTE_* values for a delegated
// launch, so it must land LAST and must not be displaced by the pins.
func TestSpawnEnv_ExtraEnvLandsLastAndReplacesInheritedRemoteValues(t *testing.T) {
	t.Parallel()

	env := spawnEnv(
		[]string{"PATH=/usr/bin", "LEAPMUX_REMOTE_SOCKET=/stale/sock"},
		[]string{"LEAPMUX_REMOTE_SOCKET=/fresh/sock"},
	)

	assert.Equal(t, []string{"/fresh/sock"}, envutil.ValuesFor(env, "LEAPMUX_REMOTE_SOCKET"),
		"an inherited LEAPMUX_REMOTE_* must be stripped, not layered under the fresh value")
	assert.Equal(t, "LEAPMUX_REMOTE_SOCKET=/fresh/sock", env[len(env)-1],
		"ExtraEnv lands last so it wins over everything the pins set")
	assert.Contains(t, env, "TERM=xterm-256color", "the pins survive the ExtraEnv append")
}
