package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// inputQueueWait limits each wait for work that the input queue does on a
// goroutine of its own: a dispatch, a cold start, a broadcast. A wait that
// succeeds returns at once, so the limit is generous for a loaded machine: a
// short limit fails a correct test when the machine is busy.
const inputQueueWait = 30 * time.Second

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
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	assert.True(t, svc.agentToProto(&dbAgent, true, nil).GetSupportsSteering())
	assert.True(t, svc.buildAgentActiveStatus(&dbAgent, nil).GetSupportsSteering())
	assert.False(t, svc.agentToProto(&dbAgent, false, nil).GetSupportsSteering())
}

// TestAgentInfoPublishesEffectivePreemptionCapability mirrors the steering
// capability test for the interrupt-only providers: a running process that
// cannot steer gets Preempt, a steerable one keeps Steer, and an agent with no
// process gets neither.
func TestAgentInfoPublishesEffectivePreemptionCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	for _, agentID := range []string{"steerable", "interrupt-only", "cold"} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		}))
	}
	for _, agentID := range []string{"steerable", "interrupt-only"} {
		var err error
		if agentID == "steerable" {
			_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
				AgentID: agentID, WorkingDir: workingDir,
			}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), claudetest.StartEcho)
		} else {
			_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
				AgentID: agentID, WorkingDir: workingDir,
			}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), agenttest.NonSteerable(claudetest.StartSilent))
		}
		require.NoError(t, err)
		t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	}

	steerable, err := svc.Queries.GetAgentByID(ctx, "steerable")
	require.NoError(t, err)
	assert.False(t, svc.agentToProto(&steerable, true, nil).GetSupportsPreemption(),
		"a provider that steers must not also offer Preempt")
	assert.True(t, svc.agentToProto(&steerable, true, nil).GetSupportsSteering())

	interruptOnly, err := svc.Queries.GetAgentByID(ctx, "interrupt-only")
	require.NoError(t, err)
	assert.True(t, svc.agentToProto(&interruptOnly, true, nil).GetSupportsPreemption())
	assert.True(t, svc.buildAgentActiveStatus(&interruptOnly, nil).GetSupportsPreemption())
	assert.False(t, svc.agentToProto(&interruptOnly, true, nil).GetSupportsSteering())

	cold, err := svc.Queries.GetAgentByID(ctx, "cold")
	require.NoError(t, err)
	assert.False(t, svc.agentToProto(&cold, true, nil).GetSupportsPreemption(),
		"an agent that owns no process has nothing to interrupt with")
	assert.False(t, svc.buildAgentActiveStatus(&cold, nil).GetSupportsPreemption())
}

// TestLiveStatusChangePublishesPreemptionCapability covers the LIVE pushes for
// the same reason the steering one does: the frontend applies
// supports_preemption from every status change, so a status built without the
// field turns the Preempt action off mid-session for an agent that has it.
func TestLiveStatusChangePublishesPreemptionCapability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	const preemptAgentID = "preempt-agent"
	const coldAgentID = "cold-agent"
	for _, agentID := range []string{preemptAgentID, coldAgentID} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		}))
	}
	preemptSink := svc.Output.NewSink(preemptAgentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: preemptAgentID, WorkingDir: workingDir,
	}, preemptSink, agenttest.NonSteerable(claudetest.StartSilent))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(preemptAgentID) })
	coldSink := svc.Output.NewSink(coldAgentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)

	preemptWatcher := newTestWriter()
	coldWatcher := newTestWriter()
	registerAgentWatch(svc, "browser-1", preemptAgentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, preemptWatcher)
	registerAgentWatch(svc, "browser-2", coldAgentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, coldWatcher)

	preemptSink.BroadcastStatusActive("session-preempt")
	coldSink.BroadcastStatusActive("session-cold")

	preempt := requireOneStatusChange(t, preemptWatcher, preemptAgentID)
	assert.True(t, preempt.GetSupportsPreemption(),
		"a live status turned the Preempt action off for an interrupt-only agent")
	assert.False(t, preempt.GetSupportsSteering())
	cold := requireOneStatusChange(t, coldWatcher, coldAgentID)
	assert.False(t, cold.GetSupportsPreemption(),
		"a live status offered Preempt for an agent that owns no process")
}

