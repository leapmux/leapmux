package providers

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cursor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/goose"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/pi"
	"github.com/stretchr/testify/assert"
)

// TestKnownOptionIDs locks the per-provider option-id allowlist that
// UpdateAgentSettings validates an incoming options map against. Each provider
// must expose exactly its real axes: the universal model, its secondary
// permission-mode/primary-agent axis, the well-known effort axis ONLY where it
// has one, and every provider-private extra (Codex's sandbox/network/...,
// Pi's pi_provider, the ACP server config options). A drift here either strips a
// legitimate setting (under-listing) or re-admits a phantom (over-listing).
func TestKnownOptionIDs(t *testing.T) {
	t.Parallel()

	registry := Registry()

	has := func(provider leapmuxv1.AgentProvider, id string) bool {
		return registry.KnownOptionIDs(provider)[id]
	}

	// model is universal, so the list comes from the GENERATED provider table rather
	// than being retyped here. A hand-written list drifts silently: it keeps passing
	// while a provider added after it goes unchecked.
	for _, p := range agentlabels.AllProviders() {
		assert.Truef(t, has(p, agent.OptionIDModel), "%s must allow model", p)
	}

	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	assert.True(t, has(claude, agent.OptionIDEffort))
	assert.True(t, has(claude, agent.OptionIDPermissionMode))
	assert.False(t, has(claude, agent.OptionIDPrimaryAgent), "claude has no primary-agent axis")
	assert.False(t, has(claude, "allow_all"), "claude has no foreign allow_all axis")

	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	for _, id := range []string{agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPermissionMode,
		contracts.CodexOptionSandboxPolicy, contracts.CodexOptionNetworkAccess, contracts.CodexOptionCollaborationMode, contracts.CodexOptionServiceTier} {
		assert.Truef(t, has(codex, id), "codex must allow %q", id)
	}
	assert.False(t, has(codex, agent.OptionIDPrimaryAgent), "codex has no primary-agent axis")

	// Cursor bakes effort/thinking/context into the model id, so it has NO
	// well-known effort axis -- `--effort` against Cursor must be foreign.
	cursor := leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR
	assert.True(t, has(cursor, agent.OptionIDModel))
	assert.True(t, has(cursor, agent.OptionIDPermissionMode))
	assert.False(t, has(cursor, agent.OptionIDEffort), "cursor has no effort axis (baked into model id)")

	// Copilot drives its reasoning axis through the well-known "effort" id
	// (`session.model.setReasoningEffort`), and carries its session mode on a second
	// axis beside the permission mode.
	copilot := leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	assert.True(t, has(copilot, agent.OptionIDPermissionMode))
	assert.True(t, has(copilot, agent.OptionIDEffort))
	assert.True(t, has(copilot, contracts.CopilotOptionSessionMode))

	// OpenCode / Kilo surface their per-model reasoning under the well-known "effort"
	// id, and use the primary-agent secondary axis (no permission mode).
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		assert.Truef(t, has(p, agent.OptionIDEffort), "%s surfaces effort", p)
		assert.Truef(t, has(p, agent.OptionIDPrimaryAgent), "%s uses primaryAgent", p)
		assert.Falsef(t, has(p, agent.OptionIDPermissionMode), "%s has no permission-mode axis", p)
	}

	gooseProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE
	assert.True(t, has(gooseProvider, agent.OptionIDPermissionMode))
	assert.True(t, has(gooseProvider, goose.ConfigThinkingEffort))
	assert.True(t, has(gooseProvider, goose.ConfigProvider))
	assert.False(t, has(gooseProvider, agent.OptionIDEffort), "goose uses thinking_effort, not the well-known effort")

	piProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	assert.True(t, has(piProvider, agent.OptionIDEffort))
	assert.True(t, has(piProvider, pi.OptionProvider))
	assert.False(t, has(piProvider, agent.OptionIDPermissionMode), "pi has no permission-mode axis")

	// Reasonix advertises these runtime options.
	reasonix := leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX
	assert.True(t, has(reasonix, agent.OptionIDModel))
	assert.True(t, has(reasonix, agent.OptionIDEffort))
	assert.True(t, has(reasonix, agent.OptionIDPermissionMode))
	assert.True(t, has(reasonix, contracts.ReasonixConfigToolApproval))
	assert.False(t, has(reasonix, agent.OptionIDPrimaryAgent))

	// ZCode surfaces thought level under the well-known effort id, and its session
	// mode under LeapMux's permission-mode axis (plan | build | edit | yolo).
	zcode := leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE
	assert.True(t, has(zcode, agent.OptionIDEffort))
	assert.True(t, has(zcode, agent.OptionIDPermissionMode))
	assert.False(t, has(zcode, agent.OptionIDPrimaryAgent), "zcode has no primary-agent axis")

	// An unknown provider yields just {model}.
	unknown := registry.KnownOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	assert.Equal(t, map[string]bool{agent.OptionIDModel: true}, unknown)
}

