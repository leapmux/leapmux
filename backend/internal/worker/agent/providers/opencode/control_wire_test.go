package opencode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodePermissionOptionalSourceAndEmptyResources(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"", `,"source":{"type":"tool","messageID":"msg_1","id":"call_1"}`} {
		permission, err := readOpenCodePermission(json.RawMessage(`{"id":"per_1","sessionID":"ses_1","action":"shell","resources":[]` + source + `}`))
		require.NoError(t, err)
		require.NotNil(t, permission.Resources)
		assert.Empty(t, *permission.Resources)
	}
}

func TestOpenCodePermissionRejectsInvalidOwnerAndSource(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"id":"per_1","sessionID":"global","resources":[]}`, `{"id":"per_1","sessionID":"../other","resources":[]}`, `{"id":"per_1","sessionID":"ses_1"}`, `{"id":"per_1","sessionID":"ses_1","resources":[],"source":{}}`} {
		_, err := readOpenCodePermission(json.RawMessage(raw))
		require.Error(t, err)
	}
}

func TestOpenCodeFormPreservesNativeFieldConstraintsAndZeroDefaults(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"id":"frm_1","sessionID":"global","title":"Native typed form","metadata":{"kind":"mcp-elicitation","server":"probe"},"fields":[{"key":"count","type":"integer","minimum":0,"default":0},{"key":"enabled","type":"boolean","default":false},{"key":"","type":"string","default":"","when":[{"key":"enabled","op":"eq","value":false}]}]}`)
	form, err := readOpenCodeForm(raw)
	require.NoError(t, err)
	assert.Equal(t, "global", form.SessionID)
	require.Len(t, form.Fields, 3)
	assert.JSONEq(t, `{"key":"enabled","type":"boolean","default":false}`, string(form.Fields[1].Raw))
	assert.Contains(t, string(form.Fields[2].Raw), `"value":false`)
}

func TestOpenCodeFormRejectsMissingFieldsAndUnsafeRoutes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"id":"frm_1","sessionID":"ses_1","fields":[]}`, `{"id":"frm_1","sessionID":"ses_1","fields":null}`, `{"id":"frm_1","sessionID":"ses_1","fields":[{"type":"string"}]}`, `{"id":"frm_1","sessionID":"ses_1","fields":[{"key":"a","type":"unknown"}]}`, `{"id":"../other","sessionID":"ses_1","fields":[{"key":"a","type":"string"}]}`} {
		_, err := readOpenCodeForm(json.RawMessage(raw))
		require.Error(t, err)
	}
}

func TestOpenCodeControlAnswersPreserveFalseZeroAndEmptySelection(t *testing.T) {
	t.Parallel()
	answer, err := readOpenCodeControlAnswer([]byte(`{"request_id":"control_1","answer":{"count":0,"enabled":false,"text":"","choices":[]}}`), "control_1", "form")
	require.NoError(t, err)
	assert.JSONEq(t, `0`, string(answer.Answer["count"]))
	assert.JSONEq(t, `false`, string(answer.Answer["enabled"]))
	assert.JSONEq(t, `[]`, string(answer.Answer["choices"]))
	for _, raw := range []string{`{"request_id":"control_1","answer":{}}`, `{"request_id":"control_1","cancel":true}`} {
		_, err := readOpenCodeControlAnswer([]byte(raw), "control_1", "form")
		require.NoError(t, err)
	}
	for _, decision := range []string{"once", "always", "reject"} {
		_, err := readOpenCodeControlAnswer([]byte(`{"request_id":"control_1","decision":"`+decision+`","message":""}`), "control_1", "permission")
		require.NoError(t, err)
	}
}

func TestOpenCodeControlAnswerRejectsMismatchedAndMixedReplies(t *testing.T) {
	t.Parallel()
	cases := []struct{ kind, raw string }{
		{"permission", `{"request_id":"other","decision":"once"}`},
		{"permission", `{"request_id":"control_1","decision":"unknown"}`},
		{"permission", `{"request_id":"control_1","decision":"once","answer":{}}`},
		{"form", `{"request_id":"control_1"}`},
		{"form", `{"request_id":"control_1","answer":null}`},
		{"form", `{"request_id":"control_1","answer":{},"cancel":true}`},
		{"form", `{"request_id":"control_1","answer":{},"decision":"once"}`},
		{"form", `{"request_id":"control_1","answer":{"a":null}}`},
		{"form", `{"request_id":"control_1","answer":{"a":{}}}`},
		{"form", `{"request_id":"control_1","answer":{"a":[1]}}`},
	}
	for _, tc := range cases {
		_, err := readOpenCodeControlAnswer([]byte(tc.raw), "control_1", tc.kind)
		require.Error(t, err, tc.raw)
	}
}

func TestOpenCodePermissionKeepsAnEmptyOptionalToolReference(t *testing.T) {
	t.Parallel()
	permission, err := readOpenCodePermission(json.RawMessage(`{"id":"per_1","sessionID":"ses_1","action":"shell","resources":[],"source":{"type":"tool","messageID":"","id":""}}`))
	require.NoError(t, err)
	require.NotNil(t, permission.Source)
	assert.Empty(t, permission.Source.ID)
	assert.Empty(t, permission.Source.MessageID)
}
