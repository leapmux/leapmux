package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// recordedFrame is one decrypted ChannelMessage observed by the peer recorder.
type recordedFrame struct {
	correlationID uint64
	flags         leapmuxv1.ChannelMessageFlags
	plaintext     []byte
}

// pairedSendChannel stands up a real classical Noise pair over a websocket,
// with a recorder that decrypts every frame in arrival order. Wire-order ==
// nonce-order is asserted by construction: one reorder makes Decrypt fail.
func pairedSendChannel(t *testing.T) (ch *Channel, frames <-chan recordedFrame) {
	t.Helper()

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := noiseutil.ClassicalInitiatorHandshake1(key.X25519Public)
	require.NoError(t, err)
	msg2, peerSession, err := noiseutil.ClassicalResponderHandshake(key.X25519Public, key.X25519Private, msg1)
	require.NoError(t, err)
	initiatorSession, err := noiseutil.ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	frameCh := make(chan recordedFrame, 256)
	var (
		mu      sync.Mutex
		srvConn *websocket.Conn
		ready   = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			return
		}
		mu.Lock()
		srvConn = c
		mu.Unlock()
		close(ready)
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	client.SetReadLimit(channelwire.WSReadLimit)
	t.Cleanup(func() { _ = client.CloseNow() })

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept the websocket")
	}

	mu.Lock()
	peer := srvConn
	mu.Unlock()
	require.NotNil(t, peer)
	peer.SetReadLimit(channelwire.WSReadLimit)

	go func() {
		for {
			msg, readErr := channelwire.ReadChannelMessage(ctx, peer)
			if readErr != nil {
				return
			}
			pt, decErr := peerSession.Decrypt(msg.GetCiphertext())
			if decErr != nil {
				// A reorder or encrypt-bug surfaces as a decrypt failure; surface
				// it as a zero-plaintext frame with the correlation id so the
				// test can fail loudly rather than hang.
				select {
				case frameCh <- recordedFrame{
					correlationID: msg.GetCorrelationId(),
					flags:         msg.GetFlags(),
				}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case frameCh <- recordedFrame{
				correlationID: msg.GetCorrelationId(),
				flags:         msg.GetFlags(),
				plaintext:     pt,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()

	ch = &Channel{
		channelID:  "send-test",
		userID:     "user-1",
		session:    initiatorSession,
		ws:         client,
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	return ch, frameCh
}

// Concurrent mixed-size senders must all decrypt, and a small frame must be
// able to land between a multi-chunk message's chunks (SendGate's reason to
// exist). Wire-order decryptability is the nonce-order assertion.
func TestChannelSendConcurrentMixedSizeInterleavesAndDecrypts(t *testing.T) {
	ch, frames := pairedSendChannel(t)

	bigPayload := make([]byte, channelwire.MaxPlaintextPerChunk+2048)
	for i := range bigPayload {
		bigPayload[i] = byte(i)
	}
	bigPlain := mustMarshalInner(t, "Big", bigPayload)
	smallPlain := mustMarshalInner(t, "Small", []byte("small"))

	holdFirst := make(chan struct{})
	started := make(chan struct{})
	var chunkN atomic.Int32
	type frameRec struct {
		id    uint64
		flags leapmuxv1.ChannelMessageFlags
	}
	var (
		mu   sync.Mutex
		recs []frameRec
	)
	writeChunk := func(id uint64) func([]byte, leapmuxv1.ChannelMessageFlags) error {
		return func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
			if id == 1 && chunkN.Add(1) == 1 {
				close(started)
				<-holdFirst
			}
			ct, err := ch.session.Encrypt(chunk)
			if err != nil {
				return err
			}
			mu.Lock()
			recs = append(recs, frameRec{id: id, flags: flags})
			mu.Unlock()
			return channelwire.WriteChannelMessage(ch.ctx, ch.ws,
				channelwire.NewChannelMessage(ch.channelID, id, flags, ct))
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, ch.sendGate.Send(context.Background(), ch.ctx, bigPlain, writeChunk(1)))
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("big send never started")
	}

	// Park small on the frame permit while big holds it; releasing holdFirst
	// lets small win the between-chunk acquire (synchronous Send would deadlock).
	smallDone := make(chan error, 1)
	go func() {
		smallDone <- ch.sendGate.Send(context.Background(), ch.ctx, smallPlain, writeChunk(2))
	}()
	time.Sleep(20 * time.Millisecond)
	close(holdFirst)

	select {
	case err := <-smallDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("small send did not complete")
	}
	wg.Wait()

	mu.Lock()
	got := append([]frameRec(nil), recs...)
	mu.Unlock()
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, uint64(1), got[0].id)
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, got[0].flags)
	assert.Equal(t, uint64(2), got[1].id, "small frame must land between big chunks")
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, got[1].flags)
	assert.Equal(t, uint64(1), got[2].id)

	for range got {
		select {
		case fr := <-frames:
			require.NotNil(t, fr.plaintext, "decrypt failed: wire order diverged from nonce order")
		case <-time.After(2 * time.Second):
			t.Fatal("recorder did not observe all frames")
		}
	}
}

func mustMarshalInner(t *testing.T, method string, payload []byte) []byte {
	t.Helper()
	pt, err := proto.Marshal(&leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{
			Request: &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload},
		},
	})
	require.NoError(t, err)
	return pt
}

