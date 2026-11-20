package tunnel

import (
	"context"
	"crypto/mlkem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// testRekeyWatchdog is the budget for the two tests that need the watchdog to
// actually FIRE. It replaces the production sessionVerifyTimeout (10s): what
// those tests pin is what the watchdog does at its deadline, not how far away
// the deadline is.
//
// Two seconds, not milliseconds. The deadline is also the window in which a
// second caller must join the in-flight rekey (see
// TestChannelRekeyConcurrentCallersShareTerminalFailure), and a watchdog that
// can beat a goroutine to its first scheduling slice would flake on a loaded
// CI box. Two seconds is orders of magnitude past any plausible stall while
// still costing a fifth of the production window.
const (
	testRekeyWatchdog = 2 * time.Second
	eventWaitLimit    = 30 * time.Second
)

// rekeyChannelOption customizes the Channel pairedRekeyChannel builds.
type rekeyChannelOption struct {
	postQuantum bool
	apply       func(*Channel)
}

// withRekeyWatchdog shortens the in-flight-rekey watchdog so a test can watch
// it fire (or prove it retired) without parking on the real clock.
func withRekeyWatchdog(d time.Duration) rekeyChannelOption {
	return rekeyChannelOption{apply: func(ch *Channel) { ch.rekey.watchdogTimeout = d }}
}

func withPostQuantumRekey() rekeyChannelOption {
	return rekeyChannelOption{postQuantum: true}
}

// withCancelGate parks ch.cancel so a test can inspect what a resolution has --
// and has not -- done at the moment it cancels the channel. entered is closed on
// the way in, and the cancel completes once release is closed; later cancels
// fall straight through.
//
// A gate rather than a lock barrier, because there is no lock to hold on the
// right side of the ordering: a terminal resolution takes ch.mu to CLAIM the
// waiter list, then cancels, then broadcasts without taking anything (see
// cancelAndResolveRekeyTerminal). Holding ch.mu parks it before the cancel --
// the wrong side -- and the old broadcast-first order parked there too, so the
// two are indistinguishable that way.
//
// Options run before pairedRekeyChannel starts recvLoop, so wrapping the field
// here never races that goroutine.
func withCancelGate(entered chan<- struct{}, release <-chan struct{}) rekeyChannelOption {
	return rekeyChannelOption{apply: func(ch *Channel) {
		inner := ch.cancel
		announce := sync.OnceFunc(func() { close(entered) })
		ch.cancel = func() {
			announce()
			<-release
			inner()
		}
	}}
}

// pairedRekeyChannel stands up a Noise pair over a WebSocket with the client's
// recvLoop running so Ack/Reject can complete an in-band rekey round trip.
func pairedRekeyChannel(t *testing.T, opts ...rekeyChannelOption) (ch *Channel, peerSession *noiseutil.Session, peerWS *websocket.Conn, workerKey *noiseutil.CompositeKeypair) {
	t.Helper()

	postQuantum := false
	for _, opt := range opts {
		postQuantum = postQuantum || opt.postQuantum
	}
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	var hs *noiseutil.HandshakeState
	var msg1 []byte
	if postQuantum {
		hs, msg1, err = noiseutil.InitiatorHandshake1("worker-test", key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPub)
	} else {
		hs, msg1, err = noiseutil.ClassicalInitiatorHandshake1("worker-test", key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPub)
	}
	require.NoError(t, err)
	var msg2 []byte
	var peerSess *noiseutil.Session
	if postQuantum {
		msg2, peerSess, err = noiseutil.ResponderHandshake(key, "worker-test", msg1)
	} else {
		msg2, peerSess, err = noiseutil.ClassicalResponderHandshake(key, "worker-test", msg1)
	}
	require.NoError(t, err)
	initiatorSession, err := hs.Finish(msg2)
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
	case <-time.After(eventWaitLimit):
		t.Fatal("server did not accept the websocket")
	}

	mu.Lock()
	peerWS = srvConn
	mu.Unlock()
	require.NotNil(t, peerWS)
	peerWS.SetReadLimit(channelwire.WSReadLimit)

	var workerMlkemPub []byte
	if postQuantum {
		workerMlkemPub = key.MlkemPublicKeyBytes()
	}
	ch = &Channel{
		channelID:      "rekey-test",
		userID:         "user-1",
		session:        initiatorSession,
		ws:             client,
		ctx:            ctx,
		cancel:         cancel,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
		pending:        make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:      make(map[uint64]*streamCallback),
		reassembly:     make(map[uint64]*channelwire.ChunkBuffer),
		rekey: rekeyController{
			lastRekeyAt:    time.Now(),
			workerMlkemPub: workerMlkemPub,
		},
	}
	for _, opt := range opts {
		if opt.apply != nil {
			opt.apply(ch)
		}
	}
	go ch.recvLoop()
	return ch, peerSess, peerWS, key
}

