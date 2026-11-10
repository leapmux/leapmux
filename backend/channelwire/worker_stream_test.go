package channelwire

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func protoFramePair(t *testing.T, handle func(context.Context, *websocket.Conn)) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		handle(r.Context(), conn)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.Dial(t.Context(), HTTPToWS(srv.URL), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func TestWriteProtoFrameRoundTripsThroughReadProtoFrame(t *testing.T) {
	t.Parallel()
	msg := &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: &leapmuxv1.WorkerHello{}}}
	conn := protoFramePair(t, func(ctx context.Context, ws *websocket.Conn) {
		var got leapmuxv1.ConnectRequest
		if !assert.NoError(t, ReadProtoFrame(ctx, ws, &got)) {
			return
		}
		assert.True(t, proto.Equal(msg, &got))
		assert.NoError(t, WriteProtoFrame(ctx, ws, &got))
	})
	require.NoError(t, WriteProtoFrame(t.Context(), conn, msg))
	kind, data, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, kind)
	require.GreaterOrEqual(t, len(data), 4)
	assert.Equal(t, uint32(len(data)-4), binary.BigEndian.Uint32(data[:4]))
	var got leapmuxv1.ConnectRequest
	require.NoError(t, proto.Unmarshal(data[4:], &got))
	assert.True(t, proto.Equal(msg, &got))
}

func TestReadProtoFrameReportsAMalformedFrame(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{"short": {0, 0, 0}, "length mismatch": {0, 0, 0, 10, 1, 2}, "protobuf": {0, 0, 0, 2, 255, 255}} {
		t.Run(name, func(t *testing.T) {
			conn := protoFramePair(t, func(ctx context.Context, ws *websocket.Conn) {
				assert.NoError(t, ws.Write(ctx, websocket.MessageBinary, data))
				_, _, _ = ws.Read(ctx)
			})
			var msg leapmuxv1.ConnectRequest
			require.ErrorIs(t, ReadProtoFrame(t.Context(), conn, &msg), ErrMalformedFrame)
		})
	}
}

func TestReadProtoFrameAcceptsAnEmptyPayload(t *testing.T) {
	t.Parallel()
	conn := protoFramePair(t, func(ctx context.Context, ws *websocket.Conn) {
		assert.NoError(t, ws.Write(ctx, websocket.MessageBinary, make([]byte, 4)))
		_, _, _ = ws.Read(ctx)
	})
	var msg leapmuxv1.ConnectRequest
	require.NoError(t, ReadProtoFrame(t.Context(), conn, &msg))
	assert.True(t, proto.Equal(&leapmuxv1.ConnectRequest{}, &msg))
}

func TestReadProtoFrameReturnsACloseUnwrapped(t *testing.T) {
	t.Parallel()
	conn := protoFramePair(t, func(_ context.Context, ws *websocket.Conn) { _ = ws.Close(websocket.StatusPolicyViolation, "x") })
	var msg leapmuxv1.ConnectRequest
	err := ReadProtoFrame(t.Context(), conn, &msg)
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
	assert.Equal(t, "x", closeErr.Reason)
	assert.NotErrorIs(t, err, ErrMalformedFrame)
}

func TestWorkerStreamConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "/ws/worker", WSRouteWorker)
	assert.Equal(t, "worker-stream", WSSubprotocolWorkerStream)
	assert.EqualValues(t, (4<<20)+4, WorkerStreamReadLimit)
	reasons := []string{WorkerCloseReasonDeleted, WorkerCloseReasonKeysDiffer, WorkerCloseReasonInvalidMessage, WorkerCloseReasonHelloTimeout, WorkerCloseReasonTooManyWorkers, WorkerCloseReasonHubShuttingDown, WorkerCloseReasonHubInternalError}
	seen := map[string]bool{}
	for _, reason := range []string{contracts.CloseReasonControlFlood, contracts.CloseReasonTooManyConnections, contracts.CloseReasonSnapshotTooLarge, contracts.CloseReasonForbidden} {
		seen[reason] = true
	}
	for _, reason := range reasons {
		assert.LessOrEqual(t, len(reason), 123)
		assert.False(t, seen[reason], reason)
		seen[reason] = true
	}
}

func TestWorkerStreamURL(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{"http://h:1": "ws://h:1/ws/worker", "https://h": "wss://h/ws/worker", "http://localhost": "ws://localhost/ws/worker"} {
		assert.Equal(t, want, WorkerStreamURL(input))
	}
}
