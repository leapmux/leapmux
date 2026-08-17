package agent

import (
	"context"
	"os/exec"
	"path/filepath"
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

	t.Run("scrub precedes LEAPMUX_CONTROL strip and ExtraEnv append", func(t *testing.T) {
		env := append(buildEnv(), "LEAPMUX_CONTROL_OLD=stale")
		out := FinalizeAgentEnv(env, Options{ExtraEnv: []string{"LEAPMUX_CONTROL_NEW=fresh"}})

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
		out := FinalizeAgentEnv(env, Options{})

		assert.NotContains(t, out, "LEAPMUX_CONTROL_OLD=stale")
		assert.False(t, envutil.HasKey(out, "LEAPMUX_CONTROL_OLD"), "inherited LEAPMUX_CONTROL_* must be stripped")
		assert.Contains(t, out, "LEAPMUX_WORKER=1")
		for _, k := range mustSurvive {
			assert.Truef(t, envutil.HasKey(out, k), "var %q must survive the scrub", k)
		}
	})
}

func TestAvailableOptionGroups_DefaultOptionMetadata(t *testing.T) {
	t.Parallel()

	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		groups := AvailableOptionGroupsForProvider(provider)
		require.NotEmpty(t, groups)
		for _, group := range groups {
			// The default now lives on the group (DefaultValue) instead of a
			// per-option IsDefault flag. A group may omit it (the ACP
			// primary-agent/permission-mode groups rely on the "first option"
			// convention), but when set it must name exactly one of the options.
			if group.GetDefaultValue() == "" {
				continue
			}
			defaults := 0
			for _, option := range group.Options {
				if option.GetId() == group.GetDefaultValue() {
					defaults++
				}
			}
			assert.Equalf(t, 1, defaults, "provider=%s group=%s default value %q must name exactly one option", provider.String(), group.GetId(), group.GetDefaultValue())
		}
	}
}

func TestPermissionModeOrDefault(t *testing.T) {
	t.Parallel()

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
		{"opencode no top-level default", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "", ""},
		{"reasonix no permission mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PermissionModeOrDefault(tc.provider, tc.mode))
		})
	}
}

// TestFindAvailableModel verifies the lookup matches by id and, critically,
// tolerates nil entries in the slice (its callers treat the catalog as possibly
// nil-bearing, so the lookup must not panic on one).
func TestFindAvailableModel(t *testing.T) {
	t.Parallel()

	models := []*ModelInfo{
		nil,
		{Id: "opus"},
		nil,
		{Id: "sonnet"},
	}

	assert.Equal(t, "sonnet", FindAvailableModel(models, "sonnet").GetId())
	assert.Equal(t, "opus", FindAvailableModel(models, "opus").GetId())
	assert.Nil(t, FindAvailableModel(models, "missing"), "no match returns nil")
	assert.Nil(t, FindAvailableModel([]*ModelInfo{nil, nil}, "x"), "all-nil slice does not panic")
	assert.Nil(t, FindAvailableModel(nil, "x"))
}

// TestOptions_Accessors locks the launch Options' by-id readers: model/effort/
// permission are NOT shadow scalar fields but accessors over the single option
// map, so a provider reading opts.Model()/Effort()/PermissionMode()/Get(id) sees
// exactly what the service resolved into Options -- and an empty map reads back as
// "" for every axis without panicking.
func TestOptions_Accessors(t *testing.T) {
	t.Parallel()

	o := Options{Options: map[string]string{
		OptionIDModel:          "opus[1m]",
		OptionIDEffort:         "xhigh",
		OptionIDPermissionMode: "plan",
		"sandbox_policy":       "workspace-write",
	}}
	assert.Equal(t, "opus[1m]", o.Model())
	assert.Equal(t, "xhigh", o.Effort())
	assert.Equal(t, "plan", o.PermissionMode())
	assert.Equal(t, "workspace-write", o.Get("sandbox_policy"), "Get reads any axis by id, not just the well-known ones")
	assert.Empty(t, o.Get("nonexistent"), "an absent id reads back empty")

	var empty Options
	assert.Empty(t, empty.Model())
	assert.Empty(t, empty.Effort())
	assert.Empty(t, empty.PermissionMode())
	assert.Empty(t, empty.Get(OptionIDModel), "a nil option map does not panic")
}

// TestNormalizeModelID_FromRegistry verifies NormalizeModelID routes through each
// provider's registered normalizer (the same one the live agent uses) rather than a
// hand-maintained switch, and returns the id unchanged for a provider with none.
func TestNormalizeModelID_FromRegistry(t *testing.T) {
	t.Parallel()

	// Claude collapses its fully-qualified CLI id into the alias space.
	const claudeFull = "claude-opus-4-8[1m]"
	assert.Equal(t, normalizeClaudeCodeModel(claudeFull),
		NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeFull))
	assert.NotEqual(t, claudeFull,
		NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeFull),
		"Claude's normalizer must actually collapse the id")

	// A legacy bare "opus" canonicalizes to "opus[1m]" through the registry path too
	// (Opus is 1M-only; the standard-context alias no longer resolves on its own).
	assert.Equal(t, "opus[1m]",
		NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "opus"))

	// Cursor maps the wire "default[]" sentinel to "auto".
	assert.Equal(t, "auto", NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "default[]"))

	// A provider with no registered normalizer returns the id unchanged.
	assert.Equal(t, "gpt-5.5", NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "gpt-5.5"))
}

