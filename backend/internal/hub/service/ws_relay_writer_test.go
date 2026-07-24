package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
)

func relayTestFrame(correlationID uint64) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      []byte("payload"),
	}
}

func relayFrameOfSize(correlationID uint64, n int) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      make([]byte, n),
	}
}

// TestRelayWriter_ConfigMatchesSendqContract pins the hub-specific
// constants the thin wrapper hands to sendq. Generic queue behaviour
// lives in sendq_test.go; this is the wiring that must not drift.
func TestRelayWriter_ConfigMatchesSendqContract(t *testing.T) {
	assert.Equal(t, 32*1024*1024, relayMaxQueueBytes)
	assert.Equal(t, 256, relayFrameOverhead)
	assert.Equal(t, 10*time.Second, relayWriteTimeout)
	assert.Equal(t, 30*time.Second, relayMaxStall)
}

// TestRelayWriter_EnqueueDoesNotBlockOnTheSocket is the property the
// whole type exists for: the hub's per-worker read loop calls this
// inline, so it must return without touching the network. A peer that
// never reads makes the drain goroutine park on Write; enqueue must
// still return promptly.
func TestRelayWriter_EnqueueDoesNotBlockOnTheSocket(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage)
	// received is unbuffered and nobody reads it -- but the client's
	// read loop is what matters for backpressure. Use a pair and stop
	// the client from draining by closing received and not consuming.
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	// Leave client open but stop its reader from freeing the receive
	// window: close received so the reader goroutine blocks on send,
	// which eventually fills the socket. Enqueue itself must not wait.
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if err := writer.enqueue(relayTestFrame(uint64(i))); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked on the socket")
	}
}

// TestRelayWriter_DisconnectsAClientOverTheByteBudget pins that the
// hub wrapper's OnGiveUp cancels the connection when sendq blows the
// budget -- the hub-specific teardown path, not the generic accounting.
func TestRelayWriter_DisconnectsAClientOverTheByteBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")

	const chunk = 1 << 20 // 1 MiB
	var err error
	for i := 0; i < (relayMaxQueueBytes/chunk)+2; i++ {
		if err = w.enqueue(relayFrameOfSize(uint64(i), chunk)); err != nil {
			break
		}
	}

	require.ErrorIs(t, err, errRelayWriterClosed, "the budget must eventually reject")
	assert.True(t, w.inner.IsClosed())
	assert.Zero(t, w.inner.QueuedLen(), "the backlog is released once the budget blows")
	assert.Zero(t, w.inner.QueuedBytes())

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("blowing the byte budget must tear the connection down")
	}
}

// TestRelayWriter_EnqueueAfterCloseReports pins that a caller handing
// frames to a torn-down connection learns about it via the hub's closed
// sentinel, so channelmgr stops routing to a dead sender.
func TestRelayWriter_EnqueueAfterCloseReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	require.NoError(t, w.enqueue(relayTestFrame(1)))
	w.close()

	assert.ErrorIs(t, w.enqueue(relayTestFrame(2)), errRelayWriterClosed)
	assert.Zero(t, w.inner.QueuedLen(), "close discards the backlog rather than pinning it")
}

// TestRelayWriter_MapsSendqClosedToHubSentinel pins that callers see the
// hub-local sentinel, not sendq.ErrClosed -- channelmgr and BindUser
// match on errRelayWriterClosed.
func TestRelayWriter_MapsSendqClosedToHubSentinel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	w.close()
	err := w.enqueue(relayTestFrame(1))
	require.ErrorIs(t, err, errRelayWriterClosed)
	assert.False(t, errors.Is(err, sendq.ErrClosed),
		"enqueue must map sendq.ErrClosed to errRelayWriterClosed, not wrap it")
}

// TestRelayWriter_PreservesFrameOrder pins that the hub wrapper still
// delivers an ordered stream through a live websocket -- the end-to-end
// wiring sendq alone cannot assert.
func TestRelayWriter_PreservesFrameOrder(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 64)
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	const frames = 32
	for i := 0; i < frames; i++ {
		require.NoError(t, writer.enqueue(relayTestFrame(uint64(i))))
	}

	for i := 0; i < frames; i++ {
		select {
		case got := <-received:
			require.Equal(t, uint64(i), got.GetCorrelationId(), "frames must arrive in enqueue order")
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d frames arrived", i, frames)
		}
	}
}

// newRelayWriterPair stands up a real websocket pair and returns a
// relayWriter bound to the server side. Every message the server writes
// is decoded onto received.
func newRelayWriterPair(t *testing.T, received chan *leapmuxv1.ChannelMessage) (
	*httptest.Server, *websocket.Conn, *relayWriter, context.CancelFunc,
) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	var (
		mu       sync.Mutex
		srvConn  *websocket.Conn
		accepted = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		srvConn = c
		mu.Unlock()
		close(accepted)
		<-ctx.Done()
	}))

	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	client.SetReadLimit(channelwire.WSReadLimit)

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept the websocket")
	}

	go func() {
		for {
			msg, err := channelwire.ReadChannelMessage(ctx, client)
			if err != nil {
				return
			}
			select {
			case received <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	mu.Lock()
	conn := srvConn
	mu.Unlock()
	return srv, client, newRelayWriter(ctx, conn, cancel, "user-1", "conn-1"), cancel
}
