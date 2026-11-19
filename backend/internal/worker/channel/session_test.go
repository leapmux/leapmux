package channel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/authscope"
)

// testChannelGrant is the grant a channel-open fixture announces.
//
// The Hub sends the opening credential's scopes on the wire, and a worker that
// receives NONE refuses the handshake -- the zero grant reaches nothing, so an
// omitted field denies rather than admits. Every fixture here is about what
// happens AFTER the channel opens, so each announces the ordinary
// non-administrative grant. A test that means to exercise a narrower one says
// so at its own call site.
const testWorkerID = "worker-test"

var testChannelGrant = authscope.ScopesToWire(authscope.NonAdminGrant())

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
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, 0, maxIncompleteChunked)
	// Tests that exercise the size cap inject a reassembled ceiling directly
	// so both the sender and the reassembler pick it up at HandleOpen.
	if maxMessageSize > 0 {
		mgr.reassembledOverride = maxMessageSize
	}
	return mgr, ck, sender
}

func startTestHandshake(t *testing.T, ck *noiseutil.CompositeKeypair) (*noiseutil.HandshakeState, []byte, error) {
	t.Helper()
	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	return noiseutil.InitiatorHandshake1(testWorkerID, ck.X25519Public, ck.MlkemPublicKeyBytes(), slhdsaPub)
}

// performHandshake runs a full hybrid Noise_NK handshake between initiator and responder (Manager).
func performHandshake(t *testing.T, mgr *Manager, ck *noiseutil.CompositeKeypair, channelID, userID string) *noiseutil.Session {
	t.Helper()

	// Initiator creates msg1.
	hs, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)

	// Responder (Manager) handles open.
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           userID,
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError(), "handshake should succeed")
	require.NotEmpty(t, resp.GetHandshakePayload())
	assert.Equal(t, channelID, resp.GetChannelId())
	assert.Equal(t, uint64(contracts.MaxMessageSize), resp.GetMaxMessageSize(),
		"worker must echo the effective max_message_size")

	// Initiator completes handshake.
	initiatorSession, err := hs.Finish(resp.GetHandshakePayload())
	require.NoError(t, err)

	return initiatorSession
}

// rekeyInitiator mirrors the tunnel's owned ephemeral seed and PQ offer.
type rekeyInitiator struct {
	ephemeralSeed []byte
	pq            *noiseutil.RekeyPQOffer
}

func initiatorStartRekey(t *testing.T, workerMlkemPub []byte) (*leapmuxv1.InnerMessage, *rekeyInitiator) {
	t.Helper()
	seed, err := noiseutil.GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	publicKey, err := noiseutil.X25519PublicFromSeed(seed)
	require.NoError(t, err)
	offer, err := noiseutil.OfferRekeyPQ(workerMlkemPub)
	require.NoError(t, err)
	return &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyRequest{RekeyRequest: &leapmuxv1.RekeyRequest{
			DhPub:             publicKey,
			MlkemStaticCt:     offer.StaticCiphertext,
			MlkemEphemeralPub: offer.EphemeralPublicKey,
		}},
	}, &rekeyInitiator{ephemeralSeed: seed, pq: offer}
}

func initiatorApplyAck(t *testing.T, init *noiseutil.Session, ri *rekeyInitiator, ack *leapmuxv1.RekeyAck) {
	t.Helper()
	defer noiseutil.Zero(ri.ephemeralSeed)
	pqSecret, err := ri.pq.Accept(ack.GetMlkemEphemeralCt())
	require.NoError(t, err)
	defer noiseutil.Zero(pqSecret)
	dh, pq, err := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
		LocalEphemeralPriv: ri.ephemeralSeed,
		PeerEphemeralPub:   ack.GetDhPub(),
		PQSharedSecret:     pqSecret,
	})
	require.NoError(t, err)
	dh2 := append([]byte(nil), dh...)
	pq2 := append([]byte(nil), pq...)
	init.RekeySendWithSecret(dh, pq)
	init.RekeyReceiveWithSecret(dh2, pq2)
}

// performClassicalHandshake runs a classic handshake against a classic-mode
// Manager. Its rekey uses X25519 and no ML-KEM exchange.
func performClassicalHandshake(t *testing.T, mgr *Manager, ck *noiseutil.CompositeKeypair, channelID, userID string) *noiseutil.Session {
	t.Helper()
	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	hs, msg1, err := noiseutil.ClassicalInitiatorHandshake1(testWorkerID, ck.X25519Public, ck.MlkemPublicKeyBytes(), slhdsaPub)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           userID,
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError(), "classical handshake should succeed")
	require.NotEmpty(t, resp.GetHandshakePayload())
	initiator, err := hs.Finish(resp.GetHandshakePayload())
	require.NoError(t, err)
	return initiator
}

func TestHandleMessage_ClosesTheChannelOnAChangedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sealChannelID string
		flags         leapmuxv1.ChannelMessageFlags
		change        func(*leapmuxv1.ChannelMessage)
	}{
		{
			name: "correlation id",
			change: func(msg *leapmuxv1.ChannelMessage) {
				msg.CorrelationId = 8
			},
		},
		{
			name:  "a first chunk loses MORE",
			flags: leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
			change: func(msg *leapmuxv1.ChannelMessage) {
				msg.Flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
			},
		},
		{
			name: "a final frame gains MORE",
			change: func(msg *leapmuxv1.ChannelMessage) {
				msg.Flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
			},
		},
		{
			name:          "channel id",
			sealChannelID: "other-channel",
			change: func(msg *leapmuxv1.ChannelMessage) {
				msg.ChannelId = "ch-envelope"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, key, sender := setupTestManager(t)
			mgr.SetDispatcher(NewDispatcher())
			session := performHandshake(t, mgr, key, "ch-envelope", "user-1")
			plaintext, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Request{
				Request: &leapmuxv1.InnerRpcRequest{Method: "test"},
			}})
			require.NoError(t, err)
			channelID := tc.sealChannelID
			if channelID == "" {
				channelID = "ch-envelope"
			}
			msg, err := channelwire.SealChannelMessage(session, channelID, 7, tc.flags, plaintext)
			require.NoError(t, err)
			tc.change(msg)

			mgr.HandleMessage(msg)

			mgr.mu.RLock()
			_, exists := mgr.sessions["ch-envelope"]
			mgr.mu.RUnlock()
			assert.False(t, exists, "a changed envelope must close the channel")
			var closeFrames int
			for _, sent := range sender.messages() {
				if sent.GetChannelMessageResp().GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE {
					closeFrames++
				}
			}
			assert.Equal(t, 1, closeFrames)
			mgr.HandleMessage(msg)
		})
	}
}

func TestHandleOpen_RefusesAHandshakeForAnotherWorker(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	firstSender := newCollectSender()
	secondSender := newCollectSender()
	_ = NewManager("worker-a", key, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, firstSender.send, firstSender.trySend, 0, 0)
	second := NewManager("worker-b", key, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, secondSender.send, secondSender.trySend, 0, 0)

	slhdsaPub, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	hs, message1, err := noiseutil.InitiatorHandshake1("worker-a", key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPub)
	require.NoError(t, err)
	defer hs.Clear()
	resp := second.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-other-worker",
		UserId:           "user-1",
		HandshakePayload: message1,
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})

	assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED, resp.GetErrorCode())
	second.mu.RLock()
	_, exists := second.sessions["ch-other-worker"]
	second.mu.RUnlock()
	assert.False(t, exists)
}

func TestHandleOpen_RefusesAHandshakeOfTheOtherMode(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	for _, managerMode := range []leapmuxv1.EncryptionMode{
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	} {
		t.Run(managerMode.String(), func(t *testing.T) {
			sender := newCollectSender()
			mgr := NewManager(testWorkerID, key, managerMode, sender.send, sender.trySend, 0, 0)
			var hs *noiseutil.HandshakeState
			var message1 []byte
			if managerMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
				hs, message1, err = noiseutil.InitiatorHandshake1(testWorkerID, key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPub)
			} else {
				hs, message1, err = noiseutil.ClassicalInitiatorHandshake1(testWorkerID, key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPub)
			}
			require.NoError(t, err)
			defer hs.Clear()
			resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
				ChannelId:        "ch-wrong-mode",
				UserId:           "user-1",
				HandshakePayload: message1,
				MaxMessageSize:   uint64(contracts.MaxMessageSize),
				GrantedScopes:    testChannelGrant,
			})
			assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED, resp.GetErrorCode())
			mgr.mu.RLock()
			_, exists := mgr.sessions["ch-wrong-mode"]
			mgr.mu.RUnlock()
			assert.False(t, exists)
		})
	}
}

// initiatorStartClassicalRekey builds a classic rekey with empty ML-KEM fields.
func initiatorStartClassicalRekey(t *testing.T) (*leapmuxv1.InnerMessage, *rekeyInitiator) {
	t.Helper()
	return initiatorStartRekey(t, nil)
}

func TestHandleOpen_NegotiatesMinOfHubAndWorker(t *testing.T) {
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	sender := newCollectSender()

	const workerMax = 2 * 1024 * 1024
	const hubMax = 4 * 1024 * 1024
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, workerMax, 0)

	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-min",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(hubMax),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError())
	assert.Equal(t, uint64(workerMax), resp.GetMaxMessageSize(),
		"effective must be min(hub=%d, worker=%d)", hubMax, workerMax)

	mgr.mu.RLock()
	sess := mgr.sessions["ch-min"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	assert.Equal(t, channelwire.MaxReassembledMessageSize(workerMax), sess.sender.maxReassembled)
	assert.Equal(t, workerMax, sess.sender.maxPayload)
}

func TestHandleOpen_TakesHubWhenHubSmallerThanWorker(t *testing.T) {
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	sender := newCollectSender()

	const workerMax = 4 * 1024 * 1024
	const hubMax = 2 * 1024 * 1024
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, workerMax, 0)

	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-hub-wins",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(hubMax),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError())
	assert.Equal(t, uint64(hubMax), resp.GetMaxMessageSize(),
		"effective must be min(hub=%d, worker=%d)", hubMax, workerMax)

	mgr.mu.RLock()
	sess := mgr.sessions["ch-hub-wins"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	want := channelwire.MaxReassembledMessageSize(hubMax)
	assert.Equal(t, want, sess.sender.maxReassembled)
	assert.Equal(t, hubMax, sess.sender.maxPayload)
	assert.Equal(t, want, sess.reassembly.maxMessageSize)
}

