package channelwire

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestCloseReasonsFitTheCloseFrame guards an RFC 6455 constraint the wire
// format itself imposes: a close reason longer than 123 bytes cannot be sent
// (coder/websocket rejects it on send), so a token that outgrew it would not
// be a worse message -- it would be no close frame at all. The values come
// from contracts/wire.json via the generator; this test is the wire-format
// backstop, not a cross-language pin (none is needed: both sides are
// generated from the same file).
func TestCloseReasonsFitTheCloseFrame(t *testing.T) {
	for _, reason := range []string{
		contracts.CloseReasonTooManyConnections, contracts.CloseReasonSnapshotTooLarge,
		contracts.CloseReasonForbidden, contracts.CloseReasonControlFlood,
	} {
		assert.LessOrEqual(t, len(reason), 123,
			"a close reason longer than 123 bytes cannot be sent: %q", reason)
	}
}

// SendChannelFrames is the one place the two Go senders (the worker's
// sendEncrypted and the tunnel's sendInnerContext) frame a chunked message, so
// its split boundaries, MORE flags, and error propagation pin the wire contract
// for both. The empty-payload case is the landmine the boundary math used to
// carry as a standalone helper: it must emit exactly one terminating zero-byte
// frame rather than spin forever.
func TestSendChannelFrames(t *testing.T) {
	run := func(t *testing.T, plaintext []byte) []struct {
		chunk []byte
		flags leapmuxv1.ChannelMessageFlags
	} {
		var frames []struct {
			chunk []byte
			flags leapmuxv1.ChannelMessageFlags
		}
		err := SendChannelFrames(plaintext, func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
			cp := append([]byte(nil), chunk...)
			frames = append(frames, struct {
				chunk []byte
				flags leapmuxv1.ChannelMessageFlags
			}{cp, flags})
			return nil
		})
		require.NoError(t, err)
		return frames
	}

	more := func(i, n int) leapmuxv1.ChannelMessageFlags {
		if i < n-1 {
			return leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		return leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
	}

	t.Run("empty payload emits one terminating zero-byte frame", func(t *testing.T) {
		frames := run(t, nil)
		require.Len(t, frames, 1)
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[0].flags,
			"the sole frame of an empty payload must NOT set MORE")
		assert.Empty(t, frames[0].chunk)
	})

	t.Run("a sub-max payload is one frame without MORE", func(t *testing.T) {
		frames := run(t, []byte("abc"))
		require.Len(t, frames, 1)
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[0].flags)
		assert.Equal(t, []byte("abc"), frames[0].chunk)
	})

	t.Run("a multi-chunk payload splits at contracts.MaxPlaintextPerChunk with MORE on all but the last", func(t *testing.T) {
		// Two full chunks plus a ragged tail.
		plaintext := make([]byte, 2*contracts.MaxPlaintextPerChunk+contracts.MaxPlaintextPerChunk/2)
		for i := range plaintext {
			plaintext[i] = byte(i % 251)
		}
		frames := run(t, plaintext)
		require.Len(t, frames, 3)
		for i, f := range frames {
			assert.Equal(t, more(i, 3), f.flags, "frame %d MORE flag", i)
		}
		reassembled := make([]byte, 0, len(plaintext))
		for _, f := range frames {
			reassembled = append(reassembled, f.chunk...)
		}
		assert.Equal(t, plaintext, reassembled)
	})

	t.Run("an exact-multiple payload has no empty trailing frame", func(t *testing.T) {
		plaintext := make([]byte, 2*contracts.MaxPlaintextPerChunk)
		frames := run(t, plaintext)
		require.Len(t, frames, 2, "an exact two-chunk payload is exactly two frames")
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, frames[0].flags)
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[1].flags)
	})

	t.Run("a sendChunk error aborts and surfaces the caller's error", func(t *testing.T) {
		boom := errors.New("encrypt or write failed")
		var calls int
		err := SendChannelFrames([]byte("abc"), func([]byte, leapmuxv1.ChannelMessageFlags) error {
			calls++
			return boom
		})
		require.ErrorIs(t, err, boom)
		assert.Equal(t, 1, calls, "sendChunk is invoked once before the error aborts")
	})

	t.Run("a mid-message sendChunk error leaves earlier chunks emitted", func(t *testing.T) {
		boom := errors.New("write ws")
		plaintext := make([]byte, contracts.MaxPlaintextPerChunk+1)
		var calls int
		err := SendChannelFrames(plaintext, func([]byte, leapmuxv1.ChannelMessageFlags) error {
			calls++
			if calls == 2 {
				return boom
			}
			return nil
		})
		require.ErrorIs(t, err, boom)
		assert.Equal(t, 2, calls)
	})
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse(5, "not found")
	require.IsType(t, &leapmuxv1.InnerRpcResponse_Error{}, resp.GetResult(), "the error case is what marks the response a failure")
	assert.Equal(t, int32(5), resp.GetError().GetCode())
	assert.Equal(t, "not found", resp.GetError().GetMessage())
	assert.Empty(t, resp.GetPayload())
}

// A success selects the payload case even when the payload is empty, as the
// response of a method whose response message has no field set is. The case
// survives the wire, so a reader that switches on it sees a success.
func TestNewPayloadResponse(t *testing.T) {
	for name, payload := range map[string][]byte{"a payload": []byte("ok"), "an empty payload": nil} {
		t.Run(name, func(t *testing.T) {
			encoded, err := proto.Marshal(NewPayloadResponse(payload))
			require.NoError(t, err)
			var decoded leapmuxv1.InnerRpcResponse
			require.NoError(t, proto.Unmarshal(encoded, &decoded))
			require.IsType(t, &leapmuxv1.InnerRpcResponse_Payload{}, decoded.GetResult())
			assert.Equal(t, len(payload), len(decoded.GetPayload()))
			assert.Nil(t, decoded.GetError())
		})
	}
}

// ErrorOf returns an untyped nil for a nil RpcError, so a caller that returns
// its result as an error returns no error, and the typed error otherwise.
func TestErrorOf(t *testing.T) {
	require.NoError(t, ErrorOf(nil))
	assert.True(t, ErrorOf(nil) == nil, "the result must be an untyped nil, not a nil *RPCError in an interface")

	err := ErrorOf(&leapmuxv1.RpcError{Code: 5, Message: "agent not found"})
	var rpcErr *RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, &RPCError{Code: 5, Message: "agent not found"}, rpcErr)
	assert.EqualError(t, err, "rpc error (code 5): agent not found")

	assert.EqualError(t, ErrorOf(&leapmuxv1.RpcError{}), "rpc error (code 0): ", "an empty error is still an error")
}

