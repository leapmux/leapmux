package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/worker"
)

// A Worker with no keypair cannot connect once the Hub pins keys, so Run refuses
// it before it opens anything -- no database, no socket.
func TestRunRefusesAMissingKeypair(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	err := worker.Run(context.Background(), worker.RunConfig{
		HubURL:    "http://127.0.0.1:1",
		DataDir:   dataDir,
		AuthToken: "token",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CompositeKey")

	assertNoWorkerDB(t, dataDir)
}

// An empty auth token sends "Bearer ", which the Hub refuses with a bare code the
// worker would retry for ever, so Run refuses it up front too.
func TestRunRefusesAMissingAuthToken(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	err = worker.Run(context.Background(), worker.RunConfig{
		HubURL:       "http://127.0.0.1:1",
		DataDir:      dataDir,
		CompositeKey: key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthToken")

	assertNoWorkerDB(t, dataDir)
}

// assertNoWorkerDB proves Run returned before it opened anything.
func assertNoWorkerDB(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Run must refuse before it creates a database")
}