func TestPreemptQueuedAgentInputRPC(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), agenttest.NonSteerable(claudetest.StartSilent))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })

	// The mock provider reports no turn end of its own, so the head stays in place
	// after the interrupt and the RPC's answer is deterministic. The queue must NOT be
	// paused for that: a pause is precisely the state preemption has to refuse, and
	// using one here pinned the defect instead of the behaviour.
	_, err = svc.InputQueue.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)

	writer := newTestWriter()
	dispatch(dispatcher, "PreemptQueuedAgentInput", &leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: "agent-1", InputId: "one"}, writer)
	response := decodeQueueResponse(t, writer, &leapmuxv1.PreemptQueuedAgentInputResponse{})
	require.NotNil(t, response.GetSnapshot())
	require.Len(t, response.GetSnapshot().GetItems(), 1)
	assert.Equal(t, "one", response.GetSnapshot().GetItems()[0].GetId())
	// The item stays queued: the cancelled turn's end is what dispatches it.
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, response.GetSnapshot().GetItems()[0].GetState())
}

// Preemption cancels the running turn and then waits for the ORDINARY turn-end drain
// to deliver. A paused queue refuses that drain, so the cancel destroys the turn and
// delivers nothing: the reader loses the turn AND the message, and sees no error.
func TestPreemptQueuedAgentInputRPCRefusesAPausedQueue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), agenttest.NonSteerable(claudetest.StartSilent))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })

	_, err = svc.InputQueue.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	// SetPaused leaves active_turn set, so this is a manual pause during a running
	// turn -- the state the offer used to answer true for.
	_, err = svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.False(t, snapshot.Items[0].CanPreempt, "a paused queue offers no Preempt")

	writer := newTestWriter()
	dispatch(dispatcher, "PreemptQueuedAgentInput", &leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: "agent-1", InputId: "one"}, writer)
	require.Len(t, writer.errors, 1, "the RPC refuses what the snapshot did not offer")
	assert.Equal(t, int32(codes.FailedPrecondition), writer.errors[0].code)
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
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: steeringAgentID, WorkingDir: workingDir,
	}, steeringSink, claudetest.StartEcho)
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
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", WorkingDir: workingDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
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
		}, inputQueueWait, 10*time.Millisecond)
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
		return svc.Agents.StartAgentWith(ctx, opts, sink, claudetest.StartEcho)
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
	}, inputQueueWait, 10*time.Millisecond)
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
	svc.startAgentFn = startWith(svc.Agents, claudetest.StartEcho)
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
	}, inputQueueWait, 10*time.Millisecond)

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
	}, inputQueueWait, 10*time.Millisecond)
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
	_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: rootID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, sink, claudetest.StartEcho)
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
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{
		AgentID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	require.NoError(t, svc.Agents.SendRawInput(agentID, []byte(
		"{\"type\":\"system\",\"subtype\":\"init\",\"slash_commands\":[\"goal\"]}\n")))
	require.Eventually(t, func() bool {
		return len(svc.Agents.SupportedGoalActions(agentID)) > 0
	}, inputQueueWait, 5*time.Millisecond)
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
	}, inputQueueWait, 5*time.Millisecond)
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

	require.NoError(t, svc.enqueuePlanExecution("agent-1", "acceptEdits", "plan-execution"))

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
	startMockAgent(t, svc, agentID, startWith(svc.Agents, claudetest.StartEcho))
}

// startRecordingEchoAgent is startEchoAgent whose echo is observed through a
// recorder on the sink rather than through the control_requests table.
//
// The raw stop path withdraws the stopped turn's pending control requests the
// moment its interrupt is delivered, and the withdrawal is a DELETE by agent id.
// An echo landing in the window between that delivery and the withdrawal is
// deleted with the rest, so a test that reads the table races the very stop it
// sent and loses on the wrong scheduling. The recorder sees the request the
// reader goroutine published BEFORE any withdrawal can reach it, which is the
// same fact -- the process echoed the frame -- stated without the race.
func startRecordingEchoAgent(t *testing.T, svc *Service, agentID string) *controlRequestRecorder {
	t.Helper()
	recorder := &controlRequestRecorder{}
	startMockAgentWrappingSink(t, svc, agentID, startWith(svc.Agents, claudetest.StartEcho), func(sink agent.ProviderServices) agent.ProviderServices {
		recorder.ProviderServices = sink
		return recorder
	})
	return recorder
}

