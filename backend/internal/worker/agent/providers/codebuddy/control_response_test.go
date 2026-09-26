package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// nativeAnswer reads CodeBuddy's own answer out of a translated envelope.
func nativeAnswer(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Response  struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope), string(content))
	assert.Equal(t, "control_response", envelope.Type)
	assert.Equal(t, "success", envelope.Response.Subtype)
	assert.Equal(t, envelope.RequestID, envelope.Response.RequestID)
	return envelope.Response.Response
}

func TestCodebuddyTranslateAllowWithoutAnUpdateStatesTheOriginalShape(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "allow", "", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": true}, nativeAnswer(t, translated))
}

// An allow carries the input the browser folded its answer into. An
// AskUserQuestion reply is the whole tool input with `answers` added, and
// CodeBuddy merges that object into the tool call.
func TestCodebuddyTranslateAllowForwardsUpdatedInput(t *testing.T) {
	t.Parallel()
	updated := map[string]any{
		"questions": []any{map[string]any{"question": "Color?"}},
		"answers":   map[string]any{"Color?": "Red"},
	}
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "allow", "", updated))
	require.True(t, ok)
	native := nativeAnswer(t, translated)
	assert.Equal(t, true, native["allowed"])
	require.IsType(t, map[string]any{}, native["updatedInput"])
	assert.Equal(t, "Red", native["updatedInput"].(map[string]any)["answers"].(map[string]any)["Color?"])
}

func TestCodebuddyTranslateDenyCarriesTheReason(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "deny", "Use a dry run.", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": false, "reason": "Use a dry run."}, nativeAnswer(t, translated))
}

// A bare deny states no typed reason. CodeBuddy's answer always carries one, so
// the placeholder fills it.
func TestCodebuddyTranslateBareDenyFillsThePlaceholder(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "deny", "", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": false, "reason": agent.ControlRejectedByUserMessage}, nativeAnswer(t, translated))
}

func TestCodebuddyTranslateIgnoresAPayloadThatStatesNoDecision(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"response":{"request_id":"x","response":{"jsonrpc":"2.0"}}}`,
		`{"response":{"request_id":"x","response":{"outcome":"proceed_once"}}}`,
	} {
		_, ok := translateCanUseToolAnswer([]byte(content))
		assert.False(t, ok, content)
	}
}