func TestRecvLoopEndsOnTheCloseFrameWithoutADecryptError(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	ch, _, peerWS, _ := pairedRekeyChannel(t)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS,
		channelwire.CloseNotification("rekey-test")))

	select {
	case <-ch.ctx.Done():
	case <-time.After(eventWaitLimit):
		t.Fatal("the close frame did not end the receive loop")
	}
	assert.True(t, ch.Closed())
	assert.NotContains(t, logs.String(), "tunnel channel decrypt error")
}

// awaitRekeyRequest opens the initiator's request and returns its full contents.
func awaitRekeyRequest(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn) (corr uint64, request *leapmuxv1.RekeyRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventWaitLimit)
	defer cancel()
	msg, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	pt, err := channelwire.OpenChannelMessage(peer, msg)
	require.NoError(t, err)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(pt, &envelope))
	req, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_RekeyRequest)
	require.True(t, ok, "expected RekeyRequest, got %T", envelope.GetKind())
	return msg.GetCorrelationId(), req.RekeyRequest
}

func sendRekeyOutcome(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, request *leapmuxv1.RekeyRequest, accept bool) {
	t.Helper()
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, request, accept, 0)
}

func sendRekeyOutcomeWithRetry(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, corr uint64, request *leapmuxv1.RekeyRequest, accept bool, retryAfterMs int64) {
	t.Helper()
	sendRekeyOutcomeWithKey(t, peer, peerWS, nil, corr, request, accept, retryAfterMs)
}

// sendRekeyOutcomeWithKey simulates the Worker, including its ML-KEM answer when
// workerKey is non-nil. It rotates receive, seals the Ack under the old send key,
// then rotates send.
func sendRekeyOutcomeWithKey(t *testing.T, peer *noiseutil.Session, peerWS *websocket.Conn, workerKey *noiseutil.CompositeKeypair, corr uint64, request *leapmuxv1.RekeyRequest, accept bool, retryAfterMs int64) {
	t.Helper()
	var envelope *leapmuxv1.InnerMessage
	if accept {
		require.Len(t, request.GetDhPub(), noiseutil.EphemeralPublicKeySize)
		seed, err := noiseutil.GenerateEphemeralX25519Seed()
		require.NoError(t, err)
		defer noiseutil.Zero(seed)
		responderPub, err := noiseutil.X25519PublicFromSeed(seed)
		require.NoError(t, err)
		var decapsulationKey = (*mlkem.DecapsulationKey1024)(nil)
		if workerKey != nil {
			decapsulationKey = workerKey.MlkemDecapsulationKey
		}
		pqSecret, ephemeralCiphertext, err := noiseutil.AnswerRekeyPQ(decapsulationKey, request.GetMlkemStaticCt(), request.GetMlkemEphemeralPub())
		require.NoError(t, err)
		dh, pq, err := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
			LocalEphemeralPriv: seed,
			PeerEphemeralPub:   request.GetDhPub(),
			PQSharedSecret:     pqSecret,
		})
		require.NoError(t, err)
		dhForSend := append([]byte(nil), dh...)
		pqForSend := append([]byte(nil), pq...)
		peer.RekeyReceiveWithSecret(dh, pq)
		envelope = &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{
				DhPub:            responderPub,
				MlkemEphemeralCt: ephemeralCiphertext,
			}},
		}
		writePeerMessage(t, peer, peerWS, corr, envelope)
		peer.RekeySendWithSecret(dhForSend, pqForSend)
		return
	}
	envelope = &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyReject{RekeyReject: &leapmuxv1.RekeyReject{
			RetryAfterMs: retryAfterMs,
		}},
	}
	writePeerMessage(t, peer, peerWS, corr, envelope)
}

func sealChannelPlaintextForTunnel(t *testing.T, session *noiseutil.Session, correlationID uint64, plaintext []byte) *leapmuxv1.ChannelMessage {
	t.Helper()
	frame, err := channelwire.SealChannelMessage(session, "rekey-test", correlationID, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, plaintext)
	require.NoError(t, err)
	return frame
}