// TestKnownOptionIDs locks the per-provider option-id allowlist that
// UpdateAgentSettings validates an incoming options map against. Each provider
// must expose exactly its real axes: the universal model, its secondary
// permission-mode/primary-agent axis, the well-known effort axis ONLY where it
// has one, and every provider-private extra (Codex's sandbox/network/...,
// Pi's pi_provider, the ACP server config options). A drift here either strips a
// legitimate setting (under-listing) or re-admits a phantom (over-listing).
func TestKnownOptionIDs(t *testing.T) {
	t.Parallel()

	has := func(provider leapmuxv1.AgentProvider, id string) bool {
		return KnownOptionIDs(provider)[id]
	}

	// model is universal.
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		assert.Truef(t, has(p, OptionIDModel), "%s must allow model", p)
	}

	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	assert.True(t, has(claude, OptionIDEffort))
	assert.True(t, has(claude, OptionIDPermissionMode))
	assert.False(t, has(claude, OptionIDPrimaryAgent), "claude has no primary-agent axis")
	assert.False(t, has(claude, "allow_all"), "claude has no copilot allow_all axis")

	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	for _, id := range []string{OptionIDModel, OptionIDEffort, OptionIDPermissionMode,
		CodexOptionSandboxPolicy, CodexOptionNetworkAccess, CodexOptionCollaborationMode, CodexOptionServiceTier} {
		assert.Truef(t, has(codex, id), "codex must allow %q", id)
	}
	assert.False(t, has(codex, OptionIDPrimaryAgent), "codex has no primary-agent axis")

	// Cursor bakes effort/thinking/context into the model id, so it has NO
	// well-known effort axis -- `--effort` against Cursor must be foreign.
	cursor := leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR
	assert.True(t, has(cursor, OptionIDModel))
	assert.True(t, has(cursor, OptionIDPermissionMode))
	assert.False(t, has(cursor, OptionIDEffort), "cursor has no effort axis (baked into model id)")

	// Copilot's reasoning axis is the option "reasoning_effort", NOT the well-known
	// "effort"; it also exposes "allow_all". `--effort` against Copilot is foreign.
	copilot := leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	assert.True(t, has(copilot, OptionIDPermissionMode))
	assert.True(t, has(copilot, CopilotConfigReasoningEffort))
	assert.True(t, has(copilot, CopilotConfigAllowAll))
	assert.False(t, has(copilot, OptionIDEffort), "copilot uses reasoning_effort, not the well-known effort")

	// OpenCode / Kilo surface their per-model reasoning under the well-known "effort"
	// id, and use the primary-agent secondary axis (no permission mode).
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		assert.Truef(t, has(p, OptionIDEffort), "%s surfaces effort", p)
		assert.Truef(t, has(p, OptionIDPrimaryAgent), "%s uses primaryAgent", p)
		assert.Falsef(t, has(p, OptionIDPermissionMode), "%s has no permission-mode axis", p)
	}

	goose := leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE
	assert.True(t, has(goose, OptionIDPermissionMode))
	assert.True(t, has(goose, GooseConfigThinkingEffort))
	assert.True(t, has(goose, GooseConfigProvider))
	assert.False(t, has(goose, OptionIDEffort), "goose uses thinking_effort, not the well-known effort")

	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	assert.True(t, has(pi, OptionIDEffort))
	assert.True(t, has(pi, PiOptionProvider))
	assert.False(t, has(pi, OptionIDPermissionMode), "pi has no permission-mode axis")

	// Reasonix's model is fixed at launch and it exposes no other axis.
	reasonix := leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX
	assert.True(t, has(reasonix, OptionIDModel))
	assert.False(t, has(reasonix, OptionIDEffort))
	assert.False(t, has(reasonix, OptionIDPermissionMode))
	assert.False(t, has(reasonix, OptionIDPrimaryAgent))

	// An unknown provider yields just {model}.
	unknown := KnownOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	assert.Equal(t, map[string]bool{OptionIDModel: true}, unknown)
}

