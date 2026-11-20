package claude

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

	keysOf := func(content []byte, msgType, self string) []string {
		return decodeClaudeFrameRouting(content).claudeEvictionKeys(msgType, self)
	}
	keys := keysOf([]byte(`{"type":"system","subtype":"model_refusal_fallback","retracted_message_uuids":["U-1","u-1","", 7, {"x":1},"u-2"]}`), claudeMsgTypeSystem, "u-9")
	assert.Equal(t, []string{"u-1", "u-2"}, keys)

	assert.Empty(t, keysOf([]byte(`{"type":"system","retracted_message_uuids":["u-9"]}`), claudeMsgTypeSystem, "u-9"),
		"a frame never evicts itself")
	assert.Empty(t, keysOf([]byte(`{"type":"system","retracted_message_uuids":"not-a-list"}`), claudeMsgTypeSystem, ""))
	assert.Empty(t, keysOf([]byte(`{"type":"system","retracted_message_uuids":["u-1"]}`), claudeMsgTypeResult, ""),
		"only a fallback system frame carries the signal")
}

// persistedAssistant is one ordinary assistant frame with a wire UUID.
func persistedAssistant(uuid, text string) []byte {
	return []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]},"uuid":"` + uuid + `","session_id":"s-1"}`)
}

// fallbackRetracting builds the model-refusal fallback notice for the supplied keys.
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
	sink.UpdateSessionID("s-1")

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
	assert.Equal(t, []string{"u-1", "u-3"}, retractions[0].Keys,
		"empty, repeated, unadmitted and self values drop out")
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
	sink.UpdateSessionID("s-1")

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
	sink.UpdateSessionID("s-1")

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
	sink.UpdateSessionID("s-1")

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
	sink.UpdateSessionID("s-1")

	// A reopened transcript can already contain a receipt for a native error.
	captured := sink.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-1"}))
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
	sink.UpdateSessionID("s-1")

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
	sink.UpdateSessionID("s-1")

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

func TestClaudeParentlessFallbackUsesOnlyUniqueCurrentDestinations(t *testing.T) {
	t.Parallel()
	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"s-1"}`))

	a.HandleOutput(persistedAssistant("shared", "root"))
	a.HandleOutput(persistedAssistant("root-only", "root"))
	a.HandleOutput(persistedAssistant("wrong-subtype", "root"))
	a.HandleOutput(persistedAssistant("foreign-signal", "root"))
	for _, key := range []string{"shared", "child-only"} {
		forwarded := strings.Replace(string(persistedAssistant(key, "child")), `"session_id"`, `"parent_tool_use_id":"spawn","session_id"`, 1)
		a.HandleOutput([]byte(forwarded))
	}
	child := sink.Child("child-of-spawn")
	require.NotNil(t, child)
	a.HandleOutput([]byte(`{"type":"system","subtype":"informational","level":"info","session_id":"s-1","retracted_message_uuids":["wrong-subtype"]}`))
	a.HandleOutput([]byte(strings.Replace(string(fallbackRetracting("foreign", "foreign-signal")), "s-1", "foreign-session", 1)))
	a.HandleOutput(fallbackRetracting("fallback", "shared", "root-only", "child-only", "unadmitted"))

	require.Len(t, sink.Retractions(), 1)
	assert.Equal(t, []string{"root-only"}, sink.Retractions()[0].Keys)
	require.Len(t, child.Retractions(), 1)
	assert.Equal(t, []string{"child-only"}, child.Retractions()[0].Keys)
	assert.Empty(t, child.Retractions()[0].AgentSessionID, "the child row has no stored native session")
}

type claudeOnceFailingHeldSink struct {
	agenttest.Sink
	failHeld bool
}

func (s *claudeOnceFailingHeldSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.failHeld && content.IdempotencyKey == "held" {
		s.failHeld = false
		return errors.New("first hold write refused")
	}
	return s.Sink.PersistMessage(source, content, span)
}

func TestClaudeResolvingFrameRetriesTheHeldRowAtItsPosition(t *testing.T) {
	t.Parallel()
	sink := &claudeOnceFailingHeldSink{failHeld: true}
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	a := newTestAgent(services)
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"held","session_id":"s-1","is_api_error_message":true,"message":{"content":[{"type":"text","text":"first error"}]}}`))
	a.HandleOutput(persistedAssistant("answer", "answer"))
	assert.Empty(t, sink.Messages(), "the first write failed at the resolving position")
	require.NoError(t, agent.FlushPendingOutput(services))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, "held", messages[0].IdempotencyKey)
	assert.Equal(t, "answer", messages[1].IdempotencyKey)
}

func TestClaudeQueueRetainsSpanEffectsUntilTheirRowsCommit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	queue := sink.OutputQueue()
	services := agent.NewOrderedProviderServices(queue, agent.NewProviderServices(sink))
	a := newTestAgent(services)
	blocked := true
	queue.Append(agent.OutputWrite{Label: "block Claude output", Run: func() error {
		if blocked {
			return errors.New("storage unavailable")
		}
		return nil
	}})
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"opener","message":{"content":[{"type":"tool_use","id":"tool","name":"Read"}]}}`))
	a.HandleOutput([]byte(`{"type":"user","uuid":"closer","message":{"content":[{"type":"tool_result","tool_use_id":"tool","content":"ok"}]}}`))
	assert.Empty(t, sink.OpenSpans(), "an unaccepted opener cannot change span geometry")
	assert.Empty(t, sink.ClosedSpans(), "an unaccepted closer cannot change span geometry")
	blocked = false
	require.NoError(t, agent.FlushPendingOutput(services))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].SpansOpenAtPersist)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "tool"}}, rows[1].SpansOpenAtPersist)
	assert.Equal(t, []string{"tool"}, sink.ClosedSpans())
}