// ConnectError keeps the in-band code as the Connect code of the same number.
// Both number every code from Canceled (1) to Unauthenticated (16) alike; any
// other value, OK (0) included, becomes Unknown.
func TestRPCError_ConnectError(t *testing.T) {
	cases := []struct {
		code int32
		want connect.Code
	}{
		{0, connect.CodeUnknown},
		{1, connect.CodeCanceled},
		{2, connect.CodeUnknown},
		{3, connect.CodeInvalidArgument},
		{4, connect.CodeDeadlineExceeded},
		{5, connect.CodeNotFound},
		{6, connect.CodeAlreadyExists},
		{7, connect.CodePermissionDenied},
		{8, connect.CodeResourceExhausted},
		{9, connect.CodeFailedPrecondition},
		{10, connect.CodeAborted},
		{11, connect.CodeOutOfRange},
		{12, connect.CodeUnimplemented},
		{13, connect.CodeInternal},
		{14, connect.CodeUnavailable},
		{15, connect.CodeDataLoss},
		{16, connect.CodeUnauthenticated},
		{17, connect.CodeUnknown},
		{99, connect.CodeUnknown},
		{-1, connect.CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(int(tc.code)), func(t *testing.T) {
			rpcErr := &RPCError{Code: tc.code, Message: "worker said no"}
			got := rpcErr.ConnectError()
			assert.Equal(t, tc.want, got.Code())
			assert.Equal(t, "worker said no", got.Message())
			assert.ErrorIs(t, got, rpcErr, "the in-band error stays the cause")
		})
	}
}

func TestNewStreamError(t *testing.T) {
	msg := NewStreamError(13, "internal error")
	require.NotNil(t, msg.GetError(), "a present error is what marks the frame a failure")
	assert.Equal(t, int32(13), msg.GetError().GetCode())
	assert.Equal(t, "internal error", msg.GetError().GetMessage())
	assert.True(t, msg.GetEnd(), "the frame ends the stream, so a reader that checks only End stops too")
	assert.Empty(t, msg.GetPayload())
}

// A reader decides failure by the presence of the error, not by its code. A
// zero code and an empty message are still a failure, and that presence must
// survive the wire, where every field of the error has its default value.
func TestErrorConstructorsKeepAZeroErrorPresentOnTheWire(t *testing.T) {
	t.Run("a response", func(t *testing.T) {
		encoded, err := proto.Marshal(&leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_Response{Response: NewErrorResponse(0, "")},
		})
		require.NoError(t, err)
		var decoded leapmuxv1.InnerMessage
		require.NoError(t, proto.Unmarshal(encoded, &decoded))

		require.NotNil(t, decoded.GetResponse())
		require.NotNil(t, decoded.GetResponse().GetError(), "an empty error still marks the response a failure")
		assert.Zero(t, decoded.GetResponse().GetError().GetCode())
		assert.Empty(t, decoded.GetResponse().GetError().GetMessage())
	})

	t.Run("a stream frame", func(t *testing.T) {
		encoded, err := proto.Marshal(&leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_Stream{Stream: NewStreamError(0, "")},
		})
		require.NoError(t, err)
		var decoded leapmuxv1.InnerMessage
		require.NoError(t, proto.Unmarshal(encoded, &decoded))

		require.NotNil(t, decoded.GetStream())
		require.NotNil(t, decoded.GetStream().GetError(), "an empty error still marks the frame a failure")
		assert.True(t, decoded.GetStream().GetEnd(), "the frame still ends the stream")
	})
}

func TestFrameAssociatedData(t *testing.T) {
	for _, tc := range []struct {
		name          string
		channelID     string
		correlationID uint64
		flags         leapmuxv1.ChannelMessageFlags
		wantHex       string
	}{
		{name: "channel and MORE", channelID: "abc", correlationID: 5, flags: leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, wantHex: "000000000000000500000001616263"},
		{name: "largest uint64", channelID: "abc", correlationID: ^uint64(0), wantHex: "ffffffffffffffff00000000616263"},
		{name: "out-of-spec flags", correlationID: 5, flags: leapmuxv1.ChannelMessageFlags(3), wantHex: "000000000000000500000003"},
		{name: "empty channel id", wantHex: "000000000000000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := hex.DecodeString(tc.wantHex)
			require.NoError(t, err)
			assert.Equal(t, want, FrameAssociatedData(tc.channelID, tc.correlationID, tc.flags))
		})
	}
}

func TestFrameAssociatedDataCoversEveryEnvelopeField(t *testing.T) {
	fields := (&leapmuxv1.ChannelMessage{}).ProtoReflect().Descriptor().Fields()
	var names []string
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	assert.Equal(t, []string{"channel_id", "correlation_id", "flags", "ciphertext"}, names,
		"add a new envelope field to FrameAssociatedData, frameAssociatedData in channelFraming.ts, and the handshake vector frames")
}

func TestSealAndOpenChannelMessage(t *testing.T) {
	worker, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := worker.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	hs, message1, err := noiseutil.ClassicalInitiatorHandshake1("worker-1", worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPub)
	require.NoError(t, err)
	message2, responder, err := noiseutil.ClassicalResponderHandshake(worker, "worker-1", message1)
	require.NoError(t, err)
	initiator, err := hs.Finish(message2)
	require.NoError(t, err)
	defer initiator.Zero()
	defer responder.Zero()

	plaintext := []byte("payload")
	frame, err := SealChannelMessage(initiator, "ch-1", 7, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, plaintext)
	require.NoError(t, err)
	assert.Equal(t, "ch-1", frame.GetChannelId())
	assert.Equal(t, uint64(7), frame.GetCorrelationId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, frame.GetFlags())

	for _, tc := range []struct {
		name   string
		change func(*leapmuxv1.ChannelMessage)
	}{
		{name: "correlation id", change: func(msg *leapmuxv1.ChannelMessage) { msg.CorrelationId++ }},
		{name: "flags", change: func(msg *leapmuxv1.ChannelMessage) {
			msg.Flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		}},
		{name: "channel id", change: func(msg *leapmuxv1.ChannelMessage) { msg.ChannelId = "other" }},
		{name: "ciphertext", change: func(msg *leapmuxv1.ChannelMessage) { msg.Ciphertext[0] ^= 0xFF }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := proto.Clone(frame).(*leapmuxv1.ChannelMessage)
			tc.change(changed)
			_, err := OpenChannelMessage(responder, changed)
			require.Error(t, err)
		})
	}

	opened, err := OpenChannelMessage(responder, frame)
	require.NoError(t, err)
	assert.Equal(t, plaintext, opened)
}

// The close notification is the channel id and the CLOSE flag, and nothing else.
func TestCloseNotification(t *testing.T) {
	got := CloseNotification("ch1")
	want := &leapmuxv1.ChannelMessage{
		ChannelId: "ch1",
		Flags:     leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE,
	}
	assert.True(t, proto.Equal(want, got), "got %v", got)
	assert.True(t, IsCloseNotification(got))
	assert.False(t, IsCloseNotification(nil))
	assert.False(t, IsCloseNotification(&leapmuxv1.ChannelMessage{Flags: got.GetFlags(), CorrelationId: 1}))
	assert.False(t, IsCloseNotification(&leapmuxv1.ChannelMessage{Flags: got.GetFlags(), Ciphertext: []byte{1}}))
	assert.NotSame(t, got, CloseNotification("ch1"), "each call builds its own message")
}

