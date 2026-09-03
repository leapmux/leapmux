package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccountDefaultModelEntry pins the shape every provider's account-default
// row must have. The absent SupportedEfforts is load-bearing, not an oversight:
// effortGroupForModel returns nil on an empty effort list, which is what hides
// the effort menu until the CLI resolves a concrete model -- and that in turn is
// what keeps a fresh launch from forwarding an effort the resolved model may not
// offer. One helper makes that omission impossible for a new provider to lose.
func TestAccountDefaultModelEntry(t *testing.T) {
	t.Parallel()

	entry := accountDefaultModelEntry("Use the account's default model")

	assert.Equal(t, DefaultModelSentinel, entry.Id)
	assert.Equal(t, "Default (recommended)", entry.DisplayName, "every provider shows one label")
	assert.Equal(t, "Use the account's default model", entry.Description)
	assert.True(t, entry.IsDefault, "a new tab starts on the account default")
	assert.Empty(t, entry.SupportedEfforts, "the effort menu appears only once the model resolves")
	assert.Zero(t, entry.ContextWindow, "an unresolved model has no context window to report")
	assert.False(t, entry.Hidden, "the account default must be selectable")

	// Both providers that carry the sentinel go through the helper, so the row
	// cannot drift between them.
	for _, catalog := range [][]*ModelInfo{codexDefaultModels, claudeCodeAvailableModels} {
		row := FindAvailableModel(catalog, DefaultModelSentinel)
		require.NotNil(t, row)
		assert.Equal(t, "Default (recommended)", row.DisplayName)
		assert.Empty(t, row.SupportedEfforts)
		assert.Zero(t, row.ContextWindow)
	}
}
