package hub

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// writeStateFile stamps the pid as the FIRST field and names this process, and
// keeps the file at 0600. The pid is how a reader tells a live hub from a
// crashed run's leftover.
func TestWriteStateFile_PIDIsFirstAndNamesThisProcess(t *testing.T) {
	dir := t.TempDir()
	path, err := writeStateFile(dir, []string{":4327", "unix:/tmp/x.sock"})
	require.NoError(t, err)
	assert.Equal(t, stateFilePath(dir), path)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Regexp(t, `^\{\s*"pid"`, string(raw), "pid must be the first JSON key")

	var got hubState
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, os.Getpid(), got.PID, "the writer names this process")
	assert.Equal(t, []string{":4327", "unix:/tmp/x.sock"}, got.Listen)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// The state file appears only after every listener is bound, and it names the
// addresses a client can actually reach: a `--listen 127.0.0.1:0` request shows
// the port the operating system chose, and a local entry stays its URL.
func TestServer_StateFileNamesTheResolvedBindSet(t *testing.T) {
	srv := startTestServer(t, &config.Config{Listen: []string{"127.0.0.1:0"}})

	var got hubState
	raw, err := os.ReadFile(stateFilePath(srv.cfg.DataDir))
	require.NoError(t, err, "a hub that serves must have written its state file")
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Len(t, got.Listen, 2, "the bind set is the TCP request and the local URL")
	assert.Equal(t, srv.PrimaryListenAddr(), got.Listen[0],
		"a port-0 request must appear as the port the operating system chose")
	assert.Equal(t, srv.localListenURLs[0], got.Listen[1], "a local entry stays its URL")
}

// A clean shutdown removes the state file: after the listeners are released it
// would claim a hub that is not running.
func TestServer_StateFileRemovedOnCleanShutdown(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Listen: []string{"127.0.0.1:0"}, SoloMode: true}
	_, stop := startTestServerIn(t, cfg, dataDir, locallistentest.UniqueListenURL(t, "lmx-state"))

	require.FileExists(t, stateFilePath(dataDir), "the hub must have written its state file")
	stop()
	assert.NoFileExists(t, stateFilePath(dataDir),
		"a clean shutdown must remove the state file")
}

// A hub that fails to bind writes no state file: the file must never claim a
// hub that is not running.
func TestServer_NoStateFileWhenABindFails(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		Listen:  []string{"unix:/nonexistent-parent-dir-for-state-test/hub.sock"},
		Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
	}
	cfg.DataDir = dataDir

	_, err := NewServer(cfg)
	require.Error(t, err, "a local IPC URL under a missing directory must fail the bind")
	assert.NoFileExists(t, stateFilePath(dataDir),
		"a failed bind must leave no state file claiming a running hub")
}

// removeStateFile treats an absent file as done, and a path from a write that
// never happened as nothing to do: a failed start beside a live hub sharing
// the data directory must not delete the file that hub wrote.
func TestRemoveStateFile(t *testing.T) {
	t.Run("an absent file is not an error", func(t *testing.T) {
		require.NoError(t, removeStateFile(stateFilePath(t.TempDir())))
	})

	t.Run("an empty path removes nothing", func(t *testing.T) {
		require.NoError(t, removeStateFile(""))
	})

	t.Run("the file and any crashed temp go together", func(t *testing.T) {
		dir := t.TempDir()
		path, err := writeStateFile(dir, []string{":4327"})
		require.NoError(t, err)
		crashed := path + ".tmp12345"
		require.NoError(t, os.WriteFile(crashed, []byte("{}"), 0o600))

		require.NoError(t, removeStateFile(path))
		assert.NoFileExists(t, path)
		assert.NoFileExists(t, crashed,
			"an interrupted write's temp holds the same content and must go with the delete")
	})
}

// The pid in the file is the process that wrote it, asserted through the
// server path rather than the writer alone.
func TestServer_StateFilePIDMatchesThisProcess(t *testing.T) {
	srv := startTestServer(t, &config.Config{Listen: []string{"127.0.0.1:0"}})

	var got hubState
	raw, err := os.ReadFile(stateFilePath(srv.cfg.DataDir))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, os.Getpid(), got.PID, "the state file must name the process that wrote it")
}