func TestHandleOpen_NotifiesNegotiatedMaxMessageSize(t *testing.T) {
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	sender := newCollectSender()

	const workerMax = 4 * 1024 * 1024
	const hubMax = 2 * 1024 * 1024
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, workerMax, 0)
	var seenID string
	var seen int
	mgr.SetOnNegotiatedMaxMessageSize(func(channelID string, n int) {
		seenID = channelID
		seen = n
	})

	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-notify",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(hubMax),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError())
	assert.Equal(t, "ch-notify", seenID)
	assert.Equal(t, hubMax, seen, "callback must receive min(hub, worker)")

	var released string
	mgr.SetOnReleasedMaxMessageSize(func(channelID string) { released = channelID })
	mgr.HandleClose("ch-notify")
	assert.Equal(t, "ch-notify", released)
}

func TestHandleOpen_ReassembledOverrideClampsMaxPayloadBudget(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManagerWith(t, 100, 0)
	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-tiny",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError())
	mgr.mu.RLock()
	sess := mgr.sessions["ch-tiny"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	assert.Equal(t, 100, sess.sender.maxReassembled)
	assert.Equal(t, 100, sess.sender.MaxPayloadBudget(),
		"MaxPayloadBudget must not exceed a tiny reassembledOverride")
}

func TestCloseAll_UsesReleasedAllCallback(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManager(t)
	var perID int
	var allCalls int
	mgr.SetOnReleasedMaxMessageSize(func(string) { perID++ })
	mgr.SetOnReleasedAllMaxMessageSizes(func() { allCalls++ })

	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	for _, id := range []string{"ch-a", "ch-b"} {
		resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
			ChannelId:        id,
			UserId:           "user-1",
			HandshakePayload: msg1,
			MaxMessageSize:   uint64(contracts.MaxMessageSize),
			GrantedScopes:    testChannelGrant,
		})
		require.Empty(t, resp.GetError())
		_, msg1, err = startTestHandshake(t, ck)
		require.NoError(t, err)
	}

	mgr.CloseAll()
	assert.Equal(t, 1, allCalls, "CloseAll must clear budgets once via the all-callback")
	assert.Zero(t, perID, "CloseAll must not also walk per-id Release when all-callback is set")
}

func TestCloseAll_FallsBackToPerIDRelease(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManager(t)
	var released []string
	mgr.SetOnReleasedMaxMessageSize(func(id string) { released = append(released, id) })

	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	for _, id := range []string{"ch-a", "ch-b"} {
		resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
			ChannelId:        id,
			UserId:           "user-1",
			HandshakePayload: msg1,
			MaxMessageSize:   uint64(contracts.MaxMessageSize),
			GrantedScopes:    testChannelGrant,
		})
		require.Empty(t, resp.GetError())
		_, msg1, err = startTestHandshake(t, ck)
		require.NoError(t, err)
	}

	mgr.CloseAll()
	assert.ElementsMatch(t, []string{"ch-a", "ch-b"}, released,
		"CloseAll without the all-callback must Release each open channel id")
}

func TestHandleOpen_RejectsOutOfBoundsHubMax(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManager(t)
	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)

	t.Run("below floor", func(t *testing.T) {
		resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
			ChannelId:        "ch-bad-max-low",
			UserId:           "user-1",
			HandshakePayload: msg1,
			MaxMessageSize:   1,
			GrantedScopes:    testChannelGrant,
		})
		assert.NotEmpty(t, resp.GetError())
		assert.Contains(t, resp.GetError(), "max_message_size")
		assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE, resp.GetErrorCode())
		mgr.mu.RLock()
		_, exists := mgr.sessions["ch-bad-max-low"]
		mgr.mu.RUnlock()
		assert.False(t, exists)
	})

	t.Run("above ceiling", func(t *testing.T) {
		resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
			ChannelId:        "ch-bad-max-high",
			UserId:           "user-1",
			HandshakePayload: msg1,
			MaxMessageSize:   uint64(contracts.MaxConfigurableMessageSize + 1),
			GrantedScopes:    testChannelGrant,
		})
		assert.NotEmpty(t, resp.GetError())
		assert.Contains(t, resp.GetError(), "max_message_size")
		assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE, resp.GetErrorCode())
		mgr.mu.RLock()
		_, exists := mgr.sessions["ch-bad-max-high"]
		mgr.mu.RUnlock()
		assert.False(t, exists)
	})
}

func sealChannelPlaintext(t *testing.T, session *noiseutil.Session, channelID string, correlationID uint64, flags leapmuxv1.ChannelMessageFlags, plaintext []byte) *leapmuxv1.ChannelMessage {
	t.Helper()
	frame, err := channelwire.SealChannelMessage(session, channelID, correlationID, flags, plaintext)
	require.NoError(t, err)
	return frame
}

// sendRequest returns a sealed InnerRpcRequest frame.
func sendRequest(t *testing.T, initiatorSession *noiseutil.Session, channelID string, req *leapmuxv1.InnerRpcRequest, correlationID uint64, flags leapmuxv1.ChannelMessageFlags) *leapmuxv1.ChannelMessage {
	t.Helper()
	envelope := &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Request{Request: req}}
	plaintext, err := proto.Marshal(envelope)
	require.NoError(t, err)
	return sealChannelPlaintext(t, initiatorSession, channelID, correlationID, flags, plaintext)
}

// decryptInnerResponse decrypts and unwraps a response from the sender.
func decryptInnerResponse(t *testing.T, initiatorSession *noiseutil.Session, msg *leapmuxv1.ConnectRequest) *leapmuxv1.InnerRpcResponse {
	t.Helper()

	chMsg := msg.GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPlaintext, err := channelwire.OpenChannelMessage(initiatorSession, chMsg)
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPlaintext, &envelope))

	resp, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())

	return resp.Response
}

func TestHandleOpen_Success(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)

	session := performHandshake(t, mgr, kp, "ch-1", "user-1")
	require.NotNil(t, session)

	// Verify the session was registered.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-1"]
	mgr.mu.RUnlock()
	assert.True(t, exists)
}

func TestHandleOpen_DuplicateChannelIdRejected(t *testing.T) {
	t.Parallel()

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
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, 0, 0)
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
	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)
	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-reuse",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})
	assert.NotEmpty(t, resp.GetError(), "re-open must return an error response")
	assert.Contains(t, resp.GetError(), "already active")
	assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE, resp.GetErrorCode())

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
// so unlike the machine-scoped families -- which the owner gate fails closed on
// for exactly this input -- an empty id is not self-limiting there.
func TestHandleOpen_RefusesEmptyUserID(t *testing.T) {
	t.Parallel()

	mgr, ck, _ := setupTestManager(t)

	// A VALID handshake payload, so the rejection is provably the identity check
	// and not the handshake layer short-circuiting earlier.
	_, msg1, err := startTestHandshake(t, ck)
	require.NoError(t, err)

	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-anon",
		UserId:           "",
		HandshakePayload: msg1,
		GrantedScopes:    testChannelGrant,
	})
	assert.NotEmpty(t, resp.GetError(), "an open naming no user must be refused")
	assert.Contains(t, resp.GetError(), "no authenticated user id")
	assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER, resp.GetErrorCode())
	assert.Equal(t, "ch-anon", resp.GetChannelId())

	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-anon"]
	mgr.mu.RUnlock()
	assert.False(t, exists, "a refused open must not install a session")
}

func TestHandleOpen_BadHandshake(t *testing.T) {
	t.Parallel()

	mgr, _, _ := setupTestManager(t)

	resp := mgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-bad",
		UserId:           "user-1",
		HandshakePayload: []byte("not a valid handshake message"),
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    testChannelGrant,
	})
	assert.NotEmpty(t, resp.GetError())
	assert.Contains(t, resp.GetError(), "handshake failed")
	assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED, resp.GetErrorCode())
	assert.Equal(t, "ch-bad", resp.GetChannelId())

	// Session should not have been registered.
	mgr.mu.RLock()
	_, exists := mgr.sessions["ch-bad"]
	mgr.mu.RUnlock()
	assert.False(t, exists)
}

func TestHandleMessage_DispatchAndResponse(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	// Set up a dispatcher with a test handler.
	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)

	// Open a channel and run the Noise handshake.
	initiatorSession := performHandshake(t, mgr, kp, "ch-echo", "user-echo")

	// Create an inner RPC request wrapped in InnerMessage.
	ct := sendRequest(t, initiatorSession, "ch-echo", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("hello"),
	}, 42, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)

	// Send the encrypted message.
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	// Wait for the async handler to send the response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)

	innerResp := decryptInnerResponse(t, initiatorSession, msgs[len(msgs)-1])
	assert.Equal(t, uint64(42), msgs[len(msgs)-1].GetChannelMessageResp().GetCorrelationId())
	assert.Equal(t, []byte("hello"), innerResp.GetPayload())
	assert.Nil(t, innerResp.GetError())
}

// A frame whose flags value is one no conformant sender emits (e.g. MORE|CLOSE
// combined) is a protocol violation: it must be dropped -- NOT read as a final
// chunk and delivered truncated -- and the drop must happen after the decrypt,
// so the receive nonce stays in step with the peer and the session survives.
func TestHandleMessage_OutOfSpecFlagsDroppedWithoutNonceDesync(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-flags", "user-flags")

	// Frame 1: a well-formed request under an out-of-spec flags value. It must
	// be dropped without any response.
	ct1 := sendRequest(t, initiatorSession, "ch-flags", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("dropped"),
	}, 41, leapmuxv1.ChannelMessageFlags(3))
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct1)
	assert.Len(t, sender.messages(), msgsBefore, "an out-of-spec frame must produce no response")

	// Frame 2: the NEXT ciphertext from the same initiator session still
	// decrypts and dispatches -- proving the dropped frame advanced the
	// receive nonce (a drop before the decrypt would desync every later frame).
	ct2 := sendRequest(t, initiatorSession, "ch-flags", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("delivered"),
	}, 42, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct2)
	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)
	innerResp := decryptInnerResponse(t, initiatorSession, msgs[len(msgs)-1])
	assert.Equal(t, []byte("delivered"), innerResp.GetPayload())
}

