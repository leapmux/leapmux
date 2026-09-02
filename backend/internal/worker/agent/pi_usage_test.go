package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAugmentPiMessageEnd_NoUsageReturnsRawUnchanged covers a
// message_end whose `message.usage` field is missing — the helper must
// pass the bytes through unchanged so downstream consumers see Pi's
// original envelope, byte-for-byte.
func TestAugmentPiMessageEnd_NoUsageReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	out := a.augmentPiMessageEnd(raw)
	assert.Equal(t, string(raw), string(out))
}

// TestAugmentPiMessageEnd_NonAssistantRoleReturnsRawUnchanged verifies
// the role guard. Pi emits message_end for tool/user messages too;
// those must skip the augment path so the broadcast doesn't double-bill
// the user's own prompt.
func TestAugmentPiMessageEnd_NonAssistantRoleReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	raw := []byte(`{"type":"message_end","message":{"role":"user","content":[]}}`)
	out := a.augmentPiMessageEnd(raw)
	assert.Equal(t, string(raw), string(out))
}

// TestAugmentPiMessageEnd_NonObjectMessageReturnsRawUnchanged is a
// defensive case: if Pi ever sends `message` as a string or array
// instead of an object the helper must not panic.
func TestAugmentPiMessageEnd_NonObjectMessageReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	raw := []byte(`{"type":"message_end","message":"not an object"}`)
	out := a.augmentPiMessageEnd(raw)
	assert.Equal(t, string(raw), string(out))
}

// TestAugmentPiMessageEnd_MalformedJSONReturnsRawUnchanged covers the
// json.Unmarshal failure path. The augment helper is best-effort — a
// malformed payload still flows through to PersistMessage as-is rather
// than being silently dropped.
func TestAugmentPiMessageEnd_MalformedJSONReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	raw := []byte(`{"type":"message_end","message":`)
	out := a.augmentPiMessageEnd(raw)
	assert.Equal(t, string(raw), string(out))
}

// TestAugmentPiMessageEnd_TotalsAreCumulative documents that two
// successive assistant message_ends produce a snapshot whose
// total_cost_usd reflects the sum, not the latest delta. This was true
// before the single-decode refactor and must remain so.
func TestAugmentPiMessageEnd_TotalsAreCumulative(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	a.model = "m1"
	a.availableModels = []*ModelInfo{{Id: "m1", ContextWindow: 1000}}
	first := a.augmentPiMessageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":10,"cost":{"total":0.5}}}}`))
	second := a.augmentPiMessageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":50,"output":5,"cost":{"total":0.25}}}}`))

	var p1, p2 map[string]any
	require.NoError(t, json.Unmarshal(first, &p1))
	require.NoError(t, json.Unmarshal(second, &p2))
	assert.InDelta(t, 0.5, p1["total_cost_usd"], 1e-9)
	assert.InDelta(t, 0.75, p2["total_cost_usd"], 1e-9, "total_cost_usd must be cumulative across message_ends")
}

// TestPiAugmentAgentEnd_NoOpWhenNothingToInject exercises the fast-path:
// with no cost, no contextUsage and no measured duration, the helper
// returns raw without re-marshalling.
func TestPiAugmentAgentEnd_NoOpWhenNothingToInject(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"agent_end","messages":[]}`)
	out := piAugmentAgentEnd(raw, piUsageSnapshot{}, nil)
	assert.Equal(t, string(raw), string(out))
}

// A measured duration alone is enough to enter the injection path, even
// when the usage snapshot is empty.
func TestPiAugmentAgentEnd_InjectsDurationWithEmptySnapshot(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"agent_end","messages":[]}`)
	durationMs := int64(0)
	out := piAugmentAgentEnd(raw, piUsageSnapshot{}, &durationMs)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, float64(0), got["duration_ms"], "a real zero must survive as a zero")
	assert.NotContains(t, got, "total_cost_usd")
}

