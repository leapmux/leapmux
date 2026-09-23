//go:build unix

package codex

import (
	"encoding/base64"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCodexInputBlocks_imageAttachment(t *testing.T) {
	t.Parallel()

	data := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "photo.jpg", MimeType: "image/jpeg", Data: data},
	}
	blocks := buildCodexInputBlocks("describe this", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 2)

	textBlock := blocks[0]
	assert.Equal(t, "text", textBlock["type"])

	imgBlock := blocks[1]
	assert.Equal(t, "image", imgBlock["type"])
	expectedURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	assert.Equal(t, expectedURI, imgBlock["url"])
}

func TestBuildCodexInputBlocks_textAttachment(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.csv", MimeType: "", Data: []byte("name,value\nfoo,1\n")},
	}
	blocks := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 1)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Contains(t, blocks[0]["text"], "BEGIN ATTACHED FILE: report.csv")
	assert.Contains(t, blocks[0]["text"], "name,value")
}

func TestBuildCodexInputBlocks_pdfSkipped(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	}
	blocks := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments))
	require.Empty(t, blocks)
}

func TestNormalizeAttachmentsForProvider_RejectsUnsupportedBinary(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "", Data: []byte{0xff, 0xfe, 0xfd}},
	}
	_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, attachments)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex does not support binary attachments")
}

func TestNormalizeAttachmentsForProvider_CodexRejectsPDFAndBinary(t *testing.T) {
	t.Parallel()

	// Codex and Pi share providerkit.RejectPDFAndBinaryAttachment; exercise BOTH branches for Codex so the
	// shared helper's PDF and binary paths are each covered under the Codex label.
	cases := map[string]struct {
		att  *leapmuxv1.Attachment
		want string
	}{
		"pdf":    {&leapmuxv1.Attachment{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}, "codex does not support PDF attachments"},
		"binary": {&leapmuxv1.Attachment{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}, "codex does not support binary attachments"},
	}
	for kind, tc := range cases {
		t.Run(kind, func(t *testing.T) {
			_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, []*leapmuxv1.Attachment{tc.att})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
