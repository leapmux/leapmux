package agent

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFinalizeAgentEnv_DeclinesOptionalLocks pins the env var that keeps the
// AGENT's git polling out of the index-lock contention set.
//
// The worker setting it on its own commands covered only one of three
// contenders. A coding agent runs `git status` continuously, and that probe
// takes .git/index.lock purely to write back a refreshed index -- enough to
// kill a concurrent worker checkout with "Another git process seems to be
// running", which surfaced as an agent that failed to start mid-checkout and
// whose rollback then restored the user's original branch.
// The fixture carries inherited values for BOTH pinned keys on purpose. Every
// caller passes cmd.Environ(), and a worker launched from a LeapMux terminal or
// agent already carries these -- so a fixture without them cannot tell "pinned
// exactly once" from "appended blindly onto an env that happened to be clean",
// which is how this assertion sat green while production emitted duplicates.
func TestFinalizeAgentEnv_DeclinesOptionalLocks(t *testing.T) {
	t.Parallel()

	env := FinalizeAgentEnv([]string{
		"PATH=/usr/bin",
		"GIT_OPTIONAL_LOCKS=1",
		"LEAPMUX_WORKER=0",
	}, Options{})

	for key, want := range map[string]string{
		"GIT_OPTIONAL_LOCKS": "0",
		"LEAPMUX_WORKER":     "1",
	} {
		values := envutil.ValuesFor(env, key)
		require.Len(t, values, 1, "%s must be pinned exactly once, not layered over the inherited value", key)
		assert.Equal(t, want, values[0], "%s must be the value FinalizeAgentEnv pins", key)
	}
	assert.Equal(t, "GIT_OPTIONAL_LOCKS=0", gitutil.GitOptionalLocksOff,
		"the exported constant is what every spawn path shares")
}

// TestFinalizeAgentEnv_ExtraEnvSurvivesTheLockSetting guards the append order:
// the lock setting must not displace a caller's ExtraEnv, which is where the
// fresh LEAPMUX_CONTROL_* values arrive.
func TestFinalizeAgentEnv_ExtraEnvSurvivesTheLockSetting(t *testing.T) {
	t.Parallel()

	env := FinalizeAgentEnv([]string{"PATH=/usr/bin"}, Options{
		ExtraEnv: []string{"LEAPMUX_CONTROL_SOCKET=/tmp/sock"},
	})

	assert.Contains(t, env, "LEAPMUX_CONTROL_SOCKET=/tmp/sock")
	assert.Contains(t, env, gitutil.GitOptionalLocksOff)
	assert.Contains(t, env, "LEAPMUX_WORKER=1")
}
