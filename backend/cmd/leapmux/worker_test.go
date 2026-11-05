package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

// fakeConnector answers the Worker's Connect stream with connect.
type fakeConnector struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler
	connect func(context.Context, leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error
}

func (c *fakeConnector) Connect(ctx context.Context, stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
	return c.connect(ctx, stream)
}

// startFakeHub serves connector over h2c, which the Worker's Connect stream
// requires.
func startFakeHub(t *testing.T, connector *fakeConnector) string {
	t.Helper()
	mux := http.NewServeMux()
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, connector)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	httpServer := httptest.NewUnstartedServer(mux)
	httpServer.Config.Protocols = &http.Protocols{}
	httpServer.Config.Protocols.SetHTTP1(true)
	httpServer.Config.Protocols.SetUnencryptedHTTP2(true)
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

// startRefusingHub answers every Connect with a refusal for good.
func startRefusingHub(t *testing.T, code connect.Code, reason leapmuxv1.WorkerConnectRefusalReason) string {
	t.Helper()
	return startFakeHub(t, &fakeConnector{connect: func(context.Context, leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
		detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: reason})
		if err != nil {
			return err
		}
		return connect.NewError(code, "the hub refuses this worker").WithDetail(detail)
	}})
}

// writeRegisteredState writes the state of a Worker that already registered, and
// returns the data directory that holds it.
func writeRegisteredState(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir()}
	// The keypair comes first: a state that holds a token refuses to generate one.
	state := &config.State{}
	_, _, err := state.EnsureCompositeKeypair()
	require.NoError(t, err)
	state.WorkerID = "worker-1"
	state.AuthToken = "token"
	require.NoError(t, cfg.SaveState(state))
	return cfg
}

// A Worker that the Hub deleted forgets its registration and exits cleanly.
//
// Not parallel: runWorker sets the process-global log level.
func TestRunWorkerClearsTheStateOfADeletedWorker(t *testing.T) {
	cfg := writeRegisteredState(t)
	hubURL := startRefusingHub(t, connect.CodeUnauthenticated,
		leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED)

	require.NoError(t, runWorker([]string{"--hub", hubURL, "--data-dir", cfg.DataDir, "--log-level", "error"}))

	_, err := os.Stat(cfg.StatePath())
	assert.ErrorIs(t, err, os.ErrNotExist, "a deleted worker must forget its registration")
}

// A Worker whose keys the Hub refuses keeps its state, because the deregistration
// of the old row needs it, and reports the remedy.
//
// Not parallel: runWorker sets the process-global log level.
func TestRunWorkerKeepsTheStateAndAdvisesWhenTheHubRefusesItsKeys(t *testing.T) {
	cfg := writeRegisteredState(t)
	hubURL := startRefusingHub(t, connect.CodePermissionDenied,
		leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER)

	err := runWorker([]string{"--hub", hubURL, "--data-dir", cfg.DataDir, "--log-level", "error"})

	require.ErrorIs(t, err, hub.ErrKeysRefused)
	assert.Contains(t, err.Error(), reregistrationSteps(cfg.StatePath()))
	_, statErr := os.Stat(cfg.StatePath())
	assert.NoError(t, statErr, "a key refusal must keep the worker's state")
}

// A signal ends a connected Worker with a clean exit, after its shutdown ran.
// The child is the real entry point, because the signal must reach the process.
func TestWorkerExitsCleanlyOnASignal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("os.Process.Signal cannot send SIGTERM on Windows")
	}
	ctx := testutil.DeadlineContext(t)
	connected := make(chan struct{}, 1)
	hubURL := startFakeHub(t, &fakeConnector{connect: func(ctx context.Context, stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
		for {
			if _, err := stream.Receive(); err != nil {
				return ctx.Err()
			}
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}})
	cfg := writeRegisteredState(t)

	cmd := exec.Command(os.Args[0], "worker", "--hub", hubURL, "--data-dir", cfg.DataDir, "--log-level", "info")
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	select {
	case <-connected:
	case err := <-exited:
		require.FailNow(t, "the worker exited before it connected", "%v\n%s", err, output.String())
	case <-ctx.Done():
		require.FailNow(t, "the worker did not connect", "%s", output.String())
	}

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
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
}

// The hang-up of the terminal that runs the worker must take the same clean
// path: the worker drains its state before it exits.
func TestWorkerExitsCleanlyOnAHangUp(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("os.Process.Signal cannot send SIGHUP on Windows")
	}
	ctx := testutil.DeadlineContext(t)
	connected := make(chan struct{}, 1)
	hubURL := startFakeHub(t, &fakeConnector{connect: func(ctx context.Context, stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
		for {
			if _, err := stream.Receive(); err != nil {
				return ctx.Err()
			}
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}})
	cfg := writeRegisteredState(t)

	cmd := exec.Command(os.Args[0], "worker", "--hub", hubURL, "--data-dir", cfg.DataDir, "--log-level", "info")
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	select {
	case <-connected:
	case err := <-exited:
		require.FailNow(t, "the worker exited before it connected", "%v\n%s", err, output.String())
	case <-ctx.Done():
		require.FailNow(t, "the worker did not connect", "%s", output.String())
	}

	require.NoError(t, cmd.Process.Signal(syscall.SIGHUP))
	select {
	case err := <-exited:
		require.NoError(t, err, "%s", output.String())
	case <-ctx.Done():
		require.FailNow(t, "the worker did not exit after the hang-up", "%s", output.String())
	}
	assert.Contains(t, output.String(), "shutdown signal received")
	assert.Contains(t, output.String(), "worker state drained", "the shutdown must finish before the exit")
	_, err := os.Stat(cfg.StatePath())
	assert.NoError(t, err, "a hang-up must keep the worker's state")
}

// The re-registration advice appears on exactly the two errors no reconnect can
// fix, and every other error is returned unchanged.
func TestReregistrationAdvice(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state.json")

	keysRefused := reregistrationAdvice(
		errors.Join(errors.New("connect"), hub.ErrKeysRefused), statePath)
	require.Error(t, keysRefused)
	assert.Equal(t, 1, strings.Count(keysRefused.Error(), reregistrationSteps(statePath)),
		"the advice must appear exactly once")
	assert.Contains(t, keysRefused.Error(), statePath)

	unusable := reregistrationAdvice(
		errors.Join(errors.New("restore"), config.ErrUnusableRegisteredKeypair), statePath)
	require.Error(t, unusable)
	assert.Equal(t, 1, strings.Count(unusable.Error(), reregistrationSteps(statePath)),
		"the advice must appear exactly once")
	assert.Contains(t, unusable.Error(), statePath)

	other := errors.New("the hub is unreachable")
	assert.Equal(t, other, reregistrationAdvice(other, statePath),
		"an error a reconnect can fix gets no advice")
	assert.NoError(t, reregistrationAdvice(nil, statePath))
}

// An already-registered worker is told to deregister and register again. Wiping
// the state alone would leave the old row ACTIVE -- counting against
// max_workers_per_user and keeping its token valid for any copy of the file.
func TestAlreadyRegisteredError(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "state.json")

	err := alreadyRegisteredError(statePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove --registration-key")
	assert.Equal(t, 1, strings.Count(err.Error(), reregistrationSteps(statePath)),
		"the steps must appear exactly once")
	assert.NotContains(t, err.Error(), "wipe",
		"wiping the state would leave the old row ACTIVE")
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