func TestChannelRekeyAcceptSurvivesLiveConn(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	tc := newOpenedConn(ch, "conn-rekey", "example.test", 443)
	t.Cleanup(func() { _ = tc.Close() })

	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed(), "accepted rekey must not cancel the channel")
	assert.Equal(t, uint64(0), ch.session.Send.Nonce(), "Send nonce resets after rekey")
	assert.Equal(t, uint64(0), ch.session.Receive.Nonce(), "Receive nonce resets after rekey")

	// Live Conn must still be able to put a frame on the wire under the new key.
	n, err := tc.Write([]byte("after-rekey"))
	require.NoError(t, err)
	assert.Equal(t, 11, n)

	ctx, cancel := context.WithTimeout(context.Background(), eventWaitLimit)
	defer cancel()
	msg, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	_, err = channelwire.OpenChannelMessage(peer, msg)
	require.NoError(t, err, "peer must decrypt post-rekey frames under the new receive key")
}

// TestChannelRekeyAcceptSurvivesWatchdog pins the F-1 regression: the rekey
// watchdog timer (runRekeyTimer) MUST retire the instant the rekey resolves on
// Ack — not fire a full watchdog window later and cancel a healthy channel.
// Pre-fix the detached timer only selected on <-timer.C / <-ctx.Done(), so
// every successful rekey closed the channel one window after the Ack; the
// happy-path tests above assert !Closed() immediately after the Ack and never
// wait out the watchdog, so they would NOT catch this regression returning.
//
// The channel keeps the PRODUCTION watchdog. The assertion is on the timer
// goroutine retiring (watchdogLive draining to zero), not on the channel still
// being open once the window has elapsed, so it neither waits out 10s nor
// races a shortened window against the Ack round trip -- a race a loaded CI
// box would eventually lose.
func TestChannelRekeyAcceptSurvivesWatchdog(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	defer func() { _ = peerWS.CloseNow() }()

	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	// Poll rather than read once: startRekeyLocked SPAWNS runRekeyTimer and the
	// counter is incremented inside that goroutine, so awaitRekeyRequest
	// (which returns once the peer has read the Request frame) orders nothing
	// against it. A single Load() passes only because the runtime usually
	// schedules the new goroutine first.
	require.Eventually(t, func() bool { return ch.rekey.watchdogLive.Load() > 0 },
		2*time.Second, time.Millisecond, "the starter must have armed a watchdog")
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)
	require.NoError(t, <-done)
	require.False(t, ch.Closed(), "channel open right after Ack")

	// The Ack resolved the rekey, so runRekeyTimer's <-done arm must be taken
	// and the goroutine must return. Pre-fix it selected only on <-timer.C /
	// <-ctx.Done() and stayed live until it cancelled the healthy channel at
	// +sessionVerifyTimeout. The generous deadline is a scheduling allowance
	// for a loaded machine, not a timing budget: the retire is immediate.
	require.Eventually(t, func() bool { return ch.rekey.watchdogLive.Load() == 0 },
		30*time.Second, 5*time.Millisecond,
		"REGRESSION: the rekey watchdog goroutine did not retire on Ack — it is still armed to cancel a healthy channel at +%s",
		sessionVerifyTimeout)
	assert.False(t, ch.Closed(), "a resolved rekey must leave the channel open")
}

// TestChannelRekeyConcurrentCallersShareOneRequest proves the multi-waiter
// broadcast: when two callers race into ensureRekeyed while a rekey is due, one
// starts it (owns the Request + material) and the other joins its outcome — so
// only a single RekeyRequest hits the wire, and both callers complete on the one
// Ack. This is the core of the non-blocking rekey: rekeyMu is released across
// the RTT, so a second sender arriving mid-rekey must not start a second one.
func TestChannelRekeyConcurrentCallersShareOneRequest(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	// Two concurrent callers. Both should block on the in-flight rekey.
	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	// Exactly one RekeyRequest must reach the wire (the joiner must not send).
	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	// Both callers must resolve on the single Ack.
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.NoError(t, err, "caller %d must resolve on the shared Ack", i)
		case <-time.After(eventWaitLimit):
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
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, request, false, 0)

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.NoError(t, err, "caller %d must resolve on the shared Reject", i)
		case <-time.After(eventWaitLimit):
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

