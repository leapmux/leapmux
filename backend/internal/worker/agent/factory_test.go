package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFinalizeAgentEnv_ScrubsAgentIdentity verifies the single chokepoint
// every provider funnels through strips inherited agent-harness identity vars
// (so a worker launched from inside another agent's session doesn't spawn a
// nested one) while preserving the per-provider rc markers and auth/config the
// providers re-add before the call.
func TestFinalizeAgentEnv_ScrubsAgentIdentity(t *testing.T) {
	// Assert against the production list itself rather than a hand-maintained
	// subset, so EVERY scrub key is exercised and a newly-added key (or a typo
	// in an oddly-shaped one like "_EXTENSION_OPENCODE_PORT") is automatically
	// covered.
	identity := agentIdentityEnvScrubKeys
	// Must survive: per-provider rc markers / entrypoint re-added BEFORE
	// FinalizeAgentEnv, plus auth tokens, provider-selection, and config dirs.
	mustSurvive := []string{
		"CLAUDECODE", "CODEX_CI", "OPENCODE_CLIENT", "KILO_CLIENT", "GEMINI_CLI", "CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY",
		"CLAUDE_CODE_USE_BEDROCK", "CODEX_HOME", "GOOSE_MODEL", "PI_CODING_AGENT_DIR", "PATH",
	}

	buildEnv := func() []string {
		var env []string
		for _, k := range identity {
			env = append(env, k+"=leaked")
		}
		env = append(env,
			"CLAUDECODE=1", "CODEX_CI=1", "OPENCODE_CLIENT=1", "KILO_CLIENT=1", "GEMINI_CLI=1",
			"CLAUDE_CODE_ENTRYPOINT=cli",
			"CLAUDE_CODE_OAUTH_TOKEN=tok", "OPENAI_API_KEY=sk-test", "CODEX_API_KEY=sk-codex",
			"CLAUDE_CODE_USE_BEDROCK=1", "CODEX_HOME=/home/u/.codex", "GOOSE_MODEL=gpt-x",
			"PI_CODING_AGENT_DIR=/home/u/.pi", "PATH=/usr/bin:/bin",
		)
		return env
	}

	t.Run("strips identity, keeps markers, adds worker flag", func(t *testing.T) {
		out := FinalizeAgentEnv(buildEnv(), Options{})

		for _, k := range identity {
			assert.Falsef(t, envutil.HasKey(out, k), "identity var %q must be scrubbed", k)
		}
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
		assert.True(t, envutil.HasKey(out, "LEAPMUX_WORKER"), "LEAPMUX_WORKER=1 must be added")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
	})

	t.Run("scrub precedes LEAPMUX_REMOTE strip and ExtraEnv append", func(t *testing.T) {
		env := append(buildEnv(), "LEAPMUX_REMOTE_OLD=stale")
		out := FinalizeAgentEnv(env, Options{ExtraEnv: []string{"LEAPMUX_REMOTE_NEW=fresh"}})

		// Identity scrub still applied even on the ExtraEnv path.
		for _, k := range identity {
			assert.Falsef(t, envutil.HasKey(out, k), "identity var %q must be scrubbed", k)
		}
		// Inherited LEAPMUX_REMOTE_* stripped; the injected one wins.
		assert.NotContains(t, out, "LEAPMUX_REMOTE_OLD=stale")
		assert.Contains(t, out, "LEAPMUX_REMOTE_NEW=fresh")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
		// Markers + auth still survive on this path too.
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
	})

	t.Run("strips inherited LEAPMUX_REMOTE even with no ExtraEnv", func(t *testing.T) {
		// A worker spawned inside another worker's session inherits the
		// parent's LEAPMUX_REMOTE_* but injects no fresh ExtraEnv. The stale
		// remote context must still be shed so the child doesn't act on it.
		env := append(buildEnv(), "LEAPMUX_REMOTE_OLD=stale")
		out := FinalizeAgentEnv(env, Options{})

		assert.NotContains(t, out, "LEAPMUX_REMOTE_OLD=stale")
		assert.False(t, envutil.HasKey(out, "LEAPMUX_REMOTE_OLD"), "inherited LEAPMUX_REMOTE_* must be stripped")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
	})
}

func TestAvailableOptionGroups_DefaultOptionMetadata(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		groups := AvailableOptionGroupsForProvider(provider)
		require.NotEmpty(t, groups)
		for _, group := range groups {
			defaults := 0
			for _, option := range group.Options {
				if option.IsDefault {
					defaults++
				}
			}
			assert.Equalf(t, 1, defaults, "provider=%s group=%s should expose exactly one default option", provider.String(), group.Key)
		}
	}
}

func TestPermissionModeOrDefault(t *testing.T) {
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		mode     string
		want     string
	}{
		{"claude empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "", PermissionModeDefault},
		{"claude default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, PermissionModeDefault, PermissionModeDefault},
		{"codex empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "", CodexDefaultApprovalPolicy},
		{"codex legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, PermissionModeDefault, CodexDefaultApprovalPolicy},
		{"codex explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "never", "never"},
		{"cursor legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, PermissionModeDefault, CursorCLIModeAgent},
		{"copilot legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, PermissionModeDefault, CopilotCLIModeAgent},
		{"goose legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, PermissionModeDefault, GooseCLIModeAuto},
		{"gemini default is valid", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, PermissionModeDefault, PermissionModeDefault},
		{"opencode no top-level default", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PermissionModeOrDefault(tc.provider, tc.mode))
		})
	}
}
