package channelwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestChannelWireLimitsMatchCrossLanguageFixture pins the chunk/message/sequence
// limits against the shared testdata/channelwire_limits.json fixture that the
// TypeScript browser client (frontend/src/lib/channel.wire-limits.test.ts) asserts too. Both
// ends chunk and reassemble the same encrypted channel messages, so a retune on
// one side that is not mirrored on the other would silently reject or mis-split a
// legitimate message at the un-updated receiver; keeping both constant sets tied
// to one fixture turns that drift into a red build here instead. See the fixture's
// _readme.
func TestChannelWireLimitsMatchCrossLanguageFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/channelwire_limits.json")
	require.NoError(t, err)

	var limits struct {
		MaxPlaintextPerChunk        int    `json:"maxPlaintextPerChunk"`
		MaxMessageSize              int    `json:"maxMessageSize"`
		InnerEnvelopeHeadroom       int    `json:"innerEnvelopeHeadroom"`
		MaxReassembledMessageSize   int    `json:"maxReassembledMessageSize"`
		MaxConfigurableMessageSize  int    `json:"maxConfigurableMessageSize"`
		MaxIncompleteChunked        int    `json:"maxIncompleteChunked"`
		PingMethod                  string `json:"pingMethod"`
		SessionKeyMaxAgeMs          int64  `json:"sessionKeyMaxAgeMs"`
		MinRekeyIntervalMs          int64  `json:"minRekeyIntervalMs"`
		SessionKeyHardCeilingMs     int64  `json:"sessionKeyHardCeilingMs"`
		CloseReasonTooManyConns     string `json:"closeReasonTooManyConnections"`
		CloseReasonSnapshotTooLarge string `json:"closeReasonSnapshotTooLarge"`
		CloseReasonForbidden        string `json:"closeReasonForbidden"`
		CloseReasonControlFlood     string `json:"closeReasonControlFlood"`
	}
	require.NoError(t, json.Unmarshal(data, &limits))

	assert.Equal(t, limits.MaxPlaintextPerChunk, MaxPlaintextPerChunk,
		"MaxPlaintextPerChunk must match the cross-language fixture")
	assert.Equal(t, limits.MaxMessageSize, MaxMessageSize,
		"MaxMessageSize must match the cross-language fixture")
	assert.Equal(t, limits.InnerEnvelopeHeadroom, InnerEnvelopeHeadroom,
		"InnerEnvelopeHeadroom must match the cross-language fixture")
	assert.Equal(t, limits.MaxReassembledMessageSize, DefaultMaxReassembledMessageSize,
		"DefaultMaxReassembledMessageSize must match the cross-language fixture")
	assert.Equal(t, limits.MaxConfigurableMessageSize, MaxConfigurableMessageSize,
		"MaxConfigurableMessageSize must match the cross-language fixture")
	assert.Equal(t, limits.MaxIncompleteChunked, DefaultMaxIncompleteChunked,
		"DefaultMaxIncompleteChunked must match the cross-language fixture")
	assert.Equal(t, limits.PingMethod, PingMethod,
		"PingMethod must match the cross-language fixture the browser client opens the channel with")
	assert.Equal(t, limits.SessionKeyMaxAgeMs, SessionKeyMaxAge.Milliseconds(),
		"SessionKeyMaxAge must match the cross-language fixture")
	assert.Equal(t, limits.MinRekeyIntervalMs, MinRekeyInterval.Milliseconds(),
		"MinRekeyInterval must match the cross-language fixture")
	assert.Equal(t, limits.SessionKeyHardCeilingMs, SessionKeyHardCeiling.Milliseconds(),
		"SessionKeyHardCeiling must match the cross-language fixture")
	assert.Equal(t, limits.CloseReasonTooManyConns, CloseReasonTooManyConnections,
		"CloseReasonTooManyConnections must match the cross-language fixture the browser branches on")
	assert.Equal(t, limits.CloseReasonSnapshotTooLarge, CloseReasonSnapshotTooLarge,
		"CloseReasonSnapshotTooLarge must match the cross-language fixture the browser branches on")
	assert.Equal(t, limits.CloseReasonForbidden, CloseReasonForbidden,
		"CloseReasonForbidden must match the cross-language fixture the browser branches on")
	assert.Equal(t, limits.CloseReasonControlFlood, CloseReasonControlFlood,
		"CloseReasonControlFlood must match the cross-language fixture the browser branches on")
	// RFC 6455 caps a close reason at 123 bytes and coder/websocket rejects a
	// longer one on send, so a token that outgrew it would not be a worse
	// message -- it would be no close frame at all.
	for _, reason := range []string{
		CloseReasonTooManyConnections, CloseReasonSnapshotTooLarge,
		CloseReasonForbidden, CloseReasonControlFlood,
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

	t.Run("a multi-chunk payload splits at MaxPlaintextPerChunk with MORE on all but the last", func(t *testing.T) {
		// Two full chunks plus a ragged tail.
		plaintext := make([]byte, 2*MaxPlaintextPerChunk+MaxPlaintextPerChunk/2)
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
		plaintext := make([]byte, 2*MaxPlaintextPerChunk)
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
		plaintext := make([]byte, MaxPlaintextPerChunk+1)
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

func TestNewChannelMessage(t *testing.T) {
	msg := NewChannelMessage("ch", 42, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, []byte("ct"))
	assert.Equal(t, uint32(1), msg.GetProtocolVersion())
	assert.Equal(t, "ch", msg.GetChannelId())
	assert.Equal(t, uint64(42), msg.GetCorrelationId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, msg.GetFlags())
	assert.Equal(t, []byte("ct"), msg.GetCiphertext())
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
		{"close is valid and carries no continuation", leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, false, true},
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
// -> InnerMessage) still fits under MaxReassembledMessageSize(MaxMessageSize),
// and that the overhead stays under half of InnerEnvelopeHeadroom so CI
// reddens long before a mid-stream drop. A tautological
// DefaultMaxReassembledMessageSize > MaxMessageSize assert would prove nothing.
func TestWidestEnvelopeFitsUnderHeadroom(t *testing.T) {
	encoded := widestWatchEventsEnvelope(t, MaxMessageSize)
	ceiling := MaxReassembledMessageSize(MaxMessageSize)
	assert.LessOrEqual(t, len(encoded), ceiling,
		"a max-sized payload in the widest WatchEvents fan-out must fit under the reassembled ceiling; "+
			"while payload and ceiling were equal it did not, and the drop had no recovery path")
	overhead := len(encoded) - MaxMessageSize
	assert.Greater(t, overhead, 0, "the envelope must actually cost something, or this proves nothing")
	assert.Less(t, overhead, InnerEnvelopeHeadroom/2,
		"envelope overhead has grown into over half the headroom (%d of %d bytes); "+
			"raise InnerEnvelopeHeadroom rather than letting it converge", overhead, InnerEnvelopeHeadroom)
}

// TestInnerRpcEnvelopeFitsUnderHeadroom is the file-read / unary-RPC twin of
// TestWidestEnvelopeFitsUnderHeadroom: a non-WatchEvents producer must not
// silently be the wider case.
func TestInnerRpcEnvelopeFitsUnderHeadroom(t *testing.T) {
	encoded := widestInnerRpcEnvelope(t, MaxMessageSize)
	ceiling := MaxReassembledMessageSize(MaxMessageSize)
	assert.LessOrEqual(t, len(encoded), ceiling)
	overhead := len(encoded) - MaxMessageSize
	assert.Greater(t, overhead, 0)
	assert.Less(t, overhead, InnerEnvelopeHeadroom/2,
		"InnerRpc envelope overhead has grown into over half the headroom (%d of %d bytes); "+
			"raise InnerEnvelopeHeadroom rather than letting it converge", overhead, InnerEnvelopeHeadroom)
}

// TestHeadroomCoversConfiguredMaxMessageSize pins that headroom is enough at
// non-default payload sizes too -- not only at the 16 MiB default -- so a
// varint-length growth or nesting change cannot leave larger configs short.
func TestHeadroomCoversConfiguredMaxMessageSize(t *testing.T) {
	for _, size := range []int{1024 * 1024, 4 * 1024 * 1024, MaxConfigurableMessageSize} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			encoded := widestWatchEventsEnvelope(t, size)
			assert.LessOrEqual(t, len(encoded), MaxReassembledMessageSize(size),
				"widest fan-out at payload %d must fit under MaxReassembledMessageSize", size)
			overhead := len(encoded) - size
			assert.Less(t, overhead, InnerEnvelopeHeadroom/2,
				"overhead at payload %d (%d) ate half of headroom %d", size, overhead, InnerEnvelopeHeadroom)
		})
	}
}

func TestResolveAndValidateMaxMessageSize(t *testing.T) {
	assert.Equal(t, MaxMessageSize, ResolveMaxMessageSize(0))
	assert.Equal(t, MaxMessageSize, ResolveMaxMessageSize(-1))
	assert.Equal(t, 32*1024*1024, ResolveMaxMessageSize(32*1024*1024))
	assert.Equal(t, MaxMessageSize+InnerEnvelopeHeadroom, MaxReassembledMessageSize(MaxMessageSize))

	require.NoError(t, ValidateMaxMessageSize(MaxMessageSize))
	require.NoError(t, ValidateMaxMessageSize(MaxPlaintextPerChunk))
	require.NoError(t, ValidateMaxMessageSize(MaxConfigurableMessageSize))
	assert.Error(t, ValidateMaxMessageSize(MaxPlaintextPerChunk-1))
	assert.Error(t, ValidateMaxMessageSize(MaxConfigurableMessageSize+1))

	require.NoError(t, ValidateConfiguredMaxMessageSize(0))
	require.NoError(t, ValidateConfiguredMaxMessageSize(-1))
	require.NoError(t, ValidateConfiguredMaxMessageSize(MaxMessageSize))
	assert.Error(t, ValidateConfiguredMaxMessageSize(1))

	got, err := IntFromUint64(uint64(MaxMessageSize))
	require.NoError(t, err)
	assert.Equal(t, MaxMessageSize, got)
	_, err = IntFromUint64(^uint64(0))
	require.Error(t, err)

	assert.Equal(t, 8*1024*1024, NegotiatePayloadBudget(16*1024*1024, 8*1024*1024))
	assert.Equal(t, 8*1024*1024, NegotiatePayloadBudget(8*1024*1024, 16*1024*1024))
	assert.Equal(t, MaxMessageSize, NegotiatePayloadBudget(MaxMessageSize, MaxMessageSize))

	adopted, err := AdoptWireMaxMessageSize(uint64(MaxMessageSize))
	require.NoError(t, err)
	assert.Equal(t, MaxMessageSize, adopted)
	_, err = AdoptWireMaxMessageSize(0)
	require.ErrorContains(t, err, "no max_message_size")
	_, err = AdoptWireMaxMessageSize(uint64(MaxPlaintextPerChunk - 1))
	require.Error(t, err)
	_, err = AdoptWireMaxMessageSize(uint64(MaxConfigurableMessageSize + 1))
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
		DeliveryError:      strings.Repeat("e", 1024),
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
				Payload:      watchBytes,
				End:          true,
				IsError:      true,
				ErrorCode:    int32(^uint32(0) >> 1),
				ErrorMessage: strings.Repeat("e", 4096),
			},
		},
	}
	encoded, err := proto.Marshal(envelope)
	require.NoError(t, err)
	return encoded
}