// A caller context cancelled at entry must emit no frame and must NOT cancel
// the channel (ErrSendAborted is not a transport failure).
func TestChannelSendCancelledCtxEmitsNothingAndLeavesChannelOpen(t *testing.T) {
	ch, frames := pairedSendChannel(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ch.SendRPCNoWait(ctx, "Nope", []byte("x"), RPCHandlers{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, ch.Closed(), "an entry-ctx abort must not cancel the shared channel")

	select {
	case fr := <-frames:
		t.Fatalf("cancelled entry must emit no frame, got correlation=%d flags=%v", fr.correlationID, fr.flags)
	case <-time.After(100 * time.Millisecond):
	}
}

// An encrypt/write failure must cancel the channel so pooled callers re-open.
func TestChannelSendWriteFailureCancelsChannel(t *testing.T) {
	ch, _ := pairedSendChannel(t)

	// Tear down the peer socket so the next Write fails.
	require.NoError(t, ch.ws.CloseNow())

	_, err := ch.SendRPCNoWait(context.Background(), "Doomed", []byte("x"), RPCHandlers{})
	require.Error(t, err)
	assert.True(t, ch.Closed(), "a write failure must cancel the channel")
}

// deliverResponse must drop an orphaned partial reassembly when it retires the
// last handler for that correlation id.
func TestDeliverResponseDropsOrphanedPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 9
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[reqID] = respCh
	ch.reassembly[reqID] = &channelwire.ChunkBuffer{Parts: [][]byte{[]byte("partial")}, Total: 7}

	require.True(t, ch.deliverResponse(reqID, &leapmuxv1.InnerRpcResponse{Payload: []byte("ok")}))

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.NotContains(t, ch.pending, uint64(reqID))
	assert.NotContains(t, ch.reassembly, uint64(reqID),
		"retiring the last handler must drop its orphaned partial")
	assert.Equal(t, []byte("ok"), (<-respCh).GetPayload())
}

// After N tunnel writes and N acks, Channel.pending must be empty: deliverResponse
// is one-shot and runAckLoop must not leave registrations behind.
func TestChannelPendingEmptyAfterWritesAndAcks(t *testing.T) {
	ch, _ := pairedSendChannel(t)
	tc := newConn(ch, "conn-pending", "example.test", 443)

	const n = 8
	for range n {
		_, err := tc.Write([]byte("x"))
		require.NoError(t, err)
	}
	ch.mu.Lock()
	require.Equal(t, n, len(ch.pending), "each Write registers one pending ack handler")
	ids := make([]uint64, 0, n)
	for id := range ch.pending {
		ids = append(ids, id)
	}
	ch.mu.Unlock()

	for _, id := range ids {
		require.True(t, ch.deliverResponse(id, &leapmuxv1.InnerRpcResponse{}))
	}

	require.Eventually(t, func() bool {
		return tc.sendWindow.InUse() == 0
	}, 2*time.Second, 5*time.Millisecond, "ack loop must grant every slot back")

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.Empty(t, ch.pending, "every one-shot pending registration must be consumed")
}
