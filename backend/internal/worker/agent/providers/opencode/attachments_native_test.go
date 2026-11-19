package opencode

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodePromptPreservesTextAndMedia(t *testing.T) {
	t.Parallel()
	model := openCodeCatalogFixture(t).findModel("provider/model")
	image := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	request, err := buildOpenCodePrompt("Read both attachments.", []*leapmuxv1.Attachment{
		nil,
		{Filename: "notes.md", MimeType: "text/markdown", Data: []byte("# Notes\nzero: 0\nflag: false")},
		{Filename: "image with spaces.png", MimeType: "image/png", Data: image},
	}, model)
	require.NoError(t, err)
	assert.Contains(t, request.Text, "Read both attachments.")
	assert.Contains(t, request.Text, "notes.md")
	assert.Contains(t, request.Text, "# Notes\nzero: 0\nflag: false")
	require.Len(t, request.Files, 1)
	assert.Equal(t, "image with spaces.png", request.Files[0].Name)
	assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image), request.Files[0].URI)
}

func TestOpenCodePromptHandlesEmptyInputsAndFilesWithoutText(t *testing.T) {
	t.Parallel()
	request, err := buildOpenCodePrompt("", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, request.Text)
	assert.Empty(t, request.Files)
	request, err = buildOpenCodePrompt("", []*leapmuxv1.Attachment{{Filename: "empty.txt", MimeType: "text/plain"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, request.Text, "empty.txt")
	assert.Empty(t, request.Files)
	model := openCodeCatalogFixture(t).findModel("provider/model")
	request, err = buildOpenCodePrompt("", []*leapmuxv1.Attachment{{Filename: "image.png", MimeType: "image/png", Data: []byte("media")}}, model)
	require.NoError(t, err)
	assert.Empty(t, request.Text)
	require.Len(t, request.Files, 1)
}

func TestOpenCodePromptPreservesUTF8WithAGenericMIMEType(t *testing.T) {
	t.Parallel()
	request, err := buildOpenCodePrompt("", []*leapmuxv1.Attachment{{Filename: "note.bin", MimeType: "application/octet-stream", Data: []byte("UTF-8 text: zero 0, false")}}, nil)
	require.NoError(t, err)
	assert.Contains(t, request.Text, "UTF-8 text: zero 0, false")
	assert.Empty(t, request.Files)
}

func TestOpenCodePromptRejectsUnsupportedOrUnverifiedMedia(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		attachment   *leapmuxv1.Attachment
		model        *openCodeModelInfo
	}{
		{"empty image", "is empty", &leapmuxv1.Attachment{Filename: "image.png", MimeType: "image/png"}, openCodeCatalogFixture(t).findModel("provider/model")},
		{"unknown model", "model is unknown", &leapmuxv1.Attachment{Filename: "image.png", MimeType: "image/png", Data: []byte("media")}, nil},
		{"unoffered modality", "does not accept image", &leapmuxv1.Attachment{Filename: "image.png", MimeType: "image/png", Data: []byte("media")}, openCodeCatalogFixture(t).findModel("other/model/special")},
		{"binary", "binary attachment", &leapmuxv1.Attachment{Filename: "data.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0xfe, 0xfd}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request, err := buildOpenCodePrompt("", []*leapmuxv1.Attachment{tc.attachment}, tc.model)
			require.ErrorContains(t, err, tc.reason)
			assert.Equal(t, openCodePrompt{}, request, "a rejected batch must expose no partially encoded input")
		})
	}
}

func TestOpenCodePromptChecksTheCombinedInputLimit(t *testing.T) {
	t.Parallel()
	_, err := buildOpenCodePrompt(strings.Repeat("x", contracts.MaxMessageSize+1), nil, nil)
	require.ErrorContains(t, err, "exceeds")
	request, err := buildOpenCodePrompt("x", []*leapmuxv1.Attachment{{Filename: "large.txt", MimeType: "text/plain", Data: make([]byte, contracts.MaxMessageSize)}}, nil)
	require.ErrorContains(t, err, "exceeds")
	assert.Equal(t, openCodePrompt{}, request)
}
