package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/leapmux/leapmux/util/repopaths"
)

type forkCapabilityPlugin struct {
	agent.Provider
	supportsFork bool
}

func (p forkCapabilityPlugin) SupportsFork() bool { return p.supportsFork }

func registryWithForkSupport(t *testing.T, overrides map[leapmuxv1.AgentProvider]bool) *agent.Registry {
	t.Helper()
	regs := providers.Registrations()
	for i := range regs {
		if supports, ok := overrides[regs[i].Provider]; ok {
			regs[i].Plugin = forkCapabilityPlugin{Provider: regs[i].Plugin, supportsFork: supports}
		}
	}
	r, err := agent.NewRegistry(regs...)
	require.NoError(t, err)
	return r
}

type forkCapableTestAgent struct {
	agenttest.IdleAgent
	supportsFork bool
}

func (a forkCapableTestAgent) SupportsNativeFork() bool { return a.supportsFork }

func openAgentAs(t *testing.T, d testRPCDispatcher, caller channel.Caller, req *leapmuxv1.OpenAgentRequest) *testResponseWriter {
	t.Helper()
	payload, err := proto.Marshal(req)
	require.NoError(t, err)
	writer := newTestWriter()
	dispatchTestRequest(d, context.Background(), caller, &leapmuxv1.InnerRpcRequest{
		Method:  leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure,
		Payload: payload,
	}, writer)
	return writer
}

func decodeOpenAgentResponse(t *testing.T, writer *testResponseWriter) *leapmuxv1.OpenAgentResponse {
	t.Helper()
	require.Empty(t, writer.errors, "unexpected RPC errors: %+v", writer.errors)
	require.Len(t, writer.responses, 1)
	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &resp))
	return &resp
}

func listAgentInfo(t *testing.T, d testRPCDispatcher, tabID string) *leapmuxv1.AgentInfo {
	t.Helper()
	writer := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
		TabIds: []string{tabID},
	}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	return resp.GetAgents()[0]
}

func TestOpenAgentSpawnedByAgentLineage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, dispatcher, _ := setupTestService(t)
	t.Cleanup(func() { drainAllInFlight(svc) })
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, nil
	}

	const spawnerID = "spawner-agent"
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            spawnerID,
		WorkingDir:    workingDir,
		Title:         "Coordinator",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateTerminal(ctx, db.CreateTerminalParams{
		ID:            "terminal-1",
		WorkingDir:    workingDir,
		ShellStartDir: workingDir,
		Shell:         "/bin/sh",
		Title:         "Shell",
		Cols:          80,
		Rows:          24,
		Screen:        []byte{},
	}))

	// 1. Local agent tab caller without InitialMessage stamps spawned_by_agent_id.
	respNoMsg := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher, tabCaller(spawnerID), &leapmuxv1.OpenAgentRequest{
		WorkingDir:    workingDir,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
		Title:         "Worker A",
	}))
	workerAID := respNoMsg.GetAgent().GetId()
	assert.Equal(t, spawnerID, respNoMsg.GetAgent().GetSpawnedByAgentId())
	assert.Equal(t, spawnerID, listAgentInfo(t, dispatcher, workerAID).GetSpawnedByAgentId())

	rowA, err := svc.Queries.GetAgentByID(ctx, workerAID)
	require.NoError(t, err)
	assert.Equal(t, spawnerID, rowA.SpawnedByAgentID)

	// 2. Local agent tab caller WITH InitialMessage stamps both spawned_by_agent_id and queue sender.
	respWithMsg := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher, tabCaller(spawnerID), &leapmuxv1.OpenAgentRequest{
		WorkingDir:       workingDir,
		AgentProvider:    leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
		Title:            "Worker B",
		InitialMessage:   proto.String("Investigate failure"),
		InitialMessageId: proto.String("init-msg-1"),
	}))
	workerBID := respWithMsg.GetAgent().GetId()
	assert.Equal(t, spawnerID, respWithMsg.GetAgent().GetSpawnedByAgentId())

	snap, err := svc.InputQueue.Snapshot(ctx, workerBID)
	require.NoError(t, err)
	require.Len(t, snap.Items, 1)
	require.NotNil(t, snap.Items[0].Sender)
	assert.Equal(t, spawnerID, snap.Items[0].Sender.TabID)

	spawnedRows, err := svc.Queries.ListAgentsBySpawnedBy(ctx, spawnerID)
	require.NoError(t, err)
	require.Len(t, spawnedRows, 2)
	assert.ElementsMatch(t, []string{workerAID, workerBID}, []string{spawnedRows[0].ID, spawnedRows[1].ID})

	// 3. Relayed sibling-Worker agent caller stamps spawned_by_agent_id.
	relayedResp := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher,
		relayedCaller("remote-agent-9", channel.SourceAgent{
			Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			Title:    "Remote Coordinator",
		}),
		&leapmuxv1.OpenAgentRequest{
			WorkingDir:    workingDir,
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
			Title:         "Relayed Worker",
		}))
	assert.Equal(t, "remote-agent-9", relayedResp.GetAgent().GetSpawnedByAgentId())

	// 4. Browser/CLI caller (empty SourceTabID) leaves spawned_by_agent_id empty.
	browserResp := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher,
		channel.NewCaller(svc.RegisteredBy(), authscope.UnscopedGrant()),
		&leapmuxv1.OpenAgentRequest{
			WorkingDir:    workingDir,
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
			Title:         "Browser Agent",
		}))
	assert.Empty(t, browserResp.GetAgent().GetSpawnedByAgentId())

	// 5. Terminal tab caller leaves spawned_by_agent_id empty.
	termResp := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher,
		tabCaller("terminal-1"),
		&leapmuxv1.OpenAgentRequest{
			WorkingDir:    workingDir,
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
			Title:         "Terminal Spawned Agent",
		}))
	assert.Empty(t, termResp.GetAgent().GetSpawnedByAgentId())
}

