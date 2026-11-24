package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestEnqueueAgentInput_OneCharMinimum(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// A single character should be accepted.
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
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
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Empty text with no attachments should be rejected.
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
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
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Empty text with attachments should be accepted.
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
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
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Create an attachment that exceeds 10 MB.
	bigData := make([]byte, 11*1024*1024) // 11 MB
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
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
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-5", true)
	require.NoError(t, err)

	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
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

// An attachment that states no media type takes the one that the Worker's
// classifier infers from its name and content. The queue's snapshot and the
// transcript row then state the type that the provider receives, as they do
// for the browser, which infers the type before it sends. A stated type stays
// as the client gave it. The agent runs Antigravity CLI, whose policy takes a
// binary file, so every attachment passes the policy.
func TestEnqueueAgentInput_InfersAnAbsentMediaType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-7",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ANTIGRAVITY_CLI,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-7", true)
	require.NoError(t, err)

	request := &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-7",
		Text:    "see these",
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "shot.png", Data: []byte{0x89, 'P', 'N', 'G'}},
			{Filename: "notes.md", Data: []byte("# Notes")},
			{Filename: "Dockerfile", MimeType: " ", Data: []byte("FROM scratch")},
			{Filename: "blob", Data: []byte{0xff, 0x00}},
			{Filename: "stated.png", MimeType: "text/plain", Data: []byte("text")},
			// A generic binary type states nothing, as an absent one does, and a
			// stated type reaches the provider in lower case.
			{Filename: "generic.md", MimeType: "application/octet-stream", Data: []byte("# Generic")},
			{Filename: "upper.png", MimeType: " IMAGE/PNG ", Data: []byte{0x89, 'P', 'N', 'G'}},
		},
	}
	// The second request repeats the first. The inference gives the same
	// types both times, so the Worker still takes the repeat as one input.
	for range 2 {
		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, w)
		require.Empty(t, w.errors)
	}

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-7")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1, "a repeated request is one input")
	var mimeTypes []string
	for _, attachment := range snapshot.Items[0].Metadata {
		mimeTypes = append(mimeTypes, attachment.MimeType)
	}
	assert.Equal(t, []string{"image/png", "text/markdown", "text/plain", "application/octet-stream", "text/plain", "text/markdown", "image/png"}, mimeTypes,
		"the snapshot states the type that the provider receives")
}

// The provider's attachment policy refuses a file that the provider cannot
// read when a client sends it, and the refusal states the file. A refusal at
// dispatch instead fails the input in the queue, and a failed first input
// holds every input behind it. The browser checks the provider's
// capabilities before it sends; the CLI has no provider code, so this check
// is the one that it meets.
func TestEnqueueAgentInput_RefusesAnAttachmentThatTheProviderCannotRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-8",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-8", true)
	require.NoError(t, err)
	binary := &leapmuxv1.Attachment{Filename: "build.tar", Data: []byte{0xff, 0x00, 0x01}}

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(), Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, AgentId: "agent-8", Text: "see this",
		Attachments: []*leapmuxv1.Attachment{{Filename: "notes.md", Data: []byte("# Notes")}, binary},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	assert.Contains(t, w.errors[0].message, "build.tar")
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-8")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items, "a refused input never joins the queue")

	// An edit that adds the file meets the same policy.
	inputID := newTestAgentInputID()
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		InputId: inputID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, AgentId: "agent-8", Text: "see this",
	}, w)
	require.Empty(t, w.errors)
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure, &leapmuxv1.BeginQueuedAgentInputEditRequest{
		AgentId: "agent-8", InputId: inputID, ClientId: "browser-1",
	}, w)
	require.Empty(t, w.errors)
	begin := decodeQueueResponse(t, w, &leapmuxv1.BeginQueuedAgentInputEditResponse{})
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure, &leapmuxv1.UpdateQueuedAgentInputRequest{
		AgentId: "agent-8", InputId: inputID, ClientId: "browser-1", ExpectedVersion: begin.GetSnapshot().GetItems()[0].GetVersion(),
		Text: "see this", Attachments: []*leapmuxv1.Attachment{binary},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	assert.Contains(t, w.errors[0].message, "build.tar")
	snapshot, err = svc.InputQueue.Snapshot(ctx, "agent-8")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Empty(t, snapshot.Items[0].Metadata, "a refused edit changes nothing")
}

