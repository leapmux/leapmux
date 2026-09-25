package amp

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height))))
	return buf.Bytes()
}

// paddedPNG is a valid PNG header followed by padding, so its size is what a
// test needs. Amp and the worker read only the header.
func paddedPNG(t *testing.T, size int) []byte {
	t.Helper()
	data := pngBytes(t, 2, 2)
	require.Less(t, len(data), size)
	return append(data, make([]byte, size-len(data))...)
}

func webpHeader(chunk string, body []byte) []byte {
	data := []byte("RIFF\x00\x00\x00\x00WEBP" + chunk + "\x00\x00\x00\x00")
	data = append(data, body...)
	for len(data) < 30 {
		data = append(data, 0)
	}
	return data
}

func decodeLine(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(line, &decoded))
	return decoded
}

func lineBlocks(t *testing.T, line []byte) []map[string]any {
	t.Helper()
	message := decodeLine(t, line)["message"].(map[string]any)
	var blocks []map[string]any
	for _, block := range message["content"].([]any) {
		blocks = append(blocks, block.(map[string]any))
	}
	return blocks
}

func TestBuildUserLineText(t *testing.T) {
	t.Parallel()
	line, err := buildUserLine("hello", nil, false)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`, string(line))

	line, err = buildUserLine("", nil, false)
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{{"type": "text", "text": ""}}, lineBlocks(t, line), "an empty message is one empty text block")
}

func TestBuildUserLineSteer(t *testing.T) {
	t.Parallel()
	line, err := buildUserLine("also this", nil, true)
	require.NoError(t, err)
	assert.Equal(t, true, decodeLine(t, line)["steer"])
}

func TestBuildUserLineTextAttachment(t *testing.T) {
	t.Parallel()
	line, err := buildUserLine("read it", []*leapmuxv1.Attachment{{Filename: "notes.md", Data: []byte("# Notes")}}, false)
	require.NoError(t, err)
	blocks := lineBlocks(t, line)
	require.Len(t, blocks, 2)
	assert.Equal(t, "read it", blocks[0]["text"])
	assert.Equal(t, "text", blocks[1]["type"])
	assert.Equal(t, "----- BEGIN ATTACHED FILE: notes.md (text/markdown) -----\n# Notes\n----- END ATTACHED FILE: notes.md -----", blocks[1]["text"])
}

func TestBuildUserLineAttachmentOnly(t *testing.T) {
	t.Parallel()
	line, err := buildUserLine("", []*leapmuxv1.Attachment{{Filename: "a.png", MimeType: "image/png", Data: pngBytes(t, 1, 1)}}, false)
	require.NoError(t, err)
	blocks := lineBlocks(t, line)
	require.Len(t, blocks, 1, "no empty text block precedes an attachment")
	assert.Equal(t, "image", blocks[0]["type"])
}

// Amp compares the declared type with the bytes, so the type comes from the
// bytes.
func TestBuildUserLineImageTakesTheTypeOfItsBytes(t *testing.T) {
	t.Parallel()
	data := pngBytes(t, 3, 2)
	line, err := buildUserLine("look", []*leapmuxv1.Attachment{{Filename: "shot.jpg", MimeType: "image/jpeg", Data: data}}, false)
	require.NoError(t, err)
	blocks := lineBlocks(t, line)
	require.Len(t, blocks, 2)
	assert.Equal(t, map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": "image/png",
			"data":       base64.StdEncoding.EncodeToString(data),
		},
	}, blocks[1])
}

func TestBuildUserLineImageCount(t *testing.T) {
	t.Parallel()
	images := func(n int) []*leapmuxv1.Attachment {
		out := make([]*leapmuxv1.Attachment, n)
		for i := range out {
			out[i] = &leapmuxv1.Attachment{Filename: "a.png", MimeType: "image/png", Data: pngBytes(t, 1, 1)}
		}
		return out
	}
	_, err := buildUserLine("four", images(maxImagesPerLine), false)
	require.NoError(t, err)
	_, err = buildUserLine("five", images(maxImagesPerLine+1), false)
	assert.ErrorContains(t, err, "holds 5 images, and Amp takes at most 4")
}

func TestBuildUserLineSize(t *testing.T) {
	t.Parallel()
	// `[{"type":"text","text":""}]` is 27 bytes, so this text fills the line.
	const overhead = 27
	_, err := buildUserLine(strings.Repeat("x", maxLineContentBytes-overhead), nil, false)
	require.NoError(t, err)
	_, err = buildUserLine(strings.Repeat("x", maxLineContentBytes-overhead+1), nil, false)
	assert.ErrorContains(t, err, "too large for Amp")

	// A multi-byte character counts as its UTF-8 bytes, as Amp counts it.
	_, err = buildUserLine(strings.Repeat("é", (maxLineContentBytes-overhead)/2+1), nil, false)
	assert.ErrorContains(t, err, "too large for Amp")
}

// Two images that each fit alone can still overflow one line together.
func TestBuildUserLineRefusesImagesThatOverflowTogether(t *testing.T) {
	t.Parallel()
	picture := &leapmuxv1.Attachment{Filename: "a.png", MimeType: "image/png", Data: paddedPNG(t, 450_000)}
	require.NoError(t, validateAttachment(agent.ClassifyAttachments([]*leapmuxv1.Attachment{picture})[0]))
	_, err := buildUserLine("both", []*leapmuxv1.Attachment{picture, picture}, false)
	assert.ErrorContains(t, err, "Remove an image or shorten the text")
}

func TestBuildUserLineRefusesPDFAndBinary(t *testing.T) {
	t.Parallel()
	_, err := buildUserLine("", []*leapmuxv1.Attachment{{Filename: "spec.pdf", Data: []byte("%PDF-1.7")}}, false)
	assert.ErrorContains(t, err, "Amp does not support PDF attachments: spec.pdf")
	_, err = buildUserLine("", []*leapmuxv1.Attachment{{Filename: "blob.bin", Data: []byte{0xff, 0xfe, 0x00}}}, false)
	assert.ErrorContains(t, err, "Amp does not support binary attachments: blob.bin")
}

func TestValidateAttachment(t *testing.T) {
	t.Parallel()
	gifData := func() []byte {
		var buf bytes.Buffer
		require.NoError(t, gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 2, 2), []color.Color{color.Black}), nil))
		return buf.Bytes()
	}()
	jpegData := func() []byte {
		var buf bytes.Buffer
		require.NoError(t, jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2)), nil))
		return buf.Bytes()
	}()
	cases := []struct {
		name    string
		file    *leapmuxv1.Attachment
		wantErr string
	}{
		{name: "text", file: &leapmuxv1.Attachment{Filename: "main.go", Data: []byte("package main\n")}},
		{name: "png", file: &leapmuxv1.Attachment{Filename: "a.png", Data: pngBytes(t, 4, 4)}},
		{name: "jpeg", file: &leapmuxv1.Attachment{Filename: "a.jpg", Data: jpegData}},
		{name: "gif", file: &leapmuxv1.Attachment{Filename: "a.gif", Data: gifData}},
		{name: "webp", file: &leapmuxv1.Attachment{Filename: "a.webp", Data: webpHeader("VP8L", []byte{0x2f, 0, 0, 0, 0})}},
		{name: "pdf", file: &leapmuxv1.Attachment{Filename: "a.pdf", Data: []byte("%PDF")}, wantErr: "does not support PDF attachments"},
		{name: "binary", file: &leapmuxv1.Attachment{Filename: "a.bin", Data: []byte{0, 0xff}}, wantErr: "does not support binary attachments"},
		{name: "empty image", file: &leapmuxv1.Attachment{Filename: "a.png", MimeType: "image/png"}, wantErr: "the image a.png is empty"},
		{
			name:    "image of an unknown type",
			file:    &leapmuxv1.Attachment{Filename: "a.png", MimeType: "image/png", Data: []byte("BM not a png")},
			wantErr: "is not a PNG, JPEG, GIF or WebP image",
		},
		{
			name:    "unnamed image",
			file:    &leapmuxv1.Attachment{MimeType: "image/png", Data: []byte("nope")},
			wantErr: "the image (unnamed)",
		},
		{
			name:    "image larger than Amp takes",
			file:    &leapmuxv1.Attachment{Filename: "big.png", Data: paddedPNG(t, maxImageBytes+1)},
			wantErr: "is 5138023 bytes, and Amp takes at most 5138022",
		},
		{
			name:    "image wider than Amp takes",
			file:    &leapmuxv1.Attachment{Filename: "wide.png", Data: pngBytes(t, maxImageDimension+1, 1)},
			wantErr: "is 8001x1 pixels",
		},
		{
			name:    "image that fills a line alone",
			file:    &leapmuxv1.Attachment{Filename: "full.png", Data: paddedPNG(t, 800_000)},
			wantErr: "Attach a smaller image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{tc.file})
			require.Len(t, classified, 1)
			err := ampProvider{}.ValidateAttachment(classified[0])
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestImageAtTheDimensionLimitPasses(t *testing.T) {
	t.Parallel()
	classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "edge.png", Data: pngBytes(t, maxImageDimension, 1)}})
	assert.NoError(t, validateAttachment(classified[0]))
}

// An image whose type the worker detects but whose header it cannot read
// passes to Amp, which judges the image itself. The worker refuses only what it
// can show to be past a limit.
func TestImageWithAnUnreadableHeaderPassesToAmp(t *testing.T) {
	t.Parallel()
	data := append([]byte("\x89PNG\r\n\x1a\n"), "not a header"...)
	attachments := []*leapmuxv1.Attachment{{Filename: "odd.png", Data: data}}
	_, _, ok := imageDimensions("image/png", data)
	require.False(t, ok, "the header cannot be read")
	assert.NoError(t, validateAttachment(agent.ClassifyAttachments(attachments)[0]))

	line, err := buildUserLine("", attachments, false)
	require.NoError(t, err)
	blocks := lineBlocks(t, line)
	require.Len(t, blocks, 1)
	assert.Equal(t, "image/png", blocks[0]["source"].(map[string]any)["media_type"])
}

// jsonSize counts the UTF-8 bytes of the JSON, as Amp counts them. HTML
// characters stay as they are, as JavaScript writes them, and U+2028 takes its
// escape, which only overstates the size.
func TestJSONSize(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]int{
		"":      2,
		"<a&b>": 7,
		"é":     4,
		" ":     8,
	} {
		size, err := jsonSize(value)
		require.NoError(t, err)
		assert.Equalf(t, want, size, "%q", value)
	}
}

func TestDetectImageType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "image/png", detectImageType(pngBytes(t, 1, 1)))
	assert.Equal(t, "image/jpeg", detectImageType([]byte{0xff, 0xd8, 0xff, 0xe0}))
	assert.Equal(t, "image/gif", detectImageType([]byte("GIF87a....")))
	assert.Equal(t, "image/gif", detectImageType([]byte("GIF89a....")))
	assert.Equal(t, "image/webp", detectImageType(webpHeader("VP8 ", nil)))
	assert.Empty(t, detectImageType([]byte("RIFF\x00\x00\x00\x00WAVE")))
	assert.Empty(t, detectImageType([]byte("RIFF")), "a header shorter than WebP's is not WebP")
	assert.Empty(t, detectImageType(nil))
}

func TestWebPDimensions(t *testing.T) {
	t.Parallel()
	lossy := make([]byte, 10)
	copy(lossy[3:6], []byte{0x9d, 0x01, 0x2a})
	binary.LittleEndian.PutUint16(lossy[6:8], 640|0xc000) // the top two bits are the scale, not the size
	binary.LittleEndian.PutUint16(lossy[8:10], 480)
	width, height, ok := webpDimensions(webpHeader("VP8 ", lossy))
	require.True(t, ok)
	assert.Equal(t, [2]int{640, 480}, [2]int{width, height})

	lossless := make([]byte, 5)
	lossless[0] = 0x2f
	binary.LittleEndian.PutUint32(lossless[1:5], uint32(1023)|uint32(767)<<14)
	width, height, ok = webpDimensions(webpHeader("VP8L", lossless))
	require.True(t, ok)
	assert.Equal(t, [2]int{1024, 768}, [2]int{width, height})

	extended := make([]byte, 10)
	extended[4], extended[5], extended[6] = 0x3f, 0x1f, 0x00 // 8000 - 1
	extended[7], extended[8], extended[9] = 0x00, 0x00, 0x00
	width, height, ok = webpDimensions(webpHeader("VP8X", extended))
	require.True(t, ok)
	assert.Equal(t, [2]int{8000, 1}, [2]int{width, height})

	_, _, ok = webpDimensions(webpHeader("VP8 ", make([]byte, 10)))
	assert.False(t, ok, "a lossy header with no start code is unreadable")
	_, _, ok = webpDimensions(webpHeader("VP8L", []byte{0x00}))
	assert.False(t, ok, "a lossless header with no signature is unreadable")
	_, _, ok = webpDimensions(webpHeader("ALPH", nil))
	assert.False(t, ok)
	_, _, ok = webpDimensions([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"))
	assert.False(t, ok, "a truncated header is unreadable")
}

func TestWebPWiderThanAmpTakesIsRefused(t *testing.T) {
	t.Parallel()
	extended := make([]byte, 10)
	extended[4], extended[5] = 0x40, 0x1f // 8001 - 1
	classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "wide.webp", Data: webpHeader("VP8X", extended)}})
	assert.ErrorContains(t, validateAttachment(classified[0]), "is 8001x1 pixels")
}
