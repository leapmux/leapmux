package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// newLeasedTestServer builds a server over a fresh store and a properly
// leased data directory, ready to Serve.
func newLeasedTestServer(t *testing.T) *Server {
	t.Helper()
	dataDir := t.TempDir()
	cfg := &config.Config{
		DataDir:  dataDir,
		Listen:   []string{locallistentest.UniqueListenURL(t, "lmx-hub-ready")},
		Storage:  config.StorageConfig{Type: config.StorageTypeSQLite},
		SoloMode: true,
	}
	srv, err := NewServer(cfg, testHubLease(t, dataDir))
	require.NoError(t, err)
	return srv
}

// TestReadyWaitsForHubInitialization pins readiness to initialization,
// not to the bound sockets: NewServer binds every listener, and Ready
// stays open until Serve completed its initialization and began
// serving. An initialization failure never signals readiness.
func TestReadyWaitsForHubInitialization(t *testing.T) {
	t.Run("ready closes once serving begins", func(t *testing.T) {
		srv := newLeasedTestServer(t)
		select {
		case <-srv.Ready():
			t.Fatal("Ready cannot close before Serve runs")
		default:
		}

		serveCtx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- srv.Serve(serveCtx) }()
		t.Cleanup(func() {
			cancel()
			require.NoError(t, <-served)
		})

		select {
		case <-srv.Ready():
		case err := <-served:
			// Serve ended before readiness: fine only when it was asked
			// to; its error is still checked.
			require.NoError(t, err)
			select {
			case <-serveCtx.Done():
			default:
				t.Fatal("Serve ended before Ready without being asked to")
			}
		}
	})

	t.Run("a lease NewServer refuses never reaches a server", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := &config.Config{DataDir: dataDir, Storage: config.StorageConfig{Type: config.StorageTypeSQLite}}
		_, err := NewServer(cfg, nil)
		require.Error(t, err, "a nil lease is refused before any listener binds")

		closed := testHubLease(t, dataDir)
		require.NoError(t, closed.Close())
		_, err = NewServer(cfg, closed)
		require.Error(t, err, "a closed lease is refused")

		// A lease for another component or directory is refused too.
		other := testHubLease(t, t.TempDir())
		_, err = NewServer(cfg, other)
		require.Error(t, err, "a lease over another directory is refused")
	})
}

// TestServerConsumesDirectoryLease states the ownership contract: a
// successful construction owns the lease, a refused one does not, and
// Serve's teardown releases it after the store closes.
func TestServerConsumesDirectoryLease(t *testing.T) {
	t.Run("a refused construction leaves the lease with the caller", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := &config.Config{DataDir: dataDir, Storage: config.StorageConfig{Type: config.StorageTypeSQLite}}
		// A worker-component lease over the same directory: refused.
		canonical, err := lifecycle.CanonicalDir(dataDir)
		require.NoError(t, err)
		wrong := lifecycle.DirectoryOwner{AccountID: lifecycle.CurrentAccountID(), Mode: lifecycle.RoleSolo, Root: canonical}
		lease, err := lifecycle.AcquireDirectory(wrong, lifecycle.RoleWorker, canonical)
		require.NoError(t, err)

		_, err = NewServer(cfg, lease)
		require.Error(t, err)
		assert.False(t, lease.Closed(), "the refused construction returns the lease unreleased")
		require.NoError(t, lease.Close())
	})

	t.Run("Serve releases the lease with its teardown", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := &config.Config{
			DataDir: dataDir,
			Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
			Listen:  []string{locallistentest.UniqueListenURL(t, "lmx-hub-ready-serve")},
		}
		lease := testHubLease(t, dataDir)
		srv, err := NewServer(cfg, lease)
		require.NoError(t, err)

		serveCtx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- srv.Serve(serveCtx) }()
		<-srv.Ready()
		cancel()
		require.NoError(t, <-served)
		assert.True(t, lease.Closed(), "Serve's teardown releases the data lease")
	})
}
