package msgcodec

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestCompressDecompressConcurrentRoundTrips(t *testing.T) {
	for _, size := range []int{1, 2, 64, 1 << 20} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			t.Parallel()
			input := bytes.Repeat([]byte{0, 255, 1, 128}, (size+3)/4)[:size]
			for range 8 {
				compressed, compression := Compress(input)
				decoded, err := Decompress(compressed, compression)
				require.NoError(t, err)
				require.Equal(t, input, decoded)
			}
		})
	}
}

func TestDecompressRejectsInvalidFrames(t *testing.T) {
	compressed, compression := Compress(bytes.Repeat([]byte("message content"), 1024))
	for label, input := range map[string][]byte{
		"invalid frame":   {1, 2, 3, 4},
		"truncated frame": compressed[:len(compressed)/2],
	} {
		t.Run(label, func(t *testing.T) {
			_, err := Decompress(input, compression)
			require.Error(t, err)
		})
	}
}

func TestCompressDecompressRoundTrip(t *testing.T) {
	inputs := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, world!"}]}}`,
		`{"content":"short"}`,
		`{}`,
		// Repetitive content that benefits from compression.
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` +
			"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
			"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
			"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
			`"}]}}`,
	}

	for _, input := range inputs {
		data := []byte(input)
		compressed, compression := Compress(data)
		assert.Equal(t, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD, compression)

		decompressed, err := Decompress(compressed, compression)
		require.NoError(t, err)
		assert.Equal(t, data, decompressed)
	}
}

// Empty input is no content: Compress stores no bytes under no compression,
// so a field that carries nothing never holds a frame that decodes back to
// nothing.
func TestCompressEmptyIsNoContent(t *testing.T) {
	compressed, compression := Compress(nil)
	assert.Equal(t, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, compression)
	assert.Empty(t, compressed)

	decompressed, err := Decompress(compressed, compression)
	require.NoError(t, err)
	assert.Empty(t, decompressed)
}

func TestDecompressNone(t *testing.T) {
	data := []byte(`{"content":"hello"}`)
	result, err := Decompress(data, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
	require.NoError(t, err)
	assert.Equal(t, data, result)
}

func TestDecompressUnknownReturnsError(t *testing.T) {
	data := []byte(`{"content":"hello"}`)
	_, err := Decompress(data, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_UNSPECIFIED)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported compression")
}

func TestDecompressUnsupportedValueReturnsError(t *testing.T) {
	data := []byte(`{"content":"hello"}`)
	_, err := Decompress(data, leapmuxv1.ContentCompression(99))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported compression")
}
