package kimi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func applyKimiConversationTestFrame(t *testing.T, state *kimiConversationState, raw string) bool {
	t.Helper()
	frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, state.sessionID, state.agentID, raw))
	require.NoError(t, err)
	applied, err := state.apply(frame)
	require.NoError(t, err)
	return applied
}

func newKimiConversationTestState(t *testing.T) *kimiConversationState {
	t.Helper()
	state := newKimiConversationState("session_1", "main")
	applyKimiConversationTestFrame(t, state, `{"type":"turn.started","turn":`+kimiConversationTestTurn+`}`)
	return state
}

func TestKimiConversationStateRetainsRepeatedDeltasAndAuthoritativeReplacements(t *testing.T) {
	t.Parallel()
	state := newKimiConversationTestState(t)
	applyKimiConversationTestFrame(t, state, `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"answer-1","status":"running","created_at":"2026-11-18T00:00:00Z","text":"A"}}`)
	before := state.turns["turn-1"].items["answer-1"].snapshot()
	for range 2 {
		applyKimiConversationTestFrame(t, state, `{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"text","delta":"🙂"}`)
	}
	assert.Equal(t, "A", *before.Text, "a prior snapshot keeps its original prefix")
	assert.Equal(t, "A🙂🙂", *state.turns["turn-1"].items["answer-1"].snapshot().Text)
	ended := `{"type":"item.ended","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"answer-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"A🙂한!"}}`
	assert.True(t, applyKimiConversationTestFrame(t, state, ended))
	assert.Equal(t, "A🙂한!", *state.turns["turn-1"].items["answer-1"].snapshot().Text)
	assert.False(t, applyKimiConversationTestFrame(t, state, ended), "an identical completed item has no second projection")
	assert.True(t, applyKimiConversationTestFrame(t, state, strings.Replace(ended, "A🙂한!", "Corrected answer", 1)))
	assert.Equal(t, "Corrected answer", *state.turns["turn-1"].items["answer-1"].snapshot().Text)
}

func TestKimiConversationStatePatchesReplaceMapsAndPreserveStreamedOutput(t *testing.T) {
	t.Parallel()
	state := newKimiConversationTestState(t)
	applyKimiConversationTestFrame(t, state, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"input","input_text":"{","output":"","fields":{"old":1,"keep":2}}}`)
	applyKimiConversationTestFrame(t, state, `{"type":"item.delta","turn_id":"turn-1","item_id":"call-1","field":"input_text","delta":"\"command\":\"printf ok\"}"}`)
	applyKimiConversationTestFrame(t, state, `{"type":"item.delta","turn_id":"turn-1","item_id":"call-1","field":"output","delta":"OUT"}`)
	before := state.turns["turn-1"].items["call-1"].snapshot()
	applyKimiConversationTestFrame(t, state, `{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"stage":"executing","input":{"command":"printf ok"},"fields":{"new":0}}}`)
	after := state.turns["turn-1"].items["call-1"].snapshot()
	assert.JSONEq(t, `"OUT"`, string(after.Output))
	assert.Equal(t, `{"command":"printf ok"}`, *after.InputText)
	assert.JSONEq(t, `{"command":"printf ok"}`, string(after.Input))
	assert.Equal(t, map[string]json.RawMessage{"new": json.RawMessage(`0`)}, after.Fields)
	assert.Equal(t, map[string]json.RawMessage{"old": json.RawMessage(`1`), "keep": json.RawMessage(`2`)}, before.Fields)
	for _, raw := range []string{
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"fields":{"bad":0},"name":null}}`,
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"step_index":2}}`,
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"unsupported":true}}`,
	} {
		frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", raw))
		require.NoError(t, err)
		_, err = state.apply(frame)
		require.Error(t, err)
		assert.Equal(t, after, state.turns["turn-1"].items["call-1"].snapshot(), "a refused patch changes no field or accumulator")
	}
}

func TestKimiConversationStateKeepsObservedStepIdentityAcrossMissingMetadata(t *testing.T) {
	t.Parallel()
	state := newKimiConversationTestState(t)
	first := `{"type":"item.started","turn_id":"turn-1","item":{"type":"assistant_message","item_id":"answer-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"First"}}`
	applyKimiConversationTestFrame(t, state, first)
	applyKimiConversationTestFrame(t, state, strings.Replace(first, `"step_index":1,`, "", 1))
	frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", strings.Replace(first, `"step_index":1`, `"step_index":2`, 1)))
	require.NoError(t, err)
	_, err = state.apply(frame)
	require.ErrorContains(t, err, "identity")
	assert.Equal(t, int64(1), *state.turns["turn-1"].items["answer-1"].identity.stepIndex)
}

