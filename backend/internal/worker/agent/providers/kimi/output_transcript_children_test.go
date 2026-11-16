package kimi

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

type kimiChildResetLifecycleEvent struct {
	kind, childID string
	active        bool
	registryFinal bool
}

type kimiChildResetLifecycleSink struct {
	agent.ProviderServices
	registry *agenttest.ControlSink
	mu       sync.Mutex
	events   []kimiChildResetLifecycleEvent
}

func (s *kimiChildResetLifecycleSink) record(event kimiChildResetLifecycleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *kimiChildResetLifecycleSink) ChildSink(childID string) agent.ProviderServices {
	s.record(kimiChildResetLifecycleEvent{kind: "acquire", childID: childID})
	return &kimiChildResetPublicationSink{ProviderServices: s.ProviderServices.ChildSink(childID), owner: s, childID: childID}
}

func (s *kimiChildResetLifecycleSink) AcquireChildSink(childID string) agent.ChildSinkLease {
	return s.wrapChildLease(childID, s.ProviderServices.AcquireChildSink(childID))
}

func (s *kimiChildResetLifecycleSink) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return s.wrapChildLease(childID, s.ProviderServices.AcquireChildStateSink(childID))
}

func (s *kimiChildResetLifecycleSink) wrapChildLease(childID string, lease agent.ChildSinkLease) agent.ChildSinkLease {
	s.record(kimiChildResetLifecycleEvent{kind: "acquire", childID: childID})
	direct, valid := agent.DirectTranscript(lease.Sink).(agent.ProviderServices)
	if !valid {
		return agent.ChildSinkLease{}
	}
	lease.Sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(lease.Sink), &kimiChildResetPublicationSink{
		ProviderServices: direct, owner: s, childID: childID,
	})
	cleanup := lease.Cleanup
	lease.Cleanup = func() bool {
		final := false
		if s.registry != nil {
			for _, row := range s.registry.BackgroundTasks() {
				if row.ChildAgentID == childID && row.Role.IsCanonicalChildActivity() {
					final = row.Status.IsFinished()
					break
				}
			}
		}
		if cleanup == nil || !cleanup() {
			return false
		}
		s.record(kimiChildResetLifecycleEvent{kind: "cleanup", childID: childID, registryFinal: final})
		return true
	}
	return lease
}

func (s *kimiChildResetLifecycleSink) CleanupChildAgent(childID string) {
	final := false
	if s.registry != nil {
		for _, row := range s.registry.BackgroundTasks() {
			if row.ChildAgentID == childID && row.Role.IsCanonicalChildActivity() {
				final = row.Status.IsFinished()
				break
			}
		}
	}
	s.record(kimiChildResetLifecycleEvent{kind: "cleanup", childID: childID, registryFinal: final})
	s.ProviderServices.CleanupChildAgent(childID)
}

type kimiChildResetPublicationSink struct {
	agent.ProviderServices
	owner   *kimiChildResetLifecycleSink
	childID string
}

func (s *kimiChildResetPublicationSink) SetTurnState(state agent.TurnState, sequence uint64) {
	s.owner.record(kimiChildResetLifecycleEvent{kind: "state", childID: s.childID, active: state.Active})
	s.ProviderServices.SetTurnState(state, sequence)
}

