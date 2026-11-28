package kimi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeCompactingStateControlsExactAgentInput(t *testing.T) {
	t.Parallel()
	for _, agentID := range []string{kimiMainAgentID, "agent-0"} {
		t.Run(agentID, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			if agentID != kimiMainAgentID {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
				run := rig.agent.run(agentID)
				waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the native child initial page completes before compaction")
			}
			state := func(compacting bool) {
				rig.agent.HandleOutput(kimiCompactingStateFrame(t, agentID, compacting))
			}
			active := func() agent.TurnState {
				if agentID == kimiMainAgentID {
					return rig.agent.PublishTurnActive()
				}
				return rig.agent.ActiveChildTurnState("session_1/" + agentID)
			}
			send := func() error {
				if agentID == kimiMainAgentID {
					_, err := rig.agent.SendInput("After compaction", nil)
					return err
				}
				_, err := rig.agent.SendChildInput("session_1/"+agentID, agent.ChildInput{Content: "After compaction"})
				return err
			}
			state(true)
			assert.Equal(t, agent.TurnState{Active: true}, active(), "native compaction holds only that agent's input and refuses steering")
			require.ErrorIs(t, send(), agent.ErrAgentBusy)
			assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")))
			state(false)
			assert.Equal(t, agent.TurnState{}, active())
			require.NoError(t, send())
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")), 1)
		})
	}
}

func TestKimiNativeCompactingChildRetainsItsLifetimeAfterModelCompletion(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	state := func(compacting bool) {
		rig.agent.HandleOutput(kimiCompactingStateFrame(t, "agent-0", compacting))
	}
	frame := func(raw string) {
		rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
	}
	frame(`{"type":"turn.started","turn":{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	run := rig.agent.run("agent-0")
	lifetime := run.childLifetime
	state(true)
	frame(`{"type":"turn.ended","turn":{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.False(t, lifetime.retiring, "model completion cannot retire the still-compacting native child")
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	assert.False(t, rig.agent.PublishTurnActive().Active, "the child's compaction never occupies main")
	state(false)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Same(t, lifetime, run.childLifetime)
	assert.True(t, lifetime.retiring)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiProcessExitClearsNativeCompactionForEachAgent(t *testing.T) {
	t.Parallel()
	for _, agentID := range []string{kimiMainAgentID, "agent-0"} {
		t.Run(agentID, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			if agentID != kimiMainAgentID {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			}
			rig.agent.HandleOutput(kimiCompactingStateFrame(t, agentID, true))
			rig.agent.SimulateExitForTest()
			require.NoError(t, rig.agent.Wait())
			rig.agent.Mu.Lock()
			compacting := rig.agent.runs[agentID].compacting
			rig.agent.Mu.Unlock()
			assert.False(t, compacting, "an exited process cannot retain native compaction state")
			if agentID != kimiMainAgentID {
				child, linked := rig.agent.children.get(agentID)
				require.True(t, linked)
				active, published := rig.sink.Child(child.childAgentID).LastTurnActive()
				assert.True(t, published)
				assert.False(t, active, "a child that was only compacting still publishes exit readiness")
			}
		})
	}
}

func kimiCompactingStateFrame(t *testing.T, agentID string, compacting bool) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "agent", "session_id": "session_1", "agent_id": agentID, "event_created_at": "2026-11-18T00:00:00Z", "status": "idle", "compacting": compacting,
		"model": "kimi-k2", "thinking": "", "permission_mode": "manual", "plan_mode": false, "swarm_mode": false, "tower_mode": false})
	require.NoError(t, err)
	return raw
}

const kimiPublicCompactionFixture = `{"type":"system","kind":"compaction","item_id":"compact","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","trigger":"manual","tokens_before":9000,"tokens_after":1000,"compacted_count":3,"summary":"Keep the task state."}`

const kimiPublicCompactionFixtureSnapshot = `{"type":"turns","has_more":false,"turns":[{"turn_id":"context","ordinal":0,"kind":"system","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` + kimiPublicCompactionFixture + `]}]}`

