package tunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestOpenChannelPreservesWebSocketCloseReasonOnImmediatePolicyClose(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	hub := &pinFlowHub{
		key:        key,
		keys:       workerKeysOf(t, key),
		wsAccepted: make(chan struct{}),
	}
	mux := channelServiceMux(hub)
	mux.HandleFunc(contracts.WSRouteChannel, func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{contracts.WSSubprotocolChannelRelay},
		})
		if acceptErr != nil {
			return
		}
		_ = conn.Close(websocket.StatusPolicyViolation, contracts.CloseReasonTooManyConnections)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	opCtx := testutil.DeadlineContext(t)
	lifetimeCtx, cancelLifetime := context.WithCancel(t.Context())
	defer cancelLifetime()

	ch, err := OpenChannel(opCtx, server.URL, pinHubWorkerID, &OpenChannelOptions{
		HTTPClient:          server.Client(),
		WebSocketHTTPClient: server.Client(),
		LifetimeContext:     lifetimeCtx,
		BearerToken:         "test-token",
	})
	require.Error(t, err)
	assert.Nil(t, ch)

	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr, "OpenChannel must preserve the peer's WebSocket CloseError instead of returning context.Canceled")
	assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
	assert.Equal(t, contracts.CloseReasonTooManyConnections, closeErr.Reason)
}

func TestCallRPCPreservesWebSocketCloseReasonWhileInFlight(t *testing.T) {
	hub, hubURL := startPinFlowHub(t)

	opCtx := testutil.DeadlineContext(t)
	lifetimeCtx, cancelLifetime := context.WithCancel(t.Context())
	defer cancelLifetime()

	results := startOpenChannel(opCtx, lifetimeCtx, hubURL, nil)
	hub.awaitRelay(t)
	pingID, _ := hub.readRequest(t)
	hub.answerPing(t, pingID)

	var ch *Channel
	select {
	case res := <-results:
		require.NoError(t, res.err)
		ch = res.ch
	case <-time.After(eventWaitLimit):
		t.Fatal("OpenChannel did not finish after answering verification Ping")
	}
	defer ch.Close()

	// Start an in-flight RPC and close the server WebSocket with a policy reason
	// while the client is blocked in CallRequest.
	rpcDone := make(chan error, 1)
	go func() {
		_, err := ch.CallRPC(opCtx, leapmuxv1connect.WorkerSystemServicePingProcedure, nil)
		rpcDone <- err
	}()

	// Wait until the client's RPC frame reaches the relay so we know CallRequest
	// is blocked waiting on respCh / ch.ctx.Done().
	_, _ = hub.readRequest(t)

	hub.mu.Lock()
	peerWS := hub.peerWS
	hub.mu.Unlock()
	require.NoError(t, peerWS.Close(websocket.StatusPolicyViolation, contracts.CloseReasonTooManyConnections))

	select {
	case err := <-rpcDone:
		require.Error(t, err)
		var closeErr websocket.CloseError
		require.ErrorAs(t, err, &closeErr)
		assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
		assert.Equal(t, contracts.CloseReasonTooManyConnections, closeErr.Reason)
		assert.False(t, errors.Is(err, context.Canceled), "transport close error should replace bare context.Canceled")
	case <-time.After(eventWaitLimit):
		t.Fatal("CallRPC did not wake up after peer closed WebSocket")
	}
}

func openVerifiedPinChannel(t *testing.T) (*pinFlowHub, *Channel) {
	t.Helper()
	hub, hubURL := startPinFlowHub(t)

	opCtx := testutil.DeadlineContext(t)
	lifetimeCtx, cancelLifetime := context.WithCancel(t.Context())
	t.Cleanup(cancelLifetime)

	results := startOpenChannel(opCtx, lifetimeCtx, hubURL, nil)
	hub.awaitRelay(t)
	pingID, _ := hub.readRequest(t)
	hub.answerPing(t, pingID)

	got := awaitOpen(t, results)
	require.NoError(t, got.err)
	require.NotNil(t, got.ch)
	t.Cleanup(got.ch.Close)
	return hub, got.ch
}

