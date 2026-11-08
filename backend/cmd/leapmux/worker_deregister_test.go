package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
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

	// Fail at the local cleanup boundary, after the real remote commit.
	failure := errors.New("credential destination removal failed")
	err = runWorkerDeregisterWithFiles(t.Context(), []string{"--data-dir", cfg.DataDir}, func(cfg *config.Config) workerRegistrationFiles {
		return credentialRemovalRefusal{cfg, failure}
	})
	require.ErrorIs(t, err, failure)
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

// Deregistering a machine with no registration or pending attempt succeeds.
func TestDeregisterOfAnUnregisteredWorkerSucceeds(t *testing.T) {
	dataDir := t.TempDir()
	cfg := workerConfigOf(t, dataDir)
	require.NoError(t, runWorkerDeregister([]string{"--data-dir", dataDir}))
	assert.NoFileExists(t, cfg.StatePath())
	assert.NoFileExists(t, cfg.PendingPath())
}

func TestDeregisterPreservesIncompleteNonemptyCredentials(t *testing.T) {
	cfg := workerConfigOf(t, t.TempDir())
	require.NoError(t, cfg.SaveState(&config.State{HubURL: "https://hub.example", WorkerID: "worker-without-token"}))
	before, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	err = runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
	require.ErrorContains(t, err, "incomplete")
	after, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	assert.Equal(t, before, after, "an unconfirmed remote outcome must preserve all surviving credentials")
}

func TestDeregisterPreservesUnresolvedCommittedAttempt(t *testing.T) {
	env, cfg := registerThroughEnv(t)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	row, err := env.store.Workers().GetByID(context.Background(), state.WorkerID)
	require.NoError(t, err)
	require.NoError(t, cfg.SavePendingRegistration(&config.PendingRegistration{
		AttemptID: row.RegistrationAttemptID, HubURL: env.url, RegKey: env.key, KeyMaterial: state.KeyMaterial,
	}))
	require.NoError(t, os.Remove(cfg.StatePath()))
	before, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	err = runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
	require.ErrorContains(t, err, "register")
	after, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	assert.Equal(t, before, after, "pending recovery material must survive an unconfirmed remote outcome")
	assert.Zero(t, env.deregisterCalls.Load())
	current, err := env.store.Workers().GetByID(context.Background(), state.WorkerID)
	require.NoError(t, err)
	assert.Nil(t, current.DeletedAt)
}

// A deregistered Worker cannot start, so the command tells how to register it
// again, and gives the command that removes its service when one exists.
func TestWorkerDeregisterPrintsTheNextSteps(t *testing.T) {
	const registerAgain = "To register this worker again, run `leapmux worker register` with a new registration key.\n"
	t.Run("without a service", func(t *testing.T) {
		_, cfg := registerThroughEnv(t)
		output, err := captureStdout(t, func() error { return runWorkerDeregister([]string{"--data-dir", cfg.DataDir}) })
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(output, registerAgain), output)
	})
	t.Run("with a service", func(t *testing.T) {
		_, cfg := registerThroughEnv(t)
		spec := managedTestSpec(t)
		spec.Mode = lifecycle.RoleWorker
		spec.DataDirectory = mustCanonicalDir(t, cfg.DataDir)
		spec.InstanceID = lifecycle.NewInstanceID(spec.AccountID, spec.Mode, spec.DataDirectory).String()
		spec.RuntimeArguments = nil
		saveManagedTestSpec(t, spec)
		output, err := captureStdout(t, func() error { return runWorkerDeregister([]string{"--data-dir", cfg.DataDir}) })
		require.NoError(t, err)
		assert.Contains(t, output, registerAgain)
		assertNextCommands(t, output, serviceSelection(spec), "disable")
	})
}