// controlRequestRecorder wraps an agent's sink and records every control request
// its process published. Embedding the interface keeps every other facet
// forwarding to the wrapped sink.
type controlRequestRecorder struct {
	agent.ProviderServices

	mu        sync.Mutex
	published []agent.ControlRequest
}

// PublishControlRequest records the request, then forwards it. Recording first
// is the point: the underlying store write is what a stop's withdrawal deletes,
// and the recorder must not depend on it having survived.
func (r *controlRequestRecorder) PublishControlRequest(request agent.ControlRequest) error {
	r.mu.Lock()
	r.published = append(r.published, request)
	r.mu.Unlock()
	return r.ProviderServices.PublishControlRequest(request)
}

// requests returns the control requests published so far.
func (r *controlRequestRecorder) requests() []agent.ControlRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.published)
}

// startSilentAgent is startEchoAgent with a mock that writes nothing back. A
// case that reads the derived activity state needs it; see claudetest.StartSilent
// for the artifact the echo leaves behind.
func startSilentAgent(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	startMockAgent(t, svc, agentID, startWith(svc.Agents, claudetest.StartSilent))
}

func startMockAgent(
	t *testing.T,
	svc *Service,
	agentID string,
	start func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error),
) {
	t.Helper()
	startMockAgentWrappingSink(t, svc, agentID, start, nil)
}

// startMockAgentWrappingSink is startMockAgent with the sink passed through
// wrap first, so a test can interpose an observer on one facet.
func startMockAgentWrappingSink(
	t *testing.T,
	svc *Service,
	agentID string,
	start func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error),
	wrap func(agent.ProviderServices) agent.ProviderServices,
) {
	t.Helper()
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	if wrap != nil {
		sink = wrap(sink)
	}
	_, err := start(context.Background(), agent.Options{
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
	}, inputQueueWait, 10*time.Millisecond, "the interrupt never reached the agent process")
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

// TestInterruptAgentEscalatesWhenTheProviderProvesItsStopIgnored pins the
// second-press escalation.
//
// The branch is reached through the Manager's capability probe alone -- the
// running provider states that an earlier stop was accepted and changed
// nothing -- so the handler carries no provider enum. What the press then does
// is replace the process (the only stop a provider in that state answers to)
// while resuming the SAME session, and set no stop-pause of its own: the
// planned-restart guard owns the swap's window, and the queue must stand open
// behind it for the input that follows.
func TestInterruptAgentEscalatesWhenTheProviderProvesItsStopIgnored(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	const agentID = "agent-escalate"
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, Resumed: 1,
	}))
	// A stored session the row was RESUMED from is what the replacement must
	// resume in turn -- the fact that keeps the conversation across a forced
	// stop instead of resetting it.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: agentID, AgentSessionID: "sess-stored",
	}))
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: agentID, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		WorkingDir: workingDir, APITimeout: 200 * time.Millisecond,
	}, sink, agenttest.IgnoredStop(claudetest.StartSilent))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })

	var restartOpts []agent.Options
	svc.startAgentFn = mockAgentStarter(t, svc, func(opts agent.Options) { restartOpts = append(restartOpts, opts) })

	writer := newTestWriter()
	dispatch(dispatcher, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: agentID}, writer)

	require.Empty(t, writer.rejections(), "the escalated press is a successful stop")
	require.Len(t, restartOpts, 1, "the press replaced the process once")
	assert.Equal(t, "sess-stored", restartOpts[0].ResumeSessionID,
		"the replacement resumes the session the row holds")
	assert.True(t, svc.Agents.HasAgent(agentID), "a completed escalation leaves a running agent")

	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, snapshot.Paused,
		"the escalation sets no stop-pause of its own; the queue stands open for the input that follows")
}

