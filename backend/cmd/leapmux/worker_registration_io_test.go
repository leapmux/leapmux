package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
)

type pendingPublicationFailure struct {
	*config.Config
	failure error
}

func (f pendingPublicationFailure) SavePendingRegistration(*config.PendingRegistration) error {
	return f.failure
}

type uncertainCredentialPublication struct {
	*config.Config
	failure error
}

func (f uncertainCredentialPublication) SaveState(state *config.State) error {
	if err := f.Config.SaveState(state); err != nil {
		return err
	}
	return f.failure
}

type credentialPublicationFailure struct {
	*config.Config
	failure error
}

func (f credentialPublicationFailure) SaveState(*config.State) error { return f.failure }

type interruptedCredentialRemoval struct {
	*config.Config
	failure error
}

type credentialRemovalRefusal struct {
	*config.Config
	failure error
}

type pendingRemovalRefusal struct {
	*config.Config
	failure error
}

func (f pendingRemovalRefusal) RemovePendingRegistration() error { return f.failure }

func TestDeregisterRetainsCredentialsUntilPendingCleanupCompletes(t *testing.T) {
	env, cfg := registerThroughEnv(t)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	row, err := env.store.Workers().GetByID(t.Context(), state.WorkerID)
	require.NoError(t, err)
	require.NoError(t, cfg.SavePendingRegistration(&config.PendingRegistration{AttemptID: row.RegistrationAttemptID, HubURL: env.url, RegKey: env.key, KeyMaterial: state.KeyMaterial}))
	database := []byte("preserve worker database through pending cleanup retry")
	require.NoError(t, os.WriteFile(cfg.DBPath(), database, 0o600))
	failure := errors.New("pending recovery record removal failed")
	err = runWorkerDeregisterWithFiles(t.Context(), []string{"--data-dir", cfg.DataDir}, func(cfg *config.Config) workerRegistrationFiles { return pendingRemovalRefusal{cfg, failure} })
	require.ErrorIs(t, err, failure)
	assert.FileExists(t, cfg.StatePath(), "confirmed deletion retries still need the saved token until pending cleanup succeeds")
	assert.FileExists(t, cfg.PendingPath())
	deleted, err := env.store.Workers().GetByIDIncludeDeleted(t.Context(), state.WorkerID)
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
	require.NoError(t, runWorkerDeregister([]string{"--data-dir", cfg.DataDir}))
	assert.EqualValues(t, 2, env.deregisterCalls.Load())
	assert.NoFileExists(t, cfg.StatePath())
	assert.NoFileExists(t, cfg.PendingPath())
	after, err := os.ReadFile(cfg.DBPath())
	require.NoError(t, err)
	assert.Equal(t, database, after)
}

func (f credentialRemovalRefusal) ClearState() error { return f.failure }

func (f interruptedCredentialRemoval) ClearState() error {
	if err := os.Remove(f.StatePath()); err != nil {
		return err
	}
	return f.failure
}

func TestRegisterPendingPublicationFailurePreventsNetwork(t *testing.T) {
	env := startRegisterEnv(t)
	dataDir := t.TempDir()
	failure := errors.New("pending publication failed")
	err := runWorkerRegisterWithFiles(t.Context(), []string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir}, func(cfg *config.Config) workerRegistrationFiles {
		return pendingPublicationFailure{cfg, failure}
	})
	require.ErrorIs(t, err, failure)
	assert.Zero(t, env.registerCalls.Load())
	cfg := workerConfigOf(t, dataDir)
	assert.NoFileExists(t, cfg.StatePath())
	assert.NoFileExists(t, cfg.PendingPath())
	assert.FileExists(t, lifecycle.PathsOf(mustCanonicalDir(t, dataDir)).OwnershipRecord, "the fault must occur after ownership preparation")
}

