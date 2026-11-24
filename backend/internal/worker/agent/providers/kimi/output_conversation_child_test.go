package kimi

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestKimiConversationChildAdoptionSubscribesAndRetainsItsNativePrompt(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(subscription fakeKapSubscribe) {
		if slices.Contains(subscription.AgentIDs, "agent-0") {
			<-released
		}
	}
	rig.fake.mu.Unlock()
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Child assignment"}`))
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	run := rig.agent.run("agent-0")
	require.NotNil(t, run.conversationRecovery.Load(), "child admission must wait for its exact native conversation subscription")
	waitFor(t, func() bool {
		for _, subscription := range rig.fake.subscribeFrames() {
			if len(subscription.AgentIDs) == 1 && subscription.AgentIDs[0] == "agent-0" {
				return true
			}
		}
		return false
	}, "the native child receives an explicit agent_ids subscription")
	snapshot := map[string]any{"type": "turns", "session_id": "session_1", "agent_id": "agent-0", "event_created_at": "2026-11-18T00:00:00Z", "has_more": false,
		"turns": []any{map[string]any{"turn_id": "child-turn", "ordinal": 0, "kind": "agent", "status": "ended", "outcome": "completed", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}, "items": []any{
			map[string]any{"type": "user_message", "item_id": "child-prompt", "status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z", "prompt_ids": []string{}, "placement": "opener", "origin": map[string]any{"kind": "system"}, "text": "Read the child file.", "attachments": []any{}, "skills": []any{}},
			map[string]any{"type": "assistant_message", "item_id": "child-answer", "status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z", "text": "Child answer"},
		}}}}
	rig.fake.mu.Lock()
	rig.fake.sessions["session_1"].ReplayFrames = []any{snapshot}
	rig.fake.mu.Unlock()
	release()
	childSink := rig.sink.Child(child.childAgentID)
	waitFor(t, func() bool { return len(childSink.Messages()) == 3 }, "the subscribed child page retains its prompt, answer and divider")
	require.NoError(t, agent.FlushPendingOutput(run.childLifetime.writer))
	require.Len(t, childSink.Messages(), 3, "the native child prompt, answer and divider persist")
	rows := childSink.Messages()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	prompt, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, "Read the child file.", *prompt.item.Text)
	answer, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	assert.Equal(t, "Child answer", *answer.item.Text)
	assert.True(t, rows[2].TurnEnd)
}

func TestKimiConversationChildSubscriptionReceivesItsOwnInitialPage(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	main := rig.agent.run(kimiMainAgentID)
	waitFor(t, func() bool { return main.conversationRecovery.Load() == nil }, "main's initial conversation page completes before the child-only subscription")
	before := rig.sink.TurnLifecycle()
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	run := rig.agent.run("agent-0")
	waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the child subscription receives its own initial native page")
	assert.Equal(t, before, rig.sink.TurnLifecycle(), "a child-only subscription cannot republish main's state")
}