func TestHandleMessage_UnknownChannel(t *testing.T) {
	t.Parallel()

	mgr, _, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	// Sending to a non-existent channel should not panic.
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ChannelId:  "nonexistent",
		Ciphertext: []byte("garbage"),
	})
}

func TestHandleMessage_NoDispatcher(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	// Intentionally do NOT set a dispatcher.

	initiatorSession := performHandshake(t, mgr, kp, "ch-no-disp", "user-1")

	ct := sendRequest(t, initiatorSession, "ch-no-disp", &leapmuxv1.InnerRpcRequest{
		Method: "anything",
	}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)

	mgr.HandleMessage(ct)

	// Wait for async UNIMPLEMENTED error response. It is the session's FIRST
	// message: with the identity claim gone, a request needs no preceding exchange.
	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1)

	innerResp := decryptInnerResponse(t, initiatorSession, msgs[0])
	assert.NotNil(t, innerResp.GetError())
	assert.Equal(t, int32(12), innerResp.GetError().GetCode()) // UNIMPLEMENTED
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
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	gotUserID := make(chan userid.UserID, 1)
	dispatcher := NewDispatcher()
	dispatcher.Handle("whoami", Registration{}, func(_ context.Context, caller Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		gotUserID <- caller.UserID
		_ = s.SendResponse(channelwire.NewPayloadResponse([]byte("ok")))
	})
	mgr.SetDispatcher(dispatcher)

	// No claim: the very first encrypted message is a request.
	session := performHandshake(t, mgr, kp, "ch-noclaim", "hub-user")
	ct := sendRequest(t, session, "ch-noclaim", &leapmuxv1.InnerRpcRequest{Method: "whoami"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct)

	select {
	case userID := <-gotUserID:
		assert.Equal(t, "hub-user", userID.String(), "the dispatcher sees the Hub-supplied identity")
	case <-time.After(2 * time.Second):
		t.Fatal("the first request after the handshake was not dispatched")
	}

	msgs := sender.waitForMessages(1)
	require.Len(t, msgs, 1, "the request is answered, not gated behind a claim")
	assert.False(t, decryptInnerResponse(t, session, msgs[0]).GetError() != nil)
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
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-wide", "user-1")
	// Past uint32: a 32-bit field would truncate this to 7 and correlate the reply to
	// whatever request holds id 7.
	const wideID uint64 = 1<<40 | 7
	ct := sendRequest(t, session, "ch-wide", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("hello"),
	}, wideID, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	msgs := sender.waitForMessages(msgsBefore + 1)
	require.Greater(t, len(msgs), msgsBefore)
	assert.Equal(t, wideID, msgs[len(msgs)-1].GetChannelMessageResp().GetCorrelationId(),
		"the reply must carry the id it was sent with, not a truncated one")
	assert.Equal(t, []byte("hello"), decryptInnerResponse(t, session, msgs[len(msgs)-1]).GetPayload())
}

func TestHandleClose(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)

	gotCtxC := make(chan context.Context, 1)
	dispatcher := NewDispatcher()
	dispatcher.Handle("inspect", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		gotCtxC <- ctx
		_ = s.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-inspect", "user-1")

	ct := sendRequest(t, initiatorSession, "ch-inspect", &leapmuxv1.InnerRpcRequest{Method: "inspect"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct)

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
	t.Parallel()

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

	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, blockingSend, nil, 0, 0)

	// Set up a dispatcher with a handler that sends a response.
	dispatcher := NewDispatcher()
	dispatcher.Handle("slow", Registration{}, func(_ context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse([]byte("done")))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, ck, "ch-block", "user-1")

	// Occupy the sender: dispatch a first request whose handler's response send
	// blocks in blockingSend. Sends are async, so HandleMessage returns and the
	// response send stays parked in its own goroutine.
	occupy := sendRequest(t, session, "ch-block", &leapmuxv1.InnerRpcRequest{
		Method: "slow",
	}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(occupy)

	// Wait for that send to enter the blocked state.
	<-blockCh

	// Now send a second request. The critical test: HandleMessage must return
	// promptly even though sendFn is currently blocked by the first response.
	ct := sendRequest(t, session, "ch-block", &leapmuxv1.InnerRpcRequest{
		Method: "slow",
	}, 0, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)

	done := make(chan struct{})
	go func() {
		mgr.HandleMessage(ct)
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
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Handle("ping", Registration{}, func(_ context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse([]byte("pong-" + caller.UserID.String())))
	})
	mgr.SetDispatcher(dispatcher)

	session1 := performHandshake(t, mgr, kp, "ch-a", "alice")
	session2 := performHandshake(t, mgr, kp, "ch-b", "bob")

	// Send on ch-a.
	ct1 := sendRequest(t, session1, "ch-a", &leapmuxv1.InnerRpcRequest{Method: "ping"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct1)

	// Send on ch-b.
	ct2 := sendRequest(t, session2, "ch-b", &leapmuxv1.InnerRpcRequest{Method: "ping"}, 2, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct2)

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
	pt, err := channelwire.OpenChannelMessage(session, chMsg)
	require.NoError(t, err)
	return pt, chMsg
}

func TestSendEncrypted_SingleChunk(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-single", "user-1")

	// Send a small request.
	ct := sendRequest(t, session, "ch-single", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("small"),
	}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]
	chMsg := lastMsg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	// Single-chunk response should have Flags=UNSPECIFIED.
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())
}

func TestSender_SealsEachChunkWithItsOwnEnvelope(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	// Create a handler that sends a large payload.
	largePayload := make([]byte, contracts.MaxPlaintextPerChunk+100)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	dispatcher := NewDispatcher()
	dispatcher.Handle("big", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(largePayload))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-multi", "user-1")

	ct := sendRequest(t, session, "ch-multi", &leapmuxv1.InnerRpcRequest{Method: "big"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

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
	t.Parallel()

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
				Kind: &leapmuxv1.InnerMessage_Response{Response: channelwire.NewPayloadResponse(make([]byte, size))},
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
	exactPayloadSize := findPayloadSize(contracts.MaxPlaintextPerChunk)
	require.Greater(t, exactPayloadSize, 0)

	dispatcher.Handle("exact", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(make([]byte, exactPayloadSize)))
	})

	ct := sendRequest(t, session, "ch-exact", &leapmuxv1.InnerRpcRequest{Method: "exact"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	// Should fit in 1 chunk.
	msgs := sender.waitForMessages(msgsBefore + 1)
	assert.Equal(t, msgsBefore+1, len(msgs))
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())

	// Now add 1 more byte to the payload, which should cause it to overflow.
	dispatcher.Handle("overflow", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(make([]byte, exactPayloadSize+1)))
	})

	ct2 := sendRequest(t, session, "ch-exact", &leapmuxv1.InnerRpcRequest{Method: "overflow"}, 2, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore = len(sender.messages())
	mgr.HandleMessage(ct2)

	// Should produce exactly 2 chunks.
	msgs = sender.waitForMessages(msgsBefore + 2)
	assert.Equal(t, msgsBefore+2, len(msgs))
}

func TestReassembly_E2E(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)

	var receivedPayload []byte
	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		receivedPayload = req.GetPayload()
		_ = s.SendResponse(channelwire.NewPayloadResponse([]byte("ok")))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-reasm", "user-1")

	// Build a large InnerMessage, then chunk it manually on the initiator side.
	largePayload := make([]byte, contracts.MaxPlaintextPerChunk*2+100)
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
		end := offset + contracts.MaxPlaintextPerChunk
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		offset = end

		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if offset < len(data) {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		ct := sealChannelPlaintext(t, session, "ch-reasm", 42, flags, chunk)
		mgr.HandleMessage(ct)
	}

	// Wait for the handler response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	_ = msgs // Just ensuring the handler was called.

	// The handler should have received the full payload.
	assert.Equal(t, largePayload, receivedPayload)
}

func TestReassembly_MaxSizeExceeded(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxsize", "user-1")

	// Send chunks that exceed max size.
	chunk1 := make([]byte, 60)
	ct1 := sealChannelPlaintext(t, session, "ch-maxsize", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk1)
	mgr.HandleMessage(ct1)

	chunk2 := make([]byte, 60)
	ct2 := sealChannelPlaintext(t, session, "ch-maxsize", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk2)

	// This should be dropped (total 120 > 100 max).
	mgr.HandleMessage(ct2)

	// Subsequent messages on other correlation IDs should still work: the oversize
	// drop errors only its own request, never the shared channel.
	before := len(sender.messages())
	ct3 := sendRequest(t, session, "ch-maxsize", &leapmuxv1.InnerRpcRequest{Method: "anything"}, 2, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct3)

	// An UNIMPLEMENTED error comes back, proving the channel is still functional.
	msgs := sender.waitForMessages(before + 1)
	require.NotEmpty(t, msgs)
}

func TestReassembly_MaxIncompleteExceeded(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManagerWith(t, 0, 2)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxinc", "user-1")

	// Start 2 chunked sequences.
	for i := uint64(1); i <= 2; i++ {
		chunk := make([]byte, 10)
		ct := sealChannelPlaintext(t, session, "ch-maxinc", i, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk)
		mgr.HandleMessage(ct)
	}

	// 3rd chunked sequence should be silently dropped.
	chunk := make([]byte, 10)
	ct := sealChannelPlaintext(t, session, "ch-maxinc", 3, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk)
	mgr.HandleMessage(ct)

	// Verify the session still works by completing one of the sequences.
	finalChunk := make([]byte, 10)
	ctFinal := sealChannelPlaintext(t, session, "ch-maxinc", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, finalChunk)

	// This completes correlation 1, freeing a slot. It won't unmarshal cleanly
	// but proves the reassembly path isn't stuck.
	mgr.HandleMessage(ctFinal)
}