func TestRegistrationFlagValueDoesNotOverridePendingHub(t *testing.T) {
	env := startRegisterEnv(t)
	root := t.TempDir()
	failure := errors.New("credential publication interrupted")
	err := runWorkerRegisterWithFiles(t.Context(), []string{"--hub", env.url, "--registration-key", env.key, "--data-dir", root}, func(cfg *config.Config) workerRegistrationFiles { return credentialPublicationFailure{cfg, failure} })
	require.ErrorIs(t, err, failure)
	t.Setenv("LEAPMUX_WORKER_HUB", "http://127.0.0.1:1")
	cfg := workerConfigOf(t, root)
	before, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	err = runWorkerRegister([]string{"--name", "--hub", "--registration-key", env.key, "--data-dir", root})
	require.ErrorContains(t, err, "--hub is required")
	after, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.EqualValues(t, 1, env.registerCalls.Load(), "a flag value must not count as an explicit endpoint flag")
	require.NoError(t, runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", root}))
	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered())
	assert.Equal(t, env.url, state.HubURL)
	assert.NoFileExists(t, cfg.PendingPath())
	assert.NoFileExists(t, cfg.DBPath(), "registration must not start the worker")
}

func TestRegisterReconfirmsStateBeforeRemovingPending(t *testing.T) {
	env := startRegisterEnv(t)
	dataDir := t.TempDir()
	args := []string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir}
	failure := errors.New("credential publication flush failed")
	err := runWorkerRegisterWithFiles(t.Context(), args, func(cfg *config.Config) workerRegistrationFiles {
		return uncertainCredentialPublication{cfg, failure}
	})
	require.ErrorIs(t, err, failure)
	cfg := workerConfigOf(t, dataDir)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered())
	pending, err := cfg.LoadPendingRegistration()
	require.NoError(t, err)
	require.NotNil(t, pending)
	before, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	err = runWorkerRegisterWithFiles(t.Context(), args, func(cfg *config.Config) workerRegistrationFiles {
		return credentialPublicationFailure{cfg, failure}
	})
	require.ErrorIs(t, err, failure, "cleanup must establish the credential durability barrier again")
	after, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.EqualValues(t, 1, env.registerCalls.Load())
	require.NoError(t, runWorkerRegister(args))
	assert.NoFileExists(t, cfg.PendingPath())
	assert.EqualValues(t, 1, env.registerCalls.Load(), "a cleanup retry must not register again")
	recovered, err := cfg.LoadState()
	require.NoError(t, err)
	assert.Equal(t, state, recovered)
}

func TestDeregisterRetryCleansCopiesAfterDestinationRemoval(t *testing.T) {
	env, cfg := registerThroughEnv(t)
	state, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	paths := []string{cfg.StatePath() + ".tmp-a", cfg.StatePath() + ".tmp-b"}
	for _, path := range paths {
		require.NoError(t, os.WriteFile(path, state, 0o600))
	}
	database := []byte("preserve this worker database")
	require.NoError(t, os.WriteFile(cfg.DBPath(), database, 0o600))
	failure := errors.New("temporary credential copy removal failed")
	err = runWorkerDeregisterWithFiles(t.Context(), []string{"--data-dir", cfg.DataDir}, func(cfg *config.Config) workerRegistrationFiles {
		return interruptedCredentialRemoval{cfg, failure}
	})
	require.ErrorIs(t, err, failure)
	assert.NoFileExists(t, cfg.StatePath())
	for _, path := range paths {
		assert.FileExists(t, path)
	}
	require.NoError(t, runWorkerDeregister([]string{"--data-dir", cfg.DataDir}))
	for _, path := range paths {
		assert.NoFileExists(t, path)
	}
	assert.EqualValues(t, 1, env.deregisterCalls.Load(), "absent-state cleanup needs no additional remote deletion")
	preserved, err := os.ReadFile(cfg.DBPath())
	require.NoError(t, err)
	assert.Equal(t, database, preserved)
}

