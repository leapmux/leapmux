package channel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// collectSender collects all ConnectRequest messages sent by the channel manager.
type collectSender struct {
	mu   sync.Mutex
	cond *sync.Cond
	msgs []*leapmuxv1.ConnectRequest
}

func newCollectSender() *collectSender {
	cs := &collectSender{}
	cs.cond = sync.NewCond(&cs.mu)
	return cs
}

func (c *collectSender) send(msg *leapmuxv1.ConnectRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	c.cond.Broadcast()
	return nil
}

// trySend is the non-blocking twin of send for Manager.trySendFn.
func (c *collectSender) trySend(msg *leapmuxv1.ConnectRequest) bool {
	_ = c.send(msg)
	return true
}

func (c *collectSender) messages() []*leapmuxv1.ConnectRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*leapmuxv1.ConnectRequest(nil), c.msgs...)
}

// waitForMessages blocks until at least n messages have been collected.
func (c *collectSender) waitForMessages(n int) []*leapmuxv1.ConnectRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.msgs) < n {
		c.cond.Wait()
	}
	return append([]*leapmuxv1.ConnectRequest(nil), c.msgs...)
}

func setupTestManager(t *testing.T) (*Manager, *noiseutil.CompositeKeypair, *collectSender) {
	return setupTestManagerWith(t, 0, 0)
}

func setupTestManagerWith(t *testing.T, maxMessageSize, maxIncompleteChunked int) (*Manager, *noiseutil.CompositeKeypair, *collectSender) {
	t.Helper()
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	sender := newCollectSender()
	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, maxIncompleteChunked)
	// The message-size ceiling is no longer a NewManager parameter -- it is a fixed
	// protocol constant in production (channelwire.DefaultMaxMessageSize). Tests that
	// exercise the size cap override the field directly, before any session is
	// created, so both the sender and the reassembler pick it up.
	if maxMessageSize > 0 {
		mgr.maxMessageSize = maxMessageSize
	}
	return mgr, ck, sender
}

// performHandshake runs a full hybrid Noise_NK handshake between initiator and responder (Manager).
func performHandshake(t *testing.T, mgr *Manager, ck *noiseutil.CompositeKeypair, channelID, userID string) *noiseutil.Session {
	t.Helper()

	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	// Initiator creates msg1.
	hs, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Responder (Manager) handles open.
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           userID,
		HandshakePayload: msg1,
	})
	require.Empty(t, resp.GetError(), "handshake should succeed")
	require.NotEmpty(t, resp.GetHandshakePayload())
	assert.Equal(t, channelID, resp.GetChannelId())

	// Initiator completes handshake.
	initiatorSession, err := noiseutil.InitiatorHandshake2(hs, resp.GetHandshakePayload(), slhdsaPub)
	require.NoError(t, err)

	return initiatorSession
}

// sendRequest sends an InnerRpcRequest wrapped in InnerMessage via the encrypted channel.
func sendRequest(t *testing.T, initiatorSession *noiseutil.Session, channelID string, req *leapmuxv1.InnerRpcRequest) []byte {
	t.Helper()

	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{Request: req},
	}
	plaintext, err := proto.Marshal(envelope)
	require.NoError(t, err)

	ciphertext, err := initiatorSession.Encrypt(plaintext)
	require.NoError(t, err)

	return ciphertext
}

// decryptInnerResponse decrypts and unwraps a response from the sender.
func decryptInnerResponse(t *testing.T, initiatorSession *noiseutil.Session, msg *leapmuxv1.ConnectRequest) *leapmuxv1.InnerRpcResponse {
	t.Helper()

	chMsg := msg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	require.Equal(t, uint32(1), chMsg.GetProtocolVersion())

	respPlaintext, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPlaintext, &envelope))

	resp, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())

	return resp.Response
}

func TestHandleOpen_Success(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)

	session := performHandshake(t, mgr, kp, "ch-1", "user-1")
	require.NotNil(t, session)

	// Verify the session was registered.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-1"]
	mgr.mu.RUnlock()
	assert.True(t, exists)
}

func TestAddAccessibleWorkspaceID(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)

	_ = performHandshake(t, mgr, kp, "ch-aws", "user-1")

	// Initially the channel has no accessible workspaces.
	ids := mgr.AccessibleWorkspaceIDs("ch-aws")
	assert.Empty(t, ids)

	// Add a workspace ID dynamically.
	mgr.AddAccessibleWorkspaceID("ch-aws", "ws-1")
	ids = mgr.AccessibleWorkspaceIDs("ch-aws")
	assert.True(t, ids["ws-1"])

	// Adding the same ID again is a no-op.
	mgr.AddAccessibleWorkspaceID("ch-aws", "ws-1")
	assert.True(t, ids["ws-1"])

	// Adding to an unknown channel is a no-op (no panic).
	mgr.AddAccessibleWorkspaceID("ch-unknown", "ws-2")
}

// TestIsWorkspaceAccessible pins the per-RPC membership check the access gates
// call on every workspace-scoped request: it must answer from the channel's
// accessible set with a single lookup (no whole-set copy) and fail closed for
// an unknown channel.
func TestIsWorkspaceAccessible(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	_ = performHandshake(t, mgr, kp, "ch-aws", "user-1")

	mgr.AddAccessibleWorkspaceID("ch-aws", "ws-1")

	assert.True(t, mgr.IsWorkspaceAccessible("ch-aws", "ws-1"),
		"an accessible workspace must be reported accessible")
	assert.False(t, mgr.IsWorkspaceAccessible("ch-aws", "ws-other"),
		"a workspace not in the set must be reported inaccessible")
	assert.False(t, mgr.IsWorkspaceAccessible("ch-aws", ""),
		"an empty workspace id must not match an empty key")
	assert.False(t, mgr.IsWorkspaceAccessible("ch-unknown", "ws-1"),
		"an unknown channel must fail closed")
}

// TestAccessibleWorkspaceIDs_ConcurrentAddAndRead pins the locking
// contract of AddAccessibleWorkspaceID + AccessibleWorkspaceIDs: the
// inner map is now guarded by sess.awsMu, so a hot loop of writes from
// one goroutine and reads from another must complete without
// `fatal error: concurrent map writes` (or read/write).
//
// Returns a defensive copy from AccessibleWorkspaceIDs so callers can
// iterate without holding the manager-level lock. Without that copy the
// previous implementation handed callers a live map reference and a
// subsequent AddAccessibleWorkspaceID write raced their next read.
//
// Best run under `go test -race`. The previous unsynchronised code
// panics with -race on this test in seconds.
func TestAccessibleWorkspaceIDs_ConcurrentAddAndRead(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	_ = performHandshake(t, mgr, kp, "ch-race", "user-1")

	const iter = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iter; i++ {
			mgr.AddAccessibleWorkspaceID("ch-race", "ws")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iter; i++ {
			snap := mgr.AccessibleWorkspaceIDs("ch-race")
			// Touch every key; under the old behaviour the live map
			// reference was being mutated by the other goroutine here.
			for range snap {
			}
		}
	}()
	wg.Wait()
}

