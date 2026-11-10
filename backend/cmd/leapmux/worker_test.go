package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
)

func TestRejectedStandaloneWorkerReleasesDirectory(t *testing.T) {
	for _, launcher := range []string{"standalone", "foreground"} {
		for _, scenario := range []string{"missing", "malformed", "missing keys", "invalid keys", "invalid endpoint"} {
			t.Run(launcher+"/"+scenario, func(t *testing.T) {
				dataDir := t.TempDir()
				switch scenario {
				case "malformed":
					require.NoError(t, atomicfile.WriteFileDurable(filepath.Join(dataDir, "state.json"), []byte("invalid JSON"), 0o600))
				case "missing keys", "invalid keys", "invalid endpoint":
					cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
					dataDir = cfg.DataDir
					state, err := cfg.LoadState()
					require.NoError(t, err)
					switch scenario {
					case "missing keys":
						state.KeyMaterial = config.KeyMaterial{}
					case "invalid keys":
						state.X25519PrivateKey = "not-valid-base64!"
					case "invalid endpoint":
						state.HubURL = "unsupported:hub"
					}
					require.NoError(t, cfg.SaveState(state))
				}
				if launcher == "standalone" {
					require.Error(t, runWorkerCommandForTest(t, []string{"--data-dir", dataDir}))
				} else {
					runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleWorker, DataDir: dataDir, Execution: lifecycle.ExecutionForeground})
					require.NoError(t, err)
					t.Cleanup(func() { _ = runtime.Finish(nil) })
					require.Error(t, runWorkerForeground(t.Context(), runtime, preparedWorkerConfig(t, dataDir)))
					require.NoError(t, runtime.Finish(nil))
				}
				canonical, err := lifecycle.CanonicalDir(dataDir)
				require.NoError(t, err)
				lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
				require.NoError(t, err, "the launcher must retain cleanup until Run consumes its lease")
				require.NoError(t, lease.Close())
			})
		}
	}
}

// startFakeHub serves the production worker WebSocket protocol.
func startFakeHub(t *testing.T, handle func(context.Context, *fakehubtest.Stream) error) string {
	t.Helper()
	return fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) { _ = handle(ctx, stream) })
}

