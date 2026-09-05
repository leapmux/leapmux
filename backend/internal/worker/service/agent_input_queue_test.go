package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

func TestQueueSnapshotFitsDefaultWireBudgetWithLargeTextItems(t *testing.T) {
	t.Parallel()

	largeText := strings.Repeat("x", inputqueue.MaxItemBytes)
	metadata := make([]inputqueue.AttachmentMetadata, inputqueue.MaxAttachmentsPerItem)
	for i := range metadata {
		metadata[i] = inputqueue.AttachmentMetadata{
			Filename: strings.Repeat("f", inputqueue.MaxAttachmentFilenameBytes),
			MimeType: strings.Repeat("m", inputqueue.MaxAttachmentMIMETypeBytes),
		}
	}
	items := make([]inputqueue.Item, inputqueue.MaxItems)
	for i := range items {
		items[i] = inputqueue.Item{
			ID: fmt.Sprintf("input-%d", i), AgentID: "agent-1", Text: largeText, Metadata: metadata,
		}
	}
	snapshot := queueSnapshotProto(inputqueue.Snapshot{AgentID: "agent-1", Items: items})
	encoded, err := proto.Marshal(snapshot)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), contracts.MaxMessageSize)
}

func decodeQueueResponse[T proto.Message](t *testing.T, writer *testResponseWriter, target T) T {
	t.Helper()
	require.NotEmpty(t, writer.responses)
	require.NoError(t, proto.Unmarshal(writer.responses[len(writer.responses)-1].GetPayload(), target))
	return target
}

func TestAgentInputQueueRPCsReturnAuthoritativeSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	pauseWriter := newTestWriter()
	dispatch(dispatcher, "SetAgentInputQueuePaused", &leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: "agent-1", Paused: true}, pauseWriter)
	pause := decodeQueueResponse(t, pauseWriter, &leapmuxv1.SetAgentInputQueuePausedResponse{})
	require.NotNil(t, pause.GetSnapshot())
	assert.True(t, pause.GetSnapshot().GetPaused())

	enqueueWriter := newTestWriter()
	dispatch(dispatcher, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: "agent-1", InputId: "input-1", Text: "hello",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []*leapmuxv1.Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("body")}},
	}, enqueueWriter)
	enqueued := decodeQueueResponse(t, enqueueWriter, &leapmuxv1.EnqueueAgentInputResponse{})
	require.Len(t, enqueued.GetSnapshot().GetItems(), 1)
	assert.Equal(t, int64(4), enqueued.GetSnapshot().GetItems()[0].GetAttachments()[0].GetSize())

	listWriter := newTestWriter()
	dispatch(dispatcher, "ListAgentInputQueue", &leapmuxv1.ListAgentInputQueueRequest{AgentId: "agent-1"}, listWriter)
	listed := decodeQueueResponse(t, listWriter, &leapmuxv1.ListAgentInputQueueResponse{})
	assert.Equal(t, enqueued.GetSnapshot().GetRevision(), listed.GetSnapshot().GetRevision())
	assert.Equal(t, "input-1", listed.GetSnapshot().GetItems()[0].GetId())
}

func TestAgentInfoPublishesEffectiveSteeringCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	assert.True(t, svc.agentToProto(&dbAgent, true, nil).GetSupportsSteering())
	assert.True(t, svc.buildAgentActiveStatus(&dbAgent, nil).GetSupportsSteering())
	assert.False(t, svc.agentToProto(&dbAgent, false, nil).GetSupportsSteering())
}

func TestCompactOperationFallsBackToProviderInputWhenNativeCompactionIsUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })

	result, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.Item{
		ID: "compact", AgentID: "agent-1", Text: "/compact",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT,
	})
	require.NoError(t, err)
	assert.True(t, result.StartsTurn)
}