func TestHandleOpen_DuplicateChannelIdRejected(t *testing.T) {
	// A second ChannelOpen for an already-active channel id is rejected:
	// the prior session stays intact (ctx live, registered in the map)
	// and the close callback does NOT fire. The previous defensive
	// swap-then-cancel design had two unsafe windows — an in-flight
	// OLD handler could encrypt+send a final response with OLD noise
	// state after NEW was installed (frontend decodes with NEW's state
	// → garbage), and closeCallback(channelID) could unregister
	// subscriptions already attached to NEW. Rejecting the re-open
	// leaves OLD in charge and surfaces the hub bug as an error
	// response.
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	sender := newCollectSender()
	var closeCallbackCount int
	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, 0)
	mgr.SetOnChannelClose(func(id string) {
		_ = id
		closeCallbackCount++
	})

	_ = performHandshake(t, mgr, ck, "ch-reuse", "user-1")

	mgr.mu.RLock()
	first := mgr.sessions["ch-reuse"]
	mgr.mu.RUnlock()
	require.NotNil(t, first)
	firstCtx := first.ctx
	require.NoError(t, firstCtx.Err(), "fresh session ctx must not be cancelled yet")

	// Re-open with the same channel id. Build a valid handshake payload
	// so the rejection happens at the session-table check, not at the
	// handshake layer (a bad payload would short-circuit earlier).
	_, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-reuse",
		UserId:           "user-1",
		HandshakePayload: msg1,
	})
	assert.NotEmpty(t, resp.GetError(), "re-open must return an error response")
	assert.Contains(t, resp.GetError(), "already active")

	// Prior session is intact: ctx still live, still in the map, and the
	// close callback did NOT fire (rejection doesn't tear down OLD).
	require.NoError(t, firstCtx.Err(), "prior session ctx must remain live after a rejected re-open")
	assert.Equal(t, 0, closeCallbackCount, "close callback must not fire on a rejected re-open")

	mgr.mu.RLock()
	current := mgr.sessions["ch-reuse"]
	mgr.mu.RUnlock()
	assert.Same(t, first, current, "the original session must still be the one registered for ch-reuse")
}

// The Hub establishes channel identity and names it in the open request, so an
// empty user id means the Hub failed to say who the caller is -- not that the
// caller is anonymous. Installing the session anyway would run every
// workspace-scoped family (agent, terminal, tab moves, cleanup) as nobody: those
// gate on the Hub-supplied accessible-workspace set and never look at the user id,
// so unlike the machine-scoped families -- which requireWorkerOwner fails closed on
// for exactly this input -- an empty id is not self-limiting there.
func TestHandleOpen_RefusesEmptyUserID(t *testing.T) {
	mgr, ck, _ := setupTestManager(t)

	// A VALID handshake payload, so the rejection is provably the identity check
	// and not the handshake layer short-circuiting earlier.
	_, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)

	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-anon",
		UserId:           "",
		HandshakePayload: msg1,
	})
	assert.NotEmpty(t, resp.GetError(), "an open naming no user must be refused")
	assert.Contains(t, resp.GetError(), "no authenticated user id")
	assert.Equal(t, "ch-anon", resp.GetChannelId())

	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-anon"]
	mgr.mu.RUnlock()
	assert.False(t, exists, "a refused open must not install a session")
}

func TestHandleOpen_BadHandshake(t *testing.T) {
	mgr, _, _ := setupTestManager(t)

	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-bad",
		UserId:           "user-1",
		HandshakePayload: []byte("not a valid handshake message"),
	})
	assert.NotEmpty(t, resp.GetError())
	assert.Equal(t, "ch-bad", resp.GetChannelId())

	// Session should not have been registered.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-bad"]
	mgr.mu.RUnlock()
	assert.False(t, exists)
}

func TestHandleMessage_DispatchAndResponse(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	// Set up a dispatcher with a test handler.
	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(_ context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: req.GetPayload(),
		})
	})
	mgr.SetDispatcher(dispatcher)

	// Open a channel and run the Noise handshake.
	initiatorSession := performHandshake(t, mgr, kp, "ch-echo", "user-echo")

	// Create an inner RPC request wrapped in InnerMessage.
	ct := sendRequest(t, initiatorSession, "ch-echo", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("hello"),
	})

	// Send the encrypted message.
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-echo",
		Ciphertext:      ct,
		CorrelationId:   42,
	})

	// Wait for the async handler to send the response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)

	innerResp := decryptInnerResponse(t, initiatorSession, msgs[len(msgs)-1])
	assert.Equal(t, uint64(42), msgs[len(msgs)-1].GetChannelMessageResp().GetCorrelationId())
	assert.Equal(t, []byte("hello"), innerResp.GetPayload())
	assert.False(t, innerResp.GetIsError())
}

// A frame whose flags value is one no conformant sender emits (e.g. MORE|CLOSE
// combined) is a protocol violation: it must be dropped -- NOT read as a final
// chunk and delivered truncated -- and the drop must happen after the decrypt,
// so the receive nonce stays in step with the peer and the session survives.
func TestHandleMessage_OutOfSpecFlagsDroppedWithoutNonceDesync(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(_ context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: req.GetPayload()})
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-flags", "user-flags")

	// Frame 1: a well-formed request under an out-of-spec flags value. It must
	// be dropped without any response.
	ct1 := sendRequest(t, initiatorSession, "ch-flags", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("dropped"),
	})
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-flags",
		Ciphertext:      ct1,
		CorrelationId:   41,
		Flags:           leapmuxv1.ChannelMessageFlags(3), // MORE|CLOSE combined
	})
	assert.Len(t, sender.messages(), msgsBefore, "an out-of-spec frame must produce no response")

	// Frame 2: the NEXT ciphertext from the same initiator session still
	// decrypts and dispatches -- proving the dropped frame advanced the
	// receive nonce (a drop before the decrypt would desync every later frame).
	ct2 := sendRequest(t, initiatorSession, "ch-flags", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("delivered"),
	})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-flags",
		Ciphertext:      ct2,
		CorrelationId:   42,
	})
	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)
	innerResp := decryptInnerResponse(t, initiatorSession, msgs[len(msgs)-1])
	assert.Equal(t, []byte("delivered"), innerResp.GetPayload())
}