func TestKimiConversationStateRejectsForeignOwnersAndAtomicSnapshotFailures(t *testing.T) {
	t.Parallel()
	state := newKimiConversationTestState(t)
	previous := state.turns["turn-1"]
	frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "another-session", "main", `{"type":"turns","turns":[],"has_more":false}`))
	require.NoError(t, err)
	_, err = state.apply(frame)
	require.ErrorContains(t, err, "owner")
	var turns []kimiConversationTurn
	require.NoError(t, json.Unmarshal([]byte(`[`+kimiConversationTestTurn+`,{"turn_id":"turn-2","ordinal":1,"kind":"agent","status":"ended","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"bad","status":"ended","created_at":"2026-11-18T00:00:00Z"}]}]`), &turns))
	_, err = state.replaceSnapshot(turns)
	require.Error(t, err)
	assert.Same(t, previous, state.turns["turn-1"])
	assert.NotContains(t, state.turns, "turn-2")
	applyKimiConversationTestFrame(t, state, `{"type":"turns.removed","turn_ids":["turn-1"]}`)
	assert.False(t, applyKimiConversationTestFrame(t, state, `{"type":"turn.started","turn":`+kimiConversationTestTurn+`}`))
	assert.Empty(t, state.turns)
	applyKimiConversationTestFrame(t, state, `{"type":"turns","turns":[],"has_more":false}`)
	assert.Empty(t, state.turns)
}

func TestKimiConversationStateReplacesStepsByIndexAndClearsRetry(t *testing.T) {
	t.Parallel()
	state := newKimiConversationTestState(t)
	applyKimiConversationTestFrame(t, state, `{"type":"turn.updated","turn_id":"turn-1","retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"APIError","message":"Retry."}}}`)
	require.NotNil(t, state.turns["turn-1"].header.Retry)
	for _, index := range []string{"2", "1", "2"} {
		applyKimiConversationTestFrame(t, state, `{"type":"turn.updated","turn_id":"turn-1","step":{"index":`+index+`,"outcome":"completed","started_at":"2026-11-18T00:00:00Z"}}`)
	}
	header := state.turns["turn-1"].headerSnapshot()
	assert.Nil(t, header.Retry)
	require.Len(t, header.Steps, 2)
	assert.Equal(t, int64(1), header.Steps[0].Index)
	assert.Equal(t, int64(2), header.Steps[1].Index)
	encoded, err := json.Marshal(header)
	require.NoError(t, err)
	assert.False(t, applyKimiConversationTestFrame(t, state, `{"type":"turn.started","turn":`+string(encoded)+`}`), "an identical header needs no second projection")
}

func TestKimiConversationStateReleasesCompletedPayloadsAndRetainsReplayProof(t *testing.T) {
	t.Parallel()
	var item kimiConversationItem
	require.NoError(t, json.Unmarshal([]byte(`{"type":"tool_call","item_id":"call-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash"}`), &item))
	var err error
	item.Output, err = json.Marshal(strings.Repeat("output", 100_000))
	require.NoError(t, err)
	value, changed, err := prepareKimiConversationItemState(nil, item)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, value.snapshot())
	value.releaseBody()
	assert.Nil(t, value.data)
	assert.Nil(t, value.text)
	assert.Nil(t, value.inputText)
	assert.Nil(t, value.output)
	replayed, changed, err := prepareKimiConversationItemState(value, item)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Same(t, value, replayed)
	assert.Nil(t, replayed.snapshot())
}

func TestKimiConversationLiveStringsCannotExceedTheNativeFrameLimit(t *testing.T) {
	t.Parallel()
	const finalRune = "🙂"
	prefix := strings.Repeat("x", kimiStreamReadLimit-len(finalRune))
	for _, scenario := range []struct {
		name, kind, field string
	}{
		{"assistant answer", contracts.KimiConversationItemKindAssistantMessage, "text"},
		{"reasoning", contracts.KimiConversationItemKindReasoning, "text"},
		{"streamed tool arguments", contracts.KimiConversationItemKindToolCall, "input_text"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			item := &kimiConversationItemState{
				identity: kimiConversationItemIdentity{kind: scenario.kind, itemID: "owned-item"},
				data:     &kimiConversationItem{Type: scenario.kind, ItemID: "owned-item", Status: contracts.KimiConversationStatusRunning},
			}
			require.NoError(t, item.append(scenario.field, prefix))
			require.NoError(t, item.append(scenario.field, finalRune))
			err := item.append(scenario.field, "x")
			assert.ErrorIs(t, err, errKimiConversationNeedsSnapshot)
			retained := item.snapshot()
			body := retained.Text
			if scenario.field == "input_text" {
				body = retained.InputText
			}
			require.NotNil(t, body)
			assert.Equal(t, kimiStreamReadLimit, len(*body))
			assert.True(t, strings.HasSuffix(*body, finalRune))
		})
	}
}

