package antigravity

import (
	"encoding/base64"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityAttachmentsMapToMediaAndText(t *testing.T) {
	a, _ := testAgent(t)
	items, media, err := a.buildMessageItems("inspect", []*leapmuxv1.Attachment{
		{Filename: "notes.yaml", MimeType: "application/yaml", Data: []byte("key: value")},
		{Filename: "document.pdf", MimeType: "application/pdf", Data: []byte{1, 2}},
		{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{3}},
		{Filename: "video.mp4", MimeType: "video/mp4", Data: []byte{}},
	}, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Contains(t, items[0]["text"], "notes.yaml")
	require.Len(t, media, 3)
	assert.Equal(t, "application/pdf", media[0].MIMEType)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{1, 2}), base64.StdEncoding.EncodeToString(media[0].InlineData))
}

func TestAntigravityAttachmentRefusesUnsupportedType(t *testing.T) {
	a, _ := testAgent(t)
	_, _, err := a.buildMessageItems("", []*leapmuxv1.Attachment{{Filename: "image.gif", MimeType: "image/gif", Data: []byte{1}}}, "")
	assert.ErrorContains(t, err, "image/gif")
	a.model = "gpt-oss-120b"
	a.catalog.variants["gpt-oss-120b"] = map[string]catalogVariant{"medium": {token: "MODEL_GPT", mimeTypes: nil}}
	_, _, err = a.buildMessageItems("", []*leapmuxv1.Attachment{{Filename: "image.png", MimeType: "image/png", Data: []byte{1}}}, "")
	assert.ErrorContains(t, err, "cannot read image/png")
}

func TestAntigravityAttachmentLimit(t *testing.T) {
	a, _ := testAgent(t)
	_, _, err := a.buildMessageItems("", []*leapmuxv1.Attachment{{
		Filename: "large.pdf", MimeType: "application/pdf", Data: make([]byte, antigravityMaxAttachmentBytes+1),
	}}, "")
	assert.ErrorContains(t, err, "exceed")
}