// TestChannelRekeyRejectArmsBackoffBeforeWakingWaiters pins the ordering the
// concurrent-Reject broadcast depends on: the backoff must be armed by the
// RESOLUTION (on the recvLoop goroutine, before the waiter list is drained), not
// by each waiting caller once it wakes up. Arming it in the waiter leaves a
// window -- waiters drained, backoff still zero, lastRekeyAt still stale -- in
// which a sender entering startRekeyLocked finds no in-flight rekey to join and
// no backoff to respect, so it starts a SECOND rekey the peer just rejected.
//
// This is the deterministic form of the flake in
// TestChannelRekeyConcurrentCallersShareReject: there the redundant Request went
// unanswered by the single-outcome peer stub, so that caller blocked until the
// sessionVerifyTimeout watchdog fired and cancelled the whole channel.
func TestChannelRekeyRejectArmsBackoffBeforeWakingWaiters(t *testing.T) {
	ch, _, _, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	// Stand in for an in-flight rekey whose starter has not woken yet: a
	// registered waiter nobody is reading. This is exactly the state
	// handleRekeyReject observes on the recvLoop goroutine.
	waiter := make(chan rekeyOutcome, 1)
	const reqID = uint64(7)
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.rekey.rekeyReqID = reqID
	ch.mu.Unlock()

	ch.handleRekeyReject(reqID, 0)

	require.Len(t, waiter, 1, "the Reject must have resolved the in-flight rekey")
	// assert (not require) so a regression reports BOTH the un-armed backoff and
	// the redundant Request it lets through, rather than stopping at the state.
	assert.False(t, ch.rekey.rekeyNotBefore.IsZero(),
		"the resolution must arm the backoff; a waiting caller may not have run yet")

	// A sender arriving in the window between the Reject resolving and the
	// starter waking must see the backoff and start no second rekey. Call
	// startRekeyLocked the way ensureRekeyed does (under rekeyMu) so the check
	// covers the real decision path rather than the policy fields alone.
	ch.rekey.rekeyMu.Lock()
	wait, _, send, err := ch.startRekeyLocked(context.Background())
	ch.rekey.rekeyMu.Unlock()
	require.NoError(t, err)
	assert.Nil(t, send, "the Reject's backoff must suppress a redundant RekeyRequest")
	assert.Nil(t, wait, "nothing in flight to wait on after the Reject resolved")
	assert.False(t, ch.Closed(), "a rejected rekey must not cancel the channel")
}

func TestChannelRekeyRejectLeavesChannelOpen(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)

	sendNonceBefore := ch.session.Send.Nonce()
	recvNonceBefore := ch.session.Receive.Nonce()
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, false)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not complete")
	}

	assert.False(t, ch.Closed(), "Reject must not cancel the channel")
	// Request advanced Send; Reject advanced Receive; neither CipherState rekeyed.
	assert.Equal(t, sendNonceBefore+1, ch.session.Send.Nonce())
	assert.Equal(t, recvNonceBefore+1, ch.session.Receive.Nonce())
	assert.False(t, ch.rekey.rekeyNotBefore.IsZero(), "Reject arms age-only backoff")
}

func TestPreparedRekeyRequestOwnsWireBytes(t *testing.T) {
	t.Parallel()

	worker, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	offer, err := noiseutil.OfferRekeyPQ(worker.MlkemPublicKeyBytes())
	require.NoError(t, err)
	seed, err := noiseutil.GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	material := rekeySecrets{ephemeralSeed: seed, pq: offer}
	defer material.zero()
	wantCiphertext := slices.Clone(offer.StaticCiphertext)
	wantPublicKey := slices.Clone(offer.EphemeralPublicKey)
	envelope, err := material.request()
	require.NoError(t, err)

	material.zero()

	assert.Equal(t, wantCiphertext, envelope.GetRekeyRequest().GetMlkemStaticCt())
	assert.Equal(t, wantPublicKey, envelope.GetRekeyRequest().GetMlkemEphemeralPub())
	assert.Equal(t, make([]byte, len(seed)), seed)
}

func TestStaleRekeyWatchdogLeavesCurrentAttemptAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiter := make(chan rekeyOutcome, 1)
		currentDone := make(chan struct{})
		ch := &Channel{
			ctx: ctx, cancel: cancel,
			rekey: rekeyController{
				rekeyWaiters: []chan rekeyOutcome{waiter},
				rekeyDone:    currentDone,
			},
		}

		// An older attempt can already be claimed without having broadcast its outcome yet.
		ch.runRekeyTimer(make(chan struct{}))

		assert.NoError(t, ctx.Err())
		assert.Empty(t, waiter)
		assert.Equal(t, currentDone, ch.rekey.rekeyDone)
	})
}

