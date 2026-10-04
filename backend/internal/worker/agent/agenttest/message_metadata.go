package agenttest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// TurnToolUseCounts reads explicit tool counts from recorded turn-end metadata.
func TurnToolUseCounts(t *testing.T, messages []Message) []int {
	t.Helper()
	var counts []int
	for _, row := range messages {
		if !row.TurnEnd {
			continue
		}
		require.NotEmpty(t, row.Metadata, "a native turn end needs explicit tool-count metadata, including zero")
		var metadata map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(row.Metadata, &metadata))
		require.Contains(t, metadata, contracts.MessageMetadataFieldToolUses)
		var count int
		require.NoError(t, json.Unmarshal(metadata[contracts.MessageMetadataFieldToolUses], &count))
		require.GreaterOrEqual(t, count, 0, "a native tool count cannot be negative")
		counts = append(counts, count)
	}
	return counts
}