type claudeReceiptLookupFailureSink struct {
	agenttest.Sink
	receiptErr error
}

func (s *claudeReceiptLookupFailureSink) IsMessageRetracted(content agent.MessageContent, key string) (bool, error) {
	if key == "removed" && s.receiptErr != nil {
		return false, s.receiptErr
	}
	return s.Sink.IsMessageRetracted(content, key)
}

func TestClaudeReceiptLookupFailureKeepsTheHoldAndTheQueuePosition(t *testing.T) {
	t.Parallel()
	sink := &claudeReceiptLookupFailureSink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	a := newTestAgent(services)
	captured := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	require.NoError(t, services.RetractMessagesByIdempotencyKey(captured, []string{"removed"}))
	sink.receiptErr = errors.New("receipt lookup unavailable")
	a.HandleOutput(persistedAssistant("removed", "replayed while idle"))
	assert.Empty(t, sink.TurnActives(), "the unresolved receipt blocks heuristic activation")
	sink.receiptErr = nil
	require.NoError(t, agent.FlushPendingOutput(services))
	assert.Empty(t, sink.TurnActives(), "a retracted assistant leaves the provider idle")
	assert.Empty(t, sink.Messages())

	a.HandleOutput([]byte(`{"type":"assistant","uuid":"held","is_api_error_message":true,"message":{"content":[{"type":"text","text":"first error"}]}}`))
	sink.receiptErr = errors.New("receipt lookup unavailable")
	a.HandleOutput(persistedAssistant("removed", "replayed with an unrelated hold"))
	a.HandleOutput(persistedAssistant("survivor", "continues the conversation"))
	assert.Empty(t, sink.Messages(), "both the hold and its later resolver wait for the receipt")
	sink.receiptErr = nil
	require.NoError(t, agent.FlushPendingOutput(services))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, "held", rows[0].IdempotencyKey)
	assert.Equal(t, "survivor", rows[1].IdempotencyKey)
}

func TestClaudeBothEvictionSignalsDeleteTheTargetOnce(t *testing.T) {
	t.Parallel()
	for label, order := range map[string][]string{
		"fallback first":    {"fallback", "replacement"},
		"replacement first": {"replacement", "fallback"},
	} {
		t.Run(label, func(t *testing.T) {
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("s-1")
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput(persistedAssistant("target", "refused partial"))
			frames := map[string][]byte{
				"fallback":    fallbackRetracting("notice", "target"),
				"replacement": []byte(`{"type":"assistant","uuid":"answer","session_id":"s-1","supersedes":"target","message":{"content":[{"type":"text","text":"replacement"}]}}`),
			}
			for _, frame := range order {
				a.HandleOutput(frames[frame])
			}
			var removed []int64
			for _, signal := range sink.Retractions() {
				removed = append(removed, signal.Seqs...)
			}
			assert.Equal(t, []int64{1}, removed)
			rows := sink.Messages()
			require.Len(t, rows, 3)
			assert.True(t, rows[0].Retracted)
			assert.False(t, rows[1].Retracted)
			assert.False(t, rows[2].Retracted)
		})
	}
}

func TestClaudeRejectedRootAttributionLeavesTheChildAsTheOnlyDestination(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("s-1")
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	captured := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	require.NoError(t, services.RetractMessagesByIdempotencyKey(captured, []string{"shared"}))
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"shared","session_id":"s-1","parent_tool_use_id":"spawn","message":{"content":[{"type":"text","text":"the child owns this"}]}}`))
	child := sink.Child("child-of-spawn")
	require.Len(t, child.Messages(), 1)
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block root receipt", Run: func() error {
		return errors.New("root storage is unavailable")
	}})
	a.HandleOutput(persistedAssistant("shared", "rejected root replay"))
	a.HandleOutput(fallbackRetracting("fallback", "shared"))
	assert.False(t, child.Messages()[0].Retracted, "fallback must first resolve the root receipt")
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(services))
	require.Len(t, child.Retractions(), 1)
	assert.True(t, child.Messages()[0].Retracted)
}

func TestClaudeFallbackModelObservationsKeepNativeSourceOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a := newTestAgent(services)
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block fallback notices", Run: func() error {
		return errors.New("the notice store is unavailable")
	}})
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","scope":"session","fallback_model":"opus","content":"refusal"}`))
	assert.Equal(t, "opus[1m]", a.model, "the observation does not wait for the notice")
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_consent_fallback","fallback_model":"sonnet","content":"consent"}`))
	assert.Equal(t, "sonnet", a.model)
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(services))
	assert.Equal(t, "sonnet", a.model, "the older persisted notice cannot replace the newer model")
}
