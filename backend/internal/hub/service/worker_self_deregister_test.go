package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// deregisterSelfCall performs one DeregisterSelf with the worker token.
func (e *regKeyEnv) deregisterSelfCall(t *testing.T, ctx context.Context, token string) error {
	t.Helper()
	requestCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+token)
	_, err := e.connectorClient.DeregisterSelf(requestCtx, &leapmuxv1.DeregisterSelfRequest{})
	return err
}

// registerSelfDeregisterableWorker registers one worker through the real
// service and returns its credentials.
func registerSelfDeregisterableWorker(t *testing.T, env *regKeyEnv, ctx context.Context) (key, token string) {
	t.Helper()
	admin := userid.MustNew(env.adminID(t))
	key = id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
	}))
	resp, err := env.registerWithKey(t, key)
	require.NoError(t, err)
	return key, resp.GetAuthToken()
}

func TestSelfDeregisterRepairsEffectsAfterCommittedDeletion(t *testing.T) {
	env := setupRegKeyEnv(t)
	ctx := context.Background()
	_, token := registerSelfDeregisterableWorker(t, env, ctx)
	worker, err := env.store.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
	require.NoError(t, err)
	owner := userid.MustNew(worker.RegisteredBy)
	// Commit the same deletion as the handler, without running effects. This
	// represents a Hub exit between the commit and the first effect.
	require.NoError(t, env.store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		return tx.Workers().MarkDeleted(ctx, worker.ID)
	}))
	notifications := make(chan string, 2)
	env.connector.SetDeregisterEffects(service.NewWorkerDeregisterEffects(nil, nil, func(userID string) {
		notifications <- userID
	}))
	for range 2 {
		require.NoError(t, env.deregisterSelfCall(t, ctx, token))
		select {
		case userID := <-notifications:
			assert.Equal(t, worker.RegisteredBy, userID)
		default:
			t.Fatal("a successful deletion retry must complete the post-commit effects")
		}
	}
	_, err = auth.AuthenticateWorkerBearer(ctx, env.store, "Bearer "+token)
	require.ErrorIs(t, err, auth.ErrWorkerDeleted)
}

func TestSelfDeregisterFencesDelayedAdmission(t *testing.T) {
	ctx, cancelTest := context.WithCancel(testutil.DeadlineContext(t))
	defer cancelTest()
	read := make(chan struct{})
	release := make(chan struct{})
	var paused atomic.Bool
	env := newRegKeyEnv(t, testConfig(), nil, func(st store.Store) store.Store {
		return authReadHookStore{Store: st, afterAuthRead: func(*store.Worker) {
			if paused.CompareAndSwap(false, true) {
				close(read)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		}}
	})
	_, token := registerSelfDeregisterableWorker(t, env, ctx)
	worker, err := env.store.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
	require.NoError(t, err)
	env.connector.SetDeregisterEffects(service.NewWorkerDeregisterEffects(nil, env.notifier, env.registry.BroadcastWorkersChanged))
	server := httptest.NewUnstartedServer(env.mux)
	server.Config.Protocols = &http.Protocols{}
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	t.Cleanup(server.Close)
	endpoint, err := hubtransport.New(server.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	client := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.StreamClient(), server.URL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	streamCtx, info := connect.NewClientContext(streamCtx)
	info.RequestHeader().Set("Authorization", "Bearer "+token)
	stream, err := client.Connect(streamCtx)
	require.NoError(t, err)
	defer func() { _ = stream.CloseSend() }()
	require.NoError(t, stream.Send(helloRequest(postQuantumHello(0x31))))
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("Connect did not reach its credential lookup")
	}
	assert.False(t, env.wMgr.OnlineForTrustedPath(worker.ID))
	require.NoError(t, env.deregisterSelfCall(t, ctx, token))
	deleted, err := env.store.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
	close(release)
	for {
		frame, err := stream.Receive()
		require.NoError(t, err, "the delayed connection must receive its deregistration before closing")
		if frame.GetDeregister() != nil {
			require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
				RequestId: frame.GetRequestId(), Payload: &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
			}))
			break
		}
	}
	testutil.AssertEventually(t, func() bool {
		return !env.wMgr.OnlineForTrustedPath(worker.ID) && !env.wMgr.IsDeregistering(worker.ID)
	})
	_, err = auth.AuthenticateWorkerBearer(ctx, env.store, "Bearer "+token)
	require.ErrorIs(t, err, auth.ErrWorkerDeleted)
}

