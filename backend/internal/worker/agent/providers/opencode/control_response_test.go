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

// A row whose payload states another request than its key disagrees with that
// key, so LeapMux cannot read the request that the answer is for.
func TestNativeWorkerControlRefusesARequestThatDisagreesWithItsKey(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		kind    string
		request json.RawMessage
	}{
		"a permission": {"permission", json.RawMessage(`{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell","resources":[]}}`)},
		"a form":       {"form", json.RawMessage(`{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","fields":[{"key":"count","type":"integer"}]}}}`)},
	} {
		for _, key := range []string{
			openCodeControlKey(tc.kind, "ses_1", "other"),
			openCodeControlKey(tc.kind, "ses_other", map[string]string{"permission": "per_1", "form": "frm_1"}[tc.kind]),
		} {
			answer := agent.ControlAnswer{Content: agent.ControlResponseEnvelope(key, map[string]any{"behavior": "deny"})}
			result := agenttest.ResolveAnswer(Registration().Plugin, tc.request, key, answer)
			assert.True(t, result.Withhold, "%s under %s", name, key)
			assert.EqualError(t, result.Refusal(), agent.RefusalUnreadableRequest, "%s under %s", name, key)
		}
	}
}
