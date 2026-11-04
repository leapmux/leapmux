package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
		HubURL:         "http://127.0.0.1:1",
		DataDir:        dataDir,
		CompositeKey:   key,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthToken")

	assertNoWorkerDB(t, dataDir)
}

// A keypair that is present but cannot yield a complete key set cannot connect
// either. Run refuses it with the reason, before it opens anything, instead of
// letting bootstrap.Wire panic on it: solo runs Run on a goroutine that recovers
// nothing, so that panic would end the whole process.
func TestRunRefusesAKeypairWithoutACompleteKeySet(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	var err error
	require.NotPanics(t, func() {
		err = worker.Run(context.Background(), worker.RunConfig{
			HubURL:         "http://127.0.0.1:1",
			DataDir:        dataDir,
			AuthToken:      "token",
			CompositeKey:   &noiseutil.CompositeKeypair{},
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		})
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CompositeKey")
	assert.Contains(t, err.Error(), "ML-KEM", "the error states what the keypair lacks")

	assertNoWorkerDB(t, dataDir)
}

// The Hub refuses a hello with no encryption mode, with a bare code the worker
// would retry for ever, so Run refuses the zero mode up front too.
func TestRunRefusesAnUnspecifiedEncryptionMode(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	// The deadline only ends a Run that wrongly started; a correct Run
	// returns before it opens anything.
	err = worker.Run(testutil.DeadlineContext(t), worker.RunConfig{
		HubURL:       "http://127.0.0.1:1",
		DataDir:      dataDir,
		AuthToken:    "token",
		CompositeKey: key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EncryptionMode")

	assertNoWorkerDB(t, dataDir)
}

// assertNoWorkerDB proves Run returned before it opened anything.
func assertNoWorkerDB(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Run must refuse before it creates a database")
}
