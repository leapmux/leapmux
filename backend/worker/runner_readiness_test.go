package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/worker"
)

// startOfflineHub serves a hub whose Connect never answers: it accepts
// the connection and holds it, the shape of a registered worker whose
// Hub is unavailable mid-stream. The worker is already locally ready,
// and its connection hook reports the disconnect once the stream ends.
func startOfflineHub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Hold the request open until the client gives up: an
		// unavailable hub that accepted the connection.
		<-r.Context().Done()
	})
	httpServer := httptest.NewUnstartedServer(mux)
	httpServer.Config.Protocols = &http.Protocols{}
	httpServer.Config.Protocols.SetHTTP1(true)
	httpServer.Config.Protocols.SetUnencryptedHTTP2(true)
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

// TestRegisteredOfflineWorkerBecomesLocallyReady pins the two facts the
// plan separates: a registered worker whose Hub is unavailable becomes
// LOCALLY READY (OnReady fires after the database opens), and its
// connection reports disconnected. A database that does not open never
// signals readiness.
func TestRegisteredOfflineWorkerBecomesLocallyReady(t *testing.T) {
	hubURL := startOfflineHub(t)
	lease := workerLeaseForTest(t)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	ready := make(chan struct{}, 1)
	var states atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx, worker.RunConfig{
			HubURL:         hubURL,
			Directory:      lease,
			AuthToken:      "token",
			CompositeKey:   key,
			WorkerID:       "worker-1",
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			OnReady: func() {
				ready <- struct{}{}
			},
			OnConnectionState: func(state worker.ConnectionState) {
				if !state.Connected {
					states.Add(1)
				}
			},
		})
	}()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the worker never became locally ready against an unavailable hub")
	}
	// The worker reports its disconnected hub while it stays ready.
	require.Eventually(t, func() bool { return states.Load() > 0 },
		30*time.Second, 50*time.Millisecond, "the disconnected hub is reported")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the worker did not stop after the cancellation")
	}
}

// TestRunConsumesDirectoryLease states the ownership contract: nil,
// closed, wrong-mode, and wrong-path leases fail before any database or
// network work; a valid lease releases only after the runtime cleaned
// up, including on a constructor-level failure.
func TestRunConsumesDirectoryLease(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	runCfg := func(lease *lifecycle.DirectoryLease) worker.RunConfig {
		return worker.RunConfig{
			HubURL:         "http://127.0.0.1:1",
			Directory:      lease,
			AuthToken:      "token",
			CompositeKey:   key,
			WorkerID:       "worker-1",
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		}
	}

	t.Run("nil lease", func(t *testing.T) {
		err := worker.Run(context.Background(), runCfg(nil))
		require.ErrorContains(t, err, "Directory")
	})
	t.Run("empty lease", func(t *testing.T) {
		lease := new(lifecycle.DirectoryLease)
		require.NotPanics(t, func() {
			require.Error(t, worker.Run(context.Background(), runCfg(lease)))
		})
		require.True(t, lease.Closed())
	})

	t.Run("closed lease", func(t *testing.T) {
		lease := workerLeaseForTest(t)
		require.NoError(t, lease.Close())
		require.ErrorContains(t, worker.Run(context.Background(), runCfg(lease)), "closed lease")
	})

	t.Run("wrong-mode lease", func(t *testing.T) {
		canonical, err := lifecycle.CanonicalDir(t.TempDir())
		require.NoError(t, err)
		lease, err := lifecycle.AcquireDirectory(
			lifecycle.NewDirectoryOwner(lifecycle.RoleHub, canonical), lifecycle.RoleHub, canonical)
		require.NoError(t, err)
		defer func() { _ = lease.Close() }()
		require.ErrorContains(t, worker.Run(context.Background(), runCfg(lease)), "worker component")
		require.True(t, lease.Closed(), "Run consumes every non-nil supplied lease, including rejection")
		replacement, err := lifecycle.AcquireDirectory(lease.Owner(), lease.Mode(), lease.Path())
		require.NoError(t, err)
		require.NoError(t, replacement.Close())
	})

	t.Run("an ended run releases the lease", func(t *testing.T) {
		lease := workerLeaseForTest(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- worker.Run(ctx, runCfg(lease))
		}()
		// The run retries against the unreachable hub until it is asked
		// to stop; the lease stays held for exactly that long.
		require.False(t, lease.Closed(), "the lease stays held while the run retries")
		cancel()
		select {
		case err := <-done:
			// A cancellation that lands mid-startup surfaces as the
			// wrapped context error; either way the run ended and the
			// lease must release.
			if err != nil {
				require.ErrorIs(t, err, context.Canceled)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the run did not end after the cancellation")
		}
		require.True(t, lease.Closed(), "the lease releases when the run ends")
	})
	t.Run("database failure never reports readiness and releases the lease", func(t *testing.T) {
		lease := workerLeaseForTest(t)
		require.NoError(t, os.Mkdir(filepath.Join(lease.Path(), "worker.db"), 0o700))
		cfg := runCfg(lease)
		var ready atomic.Bool
		cfg.OnReady = func() { ready.Store(true) }
		require.ErrorContains(t, worker.Run(context.Background(), cfg), "open worker db")
		require.False(t, ready.Load())
		require.True(t, lease.Closed())
	})
}
