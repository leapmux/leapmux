package providerkit

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
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
		"CLAUDE_CODE_USE_BEDROCK", "CODEX_HOME", "GOOSE_MODEL", "PI_CODING_AGENT_DIR", "GROK_HOME",
		"QWEN_HOME", "MIMOCODE_HOME", "CODEWHALE_HOME", "KIRO_HOME", "AMP_API_KEY", "AMP_URL",
		"CLINE_DIR", "CLINE_DATA_DIR", "CLINE_PROVIDER_SETTINGS_PATH", "PATH",
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
			"PI_CODING_AGENT_DIR=/home/u/.pi", "GROK_HOME=/home/u/.grok", "QWEN_HOME=/home/u/.qwen",
			"MIMOCODE_HOME=/home/u/.mimo", "CODEWHALE_HOME=/home/u/.codewhale", "KIRO_HOME=/home/u/.kiro",
			"AMP_API_KEY=sgamp-test", "AMP_URL=https://amp.example.com",
			"CLINE_DIR=/home/u/.cline", "CLINE_DATA_DIR=/home/u/.cline/data",
			"CLINE_PROVIDER_SETTINGS_PATH=/home/u/.cline/data/settings/providers.json", "PATH=/usr/bin:/bin",
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

// Qwen Code states its session to each shell command that it runs
// (getShellContextEnvVars in qwen-code 0.24), and a Qwen started from such a
// command reads the session id, the project directory and the model back from
// its own environment. Each name is listed here on its own, because the test
// above reads the scrub list and so cannot fail for a name that it lacks.
func TestFinalizeAgentEnvScrubsTheQwenShellContext(t *testing.T) {
	t.Parallel()
	qwenShellContext := []string{
		"QWEN_CODE", "QWEN_CODE_SESSION_ID", "QWEN_CODE_PROJECT_DIR", "QWEN_CODE_CLI",
		"QWEN_CODE_MODEL", "QWEN_CODE_MODEL_IDENTITY", "QWEN_CODE_AGENT_ID", "QWEN_CODE_PROMPT_ID",
	}
	var env []string
	for _, key := range qwenShellContext {
		env = append(env, key+"=parent")
	}

	out := FinalizeAgentEnv(env, agent.Options{})

	for _, key := range qwenShellContext {
		assert.Falsef(t, envutil.HasKey(out, key), "%s of the parent Qwen session must not reach the agent", key)
	}
}

// Each harness below marks the commands that it runs with its own session, and
// a CLI that a worker starts from such a command reads that session back. The
// names are stated here for each harness, for the reason that the Qwen test
// above gives: a test that reads the scrub list cannot fail for a name that the
// list lost.
func TestFinalizeAgentEnvScrubsTheSessionOfEachHarness(t *testing.T) {
	t.Parallel()
	for harness, keys := range map[string][]string{
		"Grok Build": {"GROK_SESSION_ID"},
		"Kiro":       {"KIRO_SESSION_ID"},
		"Oh My Pi":   {"AGENT", "PI_SESSION_FILE"},
		"MiMo Code":  {"MIMOCODE", "MIMOCODE_PID", "MIMOCODE_RUN_ID", "MIMOCODE_PROCESS_ROLE"},
		"Codewhale":  {"CODEWHALE_SANDBOX", "DEEPSEEK_SANDBOX", "CODEWHALE_SESSION_ID"},
		"Amp":        {"AMP_THREAD_ID", "AMP_CURRENT_THREAD_ID", "AGENT_THREAD_ID", "AI_AGENT"},
		"Cline": {
			"CLINE_RUN_AS_HUB_DAEMON", "CLINE_NO_INTERACTIVE", "CLINE_WRAPPER_PATH",
			"CLINE_CONNECTOR_CLI_LAUNCH", "CLINE_CONNECTOR_STARTING_INSTANCE", "CLINE_CONNECTOR_SUPERVISED",
			"CLINE_HOOK_AGENT_RESUME", "CLINE_SANDBOX", "CLINE_SANDBOX_DATA_DIR",
		},
	} {
		t.Run(harness, func(t *testing.T) {
			t.Parallel()
			env := []string{"PATH=/usr/bin"}
			for _, key := range keys {
				env = append(env, key+"=parent")
			}

			out := FinalizeAgentEnv(env, agent.Options{})

			for _, key := range keys {
				assert.Falsef(t, envutil.HasKey(out, key), "%s of the parent %s session must not reach the agent", key, harness)
			}
			assert.Contains(t, out, "PATH=/usr/bin")
		})
	}
}

