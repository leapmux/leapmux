package channelwire

import (
	"encoding/json"
	"errors"
	"os"
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
// TypeScript browser client (frontend/src/lib/channel.test.ts) asserts too. Both
// ends chunk and reassemble the same encrypted channel messages, so a retune on
// one side that is not mirrored on the other would silently reject or mis-split a
// legitimate message at the un-updated receiver; keeping both constant sets tied
// to one fixture turns that drift into a red build here instead. See the fixture's
// _readme.
func TestChannelWireLimitsMatchCrossLanguageFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/channelwire_limits.json")
	require.NoError(t, err)

	var limits struct {
		MaxPlaintextPerChunk int    `json:"maxPlaintextPerChunk"`
		MaxMessageSize       int    `json:"maxMessageSize"`
		MaxIncompleteChunked int    `json:"maxIncompleteChunked"`
		PingMethod           string `json:"pingMethod"`
	}
	require.NoError(t, json.Unmarshal(data, &limits))

	assert.Equal(t, limits.MaxPlaintextPerChunk, MaxPlaintextPerChunk,
		"MaxPlaintextPerChunk must match the cross-language fixture")
	assert.Equal(t, limits.MaxMessageSize, DefaultMaxMessageSize,
		"DefaultMaxMessageSize must match the cross-language fixture")
	assert.Equal(t, limits.MaxIncompleteChunked, DefaultMaxIncompleteChunked,
		"DefaultMaxIncompleteChunked must match the cross-language fixture")
	assert.Equal(t, limits.PingMethod, PingMethod,
		"PingMethod must match the cross-language fixture the browser client opens the channel with")
}

// SendChannelFrames is the one place the two Go senders (the worker's
// sendEncrypted and the tunnel's sendInnerContext) frame a chunked message, so
// its split boundaries, MORE flags, and error propagation pin the wire contract
// for both. The empty-payload case is the landmine the boundary math used to
// carry as a standalone helper: it must emit exactly one terminating zero-byte
// frame rather than spin forever.
func TestSendChannelFrames(t *testing.T) {
	// encrypt prepends a 1-byte tag so the ciphertext is distinguishable from the
	// plaintext chunk and the test can assert each frame carries its own chunk.
	encrypt := func(b []byte) ([]byte, error) {
		out := make([]byte, 0, len(b)+1)
		out = append(out, 0x7e)
		out = append(out, b...)
		return out, nil
	}

	run := func(t *testing.T, plaintext []byte) []*leapmuxv1.ChannelMessage {
		var frames []*leapmuxv1.ChannelMessage
		err := SendChannelFrames(encrypt, "ch", 42, plaintext, func(chMsg *leapmuxv1.ChannelMessage) error {
			frames = append(frames, chMsg)
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
		assert.Equal(t, "ch", frames[0].GetChannelId())
		assert.Equal(t, uint64(42), frames[0].GetCorrelationId())
		assert.Equal(t, uint32(1), frames[0].GetProtocolVersion())
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[0].GetFlags(),
			"the sole frame of an empty payload must NOT set MORE")
		// encrypt still ran (the tag byte), so a peer sees one decryptable empty frame.
		assert.Equal(t, []byte{0x7e}, frames[0].GetCiphertext())
	})

	t.Run("a sub-max payload is one frame without MORE", func(t *testing.T) {
		frames := run(t, []byte("abc"))
		require.Len(t, frames, 1)
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[0].GetFlags())
		assert.Equal(t, append([]byte{0x7e}, "abc"...), frames[0].GetCiphertext())
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
			assert.Equal(t, more(i, 3), f.GetFlags(), "frame %d MORE flag", i)
		}
		// Reassembling the ciphertext tails (after stripping the tag) reconstructs
		// the plaintext in order.
		reassembled := make([]byte, 0, len(plaintext))
		for _, f := range frames {
			reassembled = append(reassembled, f.GetCiphertext()[1:]...)
		}
		assert.Equal(t, plaintext, reassembled)
	})

	t.Run("an exact-multiple payload has no empty trailing frame", func(t *testing.T) {
		plaintext := make([]byte, 2*MaxPlaintextPerChunk)
		frames := run(t, plaintext)
		require.Len(t, frames, 2, "an exact two-chunk payload is exactly two frames")
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, frames[0].GetFlags())
		assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, frames[1].GetFlags())
	})

	t.Run("an encrypt error aborts before any frame is sent", func(t *testing.T) {
		boom := errors.New("nonce exhausted")
		var frames []*leapmuxv1.ChannelMessage
		err := SendChannelFrames(func([]byte) ([]byte, error) { return nil, boom }, "ch", 1, []byte("abc"), func(chMsg *leapmuxv1.ChannelMessage) error {
			frames = append(frames, chMsg)
			return nil
		})
		require.ErrorIs(t, err, boom)
		assert.Empty(t, frames, "no frame is sent when encryption fails")
	})

	t.Run("a send error aborts and surfaces the caller's error", func(t *testing.T) {
		boom := errors.New("write ws")
		err := SendChannelFrames(encrypt, "ch", 1, []byte("abc"), func(*leapmuxv1.ChannelMessage) error {
			return boom
		})
		require.ErrorIs(t, err, boom)
	})
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

