package droid

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestPDFValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		size int
		err  string
	}{
		{name: "empty", err: "empty PDF"},
		{name: "nonempty", size: 8},
		{name: "native size limit", size: 3 * 1024 * 1024},
		{name: "above native size limit", size: 3*1024*1024 + 1, err: "3 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := (droidProvider{}).ValidateAttachment(agent.ClassifiedAttachment{
				Kind: agent.AttachmentKindPDF, Filename: "sample.pdf", MIMEType: "application/pdf", Data: bytes.Repeat([]byte{'x'}, tc.size),
			})
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				assert.Contains(t, err.Error(), "sample.pdf")
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.ErrorContains(t, (droidProvider{}).ValidateAttachment(agent.ClassifiedAttachment{
		Kind: agent.AttachmentKindBinary, Filename: "archive.bin", MIMEType: "application/octet-stream", Data: []byte{0, 255},
	}), "binary attachments")
	require.ErrorContains(t, (droidProvider{}).ValidateAttachment(agent.ClassifiedAttachment{
		Kind: agent.AttachmentKindImage, Filename: "empty.png", MIMEType: "image/png",
	}), "empty image")
}

func TestBuildUserMessageKeepsMixedPDFAttachments(t *testing.T) {
	t.Parallel()
	message, err := buildUserMessage("Review the files.", []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("notes")},
		{Filename: "image.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		{Filename: "sample.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4\n")},
	})
	require.NoError(t, err)
	assert.Contains(t, message.Text, "Review the files.")
	assert.Contains(t, message.Text, "notes")
	assert.NotContains(t, message.Text, "%PDF")
	assert.NotContains(t, message.Text, "sample.pdf")
	assert.Equal(t, []droidInputImage{{Type: "base64", MediaType: "image/png", Data: "iVBORw=="}}, message.Images)
}

func TestBuildUserMessageRejectsAnInvalidPDFWithoutPartialContent(t *testing.T) {
	t.Parallel()
	message, err := buildUserMessage("Keep this unsent.", []*leapmuxv1.Attachment{
		{Filename: "image.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		{Filename: "empty.pdf", MimeType: "application/pdf"},
	})
	require.ErrorContains(t, err, "empty PDF")
	assert.Empty(t, message.Text)
	assert.Empty(t, message.Images)
}
