package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/skills"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

func TestPrivateEventsBus_SnapshotPanicCleansUpSubscriber(t *testing.T) {
	t.Parallel()
	bus := NewPrivateEventsBus()
	defer bus.Stop()

	owner := userid.MustNew("user-1")
	require.Panics(t, func() {
		_ = bus.SnapshotAndSubscribe(context.Background(), owner, func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
			panic("snapshot boom")
		}, func(*leapmuxv1.WorkerPrivateEvent) error {
			return nil
		})
	})

	// SWEEP-5: Subscriber map must be empty after snapshotFn panics.
	bus.mu.RLock()
	remaining := len(bus.subscribers[owner.String()])
	bus.mu.RUnlock()
	assert.Zero(t, remaining, "leaked subscriber after snapshotFn panic")
}

func TestTabStatusStore_SnapshotSortsEqualTabIDByTabType(t *testing.T) {
	t.Parallel()
	bus := NewPrivateEventsBus()
	defer bus.Stop()
	store := NewTabStatusStore(bus)
	owner := userid.MustNew("user-1")
	keyAgent := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "same-id", owner.String())
	keyTerm := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "same-id", owner.String())
	keyFile := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "same-id", owner.String())

	_, err := store.set(keyFile, owner, defaultTabStatusKey, &leapmuxv1.TabStatus{Text: "f"}, 0)
	require.NoError(t, err)
	_, err = store.set(keyAgent, owner, defaultTabStatusKey, &leapmuxv1.TabStatus{Text: "a"}, 0)
	require.NoError(t, err)
	_, err = store.set(keyTerm, owner, defaultTabStatusKey, &leapmuxv1.TabStatus{Text: "t"}, 0)
	require.NoError(t, err)

	// TRAPS-21: Repeated snapshots must always yield identical deterministic order by (tabID, tabType).
	for range 32 {
		events := store.snapshotFor(owner)
		require.Len(t, events, 3)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, events[0].GetTabStatusChanged().GetTabType())
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, events[1].GetTabStatusChanged().GetTabType())
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, events[2].GetTabStatusChanged().GetTabType())
	}
}

func TestSetNativeControlInputs_UnmatchedClaimDoesNotBroadcastSpuriousEvent(t *testing.T) {
	t.Parallel()
	svc, _, w := setupTestService(t)
	ctx := context.Background()
	const agentID = "agent-native-ctrl"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	registerAgentWatch(svc, "test-ch", agentID, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, w)

	// WRAPPERS-5: Calling SetNativeControlInputs for a non-existent/stale claim must NOT broadcast InputReplyChanged.
	before := len(w.streamsSnapshot())
	require.NoError(t, sink.SetNativeControlInputs(agent.NativeControlInputs{
		SessionID:  "sess-1",
		RequestID:  "missing-req",
		InstanceID: "stale-claim",
		InputIDs:   []string{"in-1"},
	}))
	after := w.streamsSnapshot()[before:]
	for _, msg := range after {
		evt := decodeWatchAgentEvent(t, msg)
		_, isInputReplyChanged := evt.GetEvent().(*leapmuxv1.AgentEvent_InputReplyChanged)
		assert.False(t, isInputReplyChanged, "unmatched SetNativeControlInputs must not broadcast InputReplyChanged")
	}
}