func TestIsOrgEventsCloseErrorClassifiesRecoverableCloses(t *testing.T) {
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
		require.True(t, IsOrgEventsCloseError(websocket.CloseError{Code: code}),
			"code %d should be recoverable", code)
	}
	// Terminal protocol/policy failures must not collapse to a clean close.
	for _, code := range []websocket.StatusCode{
		websocket.StatusProtocolError,
		websocket.StatusPolicyViolation,
		websocket.StatusInternalError,
		websocket.StatusAbnormalClosure,
	} {
		require.False(t, IsOrgEventsCloseError(websocket.CloseError{Code: code}),
			"code %d should be terminal", code)
	}
}

func TestWebSocketCloseDetailsUsesRecoverableClassifier(t *testing.T) {
	// wasClean tracks the same recoverable classification as IsOrgEventsCloseError
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

// TestMaxMessageSizeExceedsMaxInnerPayload pins the relationship that
// makes a mid-stream drop impossible rather than merely unlikely.
//
// The receiver caps the whole reassembled InnerMessage; a producer caps
// only the payload it puts inside one. While both numbers were 16 MiB, an
// agent line that used its full budget produced an envelope a byte or two
// over the receiver's limit -- and that refusal has no recovery, because
// the ordered encrypted stream has no resync path and the transport never
// errors, so nothing trips the client's reconnect.
// Asserting DefaultMaxMessageSize > MaxInnerPayloadBytes would prove
// nothing: the former is DEFINED as the latter plus the headroom, so any
// such comparison reduces to "the headroom is positive" and holds for
// whatever the constants are edited to. What has to be true is empirical
// -- that a real envelope wrapped around a maximum-sized payload still
// fits under the receiver's cap -- so that is what is measured here.
func TestMaxMessageSizeExceedsMaxInnerPayload(t *testing.T) {
	maxPayload := make([]byte, MaxInnerPayloadBytes)
	// The widest envelope a producer can put a max-sized payload in: a
	// stream frame also carrying an error, which is every field the
	// payload travels beside.
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Stream{
			Stream: &leapmuxv1.InnerStreamMessage{
				Payload:      maxPayload,
				End:          true,
				IsError:      true,
				ErrorCode:    int32(^uint32(0) >> 1),
				ErrorMessage: strings.Repeat("e", 1024),
			},
		},
	}

	encoded, err := proto.Marshal(envelope)
	require.NoError(t, err)

	assert.Greater(t, len(encoded), MaxInnerPayloadBytes,
		"the envelope must actually cost something, or this proves nothing")
	assert.LessOrEqual(t, len(encoded), DefaultMaxMessageSize,
		"a max-sized payload in a real envelope must fit under the receiver's cap; "+
			"while both numbers were 16 MiB it did not, and the drop had no recovery path")
}

// TestInnerEnvelopeHeadroomIsNotConsumedByGrowth pins the slack itself,
// so that adding fields to the envelope shows up here as a shrinking
// margin long before it becomes a silent mid-stream drop.
func TestInnerEnvelopeHeadroomIsNotConsumedByGrowth(t *testing.T) {
	envelope := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Stream{
			Stream: &leapmuxv1.InnerStreamMessage{Payload: make([]byte, MaxInnerPayloadBytes)},
		},
	}
	encoded, err := proto.Marshal(envelope)
	require.NoError(t, err)

	overhead := len(encoded) - MaxInnerPayloadBytes
	assert.Less(t, overhead, InnerEnvelopeHeadroom/2,
		"envelope overhead has grown into over half the headroom (%d of %d bytes); "+
			"raise InnerEnvelopeHeadroom rather than letting it converge", overhead, InnerEnvelopeHeadroom)
}