func TestChannelRekeyTimeoutCancelsChannel(t *testing.T) {
	// The whole cost of this test is waiting out testRekeyWatchdog, and it
	// shares no state with anything else -- so it waits alongside its sibling
	// watchdog test rather than after it.
	t.Parallel()

	ch, peer, peerWS, _ := pairedRekeyChannel(t, withRekeyWatchdog(testRekeyWatchdog))
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

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
	case <-time.After(30 * time.Second):
		t.Fatal("rekey timeout did not fire")
	}
}

// TestChannelRekeyTimeoutCancelsBeforeWakingWaiters pins the ordering
// TestChannelRekeyTimeoutCancelsChannel above can only observe by luck: the
// watchdog must cancel the channel BEFORE it broadcasts the timeout, never
// after.
//
// Broadcasting first leaves a window -- outcome delivered, ch.ctx still live --
// in which the waking sender returns "rekey timeout" from a channel that still
// reports Closed() == false. A pooled caller (crossworker.channelFor, the
// desktop TunnelManager) consults Closed() to decide whether to re-resolve, so
// in that window it hands the dead channel straight back out. The window is a
// few instructions wide, which is why it surfaced as a one-off Windows CI
// failure rather than a reproducible one.
//
// Parking the cancel makes it deterministic in both directions: broadcast-first
// reaches the waiter before the gate opens, cancel-first cannot.
func TestChannelRekeyTimeoutCancelsBeforeWakingWaiters(t *testing.T) {
	t.Parallel()

	cancelling := make(chan struct{})
	resumeCancel := make(chan struct{})
	// OnceFunc so the cleanup can never strand the watchdog inside the gate,
	// however the body exits, and never double-closes when it did not.
	resume := sync.OnceFunc(func() { close(resumeCancel) })
	t.Cleanup(resume)

	ch, _, _, _ := pairedRekeyChannel(t,
		withRekeyWatchdog(50*time.Millisecond),
		withCancelGate(cancelling, resumeCancel))

	waiter := make(chan rekeyOutcome, 1)
	rekeyDone := make(chan struct{})
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.rekey.rekeyDone = rekeyDone
	ch.rekey.rekeyReqID = 9
	ch.mu.Unlock()

	go ch.runRekeyTimer(rekeyDone)

	select {
	case <-cancelling:
	case <-time.After(eventWaitLimit):
		t.Fatal("the watchdog did not cancel the channel at its deadline")
	}
	// The watchdog is parked inside ch.cancel: it has claimed the rekey but the
	// channel is not dead yet, so the timeout must not have reached a waiter.
	require.Empty(t, waiter, "the outcome must not be broadcast before the cancel")

	resume()

	select {
	case outcome := <-waiter:
		require.Error(t, outcome.err)
		assert.Contains(t, outcome.err.Error(), "rekey timeout")
		assert.True(t, ch.Closed(), "the cancel must be visible to every waiter it wakes")
	case <-time.After(eventWaitLimit):
		t.Fatal("the watchdog did not broadcast the timeout after the cancel")
	}
}

// TestChannelRekeyAwaitPrefersTerminalOutcomeOverCancel pins the other half of
// that ordering. Cancelling before the broadcast makes ch.ctx.Done() ready
// first, so a sender parked in awaitRekeyOutcome can wake on the cancel arm
// while the timeout outcome is still in flight. It must report the reason the
// channel died -- "rekey timeout" -- not the cancel that reason performed, and
// it must not resolve the already-claimed rekey out from under the claimant
// with a substitute "channel closed" outcome.
//
// Without this, cancelling first would only trade the flake in
// TestChannelRekeyTimeoutCancelsChannel's Closed() assertion for a flake in its
// error-message assertion.
func TestChannelRekeyAwaitPrefersTerminalOutcomeOverCancel(t *testing.T) {
	ch, _, _, _ := pairedRekeyChannel(t)

	waiter := make(chan rekeyOutcome, 1)
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.mu.Unlock()

	// Reproduce the exact state cancelAndResolveRekeyTerminal is in between its
	// claim and its broadcast: waiters claimed, channel already cancelled.
	attempt := ch.rekey.rekeyDone
	claim := ch.claimRekeyResolution(attempt)
	ch.cancel()

	got := make(chan error, 1)
	go func() { got <- ch.awaitRekeyOutcome(waiter, attempt) }()

	select {
	case err := <-got:
		t.Fatalf("await returned %v instead of parking for the claimed resolution", err)
	case <-time.After(100 * time.Millisecond):
	}

	ch.broadcastRekeyResolution(claim, rekeyOutcome{accepted: false, err: errors.New("rekey timeout")})

	select {
	case err := <-got:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rekey timeout",
			"the claimant's cause must win over the cancel it performed")
	case <-time.After(eventWaitLimit):
		t.Fatal("await did not surface the claimed resolution's outcome")
	}
}

