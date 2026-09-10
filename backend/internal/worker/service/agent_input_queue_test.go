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
	items := make([]inputqueue.SnapshotItem, inputqueue.MaxItems)
	for i := range items {
		items[i] = inputqueue.SnapshotItem{
			StoredItem: inputqueue.StoredItem{ID: fmt.Sprintf("input-%d", i), AgentID: "agent-1", Text: largeText},
			Metadata:   metadata,
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

// TestLiveStatusChangePublishesSteeringCapability covers the LIVE pushes. The
// test above covers the initial read.
//
// The frontend applies supports_steering from EVERY status change it receives.
// A status that the Worker builds without the field therefore reports false,
// and the Steer action disappears in the middle of a session for an agent that
// steers. buildStatusChange is the one builder every live ACTIVE push shares,
// so the capability hook that service.New wires must reach it.
func TestLiveStatusChangePublishesSteeringCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const steeringAgentID = "steering-agent"
	const coldAgentID = "cold-agent"
	workingDir := t.TempDir()
	for _, agentID := range []string{steeringAgentID, coldAgentID} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
	}

	// A running Claude Code process steers, and an agent with no process cannot
	// -- the same split the test above uses.
	steeringSink := svc.Output.NewSink(steeringAgentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: steeringAgentID, WorkingDir: workingDir,
	}, steeringSink)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(steeringAgentID) })
	coldSink := svc.Output.NewSink(coldAgentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Two channels, because one channel holds one watch set: a second
	// registration on the same channel replaces the first.
	steeringWatcher := newTestWriter()
	coldWatcher := newTestWriter()
	registerAgentWatch(svc, "browser-1", steeringAgentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, steeringWatcher)
	registerAgentWatch(svc, "browser-2", coldAgentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, coldWatcher)

	steeringSink.BroadcastStatusActive("session-steering")
	coldSink.BroadcastStatusActive("session-cold")

	steering := requireOneStatusChange(t, steeringWatcher, steeringAgentID)
	assert.True(t, steering.GetSupportsSteering(),
		"a live status for a steering agent turned the Steer action off")
	cold := requireOneStatusChange(t, coldWatcher, coldAgentID)
	assert.False(t, cold.GetSupportsSteering(),
		"a live status offered Steer for an agent that owns no process")
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

	result, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.DispatchItem{
		StoredItem: inputqueue.StoredItem{ID: "compact", AgentID: "agent-1", Text: "/compact",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT},
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
	_, err := svc.DB.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_steerable = 1`)
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
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
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
	assert.Equal(t, []string{contracts.NotificationTypeContextCleared}, decodeMessageTypes(t, messageToProto(&messages[1])))
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
			if messageType == contracts.NotificationTypeContextCleared {
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
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
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
	assert.Equal(t, []string{contracts.NotificationTypeAgentError}, decodeMessageTypes(t, messageToProto(&messages[0])))
}

func TestChildSteerReturnsOwnerDeliveryError(t *testing.T) {
	t.Parallel()

	svc, _, childID, _ := setupChildAgentTest(t)
	_, err := (&agentInputQueueAdapter{svc: svc}).Steer(inputqueue.DispatchItem{
		StoredItem: inputqueue.StoredItem{ID: "input-1", AgentID: childID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "guide"},
	})
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestChildQueuePublishesOnlyEffectiveSteeringCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const rootID = "zcode-root"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: rootID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}))
	sink := svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	childID, err := sink.EnsureChildAgent("zcode-span", "zcode-child", "read-only child")
	require.NoError(t, err)
	_, err = svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: rootID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, sink)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(rootID) })
	require.True(t, svc.Agents.SupportsSteering(rootID), "the mock root must expose regular steering")

	assert.False(t, (&agentInputQueueAdapter{svc: svc}).SupportsSteering(childID))
}

func TestClassifyQueueSteerErrorPreservesUncertainDelivery(t *testing.T) {
	t.Parallel()

	err := classifyQueueSteerError(fmt.Errorf("steer timed out: %w", agent.ErrDeliveryUncertain))
	assert.Equal(t, inputqueue.DispatchUncertain, dispatchOutcomeOf(t, err))
}

func TestClassifyQueueDeliveryErrorPreservesTheBusyTurnKind(t *testing.T) {
	t.Parallel()

	err := classifyQueueDeliveryError(&agent.AgentBusyError{
		Err:                 agent.ErrAgentBusy,
		ActiveTurnSteerable: true,
	})
	var deliveryErr *inputqueue.DeliveryError
	require.ErrorAs(t, err, &deliveryErr)
	assert.Equal(t, inputqueue.DispatchBusy, deliveryErr.Outcome)
	assert.True(t, deliveryErr.ActiveTurnSteerable)
}

// dispatchOutcomeOf reads the outcome a classified refusal carries. Every case
// goes through it rather than through errors.Is on a sentinel: the outcome is
// the queue's whole answer, so a test that matched only the CAUSE would pass
// while the queue did the wrong thing with it.
func dispatchOutcomeOf(t *testing.T, err error) inputqueue.DispatchOutcome {
	t.Helper()
	var deliveryErr *inputqueue.DeliveryError
	require.ErrorAs(t, err, &deliveryErr)
	return deliveryErr.Outcome
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

	require.NoError(t, svc.enqueueSyntheticUserInput(
		"agent-1", "Use a safer command", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK))

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, snapshot.Items[0].Kind)
}

func startGoalTextAgent(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	_, err := svc.Agents.MockStartAgent(t.Context(), agent.Options{
		AgentID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	require.NoError(t, svc.Agents.SendRawInput(agentID, []byte(
		"{\"type\":\"system\",\"subtype\":\"init\",\"slash_commands\":[\"goal\"]}\n")))
	require.Eventually(t, func() bool {
		return len(svc.Agents.SupportedGoalActions(agentID)) > 0
	}, time.Second, 5*time.Millisecond)
}

// A text-route goal command enters the durable input queue. The local goal row
// changes only after the queue delivers the command.
func TestUpdateAgentGoalQueuesTextUntilDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	startGoalTextAgent(t, svc, "agent-1")
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	writer := newTestWriter()
	dispatch(dispatcher, "UpdateAgentGoal", &leapmuxv1.UpdateAgentGoalRequest{
		AgentId:   "agent-1",
		Action:    leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET,
		Objective: "ship the release",
	}, writer)
	require.Empty(t, writer.rejections())
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "/goal ship the release", snapshot.Items[0].Text)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, snapshot.Items[0].Kind)
	before, err := svc.Output.LoadGoal(ctx, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, before.Goal)

	_, err = svc.InputQueue.SetPaused(ctx, "agent-1", false)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		loaded, loadErr := svc.Output.LoadGoal(ctx, "agent-1")
		return loadErr == nil && loaded.Goal.GetObjective() == "ship the release"
	}, time.Second, 5*time.Millisecond)
}

func TestUpdateAgentGoalReturnsTheQueueFullMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	startGoalTextAgent(t, svc, "agent-1")
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	for i := 0; i < inputqueue.MaxItems; i++ {
		_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
			ID: fmt.Sprintf("input-%d", i), AgentID: "agent-1", Text: "queued",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		})
		require.NoError(t, err)
	}

	writer := newTestWriter()
	dispatch(dispatcher, "UpdateAgentGoal", &leapmuxv1.UpdateAgentGoalRequest{
		AgentId: "agent-1", Action: leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR,
	}, writer)
	rejections := writer.rejections()
	require.Len(t, rejections, 1)
	assert.Equal(t, int32(codes.InvalidArgument), rejections[0].code)
	assert.Equal(t, inputqueue.ErrQueueFull.Error(), rejections[0].message)
}

func TestUpdateAgentGoalRefusesASubagentWithoutEnqueueing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child-1", ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID: "spawn-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	writer := newTestWriter()
	dispatch(dispatcher, "UpdateAgentGoal", &leapmuxv1.UpdateAgentGoalRequest{
		AgentId: "child-1", Action: leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET,
		Objective: "child objective",
	}, writer)
	rejections := writer.rejections()
	require.Len(t, rejections, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), rejections[0].code)
	snapshot, err := svc.InputQueue.Snapshot(ctx, "child-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
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

// The accepted input's live row must carry the same passthrough span column
// that the persisted row holds. Without it the bubble renders with no bars
// now, and grows them after a tab switch or a reload, because those paths read
// the database row instead of this broadcast.
func TestAcceptedTranscriptProtoCarriesTheSpanColumn(t *testing.T) {
	t.Parallel()

	message := acceptedTranscriptProto(inputqueue.AcceptedTranscript{
		ID: "input-1", AgentID: "agent-1", Seq: 7, SpanLines: `[{"color":3}]`,
		MarkType: leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE,
	})
	assert.Equal(t, `[{"color":3}]`, message.GetSpanLines())
	assert.Equal(t, int64(7), message.GetSeq())
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, message.GetSource())
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, message.GetMarkType())
}

// A snapshot the store did not build still cannot exceed the channel budget.
// The store truncates in SQL, so this cap never fires in production; it is the
// guard at the wire boundary, where an oversize message disconnects every
// watcher instead of showing a long message.
func TestQueueSnapshotProtoCapsAnUntruncatedItem(t *testing.T) {
	t.Parallel()

	snapshot := queueSnapshotProto(inputqueue.Snapshot{
		AgentID: "agent-1",
		Items: []inputqueue.SnapshotItem{{StoredItem: inputqueue.StoredItem{
			ID: "one", AgentID: "agent-1", Text: strings.Repeat("x", inputqueue.MaxItemBytes),
		}}},
	})
	require.Len(t, snapshot.GetItems(), 1)
	assert.LessOrEqual(t, len(snapshot.GetItems()[0].GetText()), inputqueue.SnapshotTextPreviewBytes+len("…"))
}

// A process that exits between the readiness test and the write never received
// the input. The manager resolves the provider before it writes, so
// ErrAgentNotFound always means "not delivered" -- the item returns to the
// queue instead of becoming a permanent failure the user must retry by hand.
func TestClassifyQueueDeliveryErrorRequeuesAVanishedProcess(t *testing.T) {
	t.Parallel()

	err := classifyQueueDeliveryError(fmt.Errorf("send: %w", agent.ErrAgentNotFound))
	assert.Equal(t, inputqueue.DispatchNotReady, dispatchOutcomeOf(t, err))
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestClassifyQueueDeliveryErrorKeepsTheQueueOpenForABusyAgent(t *testing.T) {
	t.Parallel()

	// A busy agent works, and its turn end releases the item. Pausing there
	// stopped a healthy queue on the outcome its own contract calls transient,
	// and only the user could start it again.
	err := classifyQueueDeliveryError(fmt.Errorf("send: %w", agent.ErrAgentBusy))
	assert.Equal(t, inputqueue.DispatchBusy, dispatchOutcomeOf(t, err),
		"a busy agent needs no state change, so the queue must not pause for one")
	assert.ErrorIs(t, err, agent.ErrAgentBusy, "and the cause still reaches the user")
}

// startEchoAgent registers a mock Claude Code process for agentID. The mock is
// a `cat`, so every frame the Worker writes to its stdin comes back as agent
// output: a control_request the Worker sends therefore lands in the
// control_requests table. That is how a test observes that a frame REACHED the
// process.
//
// `cat` returns no control_response, so a caller that waits for one waits the
// full APITimeout. The short timeout below caps that wait. It does not change
// what the process received.
func startEchoAgent(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err := svc.Agents.MockStartAgent(context.Background(), agent.Options{
		AgentID: agentID, WorkingDir: workingDir, APITimeout: 200 * time.Millisecond,
	}, sink)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
}

// requireInterruptReachedAgent waits until the agent process echoed an
// interrupt control_request back, which the sink stores.
func requireInterruptReachedAgent(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		rows, err := svc.Queries.ListControlRequestsByAgentID(context.Background(), agentID)
		if err != nil {
			return false
		}
		for i := range rows {
			if strings.Contains(string(rows[i].Payload), `"subtype":"interrupt"`) {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the interrupt never reached the agent process")
}

// stopInputQueue stops the queue manager, so every later queue write returns
// ErrManagerStopped -- the same refusal a database error produces on the pause
// that a stop path runs first.
func stopInputQueue(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	svc.InputQueue.Stop()()
	_, err := svc.InputQueue.Pause(context.Background(), agentID,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED)
	require.ErrorIs(t, err, inputqueue.ErrManagerStopped,
		"fixture check: the pause the handler runs must fail")
}

// TestInterruptAgentRunsWhenTheQueuePauseFails pins that a stop never depends
// on a bookkeeping write.
//
// InterruptAgent pauses the input queue before it interrupts, so the queue does
// not dispatch the next item into a turn the user just stopped. That pause is
// bookkeeping. A database error or a stopping queue refuses it, and the
// interrupt must still reach the agent: a queue that dispatches one more item
// is a condition the user can correct, and an agent that the user cannot stop
// is not.
//
// This test reads the CONTENT of the rejection list, not its length. The RPC
// waits for the agent's answer to the interrupt, and the `cat` mock sends no
// answer, so the call ends on the control timeout and reports the agent as not
// running. The refused pause must never be what the caller reads. The raw-frame
// test below covers the empty list, on the stop path that waits for no answer.
func TestInterruptAgentRunsWhenTheQueuePauseFails(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	startEchoAgent(t, svc, "agent-1")
	stopInputQueue(t, svc, "agent-1")

	writer := newTestWriter()
	dispatch(dispatcher, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"}, writer)

	requireInterruptReachedAgent(t, svc, "agent-1")
	for _, rejection := range writer.rejections() {
		assert.NotEqual(t, int32(codes.Internal), rejection.code,
			"the failed pause became the caller's answer, and the agent kept running")
		assert.NotContains(t, rejection.message, "pause agent input queue")
	}
}

// TestRawInterruptFrameRunsWhenTheQueuePauseFails is the same rule on the
// second stop path. SendAgentRawMessage forwards a provider-shaped frame, and
// it runs the same pause when that frame is an interrupt, so it must survive
// the refused pause too. Here the caller reads a plain success, because the
// forward waits for no answer of its own.
func TestRawInterruptFrameRunsWhenTheQueuePauseFails(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	startEchoAgent(t, svc, "agent-1")
	stopInputQueue(t, svc, "agent-1")

	writer := newTestWriter()
	dispatch(dispatcher, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"raw-interrupt-1","request":{"subtype":"interrupt"}}`,
	}, writer)

	assert.Empty(t, writer.rejections(), "a refused pause must not refuse the stop")
	assert.Len(t, writer.responses, 1)
	requireInterruptReachedAgent(t, svc, "agent-1")
	stored, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{
		AgentID: "agent-1", RequestID: "raw-interrupt-1",
	})
	require.NoError(t, err, "the agent received a frame with a different request id")
	assert.Contains(t, string(stored.Payload), `"subtype":"interrupt"`)
}

