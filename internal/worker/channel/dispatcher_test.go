package channel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// setupTestSessions creates a composite keypair and performs a full hybrid handshake,
// returning the worker and initiator sessions.
func setupTestSessions(t *testing.T) (*noiseutil.Session, *noiseutil.Session) {
	t.Helper()
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := noiseutil.ResponderHandshake(ck, msg1)
	require.NoError(t, err)

	initiatorSession, err := noiseutil.InitiatorHandshake2(hs, msg2, slhdsaPub)
	require.NoError(t, err)

	return workerSession, initiatorSession
}

func TestDispatcher_RegisterAndDispatch(t *testing.T) {
	d := NewDispatcher()

	var calledWith struct {
		userID string
		method string
	}

	d.Register("test.method", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *Sender) {
		calledWith.userID = userID
		calledWith.method = req.GetMethod()
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("ok"),
		})
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: DefaultMaxMessageSize,
	}

	d.Dispatch("user-1", &leapmuxv1.InnerRpcRequest{
		Method: "test.method",
	}, 7, cs)

	assert.Equal(t, "user-1", calledWith.userID)
	assert.Equal(t, "test.method", calledWith.method)

	// Verify response was sent and can be decrypted.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	require.Equal(t, uint32(1), chMsg.GetProtocolVersion())

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.Equal(t, uint32(7), chMsg.GetCorrelationId())
	assert.Equal(t, []byte("ok"), resp.GetPayload())
}

func TestDispatcher_PanicRecovery(t *testing.T) {
	d := NewDispatcher()

	d.Register("panicking", func(_ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {
		panic("test panic in handler")
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: DefaultMaxMessageSize,
	}

	// Dispatch should not panic — the panic should be recovered and
	// an INTERNAL error response should be sent instead.
	require.NotPanics(t, func() {
		d.Dispatch("user-1", &leapmuxv1.InnerRpcRequest{
			Method: "panicking",
		}, 42, cs)
	})

	// Should get an INTERNAL error response.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(13), resp.GetErrorCode()) // INTERNAL
	assert.Contains(t, resp.GetErrorMessage(), "internal error")
}

func TestDispatcher_UnknownMethod(t *testing.T) {
	d := NewDispatcher()
	d.Register("known", func(_ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: DefaultMaxMessageSize,
	}

	d.Dispatch("user-1", &leapmuxv1.InnerRpcRequest{
		Method: "unknown",
	}, 1, cs)

	// Should get an UNIMPLEMENTED error.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	require.Equal(t, uint32(1), chMsg.GetProtocolVersion())

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(12), resp.GetErrorCode())
	assert.Contains(t, resp.GetErrorMessage(), "unknown")
}
