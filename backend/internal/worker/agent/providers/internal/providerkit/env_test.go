package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	}, agent.Options{})

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

	env := FinalizeAgentEnv([]string{"PATH=/usr/bin"}, agent.Options{
		ExtraEnv: []string{"LEAPMUX_CONTROL_SOCKET=/tmp/sock"},
	})

	assert.Contains(t, env, "LEAPMUX_CONTROL_SOCKET=/tmp/sock")
	assert.Contains(t, env, gitutil.GitOptionalLocksOff)
	assert.Contains(t, env, "LEAPMUX_WORKER=1")
}

// TestFinalizeAgentEnv_KeepsTheLaunchEnv pins the other half of
// TestBuildShellWrappedCommand_LaunchEnvReachesTheProcess: the environment a
// launch requires (ELECTRON_RUN_AS_NODE for ZCode's Electron-as-Node runtime)
// survives the scrub every caller finishes with, which removes only the
// agent-identity and LEAPMUX_CONTROL_ keys. A hand merge used to have to run here.
func TestFinalizeAgentEnv_KeepsTheLaunchEnv(t *testing.T) {
	env := FinalizeAgentEnv([]string{"PATH=/usr/bin", "ELECTRON_RUN_AS_NODE=1"}, agent.Options{})
	assert.Contains(t, env, "ELECTRON_RUN_AS_NODE=1",
		"FinalizeAgentEnv scrubs only the agent-identity and LEAPMUX_CONTROL_ keys")
}

// TestFinalizeAgentEnv_ScrubsAgentIdentity verifies the single chokepoint
// every provider funnels through strips inherited agent-harness identity vars
// (so a worker launched from inside another agent's session doesn't spawn a
// nested one) while preserving the per-provider rc markers and auth/config the
// providers re-add before the call.
func TestFinalizeAgentEnv_ScrubsAgentIdentity(t *testing.T) {
	t.Parallel()

	// Assert against the production list itself rather than a hand-maintained
	// subset, so EVERY scrub key is exercised and a newly-added key (or a typo
	// in an oddly-shaped one like "_EXTENSION_OPENCODE_PORT") is automatically
	// covered.
	identity := agentIdentityEnvScrubKeys
	// Must survive: per-provider rc markers / entrypoint re-added BEFORE
	// FinalizeAgentEnv, plus auth tokens, provider-selection, and config dirs.
	mustSurvive := []string{
		"CLAUDECODE", "CODEX_CI", "OPENCODE_CLIENT", "KILO_CLIENT", "CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS",
		"CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY",
		"CLAUDE_CODE_USE_BEDROCK", "CODEX_HOME", "GOOSE_MODEL", "PI_CODING_AGENT_DIR", "PATH",
	}

	buildEnv := func() []string {
		var env []string
		for _, k := range identity {
			env = append(env, k+"=leaked")
		}
		env = append(env,
			"CLAUDECODE=1", "CODEX_CI=1", "OPENCODE_CLIENT=1", "KILO_CLIENT=1",
			"CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
			"CLAUDE_CODE_OAUTH_TOKEN=tok", "OPENAI_API_KEY=sk-test", "CODEX_API_KEY=sk-codex",
			"CLAUDE_CODE_USE_BEDROCK=1", "CODEX_HOME=/home/u/.codex", "GOOSE_MODEL=gpt-x",
			"PI_CODING_AGENT_DIR=/home/u/.pi", "PATH=/usr/bin:/bin",
		)
		return env
	}

	t.Run("strips identity, keeps markers, adds worker flag", func(t *testing.T) {
		out := FinalizeAgentEnv(buildEnv(), agent.Options{})

		for _, k := range identity {
			assert.Falsef(t, envutil.HasKey(out, k), "identity var %q must be scrubbed", k)
		}
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
		assert.True(t, envutil.HasKey(out, "LEAPMUX_WORKER"), "LEAPMUX_WORKER=1 must be added")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
	})

	t.Run("scrub precedes LEAPMUX_CONTROL strip and ExtraEnv append", func(t *testing.T) {
		env := append(buildEnv(), "LEAPMUX_CONTROL_OLD=stale")
		out := FinalizeAgentEnv(env, agent.Options{ExtraEnv: []string{"LEAPMUX_CONTROL_NEW=fresh"}})

		// Identity scrub still applied even on the ExtraEnv path.
		for _, k := range identity {
			assert.Falsef(t, envutil.HasKey(out, k), "identity var %q must be scrubbed", k)
		}
		// Inherited LEAPMUX_CONTROL_* stripped; the injected one wins.
		assert.NotContains(t, out, "LEAPMUX_CONTROL_OLD=stale")
		assert.Contains(t, out, "LEAPMUX_CONTROL_NEW=fresh")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
		// Markers + auth still survive on this path too.
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
	})

	t.Run("strips inherited LEAPMUX_CONTROL even with no ExtraEnv", func(t *testing.T) {
		// A worker spawned inside another worker's session inherits the
		// parent's LEAPMUX_CONTROL_* but injects no fresh ExtraEnv. The stale
		// remote context must still be shed so the child doesn't act on it.
		env := append(buildEnv(), "LEAPMUX_CONTROL_OLD=stale")
		out := FinalizeAgentEnv(env, agent.Options{})

		assert.NotContains(t, out, "LEAPMUX_CONTROL_OLD=stale")
		assert.False(t, envutil.HasKey(out, "LEAPMUX_CONTROL_OLD"), "inherited LEAPMUX_CONTROL_* must be stripped")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
	})
}