func TestChunkContinuation(t *testing.T) {
	cases := []struct {
		name  string
		flags leapmuxv1.ChannelMessageFlags
		more  bool
		valid bool
	}{
		{"unspecified is a valid final chunk", leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, false, true},
		{"more is a valid non-final chunk", leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, true, true},
		{"close is not a data flag", leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, false, false},
		// proto3 enums are open: a hostile peer can put any integer on the
		// wire. A combined value must NOT be read as "final chunk" -- that
		// delivers a truncated assembly -- so it is invalid, full stop.
		{"combined MORE|CLOSE is invalid", leapmuxv1.ChannelMessageFlags(3), false, false},
		{"an unknown high value is invalid", leapmuxv1.ChannelMessageFlags(255), false, false},
		{"a negative value is invalid", leapmuxv1.ChannelMessageFlags(-1), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			more, valid := ChunkContinuation(tc.flags)
			assert.Equal(t, tc.more, more)
			assert.Equal(t, tc.valid, valid)
		})
	}
}

func TestIsUserEventsCloseErrorClassifiesRecoverableCloses(t *testing.T) {
	// Recoverable: a clean shutdown, an endpoint going away, or a transient
	// intermediary signal (load balancer / server restart) the caller reconnects
	// from rather than treating as a fatal stream error.
	for _, code := range []websocket.StatusCode{
		websocket.StatusNormalClosure,
		websocket.StatusGoingAway,
		websocket.StatusServiceRestart,
		websocket.StatusTryAgainLater,
		// A close frame with no status code. The Hub cannot emit it (the library
		// refuses 1005 on send), so it always means an intermediary ended an idle
		// stream with a bare close frame -- routine on a long-lived stream, and
		// nothing the consumer should surface as a hard error.
		websocket.StatusNoStatusRcvd,
	} {
		require.True(t, IsUserEventsCloseError(websocket.CloseError{Code: code}),
			"code %d should be recoverable", code)
	}
	// Terminal protocol/policy failures must not collapse to a clean close.
	for _, code := range []websocket.StatusCode{
		websocket.StatusProtocolError,
		websocket.StatusPolicyViolation,
		websocket.StatusInternalError,
		websocket.StatusAbnormalClosure,
	} {
		require.False(t, IsUserEventsCloseError(websocket.CloseError{Code: code}),
			"code %d should be terminal", code)
	}
}

func TestWebSocketCloseDetailsUsesRecoverableClassifier(t *testing.T) {
	// wasClean tracks the same recoverable classification as IsUserEventsCloseError
	// so the desktop relay and the CLI/worker consumers agree on which closes are
	// reconnect signals.
	for _, code := range []websocket.StatusCode{
		websocket.StatusNormalClosure,
		websocket.StatusGoingAway,
		websocket.StatusServiceRestart,
		websocket.StatusTryAgainLater,
	} {
		_, _, wasClean := WebSocketCloseDetails(websocket.CloseError{Code: code})
		require.True(t, wasClean, "code %d should be clean/recoverable", code)
	}
	for _, code := range []websocket.StatusCode{
		websocket.StatusProtocolError,
		websocket.StatusInternalError,
	} {
		_, _, wasClean := WebSocketCloseDetails(websocket.CloseError{Code: code})
		require.False(t, wasClean, "code %d should not be clean", code)
	}

	// A non-close transport failure surfaces as an abnormal-closure (never clean),
	// so callers never mistake a hard transport error for a recoverable close.
	code, _, wasClean := WebSocketCloseDetails(assertError("transport reset"))
	require.Equal(t, uint32(websocket.StatusAbnormalClosure), code)
	require.False(t, wasClean)
}

type assertError string

func (e assertError) Error() string { return string(e) }

// TestWidestEnvelopeFitsUnderHeadroom pins that a max-sized application
// payload wrapped in the real WatchEvents fan-out
// (AgentChatMessage -> AgentEvent -> WatchEventsResponse -> InnerStreamMessage
// -> InnerMessage) still fits under MaxReassembledMessageSize(contracts.MaxMessageSize),
// and that the overhead stays under half of contracts.InnerEnvelopeHeadroom so CI
// reddens long before a mid-stream drop. A tautological
// contracts.DefaultMaxReassembledMessageSize > contracts.MaxMessageSize assert would prove nothing.
func TestWidestEnvelopeFitsUnderHeadroom(t *testing.T) {
	encoded := widestWatchEventsEnvelope(t, contracts.MaxMessageSize)
	ceiling := MaxReassembledMessageSize(contracts.MaxMessageSize)
	assert.LessOrEqual(t, len(encoded), ceiling,
		"a max-sized payload in the widest WatchEvents fan-out must fit under the reassembled ceiling; "+
			"while payload and ceiling were equal it did not, and the drop had no recovery path")
	overhead := len(encoded) - contracts.MaxMessageSize
	assert.Greater(t, overhead, 0, "the envelope must actually cost something, or this proves nothing")
	assert.Less(t, overhead, contracts.InnerEnvelopeHeadroom/2,
		"envelope overhead has grown into over half the headroom (%d of %d bytes); "+
			"raise contracts.InnerEnvelopeHeadroom rather than letting it converge", overhead, contracts.InnerEnvelopeHeadroom)
}

// TestInnerRpcEnvelopeFitsUnderHeadroom is the file-read / unary-RPC twin of
// TestWidestEnvelopeFitsUnderHeadroom: a non-WatchEvents producer must not
// silently be the wider case.
func TestInnerRpcEnvelopeFitsUnderHeadroom(t *testing.T) {
	encoded := widestInnerRpcEnvelope(t, contracts.MaxMessageSize)
	ceiling := MaxReassembledMessageSize(contracts.MaxMessageSize)
	assert.LessOrEqual(t, len(encoded), ceiling)
	overhead := len(encoded) - contracts.MaxMessageSize
	assert.Greater(t, overhead, 0)
	assert.Less(t, overhead, contracts.InnerEnvelopeHeadroom/2,
		"InnerRpc envelope overhead has grown into over half the headroom (%d of %d bytes); "+
			"raise contracts.InnerEnvelopeHeadroom rather than letting it converge", overhead, contracts.InnerEnvelopeHeadroom)
}

// TestHeadroomCoversConfiguredMaxMessageSize pins that headroom is enough at
// non-default payload sizes too -- not only at the 16 MiB default -- so a
// varint-length growth or nesting change cannot leave larger configs short.
func TestHeadroomCoversConfiguredMaxMessageSize(t *testing.T) {
	for _, size := range []int{1024 * 1024, 4 * 1024 * 1024, contracts.MaxConfigurableMessageSize} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			encoded := widestWatchEventsEnvelope(t, size)
			assert.LessOrEqual(t, len(encoded), MaxReassembledMessageSize(size),
				"widest fan-out at payload %d must fit under MaxReassembledMessageSize", size)
			overhead := len(encoded) - size
			assert.Less(t, overhead, contracts.InnerEnvelopeHeadroom/2,
				"overhead at payload %d (%d) ate half of headroom %d", size, overhead, contracts.InnerEnvelopeHeadroom)
		})
	}
}

