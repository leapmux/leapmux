// Package channelproto provides shared wire-format helpers and constants for
// the E2EE channel relay protocol used between Frontend/Desktop and Hub.
package channelproto

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	// MaxPlaintextPerChunk is the maximum plaintext bytes per Noise transport
	// message (65535 max ciphertext - 16 byte AEAD auth tag).
	MaxPlaintextPerChunk = 65535 - 16

	// DefaultMaxMessageSize is the maximum reassembled message size (16 MiB).
	DefaultMaxMessageSize = 16 * 1024 * 1024

	// WSReadLimit is the WebSocket per-message read limit for channel relays.
	// It must exceed the max ciphertext size to accommodate the 4-byte length
	// prefix and protobuf framing of a ChannelMessage.
	WSReadLimit = 65535 + 4096
)

// WriteChannelMessage writes a length-prefixed ChannelMessage to a WebSocket.
// Wire format: [4 bytes big-endian length][protobuf-encoded ChannelMessage]
func WriteChannelMessage(ctx context.Context, ws *websocket.Conn, msg *leapmuxv1.ChannelMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(data)))
	copy(buf[4:], data)
	return ws.Write(ctx, websocket.MessageBinary, buf)
}

// ReadChannelMessage reads a length-prefixed ChannelMessage from a WebSocket.
func ReadChannelMessage(ctx context.Context, ws *websocket.Conn) (*leapmuxv1.ChannelMessage, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("message too short")
	}
	length := binary.BigEndian.Uint32(data[:4])
	if int(length) != len(data)-4 {
		return nil, fmt.Errorf("length mismatch: header=%d, actual=%d", length, len(data)-4)
	}
	msg := &leapmuxv1.ChannelMessage{}
	if err := proto.Unmarshal(data[4:], msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return msg, nil
}