// TestRawInterruptFramePublishesTheStop pins the half of a stop the reader
// actually sees.
//
// Every provider answers a stop with a round trip, and its turn flag reads
// WORKING until that answer lands. So the press itself has to drop the
// indicator; waiting for the provider left the spinner and the Interrupt button
// on screen for the whole round trip, which invites a second press.
//
// The raw frame is the stop path that waits for no answer, so the delivery here
// SUCCEEDS and the published stop stands. The InterruptAgent RPC reaches the
// same two calls in the same order; what it adds is the wait, and the case below
// covers the refusal that wait can end in.
func TestRawInterruptFramePublishesTheStop(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	startSilentAgent(t, svc, "agent-1")
	svc.Output.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State)

	writer := newTestWriter()
	dispatch(dispatcher, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"raw-interrupt-1","request":{"subtype":"interrupt"}}`,
	}, writer)

	require.Empty(t, writer.rejections())
	// The provider reported no turn end -- the mock reports nothing at all -- so
	// the turn flag still says WORKING. The stop is what the tab reads.
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State,
		"the press, not the provider's answer, is what drops the indicator")
}

// The other half. A stop the worker could not deliver leaves the agent running
// whatever it was running, so the indicator and the Interrupt button belong back
// on screen -- hiding the button on a runaway agent is the one failure it exists
// to prevent.
//
// The mock answers no interrupt, so this call ends on the control timeout, which
// is the refusal the caller reads as "not running".
func TestInterruptAgentPutsTheIndicatorBackWhenTheStopIsRefused(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	startSilentAgent(t, svc, "agent-1")
	svc.Output.setTurnActive("agent-1", "agent-1", true)

	writer := newTestWriter()
	dispatch(dispatcher, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"}, writer)

	require.NotEmpty(t, writer.rejections(), "the control timeout is a refused stop")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("agent-1", "agent-1").State,
		"the turn never stopped, so the tab must not read idle")
}

// TestInterruptAgentCancelsTheRequestsTheTurnWasBlockedOn pins what a stop does to a
// question nobody answered.
//
// A permission request is part of the turn that asked it. Four providers were measured
// blocked on one, and an interrupt there stopped the turn and left the card asking: the
// reader kept a live-looking question about a turn that had already ended, and the
// answer had nowhere to go.
func TestInterruptAgentCancelsTheRequestsTheTurnWasBlockedOn(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	startEchoAgent(t, svc, "agent-1")
	_, err := svc.Queries.StoreControlRequest(context.Background(), db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "request-1",
		Payload:   []byte(`{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool"}}`),
	})
	require.NoError(t, err)

	// The raw frame is the stop path that waits for no answer, so the stop it sends is
	// DELIVERED rather than merely attempted. That is the bar the cancel runs behind.
	writer := newTestWriter()
	dispatch(dispatcher, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"raw-interrupt-1","request":{"subtype":"interrupt"}}`,
	}, writer)
	assert.Empty(t, writer.rejections())

	_, err = svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{
		AgentID: "agent-1", RequestID: "request-1",
	})
	assert.Error(t, err, "the question belonged to the turn the stop ended")
}

// TestRawInterruptFrameRunsWhenTheQueuePauseFails is the same rule on the
// second stop path. SendAgentRawMessage forwards a provider-shaped frame, and
// it runs the same pause when that frame is an interrupt, so it must survive
// the refused pause too. Here the caller reads a plain success, because the
// forward waits for no answer of its own.
//
// The echo is read through the recorder rather than the control_requests table
// (see startRecordingEchoAgent): this stop path delivers and then withdraws the
// turn's pending requests, and a table read races that withdrawal -- the same
// scheduling lottery on every run, lost often enough to fail CI.
func TestRawInterruptFrameRunsWhenTheQueuePauseFails(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	recorder := startRecordingEchoAgent(t, svc, "agent-1")
	stopInputQueue(t, svc, "agent-1")

	writer := newTestWriter()
	dispatch(dispatcher, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"raw-interrupt-1","request":{"subtype":"interrupt"}}`,
	}, writer)

	assert.Empty(t, writer.rejections(), "a refused pause must not refuse the stop")
	assert.Len(t, writer.responses, 1)
	var echoed []agent.ControlRequest
	require.Eventually(t, func() bool {
		echoed = recorder.requests()
		return len(echoed) > 0
	}, inputQueueWait, 10*time.Millisecond, "the interrupt never reached the agent process")
	require.Len(t, echoed, 1)
	assert.Equal(t, "raw-interrupt-1", echoed[0].RequestID,
		"the agent received a frame with a different request id")
	assert.Contains(t, string(echoed[0].Payload), `"subtype":"interrupt"`)
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

