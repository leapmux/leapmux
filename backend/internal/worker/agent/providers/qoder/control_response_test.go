package qoder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// browserAnswer is the neutral envelope the browser sends.
func browserAnswer(t *testing.T, requestID, behavior, message string, updatedInput map[string]any) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	if message != "" {
		inner["message"] = message
	}
	if updatedInput != nil {
		inner["updatedInput"] = updatedInput
	}
	data, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": inner}})
	require.NoError(t, err)
	return data
}

// nativeAnswer reads Qoder's own answer out of a translated envelope.
func nativeAnswer(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(content, &envelope), string(content))
	// Qoder rejects a frame that carries a top-level request_id, so the answer
	// travels under `response` alone.
	assert.NotContains(t, envelope, "request_id")
	outer, ok := envelope["response"].(map[string]any)
	require.True(t, ok, string(content))
	assert.Equal(t, "success", outer["subtype"])
	assert.Equal(t, "approval:1", outer["request_id"])
	native, ok := outer["response"].(map[string]any)
	require.True(t, ok, string(content))
	return native
}

func TestQoderTranslateAllowWithoutAnUpdateStatesProceedOnce(t *testing.T) {
	t.Parallel()
	translated, ok := translateQoderCanUseTool(browserAnswer(t, "approval:1", "allow", "", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"behavior": "allow",
		"outcome":  contracts.QoderPermissionOutcomeProceedOnce,
	}, nativeAnswer(t, translated))
}

// An allow that carries the browser's updatedInput states NO outcome beside it:
// Qoder resolves an explicit outcome first and then reads the modified input
// from a `payload` field, so the two together drop the input -- which is what
// an AskUserQuestion reply would lose.
func TestQoderTranslateAllowForwardsUpdatedInputWithoutAnOutcome(t *testing.T) {
	t.Parallel()
	updated := map[string]any{
		"questions": []any{map[string]any{"question": "Color?"}},
		"answers":   map[string]any{"Color?": "Red"},
	}
	translated, ok := translateQoderCanUseTool(browserAnswer(t, "approval:1", "allow", "", updated))
	require.True(t, ok)
	native := nativeAnswer(t, translated)
	assert.Equal(t, "allow", native["behavior"])
	assert.NotContains(t, native, "outcome")
	require.IsType(t, map[string]any{}, native["updatedInput"])
	assert.Equal(t, "Red", native["updatedInput"].(map[string]any)["answers"].(map[string]any)["Color?"])
}

// A rejection carries the user's words on BOTH fields: Qoder's reader takes
// `message` on the `behavior` spelling and `reason` on the `allowed` one, and
// the words must reach the model whichever reader runs.
func TestQoderTranslateDenyCarriesTheReasonOnBothFields(t *testing.T) {
	t.Parallel()
	translated, ok := translateQoderCanUseTool(browserAnswer(t, "approval:1", "deny", "Use a dry run.", nil))
	require.True(t, ok)
	native := nativeAnswer(t, translated)
	assert.Equal(t, "deny", native["behavior"])
	assert.Equal(t, "Use a dry run.", native["message"])
	assert.Equal(t, "Use a dry run.", native["reason"])
	assert.NotContains(t, native, "outcome", "an explicit cancel outcome would drop the words")
}

// A bare deny states no typed reason, and the placeholder the browser auto-fills
// is never handed to the model as if the user had written it.
func TestQoderTranslateBareDenyStatesNoReason(t *testing.T) {
	t.Parallel()
	translated, ok := translateQoderCanUseTool(browserAnswer(t, "approval:1", "deny", "", nil))
	require.True(t, ok)
	native := nativeAnswer(t, translated)
	assert.Equal(t, map[string]any{"behavior": "deny"}, native)
}

func TestQoderTranslateIgnoresAPayloadThatStatesNoDecision(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"response":{"request_id":"x","response":{"jsonrpc":"2.0"}}}`,
		`{"response":{"request_id":"x","response":{"outcome":"proceed_once"}}}`,
		`{"response":{"request_id":"x","response":{"allowed":true}}}`,
	} {
		_, ok := translateQoderCanUseTool([]byte(content))
		assert.False(t, ok, content)
	}
}

func TestQoderTranslateRejectsADecisionItCannotRead(t *testing.T) {
	t.Parallel()
	_, ok := translateQoderCanUseTool(browserAnswer(t, "approval:1", "allow_session", "", nil))
	assert.False(t, ok, "only allow and deny are translated")
}

// DecodeControlUpdatedInput is the one reader of the neutral envelope's
// modified input. A present non-object is absent: a tool input is an object.
func TestDecodeControlUpdatedInput(t *testing.T) {
	t.Parallel()
	assert.Nil(t, agent.DecodeControlUpdatedInput([]byte(``)))
	assert.Nil(t, agent.DecodeControlUpdatedInput([]byte(`not json`)))
	assert.Nil(t, agent.DecodeControlUpdatedInput([]byte(`{"response":{"request_id":"x","response":{"behavior":"allow"}}}`)))
	assert.Nil(t, agent.DecodeControlUpdatedInput([]byte(`{"response":{"request_id":"x","response":{"behavior":"allow","updatedInput":"text"}}}`)))
	assert.Equal(t, map[string]any{"command": "ls"},
		agent.DecodeControlUpdatedInput([]byte(`{"response":{"request_id":"x","response":{"behavior":"allow","updatedInput":{"command":"ls"}}}}`)))
}