func TestHandleMessage_UnknownChannel(t *testing.T) {
	mgr, _, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	// Sending to a non-existent channel should not panic.
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "nonexistent",
		Ciphertext:      []byte("garbage"),
	})
}

func TestHandleMessage_NoDispatcher(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)
	// Intentionally do NOT set a dispatcher.

	initiatorSession := performHandshake(t, mgr, kp, "ch-no-disp", "user-1")

	ct := sendRequest(t, initiatorSession, "ch-no-disp", &leapmuxv1.InnerRpcRequest{
		Method: "anything",
	})

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-no-disp",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Wait for async UNIMPLEMENTED error response. It is the session's FIRST
	// message: with the identity claim gone, a request needs no preceding exchange.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)

	innerResp := decryptInnerResponse(t, initiatorSession, msgs[0])
	assert.True(t, innerResp.GetIsError())
	assert.Equal(t, int32(12), innerResp.GetErrorCode()) // UNIMPLEMENTED
}

// A request must be served immediately after the handshake, with no in-channel
// identity claim, and must carry the identity the HUB supplied at open.
//
// The claim exchange this replaces could only restate what the Hub already told
// both ends (the Worker reads it from ChannelOpenRequest.user_id), and Noise_NK leaves
// the initiator unauthenticated, so the claim was an unsigned string the Worker
// "verified" against the Hub's own value. Deleting it must not weaken the
// identity the dispatcher sees, nor gate the first request behind a round trip.
func TestHandleMessage_RequestNeedsNoIdentityClaim(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	gotUserID := make(chan userid.UserID, 1)
	dispatcher := NewDispatcher()
	dispatcher.Register("whoami", func(_ context.Context, userID userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		gotUserID <- userID
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: []byte("ok")})
	})
	mgr.SetDispatcher(dispatcher)

	// No claim: the very first encrypted message is a request.
	session := performHandshake(t, mgr, kp, "ch-noclaim", "hub-user")
	ct := sendRequest(t, session, "ch-noclaim", &leapmuxv1.InnerRpcRequest{Method: "whoami"})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-noclaim",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	select {
	case userID := <-gotUserID:
		assert.Equal(t, "hub-user", userID.String(), "the dispatcher sees the Hub-supplied identity")
	case <-time.After(2 * time.Second):
		t.Fatal("the first request after the handshake was not dispatched")
	}

	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1, "the request is answered, not gated behind a claim")
	assert.False(t, decryptInnerResponse(t, session, msgs[0]).GetIsError())
}

// A correlation id ABOVE 2^32 must survive the round trip intact.
//
// This is the whole point of correlation_id being uint64 (see channel.proto): the id
// space cannot wrap, so a live registration can never be collided onto by a counter
// coming around again. Everything else about the change is invisible in-memory -- a
// uint32 wire field would still let the allocator hand out big ids and still let the
// maps key on them; the truncation would happen silently at marshal, and the response
// would come back correlated to the WRONG request. So pin the wire, not the map.
func TestHandleMessage_CorrelationIdSurvivesAbove32Bits(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: req.GetPayload()})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-wide", "user-1")
	ct := sendRequest(t, session, "ch-wide", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("hello"),
	})

	// Past uint32: a 32-bit field would truncate this to 7 and correlate the reply to
	// whatever request holds id 7.
	const wideID uint64 = 1<<40 | 7
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-wide",
		Ciphertext:      ct,
		CorrelationId:   wideID,
	})

	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)
	assert.Equal(t, wideID, msgs[len(msgs)-1].GetChannelMessageResp().GetCorrelationId(),
		"the reply must carry the id it was sent with, not a truncated one")
	assert.Equal(t, []byte("hello"), decryptInnerResponse(t, session, msgs[len(msgs)-1]).GetPayload())
}

func TestHandleClose(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	performHandshake(t, mgr, kp, "ch-close", "user-1")

	// Verify session exists.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-close"]
	mgr.mu.RUnlock()
	require.True(t, exists)

	// Close the channel.
	mgr.HandleClose("ch-close")

	// Verify session was removed.
	mgr.mu.RLock()
	_, exists = mgr.sessions["ch-close"]
	mgr.mu.RUnlock()
	assert.False(t, exists)
}

func TestCloseAll(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	performHandshake(t, mgr, kp, "ch-1", "user-1")
	performHandshake(t, mgr, kp, "ch-2", "user-2")

	mgr.mu.RLock()
	count := len(mgr.sessions)
	mgr.mu.RUnlock()
	require.Equal(t, 2, count)

	mgr.CloseAll()

	mgr.mu.RLock()
	count = len(mgr.sessions)
	mgr.mu.RUnlock()
	assert.Equal(t, 0, count)
}

// TestHandleClose_CancelsSessionCtx pins the session-ctx → handler-ctx
// chain that the entire dispatcher refactor was built to enable. The
// session ctx is what HandleMessage hands to the dispatcher; cancelling
// it via HandleClose must reach the ctx that any still-running handler
// holds — otherwise a `git push` started moments before the user closed
// the dialog would keep running until pushBranchTimeout.
func TestHandleClose_CancelsSessionCtx(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	performHandshake(t, mgr, kp, "ch-close-ctx", "user-1")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-close-ctx"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	require.NotNil(t, sess.ctx)
	require.NoError(t, sess.ctx.Err(), "ctx must be live before close")

	mgr.HandleClose("ch-close-ctx")

	// HandleClose drops the session from the map AND cancels the ctx.
	// Capture the ctx before close so we can still observe it post-close.
	require.ErrorIs(t, sess.ctx.Err(), context.Canceled, "session ctx must be cancelled by HandleClose")
}

// TestCloseAll_CancelsEverySessionCtx covers the bulk-shutdown path
// (worker reconnect / process exit). The snapshot-then-cancel pattern
// in CloseAll runs the cancels outside the manager lock, so this test
// also acts as a regression guard against a future refactor moving
// the cancel back inside the lock and re-introducing the cleanup-
// path lock-cycle the fix was designed to prevent.
func TestCloseAll_CancelsEverySessionCtx(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	performHandshake(t, mgr, kp, "ch-a", "user-1")
	performHandshake(t, mgr, kp, "ch-b", "user-2")

	mgr.mu.RLock()
	a := mgr.sessions["ch-a"]
	b := mgr.sessions["ch-b"]
	mgr.mu.RUnlock()
	require.NotNil(t, a)
	require.NotNil(t, b)

	mgr.CloseAll()

	require.ErrorIs(t, a.ctx.Err(), context.Canceled)
	require.ErrorIs(t, b.ctx.Err(), context.Canceled)
}