func TestSendEncrypted_MaxMessageSizeExceeded(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManagerWith(t, 100, 0) // Very small limit.

	// The handler runs on the dispatcher's goroutine, so its error must come
	// BACK to the test body. An assert inside the handler is not a test
	// result: a dispatch that never reaches the handler leaves the test green,
	// and one that arrives after the test finishes asserts on a dead *T.
	sendErr := make(chan error, 1)
	dispatcher := NewDispatcher()
	dispatcher.Handle("big", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		sendErr <- s.SendResponse(channelwire.NewPayloadResponse(make([]byte, 200)))
	})
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-sendmax", "user-1")
	before := len(sender.messages())

	ct := sendRequest(t, session, "ch-sendmax", &leapmuxv1.InnerRpcRequest{Method: "big"}, 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(ct)

	select {
	case err := <-sendErr:
		require.ErrorIs(t, err, ErrMessageRejected,
			"the size cap is a per-message rejection, not a dead transport")
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran; the size cap was never exercised")
	}

	// The rejection happens before the write, so nothing over the cap reaches
	// the transport.
	assert.Len(t, sender.messages(), before, "an over-cap response must not be sent")
}

func TestReassembly_FinalChunkExceedsMaxSize(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-finalover", "user-1")

	// Send a first chunk within limits (60 bytes).
	chunk1 := make([]byte, 60)
	ct1 := sealChannelPlaintext(t, session, "ch-finalover", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk1)
	mgr.HandleMessage(ct1)

	// Send a final chunk that pushes total over the limit (60 + 60 = 120 > 100).
	chunk2 := make([]byte, 60)
	ct2 := sealChannelPlaintext(t, session, "ch-finalover", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chunk2)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct2)

	// Should get a RESOURCE_EXHAUSTED error response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.NotNil(t, resp.GetError())
	assert.Equal(t, int32(8), resp.GetError().GetCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetError().GetMessage(), "too large")
	assert.Contains(t, resp.GetError().GetMessage(), "exceeds")
}

func TestReassembly_ErrorResponseContent(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManagerWith(t, 100, 0)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-errcontent", "user-1")

	// Send two MORE chunks that exceed the limit.
	chunk1 := make([]byte, 60)
	ct1 := sealChannelPlaintext(t, session, "ch-errcontent", 42, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk1)
	mgr.HandleMessage(ct1)

	chunk2 := make([]byte, 60)
	ct2 := sealChannelPlaintext(t, session, "ch-errcontent", 42, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk2)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct2)

	// Verify the full error response structure.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	chMsg := lastMsg.GetChannelMessageResp()
	require.NotNil(t, chMsg)
	assert.Equal(t, uint64(42), chMsg.GetCorrelationId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chMsg.GetFlags())

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.NotNil(t, resp.GetError())
	assert.Equal(t, int32(8), resp.GetError().GetCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetError().GetMessage(), "120 bytes exceeds 100 byte limit")
}

func TestReassembly_MaxIncompleteErrorResponse(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManagerWith(t, 0, 2)

	dispatcher := NewDispatcher()
	mgr.SetDispatcher(dispatcher)

	session := performHandshake(t, mgr, kp, "ch-maxincerr", "user-1")

	// Start 2 chunked sequences.
	for i := uint64(1); i <= 2; i++ {
		chunk := make([]byte, 10)
		ct := sealChannelPlaintext(t, session, "ch-maxincerr", i, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk)
		mgr.HandleMessage(ct)
	}

	// 3rd should trigger error response.
	chunk := make([]byte, 10)
	ct := sealChannelPlaintext(t, session, "ch-maxincerr", 3, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	// Should get a RESOURCE_EXHAUSTED error response.
	msgs := sender.waitForMessages(msgsBefore + 1)
	lastMsg := msgs[len(msgs)-1]

	resp := decryptInnerResponse(t, session, lastMsg)
	assert.NotNil(t, resp.GetError())
	assert.Equal(t, int32(8), resp.GetError().GetCode()) // RESOURCE_EXHAUSTED
	assert.Contains(t, resp.GetError().GetMessage(), "too many incomplete")
}

func TestHandleClose_MidChunk(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, "ch-midclose", "user-1")

	// Start a chunked sequence.
	chunk := make([]byte, 10)
	ct := sealChannelPlaintext(t, session, "ch-midclose", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, chunk)
	mgr.HandleMessage(ct)

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
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	sender := newCollectSender()

	var closedMu sync.Mutex
	var closedChannels []string
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, sender.trySend, 0, 0)
	mgr.SetOnChannelClose(func(channelID string) {
		closedMu.Lock()
		closedChannels = append(closedChannels, channelID)
		closedMu.Unlock()
	})

	session := performHandshake(t, mgr, ck, "ch-decrypt-fail", "user-1")

	countBefore := len(sender.messages())

	// Send a message with corrupted ciphertext.
	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ChannelId:     "ch-decrypt-fail",
		Ciphertext:    []byte("this-is-not-valid-ciphertext"),
		CorrelationId: 42,
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
	ct := sealChannelPlaintext(t, session, "ch-decrypt-fail", 43, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("should be ignored"))
	mgr.HandleMessage(ct)

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
	t.Parallel()

	const channelID = "ch-poison"
	const correlationID = uint64(7)
	mgr, kp, sender := setupTestManagerWith(t, 100, 4)
	mgr.SetDispatcher(NewDispatcher())

	session := performHandshake(t, mgr, kp, channelID, "user-1")

	sendChunk := func(size int, more bool) {
		t.Helper()
		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if more {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		ct := sealChannelPlaintext(t, session, channelID, correlationID, flags, make([]byte, size))
		mgr.HandleMessage(ct)
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
	require.NotNil(t, resp.GetError())
	require.Equal(t, int32(8), resp.GetError().GetCode()) // RESOURCE_EXHAUSTED
	require.Contains(t, resp.GetError().GetMessage(), "120 bytes exceeds 100 byte limit")

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
	t.Parallel()

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
			sender.queueError(errorSend{requestID: uint64(i), code: 1, message: "overflow"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queueError blocked on a full queue")
	}
	assert.Len(t, sender.errorSends, errorSendQueueSize, "the queue holds exactly its cap; overflow is dropped")
}

// TestSendFailureLevel covers the classification that keeps one disconnect from
// producing one warning per stream that happened to be open when it landed.
func TestSendFailureLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want slog.Level
	}{
		{"transport gone", ErrTransportGone, slog.LevelDebug},
		{"wrapped transport gone", fmt.Errorf("send channel message: %w", ErrTransportGone), slog.LevelDebug},
		{"send aborted at the gate", channelwire.ErrSendAborted, slog.LevelDebug},
		{
			// The channel is healthy and the frame was still lost, so this one
			// stays a warning -- the whole point of keeping ErrMessageRejected a
			// separate sentinel is that it is NOT a teardown.
			name: "message rejected on a live channel",
			err:  ErrMessageRejected,
			want: slog.LevelWarn,
		},
		{"unclassified failure", errors.New("boom"), slog.LevelWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sendFailureLevel(tt.err))
		})
	}
}

// TestSendStream_ReportsAGoneTransportAtDebug closes the chain the two tests
// around it only cover in pieces: the sentinel reaches logSendFailure through
// a real send, and a disconnect therefore costs a Debug line rather than a
// warning per stream that was open when it landed.
func TestSendStream_ReportsAGoneTransportAtDebug(t *testing.T) {
	buf := testutil.CaptureDefaultLogger(t)

	workerSession, _ := setupTestSessions(t)
	sender := &boundSender{
		sender: &channelSender{
			channelID:      "test-ch",
			session:        workerSession,
			sendFn:         func(*leapmuxv1.ConnectRequest) error { return ErrTransportGone },
			maxReassembled: 1 << 20,
		},
		requestID: 10,
		method:    leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
	}

	require.Error(t, sender.SendStream(&leapmuxv1.InnerStreamMessage{End: true}))

	out := buf.String()
	assert.NotContains(t, out, "level=WARN")
	assert.Contains(t, out, "level=DEBUG")
	assert.Contains(t, out, "failed to send inner stream message",
		"the diagnostic itself is kept -- only its level changes")
	assert.Contains(t, out, "method="+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
		"and it keeps the attributes that make it diagnosable")
}

// A rekey reply that a closing session refuses to send is the expected end of
// the session, so its failure logs at debug level, as every other send that a
// teardown refuses does. A rekey request can reach a session after its close
// stopped sends: HandleMessage holds the session that it looked up, and a close
// from another goroutine, such as CloseAll on a disconnect, does not wait for it.
// A reply that a live session fails to send is still a fault, and keeps its
// level.
func TestHandleRekeyRequest_LogsAFailedReplyAtTheLevelOfItsCause(t *testing.T) {
	tooSoon := time.Now()
	due := time.Now().Add(-contracts.MinRekeyInterval - time.Second)
	writeFailed := errors.New("write failed")
	cases := map[string]struct {
		lastRekeyAt time.Time
		closing     bool
		message     string
		wantLevel   string
	}{
		"reject of a closing session": {lastRekeyAt: tooSoon, closing: true, message: "failed to send rekey reject", wantLevel: "DEBUG"},
		"ack of a closing session":    {lastRekeyAt: due, closing: true, message: "failed to send rekey ack", wantLevel: "DEBUG"},
		"reject of a live session":    {lastRekeyAt: tooSoon, message: "failed to send rekey reject", wantLevel: "WARN"},
		"ack of a live session":       {lastRekeyAt: due, message: "failed to send rekey ack", wantLevel: "ERROR"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ck, err := noiseutil.GenerateCompositeKeypair()
			require.NoError(t, err)
			var sent atomic.Int32
			sendFn := func(*leapmuxv1.ConnectRequest) error {
				sent.Add(1)
				return writeFailed
			}
			mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sendFn, func(*leapmuxv1.ConnectRequest) bool { return true }, 0, 0)
			const channelID = "ch-rekey-reply"
			t.Cleanup(func() { mgr.HandleClose(channelID) })
			performHandshake(t, mgr, ck, channelID, "user-rekey-reply")
			sess, ok := mgr.getSession(channelID)
			require.True(t, ok)
			sess.lastRekeyAt = tc.lastRekeyAt
			envelope, _ := initiatorStartRekey(t, ck.MlkemPublicKeyBytes())

			buf := testutil.CaptureDefaultLogger(t)
			if tc.closing {
				sess.stopSends()
			}
			sess.handleRekeyRequest(9, envelope.GetRekeyRequest())

			var failure []string
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.Contains(line, tc.message) {
					failure = append(failure, line)
				}
			}
			require.Len(t, failure, 1, "the failure logs one line, and the diagnostic itself is kept")
			assert.Contains(t, failure[0], "level="+tc.wantLevel)
			if tc.closing {
				assert.NotContains(t, buf.String(), "level=WARN", "a closing session logs no warning")
				assert.NotContains(t, buf.String(), "level=ERROR", "a closing session logs no error")
				assert.Zero(t, sent.Load(), "a closing session writes no rekey reply")
			} else {
				assert.Equal(t, int32(1), sent.Load(), "a live session tries to write its rekey reply")
			}
		})
	}
}

