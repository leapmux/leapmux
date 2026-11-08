package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/lifecycle"
)

type callbackErrorHub struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler
	notification bool
}

func (h callbackErrorHub) Connect(ctx context.Context, stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
	if !h.notification {
		detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED})
		if err != nil {
			return err
		}
		return connect.NewError(connect.CodeUnauthenticated, "worker deleted").WithDetail(detail)
	}
	if err := stream.Send(&leapmuxv1.ConnectResponse{RequestId: "delete", Payload: &leapmuxv1.ConnectResponse_Deregister{Deregister: &leapmuxv1.DeregisterNotification{}}}); err != nil {
		return err
	}
	for {
		if _, err := stream.Receive(); err != nil {
			return ctx.Err()
		}
	}
}

func TestDeregisterCallbackErrorsSurviveCancellationAndCleanup(t *testing.T) {
	for _, notification := range []bool{false, true} {
		for _, cancelDuring := range []bool{false, true} {
			t.Run(fmt.Sprintf("notification=%v/cancel=%v", notification, cancelDuring), func(t *testing.T) {
				mux := http.NewServeMux()
				server := connect.NewServer()
				leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, callbackErrorHub{notification: notification})
				connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
				hub := httptest.NewUnstartedServer(mux)
				hub.Config.Protocols = &http.Protocols{}
				hub.Config.Protocols.SetHTTP1(true)
				hub.Config.Protocols.SetUnencryptedHTTP2(true)
				hub.Start()
				t.Cleanup(hub.Close)
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
						HubURL: hub.URL, Directory: lease, CompositeKey: key, AuthToken: "token", WorkerID: "worker", EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
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