func TestChannelRekeyRejectSuppressesAgeOnlyRetry(t *testing.T) {
	ch, _, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)
	ch.rekey.rekeyNotBefore = time.Now().Add(contracts.MinRekeyInterval)

	require.NoError(t, ch.ensureRekeyed(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.Error(t, err, "age-only retry must not send while rekeyNotBefore is in the future")
}

func TestChannelRekeyRejectFallsBackToDefaultBackoff(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	// Legacy peer: empty retry_after_ms → DefaultRejectBackoff.
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, request, false, 0)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not complete")
	}

	remain := time.Until(ch.rekey.rekeyNotBefore)
	assert.Greater(t, remain, 50*time.Second, "fallback must be ~DefaultRejectBackoff")
	assert.LessOrEqual(t, remain, contracts.DefaultRejectBackoff)
}

func TestChannelRekeyRejectHonorsRetryAfter(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	const retryMs int64 = 180_000
	sendRekeyOutcomeWithRetry(t, peer, peerWS, corr, request, false, retryMs)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
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
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(ctx)
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	cancel()
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err, "Ack must complete rekey even after caller ctx cancel")
		assert.False(t, ch.Closed(), "caller ctx cancel must not tear the channel down")
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not return after Ack")
	}
}

func TestChannelRekeyDuplicateAckDoesNotDoubleRotate(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
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
	ct := sealChannelPlaintextForTunnel(t, peer, corr, pt)
	require.NoError(t, channelwire.WriteChannelMessage(ctx, peerWS, ct))

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
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

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
	ct := sealChannelPlaintextForTunnel(t, peer, corr, pt)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS, ct))

	select {
	case err := <-done:
		require.Error(t, err, "bad responder ephemeral must surface a terminal error")
		assert.True(t, ch.Closed(), "bad responder ephemeral must cancel the channel")
	case <-time.After(eventWaitLimit):
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
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
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
	// The whole cost of this test is waiting out testRekeyWatchdog, and it
	// shares no state with anything else -- so it waits alongside its sibling
	// watchdog test rather than after it.
	t.Parallel()

	ch, peer, peerWS, _ := pairedRekeyChannel(t, withRekeyWatchdog(testRekeyWatchdog))
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 2)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	go func() { done <- ch.ensureRekeyed(context.Background()) }()

	// Let the single Request reach the wire and both callers join it.
	corr, _ := awaitRekeyRequest(t, peer, peerWS)
	// Both waiters must be registered before the watchdog resolves the rekey,
	// or the late one finds nothing in flight and starts a SECOND rekey --
	// failing the one-Request assertion below for a reason that has nothing to
	// do with the broadcast under test. Wait for the join rather than trusting
	// the goroutines to have been scheduled by now.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.rekey.rekeyWaiters) == 2
	}, testRekeyWatchdog/2, time.Millisecond, "both callers must join the one in-flight rekey")

	// Send no Ack/Reject — runRekeyTimer fires at the watchdog deadline and
	// resolves terminally. Both callers must observe an error (not nil) and the
	// channel must cancel.
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.Error(t, err, "caller %d must surface the terminal failure", i)
		case <-time.After(30 * time.Second):
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
	ch, _, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyHardCeiling - time.Second)

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
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	ch.rekey.rekeyMu.Lock() // simulate sendInnerContext mid multi-chunk send

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	select {
	case err := <-done:
		t.Fatalf("ensureRekeyed returned while rekeyMu was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Do not Read on peerWS here: a cancelled Read can poison the websocket.

	ch.rekey.rekeyMu.Unlock()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not complete after unlock")
	}
}

func TestChannelRekeyRejectPastHardCeilingCancels(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	// Simulate the key aging past the hard ceiling while waiting for the Reject
	// (e.g. a slow peer). lastRekeyAt is rekeyMu-guarded and read under that
	// lock in awaitRekeyOutcome (which runs on the ensureRekeyed goroutine
	// across the RTT), so take the lock here to avoid a data race.
	ch.rekey.rekeyMu.Lock()
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyHardCeiling)
	ch.rekey.rekeyMu.Unlock()
	sendRekeyOutcome(t, peer, peerWS, corr, request, false)

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hard ceiling")
		assert.True(t, ch.Closed(), "Reject past hard ceiling must cancel the channel")
	case <-time.After(eventWaitLimit):
		t.Fatal("ensureRekeyed did not complete")
	}
}

