package kiro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// isolatedHome is the home directory of every test of this package.
var isolatedHome string

// kiroFakeCLIEnv marks the process of the fake Kiro of the start tests, which
// runs this test binary.
const kiroFakeCLIEnv = "GO_WANT_HELPER_PROCESS_KIRO"

// TestMain runs the tests of this package under a home directory of their
// own. The session picker reads `~/.kiro/sessions` when a query states no home,
// and the user's own store holds sessions of a real Kiro account. A test that
// forgets its home then reads an empty directory, never the user's.
//
// The fake Kiro of the start tests runs this binary too, and it keeps the home
// of the test that started it.
func TestMain(m *testing.M) {
	if os.Getenv(kiroFakeCLIEnv) == "1" {
		os.Exit(m.Run())
	}
	home, err := os.MkdirTemp("", "kiro-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create the test home:", err)
		os.Exit(2)
	}
	isolatedHome = home
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			fmt.Fprintln(os.Stderr, "set the test home:", err)
			os.Exit(2)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func TestKiroTestsRunUnderAnIsolatedHome(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, isolatedHome, home)

	// A query that states no home reads the isolated one.
	dir := t.TempDir()
	sessions, err := kiroStoredSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: dir, Getenv: agenttest.FixtureEnv(nil)})
	require.NoError(t, err)
	assert.Empty(t, sessions)
	assert.NoDirExists(t, filepath.Join(home, kiroStoreDir), "the read creates nothing")
}
