package imageheader

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadEncodedImageHeaders(t *testing.T) {
	t.Parallel()
	picture := image.NewRGBA(image.Rect(0, 0, 7, 9))
	for _, tt := range []struct {
		mime   string
		encode func(*bytes.Buffer) error
	}{
		{"image/png", func(b *bytes.Buffer) error { return png.Encode(b, picture) }},
		{"image/jpeg", func(b *bytes.Buffer) error { return jpeg.Encode(b, picture, nil) }},
		{"image/gif", func(b *bytes.Buffer) error {
			return gif.Encode(b, image.NewPaletted(picture.Rect, color.Palette{color.Black, color.White}), nil)
		}},
	} {
		t.Run(tt.mime, func(t *testing.T) {
			t.Parallel()
			var data bytes.Buffer
			require.NoError(t, tt.encode(&data))
			config, err := Read(data.Bytes())
			require.NoError(t, err)
			assert.Equal(t, Config{MIMEType: tt.mime, Width: 7, Height: 9}, config)
		})
	}
}

func TestReadRejectsAbsentAndMalformedHeaders(t *testing.T) {
	t.Parallel()
	for _, data := range [][]byte{nil, {}, []byte("not an image"), []byte("\x89PNG\r\n\x1a\n"), []byte("GIF89a"), {0xff, 0xd8, 0xff}, []byte("RIFF\x00\x00\x00\x00WEBPVP8X")} {
		_, err := Read(data)
		assert.Error(t, err)
	}
}

func TestReadWebPHeaderLengthAndDimensionBoundaries(t *testing.T) {
	t.Parallel()
	header := make([]byte, 30)
	copy(header, []byte("RIFF\x00\x00\x00\x00WEBPVP8X"))
	for length := 0; length < len(header); length++ {
		_, err := Read(header[:length])
		assert.Error(t, err, "length %d", length)
	}
	config, err := Read(header)
	require.NoError(t, err)
	assert.Equal(t, Config{MIMEType: "image/webp", Width: 1, Height: 1}, config)
	header[24], header[25], header[26] = 0xff, 0xff, 0xff
	config, err = Read(header)
	require.NoError(t, err)
	assert.Equal(t, 16777216, config.Width)
	copy(header[12:16], []byte("VP8 "))
	copy(header[23:26], []byte{0x9d, 0x01, 0x2a})
	binary.LittleEndian.PutUint16(header[26:28], 0)
	binary.LittleEndian.PutUint16(header[28:30], 1)
	_, err = Read(header)
	assert.Error(t, err)
}

func TestReadVP8LUsesItsOwnMinimumHeaderLength(t *testing.T) {
	t.Parallel()
	header := make([]byte, 25)
	copy(header, []byte("RIFF\x00\x00\x00\x00WEBPVP8L"))
	header[20] = 0x2f
	for length := 0; length < len(header); length++ {
		_, err := Read(header[:length])
		assert.Error(t, err, "length %d", length)
	}
	config, err := Read(header)
	require.NoError(t, err, "VP8L has a five-byte image header, unlike the ten-byte VP8X header")
	assert.Equal(t, Config{MIMEType: "image/webp", Width: 1, Height: 1}, config)
}
