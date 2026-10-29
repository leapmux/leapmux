//go:build unix

package reasonix

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAttachmentsForProvider_ReasonixAcceptsText(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "config.json", MimeType: "application/json", Data: []byte("{}")},
	}

	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, 2)
}

func TestNormalizeAttachmentsForProvider_ReasonixAcceptsNativeAttachments(t *testing.T) {
	t.Parallel()
	attachments := []*leapmuxv1.Attachment{
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the image and its notes")},
		{Filename: "document.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")},
		{Filename: "data.bin", MimeType: "application/octet-stream", Data: []byte{0, 0xff, 1}},
	}
	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, len(attachments))
	for i, attachment := range attachments {
		assert.Equal(t, attachment.Filename, normalized[i].Filename)
		assert.Equal(t, attachment.MimeType, normalized[i].MimeType)
		assert.Equal(t, attachment.Data, normalized[i].Data)
	}
}

func TestNormalizeAttachmentsForProvider_ReasonixAcceptsNoAttachments(t *testing.T) {
	t.Parallel()
	for _, attachments := range [][]*leapmuxv1.Attachment{nil, {}} {
		normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, attachments)
		require.NoError(t, err)
		assert.Empty(t, normalized)
	}
}
