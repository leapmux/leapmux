package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestEnqueueAgentInput_OneCharMinimum(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// A single character should be accepted.
	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-1",
		Text:    "x",
	}, w)
	require.Empty(t, w.errors, "single character message should be accepted")
}

func TestEnqueueAgentInput_EmptyTextRejectedWithoutAttachments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-2",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Empty text with no attachments should be rejected.
	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-2",
		Text:    "",
	}, w)
	require.NotEmpty(t, w.errors, "empty message with no attachments should be rejected")
	assert.Contains(t, w.errors[0].message, "text or an attachment is required")
}

func TestEnqueueAgentInput_EmptyTextAllowedWithAttachments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-3",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Empty text with attachments should be accepted.
	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-3",
		Text:    "",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "test.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		},
	}, w)
	require.Empty(t, w.errors, "empty text with attachments should be accepted")
}

func TestEnqueueAgentInput_AttachmentSizeLimitEnforced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-4",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Create an attachment that exceeds 10 MB.
	bigData := make([]byte, 11*1024*1024) // 11 MB
	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-4",
		Text:    "big file",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "big.bin", MimeType: "image/png", Data: bigData},
		},
	}, w)
	require.NotEmpty(t, w.errors, "oversized attachment should be rejected")
	assert.Contains(t, w.errors[0].message, "10 MiB")
}

func TestEnqueueAgentInput_AttachmentMetadataPersisted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-5",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-5", true)
	require.NoError(t, err)

	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-5",
		Text:    "check this",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "screenshot.png", MimeType: "image/png", Data: []byte{1, 2, 3}},
			{Filename: "report.pdf", MimeType: "application/pdf", Data: []byte{4, 5}},
		},
	}, w)
	require.Empty(t, w.errors, "message with attachments should succeed")

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-5")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	require.Len(t, snapshot.Items[0].Metadata, 2)
	assert.Equal(t, "screenshot.png", snapshot.Items[0].Metadata[0].Filename)
	assert.Equal(t, int64(3), snapshot.Items[0].Metadata[0].Size)
	assert.Equal(t, "report.pdf", snapshot.Items[0].Metadata[1].Filename)
	assert.Equal(t, int64(2), snapshot.Items[0].Metadata[1].Size)
}

func TestEnqueueAgentInput_TextOnlyHasNoAttachmentMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-6",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-6", true)
	require.NoError(t, err)

	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-6",
		Text:    "just text",
	}, w)
	require.Empty(t, w.errors)

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-6")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "just text", snapshot.Items[0].Text)
	assert.Empty(t, snapshot.Items[0].Metadata)
}