func assertPolicyCloseError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
	assert.Equal(t, contracts.CloseReasonTooManyConnections, closeErr.Reason)
	assert.False(t, errors.Is(err, context.Canceled), "transport close error must not collapse to context.Canceled")
}

func TestChannelPostCloseSendAndStreamMethodsPreserveWebSocketCloseError(t *testing.T) {
	hub, ch := openVerifiedPinChannel(t)
	opCtx := testutil.DeadlineContext(t)

	hub.mu.Lock()
	peerWS := hub.peerWS
	hub.mu.Unlock()
	require.NoError(t, peerWS.Close(websocket.StatusPolicyViolation, contracts.CloseReasonTooManyConnections))
	awaitSignal(t, ch.CloseDone(), "channel close completion")

	_, err := ch.SendRequestNoWait(opCtx, &leapmuxv1.InnerRpcRequest{
		Method: leapmuxv1connect.WorkerSystemServicePingProcedure,
	}, RPCHandlers{})
	assertPolicyCloseError(t, err)

	err = ch.SendStreamRequest(opCtx, 1, []byte("stream-payload"))
	assertPolicyCloseError(t, err)

	err = ch.CancelStream(opCtx, 1)
	assertPolicyCloseError(t, err)
}

func TestChannelLocalCloseKeepsNilTransportCloseErrAndReturnsContextCanceled(t *testing.T) {
	_, ch := openVerifiedPinChannel(t)
	opCtx := testutil.DeadlineContext(t)

	ch.Close()
	awaitSignal(t, ch.CloseDone(), "channel close completion")

	assert.NoError(t, ch.transportCloseErr(), "local Close must not record a peer transport close error")

	for name, call := range map[string]func() error{
		"CallRPC": func() error {
			_, err := ch.CallRPC(opCtx, leapmuxv1connect.WorkerSystemServicePingProcedure, nil)
			return err
		},
		"SendRequestNoWait": func() error {
			_, err := ch.SendRequestNoWait(opCtx, &leapmuxv1.InnerRpcRequest{
				Method: leapmuxv1connect.WorkerSystemServicePingProcedure,
			}, RPCHandlers{})
			return err
		},
		"SendStreamRequest": func() error {
			return ch.SendStreamRequest(opCtx, 1, []byte("payload"))
		},
		"CancelStream": func() error {
			return ch.CancelStream(opCtx, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.ErrorIs(t, err, context.Canceled)
			var closeErr websocket.CloseError
			assert.False(t, errors.As(err, &closeErr), "local Close must not report a WebSocket CloseError")
		})
	}
}

