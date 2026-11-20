package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestClaudeTranscriptKey_NormalizesTheWireUUID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "u-1", claudeTranscriptKey("u-1"))
	assert.Equal(t, "u-1", claudeTranscriptKey(" U-1 "))
	assert.Equal(t, "e3a1b2c4-1234", claudeTranscriptKey("E3A1B2C4-1234"))
	assert.Empty(t, claudeTranscriptKey(""))
	assert.Empty(t, claudeTranscriptKey("   "))
}

func TestClaudeEvictionKeys_DropsEmptyRepeatSelfAndMalformedValues(t *testing.T) {
	t.Parallel()

	keys := claudeEvictionKeys([]byte(`{"type":"system","subtype":"model_refusal_fallback","retracted_message_uuids":["U-1","u-1","", 7, {"x":1},"u-2"]}`), claudeMsgTypeSystem, "u-9")
	assert.Equal(t, []string{"u-1", "u-2"}, keys)

	assert.Empty(t, claudeEvictionKeys([]byte(`{"type":"system","retracted_message_uuids":["u-9"]}`), claudeMsgTypeSystem, "u-9"),
		"a frame never evicts itself")
	assert.Empty(t, claudeEvictionKeys([]byte(`{"type":"system","retracted_message_uuids":"not-a-list"}`), claudeMsgTypeSystem, ""))
	assert.Empty(t, claudeEvictionKeys([]byte(`{"type":"system","retracted_message_uuids":["u-1"]}`), claudeMsgTypeResult, ""),
		"only a fallback system frame carries the signal")
}

// persistedAssistant is one ordinary assistant frame with a wire UUID.
func persistedAssistant(uuid, text string) []byte {
	return []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]},"uuid":"` + uuid + `","session_id":"s-1"}`)
}

// fallbackRetracting is the model-refusal fallback notice that evicts the named
// native messages.
func fallbackRetracting(uuid string, retracted ...string) []byte {
	quoted := ""
	for index, value := range retracted {
		if index > 0 {
			quoted += ","
		}
		quoted += `"` + value + `"`
	}
	return []byte(`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","scope":"local","original_model":"m1","fallback_model":"m2","content":"Switched to m2","uuid":"` + uuid + `","session_id":"s-1","retracted_message_uuids":[` + quoted + `]}`)
}

func TestClaudeModelRefusalFallbackRetractsOnlyItsTargets(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput(persistedAssistant("u-1", "partial one"))
	a.HandleOutput(persistedAssistant("u-2", "partial two"))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":"ok"}]},"uuid":"u-3","session_id":"s-1"}`))

	// The signal lists exact, repeated, empty, self, unknown and malformed
	// values on purpose.
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","scope":"local","original_model":"m1","fallback_model":"m2","content":"Switched to m2","uuid":"u-9","session_id":"s-1","retracted_message_uuids":["U-1","u-1","", 7,"u-unknown","u-3","u-9"]}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 4, "three revocable rows and the fallback notice")
	assert.True(t, msgs[0].Retracted, "the exact target is deleted")
	assert.False(t, msgs[1].Retracted, "the neighbor survives")
	assert.True(t, msgs[2].Retracted, "the tool-result row is deleted")
	assert.False(t, msgs[3].Retracted, "the fallback notice survives: a frame never evicts itself")

	retractions := sink.Retractions()
	require.Len(t, retractions, 1)
	assert.Equal(t, []string{"u-1", "u-unknown", "u-3"}, retractions[0].Keys,
		"empty, repeated and self values drop out")
	assert.Equal(t, []int64{1, 3}, retractions[0].Seqs, "only stored rows report sequences")

	// A signal naming only already-retracted keys deletes nothing.
	a.HandleOutput(fallbackRetracting("u-10", "u-1", "u-3"))
	require.Len(t, sink.Retractions(), 2)
	assert.Empty(t, sink.Retractions()[1].Seqs, "a duplicate signal emits no sequences")

	// The second valid signal deletes only its own targets.
	a.HandleOutput(fallbackRetracting("u-11", "u-2"))
	msgs = sink.Messages()
	assert.True(t, msgs[1].Retracted)
}