func TestEnqueueAgentInput_TextOnlyHasNoAttachmentMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-6",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-6", true)
	require.NoError(t, err)

	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-6",
		Text:    "just text",
	}, w)
	require.Empty(t, w.errors)

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-6")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "just text", snapshot.Items[0].TextPreview)
	assert.Empty(t, snapshot.Items[0].Metadata)
}

// An edit takes the same inference as an enqueue: an attachment that states no
// media type takes the one that the classifier infers, and a file that the
// provider can read passes the provider's policy.
func TestUpdateQueuedAgentInput_InfersAnAbsentMediaType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d := setupInputQueueRPCService(t)
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	inputID := newTestAgentInputID()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		InputId: inputID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, AgentId: "agent-1", Text: "see this",
	}, w)
	require.Empty(t, w.errors)
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure, &leapmuxv1.BeginQueuedAgentInputEditRequest{
		AgentId: "agent-1", InputId: inputID, ClientId: "browser-1",
	}, w)
	begin := decodeQueueResponse(t, w, &leapmuxv1.BeginQueuedAgentInputEditResponse{})

	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure, &leapmuxv1.UpdateQueuedAgentInputRequest{
		AgentId: "agent-1", InputId: inputID, ClientId: "browser-1", ExpectedVersion: begin.GetSnapshot().GetItems()[0].GetVersion(),
		Text: "see these", Attachments: []*leapmuxv1.Attachment{
			{Filename: "notes.md", Data: []byte("# Notes")},
			{Filename: "stated.txt", MimeType: "text/csv", Data: []byte("a,b")},
		},
	}, w)
	require.Empty(t, w.errors)

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	var mimeTypes []string
	for _, attachment := range snapshot.Items[0].Metadata {
		mimeTypes = append(mimeTypes, attachment.MimeType)
	}
	assert.Equal(t, []string{"text/markdown", "text/csv"}, mimeTypes, "an absent type is inferred, and a stated type stays")
}

// The policy reads the agent's row to find its provider. A store fault there
// fails an input with attachments as INTERNAL, before the queue holds it: the
// Worker cannot tell whether the provider reads the files. An input without
// attachments needs no provider, so it reads no row and the fault does not
// stop it.
func TestEnqueueAgentInput_FailsWhenTheAttachmentPolicyCannotReadTheAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	option, faulted := faultAgentReads()
	svc, d, _ := setupTestService(t, option)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	enqueue := func(attachments []*leapmuxv1.Attachment) *testResponseWriter {
		t.Helper()
		w := newTestWriter()
		faulted.Store(true)
		dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
			InputId: newTestAgentInputID(), Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, AgentId: "agent-1",
			Text: "see this", Attachments: attachments,
		}, w)
		faulted.Store(false)
		return w
	}

	w := enqueue([]*leapmuxv1.Attachment{{Filename: "notes.md", Data: []byte("# Notes")}})
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Internal), w.errors[0].code)
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items, "an input whose files the Worker could not check never joins the queue")

	require.Empty(t, enqueue(nil).errors, "an input without attachments reads no agent row")
	snapshot, err = svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, snapshot.Items, 1)
}

// A missing agent passes the policy, so the queue refuses the request with its
// own error for the missing agent.
func TestRefuseUnreadableAttachments_PassesAMissingAgent(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	assert.NoError(t, svc.refuseUnreadableAttachments("no-such-agent", []*leapmuxv1.Attachment{{Filename: "build.tar", Data: []byte{0xff, 0x00}}}))
}
