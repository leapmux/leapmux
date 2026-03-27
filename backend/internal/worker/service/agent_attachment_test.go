package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestSendAgentMessage_OneCharMinimum(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// A single character should be accepted.
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "x",
	}, w)
	require.Empty(t, w.errors, "single character message should be accepted")
}

func TestSendAgentMessage_EmptyTextRejectedWithoutAttachments(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-2",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// Empty text with no attachments should be rejected.
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-2",
		Content: "",
	}, w)
	require.NotEmpty(t, w.errors, "empty message with no attachments should be rejected")
	assert.Contains(t, w.errors[0].message, "at least 1 character")
}

func TestSendAgentMessage_EmptyTextAllowedWithAttachments(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-3",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// Empty text with attachments should be accepted.
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-3",
		Content: "",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "test.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		},
	}, w)
	require.Empty(t, w.errors, "empty text with attachments should be accepted")
}

func TestSendAgentMessage_AttachmentSizeLimitEnforced(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-4",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// Create an attachment that exceeds 10 MB.
	bigData := make([]byte, 11*1024*1024) // 11 MB
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-4",
		Content: "big file",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "big.bin", MimeType: "image/png", Data: bigData},
		},
	}, w)
	require.NotEmpty(t, w.errors, "oversized attachment should be rejected")
	assert.Contains(t, w.errors[0].message, "10 MB")
}

func TestSendAgentMessage_AttachmentMetadataPersistedInJSON(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-5",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-5",
		Content: "check this",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "screenshot.png", MimeType: "image/png", Data: []byte{1, 2, 3}},
			{Filename: "report.pdf", MimeType: "application/pdf", Data: []byte{4, 5}},
		},
	}, w)
	require.Empty(t, w.errors, "message with attachments should succeed")

	// Read the persisted message from the database.
	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{
		AgentID: "agent-5",
		Seq:     0,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	raw, err := msgcodec.Decompress(msgs[0].Content, msgs[0].ContentCompression)
	require.NoError(t, err)

	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &stored))

	// Verify "content" field.
	var content string
	require.NoError(t, json.Unmarshal(stored["content"], &content))
	assert.Equal(t, "check this", content)

	// Verify "attachments" field has metadata only (no "data" key).
	var attachments []map[string]string
	require.NoError(t, json.Unmarshal(stored["attachments"], &attachments))
	require.Len(t, attachments, 2)

	assert.Equal(t, "screenshot.png", attachments[0]["filename"])
	assert.Equal(t, "image/png", attachments[0]["mime_type"])
	_, hasData := attachments[0]["data"]
	assert.False(t, hasData, "binary data should NOT be stored in the database")

	assert.Equal(t, "report.pdf", attachments[1]["filename"])
	assert.Equal(t, "application/pdf", attachments[1]["mime_type"])
}

func TestSendAgentMessage_TextOnlyNoAttachmentsFieldInJSON(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-6",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-6",
		Content: "just text",
	}, w)
	require.Empty(t, w.errors)

	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{
		AgentID: "agent-6",
		Seq:     0,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	raw, err := msgcodec.Decompress(msgs[0].Content, msgs[0].ContentCompression)
	require.NoError(t, err)

	// For text-only messages, the JSON should use the simple {"content":"..."} format.
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &stored))

	var content string
	require.NoError(t, json.Unmarshal(stored["content"], &content))
	assert.Equal(t, "just text", content)

	// No "attachments" key should be present.
	_, hasAttachments := stored["attachments"]
	assert.False(t, hasAttachments, "text-only message should not have an attachments field")
}

// decodeResponse decodes a proto response payload.
func decodeResponse[T any, PT interface {
	*T
	proto.Message
}](t *testing.T, w *testResponseWriter) *T {
	t.Helper()
	require.NotEmpty(t, w.responses)
	resp := w.responses[len(w.responses)-1]
	out := PT(new(T))
	require.NoError(t, proto.Unmarshal(resp.GetPayload(), out))
	return (*T)(out)
}
