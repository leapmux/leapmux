package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiConversationRig(t *testing.T) *kimiOutputRig {
	t.Helper()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID(rig.sessionID())
	return rig
}

func (r *kimiOutputRig) conversationFrame(t *testing.T, raw string) {
	t.Helper()
	r.conversationAgentFrame(t, kimiMainAgentID, raw)
}

func (r *kimiOutputRig) conversationAgentFrame(t *testing.T, agentID, raw string) {
	t.Helper()
	encoded := kimiConversationTestFrame(t, r.sessionID(), agentID, raw)
	var frame kimiFrame
	require.NoError(t, json.Unmarshal(encoded, &frame))
	require.Equal(t, r.sessionID(), frame.SessionID)
	require.True(t, isKimiConversationFrame(frame.Type), frame.Type)
	r.agent.HandleOutput(encoded)
}

func (r *kimiOutputRig) startPublicTurn(t *testing.T, agentID, turnID string, ordinal int) {
	t.Helper()
	r.conversationAgentFrame(t, agentID, fmt.Sprintf(`{"type":"turn.started","turn":{"turn_id":%q,"ordinal":%d,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`, turnID, ordinal))
}

func (r *kimiOutputRig) endPublicTurn(t *testing.T, agentID, turnID string, ordinal int, outcome string) {
	t.Helper()
	r.conversationAgentFrame(t, agentID, fmt.Sprintf(`{"type":"turn.ended","turn":{"turn_id":%q,"ordinal":%d,"kind":"agent","status":"ended","outcome":%q,"started_at":"2026-11-18T00:00:00Z","steps":[]}}`, turnID, ordinal, outcome))
}

func (r *kimiOutputRig) publicOpener(t *testing.T, agentID, turnID, origin string) {
	t.Helper()
	r.conversationAgentFrame(t, agentID, fmt.Sprintf(`{"type":"item.ended","turn_id":%q,"item":{"type":"user_message","item_id":"opener","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[%q],"placement":"opener","origin":%s,"text":"Read the workspace.","attachments":[],"skills":[]}}`, turnID, "prompt-"+turnID, origin))
}

func (r *kimiTestRig) startPublicMainTurn(t *testing.T, turnID string, ordinal int, origin string) {
	t.Helper()
	native := &kimiOutputRig{agent: r.agent, sink: r.sink}
	native.startPublicTurn(t, kimiMainAgentID, turnID, ordinal)
	if origin != "" {
		native.publicOpener(t, kimiMainAgentID, turnID, origin)
	}
}

func kimiConversationTestFrame(t *testing.T, sessionID, agentID, raw string) []byte {
	t.Helper()
	var frame map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &frame))
	frame["session_id"] = sessionID
	frame["agent_id"] = agentID
	if _, exists := frame["event_created_at"]; !exists {
		frame["event_created_at"] = "2026-11-18T00:00:00Z"
	}
	encoded, err := json.Marshal(frame)
	require.NoError(t, err)
	return encoded
}

func kimiConversationTestPage(t *testing.T, sessionID, agentID string, turns ...json.RawMessage) []byte {
	t.Helper()
	if turns == nil {
		turns = []json.RawMessage{}
	}
	raw, err := json.Marshal(map[string]any{"type": "turns", "turns": turns, "has_more": false})
	require.NoError(t, err)
	return kimiConversationTestFrame(t, sessionID, agentID, string(raw))
}

