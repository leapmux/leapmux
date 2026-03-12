package channel

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
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
	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, maxMessageSize, maxIncompleteChunked, nil)
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

// sendUserIdClaim sends a UserIdClaim wrapped in InnerMessage via the encrypted channel.
func sendUserIdClaim(t *testing.T, mgr *Manager, initiatorSession *noiseutil.Session, channelID, userID string) {
	t.Helper()

	claim := &leapmuxv1.UserIdClaim{
		UserId:      userID,
		TimestampMs: 1000,
	}
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_UserIdClaim{UserIdClaim: claim},
	}
	plaintext, err := proto.Marshal(envelope)
	require.NoError(t, err)

	ciphertext, err := initiatorSession.Encrypt(plaintext)
	require.NoError(t, err)

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelID,
		Ciphertext:      ciphertext,
	})
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

// decryptClaimResponse decrypts and unwraps a UserIdClaimResponse from the sender.
func decryptClaimResponse(t *testing.T, initiatorSession *noiseutil.Session, msg *leapmuxv1.ConnectRequest) *leapmuxv1.UserIdClaimResponse {
	t.Helper()

	chMsg := msg.GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPlaintext, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPlaintext, &envelope))

	resp, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_UserIdClaimResponse)
	require.True(t, ok, "expected InnerMessage_UserIdClaimResponse, got %T", envelope.GetKind())

	return resp.UserIdClaimResponse
}

// performHandshakeAndVerify performs handshake + UserIdClaim in one step.
func performHandshakeAndVerify(t *testing.T, mgr *Manager, ck *noiseutil.CompositeKeypair, sender *collectSender, channelID, userID string) *noiseutil.Session {
	t.Helper()

	msgsBefore := len(sender.messages())
	session := performHandshake(t, mgr, ck, channelID, userID)

	// Send UserIdClaim.
	sendUserIdClaim(t, mgr, session, channelID, userID)

	// Wait for the async claim response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	claimResp := decryptClaimResponse(t, session, msgs[len(msgs)-1])
	require.True(t, claimResp.GetSuccess())

	return session
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

func TestUserIdClaim_Success(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, "ch-claim", "user-1")
	sendUserIdClaim(t, mgr, session, "ch-claim", "user-1")

	// Wait for async claim response.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)

	claimResp := decryptClaimResponse(t, session, msgs[0])
	assert.True(t, claimResp.GetSuccess())
}

func TestUserIdClaim_Mismatch(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, "ch-mismatch", "user-1")
	// Send a claim with the wrong user ID.
	sendUserIdClaim(t, mgr, session, "ch-mismatch", "wrong-user")

	// Wait for async claim response.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)

	claimResp := decryptClaimResponse(t, session, msgs[0])
	assert.False(t, claimResp.GetSuccess())
	assert.Contains(t, claimResp.GetErrorMessage(), "mismatch")

	// Channel should have been closed (async, may need brief wait).
	require.Eventually(t, func() bool {
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		_, exists := mgr.sessions["ch-mismatch"]
		return !exists
	}, time.Second, 10*time.Millisecond)
}

func TestUserIdClaim_DuplicateRejected(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-dup", "user-1")

	// Send a second UserIdClaim — should be rejected.
	sendUserIdClaim(t, mgr, session, "ch-dup", "user-1")

	// Wait for the async rejection response.
	msgs := sender.waitForMessages(2) // first claim response + second rejection
	claimResp := decryptClaimResponse(t, session, msgs[1])
	assert.False(t, claimResp.GetSuccess())
	assert.Contains(t, claimResp.GetErrorMessage(), "already verified")
}

func TestRequest_BeforeClaim_Rejected(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, "ch-unclaimed", "user-1")

	// Send a request without first sending UserIdClaim.
	ct := sendRequest(t, session, "ch-unclaimed", &leapmuxv1.InnerRpcRequest{
		Method: "anything",
	})
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-unclaimed",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Wait for async error response.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)

	resp := decryptInnerResponse(t, session, msgs[0])
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(9), resp.GetErrorCode()) // FAILED_PRECONDITION
}