func TestKimiConversationChildTurnSettlesItsOwnLifetime(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"user", "system"} {
		for _, outcome := range []string{"completed", "failed", "interrupted"} {
			for _, snapshot := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/snapshot=%t", origin, outcome, snapshot), func(t *testing.T) {
					t.Parallel()
					rig := newKimiConversationRig(t)
					trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
					rig.agent.sink = trace
					rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
					child, linked := rig.agent.children.get("agent-0")
					require.True(t, linked)
					frame := func(raw string) {
						rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
					}
					prompt := `{"type":"user_message","item_id":"prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"` + origin + `"},"text":"Review it.","attachments":[],"skills":[]}`
					turn := `{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"ended","outcome":"` + outcome + `","started_at":"2026-11-18T00:00:00Z","steps":[]}`
					if snapshot {
						var header map[string]any
						require.NoError(t, json.Unmarshal([]byte(turn), &header))
						header["items"] = []json.RawMessage{json.RawMessage(prompt)}
						page, err := json.Marshal(map[string]any{"type": "turns", "turns": []any{header}, "has_more": false})
						require.NoError(t, err)
						frame(string(page))
					} else {
						frame(`{"type":"turn.started","turn":{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
						row, _ := rig.sink.BackgroundTask(child.rowKey)
						assert.Equal(t, bgtask.StatusRunning, row.Status, "the native child turn revives its activity row")
						frame(`{"type":"item.ended","turn_id":"child-turn","item":` + prompt + `}`)
						frame(`{"type":"turn.ended","turn":` + turn + `}`)
					}
					run := rig.agent.run("agent-0")
					lifetime := run.childLifetime
					require.NoError(t, agent.FlushPendingOutput(lifetime.writer))
					row, _ := rig.sink.BackgroundTask(child.rowKey)
					expected := bgtask.StatusSucceeded
					switch outcome {
					case "interrupted":
						expected = bgtask.StatusStopped
					case "failed":
						expected = bgtask.StatusFailed
					case "completed":
						if origin != "user" {
							expected = bgtask.StatusEndedWithUnknownOutcome
						}
					}
					assert.Equal(t, expected, row.Status)
					task, exists := rig.sink.BackgroundTask(kimiTaskRowKey(rig.sessionID(), "main", "task-child"))
					require.True(t, exists)
					assert.Equal(t, bgtask.StatusRunning, task.Status, "a child turn cannot infer its coordinator task outcome")
					trace.mu.Lock()
					cleanupCount := 0
					for _, event := range trace.events {
						if event.kind == "cleanup" && event.childID == child.childAgentID {
							cleanupCount++
							assert.True(t, event.registryFinal, "registry storage must precede physical cleanup")
						}
					}
					trace.mu.Unlock()
					assert.Equal(t, 1, cleanupCount)
					assert.True(t, lifetime.retiring)
					frame(`{"type":"turn.ended","turn":` + turn + `}`)
					assert.Same(t, lifetime, run.childLifetime, "a repeated completed turn cannot acquire another lifetime")
					assert.False(t, run.turnActive)
				})
			}
		}
	}
}

func TestKimiChildReplacementPublishesIdleBeforeCleanup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, outcome    string
		coordinatorEnded bool
	}{
		{name: "completed user turn", outcome: "completed"},
		{name: "stopped coordinator task", outcome: "interrupted", coordinatorEnded: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
			rig.agent.sink = trace
			const childAgent = "agent-0"
			childID := adoptNativeKimiChild(t, rig.agent, childAgent)
			rig.startPublicTurn(t, childAgent, "child-turn", 0)
			var items string
			if scenario.coordinatorEnded {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-agent-0","kind":"subagent","status":"ended","outcome":"killed","detached":false,"child_agent_id":"agent-0"}`))
			} else {
				items = kimiPublicPromptItem(t, "child-prompt", "opener", "child-prompt")
				rig.conversationAgentFrame(t, childAgent, `{"type":"item.ended","turn_id":"child-turn","item":`+items+`}`)
			}
			require.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			lifetime := rig.agent.run(childAgent).childLifetime
			require.NoError(t, agent.FlushPendingOutput(lifetime.writer))
			trace.mu.Lock()
			trace.events = nil
			trace.mu.Unlock()
			page := fmt.Sprintf(`{"type":"turns","has_more":false,"turns":[{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"ended","outcome":%q,"started_at":"2026-11-18T00:00:00Z","steps":[],"items":[%s]}]}`, scenario.outcome, items)
			rig.conversationAgentFrame(t, childAgent, page)
			require.NoError(t, agent.FlushPendingOutput(lifetime.writer))
			trace.mu.Lock()
			events := slices.Clone(trace.events)
			trace.mu.Unlock()
			var eventsForChild []string
			for _, event := range events {
				if event.childID != childID {
					continue
				}
				switch event.kind {
				case "state":
					assert.False(t, event.active)
					eventsForChild = append(eventsForChild, "idle")
				case "cleanup":
					assert.True(t, event.registryFinal)
					eventsForChild = append(eventsForChild, "cleanup")
				case "acquire":
					eventsForChild = append(eventsForChild, "acquire")
				}
			}
			assert.Equal(t, []string{"idle", "cleanup"}, eventsForChild)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
		})
	}
}