func TestKimiPublicCompactionHistoryDoesNotOwnLiveAdmission(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("compacting=%t", live), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			if live {
				rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, true))
				require.True(t, rig.agent.PublishTurnActive().Active)
				assert.Empty(t, rig.sink.Messages(), "the live indicator has no transcript row")
				rig.conversationFrame(t, `{"type":"turns","turns":[],"has_more":false}`)
				assert.True(t, rig.agent.PublishTurnActive().Active, "an empty history page cannot release live compaction")
			}
			for range 2 {
				rig.conversationFrame(t, kimiPublicCompactionFixtureSnapshot)
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			assert.Equal(t, live, rig.agent.PublishTurnActive().Active)
			rows := rig.sink.Messages()
			require.Len(t, rows, 2, "the system turn and its one compaction item survive replay")
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, "compact", view.item.ItemID)
			assert.Equal(t, "manual", view.item.Trigger)
			require.NotNil(t, view.item.Summary)
			assert.Equal(t, "Keep the task state.", *view.item.Summary)
			assert.Equal(t, agent.CompactionBoundaryNotification(), (kimiProvider{}).Classify(rows[0].Content))
			assert.True(t, rows[1].TurnEnd)
			if live {
				rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, false))
				assert.False(t, rig.agent.PublishTurnActive().Active)
				info, found := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
				require.True(t, found)
				assert.Nil(t, info)
			}
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"next","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
			rig.conversationFrame(t, `{"type":"item.ended","turn_id":"context","item":`+kimiPublicCompactionFixture+`}`)
			assert.True(t, rig.agent.PublishTurnActive().Active, "a repeated old result cannot release the next model turn")
		})
	}
}

func TestKimiPublicAutoCompactionDoesNotEndTheModelTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, true))
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+strings.Replace(kimiPublicCompactionFixture, `"trigger":"manual"`, `"trigger":"auto"`, 1)+`}`)
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, false))
	assert.True(t, rig.agent.PublishTurnActive().Active, "the separate model turn still owns the queue")
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	assert.False(t, rows[0].TurnEnd)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	assert.False(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiPublicCompactionCancellationNoticeDoesNotEndLiveActivity(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, true))
	rig.conversationFrame(t, `{"type":"notice","level":"warning","title":"Context compaction did not finish","code":"compaction.cancelled"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.True(t, rig.agent.PublishTurnActive().Active)
	assert.Empty(t, rig.sink.Messages())
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, false))
	assert.False(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiPublicCompactionUsesTheChildScope(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	childID := adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, "agent-0", true))
	rig.conversationAgentFrame(t, "agent-0", kimiPublicCompactionFixtureSnapshot)
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, "agent-0", false))
	awaitKimiTestOutput(t, rig.agent.sink)
	child := rig.sink.Child(childID)
	assert.Equal(t, []map[string]any{
		{contracts.SessionInfoKeyCompaction: map[string]any{}},
		{contracts.SessionInfoKeyCompaction: nil},
	}, child.SessionInfos())
	rows := child.Messages()
	require.Len(t, rows, 2)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, "agent-0", view.record.AgentID)
	assert.Equal(t, agent.CompactionBoundaryNotification(), (kimiProvider{}).Classify(rows[0].Content))
	assert.Empty(t, rig.sink.SessionInfos())
	assert.Empty(t, rig.sink.Messages())
}

func TestKimiObsoleteMarkersCannotControlAdmissionOrPublishNotices(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[{"op":"marker.upsert","item":{"kind":"marker","markerId":"obsolete-compaction","marker":"compaction","payload":{"phase":"started","trigger":"manual"}}}]}}`))
	rig.agent.HandleOutput([]byte(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":2,"ops":[{"op":"marker.upsert","item":{"kind":"marker","markerId":"obsolete-warning","marker":"notice","payload":{"level":"warning","message":"An obsolete warning"}}}]}}`))
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.False(t, rig.agent.PublishTurnActive().Active)
	assert.Empty(t, rig.sink.SessionInfos())
	assert.Empty(t, rig.sink.PersistedNotifications())
}
