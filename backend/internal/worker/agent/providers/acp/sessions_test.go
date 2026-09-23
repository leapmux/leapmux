package acp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACPProviderWithoutAReaderListsNothing pins that Provider stays
// provider-neutral: it declares no session reader of its own, so an ACP
// provider that states none inherits the empty default, and Provider knows
// nothing about where any provider's store is.
func TestACPProviderWithoutAReaderListsNothing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sessions, err := Provider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: filepath.Join(home, "workspace", "project"),
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}
