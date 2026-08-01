package gitutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
)

// TestNewGitCmd_DeclinesOptionalLocks pins the env var that keeps read-only
// probes (git status) from taking .git/index.lock and colliding with a
// concurrent checkout/worktree-add in the same repo.
func TestNewGitCmd_DeclinesOptionalLocks(t *testing.T) {
	t.Parallel()
	cmd := NewGitCmd(context.Background(), "status", "--porcelain")

	values := envutil.ValuesFor(cmd.Env, "GIT_OPTIONAL_LOCKS")
	require.Len(t, values, 1, "every git command must decline optional locks exactly once; see NewGitCmd")
	assert.Equal(t, "0", values[0])
}

// The pinned vars have to survive an ambient value, not sit behind one -- and
// the ambient value is not hypothetical: a worker started from a LeapMux
// terminal or agent inherits GIT_OPTIONAL_LOCKS=0 from the very spawn env this
// package sets, so an append-only NewGitCmd handed git two of them (and made
// the assertion above fail on a developer's own machine).
//
// Driven from gitEnvPins rather than a hand-written list, so a fourth pin is
// covered the moment it is added instead of the day someone remembers to
// extend this test.
//
// Not t.Parallel: t.Setenv forbids it, and the mutation is process-wide.
func TestNewGitCmd_OverridesInheritedValues(t *testing.T) {
	for _, pin := range gitEnvPins {
		name, want, ok := strings.Cut(pin, "=")
		require.True(t, ok, "gitEnvPins entries are KEY=value")
		// An inherited value that differs from the pin, so a missing filter
		// shows up as two entries AND as the wrong one winning.
		t.Setenv(name, "inherited-"+want)
	}

	cmd := NewGitCmd(context.Background(), "status", "--porcelain")

	for _, pin := range gitEnvPins {
		name, want, _ := strings.Cut(pin, "=")
		values := envutil.ValuesFor(cmd.Env, name)
		require.Len(t, values, 1, "%s must appear exactly once, not layered over the inherited value", name)
		assert.Equal(t, want, values[0], "%s must be the value NewGitCmd pins", name)
	}
}

// The filter takes only the names gitEnvPins assigns, not a broad sweep: git
// needs the rest of the environment (PATH to find its own helpers, HOME/GIT_DIR,
// the credential and proxy settings a user's repo depends on), so over-filtering
// would break real clones while every assertion about the pinned vars still
// passed.
func TestNewGitCmd_KeepsTheRestOfTheEnvironment(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("LEAPMUX_GITUTIL_CANARY", "kept")

	cmd := NewGitCmd(context.Background(), "status", "--porcelain")

	assert.Equal(t, []string{"kept"}, envutil.ValuesFor(cmd.Env, "LEAPMUX_GITUTIL_CANARY"),
		"an unrelated inherited var must survive the filter")
	// Stated as the inverse of what the filter drops, so it holds on any
	// machine rather than counting entries a developer's own shell affects.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if isPinnedGitEnvKey(name) {
			continue
		}
		assert.Contains(t, cmd.Env, kv, "NewGitCmd dropped an inherited variable it does not pin")
	}
}

// isPinnedGitEnvKey reports whether name is one of the keys gitEnvPins
// replaces, matching case-insensitively as envutil.PinEnv does.
func isPinnedGitEnvKey(name string) bool {
	for _, pin := range gitEnvPins {
		if key, _, _ := strings.Cut(pin, "="); strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