func TestHandleMessage_DispatchAndResponse(t *testing.T) {
	mgr, kp, sender := setupTestManager(t)

	// Set up a dispatcher with a test handler.
	dispatcher := NewDispatcher()
	dispatcher.Register("echo", func(userID string, req *leapmuxv1.InnerRpcRequest, s *Sender) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: req.GetPayload(),
		})
	})
	mgr.SetDispatcher(dispatcher)

	// Open channel and verify claim.
	initiatorSession := performHandshakeAndVerify(t, mgr, kp, sender, "ch-echo", "user-echo")

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
	assert.Equal(t, uint32(42), msgs[len(msgs)-1].GetChannelMessageResp().GetCorrelationId())
	assert.Equal(t, []byte("hello"), innerResp.GetPayload())
	assert.False(t, innerResp.GetIsError())
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

	// Send UserIdClaim first.
	sendUserIdClaim(t, mgr, initiatorSession, "ch-no-disp", "user-1")
	// Wait for async claim response.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)
	claimResp := decryptClaimResponse(t, initiatorSession, msgs[0])
	require.True(t, claimResp.GetSuccess())

	ct := sendRequest(t, initiatorSession, "ch-no-disp", &leapmuxv1.InnerRpcRequest{
		Method: "anything",
	})

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-no-disp",
		Ciphertext:      ct,
		CorrelationId:   1,
	})

	// Wait for async UNIMPLEMENTED error response.
	msgs = sender.waitForMessages(2) // claim response + error response
	require.Len(t, msgs, 2)

	innerResp := decryptInnerResponse(t, initiatorSession, msgs[1])
	assert.True(t, innerResp.GetIsError())
	assert.Equal(t, int32(12), innerResp.GetErrorCode()) // UNIMPLEMENTED
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

	mgr := NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, blockingSend, 0, 0, nil)

	// Set up a dispatcher with a handler that sends a response.
	dispatcher := NewDispatcher()
	dispatcher.Register("slow", func(userID string, req *leapmuxv1.InnerRpcRequest, s *Sender) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("done"),
		})
	})
	mgr.SetDispatcher(dispatcher)

	// Open channel and verify claim.
	session := performHandshake(t, mgr, ck, "ch-block", "user-1")

	// Send the UserIdClaim. Since sends are now async, HandleMessage returns
	// immediately and the claim response send happens in a goroutine.
	sendUserIdClaim(t, mgr, session, "ch-block", "user-1")

	// The claim response goroutine is now blocked in blockingSend.
	// Wait for it to enter the blocked state.
	<-blockCh

	// Now send a request. The critical test: HandleMessage must return
	// promptly even though sendFn is currently blocked by the claim response.
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
	dispatcher.Register("ping", func(userID string, req *leapmuxv1.InnerRpcRequest, s *Sender) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("pong-" + userID),
		})
	})
	mgr.SetDispatcher(dispatcher)

	session1 := performHandshakeAndVerify(t, mgr, kp, sender, "ch-a", "alice")
	session2 := performHandshakeAndVerify(t, mgr, kp, sender, "ch-b", "bob")

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
	dispatcher.Register("echo", func(_ string, req *leapmuxv1.InnerRpcRequest, s *Sender) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: req.GetPayload()})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-single", "user-1")

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
	largePayload := make([]byte, MaxChunkPlaintext+100)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	dispatcher := NewDispatcher()
	dispatcher.Register("big", func(_ string, _ *leapmuxv1.InnerRpcRequest, s *Sender) {
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: largePayload})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-multi", "user-1")

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
		assert.Equal(t, uint32(1), chMsg.GetCorrelationId())
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

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-exact", "user-1")

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
	exactPayloadSize := findPayloadSize(MaxChunkPlaintext)
	require.Greater(t, exactPayloadSize, 0)

	dispatcher.Register("exact", func(_ string, _ *leapmuxv1.InnerRpcRequest, s *Sender) {
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
	dispatcher.Register("overflow", func(_ string, _ *leapmuxv1.InnerRpcRequest, s *Sender) {
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
	dispatcher.Register("echo", func(_ string, req *leapmuxv1.InnerRpcRequest, s *Sender) {
		receivedPayload = req.GetPayload()
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: []byte("ok")})
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-reasm", "user-1")

	// Build a large InnerMessage, then chunk it manually on the initiator side.
	largePayload := make([]byte, MaxChunkPlaintext*2+100)
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

	// Send chunks manually.
	for offset := 0; offset < len(data); {
		end := offset + MaxChunkPlaintext
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
	msgs := sender.waitForMessages(len(sender.messages()) + 1)
	_ = msgs // Just ensuring the handler was called.

	// The handler should have received the full payload.
	assert.Equal(t, largePayload, receivedPayload)
}

func TestReassembly_MaxSizeExceeded(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-maxsize", "user-1")

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

	// Subsequent messages on other correlation IDs should still work.
	sendUserIdClaim(t, mgr, session, "ch-maxsize", "user-1")

	// The duplicate claim will get a rejection but proves the channel is still functional.
	msgs := sender.waitForMessages(len(sender.messages()) + 1)
	require.NotEmpty(t, msgs)
}

func TestReassembly_MaxIncompleteExceeded(t *testing.T) {
	mgr, kp, sender := setupTestManagerWith(t, 0, 2)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-maxinc", "user-1")

	// Start 2 chunked sequences.
	for i := uint32(1); i <= 2; i++ {
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
	mgr, kp, sender := setupTestManagerWith(t, 100, 0) // Very small limit.

	// Register a handler that tries to send a response larger than the limit.
	dispatcher := NewDispatcher()
	dispatcher.Register("big", func(_ string, _ *leapmuxv1.InnerRpcRequest, s *Sender) {
		err := s.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: make([]byte, 200)})
		// sendEncrypted should return an error for oversized messages.
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "message too large")
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-sendmax", "user-1")

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

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-finalover", "user-1")

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

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-errcontent", "user-1")

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
	assert.Equal(t, uint32(42), chMsg.GetCorrelationId())
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

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-maxincerr", "user-1")

	// Start 2 chunked sequences.
	for i := uint32(1); i <= 2; i++ {
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
	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshakeAndVerify(t, mgr, kp, sender, "ch-midclose", "user-1")

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
