package msgcodec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

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
