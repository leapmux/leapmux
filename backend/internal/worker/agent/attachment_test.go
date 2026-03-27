package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClaudeContentBlocks_textOnly(t *testing.T) {
	blocks := buildClaudeContentBlocks("hello", nil)
	require.Len(t, blocks, 1)
	m := blocks[0].(map[string]interface{})
	assert.Equal(t, "text", m["type"])
	assert.Equal(t, "hello", m["text"])
}

func TestBuildClaudeContentBlocks_imageAttachment(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "test.png", MimeType: "image/png", Data: data},
	}
	blocks := buildClaudeContentBlocks("look at this", classifyAttachments(attachments))
	require.Len(t, blocks, 2)

	// First block: text
	textBlock := blocks[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "look at this", textBlock["text"])

	// Second block: image
	imgBlock := blocks[1].(map[string]interface{})
	assert.Equal(t, "image", imgBlock["type"])
	source := imgBlock["source"].(map[string]interface{})
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "image/png", source["media_type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), source["data"])
}

func TestBuildClaudeContentBlocks_pdfAttachment(t *testing.T) {
	data := []byte("%PDF-1.4")
	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.pdf", MimeType: "application/pdf", Data: data},
	}
	blocks := buildClaudeContentBlocks("", classifyAttachments(attachments))
	require.Len(t, blocks, 1) // no text block when content is empty

	docBlock := blocks[0].(map[string]interface{})
	assert.Equal(t, "document", docBlock["type"])
	source := docBlock["source"].(map[string]interface{})
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "application/pdf", source["media_type"])
}

func TestBuildClaudeContentBlocks_textAttachment(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "styles.css", MimeType: "", Data: []byte("body {}\n")},
	}
	blocks := buildClaudeContentBlocks("review", classifyAttachments(attachments))
	require.Len(t, blocks, 2)

	textBlock := blocks[1].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Contains(t, textBlock["text"], "BEGIN ATTACHED FILE: styles.css")
	assert.Contains(t, textBlock["text"], "body {}")
}

func TestBuildClaudeContentBlocks_noAttachments(t *testing.T) {
	blocks := buildClaudeContentBlocks("plain text", nil)
	require.Len(t, blocks, 1)
	m := blocks[0].(map[string]interface{})
	assert.Equal(t, "text", m["type"])
}

func TestBuildCodexInputBlocks_imageAttachment(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "photo.jpg", MimeType: "image/jpeg", Data: data},
	}
	blocks := buildCodexInputBlocks("describe this", classifyAttachments(attachments))
	require.Len(t, blocks, 2)

	textBlock := blocks[0]
	assert.Equal(t, "text", textBlock["type"])

	imgBlock := blocks[1]
	assert.Equal(t, "image", imgBlock["type"])
	expectedURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	assert.Equal(t, expectedURI, imgBlock["url"])
}

func TestBuildCodexInputBlocks_textAttachment(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.csv", MimeType: "", Data: []byte("name,value\nfoo,1\n")},
	}
	blocks := buildCodexInputBlocks("", classifyAttachments(attachments))
	require.Len(t, blocks, 1)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Contains(t, blocks[0]["text"], "BEGIN ATTACHED FILE: report.csv")
	assert.Contains(t, blocks[0]["text"], "name,value")
}

func TestBuildCodexInputBlocks_pdfSkipped(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	}
	blocks := buildCodexInputBlocks("", classifyAttachments(attachments))
	require.Empty(t, blocks)
}

func TestBuildOpenCodePromptBlocks_fileAttachment(t *testing.T) {
	data := []byte{0x89, 0x50}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "img.png", MimeType: "image/png", Data: data},
	}
	blocks := buildOpenCodePromptBlocks("analyze", classifyAttachments(attachments))
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
	data := []byte("%PDF-1.7")
	attachments := []*leapmuxv1.Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: data},
	}
	blocks := buildOpenCodePromptBlocks("", classifyAttachments(attachments))
	require.Len(t, blocks, 1)

	resourceBlock := blocks[0]
	assert.Equal(t, "resource", resourceBlock["type"])

	resource := resourceBlock["resource"].(map[string]interface{})
	assert.Equal(t, "application/pdf", resource["mimeType"])
	assert.Equal(t, "doc.pdf", resource["uri"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), resource["blob"])
}

func TestBuildOpenCodePromptBlocks_textAttachment(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "app.css", MimeType: "", Data: []byte("body {}\n")},
	}
	blocks := buildOpenCodePromptBlocks("", classifyAttachments(attachments))
	require.Len(t, blocks, 1)

	resourceBlock := blocks[0]
	assert.Equal(t, "resource", resourceBlock["type"])

	resource := resourceBlock["resource"].(map[string]interface{})
	assert.Equal(t, "text/css", resource["mimeType"])
	assert.Equal(t, "app.css", resource["uri"])
	assert.Equal(t, "body {}\n", resource["text"])
}

func TestNormalizeAttachmentsForProvider_RejectsUnsupportedBinary(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "", Data: []byte{0xff, 0xfe, 0xfd}},
	}
	_, err := NormalizeAttachmentsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, attachments)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex does not support binary attachments")
}

func TestNormalizeAttachmentsForProvider_InfersTextMime(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "", Data: []byte("hello")},
	}
	normalized, err := NormalizeAttachmentsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, 1)
	assert.Equal(t, "text/plain", normalized[0].GetMimeType())
}

func TestClaudeCodeAgent_SendInput_withAttachments(t *testing.T) {
	ctx := context.Background()
	sink := &testSink{}

	agent, err := mockStart(ctx, Options{
		AgentID:    "attach-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err, "mockStart")
	defer func() {
		agent.Stop()
		_ = agent.Wait()
	}()

	data := []byte{0x89, 0x50, 0x4e, 0x47}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "test.png", MimeType: "image/png", Data: data},
	}

	err = agent.SendInput("look at this image", attachments)
	require.NoError(t, err)

	// The mock process echoes stdin to stdout, so we can verify the format
	// by reading what flows through.
	// Wait for the echoed message to be processed.
	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected echoed message")

	// Verify the JSON structure of the sent message.
	msgs := sink.Messages()
	require.NotEmpty(t, msgs)

	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(msgs[0].Content, &envelope))
	assert.Equal(t, "user", envelope.Type)
	assert.Equal(t, "user", envelope.Message.Role)

	// Content should be an array (multimodal), not a string.
	var blocks []map[string]interface{}
	require.NoError(t, json.Unmarshal(envelope.Message.Content, &blocks))
	require.Len(t, blocks, 2) // text + image

	assert.Equal(t, "text", blocks[0]["type"])
	assert.Equal(t, "look at this image", blocks[0]["text"])
	assert.Equal(t, "image", blocks[1]["type"])
}

func TestClaudeCodeAgent_SendInput_withoutAttachments_producesStringContent(t *testing.T) {
	// When no attachments are provided, SendInput produces a plain string
	// content (backward compatible), not a content block array.
	// We verify this by directly marshaling a UserInputMessage.
	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role:    "user",
			Content: "plain text",
		},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var parsed struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))

	// Content should be a plain string, not an array.
	var content string
	require.NoError(t, json.Unmarshal(parsed.Message.Content, &content))
	assert.Equal(t, "plain text", content)
}