func TestInputReply_MidPagingCanonicalTransitionDoesNotTruncate(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, sessionID, inputID, nativeID, turnID = "agent-1", "session-a", "input-1", "native-in-1", "turn-1"
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: sessionID}))

	write := func(id, key, nativeIn, turn string, source leapmuxv1.MessageSource, text string) int64 {
		t.Helper()
		data, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, agent.MessageCompletionComplete)
		require.NoError(t, err)
		compressed, compression := msgcodec.Compress(data)
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: id, AgentID: agentID, AgentSessionID: sessionID, IdempotencyKey: key,
			NativeInputID: nativeIn, NativeTurnID: turn, Source: source,
			Content: compressed, ContentCompression: compression, CreatedAt: sqltime.NewSQLiteTime(nowMillis()),
		})
		require.NoError(t, err)
		if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			_, err = svc.DB.ExecContext(ctx, `UPDATE messages SET from_input_queue = 1 WHERE agent_id = ? AND id = ?`, agentID, id)
			require.NoError(t, err)
		}
		return seq
	}

	// Seed filler messages so message seqs (>10) are strictly larger than canonical ordinals (1..3).
	for i := range 10 {
		write(fmt.Sprintf("filler-%d", i), "", "", "", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, "filler")
	}
	write(inputID, inputqueue.InputRequestKeyPrefix+inputID, nativeID, "", leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, "ask")
	write("reply-1", "key-r1", nativeID, turnID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, "part one")
	write("reply-2", "key-r2", nativeID, turnID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, "part two")

	sink := &agentOutputSink{h: svc.Output, agentID: agentID}
	require.NoError(t, sink.BindNativeInputTurn(agent.NativeInputReceipt{SessionID: sessionID, InputID: nativeID}, turnID))

	// Page 1 before canonical publication returns seq cursor (>10) and ReplyCanonical == false.
	w1 := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID, ReplyInputId: inputID, IncludeReplyText: true,
		Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, Limit: 1,
	}, w1)
	require.Empty(t, w1.errors)
	page1 := decodeQueueResponse(t, w1, &leapmuxv1.ListAgentMessagesResponse{})
	require.Len(t, page1.GetMessages(), 1)
	require.True(t, page1.GetHasMore())
	assert.False(t, page1.GetReplyCanonical())
	seqCursor := page1.GetNextReplyCursor()
	require.Greater(t, seqCursor, int64(10))

	// Now publish the canonical turn with ordinals 2 and 3 (both < seqCursor!).
	require.NoError(t, sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: sessionID, TurnID: turnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: nativeID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "key-r1"}, {Ordinal: 3, MessageKey: "key-r2"}},
	}))

	// Page 2 passes seqCursor with ReplyCursorCanonical=false: Worker must restart from canonical ordinal 0
	// and set ReplyCanonical=true instead of applying seqCursor (>10) against ordinals (2, 3).
	nonCanon := false
	w2 := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID, ReplyInputId: inputID, IncludeReplyText: true,
		Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, Limit: 1,
		ReplyCursor: seqCursor, ReplyCursorCanonical: &nonCanon,
	}, w2)
	require.Empty(t, w2.errors)
	page2 := decodeQueueResponse(t, w2, &leapmuxv1.ListAgentMessagesResponse{})
	assert.True(t, page2.GetReplyCanonical())
	require.Len(t, page2.GetMessages(), 1)
	assert.Equal(t, "reply-1", page2.GetMessages()[0].GetId())
	assert.True(t, page2.GetHasMore())
	assert.Equal(t, int64(2), page2.GetNextReplyCursor())
}

func TestWorkerSkills_FailedSyncDoesNotPersistMutatedState(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	// Create a regular file at ~/.agents/skills so SyncEnabled fails to create directories under it.
	agentsDir := filepath.Join(svc.HomeDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "skills"), []byte("blocker"), 0o644))

	autoSync := true
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AutoSync:            &autoSync,
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux"},
		Targets:             []string{"agents"},
		SyncNow:             true,
	}, w)
	require.Len(t, w.errors, 1, "SyncEnabled failure must fail UpdateWorkerSkills")

	// TRAPS-15: skills-state.json must NOT hold the failed update on disk.
	st, err := skills.LoadWorkerState(svc.DataDir)
	require.NoError(t, err)
	assert.False(t, st.AutoSync, "failed skill update must not persist auto_sync=true")
	assert.Empty(t, st.Enabled, "failed skill update must not persist enabled skills")
}