func TestResolveAndValidateMaxMessageSize(t *testing.T) {
	assert.Equal(t, contracts.MaxMessageSize, ResolveMaxMessageSize(0))
	assert.Equal(t, contracts.MaxMessageSize, ResolveMaxMessageSize(-1))
	assert.Equal(t, 32*1024*1024, ResolveMaxMessageSize(32*1024*1024))
	assert.Equal(t, contracts.MaxMessageSize+contracts.InnerEnvelopeHeadroom, MaxReassembledMessageSize(contracts.MaxMessageSize))

	require.NoError(t, ValidateMaxMessageSize(contracts.MaxMessageSize))
	require.NoError(t, ValidateMaxMessageSize(contracts.MaxPlaintextPerChunk))
	require.NoError(t, ValidateMaxMessageSize(contracts.MaxConfigurableMessageSize))
	assert.Error(t, ValidateMaxMessageSize(contracts.MaxPlaintextPerChunk-1))
	assert.Error(t, ValidateMaxMessageSize(contracts.MaxConfigurableMessageSize+1))

	require.NoError(t, ValidateConfiguredMaxMessageSize(0))
	require.NoError(t, ValidateConfiguredMaxMessageSize(-1))
	require.NoError(t, ValidateConfiguredMaxMessageSize(contracts.MaxMessageSize))
	assert.Error(t, ValidateConfiguredMaxMessageSize(1))

	got, err := IntFromUint64(uint64(contracts.MaxMessageSize))
	require.NoError(t, err)
	assert.Equal(t, contracts.MaxMessageSize, got)
	_, err = IntFromUint64(^uint64(0))
	require.Error(t, err)

	assert.Equal(t, 8*1024*1024, NegotiatePayloadBudget(16*1024*1024, 8*1024*1024))
	assert.Equal(t, 8*1024*1024, NegotiatePayloadBudget(8*1024*1024, 16*1024*1024))
	assert.Equal(t, contracts.MaxMessageSize, NegotiatePayloadBudget(contracts.MaxMessageSize, contracts.MaxMessageSize))

	adopted, err := AdoptWireMaxMessageSize(uint64(contracts.MaxMessageSize))
	require.NoError(t, err)
	assert.Equal(t, contracts.MaxMessageSize, adopted)
	_, err = AdoptWireMaxMessageSize(0)
	require.ErrorContains(t, err, "no max_message_size")
	_, err = AdoptWireMaxMessageSize(uint64(contracts.MaxPlaintextPerChunk - 1))
	require.Error(t, err)
	_, err = AdoptWireMaxMessageSize(uint64(contracts.MaxConfigurableMessageSize + 1))
	require.Error(t, err)
	_, err = AdoptWireMaxMessageSize(^uint64(0))
	require.Error(t, err)
}

func widestWatchEventsEnvelope(t *testing.T, payloadSize int) []byte {
	t.Helper()
	agentMsg := &leapmuxv1.AgentChatMessage{
		Id:                 strings.Repeat("m", 128),
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:            make([]byte, payloadSize),
		Seq:                int64(^uint64(0) >> 1),
		CreatedAt:          "2026-07-25T00:00:00.000000000Z",
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Depth:              5,
		ParentSpanId:       strings.Repeat("p", 128),
		SpanId:             strings.Repeat("s", 128),
		SpanType:           "commandExecution" + strings.Repeat("x", 100),
		SpanColor:          7,
		SpanLines:          "[" + strings.Repeat(`"abcdefgh",`, 199) + `"abcdefgh"]`,
		PreviousSeq:        int64(^uint64(0) >> 1),
		MarkType:           leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED,
	}
	watch := &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
			AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: strings.Repeat("a", 128),
				Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: agentMsg},
			},
		},
	}
	watchBytes, err := proto.Marshal(watch)
	require.NoError(t, err)
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Stream{
			Stream: &leapmuxv1.InnerStreamMessage{
				Payload: watchBytes,
				End:     true,
				Error:   &leapmuxv1.RpcError{Code: int32(^uint32(0) >> 1), Message: strings.Repeat("e", 4096)},
			},
		},
	}
	encoded, err := proto.Marshal(envelope)
	require.NoError(t, err)
	return encoded
}

// widestInnerRpcEnvelope wraps a payloadSize payload in a unary response. A
// response carries its payload or its error, never both, so the widest one is
// the largest payload.
func widestInnerRpcEnvelope(t *testing.T, payloadSize int) []byte {
	t.Helper()
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Response{
			Response: NewPayloadResponse(make([]byte, payloadSize)),
		},
	}
	encoded, err := proto.Marshal(envelope)
	require.NoError(t, err)
	return encoded
}

// TestUserEventsURL_ResumeParams pins the query-string shape a reconnecting
// client emits: workspace_ids (when scoped) plus resume_after_hlc +
// resume_epoch (when a resume cursor is present). With no cursor the URL is
// byte-identical to the legacy shape, so an older client that never sends one
// is unaffected.
func TestUserEventsURL_ResumeParams(t *testing.T) {
	t.Run("nil cursor emits only workspace_ids", func(t *testing.T) {
		got, err := UserEventsURL("https://hub", UserEventsOptions{WorkspaceIDs: []string{"w1", "w2"}})
		require.NoError(t, err)
		assert.Equal(t, "wss://hub/ws/userevents?workspace_ids=w1%2Cw2", got)
	})
	// The emit gate must be the DECODER's accept rule, not the weaker all-zero
	// test. hlcIsZero only rejects {0,0,""}, so a cursor whose physical is 0 but
	// whose other fields are set used to be written onto the URL -- and
	// DecodeResumeHLC rejects any physical <= 0 (the shared corpus pins
	// `{"raw": "0.4.c", "ok": false}`), so the hub answered its own client with a
	// 400 where the intent was to omit the cursor and take a full snapshot.
	t.Run("a cursor the decoder would reject is omitted, not sent", func(t *testing.T) {
		for _, cursor := range []*leapmuxv1.HLC{
			{Physical: 0, Logical: 4, ClientId: "c"},
			{Physical: -1, Logical: 0, ClientId: "c"},
			{Physical: 5, Logical: -1, ClientId: "c"},
			{Physical: 5, Logical: 0, ClientId: ""},
		} {
			got, err := UserEventsURL("https://hub", UserEventsOptions{ResumeCursor: cursor, ResumeEpoch: 7})
			require.NoError(t, err)
			assert.NotContains(t, got, "resume_after_hlc", "cursor %v round-trips to a 400", cursor)
			assert.NotContains(t, got, "resume_epoch")
		}
	})
	t.Run("cursor appends resume params", func(t *testing.T) {
		hlc := &leapmuxv1.HLC{Physical: 1754100000000, Logical: 3, ClientId: "c-abc"}
		got, err := UserEventsURL("https://hub", UserEventsOptions{ResumeCursor: hlc, ResumeEpoch: 7})
		require.NoError(t, err)
		assert.Contains(t, got, "resume_after_hlc=1754100000000.3.c-abc")
		assert.Contains(t, got, "resume_epoch=7")
	})
	t.Run("nil cursor emits no resume params", func(t *testing.T) {
		got, err := UserEventsURL("https://hub", UserEventsOptions{ResumeEpoch: 1})
		require.NoError(t, err)
		assert.NotContains(t, got, "resume_after_hlc")
		assert.NotContains(t, got, "resume_epoch")
	})
}