// The send log reads the error from the reply's error field, which is now the
// only thing that marks a failure. A reply without one logs as a success.
func TestBoundSenderLogsTheErrorOfAReply(t *testing.T) {
	workerSession, _ := setupTestSessions(t)
	newSender := func() *boundSender {
		return &boundSender{
			sender: &channelSender{
				channelID:      "test-ch",
				session:        workerSession,
				sendFn:         func(*leapmuxv1.ConnectRequest) error { return nil },
				maxReassembled: 1 << 20,
			},
			requestID: 10,
			method:    leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
		}
	}

	t.Run("an error response", func(t *testing.T) {
		buf := testutil.CaptureDefaultLogger(t)
		require.NoError(t, newSender().SendResponse(channelwire.NewErrorResponse(5, "denied")))
		out := buf.String()
		assert.Contains(t, out, "is_error=true")
		assert.Contains(t, out, "error_code=5")
		assert.Contains(t, out, "error_message=denied")
	})

	t.Run("an error stream frame", func(t *testing.T) {
		buf := testutil.CaptureDefaultLogger(t)
		require.NoError(t, newSender().SendStream(channelwire.NewStreamError(13, "broken")))
		out := buf.String()
		assert.Contains(t, out, "end=true")
		assert.Contains(t, out, "is_error=true")
		assert.Contains(t, out, "error_code=13")
		assert.Contains(t, out, "error_message=broken")
	})

	t.Run("a successful response", func(t *testing.T) {
		buf := testutil.CaptureDefaultLogger(t)
		require.NoError(t, newSender().SendResponse(channelwire.NewPayloadResponse([]byte("ok"))))
		out := buf.String()
		assert.Contains(t, out, "is_error=false")
		assert.Contains(t, out, "error_code=0")
		assert.Contains(t, out, "payload_len=2")
	})
}

// TestSendStream_SurfacesTransportGone pins that the sentinel survives the
// wrapping sendEncryptedAfter does, which is what sendFailureLevel and
// service.transportDead both match on.
func TestSendStream_SurfacesTransportGone(t *testing.T) {
	t.Parallel()

	workerSession, _ := setupTestSessions(t)
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         func(*leapmuxv1.ConnectRequest) error { return ErrTransportGone },
		maxReassembled: 1 << 20,
	}

	err := cs.sendStream(1, &leapmuxv1.InnerStreamMessage{End: true})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTransportGone)
	assert.NotErrorIs(t, err, ErrMessageRejected,
		"a gone transport is not a per-message rejection")
}

// TestSendEncrypted_OversizedMessageIsRejectedNotFatal pins that the
// size cap reports itself as ErrMessageRejected. The distinction is
// load-bearing for fan-out callers: the channel is still healthy, so a
// broadcast must drop the one event rather than unsubscribe the client.
// Without the sentinel the two are indistinguishable and a single
// oversized payload silently deafens a live tab.
func TestSendEncrypted_OversizedMessageIsRejectedNotFatal(t *testing.T) {
	t.Parallel()

	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxReassembled: 8, // smaller than any real envelope
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
	t.Parallel()

	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
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
	t.Parallel()

	workerSession, _ := setupTestSessions(t)

	collector := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         collector.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	// Exactly the ceiling a producer is allowed to emit.
	err := cs.sendStream(1, &leapmuxv1.InnerStreamMessage{
		Payload: make([]byte, contracts.MaxMessageSize),
	})

	require.NoError(t, err,
		"a payload at MaxMessageSize must fit once the envelopes are added")
	assert.NotEmpty(t, collector.messages(), "and must actually reach the transport")
}

// A small single-chunk response must be able to overtake a concurrent
// multi-chunk send: the chunked permit is held across the big message, but the
// frame permit is released between chunks.
func TestChannelSenderSmallResponseOvertakesMultiChunk(t *testing.T) {
	t.Parallel()

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
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	big := make([]byte, contracts.MaxPlaintextPerChunk+100)
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
		smallDone <- cs.sendResponse(2, channelwire.NewPayloadResponse([]byte("hi")))
	}()

	// Wait for the small send to be QUEUED on the permit, do not guess at it.
	// Go hands a buffered-channel slot to waiting senders in FIFO order, so
	// once the small send is queued it necessarily wins the acquire that
	// follows the first chunk's release -- which is the property under test.
	// Sizing that window with a sleep instead made the test fail on a loaded
	// machine: the sleep expired before the goroutine parked, and the big
	// send re-acquired for its second chunk, and the ordering assertion below
	// failed for a scheduling reason rather than a code one.
	require.Eventually(t, func() bool { return cs.gate.FrameWaiters() == 1 },
		2*time.Second, 100*time.Microsecond,
		"the small response never parked on the frame permit")
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
		_, err := channelwire.OpenChannelMessage(initiatorSession, fr)
		require.NoError(t, err, "peer must decrypt every frame in arrival order")
	}
}

// Two concurrent multi-chunk sends must never overlap their MORE runs: the
// Hub's chunk tracker admits at most one in-flight chunked sequence.
func TestChannelSenderConcurrentMultiChunkNeverOverlapMORERuns(t *testing.T) {
	t.Parallel()

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
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	big := make([]byte, 2*contracts.MaxPlaintextPerChunk+1)
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
	t.Parallel()

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
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
		lifetime:       life,
		errorSends:     make(chan errorSend, errorSendQueueSize),
	}

	go func() {
		_ = cs.sendResponse(1, channelwire.NewPayloadResponse([]byte("hold")))
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("holding send never started")
	}

	done := make(chan error, 1)
	go func() {
		done <- cs.sendResponse(2, channelwire.NewPayloadResponse([]byte("parked")))
	}()
	select {
	case err := <-done:
		t.Fatalf("second send returned while the frame permit was held: %v", err)
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
// An unknown method has no registration to state a reply shape, so the queued
// error is unary.
func TestDispatchAsyncUnknownMethodIsQueued(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	w := &queueTrackingWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "no-such-method"}, w)
	assert.Equal(t, int32(1), w.queued.Load(), "unknown method must go through QueueError")
	assert.Zero(t, w.queuedStream.Load(), "an unknown method has no stream to refuse")
	assert.Zero(t, w.sent.Load(), "unknown method must not SendError inline on an errorQueuer")
}

// A streaming method whose stream cannot be prepared is refused through the
// queue, as a stream error frame, before its handler runs. The client holds the
// correlation id as a stream, so a unary error there carries no End frame.
func TestDispatchAsync_QueuesAStreamErrorForAStreamItCannotPrepare(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var invoked atomic.Bool
	d.Handle("stream", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		invoked.Store(true)
	})
	w := &unpreparableStreamWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "stream"}, w)

	assert.Equal(t, int32(1), w.queuedStream.Load(), "the refusal must be a queued stream error")
	assert.Equal(t, int32(codes.FailedPrecondition), w.lastCode.Load())
	assert.Zero(t, w.queued.Load(), "a streaming method is never refused with a unary error")
	assert.Zero(t, w.sent.Load(), "the refusal must not be sent inline on the receive goroutine")
	// DispatchAsync launches no goroutine for a stream it could not prepare, so
	// the handler cannot run after this point either.
	assert.False(t, invoked.Load(), "a stream that cannot be prepared never reaches its handler")
}

type queueTrackingWriter struct {
	queued, queuedStream, sent atomic.Int32
	lastCode                   atomic.Int32
}

func (w *queueTrackingWriter) SendResponse(*leapmuxv1.InnerRpcResponse) error { return nil }
func (w *queueTrackingWriter) SendError(int32, string) error {
	w.sent.Add(1)
	return nil
}
func (w *queueTrackingWriter) SendStream(*leapmuxv1.InnerStreamMessage) error {
	w.sent.Add(1)
	return nil
}
func (*queueTrackingWriter) ChannelID() string     { return "" }
func (*queueTrackingWriter) MaxPayloadBudget() int { return 0 }
func (*queueTrackingWriter) BindStream(StreamController) (func(), bool) {
	return func() {}, false
}
func (w *queueTrackingWriter) QueueError(code int32, _ string) {
	w.lastCode.Store(code)
	w.queued.Add(1)
}
func (w *queueTrackingWriter) QueueStreamError(code int32, _ string) {
	w.lastCode.Store(code)
	w.queuedStream.Add(1)
}

// unpreparableStreamWriter is a queueTrackingWriter whose stream can never be
// prepared, as a boundSender's cannot once its session closed the registry.
type unpreparableStreamWriter struct{ queueTrackingWriter }

func (*unpreparableStreamWriter) prepareStream() (func(), bool) { return nil, false }

// Decrypt-failure CLOSE must go through trySendFn BEFORE HandleClose tears the
// session down, so the CLOSE is enqueued on the connection writer's FIFO ahead
// of teardown.
func TestHandleMessage_DecryptFailureCLOSEBeforeHandleClose(t *testing.T) {
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	const channelID = "ch-decrypt-order"
	var (
		mu    sync.Mutex
		order []string
	)

	sender := newCollectSender()
	var mgr *Manager
	mgr = NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, sender.send, func(msg *leapmuxv1.ConnectRequest) bool {
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
	}, 0, 0)
	mgr.SetOnChannelClose(func(string) {
		mu.Lock()
		order = append(order, "handleClose")
		mu.Unlock()
	})
	_ = performHandshake(t, mgr, ck, channelID, "user-1")

	mgr.HandleMessage(&leapmuxv1.ChannelMessage{
		ChannelId:     channelID,
		Ciphertext:    []byte("not-valid-ciphertext"),
		CorrelationId: 1,
	})

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	assert.Equal(t, []string{"trySend", "handleClose"}, got)
}