func TestAgentInputQueueEditReturnsDataAndReclassifiesHumanCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	enqueueWriter := newTestWriter()
	dispatch(dispatcher, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: "agent-1", InputId: "input-1", Text: "hello",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []*leapmuxv1.Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("body")}},
	}, enqueueWriter)

	beginWriter := newTestWriter()
	dispatch(dispatcher, "BeginQueuedAgentInputEdit", &leapmuxv1.BeginQueuedAgentInputEditRequest{
		AgentId: "agent-1", InputId: "input-1", ClientId: "browser-1",
	}, beginWriter)
	begin := decodeQueueResponse(t, beginWriter, &leapmuxv1.BeginQueuedAgentInputEditResponse{})
	require.Len(t, begin.GetAttachments(), 1)
	assert.Equal(t, []byte("body"), begin.GetAttachments()[0].GetData())
	assert.Equal(t, "hello", begin.GetText())

	updateWriter := newTestWriter()
	dispatch(dispatcher, "UpdateQueuedAgentInput", &leapmuxv1.UpdateQueuedAgentInputRequest{
		AgentId: "agent-1", InputId: "input-1", ClientId: "browser-1",
		ExpectedVersion: begin.GetSnapshot().GetItems()[0].GetVersion(), Text: " /summarize ",
	}, updateWriter)
	updated := decodeQueueResponse(t, updateWriter, &leapmuxv1.UpdateQueuedAgentInputResponse{})
	require.Len(t, updated.GetSnapshot().GetItems(), 1)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, updated.GetSnapshot().GetItems()[0].GetKind())
	assert.Empty(t, updated.GetSnapshot().GetItems()[0].GetEditOwnerClientId())
}

func TestAgentInputQueueRejectsConflictingEnqueueRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	for _, text := range []string{"first", "different"} {
		writer := newTestWriter()
		dispatch(dispatcher, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
			AgentId: "agent-1", InputId: "stable-id", Text: text,
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		}, writer)
		if text == "different" {
			require.Len(t, writer.errors, 1)
			assert.Contains(t, writer.errors[0].message, "conflicts")
		} else {
			require.Empty(t, writer.errors)
		}
	}
}

