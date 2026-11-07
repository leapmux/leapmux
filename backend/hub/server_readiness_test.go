package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten"
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

type readinessStore struct {
	store.Store
	events store.RevocationEventStore
}

func (s readinessStore) RevocationEvents() store.RevocationEventStore { return s.events }

type readinessEvents struct {
	store.RevocationEventStore
	entered chan struct{}
	release chan struct{}
	failure error
}

func (s readinessEvents) AcquireHubRuntimeLease(ctx context.Context, p store.AcquireHubRuntimeLeaseParams) (int64, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if s.failure != nil {
		return 0, s.failure
	}
	return s.RevocationEventStore.AcquireHubRuntimeLease(ctx, p)
}

func TestHubReadinessWaitsForRequiredInitializationAndRemainsOpenOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx := testutil.DeadlineContext(t)
			srv := newLeasedTestServer(t)
			require.NoError(t, srv.revocationWatcher.Close(ctx))
			events := readinessEvents{RevocationEventStore: srv.store.RevocationEvents(), entered: make(chan struct{}), release: make(chan struct{})}
			failure := errors.New("initial revocation cursor failed")
			if fail {
				events.failure = failure
			}
			srv.revocationWatcher = revocationwatcher.New(readinessStore{srv.store, events}, auth.NewCredentialLifecycleEffects(nil, nil, nil))
			serveCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- srv.Serve(serveCtx) }()
			select {
			case <-events.entered:
			case <-ctx.Done():
				t.Fatal("initialization did not begin")
			}
			select {
			case <-srv.Ready():
				t.Fatal("a bound listener must not signal initialization readiness")
			default:
			}
			close(events.release)
			if fail {
				require.ErrorIs(t, <-done, failure)
				select {
				case <-srv.Ready():
					t.Fatal("failed initialization must not signal readiness")
				default:
				}
			} else {
				select {
				case <-srv.Ready():
				case <-ctx.Done():
					t.Fatal("initialization did not finish")
				}
				cancel()
				require.NoError(t, <-done)
			}
			require.True(t, srv.directory.Closed())
		})
	}
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

// TestServerConsumesDirectoryLease states the ownership contract:
// every construction consumes a supplied lease, and Serve's teardown releases
// the lease after the store closes.
func TestServerConsumesDirectoryLease(t *testing.T) {
	t.Run("a refused construction releases the supplied lease", func(t *testing.T) {
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
		assert.True(t, lease.Closed(), "the refused construction consumes and releases its supplied lease")
		replacement, err := lifecycle.AcquireDirectory(wrong, lifecycle.RoleWorker, canonical)
		require.NoError(t, err)
		require.NoError(t, replacement.Close())
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

func TestServerRejectionConsumesLeaseBeforeConfigWrites(t *testing.T) {
	t.Run("empty lease is rejected without a cleanup panic", func(t *testing.T) {
		lease := new(lifecycle.DirectoryLease)
		require.NotPanics(t, func() {
			_, err := NewServer(&config.Config{DataDir: t.TempDir()}, lease)
			require.Error(t, err)
		})
		assert.True(t, lease.Closed())
	})
	t.Run("nil lease never creates the configured directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "must-not-exist")
		_, err := NewServer(&config.Config{DataDir: dataDir}, nil)
		require.Error(t, err)
		assert.NoDirExists(t, dataDir)
	})
	t.Run("wrong path never creates the configured directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "must-not-exist")
		lease := testHubLease(t, t.TempDir())
		_, err := NewServer(&config.Config{DataDir: dataDir}, lease)
		require.Error(t, err)
		assert.NoDirExists(t, dataDir)
		assert.True(t, lease.Closed())
	})
	t.Run("nil configuration consumes the lease", func(t *testing.T) {
		lease := testHubLease(t, t.TempDir())
		require.NotPanics(t, func() {
			_, err := NewServer(nil, lease)
			require.Error(t, err)
		})
		assert.True(t, lease.Closed())
	})
	t.Run("configuration failure releases the lease", func(t *testing.T) {
		dataDir := t.TempDir()
		lease := testHubLease(t, dataDir)
		_, err := NewServer(&config.Config{DataDir: dataDir, Storage: config.StorageConfig{Type: "invalid"}}, lease)
		require.Error(t, err)
		assert.True(t, lease.Closed())
		assert.NoFileExists(t, filepath.Join(dataDir, "state.json"))
	})
	t.Run("listener failure releases the lease", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		dataDir := t.TempDir()
		lease := testHubLease(t, dataDir)
		_, err = NewServer(&config.Config{DataDir: dataDir, Listen: []string{listener.Addr().String()}}, lease)
		require.Error(t, err)
		assert.True(t, lease.Closed())
		_, err = os.Stat(filepath.Join(dataDir, "state.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("database failure releases the lease and listeners", func(t *testing.T) {
		dataDir := t.TempDir()
		lease := testHubLease(t, dataDir)
		blocked := filepath.Join(dataDir, "hub.db")
		require.NoError(t, os.Mkdir(blocked, 0o700))
		listen := locallistentest.UniqueListenURL(t, "lmx-hub-failed-db")
		_, err := NewServer(&config.Config{DataDir: dataDir, Listen: []string{listen}}, lease)
		require.Error(t, err)
		assert.True(t, lease.Closed())
		assert.NoFileExists(t, filepath.Join(dataDir, "state.json"))
		listener, err := locallisten.Listen(listen)
		require.NoError(t, err, "constructor cleanup must release the bound local listener")
		require.NoError(t, listener.Close())
	})
}
