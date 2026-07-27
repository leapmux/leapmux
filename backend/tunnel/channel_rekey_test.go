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
		channelID:      "rekey-test",
		userID:         "user-1",
		session:        initiatorSession,
		ws:             client,
		ctx:            ctx,
		cancel:         cancel,
		maxReassembled: channelwire.DefaultMaxReassembledMessageSize,
		pending:        make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:      make(map[uint64]*streamCallback),
		reassembly:     make(map[uint64]*channelwire.ChunkBuffer),
		rekey: rekeyController{
			lastRekeyAt: time.Now(),
			// Classical handshake ⇒ rekey is X25519-only: workerMlkemPub stays
			// nil (its zero value), which EncapsulateRekeyPQ short-circuits on.
		},
	}
	go ch.recvLoop()
	return ch, peerSess, peerWS
}

// awaitRekeyRequest reads the initiator's RekeyRequest off the peer socket and
// returns the correlation id plus the initiator's fresh ephemeral public key
// (which the peer combines with its own ephemeral to complete the DH).
func awaitRekeyRequest(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn) (corr uint64, initiatorPub []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	pt, err := peer.Decrypt(msg.GetCiphertext())
	require.NoError(t, err)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(pt, &envelope))
	req, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_RekeyRequest)
	require.True(t, ok, "expected RekeyRequest, got %T", envelope.GetKind())
	return msg.GetCorrelationId(), req.RekeyRequest.GetDhPub()
}

func sendRekeyOutcome(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, initiatorPub []byte, accept bool) {
	t.Helper()
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, initiatorPub, accept, 0)
}

// sendRekeyOutcomeWithRetry simulates the worker responder: on accept it
// generates its own fresh ephemeral, derives the shared DH secret, rotates the
// peer's receive key (retaining prev), sends the Ack carrying the responder
// ephemeral, then rotates the peer's send key. Classical mode ⇒ no ML-KEM.
func sendRekeyOutcomeWithRetry(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, initiatorPub []byte, accept bool, retryAfterMs int64) {
	t.Helper()
	ctx := context.Background()
	var envelope *leapmuxv1.InnerMessage
	if accept {
		require.Len(t, initiatorPub, noiseutil.EphemeralPublicKeySize, "initiator rekey ephemeral must be 32 bytes")
		eph, err := noiseutil.GenerateEphemeralX25519()
		require.NoError(t, err)
		dh, pq, err := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
			LocalEphemeralPriv: eph.Bytes(),
			PeerEphemeralPub:   initiatorPub,
		})
		require.NoError(t, err)
		// Each direction needs its own copy: rekeyWithSecret zeroes its inputs.
		dhForSend := append([]byte(nil), dh...)
		pqForSend := append([]byte(nil), pq...)
		peer.RekeyReceiveWithSecret(dh, pq)
		envelope = &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{
				DhPub: eph.PublicKey().Bytes(),
			}},
		}
		pt, err := proto.Marshal(envelope)
		require.NoError(t, err)
		ct, err := peer.Encrypt(pt)
		require.NoError(t, err)
		require.NoError(t, channelwire.WriteChannelMessage(ctx, peerWS,
			channelwire.NewChannelMessage("rekey-test", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, ct)))
		peer.RekeySendWithSecret(dhForSend, pqForSend)
	} else {
		envelope = &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyReject{RekeyReject: &leapmuxv1.RekeyReject{
				RetryAfterMs: retryAfterMs,
			}},
		}
		pt, err := proto.Marshal(envelope)
		require.NoError(t, err)
		ct, err := peer.Encrypt(pt)
		require.NoError(t, err)
		require.NoError(t, channelwire.WriteChannelMessage(ctx, peerWS,
			channelwire.NewChannelMessage("rekey-test", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, ct)))
	}
}