func TestClaudeReplacementAssistantSupersedesTheReplacedMessage(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput(persistedAssistant("u-1", "refused partial"))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"replacement"}]},"uuid":"u-2","session_id":"s-1","supersedes":"u-1"}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.True(t, msgs[0].Retracted, "the replaced row is deleted")
	assert.False(t, msgs[1].Retracted, "the replacement survives")

	retractions := sink.Retractions()
	require.Len(t, retractions, 1)
	assert.Equal(t, []string{"u-1"}, retractions[0].Keys)
}

func TestClaudeHeldAPIError_DroppedWhenItsUUIDIsRetracted(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"boom"}]},"uuid":"u-err","session_id":"s-1","is_api_error_message":true}`))
	require.NotNil(t, a.heldAPIError, "the API error is held")

	a.HandleOutput(fallbackRetracting("u-9", "u-err"))
	a.flushHeldAPIError()

	for _, message := range sink.Messages() {
		assert.NotEqual(t, "u-err", message.IdempotencyKey, "the matching hold can never become a new row")
	}
}

func TestClaudeHeldAPIError_SurvivesAnUnmatchedRetraction(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"boom"}]},"uuid":"u-err","session_id":"s-1","is_api_error_message":true}`))
	a.HandleOutput(fallbackRetracting("u-9", "u-other"))
	a.flushHeldAPIError()

	found := false
	for _, message := range sink.Messages() {
		if message.IdempotencyKey == "u-err" {
			found = true
		}
	}
	assert.True(t, found, "the unmatched hold still follows the normal flush")
}

func TestClaudeRetractedRedeliveryPersistsNoRowAndResolvesNoHold(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	// A fallback names an API error that never landed: its receipt must
	// suppress the redelivery below.
	a.HandleOutput(fallbackRetracting("u-9", "u-1"))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"unrelated"}]},"uuid":"u-2","session_id":"s-1","is_api_error_message":true}`))
	require.NotNil(t, a.heldAPIError, "an unrelated error is held")
	require.Equal(t, "u-2", a.heldAPIError.key)
	before := len(sink.Messages())

	// The redelivered retracted API error must neither persist, resolve, nor
	// replace the unrelated hold.
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"redelivered"}]},"uuid":"u-1","session_id":"s-1","is_api_error_message":true}`))
	assert.Len(t, sink.Messages(), before, "a retracted redelivery inserts no row")
	require.NotNil(t, a.heldAPIError, "the unrelated hold is untouched")
	assert.Equal(t, "u-2", a.heldAPIError.key, "the retracted redelivery cannot replace the hold")
}

func TestClaudePersistedRevocableEnvelopesCarryTheWireUUIDKey(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput(persistedAssistant("U-1", "answer"))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":"ok"}]},"uuid":"U-2","session_id":"s-1"}`))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"uuid":"U-3","session_id":"s-1"}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 3)
	assert.Equal(t, "u-1", msgs[0].IdempotencyKey, "the assistant envelope keys on the normalized wire UUID")
	assert.Equal(t, "u-2", msgs[1].IdempotencyKey, "the tool-result envelope keys on the normalized wire UUID")
	assert.Empty(t, msgs[2].IdempotencyKey, "an ordinary user prompt is not revocable")
}

func TestClaudeForwardedReplacementSupersedesInTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "d",
		"prompt": "p"
	}`))
	child := sink.Child("child-of-tu-spawn")
	child.UpdateSessionID("child-session")

	forwarded := func(uuid, supersedes string) []byte {
		return []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"child"}]},"uuid":"` + uuid + `","session_id":"s-1","parent_tool_use_id":"tu-spawn","supersedes":"` + supersedes + `"}`)
	}
	a.HandleOutput(forwarded("u-1", ""))
	childRows := child.Messages()
	require.NotEmpty(t, childRows, "the forwarded row lands in the child transcript")

	a.HandleOutput(forwarded("u-2", "u-1"))

	retractions := child.Retractions()
	require.Len(t, retractions, 1, "the forwarded replacement retracts through the child route")
	assert.Equal(t, []string{"u-1"}, retractions[0].Keys)
	childRows = child.Messages()
	sawTarget := false
	for _, row := range childRows {
		if row.IdempotencyKey == "u-1" {
			sawTarget = true
			assert.True(t, row.Retracted, "only the child's admitted row is deleted")
		} else {
			assert.False(t, row.Retracted)
		}
	}
	assert.True(t, sawTarget, "the superseded child row was recorded")
}
