package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

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
// nothing, and states no plan-mode change. The answer addresses the Worker's
// key of the request, as every answer that the Worker resolves does, so a
// resolver that restores the native JSON-RPC id changes that id alone.
func TestControlResponsePreservesNativeAnswerBytes(t *testing.T) {
	t.Parallel()

	registry := Registry()
	type nativeAnswerCase struct {
		name      string
		provider  leapmuxv1.AgentProvider
		request   string
		requestID string
		// answer is what the Worker resolves, and want is what the runtime
		// receives.
		answer, want string
	}
	// jsonRPCCase is a case whose answer addresses jsonrpc:7, the Worker's key
	// of a JSON-RPC request with the id 7, and whose runtime receives the same
	// bytes with its own id 7.
	jsonRPCCase := func(name string, provider leapmuxv1.AgentProvider, request, want string) nativeAnswerCase {
		answer := strings.Replace(want, `"id":7`, `"id":"jsonrpc:7"`, 1)
		require.NotEqual(t, want, answer, "the answer states the id 7")
		return nativeAnswerCase{name, provider, request, "jsonrpc:7", answer, want}
	}
	cases := []nativeAnswerCase{
		jsonRPCCase("codex questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"id":7,"method":"item/tool/requestUserInput","params":{"questions":[{"id":"task","header":"Task"}]}}`, " {\"id\":7,\"result\":{\"answers\":{\"task\":{\"answers\":[\"Inspect\"]}}},\"unknown\":9007199254740993}\n"),
		jsonRPCCase("cursor questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, `{"id":7,"method":"cursor/ask_question","params":{"questions":[{"id":"color","prompt":"Choose","options":[{"id":"red","label":"Red"}]}]}}`, `{"id":7,"result":{"outcome":{"outcome":"answered","answers":[{"questionId":"color","selectedOptionIds":["red"]}]}}}`),
		// An OpenCode question states no JSON-RPC id, so the answer keeps the key
		// that it addresses.
		{"kilo questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, `{"type":"question.asked","properties":{"id":"que_1","questions":[{"header":"Task"}]}}`, "opencode-question:que_1",
			`{"id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`, `{"id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`},
		// MiMo sends each answer to its own HTTP route, so the answer keeps the shape
		// that AnswerControlRequest wrote, and the persisted row shows that shape.
		{"mimo questions", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"question.asked","properties":{"id":"que_1","questions":[{"header":"Task"}]},"request":{"tool_name":"question"}}`, "mimo-question:que_1",
			`{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["Inspect"]]}}`, `{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["Inspect"]]}}`},
		{"mimo permission option", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"permission.asked","properties":{"id":"per_1","permission":"bash"},"request":{"tool_name":"bash"}}`, "mimo-permission:per_1",
			`{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`, `{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`},
	}
	// GitHub Copilot publishes no JSON-RPC request, so the Worker refuses such a
	// stored request before Copilot reads it (agent.StoredControlRequestAgrees).
	for _, provider := range acpProvidersForTest() {
		for _, options := range []string{`[]`, `[{"optionId":"once","name":"Allow once","kind":"allow_once"}]`} {
			cases = append(cases, jsonRPCCase(provider.String()+options, provider, `{"id":7,"method":"session/request_permission","params":{"options":`+options+`}}`, `{"id":7,"result":{"outcome":{"optionId":"once"}}}`))
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := agenttest.ResolveAnswer(registry.Plugin(tc.provider), []byte(tc.request), tc.requestID, agent.ControlAnswer{Content: []byte(tc.answer)})
			assert.Equal(t, []byte(tc.want), resolved.Content)
			assert.False(t, resolved.Withhold)
			assert.Equal(t, agent.PlanModeControlNone, resolved.PlanModeControl)
		})
	}
}

// Each provider that publishes a JSON-RPC request answers it under its native
// id. GitHub Copilot publishes none: it keys each request of its runtime by
// its session and its own request id, so the Worker refuses a stored JSON-RPC
// request before Copilot reads it (agent.StoredControlRequestAgrees).
func TestControlResponseRestoresTheNativeRequestID(t *testing.T) {
	registry := Registry()
	for _, provider := range append([]leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}, acpProvidersForTest()...) {
		for _, wireID := range []string{`42`, `0`, `-5`, `1e3`, `9007199254740993`, `"42"`, `"001"`, `"abc-123"`} {
			t.Run(provider.String()+"/"+wireID, func(t *testing.T) {
				request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/request_permission","params":{}}`, wireID))
				// The Worker stores the request under its canonical key, and the
				// answer addresses that key.
				identity, ok := agent.NewControlRequestIdentity(json.RawMessage(wireID))
				require.True(t, ok)
				requestID := identity.Key
				response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"count":0,"enabled":false,"text":"","large":9007199254740993}}`, requestID))
				plugin := registry.Plugin(provider)
				require.Equal(t, requestID, plugin.ControlResponseRequestID(response))
				resolved := agenttest.ResolveAnswer(plugin, request, requestID, agent.ControlAnswer{Content: response})
				require.False(t, resolved.Withhold)
				var actual map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(resolved.Content, &actual))
				require.Equal(t, wireID, string(actual["id"]))
				require.Equal(t, `{"count":0,"enabled":false,"text":"","large":9007199254740993}`, string(actual["result"]))
			})
		}
	}
}