func TestChannelRekeyAcceptSurvivesLiveConn(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	tc := newConn(ch, "conn-rekey", "example.test", 443)
	t.Cleanup(func() { _ = tc.Close() })

	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

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

// TestChannelRekeyAcceptSurvivesWatchdog pins the F-1 regression: the rekey
// watchdog timer (runRekeyTimer, sessionVerifyTimeout = 10s) MUST retire the
// instant the rekey resolves on Ack — not fire 10s later and cancel a healthy
// channel. Pre-fix the detached timer only selected on <-timer.C / <-ctx.Done(),
// so every successful rekey closed the channel ~10s after the Ack; the
// happy-path tests above assert !Closed() immediately after the Ack and never
// wait sessionVerifyTimeout, so they would NOT catch this regression returning.
// This test polls past sessionVerifyTimeout and fails the moment the channel
// closes. Slow (~12s) and skipped under -short, matching
// TestChannelRekeyTimeoutCancelsChannel.
func TestChannelRekeyAcceptSurvivesWatchdog(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past sessionVerifyTimeout to confirm the watchdog retired")
	}
	ch, peer, peerWS := pairedRekeyChannel(t)
	defer func() { _ = peerWS.CloseNow() }()

	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)
	require.NoError(t, <-done)
	require.False(t, ch.Closed(), "channel open right after Ack")

	// Watch the channel across the whole sessionVerifyTimeout window. A leaked
	// watchdog fires at +10s and cancels it; a correctly-disarmed one retires
	// on resolution and the channel stays open.
	deadline := time.Now().Add(sessionVerifyTimeout + 2*time.Second)
	for time.Now().Before(deadline) {
		if ch.Closed() {
			t.Fatalf("REGRESSION: channel cancelled within %s of a successful rekey — watchdog timer was not disarmed on Ack",
				sessionVerifyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.False(t, ch.Closed(), "channel survived past sessionVerifyTimeout after a successful rekey")
}

// TestChannelRekeyConcurrentCallersShareOneRequest proves the multi-waiter
// broadcast: when two callers race into ensureRekeyed while a rekey is due, one
// starts it (owns the Request + material) and the other joins its outcome — so
// only a single RekeyRequest hits the wire, and both callers complete on the one
// Ack. This is the core of the non-blocking rekey: rekeyMu is released across
// the RTT, so a second sender arriving mid-rekey must not start a second one.
func TestChannelRekeyConcurrentCallersShareOneRequest(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	// Two concurrent callers. Both should block on the in-flight rekey.
	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	// Exactly one RekeyRequest must reach the wire (the joiner must not send).
	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

	// Both callers must resolve on the single Ack.
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.NoError(t, err, "caller %d must resolve on the shared Ack", i)
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d did not complete after the shared Ack", i)
		}
	}
	assert.False(t, ch.Closed(), "shared rekey must not cancel the channel")

	// No second Request: the peer socket must have nothing more to read.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "joiner must not send a second RekeyRequest")
}

// TestChannelRekeyConcurrentCallersShareReject proves the multi-waiter
// broadcast on a REJECT: two concurrent callers both resolve on a single
// Reject, both arm the backoff, and only one RekeyRequest hit the wire. This
// covers the reject path of resolveRekey's broadcast (the accept path is
// covered by TestChannelRekeyConcurrentCallersShareOneRequest).
func TestChannelRekeyConcurrentCallersShareReject(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, initiatorPub, false, 0)

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.NoError(t, err, "caller %d must resolve on the shared Reject", i)
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d did not complete after the shared Reject", i)
		}
	}
	assert.False(t, ch.Closed(), "shared Reject must not cancel the channel")
	assert.False(t, ch.rekey.rekeyNotBefore.IsZero(), "shared Reject arms the backoff")

	// No second Request.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "joiner must not send a second RekeyRequest")
}

func TestChannelRekeyRejectLeavesChannelOpen(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)

	sendNonceBefore := ch.session.Send.Nonce()
	recvNonceBefore := ch.session.Receive.Nonce()
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, false)

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
	assert.False(t, ch.rekey.rekeyNotBefore.IsZero(), "Reject arms age-only backoff")
}

func TestChannelRekeyTimeoutCancelsChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("waits sessionVerifyTimeout for missing Ack/Reject")
	}
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	awaitRekeyRequest(t, peer, peerWS)
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
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)
	ch.rekey.rekeyNotBefore = time.Now().Add(channelwire.MinRekeyInterval)

	require.NoError(t, ch.ensureRekeyed(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "age-only retry must not send while rekeyNotBefore is in the future")
}

func TestChannelRekeyRejectFallsBackToDefaultBackoff(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	// Legacy peer: empty retry_after_ms → DefaultRejectBackoff.
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, initiatorPub, false, 0)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	remain := time.Until(ch.rekey.rekeyNotBefore)
	assert.Greater(t, remain, 50*time.Second, "fallback must be ~DefaultRejectBackoff")
	assert.LessOrEqual(t, remain, channelwire.DefaultRejectBackoff)
}

func TestChannelRekeyRejectHonorsRetryAfter(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	const retryMs int64 = 180_000
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, initiatorPub, false, retryMs)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed())
	remain := time.Until(ch.rekey.rekeyNotBefore)
	assert.Greater(t, remain, 170*time.Second)
	assert.Less(t, remain, 181*time.Second)
}

func TestChannelRekeyCallerCtxCancelKeepsChannelOpen(t *testing.T) {
	// After RekeyRequest is on the wire, cancelling the *caller's* ctx must not
	// tear down the shared channel (that would RST every multiplexed Conn). The
	// Ack wait is bound to ch.ctx + sessionVerifyTimeout; once Ack arrives,
	// ensureRekeyed succeeds and the caller sees its cancel on the subsequent send.
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(ctx)
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	cancel()
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

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
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

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

// TestChannelRekeyAckWithBadResponderEphemeralCancels proves the fresh-DH
// agreement is fail-closed: a RekeyAck whose dh_pub is not a valid 32-byte
// X25519 point must not rotate keys onto an attacker-controllable derivation.
// The channel cancels and the waiting sender observes a terminal error rather
// than a retryable Reject.
func TestChannelRekeyAckWithBadResponderEphemeralCancels(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, _ := awaitRekeyRequest(t, peer, peerWS)

	// Ack with a too-short dh_pub: handleRekeyAck must reject it and close.
	badPub := make([]byte, noiseutil.EphemeralPublicKeySize-1)
	pt, err := proto.Marshal(&leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{
			DhPub: badPub,
		}},
	})
	require.NoError(t, err)
	ct, err := peer.Encrypt(pt)
	require.NoError(t, err)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS,
		channelwire.NewChannelMessage("rekey-test", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, ct)))

	select {
	case err := <-done:
		require.Error(t, err, "bad responder ephemeral must surface a terminal error")
		assert.True(t, ch.Closed(), "bad responder ephemeral must cancel the channel")
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete after bad Ack")
	}
}