func TestDeregisterCancellationAndRunningPreserveState(t *testing.T) {
	t.Run("cancel a pending remote result", func(t *testing.T) {
		env, cfg := registerThroughEnv(t)
		before, err := os.ReadFile(cfg.StatePath())
		require.NoError(t, err)
		database := []byte("worker database")
		require.NoError(t, os.WriteFile(cfg.DBPath(), database, 0o600))
		env.holdDeregister.Store(true)
		deadline := testutil.DeadlineContext(t)
		ctx, cancel := context.WithCancel(deadline)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- runWorkerDeregisterWithFiles(ctx, []string{"--data-dir", cfg.DataDir}, configuredWorkerRegistrationFiles)
		}()
		select {
		case <-env.deregisterReceived:
		case <-deadline.Done():
			t.Fatal("deregistration did not reach the held remote request")
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-deadline.Done():
			t.Fatal("deregistration ignored cancellation")
		}
		after, err := os.ReadFile(cfg.StatePath())
		require.NoError(t, err)
		assert.Equal(t, before, after)
		preserved, err := os.ReadFile(cfg.DBPath())
		require.NoError(t, err)
		assert.Equal(t, database, preserved)
	})
	t.Run("refuse while runtime holds the directory", func(t *testing.T) {
		env, cfg := registerThroughEnv(t)
		before, err := os.ReadFile(cfg.StatePath())
		require.NoError(t, err)
		runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleWorker, DataDir: cfg.DataDir, Execution: lifecycle.ExecutionForeground})
		require.NoError(t, err)
		t.Cleanup(func() { _ = runtime.Finish(nil) })
		require.ErrorContains(t, runWorkerDeregister([]string{"--data-dir", cfg.DataDir}), "stop")
		after, err := os.ReadFile(cfg.StatePath())
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Zero(t, env.deregisterCalls.Load())
	})
}

func TestHubDeletionClearsAllRegistrationMaterial(t *testing.T) {
	for _, refusePending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending removal refused=%v", refusePending), func(t *testing.T) {
			env, cfg := registerThroughEnv(t)
			state, err := cfg.LoadState()
			require.NoError(t, err)
			database := []byte("retain application data")
			require.NoError(t, os.WriteFile(cfg.DBPath(), database, 0o600))
			require.NoError(t, cfg.SavePendingRegistration(&config.PendingRegistration{AttemptID: "interrupted-attempt", HubURL: env.url, RegKey: env.key, KeyMaterial: state.KeyMaterial}))
			for _, path := range []string{cfg.StatePath(), cfg.PendingPath()} {
				require.NoError(t, atomicfile.WriteFileDurable(path+".tmp-interrupted", []byte("credential copy"), 0o600))
			}
			if refusePending {
				require.NoError(t, os.Remove(cfg.PendingPath()))
				require.NoError(t, os.MkdirAll(filepath.Join(cfg.PendingPath(), "prevent-removal"), 0o700))
			}
			err = clearDeletedWorkerState(cfg)
			if refusePending {
				require.Error(t, err)
				assert.FileExists(t, cfg.StatePath(), "retain the token until recovery material is removed")
				require.NoError(t, os.RemoveAll(cfg.PendingPath()))
				require.NoError(t, clearDeletedWorkerState(cfg))
			} else {
				require.NoError(t, err)
			}
			for _, path := range []string{cfg.StatePath(), cfg.PendingPath(), cfg.StatePath() + ".tmp-interrupted", cfg.PendingPath() + ".tmp-interrupted"} {
				assert.NoFileExists(t, path)
			}
			after, err := os.ReadFile(cfg.DBPath())
			require.NoError(t, err)
			assert.Equal(t, database, after)
			key := mintRegistrationFixtureKey(t, env.store)
			require.NoError(t, runWorkerRegister([]string{"--hub", env.url, "--registration-key", key, "--data-dir", cfg.DataDir}))
			replacement, err := cfg.LoadState()
			require.NoError(t, err)
			require.True(t, replacement.Registered())
			assert.NotEqual(t, state.WorkerID, replacement.WorkerID)
		})
	}
}
