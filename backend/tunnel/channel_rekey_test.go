package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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

// pairedRekeyChannel stands up a Noise pair over a WebSocket with the client's
// recvLoop running so Ack/Reject can complete an in-band rekey round trip.
func pairedRekeyChannel(t *testing.T) (ch *Channel, peerSession *noiseutil.Session, peerWS *websocket.Conn) {
	t.Helper()

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := noiseutil.ClassicalInitiatorHandshake1(key.X25519Public)
	require.NoError(t, err)
	msg2, peerSess, err := noiseutil.ClassicalResponderHandshake(key.X25519Public, key.X25519Private, msg1)
	require.NoError(t, err)
	initiatorSession, err := noiseutil.ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

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
	peerWS = srvConn
	mu.Unlock()
	require.NotNil(t, peerWS)
	peerWS.SetReadLimit(channelwire.WSReadLimit)

	ch = &Channel{
		channelID:   "rekey-test",
		userID:      "user-1",
		session:     initiatorSession,
		ws:          client,
		ctx:         ctx,
		cancel:      cancel,
		pending:     make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:   make(map[uint64]*streamCallback),
		reassembly:  make(map[uint64]*channelwire.ChunkBuffer),
		lastRekeyAt: time.Now(),
	}
	go ch.recvLoop()
	return ch, peerSess, peerWS
}

func awaitRekeyRequest(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	pt, err := peer.Decrypt(msg.GetCiphertext())
	require.NoError(t, err)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(pt, &envelope))
	_, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_RekeyRequest)
	require.True(t, ok, "expected RekeyRequest, got %T", envelope.GetKind())
	return msg.GetCorrelationId()
}

func sendRekeyOutcome(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, accept bool) {
	t.Helper()
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, accept, 0)
}

func sendRekeyOutcomeWithRetry(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, accept bool, retryAfterMs int64) {
	t.Helper()
	ctx := context.Background()
	var envelope *leapmuxv1.InnerMessage
	if accept {
		peer.RekeyReceive()
		envelope = &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{}},
		}
	} else {
		envelope = &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyReject{RekeyReject: &leapmuxv1.RekeyReject{
				RetryAfterMs: retryAfterMs,
			}},
		}
	}
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct, err := peer.Encrypt(pt)
	require.NoError(t, err)
	require.NoError(t, channelwire.WriteChannelMessage(ctx, peerWS,
		channelwire.NewChannelMessage("rekey-test", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, ct)))
	if accept {
		peer.RekeySend()
	}
}

func TestChannelRekeyAcceptSurvivesLiveConn(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	tc := newConn(ch, "conn-rekey", "example.test", 443)
	t.Cleanup(func() { _ = tc.Close() })

	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed(), "accepted rekey must not cancel the channel")
	assert.Equal(t, uint64(0), ch.session.Send.Nonce(), "Send nonce resets after rekey")
	assert.Equal(t, uint64(0), ch.session.Receive.Nonce(), "Receive nonce resets after rekey")

	// Live Conn must still be able to put a frame on the wire under the new key.
	n, err := tc.Write([]byte("after-rekey"))
	require.NoError(t, err)
	assert.Equal(t, 11, n)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	_, err = peer.Decrypt(msg.GetCiphertext())
	require.NoError(t, err, "peer must decrypt post-rekey frames under the new receive key")
}

func TestChannelRekeyRejectLeavesChannelOpen(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)

	sendNonceBefore := ch.session.Send.Nonce()
	recvNonceBefore := ch.session.Receive.Nonce()
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, false)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed(), "Reject must not cancel the channel")
	// Request advanced Send; Reject advanced Receive; neither CipherState rekeyed.
	assert.Equal(t, sendNonceBefore+1, ch.session.Send.Nonce())
	assert.Equal(t, recvNonceBefore+1, ch.session.Receive.Nonce())
	assert.False(t, ch.rekeyNotBefore.IsZero(), "Reject arms age-only backoff")
}

func TestChannelRekeyTimeoutCancelsChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("waits sessionVerifyTimeout for missing Ack/Reject")
	}
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		ch.rekeyMu.Lock()
		defer ch.rekeyMu.Unlock()
		done <- ch.initiateRekeyLocked(context.Background())
	}()

	_ = awaitRekeyRequest(t, peer, peerWS)
	// No Ack/Reject — initiator must fail closed.
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rekey timeout")
		assert.True(t, ch.Closed(), "Ack timeout must cancel the channel")
	case <-time.After(sessionVerifyTimeout + 3*time.Second):
		t.Fatal("rekey timeout did not fire")
	}
}

func TestChannelRekeyRejectSuppressesAgeOnlyRetry(t *testing.T) {
	ch, _, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)
	ch.rekeyNotBefore = time.Now().Add(channelwire.MinRekeyInterval)

	require.NoError(t, ch.ensureRekeyed(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "age-only retry must not send while rekeyNotBefore is in the future")
}

