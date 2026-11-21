package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The --data-dir help states the variable that the configuration of the
// mode reads. LEAPMUX_DATA_DIR belongs to the Docker start script only, so the
// binary never reads it.
func TestDataDirHelpStatesTheVariableOfTheMode(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		variable string
	}{
		{[]string{"worker", "cross-worker-pins", "list", "--help"}, "LEAPMUX_WORKER_DATA_DIR"},
		{[]string{"worker", "stop", "--help"}, "LEAPMUX_WORKER_DATA_DIR"},
		{[]string{"hub", "stop", "--help"}, "LEAPMUX_HUB_DATA_DIR"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			o := runTestCLI(t, tc.args...)
			require.Equal(t, cli.ExitOK, o.code, o.stderr)
			assert.Contains(t, o.stdout, tc.variable)
			assert.NotContains(t, o.stdout, "LEAPMUX_DATA_DIR")
		})
	}
}

// The pin store commands read and remove the pins of the data directory that
// they select, and refuse a Worker without a pin.
func TestCrossWorkerPins_GetAndRemove(t *testing.T) {
	dataDir := t.TempDir()
	store, err := crossworker.NewPinStore(dataDir)
	require.NoError(t, err)
	for _, workerID := range []string{"sibling-b", "sibling-a"} {
		commit, err := store.Resolve(t.Context(), workerID, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))
	}
	config := filepath.Join(dataDir, "absent-worker.yaml")
	pins := func(args ...string) cliOutcome {
		return runTestCLI(t, append(append([]string{"worker", "cross-worker-pins"}, args...), "--data-dir", dataDir, "--config", config)...)
	}

	o := pins("list", "--json")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	var listed struct {
		Data struct {
			Pins []crossWorkerPin `json:"pins"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.stdout), &listed), o.stdout)
	require.Len(t, listed.Data.Pins, 2)
	assert.Equal(t, "sibling-a", listed.Data.Pins[0].WorkerID, "the list is in the order of the Worker ids")
	assert.Equal(t, "sibling-b", listed.Data.Pins[1].WorkerID)

	o = pins("list")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.Contains(t, o.stdout, "WORKER ID")
	assert.NotContains(t, o.stdout, "X25519", "the table leaves out the keys")

	o = pins("get", "sibling-a", "--json")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.Contains(t, o.stdout, `"worker_id":"sibling-a"`)
	assert.Contains(t, o.stdout, `"x25519_public_key"`)

	o = pins("get", "sibling-c")
	assert.Equal(t, cli.ExitNotFound, o.code)
	assert.Contains(t, o.stderr, `no pin for the sibling Worker "sibling-c"`)

	o = pins("remove", "sibling-c")
	assert.Equal(t, cli.ExitNotFound, o.code, "a mistyped id never reads as a removed pin")

	o = pins("remove", "sibling-a", "--json")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.JSONEq(t, `{"data":{"removed":"sibling-a"}}`, o.stdout)
	reopened, err := crossworker.NewPinStore(dataDir)
	require.NoError(t, err)
	stored, err := reopened.Snapshot(t.Context())
	require.NoError(t, err)
	assert.NotContains(t, stored, "sibling-a")
	assert.Contains(t, stored, "sibling-b")
}
