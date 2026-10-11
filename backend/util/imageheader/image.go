package imageheader

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"  // Registers the GIF header decoder.
	_ "image/jpeg" // Registers the JPEG header decoder.
	_ "image/png"  // Registers the PNG header decoder.
)

// Config holds the type and dimensions from encoded image bytes.
type Config struct {
	MIMEType string
	Width    int
	Height   int
}

// Read decodes an image header without allocating the image pixels.
func Read(data []byte) (Config, error) {
	mime := MIMEType(data)
	if mime == "" {
		return Config{}, errors.New("the encoded image type is unsupported")
	}
	width, height, ok := Dimensions(mime, data)
	if !ok || width <= 0 || height <= 0 {
		return Config{}, errors.New("the encoded image header is invalid")
	}
	return Config{MIMEType: mime, Width: width, Height: height}, nil
}

// MIMEType reads an image's type from its first bytes.
// It supports these formats:
// - PNG.
// - JPEG.
// - GIF.
// - WebP.
// Other types return an empty string.
func MIMEType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

// Dimensions reads an image's size from its header. ok is false when it
// cannot read the header. The caller decides whether to accept an unreadable header.
func Dimensions(mediaType string, data []byte) (width, height int, ok bool) {
	if mediaType == "image/webp" {
		return webpDimensions(data)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

// webpDimensions reads the canvas size from the first WebP chunk.
// It supports these chunks:
// - VP8.
// - VP8L.
// - VP8X.
// The standard library has no WebP decoder.
func webpDimensions(data []byte) (width, height int, ok bool) {
	if len(data) < 16 {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8 ":
		if len(data) < 30 {
			return 0, 0, false
		}
		// The frame header follows the 3-byte frame tag and the 3-byte start code.
		if !bytes.Equal(data[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff), true
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		return int(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1,
			int(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1, true
	default:
		return 0, 0, false
	}
}
