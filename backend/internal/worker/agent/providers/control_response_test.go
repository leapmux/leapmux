package providers

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestControlResponseRequestID pins that every provider extracts the same id
// from the same bytes. Both wire shapes cross providers, so each case runs over
// every registered plugin and the neutral default. That makes "identical across
// providers" a property, not an accident of one provider's resolver.
func TestControlResponseRequestID(t *testing.T) {
	t.Parallel()

	registry := Registry()
	plugins := map[string]agent.Provider{"defaults": agent.ProviderDefaults{}}
	for _, provider := range registry.Providers() {
		plugins[provider.String()] = registry.Plugin(provider)
	}
	cases := []struct {
		name    string
		content string
		want    string
	}{
		// Neutral approve/reject envelope: response.request_id.
		{"envelope", `{"response":{"request_id":"req-1","response":{"behavior":"allow"}}}`, "req-1"},
		// Mixed envelopes still belong to the nested control response. A top-level JSON-RPC id can be
		// present for provider plumbing, but the pending control_request row is keyed by
		// response.request_id, so the nested id wins.
		{"mixed nested wins", `{"id":"jsonrpc-req","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`, "req-1"},
		// JSON-RPC numeric id (ACP family).
		{"jsonrpc numeric", `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"once"}}}`, "5"},
		// JSON-RPC string id.
		{"jsonrpc string", `{"jsonrpc":"2.0","id":"abc-123","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`, "abc-123"},
		{"no id", `{"type":"unknown"}`, ""},
		{"null id", `{"id":null}`, ""},
		{"invalid json", `not json`, ""},
	}
	for name, plugin := range plugins {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, plugin.ControlResponseRequestID([]byte(tc.content)))
			})
		}
	}
}

// TestControlResponsePreservesNativeAnswerBytes pins that a provider forwards a
// native answer byte for byte: its resolver keeps the content, withholds
// nothing, and states no plan-mode change.
func TestControlResponsePreservesNativeAnswerBytes(t *testing.T) {
	t.Parallel()

	registry := Registry()
	type nativeAnswerCase struct {
		name     string
		provider leapmuxv1.AgentProvider
		request  string
		response string
	}
	cases := []nativeAnswerCase{
		{"codex questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"id":7,"method":"item/tool/requestUserInput","params":{"questions":[{"id":"task","header":"Task"}]}}`, " {\"id\":7,\"result\":{\"answers\":{\"task\":{\"answers\":[\"Inspect\"]}}},\"unknown\":9007199254740993}\n"},
		{"cursor questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, `{"id":7,"method":"cursor/ask_question","params":{"questions":[{"id":"color","prompt":"Choose","options":[{"id":"red","label":"Red"}]}]}}`, `{"id":7,"result":{"outcome":{"outcome":"answered","answers":[{"questionId":"color","selectedOptionIds":["red"]}]}}}`},
		{"opencode questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"question.asked","properties":{"questions":[{"header":"Task"}]}}`, `{"id":7,"result":{"answers":[["Inspect"]]}}`},
		{"kilo questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, `{"type":"question.asked","properties":{"questions":[{"header":"Task"}]}}`, `{"id":7,"result":{"answers":[["Inspect"]]}}`},
		// MiMo sends each answer to its own HTTP route, so the answer keeps the shape
		// the browser wrote, and the persisted row shows that shape.
		{"mimo questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"question.asked","properties":{"id":"que_1","questions":[{"header":"Task"}]},"request":{"tool_name":"question"}}`, `{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["Inspect"]]}}`},
		{"mimo permission option", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"permission.asked","properties":{"id":"per_1","permission":"bash"},"request":{"tool_name":"bash"}}`, `{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`},
	}
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
	} {
		for _, options := range []string{`[]`, `[{"optionId":"once","name":"Allow once","kind":"allow_once"}]`} {
			cases = append(cases, nativeAnswerCase{provider.String() + options, provider, `{"id":7,"method":"session/request_permission","params":{"options":` + options + `}}`, `{"id":7,"result":{"outcome":{"optionId":"once"}}}`})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := []byte(tc.response)
			resolved := registry.Plugin(tc.provider).ResolveControlResponse(agent.ControlResponseContext{RequestPayload: []byte(tc.request), ResponseContent: response})
			assert.Equal(t, response, resolved.Content)
			assert.False(t, resolved.Withhold)
			assert.Equal(t, agent.PlanModeControlNone, resolved.PlanModeControl)
		})
	}
}

func TestControlResponseRestoresTheNativeRequestID(t *testing.T) {
	registry := Registry()
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
	} {
		for _, wireID := range []string{`42`, `0`, `-5`, `1e3`, `9007199254740993`, `"42"`, `"001"`, `"abc-123"`} {
			t.Run(provider.String()+"/"+wireID, func(t *testing.T) {
				request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/request_permission","params":{}}`, wireID))
				_, requestID, ok := agent.ExtractJSONRPCID(request)
				require.True(t, ok)
				response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"count":0,"enabled":false,"text":"","large":9007199254740993}}`, requestID))
				plugin := registry.Plugin(provider)
				require.Equal(t, requestID, plugin.ControlResponseRequestID(response))
				resolved := plugin.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: response})
				require.False(t, resolved.Withhold)
				var actual map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(resolved.Content, &actual))
				require.Equal(t, wireID, string(actual["id"]))
				require.Equal(t, `{"count":0,"enabled":false,"text":"","large":9007199254740993}`, string(actual["result"]))
			})
		}
	}
}