func TestHandleMessage_RekeyPostQuantumRoundTrip(t *testing.T) {
	mgr, key, sender := setupTestManager(t)
	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, writer ResponseWriter) {
		_ = writer.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)
	const channelID = "ch-pq-rekey"
	initiator := performHandshake(t, mgr, key, channelID, "user-1")
	mgr.mu.RLock()
	sess := mgr.sessions[channelID]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)

	envelope, material := initiatorStartRekey(t, key.MlkemPublicKeyBytes())
	frame, err := encryptInner(t, initiator, envelope, channelID, 55, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	require.NoError(t, err)
	mgr.HandleMessage(frame)
	messages := sender.waitForMessages(1)
	ackFrame := messages[0].GetChannelMessageResp()
	plaintext, err := channelwire.OpenChannelMessage(initiator, ackFrame)
	require.NoError(t, err)
	var ackEnvelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plaintext, &ackEnvelope))
	ack := ackEnvelope.GetRekeyAck()
	require.NotNil(t, ack)
	require.NotEmpty(t, ack.GetMlkemEphemeralCt())
	initiatorApplyAck(t, initiator, material, ack)

	request := sendRequest(t, initiator, channelID, &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("after"),
	}, 56, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(request)
	messages = sender.waitForMessages(2)
	response := decryptInnerResponse(t, initiator, messages[1])
	assert.Equal(t, []byte("after"), response.GetPayload())
}

func TestHandleMessage_RekeyAcceptAndReject(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-rekey", "user-rekey")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-rekey"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)

	// sendRekey builds a fresh-DH + ML-KEM RekeyRequest the way the tunnel
	// initiator does, and returns the initiator material to combine with the
	// Ack's responder ephemeral.
	sendRekey := func(corr uint64) *rekeyInitiator {
		t.Helper()
		envelope, ri := initiatorStartRekey(t, kp.MlkemPublicKeyBytes())
		pt, err := proto.Marshal(envelope)
		require.NoError(t, err)
		ct := sealChannelPlaintext(t, initiator, "ch-rekey", corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)
		mgr.HandleMessage(ct)
		return ri
	}

	decryptOutcome := func(msg *leapmuxv1.ConnectRequest) (kind string, retryAfterMs int64, ack *leapmuxv1.RekeyAck) {
		t.Helper()
		chMsg := msg.GetChannelMessageResp()
		require.NotNil(t, chMsg)
		pt, err := channelwire.OpenChannelMessage(initiator, chMsg)
		require.NoError(t, err)
		var envelope leapmuxv1.InnerMessage
		require.NoError(t, proto.Unmarshal(pt, &envelope))
		switch k := envelope.GetKind().(type) {
		case *leapmuxv1.InnerMessage_RekeyAck:
			return "ack", 0, k.RekeyAck
		case *leapmuxv1.InnerMessage_RekeyReject:
			ms := k.RekeyReject.GetRetryAfterMs()
			assert.Greater(t, ms, int64(0), "age-only Reject must advertise retry_after_ms")
			return "reject", ms, nil
		default:
			t.Fatalf("unexpected kind %T", envelope.GetKind())
			return "", 0, nil
		}
	}

	// Too soon after handshake → Reject, keys unchanged (nonce still advances
	// from the Request/Reject frames themselves).
	recvBeforeReject := sess.Session.Receive.Nonce()
	msgsBefore := len(sender.messages())
	sendRekey(100)
	msgs := sender.waitForMessages(msgsBefore + 1)
	kind, retryMs, _ := decryptOutcome(msgs[len(msgs)-1])
	assert.Equal(t, "reject", kind)
	assert.InDelta(t, contracts.MinRekeyInterval.Milliseconds(), retryMs,
		float64(2*time.Second.Milliseconds()),
		"fresh handshake Reject should advertise ~MinRekeyInterval")
	// Receive advanced by the Request decrypt only; Rekey did not reset it.
	assert.Equal(t, recvBeforeReject+1, sess.Session.Receive.Nonce())

	// Age past MinRekeyInterval → Ack and both sides can continue.
	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)
	msgsBefore = len(sender.messages())
	ri := sendRekey(101)
	msgs = sender.waitForMessages(msgsBefore + 1)
	kind, _, ack := decryptOutcome(msgs[len(msgs)-1])
	assert.Equal(t, "ack", kind)
	// Initiator applies Ack the way tunnel.Channel does.
	initiatorApplyAck(t, initiator, ri, ack)
	assert.Equal(t, uint64(0), sess.Session.Send.Nonce())
	assert.Equal(t, uint64(0), sess.Session.Receive.Nonce())
	assert.Equal(t, uint64(0), initiator.Send.Nonce())
	assert.Equal(t, uint64(0), initiator.Receive.Nonce())

	// Post-rekey RPC still works.
	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)
	ct := sendRequest(t, initiator, "ch-rekey", &leapmuxv1.InnerRpcRequest{
		Method:  "echo",
		Payload: []byte("after-rekey"),
	}, 102, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	msgsBefore = len(sender.messages())
	mgr.HandleMessage(ct)
	msgs = sender.waitForMessages(msgsBefore + 1)
	innerResp := decryptInnerResponse(t, initiator, msgs[len(msgs)-1])
	assert.Equal(t, []byte("after-rekey"), innerResp.GetPayload())
}

// TestHandleMessage_RekeyBadEphemeralCancels covers the worker's fail-closed
// validation: a RekeyRequest whose dh_pub is not a 32-byte X25519 point must
// cancel the session and send no Ack/Reject, rather than rotating onto a
// derivation the peer could predict. The fresh-DH agreement is what closes the
// #321 forward-secrecy gap, so a peer that did not run it must not advance keys.
func TestHandleMessage_RekeyBadEphemeralCancels(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-badpub", "user-badpub")

	const channelID = "ch-badpub"
	mgr.mu.RLock()
	sess := mgr.sessions[channelID]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)

	// Build a RekeyRequest with a too-short dh_pub the way a non-compliant
	// initiator would.
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyRequest{RekeyRequest: &leapmuxv1.RekeyRequest{
			DhPub: []byte("too-short"),
		}},
	}
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, channelID, 200, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	// sess.cancel() fires synchronously: the session's lifetime context is done.
	require.Error(t, sess.ctx.Err(), "bad ephemeral must cancel the session lifetime")
	// No Ack/Reject was sent: the worker fails closed silently.
	assert.Equal(t, msgsBefore, len(sender.messages()), "bad ephemeral must send no response")
}

func TestHandleMessage_RekeyBadMlkemCtCancels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*leapmuxv1.RekeyRequest)
	}{
		{name: "malformed static ciphertext", change: func(req *leapmuxv1.RekeyRequest) {
			req.MlkemStaticCt = []byte("not-a-valid-mlkem-ciphertext")
		}},
		{name: "ephemeral key of the wrong size", change: func(req *leapmuxv1.RekeyRequest) {
			req.MlkemEphemeralPub = []byte("too-short")
		}},
		{name: "invalid ephemeral key", change: func(req *leapmuxv1.RekeyRequest) {
			req.MlkemEphemeralPub = bytes.Repeat([]byte{0xFF}, contracts.MlkemPublicKeySize)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, kp, _ := setupTestManager(t)
			const channelID = "ch-badct"
			initiator := performHandshake(t, mgr, kp, channelID, "user-badct")
			mgr.mu.RLock()
			sess := mgr.sessions[channelID]
			mgr.mu.RUnlock()
			require.NotNil(t, sess)
			sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)

			seed, err := noiseutil.GenerateEphemeralX25519Seed()
			require.NoError(t, err)
			defer noiseutil.Zero(seed)
			publicKey, err := noiseutil.X25519PublicFromSeed(seed)
			require.NoError(t, err)
			offer, err := noiseutil.OfferRekeyPQ(kp.MlkemPublicKeyBytes())
			require.NoError(t, err)
			defer offer.Zero()
			req := &leapmuxv1.RekeyRequest{
				DhPub:             publicKey,
				MlkemStaticCt:     offer.StaticCiphertext,
				MlkemEphemeralPub: offer.EphemeralPublicKey,
			}
			tc.change(req)
			envelope := &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_RekeyRequest{RekeyRequest: req}}
			pt, err := proto.Marshal(envelope)
			require.NoError(t, err)
			ct := sealChannelPlaintext(t, initiator, channelID, 201, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)
			mgr.HandleMessage(ct)
			require.Error(t, sess.ctx.Err(), "invalid ML-KEM material must cancel the session lifetime")
		})
	}
}

// TestHandleMessage_RekeyClassicChannel covers the classic rekey's empty
// ML-KEM fields, its fresh-DH-only ratchet, and traffic under the new keys.
func TestHandleMessage_RekeyClassicChannel(t *testing.T) {
	t.Parallel()

	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	sender := newCollectSender()
	mgr := NewManager(testWorkerID, ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, sender.send, sender.trySend, 0, 0)

	const channelID = "ch-classic-rekey"
	initiator := performClassicalHandshake(t, mgr, ck, channelID, "user-classic")

	mgr.mu.RLock()
	sess := mgr.sessions[channelID]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)
	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)

	envelope, ri := initiatorStartClassicalRekey(t)
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, channelID, 300, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)

	msgs := sender.waitForMessages(msgsBefore + 1)
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	plain, err := channelwire.OpenChannelMessage(initiator, chMsg)
	require.NoError(t, err)
	var resp leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plain, &resp))
	ack, ok := resp.GetKind().(*leapmuxv1.InnerMessage_RekeyAck)
	require.True(t, ok, "classic rekey must Ack, got %T", resp.GetKind())

	// Both sides rotate onto the classic fresh-DH derivation.
	initiatorApplyAck(t, initiator, ri, ack.RekeyAck)
	assert.Equal(t, uint64(0), sess.Session.Send.Nonce())
	assert.Equal(t, uint64(0), sess.Session.Receive.Nonce())
	assert.Equal(t, uint64(0), initiator.Send.Nonce())
	assert.Equal(t, uint64(0), initiator.Receive.Nonce())

	// Post-rekey traffic round-trips both directions under the new classic key.
	respFrame := sealChannelPlaintext(t, sess.Session, channelID, 300, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("classic-after"))
	respDec, err := channelwire.OpenChannelMessage(initiator, respFrame)
	require.NoError(t, err)
	assert.Equal(t, []byte("classic-after"), respDec)

	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)
	invalidEnvelope, invalidMaterial := initiatorStartClassicalRekey(t)
	defer noiseutil.Zero(invalidMaterial.ephemeralSeed)
	defer invalidMaterial.pq.Zero()
	invalidEnvelope.GetRekeyRequest().MlkemStaticCt = []byte{1}
	invalidPlaintext, err := proto.Marshal(invalidEnvelope)
	require.NoError(t, err)
	invalidFrame := sealChannelPlaintext(t, initiator, channelID, 301, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, invalidPlaintext)
	mgr.HandleMessage(invalidFrame)
	require.Error(t, sess.ctx.Err(), "a classic channel must reject non-empty ML-KEM material")
}