// TestDispatchRefusesAnAgentThatFailedToStartInMemory pins the FIRST of the two
// startup checks in the queue's dispatcher.
//
// persistAgentStartupError only logs when its write fails, so the registry can
// hold STARTUP_FAILED while the startup_error column stays empty. The persisted
// check alone would then dispatch into an agent that never came up: the
// dispatch starts a fresh process for a startup that already failed, and the
// user gets the same failure once per queued item.
func TestDispatchRefusesAnAgentThatFailedToStartInMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const failedAgentID = "failed-agent"
	const healthyAgentID = "healthy-agent"
	startEchoAgent(t, svc, failedAgentID)
	startEchoAgent(t, svc, healthyAgentID)

	handle := svc.AgentStartup.begin(failedAgentID, func() {})
	require.NotNil(t, handle)
	svc.AgentStartup.fail(handle, "claude: command not found")
	status, _, _, tracked := svc.AgentStartup.status(failedAgentID)
	require.True(t, tracked)
	require.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, failedAgentID)
	require.NoError(t, err)
	require.Empty(t, dbAgent.StartupError,
		"fixture check: only the in-memory check can refuse this dispatch")

	adapter := &agentInputQueueAdapter{svc: svc}
	result, err := adapter.Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: newTestAgentInputID(), AgentID: failedAgentID, Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent failed to start")
	assert.False(t, result.StartsTurn, "a refused dispatch must claim no turn")

	// The same fixture without the failed startup delivers, so the refusal
	// above comes from the registry and not from the process the test started.
	healthy, err := adapter.Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: newTestAgentInputID(), AgentID: healthyAgentID, Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	}})
	require.NoError(t, err)
	assert.True(t, healthy.StartsTurn)
}

// A one-word objective that equals a clear word would reach the CLI as a clear.
// The RPC refuses it, and queues nothing: a success here would close the dialog
// and then show an empty card, with no error anywhere to explain it.
func TestUpdateAgentGoalRefusesAnObjectiveThatClears(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	startGoalTextAgent(t, svc, "agent-1")

	writer := newTestWriter()
	dispatch(dispatcher, "UpdateAgentGoal", &leapmuxv1.UpdateAgentGoalRequest{
		AgentId:   "agent-1",
		Action:    leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET,
		Objective: "reset",
	}, writer)

	rejections := writer.rejections()
	require.Len(t, rejections, 1)
	assert.Equal(t, int32(codes.InvalidArgument), rejections[0].code)
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items, "a refused objective queues nothing")
}