func TestKimiConversationCompletesAuthoritativeText(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turns","turns":[],"has_more":false}`)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"user_message","item_id":"prompt-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":["prompt-1"],"placement":"opener","origin":{"kind":"user"},"text":"Reply once.","attachments":[],"skills":[]}}`)
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"answer-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":""}}`)
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"text","delta":"A🙂"}`)
	assert.Empty(t, rig.sink.Messages(), "running text remains incomplete")
	completed := `{"type":"item.ended","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"answer-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"A🙂한!"}}`
	rig.conversationFrame(t, completed)
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, contracts.KimiConversationItemKindAssistantMessage, view.item.Type)
	assert.Equal(t, "A🙂한!", *view.item.Text)
	rig.conversationFrame(t, completed)
	assert.Len(t, rig.sink.Messages(), 1, "the same authoritative item cannot publish twice")
	ended := `{"type":"turn.ended","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","steps":[{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z"}]}}`
	rig.conversationFrame(t, ended)
	rig.conversationFrame(t, ended)
	rows = rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].TurnEnd)
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiConversationCorrectionCannotEndANewerTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"old","item":{"type":"assistant_message","item_id":"text:1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"First"}}`)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"new","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"old","item":{"type":"assistant_message","item_id":"text:1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Corrected"}}`)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	assert.True(t, rig.agent.PublishTurnActive().Active)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, "First", *view.item.Text, "the original is immutable")
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[0].SupplementalContent, &supplement))
	latest, err := decodeKimiConversationRecord(supplement[contracts.KimiConversationSupplementRoot])
	require.NoError(t, err)
	assert.Equal(t, "Corrected", *latest.item.Text)
	assert.Equal(t, int64(1), rows[1].SupplementalRevision)
}

func TestKimiConversationToolRequestWaitsForCompleteInputAndContainsNoResult(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","started_at":"2026-11-18T00:00:00Z","status":"running","created_at":"2026-11-18T00:00:00Z","name":"","stage":"input","input_text":"{\"command\":"}}`)
	assert.Empty(t, rig.sink.Messages())
	rig.conversationFrame(t, `{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"name":"Bash","stage":"awaiting_approval"}}`)
	assert.Empty(t, rig.sink.Messages(), "an approval stub without execution input is not a tool declaration")
	rig.conversationFrame(t, `{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"name":"Bash","stage":"executing","input":{"command":"printf once"}}}`)
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	request, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, contracts.KimiConversationRecordRoleRequest, request.record.Role)
	assert.JSONEq(t, `{"command":"printf once"}`, string(request.item.Input))
	assert.Empty(t, request.item.Output)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","started_at":"2026-11-18T00:00:00Z","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"printf once"},"output":"once"}}`)
	rows = rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, request.record.Item, json.RawMessage(extractKimiConversationTestItem(t, rows[0].Content)))
	result, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	assert.Equal(t, contracts.KimiConversationRecordRoleResult, result.record.Role)
	assert.JSONEq(t, `"once"`, string(result.item.Output))
	assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
	assert.True(t, rows[1].Closing)
}

func extractKimiConversationTestItem(t *testing.T, raw []byte) []byte {
	t.Helper()
	view, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	return view.record.Item
}

func TestKimiConversationToolBoundaryKeepsConcurrentTextOrderAndAuthoritativeCorrections(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"reasoning","item_id":"step:thought","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","text":"Thought prefix"}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Answer prefix"}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Read","stage":"executing","input":{"path":"file"}}}`)
	rows := rig.sink.Messages()
	require.Len(t, rows, 3)
	for index, expected := range []string{"step:thought", "step:answer", "call-1"} {
		view, err := decodeKimiConversationRecord(rows[index].Content)
		require.NoError(t, err)
		assert.Equal(t, expected, view.item.ItemID)
	}
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Answer final"}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"reasoning","item_id":"step:thought","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","text":"Thought final"}}`)
	rows = rig.sink.Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, int64(1), rows[0].SupplementalRevision)
	assert.Equal(t, int64(1), rows[1].SupplementalRevision)
}

func TestKimiConversationProgressRetainsInterruptedText(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Native partial answer"}}`)
	first := rig.sink.ProgressSnapshot().ThinkingTokens
	assert.Positive(t, first, "the first native item carries the first streamed chunk")
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"turn-1","item_id":"step:answer","field":"text","delta":" and more text"}`)
	assert.Greater(t, rig.sink.ProgressSnapshot().ThinkingTokens, first)
	assert.Empty(t, rig.sink.Messages())
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, "Native partial answer and more text", *view.item.Text)
	assert.Equal(t, contracts.KimiConversationStatusRunning, view.item.Status, "the completion column must not fabricate a native item completion")
}

func TestKimiConversationProgressCountsRepeatedOutputDeltasInBytes(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"executing","input":{"command":"run the probe"}}}`)
	for range 2 {
		rig.conversationFrame(t, `{"type":"item.delta","turn_id":"turn-1","item_id":"call-1","field":"output","delta":"native🙂"}`)
	}
	total, tail := lastKimiToolOutputProgress(rig)
	assert.Equal(t, int64(len("native🙂native🙂")), total.Value)
	assert.True(t, total.Exact)
	assert.Equal(t, "native🙂native🙂", tail.Text)
	assert.False(t, tail.Truncated)
}