func TestKimiConversationStateReplaceSnapshotReportsChangesAndMaintainsToolIndex(t *testing.T) {
	t.Parallel()
	parseTurns := func(t *testing.T, raw string) []kimiConversationTurn {
		t.Helper()
		var turns []kimiConversationTurn
		require.NoError(t, json.Unmarshal([]byte(raw), &turns))
		return turns
	}

	baseJSON := `[{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` +
		`{"type":"assistant_message","item_id":"msg-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Hello"},` +
		`{"type":"tool_call","item_id":"call-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash"}` +
		`]}]`

	state := newKimiConversationState("session_1", "main")
	changed, err := state.replaceSnapshot(parseTurns(t, baseJSON))
	require.NoError(t, err)
	assert.True(t, changed, "initial snapshot changes empty state")
	assert.Equal(t, map[string]map[string]struct{}{"call-1": {"turn-1": {}}}, state.toolCallTurns, "only tool calls enter toolCallTurns")

	// Still-retained item bodies report changed=true even when digests match.
	changed, err = state.replaceSnapshot(parseTurns(t, baseJSON))
	require.NoError(t, err)
	assert.True(t, changed, "unreleased item bodies require projection")

	// Releasing item bodies makes an identical snapshot a no-op.
	for _, item := range state.turns["turn-1"].items {
		item.releaseBody()
	}
	changed, err = state.replaceSnapshot(parseTurns(t, baseJSON))
	require.NoError(t, err)
	assert.False(t, changed, "identical snapshot with released bodies reports no change")

	// Reordering the same two items reports changed=true.
	reorderedJSON := `[{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` +
		`{"type":"tool_call","item_id":"call-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash"},` +
		`{"type":"assistant_message","item_id":"msg-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Hello"}` +
		`]}]`
	changed, err = state.replaceSnapshot(parseTurns(t, reorderedJSON))
	require.NoError(t, err)
	assert.True(t, changed, "reordered items report change")

	// Pending projection flags on a turn or item force changed=true.
	for _, item := range state.turns["turn-1"].items {
		item.releaseBody()
	}
	state.turns["turn-1"].projectionPending = true
	changed, err = state.replaceSnapshot(parseTurns(t, reorderedJSON))
	require.NoError(t, err)
	assert.True(t, changed, "pending turn projection reports change")

	state.turns["turn-1"].items["call-1"].projectionPending = true
	changed, err = state.replaceSnapshot(parseTurns(t, reorderedJSON))
	require.NoError(t, err)
	assert.True(t, changed, "pending item projection reports change")

	// An atomic failure preserves both turns and toolCallTurns.
	badJSON := `[{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"tool_call","item_id":"call-new","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash"}]},` +
		`{"turn_id":"turn-2","ordinal":1,"kind":"agent","status":"ended","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"bad","status":"ended","created_at":"2026-11-18T00:00:00Z"}]}]`
	changed, err = state.replaceSnapshot(parseTurns(t, badJSON))
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, map[string]map[string]struct{}{"call-1": {"turn-1": {}}}, state.toolCallTurns)

	// Removing all turns reports changed=true and clears toolCallTurns.
	changed, err = state.replaceSnapshot(nil)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Empty(t, state.toolCallTurns)

	// Lazy ensureToolCallIndex rebuilds from pre-populated turns when toolCallTurns is nil.
	lazy := newKimiConversationState("session_1", "main")
	lazy.turns["turn-1"] = &kimiConversationTurnState{
		items: map[string]*kimiConversationItemState{
			"call-lazy": {identity: kimiConversationItemIdentity{itemID: "call-lazy", kind: contracts.KimiConversationItemKindToolCall}},
			"msg-lazy":  {identity: kimiConversationItemIdentity{itemID: "msg-lazy", kind: contracts.KimiConversationItemKindAssistantMessage}},
		},
	}
	assert.Nil(t, lazy.toolCallTurns)
	applyKimiConversationTestFrame(t, lazy, `{"type":"item.removed","turn_id":"turn-1","item_id":"call-lazy"}`)
	assert.NotNil(t, lazy.toolCallTurns)
	assert.Empty(t, lazy.toolCallTurns)
}
