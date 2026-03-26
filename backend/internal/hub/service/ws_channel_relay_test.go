package service

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
)

// TestWSReadLimit_AcceptsLargeChunk verifies that a WebSocket connection with
// SetReadLimit(WSReadLimit) accepts messages up to the maximum chunk size
// (maxChunkCiphertext + framing overhead), which would fail with the library's
// default 32KB limit.
func TestWSReadLimit_AcceptsLargeChunk(t *testing.T) {
	// Build a ChannelMessage whose wire format (4-byte length prefix +
	// protobuf) exceeds the default 32KB WebSocket read limit.
	// Use 64KB of ciphertext — the maximum single Noise transport message.
	ciphertext := []byte(strings.Repeat("X", 65535))
	msg := &leapmuxv1.ChannelMessage{
		ChannelId:     "test-channel",
		CorrelationId: 1,
		Ciphertext:    ciphertext,
	}

	data, err := proto.Marshal(msg)
	require.NoError(t, err)

	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)

	// Sanity: the frame must exceed the default 32KB read limit.
	require.Greater(t, len(frame), 32768, "test frame must exceed default WS read limit")

	// Start a test server that reads one message using the relay's read limit.
	received := make(chan *leapmuxv1.ChannelMessage, 1)
	readErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			readErr <- err
			return
		}
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

		ws.SetReadLimit(channelmgr.WSReadLimit)

		got, rerr := readChannelMessage(r.Context(), ws)
		if rerr != nil {
			readErr <- rerr
			return
		}
		received <- got
	}))
	defer srv.Close()

	// Connect and send the large message.
	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

	// Client also needs a higher write limit for large messages.
	err = ws.Write(ctx, websocket.MessageBinary, frame)
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, msg.GetChannelId(), got.GetChannelId())
		assert.Equal(t, msg.GetCorrelationId(), got.GetCorrelationId())
		assert.Equal(t, len(msg.GetCiphertext()), len(got.GetCiphertext()))
	case rerr := <-readErr:
		t.Fatalf("server read failed: %v", rerr)
	}
}

// TestWSReadLimit_DefaultRejectsLargeChunk verifies that without SetReadLimit,
// the default 32KB limit causes a read failure for large messages. This
// confirms the fix is actually necessary.
func TestWSReadLimit_DefaultRejectsLargeChunk(t *testing.T) {
	ciphertext := []byte(strings.Repeat("X", 65535))
	msg := &leapmuxv1.ChannelMessage{
		ChannelId:     "test-channel",
		CorrelationId: 1,
		Ciphertext:    ciphertext,
	}

	data, err := proto.Marshal(msg)
	require.NoError(t, err)

	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)

	// Server does NOT call SetReadLimit — uses library default (32KB).
	readErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			readErr <- err
			return
		}
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

		// No SetReadLimit — default applies.
		_, rerr := readChannelMessage(r.Context(), ws)
		readErr <- rerr
	}))
	defer srv.Close()

	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

	err = ws.Write(ctx, websocket.MessageBinary, frame)
	require.NoError(t, err)

	rerr := <-readErr
	require.Error(t, rerr, "default read limit should reject large messages")
}
