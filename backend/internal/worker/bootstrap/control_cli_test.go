package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// spawnAgentEnv spawns one agent through the wired factory and returns the
// env that the factory hands the agent.
func spawnAgentEnv(t *testing.T, w *Wiring) []string {
	t.Helper()
	factory, ok := w.Service.ControlIPC.(*controlipc.Factory)
	require.True(t, ok)
	envs, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: "agent-one"})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	return envs
}

// cliDirectoriesIn lists the CLI directories in the run directory of dataDir.
func cliDirectoriesIn(t *testing.T, dataDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "run", "cli-*"))
	require.NoError(t, err)
	return matches
}

// Wire links the executable that its entry point states, every spawn
// advertises the link, and Shutdown removes it.
func TestWire_LinksTheControlCLIForEverySpawn(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "leapmux")
	testutil.WriteExecutable(t, executable, "#!/bin/sh\necho worker-cli\n")
	var dataDir string
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) {
		p.ControlCLI = executable
		dataDir = p.DataDir
	})

	link := envutil.ValuesFor(spawnAgentEnv(t, w), controlenv.CLIVariable)
	require.Len(t, link, 1, "the spawn advertises the CLI once")
	assert.True(t, strings.HasPrefix(link[0], filepath.Join(dataDir, "run", "cli-")),
		"the link lives in a directory of this Worker process in its data directory: %s", link[0])
	assert.FileExists(t, link[0])

	w.Shutdown()

	assert.NoFileExists(t, link[0], "shutdown removes the link")
	assert.Empty(t, cliDirectoriesIn(t, dataDir))
	assert.FileExists(t, executable, "the executable stays")
}

// An entry point whose executable does not serve the CLI, as the desktop
// sidecar, states none. Wire links nothing, and the spawns keep their remote
// control without LEAPMUX_CONTROL_CLI.
func TestWire_WithoutControlCLILinksNothing(t *testing.T) {
	var dataDir string
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { dataDir = p.DataDir })

	envs := spawnAgentEnv(t, w)

	assert.False(t, envutil.HasKey(envs, controlenv.CLIVariable))
	assert.True(t, envutil.HasKey(envs, "LEAPMUX_CONTROL_SOCK"))
	assert.Empty(t, cliDirectoriesIn(t, dataDir))
}

// A CLI that Wire cannot link costs the spawns only the CLI: they keep their
// remote control.
func TestWire_AnUnusableControlCLIKeepsRemoteControl(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.ControlCLI = missing })

	envs := spawnAgentEnv(t, w)

	assert.False(t, envutil.HasKey(envs, controlenv.CLIVariable))
	assert.True(t, envutil.HasKey(envs, "LEAPMUX_CONTROL_SOCK"))
	_, err := os.Stat(missing)
	assert.ErrorIs(t, err, os.ErrNotExist, "Wire creates nothing at the path it could not link")
}
