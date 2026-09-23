package providers

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
)

// TestEveryRegisteredProviderTreatsAnAbsentStoreAsEmpty pins the other half of
// the contract: a CLI the user never ran lists nothing and fails nothing, so an
// empty picker never becomes an error banner.
func TestEveryRegisteredProviderTreatsAnAbsentStoreAsEmpty(t *testing.T) {
	t.Parallel()

	registry := Registry()
	for _, id := range registry.Providers() {
		t.Run(id.String(), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			got, err := registry.Plugin(id).ListStoredSessions(context.Background(), agent.StoredSessionQuery{
				WorkingDir: filepath.Join(home, "workspace", "project"),
				HomeDir:    home,
				Getenv:     func(string) string { return "" },
			})
			assert.NoErrorf(t, err, "%v: an absent store is the normal state, not a failure", id)
			assert.Emptyf(t, got, "%v", id)
		})
	}
}
