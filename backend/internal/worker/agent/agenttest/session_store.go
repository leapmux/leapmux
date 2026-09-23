package agenttest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionStoreFixture seeds one provider's store under home for workingDir, and
// returns the handle that the reader must find.
type sessionStoreFixture func(t *testing.T, home, workingDir string) string

// RequireReadsSessionStore seeds a store with seed under a temporary home, and
// requires that plugin lists the session that seed wrote.
//
// A provider passes the plugin of its own registration. Seeding a store and
// asserting that the provider finds it proves the thing that matters: the
// reader exists AND the registration wires it up. A comparison of method values
// cannot prove that: Go promotes an embedded method, and nothing in reflect
// reports whether a plugin declared ListStoredSessions or inherited it.
func RequireReadsSessionStore(t *testing.T, plugin agent.Provider, seed sessionStoreFixture) {
	t.Helper()
	home := t.TempDir()
	// Under the temp home, never the developer's real one: this walks provider
	// stores, and a test must not read a person's history.
	dir := filepath.Join(home, "workspace", "project")
	want := seed(t, home, dir)

	got, err := plugin.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Contains(t, Handles(got), want,
		"the provider must find the session seeded in its own store; check that its registration wires the reader")
}