func widestInnerRpcEnvelope(t *testing.T, payloadSize int) []byte {
	t.Helper()
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Response{
			Response: &leapmuxv1.InnerRpcResponse{
				Payload:      make([]byte, payloadSize),
				IsError:      true,
				ErrorMessage: strings.Repeat("e", 4096),
				ErrorCode:    int32(^uint32(0) >> 1),
			},
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
		got := UserEventsURL("https://hub", []string{"w1", "w2"}, nil, 0)
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
			got := UserEventsURL("https://hub", nil, cursor, 7)
			assert.NotContains(t, got, "resume_after_hlc", "cursor %v round-trips to a 400", cursor)
			assert.NotContains(t, got, "resume_epoch")
		}
	})
	t.Run("cursor appends resume params", func(t *testing.T) {
		hlc := &leapmuxv1.HLC{Physical: 1754100000000, Logical: 3, ClientId: "c-abc"}
		got := UserEventsURL("https://hub", nil, hlc, 7)
		assert.Contains(t, got, "resume_after_hlc=1754100000000.3.c-abc")
		assert.Contains(t, got, "resume_epoch=7")
	})
	t.Run("nil cursor emits no resume params", func(t *testing.T) {
		got := UserEventsURL("https://hub", nil, nil, 1)
		assert.NotContains(t, got, "resume_after_hlc")
		assert.NotContains(t, got, "resume_epoch")
	})
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
		// parse check. `current_epoch` is `NOT NULL DEFAULT 1` in every dialect and
		// only increments, so these are values the hub can never hold; without the
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

// It lives here rather than in the hub service suite because the function
// moved to sit beside UserEventsURL, which writes the very keys it reads.