// Only Claude Code and Codex write a plan file. ZCode and native Copilot raise
// the same plan-exit control with none, and the approval already moved the
// permission mode before this runs -- so a hard failure here would leave the
// mode moved with no turn to use it, and every retry would repeat that move.
func TestPlanExecutionWithoutASavedPlanQueuesTheNeutralTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	require.NoError(t, svc.enqueuePlanExecution("agent-1", "acceptEdits", "plan-execution"))

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	item := snapshot.Items[0]
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, item.Kind)
	assert.Equal(t, "acceptEdits", item.TargetMode)
	assert.True(t, item.PrepareContext, "the approved plan still runs in a fresh context")
	assert.Equal(t, planExecutionPromptText, item.Text)
}

// A path that EXISTS and cannot be read is the opposite case: the plan text is
// there, the read is the fault, and a retry can still succeed. Queueing the
// neutral turn there would drop a plan the user can still see on screen.
func TestPlanExecutionFailsWhenTheSavedPlanCannotBeRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
		ID: "agent-1", PlanFilePath: filepath.Join(t.TempDir(), "absent", "plan.md"), PlanTitle: "Plan",
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	require.ErrorContains(t, svc.enqueuePlanExecution("agent-1", "acceptEdits", "plan-execution"), "read the saved plan")

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
}