func TestChannelRekeyRejectFallsBackToDefaultBackoff(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	// Legacy peer: empty retry_after_ms → DefaultRejectBackoff.
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, false, 0)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	remain := time.Until(ch.rekeyNotBefore)
	assert.Greater(t, remain, 50*time.Second, "fallback must be ~DefaultRejectBackoff")
	assert.LessOrEqual(t, remain, channelwire.DefaultRejectBackoff)
}

func TestChannelRekeyRejectHonorsRetryAfter(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	const retryMs int64 = 180_000
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, false, retryMs)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed())
	remain := time.Until(ch.rekeyNotBefore)
	assert.Greater(t, remain, 170*time.Second)
	assert.Less(t, remain, 181*time.Second)
}

func TestChannelRekeyCallerCtxCancelKeepsChannelOpen(t *testing.T) {
	// After RekeyRequest is on the wire, cancelling the *caller's* ctx must not
	// tear down the shared channel (that would RST every multiplexed Conn). The
	// Ack wait is bound to ch.ctx + sessionVerifyTimeout; once Ack arrives,
	// ensureRekeyed succeeds and the caller sees its cancel on the subsequent send.
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(ctx)
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	cancel()
	sendRekeyOutcome(t, peer, peerWS, corr, true)

	select {
	case err := <-done:
		require.NoError(t, err, "Ack must complete rekey even after caller ctx cancel")
		assert.False(t, ch.Closed(), "caller ctx cancel must not tear the channel down")
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not return after Ack")
	}
}

func TestChannelRekeyDuplicateAckDoesNotDoubleRotate(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	sendNonce := ch.session.Send.Nonce()
	recvNonce := ch.session.Receive.Nonce()

	// Second Ack under the already-rotated peer send key (no further peer Rekey).
	// handleRekeyOutcome must ignore it — rekeyWait was cleared on the first Ack.
	ctx := context.Background()
	pt, err := proto.Marshal(&leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{}},
	})
	require.NoError(t, err)
	ct, err := peer.Encrypt(pt)
	require.NoError(t, err)
	require.NoError(t, channelwire.WriteChannelMessage(ctx, peerWS,
		channelwire.NewChannelMessage("rekey-test", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, ct)))

	time.Sleep(50 * time.Millisecond)
	assert.False(t, ch.Closed(), "duplicate Ack must not desync keys")
	assert.Equal(t, sendNonce, ch.session.Send.Nonce(), "Send must not rotate twice")
	assert.Equal(t, recvNonce+1, ch.session.Receive.Nonce(), "Receive advances once for the duplicate Ack decrypt only")
}

func TestChannelRekeyPastHardCeilingCancels(t *testing.T) {
	ch, _, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyHardCeiling - time.Second)

	err := ch.ensureRekeyed(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hard ceiling")
	assert.True(t, ch.Closed(), "past hard ceiling must cancel the channel")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, readErr := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, readErr, "must not send RekeyRequest when past hard ceiling")
}

// TestChannelRekeyMuSerializesEnsureAndSend proves ensureRekeyed and
// sendInnerContext share rekeyMu: while the lock is held (as sendInnerContext
// does across a whole multi-chunk message), ensure cannot put a RekeyRequest
// on the wire between chunks.
func TestChannelRekeyMuSerializesEnsureAndSend(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	ch.rekeyMu.Lock() // simulate sendInnerContext mid multi-chunk send

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	select {
	case <-done:
		t.Fatal("ensureRekeyed returned while rekeyMu was held")
	case <-time.After(50 * time.Millisecond):
	}
	// Do not Read on peerWS here: a cancelled Read can poison the websocket.

	ch.rekeyMu.Unlock()

	corr := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete after unlock")
	}
}

func TestChannelRekeyRejectPastHardCeilingCancels(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	// Simulate the key aging past the hard ceiling while waiting for the
	// Reject (e.g. a slow peer). ensureRekeyed holds rekeyMu across the wait,
	// so bump lastRekeyAt without taking that lock.
	ch.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyHardCeiling)
	sendRekeyOutcome(t, peer, peerWS, corr, false)

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hard ceiling")
		assert.True(t, ch.Closed(), "Reject past hard ceiling must cancel the channel")
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}
}

func TestChannelRekeySoftNonceBypassesRejectBackoff(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.lastRekeyAt = time.Now()
	ch.rekeyNotBefore = time.Now().Add(channelwire.MinRekeyInterval)
	ch.session.Send.SetNonceForTest(noiseutil.SoftNonceLimit + 1)
	peer.Receive.SetNonceForTest(noiseutil.SoftNonceLimit + 1)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("soft-nonce rekey did not complete")
	}
	assert.False(t, ch.Closed())
	assert.Equal(t, uint64(0), ch.session.Send.Nonce())
	assert.True(t, ch.rekeyNotBefore.IsZero(), "successful rekey clears reject backoff")
}
