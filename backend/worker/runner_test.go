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
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/worker"
)

// A Worker with no keypair cannot connect once the Hub pins keys, so Run refuses
// it before it opens anything -- no database, no socket.
func TestRunRefusesAMissingKeypair(t *testing.T) {
	t.Parallel()
	lease := workerLeaseForTest(t)

	err := worker.Run(context.Background(), worker.RunConfig{
		HubURL:    "http://127.0.0.1:1",
		Directory: lease,
		AuthToken: "token",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CompositeKey")

	assertNoWorkerDB(t, lease)
}

// An empty auth token sends "Bearer ", which the Hub refuses with a bare code the
// worker would retry for ever, so Run refuses it up front too.
func TestRunRefusesAMissingAuthToken(t *testing.T) {
	t.Parallel()
	lease := workerLeaseForTest(t)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	err = worker.Run(context.Background(), worker.RunConfig{
		HubURL:         "http://127.0.0.1:1",
		Directory:      lease,
		CompositeKey:   key,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthToken")

	assertNoWorkerDB(t, lease)
}

// A keypair that is present but cannot yield a complete key set cannot connect
// either. Run refuses it with the reason, before it opens anything, instead of
// letting bootstrap.Wire panic on it: solo runs Run on a goroutine that recovers
// nothing, so that panic would end the whole process.
func TestRunRefusesAKeypairWithoutACompleteKeySet(t *testing.T) {
	t.Parallel()
	lease := workerLeaseForTest(t)

	var err error
	require.NotPanics(t, func() {
		err = worker.Run(context.Background(), worker.RunConfig{
			HubURL:         "http://127.0.0.1:1",
			Directory:      lease,
			AuthToken:      "token",
			CompositeKey:   &noiseutil.CompositeKeypair{},
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		})
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CompositeKey")
	assert.Contains(t, err.Error(), "ML-KEM", "the error states what the keypair lacks")

	assertNoWorkerDB(t, lease)
}

// The Hub refuses a hello with no encryption mode, with a bare code the worker
// would retry for ever, so Run refuses the zero mode up front too.
func TestRunRefusesAnUnspecifiedEncryptionMode(t *testing.T) {
	t.Parallel()
	lease := workerLeaseForTest(t)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	// The deadline only ends a Run that wrongly started; a correct Run
	// returns before it opens anything.
	err = worker.Run(testutil.DeadlineContext(t), worker.RunConfig{
		HubURL:       "http://127.0.0.1:1",
		Directory:    lease,
		AuthToken:    "token",
		CompositeKey: key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EncryptionMode")

	assertNoWorkerDB(t, lease)
}

// workerLeaseForTest acquires a worker-component lease for a Run a test
// drives, the way the launcher does. The lease releases when the test
// ends; Run's own release is idempotent beside it.
func workerLeaseForTest(t *testing.T) *lifecycle.DirectoryLease {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	lease, err := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	require.NoError(t, err)
	return lease
}

// assertNoWorkerDB proves Run returned before it opened the database.
// The lease's lifecycle metadata legitimately exists in the directory,
// so the assertion names the database and the sockets, not the tree.
func assertNoWorkerDB(t *testing.T, lease *lifecycle.DirectoryLease) {
	t.Helper()
	entries, err := os.ReadDir(lease.Path())
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, "worker.db", e.Name(), "Run must refuse before it creates a database")
	}
}
