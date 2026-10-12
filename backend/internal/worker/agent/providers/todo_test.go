package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

// TestACPExtractTodoEvent_ServesEveryACPProvider pins that every ACP provider
// reads the shared plan shape.
func TestACPExtractTodoEvent_ServesEveryACPProvider(t *testing.T) {
	t.Parallel()

	registry := Registry()
	const plan = `{"sessionUpdate":"plan","entries":[{"content":"one","status":"pending"}]}`
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
	} {
		ev, ok := registry.Plugin(provider).ExtractTodoEvent("", []byte(plan), nil)
		require.True(t, ok, "provider %s", provider)
		require.Len(t, ev.Snapshot, 1)
	}
}