// TestHandleMessage_DispatchesUnderSessionCtx is the end-to-end version
// of the dispatcher ctx-propagation tests: HandleMessage must hand the
// session-scoped ctx to the dispatched handler so an in-flight handler
// observes HandleClose's cancel. The TestHandleClose_CancelsSessionCtx
// covers the session bookkeeping; this one verifies the handler-visible
// ctx is the same ctx that gets cancelled.
func TestHandleMessage_DispatchesUnderSessionCtx(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)

	gotCtxC := make(chan context.Context, 1)
	dispatcher := NewDispatcher()
	dispatcher.Register("inspect", func(ctx context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		gotCtxC <- ctx
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-inspect", "user-1")

	ct := sendRequest(t, initiatorSession, "ch-inspect", &leapmuxv1.InnerRpcRequest{Method: "inspect"})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-inspect",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Wait for the handler-supplied ctx with a generous deadline so the
	// dispatcher's `go` doesn't race the assertion.
	var handlerCtx context.Context
	select {
	case handlerCtx = <-gotCtxC:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not invoke handler within 2s")
	}
	require.NoError(t, handlerCtx.Err(), "handler ctx must be live before HandleClose")

	mgr.HandleClose("ch-inspect")
	require.ErrorIs(t, handlerCtx.Err(), context.Canceled, "handler-visible ctx must cancel when the channel closes")
}

// TestHandleMessage_NonBlocking verifies that HandleMessage returns promptly
// even when the underlying sendFn is blocked (e.g., bidi stream full).
// Before the fix (async sends in receive loop), a blocked sendFn would hold
// the send mutex and block the receive loop, causing a deadlock cascade.
func TestHandleMessage_NonBlocking(t *testing.T) {
	// Create a sendFn that blocks until explicitly released.
	blockCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{})

	sender := newCollectSender()
	blockingSend := func(msg *leapmuxv1.ConnectRequest) error {
		// Signal that we've entered the send function.
		select {
		case blockCh <- struct{}{}:
		default:
		}
		// Block until released.
		<-releaseCh
		return sender.send(msg)
	}

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, blockingSend, nil, 0)

	// Set up a dispatcher with a handler that sends a response.
	dispatcher := NewDispatcher()
	dispatcher.Register("slow", func(_ context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("done"),
		})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, ck, "ch-block", "user-1")

	// Occupy the sender: dispatch a first request whose handler's response send
	// blocks in blockingSend. Sends are async, so HandleMessage returns and the
	// response send stays parked in its own goroutine.
	occupy := sendRequest(t, session, "ch-block", &leapmuxv1.InnerRpcRequest{
		Method: "slow",
	})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-block",
		Ciphertext:      occupy,
		CorrelationId:   1,
	})

	// Wait for that send to enter the blocked state.
	<-blockCh

	// Now send a second request. The critical test: HandleMessage must return
	// promptly even though sendFn is currently blocked by the first response.
	ct := sendRequest(t, session, "ch-block", &leapmuxv1.InnerRpcRequest{
		Method: "slow",
	})

	done := make(chan struct{})
	go func() {
		mgr.HandleMessage(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       "ch-block",
			Ciphertext:      ct,
		})
		close(done)
	}()

	// HandleMessage should return quickly (dispatch is async).
	// If it blocks for more than 1 second, the deadlock fix is broken.
	select {
	case <-done:
		// Good — HandleMessage returned promptly.
	case <-time.After(time.Second):
		t.Fatal("HandleMessage blocked — receive loop deadlock not fixed")
	}

	// Release the blocking sends so goroutines can complete.
	close(releaseCh)
}

func TestMultipleChannels(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Register("ping", func(_ context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("pong-" + userID.String()),
		})
	})
	mgr.SetDispatcher(dispatcher)

	session1 := performHandshake(t, mgr, kp, "ch-a", "alice")
	session2 := performHandshake(t, mgr, kp, "ch-b", "bob")

	// Send on ch-a.
	ct1 := sendRequest(t, session1, "ch-a", &leapmuxv1.InnerRpcRequest{Method: "ping"})
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: "ch-a", Ciphertext: ct1, CorrelationId: 1})

	// Send on ch-b.
	ct2 := sendRequest(t, session2, "ch-b", &leapmuxv1.InnerRpcRequest{Method: "ping"})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: "ch-b", Ciphertext: ct2, CorrelationId: 2})

	// Wait for both async handlers to complete.
	msgs := sender.waitForMessages(msgsBefore + 2)
	require.Equal(t, msgsBefore+2, len(msgs))

	// Responses may arrive in either order due to async dispatch.
	// Collect both and match by channel.
	var resp1, resp2 *leapmuxv1.InnerRpcResponse
	for _, msg := range msgs[msgsBefore:] {
		chMsg := msg.GetChannelMessageResp()
		require.NotNil(t, chMsg)
		switch chMsg.GetChannelId() {
		case "ch-a":
			resp1 = decryptInnerResponse(t, session1, msg)
		case "ch-b":
			resp2 = decryptInnerResponse(t, session2, msg)
		}
	}
	require.NotNil(t, resp1, "expected response on ch-a")
	require.NotNil(t, resp2, "expected response on ch-b")
	assert.Equal(t, []byte("pong-alice"), resp1.GetPayload())
	assert.Equal(t, []byte("pong-bob"), resp2.GetPayload())
}

// decryptChannelMessage decrypts a ConnectRequest's ChannelMessage ciphertext.
func decryptChannelMessage(t *testing.T, session *noiseutil.Session, msg *leapmuxv1.ConnectRequest) ([]byte, *leapmuxv1.ChannelMessage) {
	t.Helper()
	chMsg := msg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	pt, err := session.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)
	return pt, chMsg
}

func TestSendEncrypted_SingleChunk(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: req.GetPayload()})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-single", "user-1")

	// Send a small request.
	ct := sendRequest(t, session, "ch-single", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("small"),
	})
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-single",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]
	chMsg := lastMsg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	// Single-chunk response should have Flags=UNSPECIFIED.
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())
}