// TestUserEventsURL_RoundTripsThroughTheHubReader proves the client's writer
// and the hub's reader agree on the query vocabulary: both spell the param
// names through contracts.WSParam*, and this test reddens the day one side
// stops. A desync does not fail loudly on its own -- an absent param is a
// legal first connect, so every reconnect would silently degrade to a full
// snapshot with no 400 and no log.
func TestUserEventsURL_RoundTripsThroughTheHubReader(t *testing.T) {
	hlc := &leapmuxv1.HLC{Physical: 1754100000000, Logical: 3, ClientId: "c-abc"}
	raw, err := UserEventsURL("https://hub", UserEventsOptions{
		WorkspaceIDs: []string{"w1"},
		ResumeCursor: hlc,
		ResumeEpoch:  7,
	})
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	cursor, epoch, err := ParseResumeCursorFromQuery(u.Query())
	require.NoError(t, err)
	require.NotNil(t, cursor)
	assert.Equal(t, hlc.GetPhysical(), cursor.GetPhysical())
	assert.Equal(t, hlc.GetLogical(), cursor.GetLogical())
	assert.Equal(t, hlc.GetClientId(), cursor.GetClientId())
	assert.Equal(t, int64(7), epoch)
}

// TestEncodeDecodeResumeHLC covers the round-trip and the malformed-input
// rejections that make a bad param degrade to a full-snapshot connect.
func TestEncodeDecodeResumeHLC(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		hlc := &leapmuxv1.HLC{Physical: 1754100000000, Logical: 42, ClientId: "c-abc"}
		assert.Equal(t, "1754100000000.42.c-abc", EncodeResumeHLC(hlc))
		got := DecodeResumeHLC("1754100000000.42.c-abc")
		require.NotNil(t, got)
		assert.Equal(t, hlc.GetPhysical(), got.GetPhysical())
		assert.Equal(t, hlc.GetLogical(), got.GetLogical())
		assert.Equal(t, hlc.GetClientId(), got.GetClientId())
	})
	cases := []string{
		"",                                  // empty
		"123",                               // no dots
		"123.4",                             // one dot
		"abc.4.c",                           // non-numeric physical
		"123.abc.c",                         // non-numeric logical
		"0.4.c",                             // zero physical (must be > 0)
		"123.4.",                            // empty client id
		"123.4." + strings.Repeat("x", 129), // overlong client id
	}
	for _, raw := range cases {
		assert.Nil(t, DecodeResumeHLC(raw), "DecodeResumeHLC(%q) must reject as nil", raw)
	}
}

// TestParseResumeCursor pins the shared cursor-validation contract that BOTH
// the hub (URL query params) and the desktop sidecar (proto fields) delegate
// to. The sidecar now REJECTS the relay-open RPC on any malformed value
// (matching the hub's HTTP 400) rather than silently degrading to a full
// snapshot, so this strictness is load-bearing for surfacing frontend bugs at
// the first boundary they cross.
func TestParseResumeCursor(t *testing.T) {
	t.Run("both empty = first connect", func(t *testing.T) {
		cursor, epoch, err := ParseResumeCursor("", "")
		require.NoError(t, err)
		assert.Nil(t, cursor)
		assert.Equal(t, int64(0), epoch)
	})
	t.Run("well-formed pair decodes", func(t *testing.T) {
		cursor, epoch, err := ParseResumeCursor("1754100000000.3.c-abc", "7")
		require.NoError(t, err)
		require.NotNil(t, cursor)
		assert.Equal(t, int64(1754100000000), cursor.GetPhysical())
		assert.Equal(t, int64(3), cursor.GetLogical())
		assert.Equal(t, "c-abc", cursor.GetClientId())
		assert.Equal(t, int64(7), epoch)
	})
	for _, c := range []struct {
		name     string
		hlcRaw   string
		epochRaw string
	}{
		{"malformed hlc", "not-an-hlc", "7"},
		{"malformed epoch", "1754100000000.3.c-abc", "not-a-number"},
		{"epoch without hlc", "", "7"},
		{"hlc without epoch", "1754100000000.3.c-abc", ""},
		{"negative logical", "100.-5.c", "7"},
		{"overlong client id", "100.0." + strings.Repeat("x", 129), "7"},
		// The epoch gets the SAME domain treatment as physical/logical, not just a
		// parse check. `UserCrdtState.current_epoch` starts at 1 (`crdt.NewState`)
		// and only increases, so these are values the hub can never hold; without the
		// check they parse as well-formed and then fail silently at decideResume's
		// equality test, degrading that client to a full snapshot on every connect
		// with nothing logged.
		{"zero epoch", "1754100000000.3.c-abc", "0"},
		{"negative epoch", "1754100000000.3.c-abc", "-1"},
		{"plus-signed epoch", "1754100000000.3.c-abc", "+7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ParseResumeCursor(c.hlcRaw, c.epochRaw)
			require.Error(t, err, "malformed resume params must surface an error (hub 400 / sidecar reject)")
		})
	}
}

