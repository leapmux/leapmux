package hub

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
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
)

func TestDialWorkerStream_SendsTheBearerAndTheSubprotocol(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		token         string
		authorization []string
	}{{"bearer", "worker-token", []string{"Bearer worker-token"}}, {"empty token", "", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := make(chan *http.Request, 1)
			url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
				request <- stream.Request()
				_ = stream.ReadUntilClosed(ctx)
			})
			stream, err := DialWorkerStream(t.Context(), http.DefaultClient, url, tc.token)
			require.NoError(t, err)
			defer func() { _ = stream.CloseNow() }()
			req := <-request
			assert.Equal(t, tc.authorization, req.Header.Values("Authorization"))
			assert.Equal(t, channelwire.WSSubprotocolWorkerStream, req.Header.Get("Sec-WebSocket-Protocol"))
			assert.Empty(t, req.Header.Get("Origin"))
			assert.Empty(t, req.Header.Get("Sec-WebSocket-Extensions"))
			assert.Equal(t, 1, req.ProtoMajor)
		})
	}
}

func TestDialWorkerStream_ReportsARefusedUpgrade(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 403, 426, 503, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			url := fakehubtest.StartRefusingUpgrade(t, status, "refusal detail\nsecond line")
			_, err := DialWorkerStream(t.Context(), http.DefaultClient, url, "token")
			var refused *UpgradeRefusedError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, status, refused.StatusCode)
			assert.ErrorContains(t, err, "refusal detail")
			assert.NotContains(t, err.Error(), "second line")
			assert.NotNil(t, errors.Unwrap(refused))
		})
	}
}

func TestDialWorkerStreamLimitsTheHandshakeTime(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	_, err := dialWorkerStream(t.Context(), srv.Client(), srv.URL, "token", 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWorkerStreamAppliesTheReadLimit(t *testing.T) {
	t.Parallel()
	target := channelwire.WorkerStreamMaxMessageBytes + 1
	message := &leapmuxv1.ChannelMessage{Ciphertext: make([]byte, target-32)}
	response := &leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_ChannelMessage{ChannelMessage: message}}
	message.Ciphertext = append(message.Ciphertext, make([]byte, target-proto.Size(response))...)
	require.Equal(t, target, proto.Size(response))
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		_ = stream.Send(ctx, response)
		_ = stream.ReadUntilClosed(ctx)
	})
	ctx := testutil.DeadlineContext(t)
	stream, err := DialWorkerStream(ctx, http.DefaultClient, url, "token")
	require.NoError(t, err)
	defer func() { _ = stream.CloseNow() }()
	_, err = stream.Receive(ctx)
	require.ErrorIs(t, err, websocket.ErrMessageTooBig)
}

func TestWorkerStreamSignalsAPingFromTheHub(t *testing.T) {
	t.Parallel()
	pingResult := make(chan error, 1)
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		readDone := make(chan error, 1)
		go func() { readDone <- stream.ReadUntilClosed(ctx) }()
		pingResult <- stream.Ping(ctx)
		assert.Error(t, <-readDone)
	})
	ctx := testutil.DeadlineContext(t)
	stream, err := DialWorkerStream(ctx, http.DefaultClient, url, "token")
	require.NoError(t, err)
	defer func() { _ = stream.CloseNow() }()
	readDone := make(chan error, 1)
	go func() { _, err := stream.Receive(ctx); readDone <- err }()
	select {
	case <-stream.PingReceived():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-pingResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = stream.CloseNow()
	assert.Error(t, <-readDone)
}

func TestUpgradeRefusedError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"a first line", 401, "unauthorized\nsecond line", "the hub refused the worker stream: HTTP 401 Unauthorized: unauthorized"},
		{"an empty body", 401, "", "the hub refused the worker stream: HTTP 401 Unauthorized"},
		{"a blank first line", 401, "\nsecond line", "the hub refused the worker stream: HTTP 401 Unauthorized"},
		{"a whitespace first line", 403, " \r\nsecond line", "the hub refused the worker stream: HTTP 403 Forbidden"},
		{"a padded first line", 403, " denied \r\nsecond line", "the hub refused the worker stream: HTTP 403 Forbidden: denied"},
		{"an unknown status", 599, "", "the hub refused the worker stream: HTTP 599"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("upgrade failed")
			err := &UpgradeRefusedError{StatusCode: tc.status, Body: tc.body, Err: cause}
			assert.EqualError(t, err, tc.want)
			assert.ErrorIs(t, err, cause)
		})
	}
}

func TestWorkerStream_AutomaticPongFailsDuringALongDataWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := testutil.NewDelayedWebSocketTransport(26 * time.Second)
		stream, err := DialWorkerStream(t.Context(), &http.Client{Transport: body}, "http://probe", "token")
		require.NoError(t, err)
		defer func() { _ = stream.CloseNow() }()
		start := time.Now()
		written := make(chan error, 1)
		go func() {
			written <- stream.Send(context.Background(), &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{Complete: true}}})
		}()
		<-body.WriteStarted
		read := make(chan error, 1)
		go func() { _, err := stream.Receive(context.Background()); read <- err }()
		go func() {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			<-timer.C
			body.Incoming <- []byte{0x89, 0}
		}()
		<-stream.PingReceived()
		require.Error(t, <-read)
		assert.Equal(t, 15*time.Second, time.Since(start), "the automatic pong has five seconds to acquire the frame mutex")
		_ = stream.CloseNow()
		require.Error(t, <-written)
	})
}