// startReceivingHub reports every received frame without blocking the stream.
func startReceivingHub(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	connected := make(chan struct{}, 1)
	hubURL := startFakeHub(t, func(ctx context.Context, stream *fakehubtest.Stream) error {
		greeted := false
		for {
			if _, err := stream.Receive(ctx); err != nil {
				return ctx.Err()
			}
			if !greeted {
				greeted = true
				if err := stream.Greet(ctx, "owner"); err != nil {
					return err
				}
			}
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	return hubURL, connected
}

// startRefusingHub answers each upgraded stream with a permanent refusal.
func startRefusingHub(t *testing.T, reason string) string {
	t.Helper()
	return fakehubtest.Start(t, func(_ context.Context, stream *fakehubtest.Stream) {
		_ = stream.Refuse(websocket.StatusPolicyViolation, reason)
	})
}

// writeRegisteredState writes the state of a Worker that already
// registered against hubURL, and returns the configuration that reads
// it. The saved endpoint is the one the runtime dials: a --hub flag on
// a runtime command never redirects saved credentials.
func writeRegisteredStateAgainst(t *testing.T, hubURL string) *config.Config {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir()}
	// The keypair comes first: a state that holds a token refuses to generate one.
	state := &config.State{HubURL: hubURL}
	_, _, err := state.EnsureCompositeKeypair()
	require.NoError(t, err)
	state.WorkerID = "worker-1"
	state.AuthToken = "token"
	require.NoError(t, cfg.SaveState(state))
	return cfg
}

// A Worker that the Hub deleted forgets its registration and exits cleanly.
//
// Not parallel: the worker command sets the process-global log level.
func TestRunWorkerClearsTheStateOfADeletedWorker(t *testing.T) {
	hubURL := startRefusingHub(t, channelwire.WorkerCloseReasonDeleted)
	cfg := writeRegisteredStateAgainst(t, hubURL)

	require.NoError(t, runWorkerCommandForTest(t, []string{"--data-dir", cfg.DataDir, "--log-level", "error"}))

	_, err := os.Stat(cfg.StatePath())
	assert.ErrorIs(t, err, os.ErrNotExist, "a deleted worker must forget its registration")
}

// A Worker whose keys the Hub refuses keeps its state, because the deregistration
// of the old row needs it, and reports the remedy.
//
// Not parallel: the worker command sets the process-global log level.
func TestRunWorkerKeepsTheStateAndAdvisesWhenTheHubRefusesItsKeys(t *testing.T) {
	hubURL := startRefusingHub(t, channelwire.WorkerCloseReasonKeysDiffer)
	cfg := writeRegisteredStateAgainst(t, hubURL)

	err := runWorkerCommandForTest(t, []string{"--data-dir", cfg.DataDir, "--log-level", "error"})

	require.ErrorIs(t, err, hub.ErrKeysRefused)
	assert.Contains(t, err.Error(), reregistrationSteps)
	_, statErr := os.Stat(cfg.StatePath())
	assert.NoError(t, statErr, "a key refusal must keep the worker's state")
}

// Each supported shutdown signal drains a connected Worker before exit.
func TestWorkerExitsCleanlyOnShutdownSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process signals are unavailable on Windows")
	}
	for _, tc := range []struct {
		name   string
		signal os.Signal
	}{{name: "sigterm", signal: syscall.SIGTERM}, {name: "sighup", signal: syscall.SIGHUP}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			hubURL, connected := startReceivingHub(t)
			cfg := writeRegisteredStateAgainst(t, hubURL)

			cmd := exec.Command(os.Args[0], "worker", "run", "--data-dir", cfg.DataDir, "--log-level", "info")
			cmd.Env = append(os.Environ(), runMainEnv+"=1")
			var output testutil.LogBuffer
			cmd.Stdout, cmd.Stderr = &output, &output
			require.NoError(t, cmd.Start())
			exited := make(chan error, 1)
			reaped := make(chan struct{})
			go func() {
				exited <- cmd.Wait()
				close(reaped)
			}()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-reaped
			})

			select {
			case <-connected:
			case err := <-exited:
				require.FailNow(t, "the worker exited before it connected", "%v\n%s", err, output.String())
			case <-ctx.Done():
				require.FailNow(t, "the worker did not connect", "%s", output.String())
			}

			require.NoError(t, cmd.Process.Signal(tc.signal))
			select {
			case err := <-exited:
				require.NoError(t, err, "%s", output.String())
			case <-ctx.Done():
				require.FailNow(t, "the worker did not exit after the signal", "%s", output.String())
			}
			assert.Contains(t, output.String(), "shutdown signal received")
			assert.Contains(t, output.String(), "worker state drained", "the shutdown must finish before the exit")
			_, err := os.Stat(cfg.StatePath())
			assert.NoError(t, err, "a signal must keep the worker's state")
		})
	}
}

// The re-registration advice appears on exactly the two errors no reconnect can
// fix, and every other error is returned unchanged.
func TestReregistrationAdvice(t *testing.T) {
	t.Parallel()

	keysRefused := reregistrationAdvice(errors.Join(errors.New("connect"), hub.ErrKeysRefused))
	require.Error(t, keysRefused)
	require.ErrorIs(t, keysRefused, hub.ErrKeysRefused, "the advice must keep the cause")
	assert.Equal(t, 1, strings.Count(keysRefused.Error(), reregistrationSteps),
		"the advice must appear exactly once")

	unusable := reregistrationAdvice(errors.Join(errors.New("restore"), config.ErrUnusableRegisteredKeypair))
	require.Error(t, unusable)
	require.ErrorIs(t, unusable, config.ErrUnusableRegisteredKeypair, "the advice must keep the cause")
	assert.Equal(t, 1, strings.Count(unusable.Error(), reregistrationSteps),
		"the advice must appear exactly once")

	other := errors.New("the hub is unreachable")
	assert.Equal(t, other, reregistrationAdvice(other),
		"an error a reconnect can fix gets no advice")
	assert.NoError(t, reregistrationAdvice(nil))
}

