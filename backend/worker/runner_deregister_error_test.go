package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/lifecycle"
)

func startCallbackErrorHub(t *testing.T, notification bool) string {
	t.Helper()
	return fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		if !notification {
			_ = stream.Refuse(websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted)
			return
		}
		if _, err := stream.Receive(ctx); err != nil {
			return
		}
		if err := stream.Greet(ctx, "owner"); err != nil {
			return
		}
		if err := stream.Send(ctx, &leapmuxv1.ConnectResponse{RequestId: "delete", Payload: &leapmuxv1.ConnectResponse_Deregister{Deregister: &leapmuxv1.DeregisterNotification{}}}); err != nil {
			return
		}
		_ = stream.ReadUntilClosed(ctx)
	})
}

func TestDeregisterCallbackErrorsSurviveCancellationAndCleanup(t *testing.T) {
	for _, notification := range []bool{false, true} {
		for _, cancelDuring := range []bool{false, true} {
			t.Run(fmt.Sprintf("notification=%v/cancel=%v", notification, cancelDuring), func(t *testing.T) {
				hubURL := startCallbackErrorHub(t, notification)
				root, err := lifecycle.CanonicalDir(t.TempDir())
				require.NoError(t, err)
				owner := lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, root)
				lease, err := lifecycle.AcquireDirectory(owner, lifecycle.RoleWorker, root)
				require.NoError(t, err)
				t.Cleanup(func() { _ = lease.Close() })
				key, err := noiseutil.GenerateCompositeKeypair()
				require.NoError(t, err)
				guard, stopWaiting := context.WithTimeout(t.Context(), 30*time.Second)
				defer stopWaiting()
				ctx, cancel := context.WithCancel(guard)
				entered, release, drained, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				var released sync.Once
				var databaseClosed atomic.Bool
				callbackFailure, databaseFailure, directoryFailure := errors.New("callback failed"), errors.New("database close failed"), errors.New("directory close failed")
				var result error
				go func() {
					defer close(done)
					result = run(ctx, RunConfig{
						HubURL: hubURL, Directory: lease, CompositeKey: key, AuthToken: "token", WorkerID: "worker", EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
						OnDeregister: func() error { close(entered); <-release; return callbackFailure }, Drained: func() { close(drained) },
					}, runCleanup{
						closeDatabase: func(database *sql.DB) error {
							err := database.Close()
							databaseClosed.Store(true)
							return errors.Join(err, databaseFailure)
						},
						closeDirectory: func(directory *lifecycle.DirectoryLease) error {
							return errors.Join(directory.Close(), directoryFailure)
						},
					})
				}()
				t.Cleanup(func() { cancel(); released.Do(func() { close(release) }); <-done })
				select {
				case <-entered:
				case <-done:
					t.Fatalf("worker ended before callback: %v", result)
				case <-guard.Done():
					t.Fatal("callback did not begin")
				}
				if cancelDuring {
					cancel()
					select {
					case <-drained:
					case <-guard.Done():
						t.Fatal("cancellation did not drain")
					}
				}
				assert.False(t, databaseClosed.Load(), "storage must remain open while the callback is held")
				held, err := lifecycle.ProbeLockAt(lifecycle.PathsOf(root).DataLock)
				require.NoError(t, err)
				assert.True(t, held)
				released.Do(func() { close(release) })
				select {
				case <-done:
				case <-guard.Done():
					t.Fatal("worker did not finish callback and cleanup")
				}
				assert.ErrorIs(t, result, callbackFailure)
				assert.ErrorIs(t, result, databaseFailure)
				assert.ErrorIs(t, result, directoryFailure)
				assert.True(t, databaseClosed.Load())
				replacement, err := lifecycle.AcquireDirectory(owner, lifecycle.RoleWorker, root)
				require.NoError(t, err)
				require.NoError(t, replacement.Close())
			})
		}
	}
}
