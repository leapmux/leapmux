package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SaveState stamps the pid as the FIRST field and names this process. The pid
// is how a reader tells a live credentials file from a crashed run's, and the
// first position is what the reader sees before anything else.
func TestSaveState_PIDIsFirstAndNamesThisProcess(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	in := &State{WorkerID: "w1", AuthToken: "tok"}

	require.NoError(t, cfg.SaveState(in))

	raw, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^\{\s*"pid"`), string(raw),
		"pid must be the first JSON key")
	assert.Equal(t, os.Getpid(), in.PID, "the writer names this process")

	var got State
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, os.Getpid(), got.PID)
}

// The credentials file keeps mode 0600 through the atomic replace: the auth
// token and the private keys inside it must not be readable by anyone else.
func TestSaveState_ModeIs0600(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	require.NoError(t, cfg.SaveState(&State{WorkerID: "w1", AuthToken: "tok"}))

	info, err := os.Stat(cfg.StatePath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// A successful SaveState leaves no temporary file behind, and a failed one
// cleans up the artifact it made. The temp holds the same secrets as the
// destination, so a residue is a leaked credentials file under a name no
// listing of the destination shows.
func TestSaveState_LeavesNoTemporaryFileBehind(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	require.NoError(t, cfg.SaveState(&State{WorkerID: "w1", AuthToken: "tok"}))

	entries, err := os.ReadDir(cfg.DataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a successful write must leave only the state file")
	assert.Equal(t, "state.json", entries[0].Name())

	// A destination the rename cannot replace: the write fails after the temp
	// is complete, and the temp must still not survive.
	require.NoError(t, os.Remove(cfg.StatePath()))
	require.NoError(t, os.Mkdir(cfg.StatePath(), 0o700))
	err = cfg.SaveState(&State{WorkerID: "w2", AuthToken: "tok2"})
	require.Error(t, err, "the rename onto a directory must fail")

	entries, err = os.ReadDir(cfg.DataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a failed write must not leave its temporary file behind")
	assert.Equal(t, "state.json", entries[0].Name())
}

// The write path changed shape; the stored shape did not. A round trip keeps
// every field, secrets included, so the atomic write cannot silently start
// dropping one.
func TestSaveState_RoundTripsEveryField(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	in := &State{
		WorkerID: "w1", AuthToken: "tok",
		PublicKey: "pk", PrivateKey: "sk",
		MlkemPublicKey: "mk", MlkemPrivateKey: "msk",
		SlhdsaPublicKey: "sp", SlhdsaPrivateKey: "ssk",
	}

	require.NoError(t, cfg.SaveState(in))
	got, err := cfg.LoadState()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, in, got)
}

// The temp name is derived from the destination, and a cleanup sweep that only
// knew the old bare-write shape would miss it. This pins the sweep target a
// deleter must use.
func TestSaveState_TempNameIsSweepable(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	// A leftover from an interrupted write, under the name atomicfile uses.
	leftover := cfg.StatePath() + ".tmp12345"
	require.NoError(t, os.WriteFile(leftover, []byte(`{"pid":0}`), 0o600))

	matches, err := filepath.Glob(cfg.StatePath() + ".tmp*")
	require.NoError(t, err)
	assert.Equal(t, []string{leftover}, matches,
		"the residue name must be discoverable from the destination alone")
}
