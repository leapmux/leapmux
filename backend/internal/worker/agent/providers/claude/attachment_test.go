//go:build unix

package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClaudeContentBlocks_textOnly(t *testing.T) {
	t.Parallel()

	blocks := buildClaudeContentBlocks("hello", nil)
	require.Len(t, blocks, 1)
	m := blocks[0].(map[string]interface{})
	assert.Equal(t, "text", m["type"])
	assert.Equal(t, "hello", m["text"])
}

func TestBuildClaudeContentBlocks_imageAttachment(t *testing.T) {
	t.Parallel()

	data := []byte{0x89, 0x50, 0x4e, 0x47}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "test.png", MimeType: "image/png", Data: data},
	}
	blocks := buildClaudeContentBlocks("look at this", agent.ClassifyAttachments(attachments))
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
	t.Parallel()

	data := []byte("%PDF-1.4")
	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.pdf", MimeType: "application/pdf", Data: data},
	}
	blocks := buildClaudeContentBlocks("", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 1) // no text block when content is empty

	docBlock := blocks[0].(map[string]interface{})
	assert.Equal(t, "document", docBlock["type"])
	source := docBlock["source"].(map[string]interface{})
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "application/pdf", source["media_type"])
}

func TestBuildClaudeContentBlocks_textAttachment(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "styles.css", MimeType: "", Data: []byte("body {}\n")},
	}
	blocks := buildClaudeContentBlocks("review", agent.ClassifyAttachments(attachments))
	require.Len(t, blocks, 2)

	textBlock := blocks[1].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Contains(t, textBlock["text"], "BEGIN ATTACHED FILE: styles.css")
	assert.Contains(t, textBlock["text"], "body {}")
}

func TestBuildClaudeContentBlocks_noAttachments(t *testing.T) {
	t.Parallel()

	blocks := buildClaudeContentBlocks("plain text", nil)
	require.Len(t, blocks, 1)
	m := blocks[0].(map[string]interface{})
	assert.Equal(t, "text", m["type"])
}

func TestClaudeCodeAgent_SendInput_withAttachments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sink := &agenttest.Sink{}

	agent, err := mockStart(ctx, agent.Options{
		AgentID:    "attach-test",
		Options:    map[string]string{agent.OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, agent.NewProviderServices(sink))
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
	t.Parallel()

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

func TestNormalizeAttachmentsForProvider_InfersTextMime(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "", Data: []byte("hello")},
	}
	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, 1)
	assert.Equal(t, "text/plain", normalized[0].GetMimeType())
}

func TestNormalizeAttachmentsForProvider_ClaudeRejectsBinaryAcceptsRest(t *testing.T) {
	t.Parallel()

	// Claude Code has no binary content block but accepts text, image, and PDF.
	_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude code does not support binary attachments")

	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	})
	require.NoError(t, err)
	require.Len(t, normalized, 3)
}
