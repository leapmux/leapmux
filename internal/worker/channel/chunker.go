package channel

import "github.com/leapmux/leapmux/internal/noise"

const (
	// MaxChunkPlaintext is the maximum plaintext bytes per Noise transport message.
	MaxChunkPlaintext = noise.MaxPlaintextSize // 65,519

	// DefaultMaxMessageSize is the maximum reassembled message size (16 MiB).
	DefaultMaxMessageSize = 16 * 1024 * 1024

	// DefaultMaxIncompleteChunked is the maximum number of in-flight chunked
	// sequences per channel before new ones are rejected.
	DefaultMaxIncompleteChunked = 4
)

// chunkBuffer accumulates decrypted plaintext chunks for one logical message.
type chunkBuffer struct {
	parts [][]byte
	total int
}
