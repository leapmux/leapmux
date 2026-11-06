package main

import (
	"context"
	"os"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/config"
)

// registerThroughEnv registers one worker through the real service and
// returns its data-dir configuration.
func registerThroughEnv(t *testing.T) (*registerEnv, *config.Config) {
	t.Helper()
	env := startRegisterEnv(t)
	dataDir := t.TempDir()
	require.NoError(t, runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir}))
	cfg := workerConfigOf(t, dataDir)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered())
	return env, cfg
}

// TestDeregisterPreservesCredentialsOnUncertainResult drives every
// outcome the hub did NOT confirm: an authentication refusal, a
// transport failure, and a cancellation. The credentials and the worker
// database stay in every case.
func TestDeregisterPreservesCredentialsOnUncertainResult(t *testing.T) {
	t.Run("an authentication refusal keeps the credentials", func(t *testing.T) {
		env, cfg := registerThroughEnv(t)
		state, err := cfg.LoadState()
		require.NoError(t, err)

		// The token the hub no longer knows: every call refuses, and a
		// refusal is never proof the deletion happened.
		env.refuseDeregisterWith = connect.CodeUnauthenticated
		err = runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
		require.Error(t, err)
		assert.FileExists(t, cfg.StatePath(), "an uncertain outcome keeps the credentials")

		// The row is untouched on the hub too.
		row, rowErr := env.store.Workers().GetByID(context.Background(), state.WorkerID)
		require.NoError(t, rowErr)
		assert.Nil(t, row.DeletedAt)
	})

	t.Run("a transport failure keeps the credentials", func(t *testing.T) {
		env, cfg := registerThroughEnv(t)
		env.transportFailure = true

		err := runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
		require.Error(t, err)
		assert.FileExists(t, cfg.StatePath())
	})
}

// TestDeregisterRetriesAfterLocalCleanupFailure completes the remote
// deletion, fails the LOCAL cleanup halfway, and requires the retry to
// finish it without another active registration on the hub.
func TestDeregisterRetriesAfterLocalCleanupFailure(t *testing.T) {
	env, cfg := registerThroughEnv(t)
	state, err := cfg.LoadState()
	require.NoError(t, err)

	// A worker database a previous run left behind: deregistration must
	// preserve it through every attempt.
	require.NoError(t, os.MkdirAll(cfg.DataDir, 0o700))
	require.NoError(t, os.WriteFile(cfg.DBPath(), []byte("sqlite"), 0o600))

	// The remote deletion succeeds; the local cleanup cannot, because
	// the data directory refuses writes for the first attempt. That is
	// the partial cleanup a full disk or a permission surprise leaves.
	require.NoError(t, os.Chmod(cfg.DataDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(cfg.DataDir, 0o700) })

	err = runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
	require.Error(t, err, "the local cleanup failure surfaces")
	assert.EqualValues(t, 1, env.deregisterCalls.Load(), "one confirmed remote deletion")
	require.Equal(t, state.WorkerID, rowIDofDeleted(t, env, state))

	// The row is deleted on the hub after the first attempt.
	row, rowErr := env.store.Workers().GetByIDIncludeDeleted(context.Background(), state.WorkerID)
	require.NoError(t, rowErr)
	require.NotNil(t, row.DeletedAt, "the remote deletion completed")

	// The retry finishes the cleanup without another ACTIVE
	// registration: the retained deleted row accepts the repeat call,
	// so the credentials the first attempt could not remove are removed
	// now.
	require.NoError(t, os.Chmod(cfg.DataDir, 0o700))
	require.NoError(t, runWorkerDeregister([]string{"--data-dir", cfg.DataDir}))
	assert.EqualValues(t, 2, env.deregisterCalls.Load(), "the retry repeated the idempotent call")
	assert.NoFileExists(t, cfg.StatePath())
	assert.NoFileExists(t, cfg.PendingPath())
	assert.FileExists(t, cfg.DBPath(), "the worker database is preserved")
}

// rowIDofDeleted states that the hub holds the worker deleted.
func rowIDofDeleted(t *testing.T, env *registerEnv, state *config.State) string {
	t.Helper()
	row, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), state.WorkerID)
	require.NoError(t, err)
	require.NotNil(t, row.DeletedAt, "the remote deletion completed")
	return row.ID
}

// Deregistering a machine that holds no registration is a success that
// names its end state and leaves no pending material behind.
func TestDeregisterOfAnUnregisteredWorkerSucceeds(t *testing.T) {
	env := startRegisterEnv(t)
	dataDir := t.TempDir()

	// An abandoned pending record is the leftover this path sweeps.
	cfg := workerConfigOf(t, dataDir)
	cfg.HubURL = env.url
	require.NoError(t, cfg.Validate())
	pending := &config.PendingRegistration{
		AttemptID: "e9a0425886d148f68a8f94716ed47a78",
		HubURL:    env.url,
		RegKey:    "an-abandoned-key",
	}
	_, err := pending.GenerateKeypair()
	require.NoError(t, err)
	require.NoError(t, cfg.SavePendingRegistration(pending))

	require.NoError(t, runWorkerDeregister([]string{"--data-dir", dataDir}))
	assert.NoFileExists(t, cfg.StatePath())
	assert.NoFileExists(t, cfg.PendingPath(), "the sweep removes the abandoned pending record")
	assert.Zero(t, env.deregisterCalls.Load(), "no remote call is made for a local-only state")
}