func TestSendInnerRaw_WriteFailureWithRecordedTransportCloseErr(t *testing.T) {
	policyErr := websocket.CloseError{
		Code:   websocket.StatusPolicyViolation,
		Reason: contracts.CloseReasonTooManyConnections,
	}
	msg := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{
			Request: &leapmuxv1.InnerRpcRequest{
				Method: leapmuxv1connect.WorkerSystemServicePingProcedure,
			},
		},
	}

	t.Run("live caller ctx wraps transportCloseErr on write failure", func(t *testing.T) {
		opCtx := testutil.DeadlineContext(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			_ = conn.CloseNow()
		}))
		t.Cleanup(srv.Close)

		wsConn, _, err := websocket.Dial(opCtx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{
			HTTPClient: srv.Client(),
		})
		require.NoError(t, err)
		require.NoError(t, wsConn.CloseNow())

		key, err := noiseutil.GenerateCompositeKeypair()
		require.NoError(t, err)
		keys := workerKeysOf(t, key)
		initHS, msg1, err := noiseutil.ClassicalInitiatorHandshake1("worker-write-fail", keys.GetX25519(), keys.GetMlkem(), keys.GetSlhdsa())
		require.NoError(t, err)
		defer initHS.Clear()
		msg2, respSession, err := noiseutil.ClassicalResponderHandshake(key, "worker-write-fail", msg1)
		require.NoError(t, err)
		defer respSession.Zero()
		initSession, err := initHS.Finish(msg2)
		require.NoError(t, err)
		defer initSession.Zero()

		chCtx, chCancel := context.WithCancel(t.Context())
		defer chCancel()
		ch := &Channel{
			channelID:      "ch-write-fail",
			session:        initSession,
			ws:             wsConn,
			ctx:            chCtx,
			cancel:         chCancel,
			maxReassembled: contracts.MaxMessageSize,
		}
		ch.recordCloseErr(policyErr)

		err = ch.sendInnerRaw(opCtx, 1, msg)
		assertPolicyCloseError(t, err)
		assert.ErrorIs(t, ch.Context().Err(), context.Canceled, "a transport write failure must cancel the channel")
	})

	t.Run("canceled caller ctx preserves context.Canceled despite recorded transportCloseErr", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			chCtx, chCancel := context.WithCancel(context.Background())
			defer chCancel()
			ch := &Channel{
				channelID:      "ch-caller-cancel",
				ctx:            chCtx,
				cancel:         chCancel,
				maxReassembled: contracts.MaxMessageSize,
			}
			ch.recordCloseErr(policyErr)

			held := make(chan struct{})
			release := make(chan struct{})
			holdDone := make(chan error, 1)
			go func() {
				holdDone <- ch.sendGate.WithFrame(context.Background(), ch.ctx, func() error {
					close(held)
					<-release
					return nil
				})
			}()
			<-held

			callerCtx, cancelCaller := context.WithCancel(context.Background())
			sendDone := make(chan error, 1)
			go func() {
				sendDone <- ch.sendInnerRaw(callerCtx, 1, msg)
			}()
			synctest.Wait()
			require.Equal(t, 1, ch.sendGate.FrameWaiters(), "sendInnerRaw must block on the send gate")

			cancelCaller()
			err := <-sendDone
			close(release)
			require.NoError(t, <-holdDone)

			require.ErrorIs(t, err, context.Canceled)
			var closeErr websocket.CloseError
			assert.False(t, errors.As(err, &closeErr), "a canceled caller ctx must not be replaced by transportCloseErr")
			assert.NoError(t, ch.Context().Err(), "a caller abort before the first frame must not cancel the channel")
		})
	})
}

func TestOpenChannelPreHandoutPolicyCloseAfterPinPublicationPreservesCloseError(t *testing.T) {
	hub, hubURL := startPinFlowHub(t)
	pin := &recordingKeyPin{
		publishedNow: make(chan struct{}),
		afterWrite:   make(chan struct{}),
	}
	results := startOpenChannel(testutil.DeadlineContext(t), testutil.DeadlineContext(t), hubURL, pin)

	hub.awaitRelay(t)
	correlationID, request := hub.readRequest(t)
	require.Equal(t, leapmuxv1connect.WorkerSystemServicePingProcedure, request.GetMethod())
	hub.answerPing(t, correlationID)
	awaitSignal(t, pin.publishedNow, "the pin publication")

	hub.mu.Lock()
	peerWS := hub.peerWS
	hub.mu.Unlock()
	require.NoError(t, peerWS.Close(websocket.StatusPolicyViolation, contracts.CloseReasonTooManyConnections))

	got := awaitOpen(t, results)
	assert.Nil(t, got.ch)
	assert.ErrorContains(t, got.err, "channel closed before handout")
	assertPolicyCloseError(t, got.err)
	assert.True(t, pin.wasPublished(), "a published pin must remain after a pre-handout close")
	assert.Equal(t, pinHubChannelID, hub.closedChannelID(), "the failed open must roll the registration back")
}