func TestSendEncrypted_MultiChunk(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	// Create a handler that sends a large payload.
	largePayload := make([]byte, channelwire.MaxPlaintextPerChunk+100)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	dispatcher := NewDispatcher()
	dispatcher.Register("big", func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: largePayload})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-multi", "user-1")

	ct := sendRequest(t, session, "ch-multi", &leapmuxv1.InnerRpcRequest{Method: "big"})
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-multi",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Should get at least 2 messages (chunked).
	msgs := sender.waitForMessages(msgsBefore + 2)
	chunkMsgs := msgs[msgsBefore:]

	// First chunk(s) should have Flags=MORE, last should have UNSPECIFIED.
	for i, m := range chunkMsgs {
		chMsg := m.GetChannelMessageResp()
		require.NotNil(t, chMsg, "chunk %d: expected ChannelMessage", i)
		if i < len(chunkMsgs)-1 {
			assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chMsg.GetFlags(),
				"chunk %d: expected MORE flag", i)
		} else {
			assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags(),
				"last chunk: expected UNSPECIFIED flag")
		}
		assert.Equal(t, uint64(1), chMsg.GetCorrelationId())
	}

	// Decrypt and concatenate all chunks.
	var fullPlaintext []byte
	for _, m := range chunkMsgs {
		pt, _ := decryptChannelMessage(t, session, m)
		fullPlaintext = append(fullPlaintext, pt...)
	}

	// Unmarshal the full plaintext as InnerMessage.
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(fullPlaintext, &envelope))
	resp := envelope.GetKind().(*leapmuxv1.InnerMessage_Response).Response
	assert.Equal(t, largePayload, resp.GetPayload())
}

func TestSendEncrypted_ExactBoundary(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-exact", "user-1")

	// Find the exact payload size that makes the marshaled InnerMessage fit
	// in exactly one chunk, by trial: marshal a response with a given payload
	// size and check the total length.
	findPayloadSize := func(target int) int {
		for size := target; size > 0; size-- {
			env := &leapmuxv1.InnerMessage{
				Kind: &leapmuxv1.InnerMessage_Response{Response: &leapmuxv1.InnerRpcResponse{
					Payload: make([]byte, size),
				}},
			}
			data, _ := proto.Marshal(env)
			if len(data) == target {
				return size
			}
			if len(data) < target {
				return size
			}
		}
		return 0
	}

	// Payload that fits in exactly 1 chunk.
	exactPayloadSize := findPayloadSize(channelwire.MaxPlaintextPerChunk)
	require.Greater(t, exactPayloadSize, 0)

	dispatcher.Register("exact", func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: make([]byte, exactPayloadSize)})
	})

	ct := sendRequest(t, session, "ch-exact", &leapmuxv1.InnerRpcRequest{Method: "exact"})
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-exact",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Should fit in 1 chunk.
	msgs := sender.waitForMessages(msgsBefore + 1)
	assert.Equal(t, msgsBefore+1, len(msgs))
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())

	// Now add 1 more byte to the payload, which should cause it to overflow.
	dispatcher.Register("overflow", func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: make([]byte, exactPayloadSize+1)})
	})

	ct2 := sendRequest(t, session, "ch-exact", &leapmuxv1.InnerRpcRequest{Method: "overflow"})
	msgsBefore = len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-exact",
		Ciphertext:      ct2,
		CorrelationId:   2,
	})

	// Should produce exactly 2 chunks.
	msgs = sender.waitForMessages(msgsBefore + 2)
	assert.Equal(t, msgsBefore+2, len(msgs))
}

func TestReassembly_E2E(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	var receivedPayload []byte
	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		receivedPayload = req.GetPayload()
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: []byte("ok")})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-reasm", "user-1")

	// Build a large InnerMessage, then chunk it manually on the initiator side.
	largePayload := make([]byte, channelwire.MaxPlaintextPerChunk*2+100)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{Request: &leapmuxv1.InnerRpcRequest{
			Method:  "echo",
			Payload: largePayload,
		}},
	}
	data, err := proto.Marshal(envelope)
	require.NoError(t, err)

	// Snapshot the message count before sending chunks. The dispatcher
	// runs in a goroutine, so the response may land in the sender buffer
	// before we reach waitForMessages — capturing the count after the
	// loop would race with the response and target an unreachable count.
	msgsBefore := len(sender.messages())

	// Send chunks manually.
	for offset := 0; offset < len(data); {
		end := offset + channelwire.MaxPlaintextPerChunk
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		offset = end

		ct, encErr := session.Encrypt(chunk)
		require.NoError(t, encErr)

		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if offset < len(data) {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}

		mgr.HandleMessage(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       "ch-reasm",
			Ciphertext:      ct,
			CorrelationId:   42,
			Flags:           flags,
		})
	}

	// Wait for the handler response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	_ = msgs // Just ensuring the handler was called.

	// The handler should have received the full payload.
	assert.Equal(t, largePayload, receivedPayload)
}

func TestReassembly_MaxSizeExceeded(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxsize", "user-1")

	// Send chunks that exceed max size.
	chunk1 := make([]byte, 60)
	ct1, err := session.Encrypt(chunk1)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxsize",
		Ciphertext:      ct1,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	chunk2 := make([]byte, 60)
	ct2, err := session.Encrypt(chunk2)
	require.NoError(t, err)

	// This should be dropped (total 120 > 100 max).
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxsize",
		Ciphertext:      ct2,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Subsequent messages on other correlation IDs should still work: the oversize
	// drop errors only its own request, never the shared channel.
	before := len(sender.messages())
	ct3 := sendRequest(t, session, "ch-maxsize", &leapmuxv1.InnerRpcRequest{Method: "anything"})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxsize",
		Ciphertext:      ct3,
		CorrelationId:   2,
	})

	// An UNIMPLEMENTED error comes back, proving the channel is still functional.
	msgs := sender.waitForMessages(before + 1)
	require.NotEmpty(t, msgs)
}

func TestReassembly_MaxIncompleteExceeded(t *testing.T) {
	mgr, kp, _ := setupTestManagerWith(t, 0, 2)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxinc", "user-1")

	// Start 2 chunked sequences.
	for i := uint64(1); i <= 2; i++ {
		chunk := make([]byte, 10)
		ct, err := session.Encrypt(chunk)
		require.NoError(t, err)

		mgr.HandleMessage(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       "ch-maxinc",
			Ciphertext:      ct,
			CorrelationId:   i,
			Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
		})
	}

	// 3rd chunked sequence should be silently dropped.
	chunk := make([]byte, 10)
	ct, err := session.Encrypt(chunk)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxinc",
		Ciphertext:      ct,
		CorrelationId:   3,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Verify the session still works by completing one of the sequences.
	finalChunk := make([]byte, 10)
	ctFinal, err := session.Encrypt(finalChunk)
	require.NoError(t, err)

	// This completes correlation 1, freeing a slot. It won't unmarshal cleanly
	// but proves the reassembly path isn't stuck.
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxinc",
		Ciphertext:      ctFinal,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED,
	})
}