// An approval's answer row reaches COMPLETED only after processControlResponse
// records the delivery, and enqueuePlanExecution inserts the input BEFORE that.
// So the queue can drain the input while its answer is still PENDING. Marking it
// FAILED there makes the user retry by hand what the NotifyDependencyReady of
// finalizeControlResponse resumes on its own moments later.
func TestControlInputWaitsForAnApprovalThatIsStillPending(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	startEchoAgent(t, svc, "agent-1")
	_, err := svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers
		(agent_id, request_id, claim_token, state, agent_session_id, input_id, feedback, agent_provider)
		VALUES ('agent-1', 'request', 'claim', ?, 'session-1', 'feedback-input', 'feedback', ?)`,
		int64(storedStatePending), int64(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: "feedback-input", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "feedback",
	}}
	adapter := &agentInputQueueAdapter{svc: svc}
	for _, tc := range []struct {
		name     string
		dispatch func(inputqueue.DispatchItem) (inputqueue.DispatchResult, error)
	}{
		{"Dispatch", adapter.Dispatch},
		{"Steer", adapter.Steer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.dispatch(item)
			var refusal *inputqueue.DeliveryError
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, inputqueue.DispatchNotReady, refusal.Outcome,
				"the queue must requeue and pause rather than fail the item")
			assert.ErrorContains(t, err, "approval is not recorded yet")
		})
	}
}

// classifyQueueDeliveryError must not re-wrap a refusal that already carries its
// outcome: queueDispatchOutcome reads the CAUSE, and answers DispatchFailed for
// every cause it does not recognize.
func TestClassifyQueueDeliveryErrorKeepsAStatedOutcome(t *testing.T) {
	t.Parallel()
	stated := &inputqueue.DeliveryError{Err: errors.New("still starting"), Outcome: inputqueue.DispatchNotReady}
	for _, err := range []error{stated, fmt.Errorf("dispatch: %w", stated)} {
		classified := classifyQueueDeliveryError(err)
		var refusal *inputqueue.DeliveryError
		require.ErrorAs(t, classified, &refusal)
		assert.Equal(t, inputqueue.DispatchNotReady, refusal.Outcome)
	}
	var fresh *inputqueue.DeliveryError
	require.ErrorAs(t, classifyQueueDeliveryError(agent.ErrAgentBusy), &fresh)
	assert.Equal(t, inputqueue.DispatchBusy, fresh.Outcome)
}

// A read of the worker's OWN store that fails never reached the provider, so the
// input is as undelivered as it was before. Failing it permanently makes the
// reader retype what a retry would have sent.
func TestQueueReadErrorClassifiesAStoreFaultAsNotReady(t *testing.T) {
	t.Parallel()

	err := queueReadError(errors.New("database is closed"))
	var delivery *inputqueue.DeliveryError
	require.ErrorAs(t, err, &delivery)
	assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT, delivery.PauseReason,
		"AGENT_STOPPED would tell the reader the agent stopped, which a database error did not")
}

// A MISSING row is permanent. The agents row cascades its queue items and its
// queue state, so this says the item's own agent is gone and no later read brings
// it back -- treating it as transient would pause the queue forever.
func TestQueueReadErrorLeavesAMissingRowPermanent(t *testing.T) {
	t.Parallel()

	err := queueReadError(sql.ErrNoRows)
	var delivery *inputqueue.DeliveryError
	assert.NotErrorIs(t, err, nil)
	assert.False(t, errors.As(err, &delivery), "a missing row must not become a transient delivery error")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// The classification must reach every store read on the dispatch path, not just
// the one helper that carries it.
//
// queueReadError's own doc claims "ONE rule at the boundary, so a read added to
// either path inherits it". It was applied BY HAND at four sites and missed five
// more, and three of those failed the reader's typed message PERMANENTLY for a
// database error that never reached the provider. These pin the two reads the
// helper now owns: the agent row, and the control responses that claim an input.
func TestDispatchClassifiesAStoreFaultRatherThanFailingTheInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	// The store itself fails, which is what tells a transient fault apart from a
	// missing row: a missing row is permanent, a closed database is not.
	require.NoError(t, svc.DB.Close())

	_, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.DispatchItem{
		StoredItem: inputqueue.StoredItem{ID: "one", AgentID: "agent-1", Text: "one",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE},
	})
	var delivery *inputqueue.DeliveryError
	require.ErrorAs(t, err, &delivery)
	assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome,
		"a read that never reached the provider leaves the input as undelivered as it was")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT,
		delivery.PauseReason,
		"AGENT_STOPPED is the default an unset reason falls back to, and it is false for a database error")
}

// resolveControlInput reads the control responses that claim this input. Its own
// comment states that every error there is a store fault -- the query is `:many`,
// so a missing row gives an empty slice and cannot produce sql.ErrNoRows -- yet it
// wrapped them with notReadyInput, which leaves the pause reason unset and lands on
// the AGENT_STOPPED fallback.
func TestResolveControlInputStatesAStoreFaultAsItsPauseReason(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	require.NoError(t, svc.DB.Close())

	_, err = svc.resolveControlInput(inputqueue.DispatchItem{
		StoredItem: inputqueue.StoredItem{ID: "one", AgentID: "agent-1", Text: "one",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION},
	}, dbAgent)
	var delivery *inputqueue.DeliveryError
	require.ErrorAs(t, err, &delivery)
	assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT,
		delivery.PauseReason)
}

// newRequeueFixture creates a Codewhale agent whose paused queue holds what a
// provider hands back, so the test reads the queue before anything dispatches.
func newRequeueFixture(t *testing.T) (*Service, agent.ProviderServices) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE,
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	return svc, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE)
}

// requeuedNotices counts the rows that state why a message appears again.
func requeuedNotices(t *testing.T, svc *Service) int {
	t.Helper()
	return len(findNotificationsByType(readAllNotifications(t, svc.Queries, "agent-1"), contracts.NotificationTypeInputRequeued))
}

// TestRequeueDroppedInputQueuesTheInputAndStatesWhyOnce pins the hand-back of a
// dropped input: the queue holds it as the reader's own message, one row states
// why it appears again, and a repeat of the same drop adds neither.
func TestRequeueDroppedInputQueuesTheInputAndStatesWhyOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := newRequeueFixture(t)
	attachments := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("body")}}

	require.NoError(t, sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", attachments))
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	item := snapshot.Items[0]
	assert.Equal(t, droppedInputID("agent-1", "turn-1/1"), item.ID)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, item.Kind)
	assert.Equal(t, "Also check the tests.", item.Text)
	assert.True(t, item.ReclassifyOnEdit, "an edit of the hand-back reads its text again, as an edit of a typed message does")
	require.Len(t, item.Metadata, 1)
	assert.Equal(t, "notes.txt", item.Metadata[0].Filename)
	assert.Equal(t, int64(4), item.Metadata[0].Size)
	assert.Equal(t, 1, requeuedNotices(t, svc))

	require.NoError(t, sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", attachments), "a repeat is not a failure")
	snapshot, err = svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, snapshot.Items, 1, "a repeat of one drop queues nothing")
	assert.Equal(t, 1, requeuedNotices(t, svc), "a repeat of one drop writes no second row")

	require.NoError(t, sink.RequeueDroppedInput("turn-1/2", "And the docs.", nil))
	snapshot, err = svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 2)
	assert.Equal(t, "And the docs.", snapshot.Items[1].Text, "the queue appends a hand-back behind what it holds")
	assert.Equal(t, 2, requeuedNotices(t, svc))
}

// TestRequeueDroppedInputReportsARefusalAndStatesNothing pins the failure paths.
// The provider states the drop to the reader itself when the queue refuses, so a
// refusal must write no row that claims the message comes again.
func TestRequeueDroppedInputReportsARefusalAndStatesNothing(t *testing.T) {
	t.Parallel()

	t.Run("a stopped queue", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		stopInputQueue(t, svc, "agent-1")
		err := sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", nil)
		require.ErrorIs(t, err, inputqueue.ErrManagerStopped)
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("the same drop with different text", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		require.NoError(t, sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", nil))
		err := sink.RequeueDroppedInput("turn-1/1", "Something else.", nil)
		require.ErrorIs(t, err, inputqueue.ErrConflict)
		assert.Equal(t, 1, requeuedNotices(t, svc))
	})

	t.Run("an empty input", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		err := sink.RequeueDroppedInput("turn-1/1", "", nil)
		require.Error(t, err)
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("a subagent", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		childID, err := sink.EnsureChildAgent("spawn-span", "row-key", "child task")
		require.NoError(t, err)
		err = sink.ChildSink(childID).RequeueDroppedInput("turn-1/1", "Also check the tests.", nil)
		require.ErrorContains(t, err, "has no input queue")
		snapshot, err := svc.InputQueue.Snapshot(context.Background(), childID)
		require.NoError(t, err)
		assert.Empty(t, snapshot.Items)
	})

	t.Run("no queue wired", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		svc.Output.SetRequeueDroppedInputFunc(nil)
		err := sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", nil)
		require.ErrorContains(t, err, "no input queue is wired")
		assert.Zero(t, requeuedNotices(t, svc))
	})
}

// A provider can hand back one drop from two goroutines at once, for example a
// turn end and a stop that race. The queue adds the item once, and one row
// states why the message appears again.
func TestRequeueDroppedInputAddsOneDropOnceAcrossConcurrentCalls(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := newRequeueFixture(t)
	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			assert.NoError(t, sink.RequeueDroppedInput("turn-1/1", "Also check the tests.", nil))
		})
	}
	wg.Wait()

	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, snapshot.Items, 1)
	assert.Equal(t, 1, requeuedNotices(t, svc))
}

func TestDroppedInputIDKeepsEachAgentAndDropApart(t *testing.T) {
	t.Parallel()

	id := droppedInputID("agent-1", "turn-1/1")
	assert.Regexp(t, `^dropped-[0-9a-f]{32}$`, id)
	assert.Equal(t, id, droppedInputID("agent-1", "turn-1/1"), "one drop gives one id")
	assert.NotEqual(t, id, droppedInputID("agent-2", "turn-1/1"), "the id is unique across the Worker")
	assert.NotEqual(t, id, droppedInputID("agent-1", "turn-1/2"))
	// A drop id is the provider's own text, so no separator keeps the pair apart.
	assert.NotEqual(t, droppedInputID("a\x00b", "c"), droppedInputID("a", "b\x00c"))
	assert.NotEqual(t, droppedInputID("ab", "c"), droppedInputID("a", "bc"))
	assert.NotEqual(t, droppedInputID("", "x"), droppedInputID("x", ""))
}