// Both halves of every provider's PermissionDefaults, asserted side by side. They live in
// one struct so a reader sees them together; pinning them in one test is the same idea,
// and it is what shows that Claude ASKS for one mode and FALLS BACK to another.
func TestPermissionDefaults(t *testing.T) {
	t.Parallel()

	registry := Registry()

	cases := []struct {
		provider     leapmuxv1.AgentProvider
		wantNew      map[string]string
		wantFallback string
	}{
		{
			// Claude asks for Auto and falls back to Default: a CLI that cannot enter Auto
			// must still start.
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeAuto},
			wantFallback: contracts.ClaudeModeDefault,
		},
		{
			// Goose uses Smart Approve for both. The fallback must never be its `auto`,
			// which is the mode its bypass shortcut selects.
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.GooseDefaultMode},
			wantFallback: contracts.GooseDefaultMode,
		},
		{
			// A new Copilot session asks for Assisted, and a session with no stored mode
			// runs Manual -- the mode the runtime itself starts in.
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.CopilotPermissionModeAssisted},
			wantFallback: contracts.CopilotPermissionModeManual,
		},
		// A provider whose starting policy already asks before acting declares no safe
		// default, and only a fallback.
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, wantFallback: codex.DefaultApprovalPolicy},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, wantFallback: contracts.ZCodeDefaultMode},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, wantFallback: cursor.ModeAgent},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, wantFallback: contracts.ReasonixModeNormal},
		// A provider with no permission-mode axis at all declares neither half, and the
		// option is left unset rather than stamped with a value it cannot accept.
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE},
	}
	for _, tc := range cases {
		t.Run(tc.provider.String(), func(t *testing.T) {
			if tc.wantNew == nil {
				assert.Empty(t, registry.NewAgentOptionDefaults(tc.provider))
			} else {
				assert.Equal(t, tc.wantNew, registry.NewAgentOptionDefaults(tc.provider))
			}
			assert.Equal(t, tc.wantFallback, registry.FallbackPermissionMode(tc.provider))
		})
	}
}

func TestPermissionModeOrDefault(t *testing.T) {
	t.Parallel()

	registry := Registry()

	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		mode     string
		want     string
	}{
		{"claude empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "", contracts.ClaudeModeDefault},
		{"claude default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, contracts.ClaudeModeDefault, contracts.ClaudeModeDefault},
		{"codex empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "", codex.DefaultApprovalPolicy},
		{"codex legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, contracts.ClaudeModeDefault, codex.DefaultApprovalPolicy},
		{"codex explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "never", "never"},
		{"cursor legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, contracts.ClaudeModeDefault, cursor.ModeAgent},
		{"copilot legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, contracts.ClaudeModeDefault, contracts.CopilotPermissionModeManual},
		// Goose's fallback is Smart Approve, never Auto: Auto is the value Goose's own
		// BYPASS preset selects, so a resumed session with no stored mode would otherwise
		// open with every permission prompt disabled.
		{"goose empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "", contracts.GooseModeSmartApprove},
		{"goose legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, contracts.ClaudeModeDefault, contracts.GooseModeSmartApprove},
		{"goose explicit auto", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, contracts.GooseModeAuto, contracts.GooseModeAuto},
		{"opencode no top-level default", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "", ""},
		{"reasonix default mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, "", contracts.ReasonixModeNormal},
		{"zcode empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, "", contracts.ZCodeDefaultMode},
		{"zcode explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, contracts.ZCodeModePlan, contracts.ZCodeModePlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, registry.PermissionModeOrDefault(tc.provider, tc.mode))
		})
	}
}