func TestSendEncrypted_MaxMessageSizeExceeded(t *testing.T) {
	mgr, kp, _ := setupTestManagerWith(t, 100, 0) // Very small limit.

	// Register a handler that tries to send a response larger than the limit.
	dispatcher := NewDispatcher()
	dispatcher.Register("big", func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		err := s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: make([]byte, 200)})
		// sendEncrypted should return an error for oversized messages.
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrMessageRejected,
			"the size cap is a per-message rejection, not a dead transport")
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-sendmax", "user-1")

	ct := sendRequest(t, session, "ch-sendmax", &leapmuxv1.InnerRpcRequest{Method: "big"})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-sendmax",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Give async dispatch time to complete.
	require.Eventually(t, func() bool {
		// No response should be sent since the message was too large to send.
		// The handler above already verified the error was returned.
		return true
	}, time.Second, 10*time.Millisecond)
}

func TestReassembly_FinalChunkExceedsMaxSize(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-finalover", "user-1")

	// Send a first chunk within limits (60 bytes).
	chunk1 := make([]byte, 60)
	ct1, err := session.Encrypt(chunk1)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-finalover",
		Ciphertext:      ct1,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Send a final chunk that pushes total over the limit (60 + 60 = 120 > 100).
	chunk2 := make([]byte, 60)
	ct2, err := session.Encrypt(chunk2)
	require.NoError(t, err)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-finalover",
		Ciphertext:      ct2,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED,
	})

	// Should get a RESOURCE_EXHAUSTED error response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(8), resp.GetErrorCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetErrorMessage(), "too large")
	assert.Contains(t, resp.GetErrorMessage(), "exceeds")
}

func TestReassembly_ErrorResponseContent(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-errcontent", "user-1")

	// Send two MORE chunks that exceed the limit.
	chunk1 := make([]byte, 60)
	ct1, err := session.Encrypt(chunk1)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-errcontent",
		Ciphertext:      ct1,
		CorrelationId:   42,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	chunk2 := make([]byte, 60)
	ct2, err := session.Encrypt(chunk2)
	require.NoError(t, err)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-errcontent",
		Ciphertext:      ct2,
		CorrelationId:   42,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Verify the full error response structure.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	chMsg := lastMsg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	assert.Equal(t, uint64(42), chMsg.GetCorrelationId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(8), resp.GetErrorCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetErrorMessage(), "120 bytes exceeds 100 byte limit")
}

func TestReassembly_MaxIncompleteErrorResponse(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 0, 2)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxincerr", "user-1")

	// Start 2 chunked sequences.
	for i := uint64(1); i <= 2; i++ {
		chunk := make([]byte, 10)
		ct, err := session.Encrypt(chunk)
		require.NoError(t, err)

		mgr.HandleMessage(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       "ch-maxincerr",
			Ciphertext:      ct,
			CorrelationId:   i,
			Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
		})
	}

	// 3rd should trigger error response.
	chunk := make([]byte, 10)
	ct, err := session.Encrypt(chunk)
	require.NoError(t, err)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-maxincerr",
		Ciphertext:      ct,
		CorrelationId:   3,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Should get a RESOURCE_EXHAUSTED error response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(8), resp.GetErrorCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetErrorMessage(), "too many incomplete")
}

func TestHandleClose_MidChunk(t *testing.T) {
	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, "ch-midclose", "user-1")

	// Start a chunked sequence.
	chunk := make([]byte, 10)
	ct, err := session.Encrypt(chunk)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-midclose",
		Ciphertext:      ct,
		CorrelationId:   1,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	})

	// Close the channel mid-chunk — should not panic.
	require.NotPanics(t, func() {
		mgr.HandleClose("ch-midclose")
	})

	// Verify session was removed.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-midclose"]
	mgr.mu.RUnlock()
	assert.False(t, exists)
}

func TestHandleMessage_decryptFailureClosesChannel(t *testing.T) {
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	sender := newCollectSender()

	var closedMu sync.Mutex
	var closedChannels []string
	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, 0)
	mgr.SetOnChannelClose(func(channelID string) {
		closedMu.Lock()
		closedChannels = append(closedChannels, channelID)
		closedMu.Unlock()
	})

	session := performHandshake(t, mgr, ck, "ch-decrypt-fail", "user-1")

	countBefore := len(sender.messages())

	// Send a message with corrupted ciphertext.
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-decrypt-fail",
		Ciphertext:      []byte("this-is-not-valid-ciphertext"),
		CorrelationId:   42,
	})

	// Verify a CLOSE notification was sent to the frontend.
	msgs := sender.waitForMessages(countBefore + 1)
	closeMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, closeMsg, "expected a ChannelMessage to be sent")
	assert.Equal(t, "ch-decrypt-fail", closeMsg.GetChannelId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, closeMsg.GetFlags())
	assert.Empty(t, closeMsg.GetCiphertext())

	// Verify the session was removed.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-decrypt-fail"]
	mgr.mu.RUnlock()
	assert.False(t, exists, "session should be removed after decrypt failure")

	// Verify the close callback was invoked.
	closedMu.Lock()
	assert.Equal(t, []string{"ch-decrypt-fail"}, closedChannels)
	closedMu.Unlock()

	// Verify subsequent messages for this channel are ignored.
	ct, encErr := session.Encrypt([]byte("should be ignored"))
	require.NoError(t, encErr)
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-decrypt-fail",
		Ciphertext:      ct,
		CorrelationId:   43,
	})

	// No additional messages should have been sent.
	assert.Len(t, sender.messages(), countBefore+1)
}