func TestListAgents_IncludeDescendantsPropagatesDBError(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	seedAgent(t, svc, "root-desc-err")
	// Force ListDescendantAgentIDs (recursive CTE over agents) to fail while ListAgentsByIDs would fail or succeed.
	_, err := svc.DB.ExecContext(ctx, `DROP TABLE agents`)
	require.NoError(t, err)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
		TabIds:             []string{"root-desc-err"},
		IncludeDescendants: true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Internal), w.errors[0].code)
}

func TestOpenTerminal_QuakeReportsAdoptedFlag(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return nil
	}
	dir := t.TempDir()

	// First quake OpenTerminal creates a fresh quake terminal -> Adopted == false.
	w1 := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir: dir,
		Target:     &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
	}, w1)
	require.Empty(t, w1.errors)
	var resp1 leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w1.responses[0].GetPayload(), &resp1))
	assert.False(t, resp1.GetAdopted(), "first quake OpenTerminal creates a new terminal")
	require.NotEmpty(t, resp1.GetTerminalId())

	// Second quake OpenTerminal adopts the live quake terminal -> Adopted == true.
	w2 := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     dir,
		Target:         &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
		InitialCommand: "echo hi",
	}, w2)
	require.Empty(t, w2.errors)
	var resp2 leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w2.responses[0].GetPayload(), &resp2))
	assert.True(t, resp2.GetAdopted(), "adopting an existing quake terminal must set Adopted=true")
	assert.Equal(t, resp1.GetTerminalId(), resp2.GetTerminalId())
}

func TestAgentInputQueueAdapter_SteerRejectsDeadChildOwnerAsNotReady(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-steer-dead",
		Title:         "Root",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-steer-dead",
		Title:         "Child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
		ParentAgentID: sql.NullString{String: "root-steer-dead", Valid: true},
	}))
	now := sqltime.NewSQLiteTime(nowMillis())
	_, err := svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID:  "root-steer-dead",
		RowKey:        "task-row-1",
		Kind:          leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Role:          leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK,
		Status:        leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
		ChildAgentID:  "child-steer-dead",
		ParentAgentID: "root-steer-dead",
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	require.NoError(t, err)

	// WRAPPERS-3: Steer must return DispatchNotReady when child owner is not alive.
	adapter := &agentInputQueueAdapter{svc: svc}
	_, err = adapter.Steer(inputqueue.DispatchItem{
		ID:      "in-steer-1",
		AgentID: "child-steer-dead",
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text:    "hello child",
	})
	var deliveryErr *inputqueue.DeliveryError
	require.True(t, errors.As(err, &deliveryErr))
	assert.Equal(t, inputqueue.DispatchNotReady, deliveryErr.Outcome)
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestListAgentMessages_IncludeReplyTextWithCompressedSupplement(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	const agentID = "agent-reply-supp"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		Title:         "ReplySupp",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))

	rawContent, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "answer with supplement", agent.MessageCompletionComplete)
	require.NoError(t, err)
	compContent, contentComp := msgcodec.Compress(rawContent)
	// Build a valid stored supplement large enough to trigger zstd compression.
	suppRaw := []byte(`{"version":1,"items":[{"kind":"note","text":"` + string(make([]byte, 512)) + `"}]}`)
	for i := range suppRaw {
		if suppRaw[i] == 0 {
			suppRaw[i] = 'x'
		}
	}
	compSupp, suppComp := msgcodec.Compress(suppRaw)

	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:                             "msg-supp-1",
		AgentID:                        agentID,
		AgentSessionID:                 "sess-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:                        compContent,
		ContentCompression:             contentComp,
		SupplementalContent:            compSupp,
		SupplementalContentCompression: suppComp,
		CreatedAt:                      sqltime.NewSQLiteTime(nowMillis()),
	})
	require.NoError(t, err)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
		AgentId:          agentID,
		Anchor:           leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST,
		Limit:            10,
		IncludeReplyText: true,
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.ListAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetMessages(), 1)
	require.NotNil(t, resp.GetMessages()[0].ReplyText)
	assert.Equal(t, "answer with supplement", resp.GetMessages()[0].GetReplyText())
}