func TestKimiConversationStepOutcomeCorrectsPartialTextButPreservesTextBeforeTools(t *testing.T) {
	t.Parallel()
	for _, toolStarted := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool_started=%t", toolStarted), func(t *testing.T) {
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"Native answer"}}`)
			rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Native answer"}}`)
			if toolStarted {
				rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"executing","input":{"command":"run the held probe"}}}`)
			}
			rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"turn-1","step":{"index":1,"outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","finish_reason":"interrupted"}}`)
			rows := rig.sink.Messages()
			require.NotEmpty(t, rows)
			expected := agent.MessageCompletionInterrupted
			if toolStarted {
				expected = agent.MessageCompletionComplete
			}
			assert.Equal(t, expected, rows[0].Completion)
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			assert.Equal(t, "Native answer", *view.item.Text)
			assert.Equal(t, contracts.KimiConversationOutcomeCompleted, view.item.Outcome, "native item bytes remain unchanged")
		})
	}
}

func TestKimiConversationSnapshotKeepsHistoricalCompletionCorrectionsInItsTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"historical","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","steps":[{"index":1,"outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","finish_reason":"interrupted"}],"items":[{"type":"assistant_message","item_id":"old:answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Historical partial"}]}]}`)
	require.NoError(t, agent.FlushPendingOutput(rig.agent.sink), "a page has no top-level turn ID; corrections must use the item's owning turn")
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.True(t, rows[0].TranscriptOnly)
	assert.True(t, rows[1].TurnEnd)
}

func TestKimiConversationQueuedCompletionKeepsItsOriginalSession(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"step:answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Old partial"}}`)
	queue := agent.OutputQueueFor(rig.agent.sink)
	released := false
	queue.Append(agent.OutputWrite{Owner: rig.agent.publicationOutputOwner(), Label: "hold the captured completion correction", Run: func() error {
		if !released {
			return agent.ErrOutputInFlight
		}
		return nil
	}})
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"turn-1","step":{"index":1,"outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","finish_reason":"interrupted"}}`)
	rig.agent.sink.UpdateSessionID("replacement")
	err := rig.agent.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"text":"Replacement answer"}`), Completion: agent.MessageCompletionComplete}, agent.SpanInfo{})
	require.True(t, agent.OutputRetained(err))
	released = true
	require.NoError(t, agent.FlushPendingOutput(rig.agent.sink))
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, "session_1", rows[0].AgentSessionID)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, "replacement", rows[1].AgentSessionID)
	assert.Equal(t, agent.MessageCompletionComplete, rows[1].Completion)
}

func TestKimiNativeTurnEndSettlesOnlyItsOwnAutomaticRetry(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, outcome, detail string
		historical            bool
		schedules, cancels    int
	}{
		{"retryable active failure", "failed", `,"error":{"code":"server_error","message":"Native service failed","retryable":true}`, false, 1, 0},
		{"definite refusal", "failed", `,"error":{"code":"forbidden","message":"Action refused","retryable":false}`, false, 0, 1},
		{"missing retry fact", "failed", `,"error":{"code":"server_error","message":"Native service failed"}`, false, 0, 1},
		{"blocked prompt", "failed", `,"end_reason":"blocked","error":{"code":"blocked","message":"Hook refused","retryable":true}`, false, 0, 1},
		{"successful turn", "completed", "", false, 0, 1},
		{"historical retryable failure", "failed", `,"error":{"code":"server_error","message":"Native service failed","retryable":true}`, true, 0, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			if !scenario.historical {
				rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			}
			end := `{"type":"turn.ended","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"ended","outcome":"` + scenario.outcome + `","started_at":"2026-11-18T00:00:00Z","steps":[]` + scenario.detail + `}}`
			rig.conversationFrame(t, end)
			rig.conversationFrame(t, end)
			awaitKimiTestOutput(t, rig.agent.sink)
			require.Equal(t, scenario.schedules, rig.sink.AutoScheduleCount())
			require.Equal(t, scenario.cancels, rig.sink.AutoCancelCount())
			if scenario.cancels != 0 {
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, rig.sink.LastAutoCancel())
			}
			if scenario.schedules != 0 {
				schedule := rig.sink.LastAutoSchedule()
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, schedule.Kind)
				view, err := decodeKimiConversationRecord(schedule.SourcePayload)
				require.NoError(t, err)
				require.NotNil(t, view.turn)
				assert.Equal(t, "turn", view.turn.TurnID)
				require.NotNil(t, view.turn.Error)
				assert.Equal(t, "Native service failed", view.turn.Error.Message)
			}
		})
	}
}