// An oversize chunked message must be errored ONCE and its id poisoned, not
// re-buffered.
//
// Deleting the buffer on breach (as this once did) let the very next MORE chunk find
// no entry, pass the max-incomplete check against the map the delete had just shrunk,
// allocate a fresh buffer and re-accumulate to the ceiling -- erroring, deleting and
// repeating for as long as the peer kept sending, burning the full budget plus an
// error goroutine per cycle on the worker's sole receive goroutine. This receiver's
// requests are peer-initiated, so nothing else bounds it.
//
// Against the old code the second breach fires another RESOURCE_EXHAUSTED and the
// assert.Never below fails. It mirrors the client-side test this defect's fix already
// has in tunnel/channel_test.go.
func TestReassembly_OversizeMessageErrorsOnceThenDropsChunks(t *testing.T) {
	const channelID = "ch-poison"
	const correlationID = uint64(7)
	mgr, kp, sender := setupTestManagerWith(t, 100, 4)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, channelID, "user-1")

	sendChunk := func(size int, more bool) {
		t.Helper()
		ct, err := session.Encrypt(make([]byte, size))
		require.NoError(t, err)
		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if more {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		mgr.HandleMessage(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       channelID,
			Ciphertext:      ct,
			CorrelationId:   correlationID,
			Flags:           flags,
		})
	}

	buffer := func() *channelwire.ChunkBuffer {
		t.Helper()
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		return mgr.sessions[channelID].reassembly.buffers[correlationID]
	}

	msgsBefore := len(sender.messages())
	sendChunk(60, true) // buffered: 60 <= 100
	sendChunk(60, true) // breach: 120 > 100

	msgs := sender.waitForMessages(msgsBefore + 1)
	resp := decryptInnerResponse(t, session, msgs[len(msgs)-1])
	require.True(t, resp.GetIsError())
	require.Equal(t, int32(8), resp.GetErrorCode()) // RESOURCE_EXHAUSTED
	require.Contains(t, resp.GetErrorMessage(), "120 bytes exceeds 100 byte limit")

	poisoned := buffer()
	require.NotNil(t, poisoned, "the breached id must leave a tombstone, not be deleted")
	assert.True(t, poisoned.Poisoned)
	assert.Zero(t, poisoned.Total, "poisoning must release the accumulated parts")
	assert.Empty(t, poisoned.Parts)

	// Keep pushing MORE chunks under the poisoned id: each must be dropped without
	// re-buffering a byte and without emitting a second error.
	for i := 0; i < 5; i++ {
		sendChunk(60, true)
	}
	assert.Never(t, func() bool { return len(sender.messages()) > msgsBefore+1 },
		300*time.Millisecond, 20*time.Millisecond,
		"a poisoned id must be errored exactly once, not once per re-accumulation")
	still := buffer()
	require.NotNil(t, still)
	assert.True(t, still.Poisoned)
	assert.Zero(t, still.Total, "dropped chunks must not re-accumulate")

	// The terminal (non-MORE) chunk reaps the tombstone: it is the worker's only
	// reaper, so without it a poisoned id would hold a slot until HandleClose.
	sendChunk(60, false)
	assert.Nil(t, buffer(), "the terminal chunk must reap the tombstone")
	assert.Never(t, func() bool { return len(sender.messages()) > msgsBefore+1 },
		200*time.Millisecond, 20*time.Millisecond,
		"reaping a poisoned id must not error again either")

	// The slot is free again: a fresh sequence under the same id is bounded afresh.
	sendChunk(60, true)
	fresh := buffer()
	require.NotNil(t, fresh)
	assert.False(t, fresh.Poisoned)
	assert.Equal(t, 60, fresh.Total)
}

// The error-send queue exists so the receive goroutine NEVER blocks on an
// error response: enqueue must return immediately even when the drainer is
// wedged and the queue is full (the overflow is dropped, per queueError's
// contract). A blocking enqueue would reintroduce the receive-loop wedge the
// queue replaced the inline sends to prevent.
func TestQueueErrorDoesNotBlockWhenFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No drainer is started: the queue can only fill.
	sender := &channelSender{
		channelID:  "ch-full",
		lifetime:   ctx,
		errorSends: make(chan errorSend, errorSendQueueSize),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range errorSendQueueSize + 8 {
			sender.queueError(uint64(i), 1, "overflow")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queueError blocked on a full queue")
	}
	assert.Len(t, sender.errorSends, errorSendQueueSize, "the queue holds exactly its cap; overflow is dropped")
}

// TestSendEncrypted_OversizedMessageIsRejectedNotFatal pins that the
// size cap reports itself as ErrMessageRejected. The distinction is
// load-bearing for fan-out callers: the channel is still healthy, so a
// broadcast must drop the one event rather than unsubscribe the client.
// Without the sentinel the two are indistinguishable and a single
// oversized payload silently deafens a live tab.
func TestSendEncrypted_OversizedMessageIsRejectedNotFatal(t *testing.T) {
	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxMessageSize: 8, // smaller than any real envelope
	}

	err := cs.sendStream(1, &leapmuxv1.InnerStreamMessage{Payload: []byte("a larger payload than the cap")})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMessageRejected,
		"the size cap is a per-message rejection, not a transport failure")
	assert.Empty(t, collector.messages(),
		"an over-cap message must not reach the transport at all")
}

// TestSendEncrypted_WithinCapDoesNotReportRejection guards the other
// side of the branch, so the sentinel can't be returned for every send.
func TestSendEncrypted_WithinCapDoesNotReportRejection(t *testing.T) {
	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	err := cs.sendStream(1, &leapmuxv1.InnerStreamMessage{Payload: []byte("ok")})

	require.NoError(t, err)
	assert.NotEmpty(t, collector.messages(), "a within-cap message reaches the transport")
}

// TestSendEncrypted_MaxSizedPayloadIsNotRejected is the guarantee the
// envelope headroom exists for: a producer that fills its entire budget
// must still get its message onto the wire.
//
// This is the case that used to fail. The agent stdout scanner accepted
// lines up to the same 16 MiB the receiver capped the whole reassembled
// message at, so a maximal line -- wrapped in AgentMessage, AgentEvent,
// WatchEventsResponse, InnerStreamMessage and InnerMessage -- came out
// over the limit and was refused. Nothing recovers from that: the stream
// is ordered and encrypted with no resync, and the transport never
// errors, so the client is simply missing an event and is never told.
func TestSendEncrypted_MaxSizedPayloadIsNotRejected(t *testing.T) {
	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	// Exactly the ceiling a producer is allowed to emit.
	err := cs.sendStream(1, &leapmuxv1.InnerStreamMessage{
		Payload: make([]byte, channelwire.MaxInnerPayloadBytes),
	})

	require.NoError(t, err,
		"a payload at MaxInnerPayloadBytes must fit once the envelopes are added")
	assert.NotEmpty(t, collector.messages(), "and must actually reach the transport")
}