func TestChannelRekeySoftNonceBypassesRejectBackoff(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now()
	ch.rekey.rekeyNotBefore = time.Now().Add(contracts.MinRekeyInterval)
	ch.session.Send.SetNonceForTest(contracts.SoftNonceLimit + 1)
	peer.Receive.SetNonceForTest(contracts.SoftNonceLimit + 1)

	done := make(chan error, 1)
	go func() {
		done <- ch.ensureRekeyed(context.Background())
	}()

	corr, request := awaitRekeyRequest(t, peer, peerWS)
	sendRekeyOutcome(t, peer, peerWS, corr, request, true)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("soft-nonce rekey did not complete")
	}
	assert.False(t, ch.Closed())
	assert.Equal(t, uint64(0), ch.session.Send.Nonce())
	assert.True(t, ch.rekey.rekeyNotBefore.IsZero(), "successful rekey clears reject backoff")
}

// TestChannelRekeyIgnoresAStaleAck pins the correlation check.
//
// resolveRekey used to be correlation-blind: the recvLoop discarded the inbound
// correlation id, so an Ack for rekey #1 arriving after rekey #2 had registered
// was accepted as #2's answer. It would then claim #2's freshly generated
// material and derive against #1's responder ephemeral -- keys the peer does not
// share -- turning a stale frame into a silently broken channel instead of a
// clean failure.
//
// Asserted through the waiter, because that is what a stale Ack must NOT
// resolve: the live rekey has to stay in flight.
func TestChannelRekeyIgnoresAStaleAck(t *testing.T) {
	ch, _, _, _ := pairedRekeyChannel(t)

	waiter := make(chan rekeyOutcome, 1)
	seed, err := noiseutil.GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	offer, err := noiseutil.OfferRekeyPQ(nil)
	require.NoError(t, err)
	material := &rekeySecrets{ephemeralSeed: seed, pq: offer}
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.rekey.rekeyMaterial = material
	ch.rekey.rekeyReqID = 42
	ch.mu.Unlock()

	// An Ack naming the PREVIOUS request id, carrying a well-formed ephemeral so
	// the only thing that can reject it is the correlation check.
	ch.handleRekeyAck(41, &leapmuxv1.RekeyAck{DhPub: make([]byte, noiseutil.EphemeralPublicKeySize)})

	assert.Empty(t, waiter, "a stale Ack must not resolve the in-flight rekey")
	ch.mu.Lock()
	stillInFlight := ch.rekey.rekeyMaterial
	ch.mu.Unlock()
	assert.Same(t, material, stillInFlight,
		"a stale Ack must not claim the live rekey's material")
	assert.NoError(t, ch.ctx.Err(), "a stale Ack must not cancel the channel")
}

// TestChannelRekeyIgnoresAStaleReject is the Reject half: a stale Reject would
// arm a backoff and drain the waiters of a rekey that is still legitimately in
// flight, stranding its starter until the watchdog fired.
func TestChannelRekeyIgnoresAStaleReject(t *testing.T) {
	ch, _, _, _ := pairedRekeyChannel(t)

	waiter := make(chan rekeyOutcome, 1)
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.rekey.rekeyReqID = 42
	ch.mu.Unlock()

	ch.handleRekeyReject(41, 0)

	assert.Empty(t, waiter, "a stale Reject must not resolve the in-flight rekey")
	assert.True(t, ch.rekey.rekeyNotBefore.IsZero(),
		"a stale Reject must not arm the backoff against a live rekey")
}

func TestChannelResolveTerminalRekeyIsSafeUnderRekeyMu(t *testing.T) {
	ch, _, _, _ := pairedRekeyChannel(t)

	waiter := make(chan rekeyOutcome, 1)
	ch.mu.Lock()
	ch.rekey.rekeyWaiters = []chan rekeyOutcome{waiter}
	ch.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch.rekey.rekeyMu.Lock()
		defer ch.rekey.rekeyMu.Unlock()
		ch.resolveRekey(terminalRekeyOutcome(errors.New("boom")), ch.rekey.rekeyDone)
	}()

	select {
	case <-done:
	case <-time.After(eventWaitLimit):
		t.Fatal("terminal rekey resolution deadlocked while holding rekeyMu")
	}
	require.Len(t, waiter, 1, "the terminal resolution must still wake the waiters")
	outcome := <-waiter
	assert.False(t, outcome.accepted)
	require.Error(t, outcome.err)
}

