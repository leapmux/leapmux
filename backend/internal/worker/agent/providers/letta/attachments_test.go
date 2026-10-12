package letta

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestBuildUserMessagesEncodesNativeImageSource(t *testing.T) {
	t.Parallel()
	image := []byte{0x89, 'P', 'N', 'G', 0x00}
	messages, err := buildUserMessages("Describe the image.", []*leapmuxv1.Attachment{{
		Filename: "shot.png", MimeType: "image/png", Data: image,
	}})
	require.NoError(t, err)
	raw, err := json.Marshal(messages)
	require.NoError(t, err)
	var decoded []struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded, 1)
	assert.Equal(t, "user", decoded[0].Role)
	require.Len(t, decoded[0].Content, 2)
	assert.Equal(t, "Describe the image.", decoded[0].Content[0].Text)
	assert.Equal(t, "image", decoded[0].Content[1].Type)
	assert.Equal(t, "base64", decoded[0].Content[1].Source.Type)
	assert.Equal(t, "image/png", decoded[0].Content[1].Source.MediaType)
	assert.Equal(t, base64.StdEncoding.EncodeToString(image), decoded[0].Content[1].Source.Data)
}

func TestBuildUserMessagesKeepsTextAttachmentBytes(t *testing.T) {
	t.Parallel()
	messages, err := buildUserMessages("Read the note.", []*leapmuxv1.Attachment{{
		Filename: "notes.txt", MimeType: "text/plain", Data: []byte("unique-note-42"),
	}})
	require.NoError(t, err)
	raw, err := json.Marshal(messages)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Read the note.")
	assert.Contains(t, string(raw), "unique-note-42")
}

func TestBuildUserMessagesRejectsInvalidImages(t *testing.T) {
	t.Parallel()
	for _, attachment := range []*leapmuxv1.Attachment{
		{Filename: "empty.png", MimeType: "image/png"},
		{Filename: "unsupported.heic", MimeType: "image/heic", Data: []byte{1, 2, 3}},
	} {
		_, err := buildUserMessages("Describe it.", []*leapmuxv1.Attachment{attachment})
		require.Error(t, err, "the native Letta message must reject %s", attachment.Filename)
	}
}
