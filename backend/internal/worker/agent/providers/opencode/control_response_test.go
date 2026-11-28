package opencode

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each answer addresses the stored request, as every answer that the Worker
// resolves does.
func TestNativeWorkerControlTranslation(t *testing.T) {
	t.Parallel()
	permission := json.RawMessage(`{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell","resources":[],"save":["*"]}}`)
	form := json.RawMessage(`{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","fields":[{"key":"count","type":"integer"},{"key":"enabled","type":"boolean"}]}}}`)
	externalForm := json.RawMessage(`{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","fields":[{"key":"open","type":"external","url":"https://example.test/auth"}]}}}`)
	for _, tc := range []struct {
		name, kind, response, expected string
		request                        json.RawMessage
		refused                        bool
	}{
		{"allow", "permission", `{"response":{"request_id":"%s","response":{"behavior":"allow"}}}`, `"decision":"once"`, permission, false},
		{"deny with text", "permission", `{"response":{"request_id":"%s","response":{"behavior":"deny","message":"keep the file"}}}`, `"message":"keep the file"`, permission, false},
		{"selected permission", "permission", `{"id":"%s","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`, `"decision":"always"`, permission, false},
		{"typed form", "form", `{"id":"%s","result":{"answer":{"count":0,"enabled":false}}}`, `"answer":{"count":0,"enabled":false}`, form, false},
		{"cancel form", "form", `{"id":"%s","result":{"cancel":true}}`, `"cancel":true`, form, false},
		{"typed elicitation", "form", `{"response":{"request_id":"%s","response":{"action":"accept","content":{"count":0,"enabled":false}}}}`, `"answer":{"count":0,"enabled":false}`, form, false},
		{"empty elicitation", "form", `{"response":{"request_id":"%s","response":{"action":"accept","content":{}}}}`, `"answer":{}`, form, false},
		{"url elicitation", "form", `{"response":{"request_id":"%s","response":{"action":"accept"}}}`, `"answer":{"open":true}`, externalForm, false},
		{"declined elicitation", "form", `{"response":{"request_id":"%s","response":{"action":"decline"}}}`, `"cancel":true`, form, false},
		{"cancelled elicitation", "form", `{"response":{"request_id":"%s","response":{"action":"cancel"}}}`, `"cancel":true`, form, false},
		{"invalid elicitation action", "form", `{"response":{"request_id":"%s","response":{"action":"unknown"}}}`, "", form, true},
		{"invalid elicitation content", "form", `{"response":{"request_id":"%s","response":{"action":"accept","content":0}}}`, "", form, true},
		{"mixed permission envelopes", "permission", `{"id":"%[1]s","result":{"outcome":{"outcome":"selected","optionId":"always"}},"response":{"request_id":"%[1]s","response":{"action":"cancel"}}}`, "", permission, true},
		{"mixed form envelopes", "form", `{"id":"%[1]s","result":{"answer":{"count":1}},"response":{"request_id":"%[1]s","response":{"action":"cancel"}}}`, `"cancel":true`, form, false},
		{"missing request", "permission", `{"id":"%s","result":{"decision":"once"}}`, "", nil, true},
		{"unknown option", "permission", `{"id":"%s","result":{"outcome":{"outcome":"selected","optionId":"unknown"}}}`, "", permission, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key := openCodeControlKey(tc.kind, "ses_1", map[string]string{"permission": "per_1", "form": "frm_1"}[tc.kind])
			response := []byte(fmt.Sprintf(tc.response, key))
			result := agenttest.ResolveAnswer(Registration().Plugin, tc.request, key, agent.ControlAnswer{Content: response})
			assert.Equal(t, tc.refused, result.Withhold)
			if tc.refused {
				return
			}
			_, err := readOpenCodeControlAnswer(result.Content, key, tc.kind)
			require.NoError(t, err)
			assert.Contains(t, string(result.Content), tc.expected)
		})
	}
}

// The key of a stored native request is its kind, the session that the
// request states and its id, so a payload of another kind, session or id
// gives another key, and the Worker refuses the row before OpenCode reads it
// (agent.StoredControlRequestAgrees). A payload that states no id or no
// session gives no key.
func TestNativeWorkerControlRequestKeyReadsTheKindTheSessionAndTheID(t *testing.T) {
	t.Parallel()
	for payload, want := range map[string]string{
		`{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell","resources":[]}}`:                  openCodeControlKey("permission", "ses_1", "per_1"),
		`{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","fields":[{"key":"count","type":"integer"}]}}}`: openCodeControlKey("form", "ses_1", "frm_1"),
	} {
		key, ok := opencodeProvider{}.ControlRequestKey([]byte(payload), "")
		assert.True(t, ok, payload)
		assert.Equal(t, want, key, payload)
	}
	for _, payload := range []string{
		`{"type":"permission.asked","data":{"sessionID":"ses_1"}}`,
		`{"type":"permission.asked","data":{"id":"per_1"}}`,
		`{"type":"permission.asked","data":{"id":1,"sessionID":"ses_1"}}`,
		`{"type":"permission.asked","data":[]}`,
		`{"type":"form.created","data":{"id":"frm_1","sessionID":"ses_1"}}`,
		`{"type":"form.created","data":{"form":"frm_1"}}`,
		`{"type":"question.asked","properties":{"id":"que_1"}}`,
		`{"data":{"id":"per_1","sessionID":"ses_1"}}`,
	} {
		key, ok := opencodeProvider{}.ControlRequestKey([]byte(payload), "ses_1")
		assert.False(t, ok, payload)
		assert.Empty(t, key, payload)
	}
}