// TestRegisteredSecondaryFallback verifies acpStart's secondary-fallback seeding sources the SAME
// option list each provider declared at registration, so dropping the duplicate
// `a.secondaryFallback = fallbackXxx()` assignment from each configure can't change what a started
// agent serves before its session reports a catalog. The unmapped channel (Reasonix exposes no
// secondary axis) resolves to nil.
func TestRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		provider    leapmuxv1.AgentProvider
		modeChannel acpModeChannel
		want        []*leapmuxv1.AvailableOption
	}{
		{"copilot permission mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, modeChannelPermissionMode, fallbackCopilotCLIModes()},
		{"goose permission mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, modeChannelPermissionMode, fallbackGooseCLIModes()},
		{"cursor permission mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, modeChannelPermissionMode, fallbackCursorCLIModes()},
		{"opencode primary agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, modeChannelPrimaryAgent, fallbackOpenCodePrimaryAgents()},
		{"kilo primary agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, modeChannelPrimaryAgent, fallbackKiloPrimaryAgents()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := registeredSecondaryFallback(tc.provider, tc.modeChannel)
			require.Len(t, got, len(tc.want), "fallback option count")
			for i := range tc.want {
				assert.Equal(t, tc.want[i].GetId(), got[i].GetId(), "option %d id", i)
				assert.Equal(t, tc.want[i].GetName(), got[i].GetName(), "option %d name", i)
			}
		})
	}

	assert.Nil(t, registeredSecondaryFallback(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, modeChannelUnmapped),
		"the unmapped channel has no secondary fallback")
}

func TestListAvailableProviders_ExpiredContextIsIncomplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	providers, complete := ListAvailableProviders(ctx, "/bin/sh", false)
	assert.False(t, complete, "a cancelled scan is not evidence of absence")
	assert.Empty(t, providers)
}

// A shell that cannot START proves nothing about the binary. Caching that
// false froze a broken environment as "not installed" for the worker's
// lifetime, and ListAvailableProviders still reported complete=true, so
// the client never retried and the new-agent button stayed empty until a
// restart.
func TestProbeBinaryInconclusiveWhenTheShellCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shell")

	available, conclusive := probeBinary(context.Background(), missing, false, "claude")
	assert.False(t, available)
	assert.False(t, conclusive, "a shell that never ran establishes nothing")

	// And nothing is cached, so a later call with a working shell is free
	// to answer for itself. The inconclusiveness reaches the caller too:
	// ListAvailableProviders needs it to tell "the shell said no" apart
	// from "the shell never ran".
	gotAvailable, gotConclusive := checkBinaryAvailable(context.Background(), missing, false, "claude")
	assert.False(t, gotAvailable)
	assert.False(t, gotConclusive, "checkBinaryAvailable must forward the probe's conclusiveness")
	_, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{missing, false, "claude"})
	assert.False(t, cached, "an inconclusive probe must not be cached")
}

// A shell that runs and reports the binary absent IS an answer, and is
// cached — that is the case the cache exists for.
func TestProbeBinaryConclusiveWhenTheShellAnswers(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	const absent = "leapmux-definitely-not-installed"

	available, conclusive := probeBinary(context.Background(), shell, false, absent)
	assert.False(t, available)
	assert.True(t, conclusive, "the shell ran and answered")

	gotAvailable, gotConclusive := checkBinaryAvailable(context.Background(), shell, false, absent)
	assert.False(t, gotAvailable)
	assert.True(t, gotConclusive)
	v, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{shell, false, absent})
	require.True(t, cached, "a conclusive probe must be cached")
	assert.Equal(t, false, v)
}

// A probe killed by an expired context reports an ExitError ("signal:
// killed"), not the context error — so the status alone would look
// conclusive.
func TestProbeBinaryInconclusiveUnderAnExpiredContext(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, conclusive := probeBinary(ctx, shell, false, "leapmux-cancelled-probe")
	assert.False(t, conclusive, "a probe under a dead context establishes nothing")
}

// TestListAvailableProvidersIncompleteWhenTheShellCannotStart is the whole
// point of threading conclusiveness out of the probe.
//
// Completeness used to be read from ctx.Err() alone, so every NON-deadline
// way a probe proves nothing — a $SHELL that cannot exec, a missing
// interpreter, EACCES, a fork failure under load, a login profile that
// exits non-zero — returned an AUTHORITATIVE empty list. The worker sends
// "provider scan did not finish; retry" only on !complete, so the client
// showed "no agent providers installed" with no retry, although every CLI
// was installed.
func TestListAvailableProvidersIncompleteWhenTheShellCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shell")

	providers, complete := ListAvailableProviders(context.Background(), missing, false)
	assert.Empty(t, providers)
	assert.False(t, complete,
		"a shell that never ran is not evidence that no provider is installed")
}

// The companion case: a shell that RUNS and reports every binary absent is
// a complete scan, and an empty list is then the truth.
func TestListAvailableProvidersCompleteWhenTheShellAnswers(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	// A PATH with nothing on it makes every probe answer "absent" fast.
	t.Setenv("PATH", t.TempDir())
	binaryAvailabilityCache.Range(func(k, _ any) bool {
		binaryAvailabilityCache.Delete(k)
		return true
	})

	providers, complete := ListAvailableProviders(context.Background(), shell, false)
	assert.Empty(t, providers)
	assert.True(t, complete, "the shell ran and answered for every binary")
}
