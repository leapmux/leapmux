package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// A client retries an enqueue whose answer it never received with the same
// input id, as the browser's composer and `leapmux control agent send
// --request-id` do. The Worker takes the repeat as the same input: it answers
// with the queue that holds the one input, and it adds nothing. A repeat with
// other content is a conflict, so a reused id never replaces a message.
func TestEnqueueAgentInput_ARepeatedRequestIsOneInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher := setupInputQueueRPCService(t)
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	request := &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: "agent-1", InputId: "request-derived-id", Text: "Run the tests",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		ScheduledAt: "2099-01-02T03:04:05.000Z",
	}
	var revisions []uint64
	for range 3 {
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, writer)
		require.Empty(t, writer.errors)
		response := decodeQueueResponse(t, writer, &leapmuxv1.EnqueueAgentInputResponse{})
		require.Len(t, response.GetSnapshot().GetItems(), 1, "each answer holds the one input")
		assert.Equal(t, "request-derived-id", response.GetSnapshot().GetItems()[0].GetId())
		assert.Zero(t, response.GetAcceptedSeq(), "the input is still queued")
		revisions = append(revisions, response.GetSnapshot().GetRevision())
	}
	assert.Equal(t, revisions[0], revisions[1], "a repeat changes nothing")
	assert.Equal(t, revisions[0], revisions[2])

	for name, changed := range map[string]*leapmuxv1.EnqueueAgentInputRequest{
		"another text":     {AgentId: "agent-1", InputId: "request-derived-id", Text: "Run the linter", Kind: request.GetKind(), ScheduledAt: request.GetScheduledAt()},
		"another schedule": {AgentId: "agent-1", InputId: "request-derived-id", Text: request.GetText(), Kind: request.GetKind(), ScheduledAt: "2099-01-02T03:04:06.000Z"},
	} {
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, changed, writer)
		require.Len(t, writer.errors, 1, name)
		assert.Contains(t, writer.errors[0].message, "conflicts", name)
	}

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "Run the tests", snapshot.Items[0].TextPreview, "a conflicting repeat never replaces the input")
}

func TestEnqueueAgentInput_RequiresAFutureScheduleOnRequest(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	paused, err := svc.InputQueue.SetPaused(t.Context(), "agent-1", true)
	require.NoError(t, err)
	request := &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: "agent-1", InputId: "new-expired", Text: "Run the tests",
		Kind:                  leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		ScheduledAt:           "2001-01-02T03:04:05.000Z",
		RequireFutureSchedule: true,
	}
	writer := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, writer)
	require.Len(t, writer.errors, 1)
	assert.EqualValues(t, codes.InvalidArgument, writer.errors[0].code)
	assert.Contains(t, writer.errors[0].message, "a new schedule must be in the future")
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	assert.Equal(t, paused.Revision, snapshot.Revision)
}

func TestEnqueueAgentInput_RequiresARecordedRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, dispatcher := setupInputQueueRPCService(t)
	before, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	request := &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: "agent-1", InputId: "guarded-input", Text: "Run the tests",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, RequireExisting: true,
	}
	send := func(request *leapmuxv1.EnqueueAgentInputRequest) *leapmuxv1.EnqueueAgentInputResponse {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.EnqueueAgentInputResponse{})
	}

	missing := send(request)
	require.True(t, missing.GetExistingInputMissing())
	assert.Nil(t, missing.GetSnapshot())
	assert.Zero(t, missing.GetAcceptedSeq())
	afterMiss, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before.Revision, afterMiss.Revision)
	assert.Empty(t, afterMiss.Items)

	newInput := proto.CloneOf(request)
	newInput.RequireExisting = false
	added := send(newInput)
	require.Len(t, added.GetSnapshot().GetItems(), 1)
	retry := send(request)
	assert.False(t, retry.GetExistingInputMissing())
	assert.True(t, proto.Equal(added.GetSnapshot(), retry.GetSnapshot()))

	conflict := proto.CloneOf(request)
	conflict.Text = "Run the linter"
	writer := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, conflict, writer)
	require.Len(t, writer.errors, 1)
	assert.EqualValues(t, codes.FailedPrecondition, writer.errors[0].code)
	assert.Contains(t, writer.errors[0].message, "conflicts")

	_, err = svc.InputQueue.Delete(ctx, request.GetAgentId(), request.GetInputId())
	require.NoError(t, err)
	removed := send(request)
	assert.False(t, removed.GetExistingInputMissing())
	require.NotNil(t, removed.GetSnapshot())
	assert.Empty(t, removed.GetSnapshot().GetItems())
	assert.Zero(t, removed.GetAcceptedSeq())
}