// A small single-chunk response must be able to overtake a concurrent
// multi-chunk send: the chunked permit is held across the big message, but the
// frame permit is released between chunks.
func TestChannelSenderSmallResponseOvertakesMultiChunk(t *testing.T) {
	workerSession, initiatorSession := setupTestSessions(t)

	holdFirst := make(chan struct{})
	started := make(chan struct{})
	var (
		mu     sync.Mutex
		frames []*leapmuxv1.ChannelMessage
	)
	sendFn := func(msg *leapmuxv1.ConnectRequest) error {
		chMsg := msg.GetChannelMessageResp()
		require.NotNil(t, chMsg)
		mu.Lock()
		first := len(frames) == 0
		frames = append(frames, chMsg)
		mu.Unlock()
		if first {
			close(started)
			<-holdFirst
		}
		return nil
	}

	life, endLife := context.WithCancel(context.Background())
	t.Cleanup(endLife)
	cs := &channelSender{
		channelID:      "ch-overtake",
		session:        workerSession,
		sendFn:         sendFn,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	big := make([]byte, channelwire.MaxPlaintextPerChunk+100)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, cs.sendStream(1, &leapmuxv1.InnerStreamMessage{Payload: big}))
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("multi-chunk send never started")
	}

	// Park the small send on the frame permit while big holds it; releasing
	// holdFirst then lets small win the between-chunk acquire. A synchronous
	// sendResponse here would deadlock on the same permit.
	smallDone := make(chan error, 1)
	go func() {
		smallDone <- cs.sendResponse(2, &leapmuxv1.InnerRpcResponse{Payload: []byte("hi")})
	}()
	time.Sleep(20 * time.Millisecond)
	close(holdFirst)

	select {
	case err := <-smallDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("small response did not complete after the first chunk released the frame")
	}
	wg.Wait()

	mu.Lock()
	got := append([]*leapmuxv1.ChannelMessage(nil), frames...)
	mu.Unlock()
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, uint64(1), got[0].GetCorrelationId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, got[0].GetFlags())
	assert.Equal(t, uint64(2), got[1].GetCorrelationId(),
		"the small response must land between the big message's chunks")
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, got[1].GetFlags())
	assert.Equal(t, uint64(1), got[2].GetCorrelationId())

	// Peer decryptability in arrival order.
	for _, fr := range got {
		_, err := initiatorSession.Decrypt(fr.GetCiphertext())
		require.NoError(t, err, "peer must decrypt every frame in arrival order")
	}
}

// Two concurrent multi-chunk sends must never overlap their MORE runs: the
// Hub's chunk tracker admits at most one in-flight chunked sequence.
func TestChannelSenderConcurrentMultiChunkNeverOverlapMORERuns(t *testing.T) {
	workerSession, _ := setupTestSessions(t)

	var (
		mu      sync.Mutex
		active  int
		overlap atomic.Bool
	)
	sendFn := func(msg *leapmuxv1.ConnectRequest) error {
		chMsg := msg.GetChannelMessageResp()
		require.NotNil(t, chMsg)
		if chMsg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
			mu.Lock()
			active++
			if active > 1 {
				overlap.Store(true)
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
		}
		return nil
	}

	life, endLife := context.WithCancel(context.Background())
	t.Cleanup(endLife)
	cs := &channelSender{
		channelID:      "ch-more",
		session:        workerSession,
		sendFn:         sendFn,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	big := make([]byte, 2*channelwire.MaxPlaintextPerChunk+1)
	var wg sync.WaitGroup
	for id := uint64(1); id <= 2; id++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			require.NoError(t, cs.sendStream(id, &leapmuxv1.InnerStreamMessage{Payload: big}))
		}(id)
	}
	wg.Wait()
	assert.False(t, overlap.Load(), "two multi-chunk MORE runs must not overlap")
}

// Cancelling the session lifetime must unwedge a sender parked on the gate.
func TestChannelSenderLifetimeUnwedgesParkedSender(t *testing.T) {
	workerSession, _ := setupTestSessions(t)

	hold := make(chan struct{})
	started := make(chan struct{})
	var holding atomic.Bool
	sendFn := func(*leapmuxv1.ConnectRequest) error {
		if holding.CompareAndSwap(false, true) {
			close(started)
			<-hold
		}
		return nil
	}

	life, endLife := context.WithCancel(context.Background())
	cs := &channelSender{
		channelID:      "ch-life",
		session:        workerSession,
		sendFn:         sendFn,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	go func() {
		_ = cs.sendResponse(1, &leapmuxv1.InnerRpcResponse{Payload: []byte("hold")})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("holding send never started")
	}

	done := make(chan error, 1)
	go func() {
		done <- cs.sendResponse(2, &leapmuxv1.InnerRpcResponse{Payload: []byte("parked")})
	}()
	select {
	case <-done:
		t.Fatal("second send returned while the frame permit was held")
	case <-time.After(50 * time.Millisecond):
	}

	endLife()
	select {
	case err := <-done:
		require.ErrorIs(t, err, channelwire.ErrSendAborted)
	case <-time.After(2 * time.Second):
		t.Fatal("lifetime end did not unwedge the parked sender")
	}
	close(hold)
}

// DispatchAsync's unknown-method arm must QueueError (not SendError inline) when
// the writer offers an errorQueuer -- the receive loop must not park on the gate.
func TestDispatchAsyncUnknownMethodIsQueued(t *testing.T) {
	d := NewDispatcher()
	w := &queueTrackingWriter{}
	d.DispatchAsync(context.Background(), userid.MustNew("u"),
		&leapmuxv1.InnerRpcRequest{Method: "no-such-method"}, w)
	assert.Equal(t, int32(1), w.queued.Load(), "unknown method must go through QueueError")
	assert.Zero(t, w.sent.Load(), "unknown method must not SendError inline on an errorQueuer")
}

type queueTrackingWriter struct {
	queued, sent atomic.Int32
}

func (w *queueTrackingWriter) SendResponse(*leapmuxv1.InnerRpcResponse) error { return nil }
func (w *queueTrackingWriter) SendError(int32, string) error {
	w.sent.Add(1)
	return nil
}
func (w *queueTrackingWriter) SendStream(*leapmuxv1.InnerStreamMessage) error { return nil }
func (*queueTrackingWriter) ChannelID() string                                { return "" }
func (w *queueTrackingWriter) QueueError(int32, string)                       { w.queued.Add(1) }

// Decrypt-failure CLOSE must go through trySendFn BEFORE HandleClose tears the
// session down, so the CLOSE is enqueued on the connection writer's FIFO ahead
// of teardown.
func TestHandleMessage_DecryptFailureCLOSEBeforeHandleClose(t *testing.T) {
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	const channelID = "ch-decrypt-order"
	var (
		mu    sync.Mutex
		order []string
	)

	sender := newCollectSender()
	var mgr *Manager
	mgr = NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, func(msg *leapmuxv1.ConnectRequest) bool {
		chMsg := msg.GetChannelMessageResp()
		require.NotNil(t, chMsg)
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, chMsg.GetFlags())

		mgr.mu.RLock()
		_, exists := mgr.sessions[channelID]
		mgr.mu.RUnlock()
		assert.True(t, exists, "trySendFn must run before HandleClose removes the session")

		mu.Lock()
		order = append(order, "trySend")
		mu.Unlock()
		return true
	}, 0)
	mgr.SetOnChannelClose(func(string) {
		mu.Lock()
		order = append(order, "handleClose")
		mu.Unlock()
	})
	_ = performHandshake(t, mgr, ck, channelID, "user-1")

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelID,
		Ciphertext:      []byte("not-valid-ciphertext"),
		CorrelationId:   1,
	})

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	assert.Equal(t, []string{"trySend", "handleClose"}, got)
}
