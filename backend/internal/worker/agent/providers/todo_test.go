package providers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestACPExtractTodoEvent_ServesEveryACPProvider pins that every ACP provider
// reads the shared plan shape.
func TestACPExtractTodoEvent_ServesEveryACPProvider(t *testing.T) {
	t.Parallel()

	registry := Registry()
	const plan = `{"sessionUpdate":"plan","entries":[{"content":"one","status":"pending"}]}`
	for _, provider := range acpProvidersForTest() {
		ev, ok := registry.Plugin(provider).ExtractTodoEvent("", []byte(plan), nil)
		require.True(t, ok, "provider %s", provider)
		require.Len(t, ev.Snapshot, 1)
	}
}
