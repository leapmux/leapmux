//go:build unix

package acp

import (
	"encoding/base64"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildOpenCodePromptBlocks_fileAttachment(t *testing.T) {
	t.Parallel()

	data := []byte{0x89, 0x50}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "img.png", MimeType: "image/png", Data: data},
	}
	blocks := BuildPromptBlocks("analyze", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 2)

	textBlock := blocks[0]
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "analyze", textBlock["text"])

	imageBlock := blocks[1]
	assert.Equal(t, "image", imageBlock["type"])
	assert.Equal(t, "image/png", imageBlock["mimeType"])
	assert.Equal(t, "img.png", imageBlock["uri"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), imageBlock["data"])
}

func TestBuildOpenCodePromptBlocks_pdfIncluded(t *testing.T) {
	t.Parallel()

	data := []byte("%PDF-1.7")
	attachments := []*leapmuxv1.Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: data},
	}
	blocks := BuildPromptBlocks("", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 1)

	resourceBlock := blocks[0]
	assert.Equal(t, "resource", resourceBlock["type"])

	resource := resourceBlock["resource"].(map[string]interface{})
	assert.Equal(t, "application/pdf", resource["mimeType"])
	assert.Equal(t, "doc.pdf", resource["uri"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), resource["blob"])
}

func TestBuildOpenCodePromptBlocks_textAttachment(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "app.css", MimeType: "", Data: []byte("body {}\n")},
	}
	blocks := BuildPromptBlocks("", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 1)

	resourceBlock := blocks[0]
	assert.Equal(t, "resource", resourceBlock["type"])

	resource := resourceBlock["resource"].(map[string]interface{})
	assert.Equal(t, "text/css", resource["mimeType"])
	assert.Equal(t, "app.css", resource["uri"])
	assert.Equal(t, "body {}\n", resource["text"])
}