func TestKimiConversationChildRenewalPrecedesActivityAndKeepsNewerTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
	rig.agent.sink = trace
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	frame := func(raw string) {
		rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
	}
	frame(`{"type":"turn.started","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	frame(`{"type":"turn.ended","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	run := rig.agent.run("agent-0")
	previous := run.childLifetime
	require.True(t, previous.retiring)
	trace.mu.Lock()
	trace.events = nil
	trace.mu.Unlock()
	frame(`{"type":"turn.started","turn":{"turn_id":"new","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
	require.NotSame(t, previous, run.childLifetime)
	trace.mu.Lock()
	events := append([]kimiChildResetLifecycleEvent(nil), trace.events...)
	trace.mu.Unlock()
	acquired, active := -1, -1
	for index, event := range events {
		if event.childID == child.childAgentID {
			if event.kind == "acquire" {
				acquired = index
			}
			if event.kind == "state" && event.active {
				active = index
			}
		}
	}
	require.NotEqual(t, -1, acquired)
	require.NotEqual(t, -1, active)
	assert.Less(t, acquired, active, "a resumed child must own a current writer before publishing activity")
	current := run.childLifetime
	frame(`{"type":"turn.ended","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	assert.Same(t, current, run.childLifetime)
	assert.False(t, current.retiring)
	assert.True(t, run.turnActive)
	row, _ := rig.sink.BackgroundTask(child.rowKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	frame(`{"type":"item.ended","turn_id":"new","item":{"type":"assistant_message","item_id":"new-answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Resumed answer"}}`)
	require.NoError(t, agent.FlushPendingOutput(current.writer))
	messages := rig.sink.Child(child.childAgentID).Messages()
	require.NotEmpty(t, messages)
	assert.Contains(t, string(messages[len(messages)-1].Content), "Resumed answer")
	assert.False(t, messages[len(messages)-1].TranscriptOnly)
}

func TestKimiUnlinkedConversationCannotAllocateAChildRun(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	const nativeChild = "agent-0"
	const nested = `{"type":"tool_call","item_id":"inner","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","input":{"description":"Inner","prompt":"Review the inner change."},"agent_refs":[{"agent_id":"agent-1","role":"child"}]}`
	const prompt = `{"type":"user_message","item_id":"assignment","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"system"},"text":"Review the change.","attachments":[],"skills":[]}`
	const page = `{"type":"turns","has_more":false,"turns":[{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` + prompt + `,` + nested + `]}]}`
	rig.conversationAgentFrame(t, nativeChild, page)
	rig.agent.Mu.Lock()
	unlinkedRun := rig.agent.runs[nativeChild]
	rig.agent.Mu.Unlock()
	assert.Nil(t, unlinkedRun, "an agent that has no adopted transcript cannot retain a conversation")
	assert.Empty(t, rig.sink.ChildAgentIDs())

	rig.startPublicTurn(t, kimiMainAgentID, "parent-turn", 0)
	const opening = `{"type":"item.started","turn_id":"parent-turn","item":{"type":"tool_call","item_id":"outer","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","input":{"description":"Reviewer","prompt":"Review the change."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`
	rig.conversationFrame(t, opening)
	rig.conversationFrame(t, opening)
	activity, childSink := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, "Reviewer", activity.Title)
	assert.Empty(t, childSink.Messages(), "the parent call cannot supply the native child prompt")
	for range 2 {
		rig.conversationAgentFrame(t, nativeChild, page)
	}
	run := rig.agent.run(nativeChild)
	require.NoError(t, agent.FlushPendingOutput(run.childLifetime.writer))
	rows := childSink.Messages()
	require.Len(t, rows, 2, "the owned initial page retains one assignment and its nested tool request")
	assignment, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, assignment.item)
	require.NotNil(t, assignment.item.Text)
	assert.Equal(t, "Review the change.", *assignment.item.Text)
	assert.Equal(t, nativeChild, assignment.record.AgentID)
	assert.True(t, rig.agent.ActiveChildTurnState(activity.RowKey).Active)
	inner, exists := childSink.BackgroundTask("session_1/agent-1")
	require.True(t, exists)
	assert.NotEqual(t, activity.ChildAgentID, inner.ChildAgentID)
	assert.Equal(t, nativeChild, rig.agent.run("agent-1").childParentAgentID)
}

func TestKimiConversationChildOlderOrEndedTurnDoesNotReviveFinishedRow(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	frame := func(raw string) {
		rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
	}
	userPrompt := `{"type":"user_message","item_id":"p1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","prompt_ids":[],"placement":"opener","origin":{"kind":"user"},"text":"Run check.","attachments":[],"skills":[]}`
	frame(`{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
	frame(`{"type":"item.ended","turn_id":"t1","item":` + userPrompt + `}`)
	frame(`{"type":"turn.ended","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	row, ok := rig.sink.BackgroundTask(child.rowKey)
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)
	beforeRevives := len(rig.sink.RevivedTasks())

	// An unseen older-ordinal running turn must not revive the finished child row.
	frame(`{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Len(t, rig.sink.RevivedTasks(), beforeRevives, "older-ordinal child turn must not call ReviveBackgroundTask")
	row, _ = rig.sink.BackgroundTask(child.rowKey)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// An unseen already-ended newer turn must not revive the finished child row to Running.
	frame(`{"type":"turn.ended","turn":{"turn_id":"t2","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:02Z","steps":[]}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Len(t, rig.sink.RevivedTasks(), beforeRevives, "already-ended child turn must not call ReviveBackgroundTask")
	row, _ = rig.sink.BackgroundTask(child.rowKey)
	assert.True(t, row.Status.IsFinished(), "finished child row must stay finished")
}

func TestKimiConversationChildWithdrawnTurnClosesRowAndQueuesCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
	}{
		{name: "turns omission", frame: `{"type":"turns","has_more":false,"turns":[]}`},
		{name: "turns.removed", frame: `{"type":"turns.removed","turn_ids":["child-turn"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
			rig.agent.sink = trace
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			frame := func(raw string) {
				rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
			}
			userPrompt := `{"type":"user_message","item_id":"p0","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"user"},"text":"Check it.","attachments":[],"skills":[]}`
			frame(`{"type":"turn.started","turn":{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			frame(`{"type":"item.ended","turn_id":"child-turn","item":` + userPrompt + `}`)
			run := rig.agent.run("agent-0")
			lifetime := run.childLifetime
			require.NotNil(t, lifetime)
			rig.agent.Mu.Lock()
			require.True(t, run.userTurn)
			rig.agent.Mu.Unlock()

			frame(tc.frame)
			awaitKimiTestOutput(t, rig.agent.sink)
			require.NoError(t, agent.FlushPendingOutput(lifetime.writer))

			rig.agent.Mu.Lock()
			userTurn := run.userTurn
			turnActive := run.turnActive
			rig.agent.Mu.Unlock()
			assert.False(t, userTurn, "withdrawing the active turn must clear run.userTurn")
			assert.False(t, turnActive)
			assert.False(t, rig.agent.ActiveChildTurnState(child.rowKey).Active)
			row, ok := rig.sink.BackgroundTask(child.rowKey)
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
			assert.True(t, lifetime.retiring)
			trace.mu.Lock()
			cleanupCount := 0
			for _, event := range trace.events {
				if event.kind == "cleanup" && event.childID == child.childAgentID {
					cleanupCount++
					assert.True(t, event.registryFinal, "registry close must precede child cleanup")
				}
			}
			trace.mu.Unlock()
			assert.Equal(t, 1, cleanupCount)
		})
	}

	t.Run("turns advancing to a newer running turn keeps row running", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
		child, linked := rig.agent.children.get("agent-0")
		require.True(t, linked)
		frame := func(raw string) {
			rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
		}
		frame(`{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		frame(`{"type":"turns","has_more":false,"turns":[{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[],"items":[]}]}`)
		awaitKimiTestOutput(t, rig.agent.sink)

		row, ok := rig.sink.BackgroundTask(child.rowKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.True(t, rig.agent.ActiveChildTurnState(child.rowKey).Active)
		assert.False(t, rig.agent.run("agent-0").childLifetime.retiring)
	})
}
