//go:build unix

package copilot

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAttachmentsForProvider_CopilotAcceptsTextAndImage(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
	}

	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, 2)
}

func TestNormalizeAttachmentsForProvider_CopilotRejectsBinary(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{{
		Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00},
	}}
	_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, attachments)
	require.ErrorContains(t, err, "binary")
}

func TestNormalizeAttachmentsForProvider_CopilotRejectsPDF(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{{
		Filename: "report.pdf", MimeType: "application/pdf", Data: []byte("%PDF"),
	}}
	_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, attachments)
	require.ErrorContains(t, err, "pdf")
}