func TestHandleMessage_RekeySoftNonceBypassesMinInterval(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-rekey-soft", "user-rekey-soft")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-rekey-soft"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)

	// Still inside MinRekeyInterval after handshake. Align both ends at the soft
	// ceiling so decrypting the Request advances Receive past SoftNonceLimit and
	// AllowRekey's soft-nonce bypass accepts.
	initiator.Send.SetNonceForTest(contracts.SoftNonceLimit)
	sess.Session.Receive.SetNonceForTest(contracts.SoftNonceLimit)

	envelope, ri := initiatorStartRekey(t, kp.MlkemPublicKeyBytes())
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, "ch-rekey-soft", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)
	msgs := sender.waitForMessages(msgsBefore + 1)
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	plain, err := channelwire.OpenChannelMessage(initiator, chMsg)
	require.NoError(t, err)
	var resp leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plain, &resp))
	ack, ok := resp.GetKind().(*leapmuxv1.InnerMessage_RekeyAck)
	require.True(t, ok, "soft-nonce rekey inside MinRekeyInterval must Ack, got %T", resp.GetKind())

	initiatorApplyAck(t, initiator, ri, ack.RekeyAck)
	assert.Equal(t, uint64(0), sess.Session.Send.Nonce())
	assert.Equal(t, uint64(0), sess.Session.Receive.Nonce())
}

func TestHandleMessage_RekeySendSoftNonceViaPeek(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-rekey-send-soft", "user-rekey-send-soft")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-rekey-send-soft"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)

	// Inside MinRekeyInterval: only worker Send is soft. Align initiator Receive
	// so Ack decrypt still matches after Encrypt under SoftNonceLimit+1.
	// Receive stays under the limit even after decrypting the Request, so Ack
	// must come from peekSendNeedsRekey (not Receive.NeedsRekey).
	sess.Session.Send.SetNonceForTest(contracts.SoftNonceLimit + 1)
	initiator.Receive.SetNonceForTest(contracts.SoftNonceLimit + 1)
	assert.False(t, sess.Session.Receive.NeedsRekey())
	assert.True(t, sess.Session.Send.NeedsRekey())

	envelope, ri := initiatorStartRekey(t, kp.MlkemPublicKeyBytes())
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, "ch-rekey-send-soft", 1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)

	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)
	msgs := sender.waitForMessages(msgsBefore + 1)
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	plain, err := channelwire.OpenChannelMessage(initiator, chMsg)
	require.NoError(t, err)
	var resp leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plain, &resp))
	ack, ok := resp.GetKind().(*leapmuxv1.InnerMessage_RekeyAck)
	require.True(t, ok, "Send soft-nonce via peek must Ack inside MinRekeyInterval, got %T", resp.GetKind())
	assert.False(t, sess.Session.Receive.NeedsRekey(), "Receive must still be under soft after the Request")

	initiatorApplyAck(t, initiator, ri, ack.RekeyAck)
	assert.Equal(t, uint64(0), sess.Session.Send.Nonce())
	assert.Equal(t, uint64(0), sess.Session.Receive.Nonce())
}

func TestHandleMessage_RekeySendHoldsFrameAcrossAck(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-rekey-frame", "user-rekey-frame")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-rekey-frame"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)

	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)
	sendNonceBefore := sess.Session.Send.Nonce()

	// Occupy the frame permit so peek / Ack Encrypt parks inside handleRekeyRequest.
	// RekeySend must not run until that same SendGate frame scope can proceed.
	release := make(chan struct{})
	holding := make(chan struct{})
	go func() {
		_ = sess.sender.gate.WithFrame(context.Background(), sess.sender.lifetime, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	select {
	case <-holding:
	case <-time.After(2 * time.Second):
		t.Fatal("failed to occupy frame permit")
	}

	envelope, ri := initiatorStartRekey(t, kp.MlkemPublicKeyBytes())
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, "ch-rekey-frame", 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)

	msgsBefore := len(sender.messages())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.HandleMessage(ct)
	}()

	select {
	case <-done:
		t.Fatal("HandleMessage returned while frame permit was held")
	case <-time.After(50 * time.Millisecond):
	}
	assert.Equal(t, sendNonceBefore, sess.Session.Send.Nonce(),
		"RekeySend must not run before the frame permit is available")
	assert.Equal(t, msgsBefore, len(sender.messages()), "Ack must not be on the wire yet")

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleMessage did not complete after frame release")
	}

	msgs := sender.waitForMessages(msgsBefore + 1)
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	plain, err := channelwire.OpenChannelMessage(initiator, chMsg)
	require.NoError(t, err)
	var resp leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plain, &resp))
	ack, ok := resp.GetKind().(*leapmuxv1.InnerMessage_RekeyAck)
	require.True(t, ok)
	initiatorApplyAck(t, initiator, ri, ack.RekeyAck)
	assert.Equal(t, uint64(0), sess.Session.Send.Nonce(), "RekeySend runs under the same frame as Ack")
}

func TestHandleMessage_RekeyUnderLoad(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	initiator := performHandshake(t, mgr, kp, "ch-rekey-load", "user-rekey-load")

	mgr.mu.RLock()
	sess := mgr.sessions["ch-rekey-load"]
	mgr.mu.RUnlock()
	require.NotNil(t, sess)

	dispatcher := NewDispatcher()
	dispatcher.Handle("echo", Registration{}, func(_ context.Context, _ Caller, req *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		_ = s.SendResponse(channelwire.NewPayloadResponse(req.GetPayload()))
	})
	mgr.SetDispatcher(dispatcher)

	echo := func(corr uint64, payload string) {
		t.Helper()
		ct := sendRequest(t, initiator, "ch-rekey-load", &leapmuxv1.InnerRpcRequest{
			Method:  "echo",
			Payload: []byte(payload),
		}, corr, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
		msgsBefore := len(sender.messages())
		mgr.HandleMessage(ct)
		msgs := sender.waitForMessages(msgsBefore + 1)
		innerResp := decryptInnerResponse(t, initiator, msgs[len(msgs)-1])
		assert.Equal(t, []byte(payload), innerResp.GetPayload())
	}

	echo(1, "before")

	sess.lastRekeyAt = time.Now().Add(-contracts.MinRekeyInterval - time.Second)
	envelope, ri := initiatorStartRekey(t, kp.MlkemPublicKeyBytes())
	pt, err := proto.Marshal(envelope)
	require.NoError(t, err)
	ct := sealChannelPlaintext(t, initiator, "ch-rekey-load", 2, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, pt)
	msgsBefore := len(sender.messages())
	mgr.HandleMessage(ct)
	msgs := sender.waitForMessages(msgsBefore + 1)
	chMsg := msgs[len(msgs)-1].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	plain, err := channelwire.OpenChannelMessage(initiator, chMsg)
	require.NoError(t, err)
	var resp leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plain, &resp))
	ack, ok := resp.GetKind().(*leapmuxv1.InnerMessage_RekeyAck)
	require.True(t, ok)
	initiatorApplyAck(t, initiator, ri, ack.RekeyAck)

	for i := 0; i < 8; i++ {
		echo(uint64(10+i), "after-"+string(rune('a'+i)))
	}
}

func TestHandleMessage_StreamRequestRoutesToBoundController(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)
	ctrl := &streamRequestRecordingController{}
	ready := make(chan struct{})

	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		release, _ := s.BindStream(ctrl)
		defer release()
		close(ready)
		select {}
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-stream-req", "user-stream-req")

	openCT := sendRequest(t, initiatorSession, "ch-stream-req", &leapmuxv1.InnerRpcRequest{
		Method: "stream", Payload: []byte("open"),
	}, 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(openCT)

	<-ready

	updateCT, err := encryptInner(t, initiatorSession, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_StreamRequest{
			StreamRequest: revisionFrame([]byte("update")),
		},
	}, "ch-stream-req", 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	require.NoError(t, err)
	mgr.HandleMessage(updateCT)

	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return len(ctrl.payloads) == 1 && string(ctrl.payloads[0]) == "update"
	}, time.Second, 10*time.Millisecond)
}

func TestHandleMessage_StreamRequestUnboundIdIsDropped(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	mgr.SetDispatcher(NewDispatcher())
	initiatorSession := performHandshake(t, mgr, kp, "ch-unbound", "user-unbound")

	updateCT, err := encryptInner(t, initiatorSession, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_StreamRequest{
			StreamRequest: revisionFrame([]byte("orphan")),
		},
	}, "ch-unbound", 99, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	require.NoError(t, err)
	// Must not panic or log as "unexpected kind" — unbound is a quiet drop.
	mgr.HandleMessage(updateCT)
	assert.Empty(t, sender.messages())
}