// The re-registration steps use `worker deregister`, which removes the saved
// credentials itself once the Hub confirms. A step that deletes state.json by
// hand would be redundant after it, and before it would leave the old row
// ACTIVE -- counting against max_workers_per_user and keeping its token valid
// for any copy of the file.
func TestReregistrationStepsDeleteNoFileByHand(t *testing.T) {
	t.Parallel()

	assert.Contains(t, reregistrationSteps, "leapmux worker deregister")
	assert.Contains(t, reregistrationSteps, "leapmux worker register")
	assert.NotContains(t, reregistrationSteps, "state.json")
	assert.NotContains(t, reregistrationSteps, "delete")
}

// An already-registered worker is told how to start it, and how to register it
// again. The register command requires the key, so the refusal never tells the
// operator to drop it.
func TestAlreadyRegisteredError(t *testing.T) {
	t.Parallel()

	err := alreadyRegisteredError(&config.State{HubURL: "https://hub.example", WorkerID: "worker-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://hub.example", "the refusal must identify the saved Hub")
	assert.Contains(t, err.Error(), "leapmux worker start")
	assert.Contains(t, err.Error(), "leapmux worker run")
	assert.NotContains(t, err.Error(), "--registration-key",
		"the register command requires the key, so dropping it is no remedy")
	assert.Equal(t, 1, strings.Count(err.Error(), reregistrationSteps),
		"the steps must appear exactly once")
}

// The deregistration callback's clear must report a failure it cannot undo, so a
// worker does not exit with state.json still on disk and repeat the same silent
// failure at every start. An absent file is already the desired end state.
func TestClearDeletedWorkerState(t *testing.T) {
	t.Parallel()

	t.Run("an absent file is nil", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		assert.NoError(t, clearDeletedWorkerState(cfg))
	})

	t.Run("a present file is removed", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		require.NoError(t, os.WriteFile(cfg.StatePath(), []byte("{}"), 0o600))

		require.NoError(t, clearDeletedWorkerState(cfg))
		_, err := os.Stat(cfg.StatePath())
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("a path that cannot be removed states the path", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{DataDir: t.TempDir()}
		// A NON-EMPTY directory cannot be removed by os.Remove on any platform,
		// which is the failure the discarded error used to hide.
		require.NoError(t, os.MkdirAll(filepath.Join(cfg.StatePath(), "inner"), 0o700))

		err := clearDeletedWorkerState(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), cfg.StatePath())
	})
}

func TestWorkerCommandReportsDeregistrationCleanupFailure(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	hubURL := startFakeHub(t, func(ctx context.Context, stream *fakehubtest.Stream) error {
		if _, err := stream.Receive(ctx); err != nil {
			return err
		}
		close(arrived)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return stream.Refuse(websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted)
	})
	cfg := writeRegisteredStateAgainst(t, hubURL)
	before, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	command := exec.Command(os.Args[0], "worker", "run", "--data-dir", cfg.DataDir)
	command.Env = append(os.Environ(), runMainEnv+"=1")
	var output testutil.LogBuffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan struct{})
	var commandErr error
	go func() { commandErr = command.Wait(); close(done) }()
	t.Cleanup(func() { released.Do(func() { close(release) }); _ = command.Process.Kill(); <-done })
	guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-arrived:
	case <-done:
		t.Fatalf("worker exited before the held refusal: %v\n%s", commandErr, output.String())
	case <-guard.Done():
		t.Fatal("worker never attempted the Hub connection")
	}
	canonical := mustCanonicalDir(t, cfg.DataDir)
	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(guard, canonical)
		return err == nil && status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReady
	}, 30*time.Second, 10*time.Millisecond)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.PendingPath(), "prevent-removal"), 0o700))
	released.Do(func() { close(release) })
	select {
	case <-done:
	case <-guard.Done():
		t.Fatal("worker did not join after the deletion refusal")
	}
	assert.Error(t, commandErr, output.String())
	status, err := lifecycle.Observe(guard, canonical)
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ProcessFailed, status.Process.State)
	require.NotNil(t, status.Attempt.Error)
	assert.Contains(t, *status.Attempt.Error, filepath.Join(canonical, filepath.Base(cfg.PendingPath())))
	after, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	assert.Equal(t, before, after, "retain the token until pending material cleanup succeeds")
	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	require.NoError(t, err)
	require.NoError(t, lease.Close())
}