// TestDecodeResumeHLC_CrossLanguageCorpus pins channelwire.DecodeResumeHLC
// against the shared testdata/hlc_wire_corpus.json fixture that the TypeScript
// client (frontend/src/lib/crdt/hlc.test.ts) asserts parseHlcWire against too.
// Both decoders parse the SAME resume-cursor wire format, and the client
// pre-validates a persisted cursor before sending so a value the hub would 400
// never leaves the browser (otherwise: non-terminal 1006 close → tight reconnect
// loop). A rule added/tightened on one side but not the other reddens CI here
// instead of drifting back into that storm. Mirrors the cross-language pattern
// TestChannelWireLimitsMatchCrossLanguageFixture established.
func TestDecodeResumeHLC_CrossLanguageCorpus(t *testing.T) {
	data, err := os.ReadFile("../../testdata/hlc_wire_corpus.json")
	require.NoError(t, err, "testdata/hlc_wire_corpus.json must exist; see its _readme")
	var fixture struct {
		Cases []struct {
			Raw      string `json:"raw"`
			OK       bool   `json:"ok"`
			Physical string `json:"physical"`
			Logical  string `json:"logical"`
			ClientID string `json:"clientId"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture), "fixture must be valid JSON")

	for _, c := range fixture.Cases {
		got := DecodeResumeHLC(c.Raw)
		if !c.OK {
			assert.Nil(t, got, "DecodeResumeHLC(%q) must reject (corpus says ok=false)", c.Raw)
			continue
		}
		require.NotNil(t, got, "DecodeResumeHLC(%q) must decode (corpus says ok=true)", c.Raw)
		wantPhys, _ := strconv.ParseInt(c.Physical, 10, 64)
		wantLog, _ := strconv.ParseInt(c.Logical, 10, 64)
		assert.Equal(t, wantPhys, got.GetPhysical(), "DecodeResumeHLC(%q) physical", c.Raw)
		assert.Equal(t, wantLog, got.GetLogical(), "DecodeResumeHLC(%q) logical", c.Raw)
		assert.Equal(t, c.ClientID, got.GetClientId(), "DecodeResumeHLC(%q) clientId", c.Raw)
	}
}

// TestParseResumeCursorFromQuery pins the strict parsing contract: a first connect
// (both params absent) yields a nil cursor + no error, a well-formed pair
// decodes, and any malformed shape (bad HLC, bad epoch, or epoch-without-hlc)
// returns an error the handler surfaces as HTTP 400. The malformed arms are
// the load-bearing part — strictness here means a buggy client fails loudly
// instead of silently degrading to full-snapshot reconnects.
func TestParseResumeCursorFromQuery(t *testing.T) {
	t.Run("both absent = first connect", func(t *testing.T) {
		cursor, epoch, err := ParseResumeCursorFromQuery(url.Values{})
		require.NoError(t, err)
		require.Nil(t, cursor)
		require.Equal(t, int64(0), epoch)
	})
	t.Run("well-formed", func(t *testing.T) {
		q := url.Values{
			"resume_after_hlc": {"1754100000000.3.c-abc"},
			"resume_epoch":     {"7"},
		}
		cursor, epoch, err := ParseResumeCursorFromQuery(q)
		require.NoError(t, err)
		require.NotNil(t, cursor)
		require.Equal(t, int64(1754100000000), cursor.GetPhysical())
		require.Equal(t, int64(3), cursor.GetLogical())
		require.Equal(t, "c-abc", cursor.GetClientId())
		require.Equal(t, int64(7), epoch)
	})
	for _, c := range []struct {
		name string
		q    url.Values
	}{
		{"malformed hlc", url.Values{"resume_after_hlc": {"not-an-hlc"}}},
		{"malformed epoch", url.Values{
			"resume_after_hlc": {"1754100000000.3.c-abc"},
			"resume_epoch":     {"not-a-number"},
		}},
		{"epoch without hlc", url.Values{"resume_epoch": {"7"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ParseResumeCursorFromQuery(c.q)
			require.Error(t, err, "malformed resume params must surface an error (→ HTTP 400)")
		})
	}
}

func TestUserEventsURLAndParsers(t *testing.T) {
	t.Run("round trips topics, layout workspace, and binary layout cursor", func(t *testing.T) {
		rawCursor := []byte{0x00, 0xff, 0x10, 0x20, 0xab, 0xcd, 0xef}
		got, err := UserEventsURL("https://hub.example", UserEventsOptions{
			WorkspaceIDs: []string{"w1", "w2"},
			Topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS, // duplicate deduped
			},
			LayoutWorkspaceID: "ws-layout",
			LayoutAfterCursor: rawCursor,
		})
		require.NoError(t, err)
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, "wss", u.Scheme)
		assert.Equal(t, contracts.WSRouteUserEvents, u.Path)

		topics, err := ParseUserEventsTopicsFromQuery(u.Query())
		require.NoError(t, err)
		assert.Equal(t, []leapmuxv1.UserEventTopic{
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
		}, topics)
		assert.Equal(t, "ws-layout", ParseLayoutWorkspaceIDFromQuery(u.Query()))

		decodedCursor, err := ParseLayoutCursorFromQuery(u.Query())
		require.NoError(t, err)
		assert.Equal(t, rawCursor, decodedCursor)
	})

	t.Run("rejects unspecified and unknown topics with ErrInvalidUserEventTopic", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			topics []leapmuxv1.UserEventTopic
		}{
			{name: "unspecified", topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_UNSPECIFIED}},
			{name: "unknown positive ordinal", topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic(99)}},
			{name: "unknown negative ordinal", topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic(-1)}},
			{name: "valid followed by unspecified", topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_UNSPECIFIED,
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := UserEventsURL("https://hub.example", UserEventsOptions{Topics: tc.topics})
				require.Error(t, err)
				assert.Empty(t, got)
				assert.ErrorIs(t, err, ErrInvalidUserEventTopic)
			})
		}
	})

	t.Run("absent topics and layout cursor return nil", func(t *testing.T) {
		topics, err := ParseUserEventsTopicsFromQuery(url.Values{})
		require.NoError(t, err)
		assert.Nil(t, topics)

		cursor, err := ParseLayoutCursorFromQuery(url.Values{})
		require.NoError(t, err)
		assert.Nil(t, cursor)
	})

	t.Run("rejects unknown or empty topic names", func(t *testing.T) {
		for _, raw := range []string{"unknown", "workers,bogus", "workers,", ",crdt"} {
			_, err := ParseUserEventsTopicsFromQuery(url.Values{wsParamTopics: {raw}})
			require.Error(t, err, "topics=%q must fail", raw)
		}
	})

	t.Run("rejects invalid base64 layout cursor", func(t *testing.T) {
		_, err := ParseLayoutCursorFromQuery(url.Values{wsParamLayoutAfterCursor: {"***not-base64***"}})
		require.Error(t, err)
	})

	t.Run("round trips crdt topic", func(t *testing.T) {
		got, err := UserEventsURL("https://hub.example", UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
			},
		})
		require.NoError(t, err)
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, "crdt", u.Query().Get(wsParamTopics))

		topics, err := ParseUserEventsTopicsFromQuery(u.Query())
		require.NoError(t, err)
		assert.Equal(t, []leapmuxv1.UserEventTopic{
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
		}, topics)
	})

	t.Run("deduplicates and trims whitespace in topics query", func(t *testing.T) {
		topics, err := ParseUserEventsTopicsFromQuery(url.Values{wsParamTopics: {" crdt , workers , crdt "}})
		require.NoError(t, err)
		assert.Equal(t, []leapmuxv1.UserEventTopic{
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
		}, topics)

		blankTopics, err := ParseUserEventsTopicsFromQuery(url.Values{wsParamTopics: {"   "}})
		require.NoError(t, err)
		assert.Nil(t, blankTopics)
	})
}

func TestTruncateCloseReason(t *testing.T) {
	t.Run("keeps empty and within-limit strings unchanged", func(t *testing.T) {
		assert.Equal(t, "", TruncateCloseReason(""))
		exact := strings.Repeat("a", maxCloseReasonBytes)
		assert.Equal(t, exact, TruncateCloseReason(exact))
	})

	t.Run("truncates ASCII at 123 bytes", func(t *testing.T) {
		got := TruncateCloseReason(strings.Repeat("a", 200))
		assert.Len(t, got, maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("does not split a 3-byte rune straddling byte 123", func(t *testing.T) {
		// 121 ASCII bytes + 3-byte rune ('한' = 3 bytes, occupies 121..123) + trailing ASCII.
		in := strings.Repeat("a", 121) + "한" + "tail"
		got := TruncateCloseReason(in)
		assert.Equal(t, strings.Repeat("a", 121), got)
		assert.LessOrEqual(t, len(got), maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("keeps a 3-byte rune ending exactly at byte 123", func(t *testing.T) {
		in := strings.Repeat("a", 120) + "한" + "tail"
		got := TruncateCloseReason(in)
		assert.Equal(t, strings.Repeat("a", 120)+"한", got)
		assert.Len(t, got, maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("does not split a 4-byte rune straddling byte 123", func(t *testing.T) {
		// 121 ASCII bytes + 4-byte rune (occupies 121..124).
		in := strings.Repeat("a", 121) + "🚀" + "tail"
		got := TruncateCloseReason(in)
		assert.Equal(t, strings.Repeat("a", 121), got)
		assert.LessOrEqual(t, len(got), maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(got))
	})
}

func TestUserEventsPolicyCloseReasonFormatAndParse(t *testing.T) {
	for _, code := range []string{"not_found", "failed_precondition", "invalid_argument", "permission_denied"} {
		t.Run("round trips "+code, func(t *testing.T) {
			formatted := FormatUserEventsPolicyCloseReason(code, "workspace missing")
			assert.Equal(t, code+": workspace missing", formatted)
			gotCode, gotMsg, ok := ParseUserEventsPolicyCloseReason(formatted)
			require.True(t, ok)
			assert.Equal(t, code, gotCode)
			assert.Equal(t, "workspace missing", gotMsg)
		})

		t.Run("round trips "+code+" with empty message", func(t *testing.T) {
			formatted := FormatUserEventsPolicyCloseReason(code, "")
			assert.Equal(t, code, formatted)
			gotCode, gotMsg, ok := ParseUserEventsPolicyCloseReason(formatted)
			require.True(t, ok)
			assert.Equal(t, code, gotCode)
			assert.Equal(t, "", gotMsg)
		})
	}

	t.Run("truncates overlong multi-byte message to valid UTF-8 <= 123 bytes", func(t *testing.T) {
		longMsg := strings.Repeat("가", 60)
		formatted := FormatUserEventsPolicyCloseReason("not_found", longMsg)
		assert.LessOrEqual(t, len(formatted), maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(formatted))
		gotCode, gotMsg, ok := ParseUserEventsPolicyCloseReason(formatted)
		require.True(t, ok)
		assert.Equal(t, "not_found", gotCode)
		assert.NotEmpty(t, gotMsg)
		assert.True(t, utf8.ValidString(gotMsg))
	})

	t.Run("empty or whitespace code returns truncated message without leading separator", func(t *testing.T) {
		assert.Equal(t, "workspace missing", FormatUserEventsPolicyCloseReason("", "workspace missing"))
		assert.Equal(t, "workspace missing", FormatUserEventsPolicyCloseReason("   \t ", "  workspace missing  "))
		assert.Equal(t, "", FormatUserEventsPolicyCloseReason("   ", "   "))

		overlong := strings.Repeat("가", 60)
		got := FormatUserEventsPolicyCloseReason(" ", overlong)
		assert.LessOrEqual(t, len(got), maxCloseReasonBytes)
		assert.True(t, utf8.ValidString(got))
		assert.False(t, strings.HasPrefix(got, ": "))
		assert.NotEmpty(t, got)
	})

	t.Run("rejects unrecognized or non-structured reasons", func(t *testing.T) {
		for _, reason := range []string{
			"",
			contracts.CloseReasonTooManyConnections,
			contracts.CloseReasonForbidden,
			"internal: unexpected failure",
			"not_found_extra: nope",
			"plain error message",
		} {
			code, msg, ok := ParseUserEventsPolicyCloseReason(reason)
			assert.False(t, ok, "reason=%q must not match", reason)
			assert.Empty(t, code)
			assert.Empty(t, msg)
		}
	})
}

func TestOpenUserEventsWS_AuthorizationHeader(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bearerToken string
		wantPresent bool
		wantValue   string
	}{
		{
			name:        "empty bearerToken omits Authorization header",
			bearerToken: "",
			wantPresent: false,
		},
		{
			name:        "non-empty bearerToken sets Authorization Bearer header",
			bearerToken: "secret-token",
			wantPresent: true,
			wantValue:   "Bearer secret-token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headersCh := make(chan http.Header, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headersCh <- r.Header.Clone()
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
					Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
				})
				if err != nil {
					return
				}
				_ = conn.Close(websocket.StatusNormalClosure, "")
			}))
			t.Cleanup(srv.Close)

			ws, err := OpenUserEventsWS(t.Context(), srv.Client(), srv.URL, tc.bearerToken, UserEventsOptions{})
			require.NoError(t, err)
			t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })

			hdr := <-headersCh
			values, present := hdr["Authorization"]
			assert.Equal(t, tc.wantPresent, present)
			if tc.wantPresent {
				assert.Equal(t, []string{tc.wantValue}, values)
			}
		})
	}
}

func TestOpenUserEventsWS_TransportDialFailureIsNotHandshakeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	_, err := OpenUserEventsWS(t.Context(), nil, closedURL, "token", UserEventsOptions{})
	require.Error(t, err)
	var hsErr *HandshakeError
	assert.False(t, errors.As(err, &hsErr), "transport dial failure with no HTTP response must not return *HandshakeError")
	assert.ErrorContains(t, err, "dial /ws/userevents:")
}

func TestOpenUserEventsWS_CapsHandshakeErrorBodyAt1024Bytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat("a", 1024) + strings.Repeat("b", 512)))
	}))
	t.Cleanup(srv.Close)

	_, err := OpenUserEventsWS(t.Context(), srv.Client(), srv.URL, "token", UserEventsOptions{})
	require.Error(t, err)
	var hsErr *HandshakeError
	require.ErrorAs(t, err, &hsErr)
	assert.Equal(t, http.StatusForbidden, hsErr.StatusCode)
	assert.Len(t, hsErr.Body, 1024)
	assert.Equal(t, strings.Repeat("a", 1024), hsErr.Body)
}

func TestOpenUserEventsWS_SurfacesHTTPHandshakeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "scope forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := OpenUserEventsWS(t.Context(), srv.Client(), srv.URL, "token", UserEventsOptions{})
	require.Error(t, err)
	var hsErr *HandshakeError
	require.ErrorAs(t, err, &hsErr)
	assert.Equal(t, http.StatusForbidden, hsErr.StatusCode)
	assert.Equal(t, "scope forbidden", hsErr.Body)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestOpenUserEventsWS_RejectsInvalidTopicBeforeDialing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "should not be dialed", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	for _, topic := range []leapmuxv1.UserEventTopic{
		leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_UNSPECIFIED,
		leapmuxv1.UserEventTopic(99),
	} {
		_, err := OpenUserEventsWS(t.Context(), srv.Client(), srv.URL, "token", UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{topic},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidUserEventTopic)
	}
	assert.Zero(t, hits.Load(), "invalid topic must fail before dialing the server")
}

func TestIsTooManyConnectionsError(t *testing.T) {
	assert.False(t, IsTooManyConnectionsError(nil))
	assert.False(t, IsTooManyConnectionsError(errors.New("plain error")))
	assert.False(t, IsTooManyConnectionsError(websocket.CloseError{
		Code:   websocket.StatusPolicyViolation,
		Reason: contracts.CloseReasonForbidden,
	}))
	assert.True(t, IsTooManyConnectionsError(websocket.CloseError{
		Code:   websocket.StatusPolicyViolation,
		Reason: contracts.CloseReasonTooManyConnections,
	}))
	assert.True(t, IsTooManyConnectionsError(fmt.Errorf("dial relay: %w", websocket.CloseError{
		Code:   websocket.StatusPolicyViolation,
		Reason: contracts.CloseReasonTooManyConnections,
	})))
}

func TestConnectErrorFromWebSocket(t *testing.T) {
	t.Run("nil and unclassified errors return false", func(t *testing.T) {
		for _, err := range []error{
			nil,
			errors.New("network reset"),
			websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: contracts.CloseReasonSnapshotTooLarge},
			&HandshakeError{StatusCode: http.StatusInternalServerError, Body: "boom", Err: errors.New("500")},
		} {
			got, ok := ConnectErrorFromWebSocket(err)
			assert.False(t, ok, "err=%v must not match", err)
			assert.Nil(t, got)
		}
	})

	t.Run("ErrInvalidUserEventTopic maps to CodeInvalidArgument", func(t *testing.T) {
		raw := fmt.Errorf("%w: %v", ErrInvalidUserEventTopic, leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_UNSPECIFIED)
		got, ok := ConnectErrorFromWebSocket(raw)
		require.True(t, ok)
		assert.Equal(t, connect.CodeInvalidArgument, got.Code())
		assert.Equal(t, raw.Error(), got.Message())
		assert.ErrorIs(t, got, ErrInvalidUserEventTopic)
	})

	t.Run("WebSocket close errors map to Connect codes and preserve cause", func(t *testing.T) {
		cases := []struct {
			name     string
			closeErr websocket.CloseError
			wantCode connect.Code
			wantMsg  string
		}{
			{
				name:     "too_many_connections",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: contracts.CloseReasonTooManyConnections},
				wantCode: connect.CodeUnavailable,
				wantMsg:  "the Hub rejected the connection: too many open connections",
			},
			{
				name:     "try_again_later with reason",
				closeErr: websocket.CloseError{Code: websocket.StatusTryAgainLater, Reason: "restarting"},
				wantCode: connect.CodeUnavailable,
				wantMsg:  "restarting",
			},
			{
				name:     "try_again_later with empty reason",
				closeErr: websocket.CloseError{Code: websocket.StatusTryAgainLater},
				wantCode: connect.CodeUnavailable,
				wantMsg:  "the Hub is temporarily unavailable",
			},
			{
				name:     "forbidden token",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: contracts.CloseReasonForbidden},
				wantCode: connect.CodePermissionDenied,
				wantMsg:  "the Hub rejected the connection: permission denied",
			},
			{
				name:     "policy not_found",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: FormatUserEventsPolicyCloseReason("not_found", "workspace missing")},
				wantCode: connect.CodeNotFound,
				wantMsg:  "workspace missing",
			},
			{
				name:     "policy failed_precondition",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: FormatUserEventsPolicyCloseReason("failed_precondition", "cursor stale")},
				wantCode: connect.CodeFailedPrecondition,
				wantMsg:  "cursor stale",
			},
			{
				name:     "policy invalid_argument",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: FormatUserEventsPolicyCloseReason("invalid_argument", "bad cursor")},
				wantCode: connect.CodeInvalidArgument,
				wantMsg:  "bad cursor",
			},
			{
				name:     "policy permission_denied with empty message",
				closeErr: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: FormatUserEventsPolicyCloseReason("permission_denied", "")},
				wantCode: connect.CodePermissionDenied,
				wantMsg:  "permission_denied",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				wrapped := fmt.Errorf("read: %w", tc.closeErr)
				got, ok := ConnectErrorFromWebSocket(wrapped)
				require.True(t, ok)
				assert.Equal(t, tc.wantCode, got.Code())
				assert.Equal(t, tc.wantMsg, got.Message())
				var gotClose websocket.CloseError
				require.ErrorAs(t, got, &gotClose)
				assert.Equal(t, tc.closeErr, gotClose)
			})
		}
	})

	t.Run("HandshakeError statuses map to Connect codes and preserve cause", func(t *testing.T) {
		cases := []struct {
			name     string
			status   int
			body     string
			wantCode connect.Code
			wantMsg  string
		}{
			{name: "400", status: http.StatusBadRequest, body: "unknown topic", wantCode: connect.CodeInvalidArgument, wantMsg: "unknown topic"},
			{name: "401", status: http.StatusUnauthorized, body: "expired token", wantCode: connect.CodeUnauthenticated, wantMsg: "expired token"},
			{name: "403", status: http.StatusForbidden, body: "missing scope", wantCode: connect.CodePermissionDenied, wantMsg: "missing scope"},
			{name: "404", status: http.StatusNotFound, body: "not found", wantCode: connect.CodeNotFound, wantMsg: "not found"},
			{name: "429", status: http.StatusTooManyRequests, body: "too many requests", wantCode: connect.CodeUnavailable, wantMsg: "too many requests"},
			{name: "503 empty body falls back to Error", status: http.StatusServiceUnavailable, body: "", wantCode: connect.CodeUnavailable, wantMsg: "dial /ws/userevents: HTTP 503: boom"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				hsErr := &HandshakeError{StatusCode: tc.status, Body: tc.body, Err: errors.New("boom")}
				got, ok := ConnectErrorFromWebSocket(hsErr)
				require.True(t, ok)
				assert.Equal(t, tc.wantCode, got.Code())
				assert.Equal(t, tc.wantMsg, got.Message())
				var unwrapped *HandshakeError
				require.ErrorAs(t, got, &unwrapped)
				assert.Same(t, hsErr, unwrapped)
			})
		}
	})
}

// It lives here rather than in the hub service suite because the function
// moved to sit beside UserEventsURL, which writes the very keys it reads.