// TestRekeyWatchdogTimeout_ZeroMeansTheProductionBudget pins the default arm of
// the watchdog seam.
//
// Only the two tests that need the watchdog to fire set watchdogTimeout, so
// every other channel -- including every channel in production -- runs on the
// zero value. If that stopped resolving to sessionVerifyTimeout, a real rekey
// would be given whatever the fallback drifted to, and no test that sets the
// field would notice.
func TestRekeyWatchdogTimeout_ZeroMeansTheProductionBudget(t *testing.T) {
	t.Parallel()

	var ch Channel
	assert.Equal(t, sessionVerifyTimeout, ch.rekeyWatchdogTimeout(),
		"an unset watchdog must fall back to the open-time session budget")

	ch.rekey.watchdogTimeout = 250 * time.Millisecond
	assert.Equal(t, 250*time.Millisecond, ch.rekeyWatchdogTimeout(),
		"a configured watchdog must be honoured over the fallback")
}

func TestChannelRekeyPostQuantumRoundTrip(t *testing.T) {
	ch, peer, peerWS, workerKey := pairedRekeyChannel(t, withPostQuantumRekey())
	conn := newOpenedConn(ch, "conn-pq-rekey", "example.test", 443)
	t.Cleanup(func() { _ = conn.Close() })
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)
	done := make(chan error, 1)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	correlationID, request := awaitRekeyRequest(t, peer, peerWS)
	require.NotEmpty(t, request.GetMlkemStaticCt())
	require.NotEmpty(t, request.GetMlkemEphemeralPub())
	sendRekeyOutcomeWithKey(t, peer, peerWS, workerKey, correlationID, request, true, 0)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("the post-quantum rekey did not complete")
	}

	_, err := conn.Write([]byte("after-pq-rekey"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), eventWaitLimit)
	defer cancel()
	frame, err := channelwire.ReadChannelMessage(ctx, peerWS)
	require.NoError(t, err)
	_, err = channelwire.OpenChannelMessage(peer, frame)
	require.NoError(t, err, "the Worker must decrypt under the new receive key")

	response := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.mu.Lock()
	ch.pending[900] = response
	ch.mu.Unlock()
	plaintext, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Response{
		Response: channelwire.NewPayloadResponse([]byte("after")),
	}})
	require.NoError(t, err)
	responseFrame := sealChannelPlaintextForTunnel(t, peer, 900, plaintext)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS, responseFrame))
	select {
	case answer := <-response:
		assert.Equal(t, []byte("after"), answer.GetPayload())
	case <-time.After(eventWaitLimit):
		t.Fatal("the initiator did not decrypt the post-rekey response")
	}
}

func TestChannelRekeyPostQuantumAckWithoutCiphertextCancels(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t, withPostQuantumRekey())
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)
	done := make(chan error, 1)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	correlationID, _ := awaitRekeyRequest(t, peer, peerWS)
	seed, err := noiseutil.GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	defer noiseutil.Zero(seed)
	responderPub, err := noiseutil.X25519PublicFromSeed(seed)
	require.NoError(t, err)
	plaintext, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_RekeyAck{
		RekeyAck: &leapmuxv1.RekeyAck{DhPub: responderPub},
	}})
	require.NoError(t, err)
	frame := sealChannelPlaintextForTunnel(t, peer, correlationID, plaintext)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS, frame))
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("the malformed Ack did not resolve the rekey")
	}
	assert.True(t, ch.Closed())
}

func TestChannelRekeyClassicAckWithCiphertextCancels(t *testing.T) {
	ch, peer, peerWS, _ := pairedRekeyChannel(t)
	ch.rekey.lastRekeyAt = time.Now().Add(-contracts.SessionKeyMaxAge - time.Second)
	done := make(chan error, 1)
	go func() { done <- ch.ensureRekeyed(context.Background()) }()
	correlationID, _ := awaitRekeyRequest(t, peer, peerWS)
	seed, err := noiseutil.GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	defer noiseutil.Zero(seed)
	responderPub, err := noiseutil.X25519PublicFromSeed(seed)
	require.NoError(t, err)
	plaintext, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_RekeyAck{
		RekeyAck: &leapmuxv1.RekeyAck{DhPub: responderPub, MlkemEphemeralCt: []byte{1}},
	}})
	require.NoError(t, err)
	frame := sealChannelPlaintextForTunnel(t, peer, correlationID, plaintext)
	require.NoError(t, channelwire.WriteChannelMessage(context.Background(), peerWS, frame))
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(eventWaitLimit):
		t.Fatal("the classic Ack with ML-KEM material did not resolve the rekey")
	}
	assert.True(t, ch.Closed())
}
