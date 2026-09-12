package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestControlResponseRestoresTheNativeRequestID(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		for _, wireID := range []string{`42`, `0`, `-5`, `1e3`, `9007199254740993`, `"42"`, `"001"`, `"abc-123"`} {
			t.Run(provider.String()+"/"+wireID, func(t *testing.T) {
				request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/request_permission","params":{}}`, wireID))
				_, requestID, ok := ExtractJSONRPCID(request)
				require.True(t, ok)
				response := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"count":0,"enabled":false,"text":"","large":9007199254740993}}`, requestID))
				plugin := ProviderFor(provider)
				require.Equal(t, requestID, plugin.ControlResponseRequestID(response))
				resolved := plugin.ResolveControlResponse(ControlResponseContext{RequestPayload: request, ResponseContent: response})
				require.False(t, resolved.Withhold)
				var actual map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(resolved.Content, &actual))
				require.Equal(t, wireID, string(actual["id"]))
				require.Equal(t, `{"count":0,"enabled":false,"text":"","large":9007199254740993}`, string(actual["result"]))
			})
		}
	}
}

func TestControlResponseIDReplacementPreservesEveryOtherByte(t *testing.T) {
	response := []byte(" { \"id\" : \"jsonrpc:9007199254740993\", \"extra\":1, \"extra\":2, \"result\":{\"large\":9999999999999999999999999,\"enabled\":false} } \n")
	resolution := restoreControlResponseID(ControlResponseContext{
		RequestID:       "jsonrpc:9007199254740993",
		RequestPayload:  []byte(`{"id":9007199254740993,"method":"session/request_permission"}`),
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)
	expected := bytes.Replace(response, []byte(`"jsonrpc:9007199254740993"`), []byte(`9007199254740993`), 1)
	require.Equal(t, expected, resolution.Content)
}

func TestControlResponseRejectsAnUnmatchedNativeRequestID(t *testing.T) {
	for _, request := range []string{
		`{"id":"another-request","method":"session/request_permission"}`,
		`{"id":null,"method":"session/request_permission"}`,
		`{"method":"session/request_permission"}`,
		`{malformed`,
	} {
		resolution := restoreControlResponseID(ControlResponseContext{
			RequestPayload: []byte(request), ResponseContent: []byte(`{"id":"request","result":false}`),
		})
		require.True(t, resolution.Withhold, request)
	}
}

func TestControlResponseIDKeepsUnrelatedEnvelopesAndExactMatchingBytes(t *testing.T) {
	for _, response := range []string{
		` {"id":7,"result":false,"future":9007199254740993} `,
		`{"response":{"request_id":"request","response":{"behavior":"allow"}}}`,
		`{"id":"request","method":"session/notify"}`,
		`{"id":"request"}`,
		`{malformed`,
	} {
		resolution := restoreControlResponseID(ControlResponseContext{
			RequestPayload: []byte(`{"id":7,"method":"session/request_permission"}`), ResponseContent: []byte(response),
		})
		require.False(t, resolution.Withhold, response)
		require.Equal(t, response, string(resolution.Content))
	}
}