func TestAgentProcessExitPausesRootAndChildQueues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child-1", ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID: "spawn-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	for _, agentID := range []string{"root-1", "child-1"} {
		_, err := svc.InputQueue.SetPaused(ctx, agentID, false)
		require.NoError(t, err)
	}
	_, err := svc.DB.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_kind = ?`, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	require.NoError(t, err)

	svc.HandleAgentProcessExit("root-1", 1, assert.AnError, false)
	for _, agentID := range []string{"root-1", "child-1"} {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		require.NoError(t, err)
		assert.True(t, snapshot.Paused, agentID)
		assert.False(t, snapshot.ActiveTurn, agentID)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snapshot.PauseReason, agentID)
	}
}

func TestSendQueueErrorMapsSteeringStateToFailedPrecondition(t *testing.T) {
	t.Parallel()

	for _, queueErr := range []error{inputqueue.ErrTurnEnded, inputqueue.ErrSteeringState} {
		writer := newTestWriter()
		assert.True(t, sendQueueError(writer, queueErr))
		require.Len(t, writer.rejections(), 1)
		assert.Equal(t, int32(codes.FailedPrecondition), writer.rejections()[0].code)
	}
}

func TestAgentInputQueueBroadcastsSnapshotToTwoClients(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	first := newTestWriter()
	second := newTestWriter()
	registerAgentWatch(svc, "browser-1", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, first)
	registerAgentWatch(svc, "browser-2", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, second)
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "input-1", AgentID: "agent-1", Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)

	for _, writer := range []*testResponseWriter{first, second} {
		require.Eventually(t, func() bool {
			for _, stream := range writer.streamsSnapshot() {
				event := decodeWatchAgentEvent(t, stream)
				snapshot := event.GetInputQueueChanged().GetSnapshot()
				if len(snapshot.GetItems()) == 1 && snapshot.GetItems()[0].GetId() == "input-1" {
					return true
				}
			}
			return false
		}, time.Second, 10*time.Millisecond)
	}
}

func TestQueuedClearStartsColdAgentOnlyOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	var starts atomic.Int32
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
		starts.Add(1)
		return svc.Agents.MockStartAgent(ctx, opts, sink)
	}
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })

	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "clear-1", AgentID: "agent-1", Text: "/clear",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), starts.Load())
}

func TestQueuedClearCreatesBoundaryBeforeLaterInputRuns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, writer := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	svc.startAgentFn = svc.Agents.MockStartAgent
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	registerAgentWatch(svc, writer.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	for _, input := range []inputqueue.NewItem{
		{ID: "clear-1", AgentID: "agent-1", Text: "/clear", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT},
		{ID: "message-1", AgentID: "agent-1", Text: "after clear", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE},
	} {
		_, err := svc.InputQueue.Enqueue(ctx, input)
		require.NoError(t, err)
	}
	_, err = svc.InputQueue.SetPaused(ctx, "agent-1", false)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0 && snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)

	messages, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, messages, 3)
	assert.Equal(t, "clear-1", messages[0].ID)
	assert.Equal(t, []string{agent.NotificationTypeContextCleared}, decodeMessageTypes(t, messageToProto(&messages[1])))
	assert.Equal(t, "message-1", messages[2].ID)
	assert.Less(t, messages[0].Seq, messages[1].Seq)
	assert.Less(t, messages[1].Seq, messages[2].Seq)
	userIndex, boundaryIndex := -1, -1
	for index, stream := range writer.streamsSnapshot() {
		event := decodeWatchAgentEvent(t, stream)
		message := event.GetAgentMessage()
		if message == nil {
			continue
		}
		if message.GetId() == "clear-1" {
			userIndex = index
		}
		for _, messageType := range decodeMessageTypes(t, message) {
			if messageType == agent.NotificationTypeContextCleared {
				boundaryIndex = index
			}
		}
	}
	require.NotEqual(t, -1, userIndex)
	require.NotEqual(t, -1, boundaryIndex)
	assert.Less(t, userIndex, boundaryIndex)
}

func TestQueuedClearFailureKeepsInputOutOfTranscript(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		return nil, assert.AnError
	}
	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "clear-1", AgentID: "agent-1", Text: "/clear",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && snapshot.Paused && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
	}, time.Second, 10*time.Millisecond)
	messages, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []string{agent.NotificationTypeAgentError}, decodeMessageTypes(t, messageToProto(&messages[0])))
}

func TestChildSteerReturnsOwnerDeliveryError(t *testing.T) {
	t.Parallel()

	svc, _, childID, _ := setupChildAgentTest(t)
	_, err := (&agentInputQueueAdapter{svc: svc}).Steer(inputqueue.Item{
		ID: "input-1", AgentID: childID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "guide",
	})
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestClassifyQueueSteerErrorPreservesUncertainDelivery(t *testing.T) {
	t.Parallel()

	err := classifyQueueSteerError(fmt.Errorf("steer timed out: %w", agent.ErrDeliveryUncertain))
	var deliveryErr *inputqueue.DeliveryError
	require.ErrorAs(t, err, &deliveryErr)
	assert.True(t, deliveryErr.Uncertain)
}

func TestAutoContinueProducerUsesGeneratedQueueKind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	require.NotNil(t, svc.Output.sendMessageFunc)
	svc.Output.sendMessageFunc("agent-1", "Continue")

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_CONTINUE, snapshot.Items[0].Kind)
}

func TestControlFeedbackProducerUsesGeneratedQueueKind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	svc.enqueueSyntheticUserInput("agent-1", "Use a safer command", leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE)

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, snapshot.Items[0].Kind)
}

func TestPlanExecutionProducerPreservesItsQueueSemantics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	planPath := filepath.Join(t.TempDir(), "plan.md")
	require.NoError(t, os.WriteFile(planPath, []byte("# Safe plan\n"), 0o600))
	require.NoError(t, svc.Queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
		ID: "agent-1", PlanFilePath: planPath, PlanTitle: "Safe plan",
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	svc.initiatePlanExecution("agent-1", "acceptEdits")

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	item := snapshot.Items[0]
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, item.Kind)
	assert.Equal(t, "acceptEdits", item.TargetMode)
	assert.True(t, item.PrepareContext)
	assert.Contains(t, item.Text, "# Safe plan")
}