// TestSelfDeregisterRejectsBundledWorkers refuses the removal for both
// an online and an offline auto-registered row: those belong to the
// solo launcher's lifecycle, not to the worker process.
func TestSelfDeregisterRejectsBundledWorkers(t *testing.T) {
	env := setupRegKeyEnv(t)
	ctx := context.Background()
	admin := userid.MustNew(env.adminID(t))

	// A bundled row, the way the solo launcher's in-process bypass
	// creates it.
	keys := workerKeysForBundled(t)
	require.NoError(t, env.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:             "bundled-worker-1",
		AuthToken:      "bundled-token",
		RegisteredBy:   admin,
		AutoRegistered: true,
		PublicKeys:     keys,
	}))

	// ONLINE first: a registered connection makes the row the running
	// bundled desktop worker, the case the refusal protects most.
	conn := workermgrtest.NewConnWithHello(t, "bundled-worker-1",
		&leapmuxv1.WorkerHello{Keys: keys.Proto(), EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM},
		func(*leapmuxv1.ConnectResponse) error { return nil })
	_, err := env.wMgr.Register(conn)
	require.NoError(t, err)
	t.Cleanup(conn.Fence)

	err = env.deregisterSelfCall(t, ctx, "bundled-token")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"an online bundled worker is the running desktop worker, not a self-service row")

	// OFFLINE too: the handler refuses the row itself, online or not.
	env.wMgr.Unregister("bundled-worker-1", conn)
	err = env.deregisterSelfCall(t, ctx, "bundled-token")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"an offline bundled worker stays exactly as its launcher left it")
	worker, rowErr := env.store.Workers().GetByID(ctx, "bundled-worker-1")
	require.NoError(t, rowErr)
	assert.Nil(t, worker.DeletedAt, "the row is untouched")
}

// workerKeysForBundled builds one valid key set for a store row a test
// creates directly.
func workerKeysForBundled(t *testing.T) store.WorkerPublicKeys {
	t.Helper()
	attempt := newRegisterAttempt(t, 0x77)
	return store.WorkerPublicKeysFromProto(attempt.keys)
}

// TestSelfDeregisterIsIdentityScopedAndRepeatable pins the identity
// rules: an invalid token cannot select any identity, a valid token
// deletes only its own worker, and a repeated call for the same retained
// deleted row succeeds.
func TestSelfDeregisterIsIdentityScopedAndRepeatable(t *testing.T) {
	env := setupRegKeyEnv(t)
	ctx := context.Background()

	_, token := registerSelfDeregisterableWorker(t, env, ctx)
	_, otherToken := registerSelfDeregisterableWorker(t, env, ctx)

	t.Run("an invalid token is refused", func(t *testing.T) {
		err := env.deregisterSelfCall(t, ctx, "not-a-token")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("a valid token deletes only its own worker", func(t *testing.T) {
		require.NoError(t, env.deregisterSelfCall(t, ctx, token))

		deleted, err := env.store.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
		require.NoError(t, err)
		assert.NotNil(t, deleted.DeletedAt, "the caller's worker is deleted")

		untouched, err := env.store.Workers().GetByAuthTokenIncludeDeleted(ctx, otherToken)
		require.NoError(t, err)
		assert.Nil(t, untouched.DeletedAt, "another worker's row stays")
	})

	t.Run("a repeated call for the retained deleted row succeeds", func(t *testing.T) {
		require.NoError(t, env.deregisterSelfCall(t, ctx, token),
			"the reply may have been lost; the end state is what the caller needs")
	})

	t.Run("the retained deleted token still permits a self-deregistration retry", func(t *testing.T) {
		err := env.deregisterSelfCall(t, ctx, token)
		require.NoError(t, err, "the retained row keeps accepting the idempotent call")
	})
	t.Run("ordinary authentication refuses the deleted worker", func(t *testing.T) {
		_, err := auth.AuthenticateWorkerBearer(ctx, env.store, "Bearer "+token)
		require.ErrorIs(t, err, auth.ErrWorkerDeleted)
	})
}