// TestPiAugmentAgentEnd_InjectsEveryField covers the persist path for
// agent_end. Persisted shape uses snake_case (`total_cost_usd`,
// `context_usage`, `duration_ms`) to match the broadcast wire format.
func TestPiAugmentAgentEnd_InjectsEveryField(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"agent_end","messages":[]}`)
	snap := piUsageSnapshot{
		HasTotalCost: true,
		TotalCostUsd: 0.42,
		ContextUsage: map[string]any{"input_tokens": int64(100)},
	}
	durationMs := int64(2500)
	out := piAugmentAgentEnd(raw, snap, &durationMs)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "agent_end", got["type"])
	assert.InDelta(t, 0.42, got["total_cost_usd"], 1e-9)
	assert.Equal(t, float64(2500), got["duration_ms"])
	usage, ok := got["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usage["input_tokens"])
}

// TestPiAugmentAgentEnd_MalformedJSONReturnsRawUnchanged: the helper does
// not panic and returns raw unchanged when the input is not valid JSON.
func TestPiAugmentAgentEnd_MalformedJSONReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	raw := []byte(`not json`)
	snap := piUsageSnapshot{HasTotalCost: true, TotalCostUsd: 1}
	durationMs := int64(10)
	out := piAugmentAgentEnd(raw, snap, &durationMs)
	assert.Equal(t, string(raw), string(out))
}

// TestSessionInfo_PreservesIndependence asserts the contract documented
// on piUsageSnapshot: the snapshot's ContextUsage map is single-use and
// surrenders ownership to the broadcast caller, but mutating the
// broadcast payload must not leak back into a future snapshot from the
// same agent. This is the safety boundary we explicitly kept after
// dropping the redundant clone in sessionInfo().
func TestSessionInfo_PreservesIndependence(t *testing.T) {
	t.Parallel()

	a := newPiAgentWithSink(&recordingControlSink{})
	usage := piAssistantUsage{Input: 10}
	usage.Cost.Total = 0.1
	snap1 := a.recordPiAssistantUsage(usage, map[string]any{"input_tokens": int64(10)})
	info1 := snap1.sessionInfo()

	// Mutating the broadcast payload must not affect future snapshots.
	if cu, ok := info1["context_usage"].(map[string]any); ok {
		cu["input_tokens"] = int64(99999)
	}

	snap2 := a.recordPiAssistantUsage(piAssistantUsage{Input: 20}, map[string]any{"input_tokens": int64(20)})
	assert.Equal(t, int64(20), snap2.ContextUsage["input_tokens"], "second snapshot must reflect the new caller input, not the mutated broadcast payload")
}

// TestSessionInfo_AliasesSnapshotMap documents the post-Fix-3 contract:
// sessionInfo() returns info["context_usage"] aliased to the snapshot's
// own map. Mutating the broadcast payload mutates the snapshot's map
// (the snapshot is single-use; this is fine), but recordPiAssistantUsage
// already cloned from a.latestContextUsage so the agent state stays
// isolated.
func TestSessionInfo_AliasesSnapshotMap(t *testing.T) {
	t.Parallel()

	snap := piUsageSnapshot{ContextUsage: map[string]any{"input_tokens": int64(42)}}
	info := snap.sessionInfo()
	got, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	got["input_tokens"] = int64(0)
	assert.Equal(t, int64(0), snap.ContextUsage["input_tokens"], "sessionInfo() must alias the snapshot's map (single-use contract)")
}

// TestSessionInfo_NilContextUsage exercises the empty-snapshot branch:
// when ContextUsage is nil/empty the broadcast info must omit the key
// entirely. Frontend's updateInfo skips nil values, but a missing key
// avoids waking the reactive store at all.
func TestSessionInfo_NilContextUsage(t *testing.T) {
	t.Parallel()

	cases := []map[string]any{nil, {}}
	for _, ctxUsage := range cases {
		snap := piUsageSnapshot{HasTotalCost: true, TotalCostUsd: 0.5, ContextUsage: ctxUsage}
		info := snap.sessionInfo()
		_, has := info["context_usage"]
		assert.False(t, has, "empty/nil ContextUsage must not surface in the broadcast payload")
		assert.InDelta(t, 0.5, info["total_cost_usd"], 1e-9)
	}
}