// TestChannelRekeyAckClaimsMaterial pins the SCAN-1 data-race fix:
// handleRekeyAck must CLAIM the in-flight rekeyMaterial (nil the slot under
// ch.mu) before reading rm.ephemeralSeed outside the lock. runRekeyTimer (a
// separate goroutine) and the awaitRekeyOutcome ctx.Done arm both call
// resolveRekey, which re-reads the SAME *rekeySecrets pointer and zeroes
// rm.ephemeralSeed. If the slot were left populated, an Ack landing at ~the
// sessionVerifyTimeout boundary would race the DeriveRekeySecrets read against
// resolveRekey's zeroBytes write — a torn X25519 scalar read (`go test -race`
// flags it). With the slot nilled, resolveRekey finds rekeyMaterial==nil and
// skips the zero; handleRekeyAck owns the wipe. This test asserts the claimed
// (nil) state after a successful Ack so a regression that re-introduces the
// snapshot-without-claim shape fails here (and -race catches the rest).
func TestChannelRekeyAckClaimsMaterial(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete")
	}

	// After a successful Ack, handleRekeyAck must have claimed (nilled) the
	// material slot — it must not leave the *rekeySecrets pointer for a later
	// resolveRekey (e.g. a timer firing on the same done channel) to zero
	// concurrently with the Ack's derivation read.
	ch.mu.Lock()
	assert.Nil(t, ch.rekey.rekeyMaterial, "handleRekeyAck must claim (nil) rekeyMaterial, not leave it for resolveRekey")
	ch.mu.Unlock()
}

// TestChannelRekeyConcurrentCallersShareTerminalFailure pins the third arm of
// the multi-waiter broadcast (SWEEP-3): when two callers join an in-flight
// rekey and it fails TERMINALLY (timeout / derivation failure / channel close),
// both callers must surface the error — not hang, and not get a retryable
// Reject. The accept and reject arms are covered by
// TestChannelRekeyConcurrentCallersShareOneRequest / ShareReject; this covers
// the outcome.err != nil branch of resolveRekey's broadcast.
func TestChannelRekeyConcurrentCallersShareTerminalFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("waits sessionVerifyTimeout for the terminal-failure broadcast")
	}
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	// Let the single Request reach the wire and both callers join it.
	corr, _ := awaitRekeyRequest(t, peer, peerWS)

	// Send no Ack/Reject — runRekeyTimer fires after sessionVerifyTimeout and
	// resolves terminally. Both callers must observe an error (not nil) and the
	// channel must cancel.
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.Error(t, err, "caller %d must surface the terminal failure", i)
		case <-time.After(sessionVerifyTimeout + 5*time.Second):
			t.Fatalf("caller %d did not surface the terminal failure", i)
		}
	}
	assert.True(t, ch.Closed(), "terminal failure must cancel the channel")

	// No second Request: the peer socket must have nothing more to read.
	_ = corr
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "only one Request must hit the wire")
}

func TestChannelRekeyPastHardCeilingCancels(t *testing.T) {
	ch, _, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyHardCeiling - time.Second)

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
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	ch.rekey.rekeyMu.Lock() // simulate sendInnerContext mid multi-chunk send

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

	ch.rekey.rekeyMu.Unlock()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensureRekeyed did not complete after unlock")
	}
}

func TestChannelRekeyRejectPastHardCeilingCancels(t *testing.T) {
	ch, peer, peerWS := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	// Simulate the key aging past the hard ceiling while waiting for the Reject
	// (e.g. a slow peer). lastRekeyAt is rekeyMu-guarded and read under that
	// lock in awaitRekeyOutcome (which runs on the ensureRekeyed goroutine
	// across the RTT), so take the lock here to avoid a data race.
	ch.rekey.rekeyMu.Lock()
	ch.rekey.lastRekeyAt = time.Now().Add(-channelwire.SessionKeyHardCeiling)
	ch.rekey.rekeyMu.Unlock()
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, false)

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
	ch.rekey.lastRekeyAt = time.Now()
	ch.rekey.rekeyNotBefore = time.Now().Add(channelwire.MinRekeyInterval)
	ch.session.Send.SetNonceForTest(noiseutil.SoftNonceLimit + 1)
	peer.Receive.SetNonceForTest(noiseutil.SoftNonceLimit + 1)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, initiatorPub := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, initiatorPub, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("soft-nonce rekey did not complete")
	}
	assert.False(t, ch.Closed())
	assert.Equal(t, uint64(0), ch.session.Send.Nonce())
	assert.True(t, ch.rekey.rekeyNotBefore.IsZero(), "successful rekey clears reject backoff")
}
