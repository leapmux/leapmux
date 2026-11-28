package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
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
	const registerAgain = "To register this worker again, run `leapmux daemon worker register` with a new registration key.\n"
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

// A saved token that the Hub refuses fails with the steps that remove the
// Worker without it, and the credentials stay.
func TestWorkerDeregisterRefusedTokenHintsTheManualRemoval(t *testing.T) {
	env := startRegisterEnv(t)
	cfg := writeRegisteredStateAgainst(t, env.url)
	err := runWorkerDeregister([]string{"--data-dir", cfg.DataDir})
	require.Error(t, err)
	failure := cli.FromError(err)
	assert.Equal(t, cli.CodeNotLoggedIn, failure.Code, failure.Message)
	assert.Equal(t, []string{"remove this Worker in the Worker list of the Hub, then delete " + cfg.StatePath()}, failure.Hints)
	assert.FileExists(t, cfg.StatePath(), "an unconfirmed deregistration keeps the credentials")
}

func TestWorkerRegisterAndDeregisterJSONAndQuietFlags(t *testing.T) {
	t.Run("quiet flag is rejected on register and deregister", func(t *testing.T) {
		reg := runTestCLI(t, "daemon", "worker", "register", "--hub", "https://hub.example", "--registration-key", "key", "-q")
		assert.Equal(t, cli.ExitUsage, reg.code)

		dereg := runTestCLI(t, "daemon", "worker", "deregister", "--data-dir", t.TempDir(), "-q")
		assert.Equal(t, cli.ExitUsage, dereg.code)
	})

	t.Run("register and deregister emit valid JSON envelopes without prose", func(t *testing.T) {
		env := startRegisterEnv(t)
		dataDir := t.TempDir()

		var regOut, regErr bytes.Buffer
		regCode := runCLI(
			t.Context(),
			[]string{"daemon", "worker", "register", "--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir, "--json"},
			strings.NewReader(""),
			&regOut,
			&regErr,
			defaultCLIRunners(),
		)
		require.Equal(t, cli.ExitOK, regCode, regErr.String())
		var regEnv struct {
			Data workerRegisterResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(regOut.Bytes(), &regEnv), regOut.String())
		assert.Equal(t, "registered", regEnv.Data.Status)
		assert.NotEmpty(t, regEnv.Data.WorkerID)
		assert.Equal(t, env.url, regEnv.Data.HubURL)

		var deregOut, deregErr bytes.Buffer
		deregCode := runCLI(
			t.Context(),
			[]string{"daemon", "worker", "deregister", "--data-dir", dataDir, "--json"},
			strings.NewReader(""),
			&deregOut,
			&deregErr,
			defaultCLIRunners(),
		)
		require.Equal(t, cli.ExitOK, deregCode, deregErr.String())
		var deregEnv struct {
			Data workerDeregisterResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(deregOut.Bytes(), &deregEnv), deregOut.String())
		assert.Equal(t, "deregistered", deregEnv.Data.Status)
		assert.Equal(t, regEnv.Data.WorkerID, deregEnv.Data.WorkerID)
	})

	t.Run("deregister --json on unregistered worker reports not_registered and suppresses banner", func(t *testing.T) {
		original := printModeBanner
		var banners []string
		printModeBanner = func(mode string) { banners = append(banners, mode) }
		t.Cleanup(func() { printModeBanner = original })

		dataDir := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := runCLI(
			t.Context(),
			[]string{"daemon", "worker", "deregister", "--data-dir", dataDir, "--json"},
			strings.NewReader(""),
			&stdout,
			&stderr,
			defaultCLIRunners(),
		)
		require.Equal(t, cli.ExitOK, code, stderr.String())
		assert.Empty(t, banners, "--json must suppress the mode banner")
		var env struct {
			Data workerDeregisterResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &env), stdout.String())
		assert.Equal(t, "not_registered", env.Data.Status)
		assert.Empty(t, env.Data.WorkerID)
		assert.Empty(t, env.Data.HubURL)
		assert.NotEmpty(t, env.Data.StatePath)
	})

	t.Run("empty required flags and non-positive timeouts fail with ExitUsage", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			args    []string
			wantErr string
		}{
			{
				name:    "empty hub",
				args:    []string{"daemon", "worker", "register", "--hub", "", "--registration-key", "key"},
				wantErr: "--hub is empty",
			},
			{
				name:    "empty registration key",
				args:    []string{"daemon", "worker", "register", "--hub", "https://hub.example", "--registration-key", ""},
				wantErr: "--registration-key is empty",
			},
			{
				name:    "register non-positive timeout",
				args:    []string{"daemon", "worker", "register", "--hub", "https://hub.example", "--registration-key", "key", "--timeout", "0s"},
				wantErr: "--timeout must be positive",
			},
			{
				name:    "deregister non-positive timeout",
				args:    []string{"daemon", "worker", "deregister", "--data-dir", t.TempDir(), "--timeout", "-1s"},
				wantErr: "--timeout must be positive",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				o := runTestCLI(t, tc.args...)
				assert.Equal(t, cli.ExitUsage, o.code)
				assert.Contains(t, o.stderr, tc.wantErr)
			})
		}
	})
}
