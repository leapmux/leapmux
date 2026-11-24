package terminal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
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

// ExtraEnv carries the canonical LEAPMUX_CONTROL_* values for a delegated
// launch, so it must land LAST and must not be displaced by the pins.
func TestSpawnEnv_ExtraEnvLandsLastAndReplacesInheritedRemoteValues(t *testing.T) {
	t.Parallel()

	env := spawnEnv(
		[]string{"PATH=/usr/bin", "LEAPMUX_CONTROL_SOCKET=/stale/sock"},
		[]string{"LEAPMUX_CONTROL_SOCKET=/fresh/sock"},
	)

	assert.Equal(t, []string{"/fresh/sock"}, envutil.ValuesFor(env, "LEAPMUX_CONTROL_SOCKET"),
		"an inherited LEAPMUX_CONTROL_* must be stripped, not layered under the fresh value")
	assert.Equal(t, "LEAPMUX_CONTROL_SOCKET=/fresh/sock", env[len(env)-1],
		"ExtraEnv lands last so it wins over everything the pins set")
	assert.Contains(t, env, "TERM=xterm-256color", "the pins survive the ExtraEnv append")
}

// A Worker that runs inside a LeapMux tab inherits the LEAPMUX_CONTROL_*
// context of that tab. A terminal that the Worker starts without remote control
// (the factory failed and degraded) must not keep it: `leapmux control tab close
// self` there would close the tab that runs the Worker.
func TestSpawnEnv_StripsInheritedControlVariablesWithoutRemoteControl(t *testing.T) {
	t.Parallel()

	env := spawnEnv([]string{
		"PATH=/usr/bin",
		"LEAPMUX_CONTROL_SOCK=unix:/parent/sock",
		"LEAPMUX_CONTROL_TOKEN=parent-token",
		"LEAPMUX_CONTROL_TAB_ID=parent-tab",
	}, nil)

	for _, key := range []string{"LEAPMUX_CONTROL_SOCK", "LEAPMUX_CONTROL_TOKEN", "LEAPMUX_CONTROL_TAB_ID"} {
		assert.Empty(t, envutil.ValuesFor(env, key), "%s of the parent Worker's tab must not reach the shell", key)
	}
	assert.Contains(t, env, "PATH=/usr/bin")
}

// The shell of a terminal with remote control finds the Worker's own CLI first
// on PATH, and LEAPMUX_CONTROL_CLI gives it exactly. The PATH that the shell
// would get otherwise follows the entry unchanged.
func TestSpawnEnv_PutsTheControlCLIDirectoryFirstOnPath(t *testing.T) {
	t.Parallel()
	cliDir := filepath.Join(t.TempDir(), "cli-1")
	cli := filepath.Join(cliDir, "leapmux")
	sep := string(os.PathListSeparator)

	env := spawnEnv(
		[]string{"PATH=/usr/bin" + sep + "/bin", "HOME=/home/u"},
		[]string{"LEAPMUX_CONTROL_SOCK=unix:/fresh/sock", controlenv.CLIVariable + "=" + cli},
	)

	assert.Equal(t, []string{cliDir + sep + "/usr/bin" + sep + "/bin"}, envutil.ValuesFor(env, "PATH"))
	assert.Equal(t, []string{cli}, envutil.ValuesFor(env, controlenv.CLIVariable))
	assert.Equal(t, controlenv.CLIVariable+"="+cli, env[len(env)-1], "ExtraEnv still lands last")
}

// A terminal without remote control gets no PATH entry: the entry derives from
// LEAPMUX_CONTROL_CLI, which only a spawn with remote control receives.
func TestSpawnEnv_GivesNoPathEntryWithoutRemoteControl(t *testing.T) {
	t.Parallel()

	env := spawnEnv([]string{"PATH=/usr/bin", "HOME=/home/u"}, nil)

	assert.Equal(t, []string{"/usr/bin"}, envutil.ValuesFor(env, "PATH"))
	assert.Empty(t, envutil.ValuesFor(env, controlenv.CLIVariable))
}

// The provider-helper variable points at one agent's helper spec. A terminal
// that inherits it would run a plain `leapmux` as that agent's helper, so the
// spawn strips it, whatever else it inherits.
func TestSpawnEnv_StripsTheAgentHelperVariable(t *testing.T) {
	t.Parallel()

	for _, extra := range [][]string{nil, {"LEAPMUX_CONTROL_SOCKET=/fresh/sock"}} {
		env := spawnEnv([]string{"PATH=/usr/bin", contracts.EnvAgentHelper + "=/tmp/agent/helper.json"}, extra)
		assert.Empty(t, envutil.ValuesFor(env, contracts.EnvAgentHelper))
		assert.Contains(t, env, "PATH=/usr/bin")
	}
}