// TestFinalizeAgentEnv_StripsAnInheritedHelperVariable pins that the
// provider-helper variable never passes from the worker's own environment into
// an agent. An inherited value points at another agent's helper spec: a worker
// started from a shell of an Amp agent carries that agent's value, and a CLI of
// this worker that inherited it would run the OTHER agent's helper.
func TestFinalizeAgentEnv_StripsAnInheritedHelperVariable(t *testing.T) {
	t.Parallel()

	out := FinalizeAgentEnv([]string{
		"PATH=/usr/bin",
		contracts.EnvAgentHelper + "=/elsewhere/helper.json",
	}, agent.Options{})
	assert.False(t, envutil.HasKey(out, contracts.EnvAgentHelper))
	assert.Contains(t, out, "PATH=/usr/bin")
}

// `cline --data-dir` and an inherited CLINE_SANDBOX=1 put Cline in a sandbox.
// Cline then sets the sandbox marker and the data variables below on itself
// (configureSandboxEnvironment in Cline 3.0.64), so each command that it runs
// inherits them. A worker started from such a command must not drive the
// sandbox's data, so the scrub drops these variables together with the marker.
// Without the marker, the same variables are the user's own configuration, and
// they stay.
func TestFinalizeAgentEnvDropsTheDataOfAClineSandbox(t *testing.T) {
	t.Parallel()
	sandboxData := []string{
		"CLINE_DATA_DIR=/work/sandbox",
		"CLINE_DB_DATA_DIR=/work/sandbox/db",
		"CLINE_SESSION_DATA_DIR=/work/sandbox/sessions",
		"CLINE_TEAM_DATA_DIR=/work/sandbox/teams",
		"CLINE_PROVIDER_SETTINGS_PATH=/work/sandbox/settings/providers.json",
		"CLINE_HOOKS_LOG_PATH=/work/sandbox/logs/hooks.jsonl",
	}
	sandboxDataKeys := make([]string, 0, len(sandboxData))
	for _, entry := range sandboxData {
		key, _, _ := strings.Cut(entry, "=")
		sandboxDataKeys = append(sandboxDataKeys, key)
	}

	for _, tc := range []struct {
		name   string
		marker []string
		drops  bool
	}{
		{name: "the sandbox marker", marker: []string{"CLINE_SANDBOX=1"}, drops: true},
		{name: "the sandbox marker with spaces", marker: []string{"CLINE_SANDBOX= 1 "}, drops: true},
		{name: "no sandbox marker", marker: nil, drops: false},
		{name: "an empty sandbox marker", marker: []string{"CLINE_SANDBOX="}, drops: false},
		{name: "a sandbox marker that Cline ignores", marker: []string{"CLINE_SANDBOX=0"}, drops: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := append([]string{"PATH=/usr/bin", "CLINE_DIR=/home/u/.cline"}, tc.marker...)
			env = append(env, sandboxData...)

			out := FinalizeAgentEnv(env, agent.Options{})

			for _, key := range sandboxDataKeys {
				assert.Equalf(t, !tc.drops, envutil.HasKey(out, key), "%s after %s", key, tc.name)
			}
			assert.False(t, envutil.HasKey(out, "CLINE_SANDBOX"), "the sandbox marker never reaches an agent")
			assert.Contains(t, out, "CLINE_DIR=/home/u/.cline", "the sandbox does not set CLINE_DIR, so it stays")
			assert.Contains(t, out, "PATH=/usr/bin")
		})
	}
}