func TestKimiAutonomousMainOpenerRetainsASeparateNativeNotice(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, origin, placement string
		keep                    bool
	}{
		{"goal", `{"kind":"goal"}`, "opener", true},
		{"retry", `{"kind":"retry"}`, "opener", true},
		{"cron", `{"kind":"cron","job_id":"daily","schedule":"0 9 * * *","recurring":true,"coalesced_count":0,"stale":false}`, "opener", true},
		{"hook", `{"kind":"hook","hook_event":"SessionStart"}`, "opener", true},
		{"skill", `{"kind":"skill","name":"Review"}`, "opener", true},
		{"plugin command", `{"kind":"plugin_command","plugin_id":"review","command":"check"}`, "opener", true},
		{"system without an identified cause", `{"kind":"system"}`, "opener", false},
		{"human opener", `{"kind":"user"}`, "opener", false},
		{"plugin steer", `{"kind":"plugin_command","plugin_id":"review","command":"check"}`, "steer", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			notice := fmt.Sprintf(`{"type":"item.ended","turn_id":"turn","item":{"type":"user_message","item_id":"opener","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":%q,"origin":%s,"text":"Native opener","attachments":[],"skills":[]}}`, scenario.placement, scenario.origin)
			for range 2 {
				rig.conversationFrame(t, notice)
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			if !scenario.keep {
				assert.Empty(t, rows)
				return
			}
			require.Len(t, rows, 1)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, "opener", view.item.ItemID)
			assert.Equal(t, contracts.KimiConversationRecordRoleOrigin, view.record.Role)
			assert.JSONEq(t, scenario.origin, string(view.item.Origin))
		})
	}
}

func TestKimiConversationRetainsTheExactPlanProposalBeforeReview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, declaration string
		startWithoutPlan  bool
		endsWithPlan      bool
	}{
		{"plan arrives after tool start", `"input":{"options":[]},"stage":"awaiting_approval"`, true, false},
		{"approval item has no input", `"stage":"awaiting_approval"`, false, false},
		{"plan first appears in the result", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			const item = `"type":"tool_call","item_id":"plan","name":"ExitPlanMode","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z"`
			const display = `"display":{"kind":"plan_review","plan":"# Native proposal\n\nKeep its whitespace.\n","path":"/tmp/private-plan.md","options":[{"label":"One","description":"First"}],"review":{"state":"pending"}}`
			if tc.startWithoutPlan {
				rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn","item":{`+item+`,"status":"running","stage":"executing","input":{"options":[]}}}`)
				require.Empty(t, rig.sink.Messages(), "the first immutable row must wait for the proposal")
			}
			var requestBeforeEnd []byte
			if !tc.endsWithPlan {
				if tc.startWithoutPlan {
					rig.conversationFrame(t, `{"type":"item.updated","turn_id":"turn","item_id":"plan","set":{`+tc.declaration+`,`+display+`}}`)
				} else {
					rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn","item":{`+item+`,"status":"running",`+tc.declaration+`,`+display+`}}`)
				}
				awaitKimiTestOutput(t, rig.agent.sink)
				rows := rig.sink.Messages()
				require.Len(t, rows, 1)
				requestBeforeEnd = rows[0].Content
			}
			const finalDisplay = `"display":{"kind":"plan_review","plan":"# Native proposal\n\nKeep its whitespace.\n","review":{"state":"approved","selected_option":"One"}}`
			ended := `{"type":"item.ended","turn_id":"turn","item":{` + item + `,"status":"ended","outcome":"completed","output":"Review result",` + finalDisplay + `}}`
			for range 2 {
				rig.conversationFrame(t, ended)
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			require.Len(t, rows, 2)
			if len(requestBeforeEnd) != 0 {
				assert.Equal(t, requestBeforeEnd, rows[0].Content)
			}
			request, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, request.item)
			assert.Equal(t, contracts.KimiConversationRecordRoleRequest, request.record.Role)
			assert.JSONEq(t, `{"kind":"plan_review","plan":"# Native proposal\n\nKeep its whitespace.\n"}`, string(request.item.Display))
			assert.Empty(t, request.item.Output)
			assert.Empty(t, request.item.Outcome)
			result, err := decodeKimiConversationRecord(rows[1].Content)
			require.NoError(t, err)
			require.NotNil(t, result.item)
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, result.record.Role)
			assert.JSONEq(t, `"Review result"`, string(result.item.Output))
			assert.Contains(t, string(result.item.Display), `"approved"`)
			assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
			assert.True(t, rows[1].Closing)
		})
	}
}

func lastKimiToolOutputProgress(rig *kimiOutputRig) (total, tail agent.ProgressUpdate) {
	for _, update := range rig.sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTotal:
			total = update
		case agent.ProgressOutputTail:
			tail = update
		default:
		}
	}
	return total, tail
}
