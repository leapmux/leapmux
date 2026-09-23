package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each provider that carries the sentinel builds its row through
// agent.AccountDefaultModelEntry, so the row cannot drift between the providers.
// TestAccountDefaultModelEntry pins the shape of that row.
func TestCodexCatalogCarriesTheAccountDefaultEntry(t *testing.T) {
	t.Parallel()

	row := agent.FindAvailableModel(codexDefaultModels, agent.DefaultModelSentinel)
	require.NotNil(t, row)
	assert.Equal(t, "Default (recommended)", row.DisplayName)
	assert.Empty(t, row.SupportedEfforts)
	assert.Zero(t, row.ContextWindow)
}