func TestOpenAgentForkSourceClonesTranscriptTodosAndChildren(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	reg := registryWithPlugin(t, provider, forkCapabilityPlugin{
		Provider:     testRegistry.Plugin(provider),
		supportsFork: true,
	})
	svc, dispatcher, _ := setupTestService(t, withRegistry(reg))
	t.Cleanup(func() { drainAllInFlight(svc) })

	capturedCh := make(chan agent.Options, 1)
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		capturedCh <- opts
		return nil, nil
	}

	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	const sourceID = "fork-source"
	const spawnerID = "spawner-root"
	const sourceSessionID = "native-session-src-1"

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            spawnerID,
		WorkingDir:    sourceDir,
		Title:         "Spawner",
		AgentProvider: provider,
	}))

	// Seed an OPEN source agent (must stay open to verify no unique-index collision).
	seedTranscriptSource(t, svc, sourceID, sourceDir, sourceSessionID, provider, false)
	require.NoError(t, svc.Queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
		PlanFilePath: "/tmp/plan.md",
		PlanTitle:    "Refactor Plan",
		ID:           sourceID,
	}))
	now := sqltime.NewSQLiteTime(time.Now().UTC().Truncate(time.Millisecond))
	require.NoError(t, svc.Queries.UpdateAgentGoal(ctx, db.UpdateAgentGoalParams{
		GoalNativeID:     "goal-1",
		GoalObjective:    "Keep this goal out of fork",
		GoalStatus:       leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE,
		GoalStatusDetail: "running",
		GoalCreatedAt:    sqltime.SQLiteNullTimeOf(now.Time),
		GoalUpdatedAt:    sqltime.SQLiteNullTimeOf(now.Time),
		ID:               sourceID,
	}))

	// Messages + turn_end + retraction on source root.
	for _, msg := range []struct {
		id      string
		source  leapmuxv1.MessageSource
		content string
		key     string
	}{
		{"src-m1", leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, `{"type":"user","text":"hello"}`, "k-1"},
		{"src-m2", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"type":"assistant","text":"world"}`, "k-2"},
	} {
		_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
			ID: msg.id, AgentID: sourceID, AgentSessionID: sourceSessionID,
			Source: msg.source, Content: []byte(msg.content),
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			IdempotencyKey:                 msg.key,
			CreatedAt:                      now,
		})
		require.NoError(t, err)
	}
	_, err := svc.Queries.ClaimAgentTurnEnd(ctx, db.ClaimAgentTurnEndParams{
		AgentID: sourceID, AgentSessionID: sourceSessionID, IdempotencyKey: "turn-end-1",
	})
	require.NoError(t, err)
	_, err = svc.Queries.RecordMessageRetraction(ctx, db.RecordMessageRetractionParams{
		AgentID: sourceID, AgentSessionID: sourceSessionID, IdempotencyKey: "retracted-1",
	})
	require.NoError(t, err)

	// Virtual child + child message + background task.
	const childID = "src-child-1"
	seedArchivedChildForResume(t, svc, sourceID, sourceID, childID, "child-1", 1, bgtask.StatusSucceeded)
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID: "child-m1", AgentID: childID, AgentSessionID: "child-sess-1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte(`{"text":"child output"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt:                      now,
	})
	require.NoError(t, err)

	// Todos on source root.
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: sourceID, Seq: 1, TaskID: "t-1", Content: "First task",
		Status: leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED,
	}))
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: sourceID, Seq: 2, TaskID: "t-2", Content: "Second task",
		Status: leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
	}))

	// Live control request and queued input on source root (must NOT be cloned).
	_, err = svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
		AgentID: sourceID, AgentSessionID: sourceSessionID, RequestID: "ctrl-1",
		Payload: []byte(`{"type":"control_request"}`), ClaimToken: "tok-1",
	})
	require.NoError(t, err)
	_, err = svc.InputQueue.SetPaused(ctx, sourceID, true)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID:      "queued-src-1",
		AgentID: sourceID,
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text:    "staged on source",
	})
	require.NoError(t, err)

	// Fork source into targetDir from spawnerID.
	resp := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher, tabCaller(spawnerID), &leapmuxv1.OpenAgentRequest{
		WorkingDir:    targetDir,
		AgentProvider: provider,
		Title:         "Forked Tab",
		ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: sourceID},
	}))
	forkID := resp.GetAgent().GetId()
	require.NotEmpty(t, forkID)
	assert.NotEqual(t, sourceID, forkID)
	assert.Equal(t, sourceID, resp.GetAgent().GetForkedFromAgentId())
	assert.Equal(t, spawnerID, resp.GetAgent().GetSpawnedByAgentId())
	assert.False(t, resp.GetAgent().GetSupportsFork(), "no confirmed native session yet before startup completes")

	// Startup receives fork options without ResumeSessionID.
	select {
	case opts := <-capturedCh:
		assert.Equal(t, sourceSessionID, opts.ForkFromSessionID)
		assert.Equal(t, sourceDir, opts.ForkSourceWorkingDir)
		assert.Empty(t, opts.ResumeSessionID)
		assert.Equal(t, targetDir, opts.WorkingDir)
	case <-time.After(10 * time.Second):
		t.Fatal("startAgentFn was not invoked for forked agent")
	}
	drainAllInFlight(svc)

	// Verify DB row fields.
	forkRow, err := svc.Queries.GetAgentByID(ctx, forkID)
	require.NoError(t, err)
	assert.Equal(t, sourceID, forkRow.ForkedFromAgentID)
	assert.Equal(t, spawnerID, forkRow.SpawnedByAgentID)
	assert.Equal(t, int64(1), forkRow.Resumed)
	assert.Empty(t, forkRow.PendingResumeSessionID, "fork must not claim source session in pending_resume_session_id")
	assert.Equal(t, "/tmp/plan.md", forkRow.PlanFilePath)
	assert.Equal(t, "Refactor Plan", forkRow.PlanTitle)
	assert.Empty(t, forkRow.GoalObjective, "live goal must not be cloned")
	assert.Zero(t, forkRow.GoalStatus, "live goal status must not be cloned")

	// Verify cloned transcript messages and high-water mark.
	forkMsgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: forkID, Seq: 0})
	require.NoError(t, err)
	require.Len(t, forkMsgs, 2)
	assert.Equal(t, []byte(`{"type":"user","text":"hello"}`), forkMsgs[0].Content)
	assert.Equal(t, []byte(`{"type":"assistant","text":"world"}`), forkMsgs[1].Content)
	nextSeq, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID: "fork-m3", AgentID: forkID, AgentSessionID: "forked-session-2",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte(`{"text":"new turn"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt:                      now,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), nextSeq)

	// Verify cloned turn_ends and retractions.
	hasTurnEnd, err := svc.Queries.HasAgentTurnEnd(ctx, db.HasAgentTurnEndParams{
		AgentID: forkID, AgentSessionID: sourceSessionID, IdempotencyKey: "turn-end-1",
	})
	require.NoError(t, err)
	assert.True(t, hasTurnEnd)
	hasRetraction, err := svc.Queries.HasMessageRetraction(ctx, db.HasMessageRetractionParams{
		AgentID: forkID, AgentSessionID: sourceSessionID, IdempotencyKey: "retracted-1",
	})
	require.NoError(t, err)
	assert.True(t, hasRetraction)

	// Verify cloned virtual child tree + child messages + background tasks.
	descendants, err := svc.Queries.ListAgentDescendantsForResume(ctx, sql.NullString{String: forkID, Valid: true})
	require.NoError(t, err)
	require.Len(t, descendants, 1)
	assert.NotEqual(t, childID, descendants[0].ID)
	childMsgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: descendants[0].ID, Seq: 0})
	require.NoError(t, err)
	require.Len(t, childMsgs, 2, "both the seeded archived child answer and the extra child message are cloned")
	assert.Equal(t, []byte(`{"text":"child output"}`), childMsgs[1].Content)
	forkTasks, err := svc.Queries.ListAllAgentBackgroundTasksForResume(ctx, forkID)
	require.NoError(t, err)
	require.Len(t, forkTasks, 1)
	assert.Equal(t, descendants[0].ID, forkTasks[0].ChildAgentID)

	// Verify cloned todos.
	forkTodos, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{
		AgentID: forkID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, forkTodos, 2)
	assert.Equal(t, "Second task", forkTodos[0].Content)
	assert.Equal(t, "First task", forkTodos[1].Content)

	// Verify live control_requests and input queue were NOT cloned.
	forkControls, err := svc.Queries.ListControlRequestsByAgentID(ctx, forkID)
	require.NoError(t, err)
	assert.Empty(t, forkControls)
	forkQueue, err := svc.InputQueue.Snapshot(ctx, forkID)
	require.NoError(t, err)
	assert.Empty(t, forkQueue.Items)

	// Confirm the newly minted session ID on the forked agent and verify
	// resolveResumeSessionIDForAgent resumes it even without new user messages.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: forkID, AgentSessionID: "forked-session-2",
	}))
	forkRow, err = svc.Queries.GetAgentByID(ctx, forkID)
	require.NoError(t, err)
	assert.Equal(t, "forked-session-2", svc.resolveResumeSessionIDForAgent(forkID, forkRow))
	assert.True(t, listAgentInfo(t, dispatcher, forkID).GetSupportsFork())
}

func TestOpenAgentForkSourceRejectsInvalidAndBusyStates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	forkableProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	unsupportedProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	reg := registryWithForkSupport(t, map[leapmuxv1.AgentProvider]bool{
		forkableProvider:    true,
		unsupportedProvider: false,
	})
	svc, dispatcher, _ := setupTestService(t, withRegistry(reg))
	t.Cleanup(func() { drainAllInFlight(svc) })
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, nil
	}

	workingDir := t.TempDir()
	// Idle forkable source.
	seedTranscriptSource(t, svc, "src-ok", workingDir, "sess-ok", forkableProvider, false)
	// Source without session ID.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "src-no-session", WorkingDir: workingDir, AgentProvider: forkableProvider,
	}))
	// Virtual child source.
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "src-child", ParentAgentID: sql.NullString{String: "src-ok", Valid: true},
		SpawnSpanID: "sp-1", WorkingDir: workingDir, AgentProvider: forkableProvider,
	}))
	// Unsupported provider source.
	seedTranscriptSource(t, svc, "src-unsupported", workingDir, "sess-unsup", unsupportedProvider, false)

	// Busy WORKING source.
	seedTranscriptSource(t, svc, "src-working", workingDir, "sess-working", forkableProvider, false)
	svc.Agents.PutAgentForTest("src-working", agenttest.IdleAgent{})
	workingSink := newTestRootOutputSink(t, svc.Output, "src-working", forkableProvider)
	workingSink.UpdateSessionID("sess-working")
	workingSink.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("src-working", "src-working").State)

	// Busy WAITING_FOR_USER source (requires an active turn behind the pending prompt).
	seedTranscriptSource(t, svc, "src-waiting", workingDir, "sess-waiting", forkableProvider, false)
	svc.Agents.PutAgentForTest("src-waiting", agenttest.IdleAgent{})
	waitingSink := newTestRootOutputSink(t, svc.Output, "src-waiting", forkableProvider)
	waitingSink.UpdateSessionID("sess-waiting")
	waitingSink.SetTurnState(agent.TurnState{Active: true}, 1)
	publishStopControl(t, waitingSink, "req-wait-1")
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.Output.AgentActivitySnapshot("src-waiting", "src-waiting").State)

	cases := []struct {
		name            string
		req             *leapmuxv1.OpenAgentRequest
		wantCode        codes.Code
		wantMsgContains string
	}{
		{
			name: "mutually exclusive with agent_session_id",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:     workingDir,
				AgentProvider:  forkableProvider,
				AgentSessionId: "sess-ok",
				ForkSource:     &leapmuxv1.AgentForkSource{SourceAgentId: "src-ok"},
			},
			wantCode:        codes.InvalidArgument,
			wantMsgContains: "exclude each other",
		},
		{
			name: "empty source_agent_id",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "   "},
			},
			wantCode:        codes.InvalidArgument,
			wantMsgContains: "source_agent_id",
		},
		{
			name: "unknown source agent",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "does-not-exist"},
			},
			wantCode:        codes.NotFound,
			wantMsgContains: "not found",
		},
		{
			name: "virtual child source",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-child"},
			},
			wantCode:        codes.FailedPrecondition,
			wantMsgContains: "virtual child",
		},
		{
			name: "provider mismatch",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: unsupportedProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-ok"},
			},
			wantCode:        codes.InvalidArgument,
			wantMsgContains: "does not match",
		},
		{
			name: "busy working source",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-working"},
			},
			wantCode:        codes.FailedPrecondition,
			wantMsgContains: "is busy",
		},
		{
			name: "busy waiting for user source",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-waiting"},
			},
			wantCode:        codes.FailedPrecondition,
			wantMsgContains: "is busy",
		},
		{
			name: "missing native session id",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: forkableProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-no-session"},
			},
			wantCode:        codes.FailedPrecondition,
			wantMsgContains: "no native session",
		},
		{
			name: "unsupported provider",
			req: &leapmuxv1.OpenAgentRequest{
				WorkingDir:    workingDir,
				AgentProvider: unsupportedProvider,
				ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "src-unsupported"},
			},
			wantCode:        codes.FailedPrecondition,
			wantMsgContains: "does not support native conversation-history forking",
		},
	}

	caller := channel.NewCaller(svc.RegisteredBy(), authscope.UnscopedGrant())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := openAgentAs(t, dispatcher, caller, tc.req)
			assert.Empty(t, w.responses)
			require.Len(t, w.errors, 1)
			assert.Equal(t, int32(tc.wantCode), w.errors[0].code)
			assert.Contains(t, w.errors[0].message, tc.wantMsgContains)
		})
	}
}

func TestAgentSupportsForkStaticAndDynamicCapability(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	staticTrue := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	staticFalse := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	reg := registryWithForkSupport(t, map[leapmuxv1.AgentProvider]bool{
		staticTrue:  true,
		staticFalse: false,
	})
	svc, dispatcher, _ := setupTestService(t, withRegistry(reg))
	t.Cleanup(func() { drainAllInFlight(svc) })
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, nil
	}

	workingDir := t.TempDir()
	// Static false provider with live ForkCapable returning true -> supports fork.
	seedTranscriptSource(t, svc, "dyn-true", workingDir, "sess-dyn-true", staticFalse, false)
	assert.False(t, listAgentInfo(t, dispatcher, "dyn-true").GetSupportsFork())
	svc.Agents.PutAgentForTest("dyn-true", forkCapableTestAgent{supportsFork: true})
	assert.True(t, listAgentInfo(t, dispatcher, "dyn-true").GetSupportsFork())

	caller := channel.NewCaller(svc.RegisteredBy(), authscope.UnscopedGrant())
	resp := decodeOpenAgentResponse(t, openAgentAs(t, dispatcher, caller, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    workingDir,
		AgentProvider: staticFalse,
		ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "dyn-true"},
	}))
	assert.Equal(t, "dyn-true", resp.GetAgent().GetForkedFromAgentId())

	// Static true provider overridden by live ForkCapable returning false -> does not support fork.
	seedTranscriptSource(t, svc, "dyn-false", workingDir, "sess-dyn-false", staticTrue, false)
	assert.True(t, listAgentInfo(t, dispatcher, "dyn-false").GetSupportsFork())
	svc.Agents.PutAgentForTest("dyn-false", forkCapableTestAgent{supportsFork: false})
	assert.False(t, listAgentInfo(t, dispatcher, "dyn-false").GetSupportsFork())

	wRefused := openAgentAs(t, dispatcher, caller, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    workingDir,
		AgentProvider: staticTrue,
		ForkSource:    &leapmuxv1.AgentForkSource{SourceAgentId: "dyn-false"},
	})
	require.Len(t, wRefused.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wRefused.errors[0].code)

	// Virtual child on static-true provider -> never supports fork.
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child-static-true", ParentAgentID: sql.NullString{String: "dyn-false", Valid: true},
		SpawnSpanID: "sp-child", WorkingDir: workingDir, AgentProvider: staticTrue, AgentSessionID: "sess-child",
	}))
	assert.False(t, listAgentInfo(t, dispatcher, "child-static-true").GetSupportsFork())
}

func TestRestoreAgentCreationForFork(t *testing.T) {
	t.Parallel()
	for _, confirmed := range []bool{false, true} {
		name := "before session confirmation preserves fork intent"
		if confirmed {
			name = "after session confirmation switches to resume"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			svc, _, _ := setupTestService(t)
			t.Cleanup(func() { drainAllInFlight(svc) })

			capturedCh := make(chan agent.Options, 1)
			svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				capturedCh <- opts
				return nil, nil
			}

			targetDir := t.TempDir()
			sourceDir := t.TempDir()
			tabID := "restored-fork-agent"
			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID:                tabID,
				WorkingDir:        targetDir,
				Title:             "Restored Fork",
				AgentProvider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
				Resumed:           1,
				ForkedFromAgentID: "orig-agent",
			}))
			if confirmed {
				require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
					ID: tabID, AgentSessionID: "confirmed-fork-session",
				}))
			}

			savedRuntime := agentCreationRuntime{
				Startup: creationStartupIntent{
					Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: targetDir, PlannedWorkingDir: targetDir},
				},
				ForkFromSessionID:    "source-session-id",
				ForkSourceWorkingDir: sourceDir,
				Options:              OptionMap{},
				DefaultedIDs:         map[string]bool{},
			}
			runtimeBytes, err := json.Marshal(savedRuntime)
			require.NoError(t, err)

			pub := &leapmuxv1.TabPublication{
				Creation: &leapmuxv1.TabCreation{TabId: tabID},
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			}
			pubBytes, err := proto.Marshal(pub)
			require.NoError(t, err)
			published := &leapmuxv1.PublishedTab{
				Tab: &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			}
			publishedBytes, err := proto.Marshal(published)
			require.NoError(t, err)
			reqBytes, err := proto.Marshal(&leapmuxv1.OpenAgentRequest{WorkingDir: targetDir})
			require.NoError(t, err)

			caller := channel.NewCaller(svc.RegisteredBy(), authscope.UnscopedGrant())
			require.NoError(t, svc.Queries.InsertTabCreation(ctx, db.InsertTabCreationParams{
				UserID:             caller.UserID.String(),
				WorkerID:           svc.WorkerID,
				TabID:              tabID,
				TabType:            int64(leapmuxv1.TabType_TAB_TYPE_AGENT),
				RequestHash:        make([]byte, 32),
				RequestPayload:     reqBytes,
				PublicationPayload: pubBytes,
				RuntimePayload:     runtimeBytes,
				PublicationResult:  publishedBytes,
				State:              creationPublished,
			}))
			record, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{
				UserID: caller.UserID.String(), WorkerID: svc.WorkerID, TabID: tabID,
			})
			require.NoError(t, err)

			op := &creationOperation{
				coordinator: svc.creations,
				caller:      caller,
				record:      &record,
				publication: pub,
				persisted:   true,
			}
			writer := newTestWriter()
			svc.restoreAgentCreation(op, writer)
			require.Empty(t, writer.errors, "unexpected restore errors: %+v", writer.errors)

			select {
			case opts := <-capturedCh:
				if confirmed {
					assert.Equal(t, "confirmed-fork-session", opts.ResumeSessionID)
					assert.Empty(t, opts.ForkFromSessionID)
					assert.Empty(t, opts.ForkSourceWorkingDir)
				} else {
					assert.Empty(t, opts.ResumeSessionID)
					assert.Equal(t, "source-session-id", opts.ForkFromSessionID)
					assert.Equal(t, sourceDir, opts.ForkSourceWorkingDir)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("startAgentFn was not invoked by restoreAgentCreation")
			}
			drainAllInFlight(svc)
		})
	}
}

func TestRelaunchForStartupSettingsChange_ForkSessionHandling(t *testing.T) {
	t.Parallel()
	for _, confirmed := range []bool{true, false} {
		name := "confirmed session switches to resume and clears fork options"
		if !confirmed {
			name = "unconfirmed session preserves fork options"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			svc, _, _ := setupTestService(t)
			t.Cleanup(func() { drainAllInFlight(svc) })

			provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
			workingDir := t.TempDir()
			sourceDir := t.TempDir()
			const agentID = "fork-relaunch-agent"

			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID:                agentID,
				WorkingDir:        workingDir,
				Title:             "Fork Relaunch",
				AgentProvider:     provider,
				Resumed:           1,
				ForkedFromAgentID: "orig-agent",
			}))
			if confirmed {
				require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
					ID: agentID, AgentSessionID: "confirmed-fork-session",
				}))
			}
			fallback, err := svc.Queries.GetAgentByID(ctx, agentID)
			require.NoError(t, err)

			restartCalls := 0
			var restarted agent.Options
			svc.startAgentFn = mockAgentStarter(t, svc, func(opts agent.Options) {
				restartCalls++
				restarted = opts
			})
			t.Cleanup(func() { svc.Agents.StopAgent(agentID) })

			relaunchOpts := svc.baseAgentOptions(agentID, workingDir, provider)
			relaunchOpts.ForkFromSessionID = "source-session-id"
			relaunchOpts.ForkSourceWorkingDir = sourceDir

			_, running := svc.relaunchForStartupSettingsChange(agentID, provider, relaunchOpts, fallback)
			require.True(t, running)
			require.Equal(t, 1, restartCalls)

			if confirmed {
				assert.Equal(t, "confirmed-fork-session", restarted.ResumeSessionID)
				assert.Empty(t, restarted.ForkFromSessionID)
				assert.Empty(t, restarted.ForkSourceWorkingDir)
			} else {
				assert.Empty(t, restarted.ResumeSessionID)
				assert.Equal(t, "source-session-id", restarted.ForkFromSessionID)
				assert.Equal(t, sourceDir, restarted.ForkSourceWorkingDir)
			}
		})
	}
}

func TestWorktreeCreationAutoExcludesLeapMuxTmp(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("CreateGitWorktree RPC", func(t *testing.T) {
		t.Parallel()
		svc, d, _ := setupTestService(t)
		repo := testutil.NewGitRepo(t)
		svc.HomeDir = filepath.Dir(repo)
		wtDir := filepath.Join(t.TempDir(), "wt-rpc")

		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
			Path:         repo,
			Branch:       "feat-tmp-exclude-rpc",
			WorktreePath: wtDir,
		}, w)
		require.Empty(t, w.errors, "unexpected CreateGitWorktree errors: %+v", w.errors)

		excludePath, ok := repopaths.GitInfoExcludePath(wtDir)
		require.True(t, ok)
		raw, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Contains(t, string(raw), ".leapmux/tmp/")

		// Verify git actually ignores files inside <worktree>/.leapmux/tmp/.
		tmpDir := filepath.Join(wtDir, ".leapmux", "tmp")
		require.NoError(t, os.MkdirAll(tmpDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "scratch.txt"), []byte("scratch\n"), 0o644))
		status, err := gitutil.Output(ctx, wtDir, "status", "--porcelain")
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(status))
	})

	t.Run("executeGitMode create worktree", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		repo := testutil.NewGitRepo(t)
		wtDir := filepath.Join(t.TempDir(), "wt-gitmode")

		plan, err := svc.validateCreateWorktree(ctx, createWorktreeValidation{
			workingDir:   repo,
			branch:       "feat-tmp-exclude-mode",
			explicitPath: wtDir,
		})
		require.NoError(t, err)

		res, err := svc.executeGitMode(ctx, plan)
		require.NoError(t, err)
		assert.Equal(t, wtDir, res.WorkingDir)

		excludePath, ok := repopaths.GitInfoExcludePath(wtDir)
		require.True(t, ok)
		raw, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Contains(t, string(raw), ".leapmux/tmp/")
	})
}