// A client frame that selects neither a revision nor a cancel states nothing
// that the worker could apply. The worker fails the stream with
// InvalidArgument, in a stream error frame on the stream's correlation id, and
// cancels the stream's controller.
func TestHandleMessage_StreamRequestWithNoActionFailsTheStream(t *testing.T) {
	t.Parallel()

	mgr, kp, sender := setupTestManager(t)
	ctrl := &streamRequestRecordingController{}
	ready := make(chan struct{})

	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		release, _ := s.BindStream(ctrl)
		defer release()
		close(ready)
		<-ctx.Done()
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-no-action", "user-no-action")
	openCT := sendRequest(t, initiatorSession, "ch-no-action", &leapmuxv1.InnerRpcRequest{Method: "stream"}, 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(openCT)
	<-ready

	frameCT, err := encryptInner(t, initiatorSession, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_StreamRequest{StreamRequest: &leapmuxv1.InnerStreamRequest{}},
	}, "ch-no-action", 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	require.NoError(t, err)
	mgr.HandleMessage(frameCT)

	require.Eventually(t, func() bool { return len(sender.messages()) > 0 }, 30*time.Second, 10*time.Millisecond)
	first := sender.messages()[0].GetChannelMessageResp()
	require.NotNil(t, first)
	assert.Equal(t, uint64(7), first.GetCorrelationId(), "the error answers the stream's correlation id")
	plaintext, err := channelwire.OpenChannelMessage(initiatorSession, first)
	require.NoError(t, err)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plaintext, &envelope))
	streamErr := envelope.GetStream().GetError()
	require.NotNil(t, streamErr, "the worker answers with a stream error frame, got %T", envelope.GetKind())
	assert.Equal(t, int32(codes.InvalidArgument), streamErr.GetCode())
	assert.Contains(t, streamErr.GetMessage(), "selects no action")
	assert.True(t, envelope.GetStream().GetEnd(), "the error frame ends the stream")

	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return ctrl.cancelled
	}, 30*time.Second, 10*time.Millisecond, "the failed stream's controller is cancelled")
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	assert.Empty(t, ctrl.payloads, "a frame with no action reaches no controller as a revision")
}

func TestHandleClose_CallsOnCancelBeforeSessionCtxCancel(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)
	ctrl := &streamRequestRecordingController{}
	ready := make(chan struct{})
	order := make(chan string, 2)

	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		release, _ := s.BindStream(ctrl)
		defer release()
		close(ready)
		<-ctx.Done()
		order <- "ctx"
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-close-order", "user-close-order")
	openCT := sendRequest(t, initiatorSession, "ch-close-order", &leapmuxv1.InnerRpcRequest{
		Method: "stream", Payload: []byte("open"),
	}, 3, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(openCT)
	<-ready

	origOnCancel := ctrl.onCancelHook
	ctrl.onCancelHook = func() {
		order <- "cancel"
		if origOnCancel != nil {
			origOnCancel()
		}
	}

	mgr.HandleClose("ch-close-order")

	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return ctrl.cancelled
	}, time.Second, 10*time.Millisecond)

	first := <-order
	assert.Equal(t, "cancel", first, "OnCancel must run before sess.ctx cancel reaches the handler")
}

func TestCloseAll_CallsOnCancelBeforeSessionCtxCancel(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)
	ctrl := &streamRequestRecordingController{}
	ready := make(chan struct{})
	order := make(chan string, 2)

	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		release, _ := s.BindStream(ctrl)
		defer release()
		close(ready)
		<-ctx.Done()
		order <- "ctx"
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-closeall-order", "user-closeall-order")
	openCT := sendRequest(t, initiatorSession, "ch-closeall-order", &leapmuxv1.InnerRpcRequest{
		Method: "stream", Payload: []byte("open"),
	}, 3, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(openCT)
	<-ready

	origOnCancel := ctrl.onCancelHook
	ctrl.onCancelHook = func() {
		order <- "cancel"
		if origOnCancel != nil {
			origOnCancel()
		}
	}

	mgr.CloseAll()

	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return ctrl.cancelled
	}, time.Second, 10*time.Millisecond)

	first := <-order
	assert.Equal(t, "cancel", first, "OnCancel must run before sess.ctx cancel reaches the handler")
}

// Closing a channel must send nothing on it. The Hub forgets a channel before
// it tells the Worker to close it, so a frame that the Worker sends while it
// closes the channel reaches a Hub that cannot route the frame. The Hub then
// logs "worker sent message for an unowned channel". WatchEvents sent such a
// frame on every close: the End frame that ends its stream when the stream is
// cancelled.
//
// Each case sends its final frame at a different point of the close:
//   - inside OnCancel, while the session context is still live
//   - from the handler goroutine that OnCancel wakes, which is how WatchEvents
//     sends its End frame. OnCancel waits for that send, so the send happens
//     before the close ends the session context.
//   - from the handler after the session context ends
func TestCloseSendsNothingFromAStreamThatEndsWithTheChannel(t *testing.T) {
	t.Parallel()

	closers := map[string]func(*Manager, string){
		"HandleClose": func(mgr *Manager, channelID string) { mgr.HandleClose(channelID) },
		"CloseAll":    func(mgr *Manager, _ string) { mgr.CloseAll() },
	}
	type finalFrame int
	const (
		fromOnCancel finalFrame = iota
		fromTheGoroutineThatOnCancelWakes
		afterTheSessionContextEnds
	)
	cases := map[string]finalFrame{
		"from OnCancel":                          fromOnCancel,
		"from the goroutine that OnCancel wakes": fromTheGoroutineThatOnCancelWakes,
		"after the session context ends":         afterTheSessionContextEnds,
	}

	for closerName, closeChannel := range closers {
		for caseName, when := range cases {
			t.Run(closerName+"/"+caseName, func(t *testing.T) {
				t.Parallel()

				mgr, kp, sender := setupTestManager(t)
				const channelID = "ch-final-frame"
				end := &leapmuxv1.InnerStreamMessage{End: true}
				ready := make(chan struct{})
				sendErr := make(chan error, 1)

				dispatcher := NewDispatcher()
				dispatcher.Handle("stream", Registration{Streaming: true}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
					streamCtx, endStream := context.WithCancel(ctx)
					defer endStream()
					sent := make(chan struct{})
					ctrl := &streamRequestRecordingController{}
					switch when {
					case fromOnCancel:
						ctrl.onCancelHook = func() { sendErr <- w.SendStream(end) }
					case fromTheGoroutineThatOnCancelWakes:
						ctrl.onCancelHook = func() {
							endStream()
							<-sent
						}
					case afterTheSessionContextEnds:
					}
					release, ok := w.BindStream(ctrl)
					if !ok {
						sendErr <- errors.New("BindStream refused the stream")
						return
					}
					defer release()
					close(ready)
					switch when {
					case fromOnCancel:
						<-ctx.Done()
					case fromTheGoroutineThatOnCancelWakes:
						<-streamCtx.Done()
						sendErr <- w.SendStream(end)
						close(sent)
					case afterTheSessionContextEnds:
						<-ctx.Done()
						sendErr <- w.SendStream(end)
					}
				})
				mgr.SetDispatcher(dispatcher)

				initiatorSession := performHandshake(t, mgr, kp, channelID, "user-final-frame")
				openCT := sendRequest(t, initiatorSession, channelID, &leapmuxv1.InnerRpcRequest{Method: "stream"}, 5, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
				mgr.HandleMessage(openCT)
				<-ready
				require.Empty(t, sender.messages(), "the stream sends nothing before the close")

				closeChannel(mgr, channelID)

				var err error
				select {
				case err = <-sendErr:
				case <-time.After(30 * time.Second):
					t.Fatal("the stream never sent its final frame")
				}
				require.ErrorIs(t, err, channelwire.ErrSendAborted,
					"the close refuses the final frame instead of sending it")
				assert.True(t, IsTransportTeardown(err), "a refused final frame is reported as a teardown")
				assert.Empty(t, sender.messages(), "no frame for a closed channel reaches the Hub")
			})
		}
	}
}

func TestHandleMessage_ChunkedStreamRequestReassembles(t *testing.T) {
	t.Parallel()

	mgr, kp, _ := setupTestManager(t)
	ctrl := &streamRequestRecordingController{}
	ready := make(chan struct{})

	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, s ResponseWriter) {
		release, _ := s.BindStream(ctrl)
		defer release()
		close(ready)
		select {}
	})
	mgr.SetDispatcher(dispatcher)

	initiatorSession := performHandshake(t, mgr, kp, "ch-chunk-sr", "user-chunk-sr")
	openCT := sendRequest(t, initiatorSession, "ch-chunk-sr", &leapmuxv1.InnerRpcRequest{
		Method: "stream", Payload: []byte("open"),
	}, 11, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED)
	mgr.HandleMessage(openCT)
	<-ready

	payload := bytes.Repeat([]byte("x"), contracts.MaxPlaintextPerChunk+50)
	envelope, err := proto.Marshal(&leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_StreamRequest{
			StreamRequest: revisionFrame(payload),
		},
	})
	require.NoError(t, err)

	for offset := 0; offset < len(envelope); {
		end := offset + contracts.MaxPlaintextPerChunk
		if end > len(envelope) {
			end = len(envelope)
		}
		chunk := envelope[offset:end]
		offset = end
		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if offset < len(envelope) {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		ct := sealChannelPlaintext(t, initiatorSession, "ch-chunk-sr", 11, flags, chunk)
		mgr.HandleMessage(ct)
	}

	require.Eventually(t, func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return len(ctrl.payloads) == 1 && bytes.Equal(ctrl.payloads[0], payload)
	}, time.Second, 10*time.Millisecond)
}

type streamRequestRecordingController struct {
	mu           sync.Mutex
	payloads     [][]byte
	cancelled    bool
	onCancelHook func()
}

func (c *streamRequestRecordingController) OnClientFrame(payload []byte) {
	c.mu.Lock()
	c.payloads = append(c.payloads, append([]byte(nil), payload...))
	c.mu.Unlock()
}

func (c *streamRequestRecordingController) OnCancel() {
	c.mu.Lock()
	c.cancelled = true
	hook := c.onCancelHook
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func encryptInner(t *testing.T, session *noiseutil.Session, msg *leapmuxv1.InnerMessage, channelID string, correlationID uint64, flags leapmuxv1.ChannelMessageFlags) (*leapmuxv1.ChannelMessage, error) {
	t.Helper()
	plain, err := proto.Marshal(msg)
	require.NoError(t, err)
	return channelwire.SealChannelMessage(session, channelID, correlationID, flags, plain)
}
