// Package msgcodec provides message content compression and decompression.
package msgcodec

import (
	"fmt"

	"github.com/klauspost/compress/zstd"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Package-level encoder/decoder, safe for concurrent use.
var (
	encoder *zstd.Encoder
	decoder *zstd.Decoder
)

func init() {
	var err error
	encoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(fmt.Sprintf("msgcodec: init zstd encoder: %v", err))
	}
	decoder, err = zstd.NewReader(nil)
	if err != nil {
		panic(fmt.Sprintf("msgcodec: init zstd decoder: %v", err))
	}
}

// Compress compresses the given data using zstd and returns the compressed
// bytes along with the corresponding ContentCompression enum value.
func Compress(data []byte) ([]byte, leapmuxv1.ContentCompression) {
	compressed := encoder.EncodeAll(data, make([]byte, 0, len(data)/2))
	return compressed, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD
}

// Decompress decompresses data according to the given compression algorithm.
// Returns an error for UNKNOWN or unsupported compression values.
func Decompress(data []byte, compression leapmuxv1.ContentCompression) ([]byte, error) {
	switch compression {
	case leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD:
		return decoder.DecodeAll(data, nil)
	case leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE:
		return data, nil
	default:
		return nil, fmt.Errorf("msgcodec: unsupported compression: %v", compression)
	}
}