func TestKimiCompletedChildResetPublishesBeforeFinalCleanup(t *testing.T) {
	t.Parallel()
	for _, endedTask := range []bool{false, true} {
		t.Run(map[bool]string{false: "independent user turn", true: "ended native task"}[endedTask], func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
			rig.agent.sink = trace
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			native.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer","prompt":"Review it."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			origin, state := "user", "completed"
			if endedTask {
				origin, state = "other", "cancelled"
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			}
			native.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"`+origin+`"},"prompt":"Review it."}}`)
			if endedTask {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"killed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			}
			trace.mu.Lock()
			trace.events = nil
			trace.mu.Unlock()
			rig.agent.Mu.Lock()
			endedAt := rig.agent.attachedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
			rig.agent.Mu.Unlock()
			turn := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": state,
				"origin": map[string]any{"kind": origin}, "prompt": "Review it.", "endedAt": endedAt, "steps": []any{}}
			rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "agent-0", 2, turn))
			row, _ := childOf(t, rig.sink, "session_1/agent-0")
			trace.mu.Lock()
			events := append([]kimiChildResetLifecycleEvent(nil), trace.events...)
			trace.mu.Unlock()
			idle, cleanup, idleCount, cleanupCount := -1, -1, 0, 0
			for index, event := range events {
				if event.childID != row.ChildAgentID {
					continue
				}
				if event.kind == "cleanup" {
					assert.True(t, event.registryFinal, "the stored registry close must precede activity cleanup")
					cleanup, cleanupCount = index, cleanupCount+1
				}
				if event.kind == "state" && !event.active {
					idle, idleCount = index, idleCount+1
				}
			}
			require.Equal(t, 1, idleCount)
			require.Equal(t, 1, cleanupCount)
			assert.Less(t, idle, cleanup, "the reset publishes inactivity before reclaiming child state")
			for _, event := range events[cleanup+1:] {
				assert.False(t, event.kind == "acquire" && event.childID == row.ChildAgentID, "the reset must not recreate the cleaned child sink")
			}
		})
	}
}

func TestKimiNativeTranscriptPaginationKeepsLatestWatermark(t *testing.T) {
	t.Parallel()
	a := newOfflineKimiAgent(t, &agenttest.Sink{})
	var calls atomic.Int64
	a.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/sessions/session_1/transcript", r.URL.Path)
		assert.Equal(t, "agent+child", r.URL.Query().Get("agent_id"))
		assert.Equal(t, "100", r.URL.Query().Get("page_size"))
		var data any
		switch calls.Add(1) {
		case 1:
			assert.Empty(t, r.URL.Query().Get("before_turn"))
			assert.NoError(t, json.Unmarshal([]byte(`{"agent_id":"agent+child","items":[{"kind":"turn","turnId":"t2"}],"has_more":true,"seq":9}`), &data))
		case 2:
			assert.Equal(t, "t2", r.URL.Query().Get("before_turn"))
			assert.NoError(t, json.Unmarshal([]byte(`{"agent_id":"agent+child","items":[{"kind":"turn","turnId":"t1"}],"has_more":true,"seq":11}`), &data))
		default:
			assert.Equal(t, "t1", r.URL.Query().Get("before_turn"))
			assert.NoError(t, json.Unmarshal([]byte(`{"agent_id":"agent+child","items":[{"kind":"turn","turnId":"t0"}],"has_more":false,"seq":12}`), &data))
		}
		writeFakeKapEnvelope(w, http.StatusOK, 0, "success", data)
	})
	snapshot, err := a.readNativeTranscript(t.Context(), "session_1", "agent+child")
	require.NoError(t, err)
	require.NotNil(t, snapshot.Seq)
	assert.EqualValues(t, 9, *snapshot.Seq)
	assert.EqualValues(t, 3, calls.Load())
	var ids []string
	for _, raw := range snapshot.Items {
		var turn struct {
			ID string `json:"turnId"`
		}
		require.NoError(t, json.Unmarshal(raw, &turn))
		ids = append(ids, turn.ID)
	}
	assert.Equal(t, []string{"t0", "t1", "t2"}, ids)
}

func TestKimiNativeChildRecoveryRetriesAfterReadFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	run := rig.agent.run("agent-0")
	clock := testutil.NewQuartzMock(t)
	rig.agent.clock = clock
	ctx := testutil.DeadlineContext(t)
	armed := clock.Trap().NewTimer(kimiTranscriptRecoveryTimerTag)
	defer armed.Close()
	var snapshot any
	assert.NoError(t, json.Unmarshal([]byte(`{"agent_id":"agent-0","items":[],"has_more":false,"seq":3}`), &snapshot))
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/transcript"),
		fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "history unavailable"}, fakeKapReply{Data: snapshot})
	rig.agent.dispatchMu.Lock()
	rig.agent.requireNativeTranscriptRecovery(run)
	rig.agent.dispatchMu.Unlock()
	delay := testutil.WaitForTimer(t, ctx, armed)
	assert.True(t, run.pendingNativeRecovery)
	native.transcriptAgentOps(t, "agent-0", 4, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.read","toolCallId":"read","name":"Read","state":"done","input":{"path":"child.go"},"output":"Output after recovery"}}`)
	clock.Advance(delay).MustWait(ctx)
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the retry restores the child transcript")
	assert.False(t, run.pendingNativeRecovery)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	messages := child.Messages()
	require.Equal(t, 2, len(messages))
	assert.Contains(t, string(messages[1].Content), "Output after recovery")
	assert.Len(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/transcript")), 2)
}

func TestKimiNativeTranscriptRejectsInvalidPages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, first, second string }{
		{name: "another agent", first: `{"agent_id":"other","items":[],"seq":1}`},
		{name: "missing sequence", first: `{"agent_id":"agent-0","items":[]}`},
		{name: "negative sequence", first: `{"agent_id":"agent-0","items":[],"seq":-1}`},
		{name: "missing items", first: `{"agent_id":"agent-0","seq":1}`},
		{name: "no pagination cursor", first: `{"agent_id":"agent-0","items":[],"has_more":true,"seq":1}`},
		{name: "another page owner", first: `{"agent_id":"agent-0","items":[{"kind":"turn","turnId":"t1"}],"has_more":true,"seq":2}`, second: `{"agent_id":"other","items":[],"has_more":false,"seq":2}`},
		{name: "repeated final page", first: `{"agent_id":"agent-0","items":[{"kind":"turn","turnId":"t1"}],"has_more":true,"seq":2}`, second: `{"agent_id":"agent-0","items":[{"kind":"turn","turnId":"t1"}],"has_more":false,"seq":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineKimiAgent(t, &agenttest.Sink{})
			var calls atomic.Int64
			a.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				body := tc.first
				if calls.Add(1) > 1 {
					body = tc.second
				}
				var data any
				assert.NoError(t, json.Unmarshal([]byte(body), &data))
				writeFakeKapEnvelope(w, http.StatusOK, 0, "success", data)
			})
			_, err := a.readNativeTranscript(t.Context(), "session_1", "agent-0")
			require.Error(t, err)
		})
	}
}
