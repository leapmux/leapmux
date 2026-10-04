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
	assert.True(t, has(gooseProvider, contracts.GooseConfigThinkingEffort))
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

	// Codewhale takes effort per turn under the well-known id, its permission
	// posture under LeapMux's permission-mode axis, and its agent/plan mode on an
	// axis of its own.
	codewhale := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE
	assert.True(t, has(codewhale, agent.OptionIDEffort))
	assert.True(t, has(codewhale, agent.OptionIDPermissionMode))
	assert.True(t, has(codewhale, contracts.CodewhaleOptionMode))
	assert.False(t, has(codewhale, agent.OptionIDPrimaryAgent), "codewhale has no primary-agent axis")

	// Kimi Code surfaces its thinking level under the well-known effort id, plan
	// mode on the permission-mode axis, and swarm mode on an axis of its own.
	kimiCode := leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE
	assert.True(t, has(kimiCode, agent.OptionIDEffort))
	assert.True(t, has(kimiCode, agent.OptionIDPermissionMode))
	assert.True(t, has(kimiCode, "swarmMode"))
	assert.False(t, has(kimiCode, agent.OptionIDPrimaryAgent), "kimi code has no primary-agent axis")

	// Qwen Code and Grok Build drive their reasoning through a server-driven
	// `reasoning_effort` option, and carry their session modes on the
	// permission-mode axis. Grok Build also keeps its approval mode, which Grok
	// never reports, as an axis of LeapMux's own.
	qwenProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE
	assert.True(t, has(qwenProvider, agent.OptionIDPermissionMode))
	assert.True(t, has(qwenProvider, contracts.QwenConfigReasoningEffort))
	assert.False(t, has(qwenProvider, agent.OptionIDEffort), "qwen uses reasoning_effort, not the well-known effort")
	grokProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD
	assert.True(t, has(grokProvider, agent.OptionIDPermissionMode))
	assert.True(t, has(grokProvider, contracts.GrokConfigReasoningEffort))
	assert.True(t, has(grokProvider, contracts.GrokOptionApprovalMode))
	assert.False(t, has(grokProvider, agent.OptionIDEffort), "grok uses reasoning_effort, not the well-known effort")

	// Kiro carries its modes on the permission-mode axis, drives its effort
	// through a server-driven `effortLevel` option, and surfaces its thinking,
	// autopilot and content-collection switches as config options. It keeps its
	// policy preset, which Kiro never reports, as an axis of LeapMux's own.
	kiroProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO
	assert.True(t, has(kiroProvider, agent.OptionIDPermissionMode))
	assert.True(t, has(kiroProvider, contracts.KiroConfigEffortLevel))
	assert.True(t, has(kiroProvider, contracts.KiroOptionPolicyPreset))
	assert.True(t, has(kiroProvider, "thinking"))
	assert.True(t, has(kiroProvider, "autopilot"))
	assert.True(t, has(kiroProvider, "contentCollection"))
	assert.False(t, has(kiroProvider, agent.OptionIDEffort), "kiro uses effortLevel, not the well-known effort")
	assert.False(t, has(kiroProvider, agent.OptionIDPrimaryAgent), "kiro carries its agents on the mode axis")

	// Oh My Pi surfaces its thinking level under the well-known effort id, and its
	// tool approval mode under LeapMux's permission-mode axis.
	ohMyPi := leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI
	assert.True(t, has(ohMyPi, agent.OptionIDEffort))
	assert.True(t, has(ohMyPi, agent.OptionIDPermissionMode))
	assert.False(t, has(ohMyPi, agent.OptionIDPrimaryAgent), "oh my pi has no primary-agent axis")

	// MiMo carries its primary agents (build, plan) on LeapMux's permission-mode
	// axis, its model's reasoning variants under the well-known effort id, and its
	// two permission switches on a provider-private policy axis.
	mimoCode := leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE
	assert.True(t, has(mimoCode, agent.OptionIDEffort))
	assert.True(t, has(mimoCode, agent.OptionIDPermissionMode))
	assert.True(t, has(mimoCode, contracts.MiMoOptionPermissionPolicy))
	assert.False(t, has(mimoCode, agent.OptionIDPrimaryAgent), "mimo carries its agents on the permission-mode axis")

	// Amp carries its agent mode on an axis of its own and the permission mode on
	// LeapMux's axis. The mode chooses the reasoning effort, so Amp has no effort
	// axis.
	ampProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP
	assert.True(t, has(ampProvider, agent.OptionIDPermissionMode))
	assert.True(t, has(ampProvider, contracts.AmpOptionAgentMode))
	assert.False(t, has(ampProvider, agent.OptionIDEffort), "amp's mode chooses the effort")
	assert.False(t, has(ampProvider, agent.OptionIDPrimaryAgent), "amp has no primary-agent axis")

	// Cline carries its model's reasoning effort on the well-known axis, and
	// Plan, Act and Auto-approve on the permission-mode axis.
	clineProvider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE
	assert.True(t, has(clineProvider, agent.OptionIDEffort))
	assert.True(t, has(clineProvider, agent.OptionIDPermissionMode))
	assert.False(t, has(clineProvider, agent.OptionIDPrimaryAgent), "cline carries its modes on the permission-mode axis")

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
		// Oh My Pi falls back to Write, which asks before a command runs. omp's own
		// default is Yolo, the mode the bypass preset selects.
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, wantFallback: contracts.OhMyPiApprovalModeWrite},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, wantFallback: cursor.ModeAgent},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, wantFallback: contracts.ReasonixModeNormal},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, wantFallback: contracts.CodewhalePostureAsk},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, wantFallback: contracts.KimiDefaultMode},
		// Qwen Code's own default is `auto`, whose classifier approves without a
		// prompt, so both halves state Default.
		{
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.QwenModeDefault},
			wantFallback: contracts.QwenModeDefault,
		},
		// Grok Build's session mode decides what the agent may do, not whether it
		// asks: its approval mode does that, and its safe default lives beside it.
		{
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.GrokModeDefault},
			wantFallback: contracts.GrokModeDefault,
		},
		// Kiro's mode decides what the agent does, not whether it asks: its
		// policy preset does that, and its safe default lives beside it.
		{
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.KiroModeDefault},
			wantFallback: contracts.KiroModeDefault,
		},
		// MiMo runs the build agent, MiMo's own default, and its permission policy
		// is a separate axis that ProviderOptionDefaults seeds.
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, wantFallback: contracts.MiMoModeBuild},
		// Amp asks before each local tool call in a new session and in one that stored
		// no mode. Allow All is what its bypass preset selects.
		{
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAsk},
			wantFallback: contracts.AmpPermissionModeAsk,
		},
		// Cline acts and asks before each tool that changes something, in a new
		// session and in one that stored no mode. Auto-approve is what its bypass
		// preset selects.
		{
			provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE,
			wantNew:      map[string]string{agent.OptionIDPermissionMode: contracts.ClinePermissionModeAct},
			wantFallback: contracts.ClinePermissionModeAct,
		},
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
		{"codewhale empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, "", contracts.CodewhalePostureAsk},
		{"codewhale legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, contracts.ClaudeModeDefault, contracts.CodewhalePostureAsk},
		{"codewhale explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, contracts.CodewhalePostureFullAccess, contracts.CodewhalePostureFullAccess},
		{"kimi empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, "", contracts.KimiModeManual},
		{"kimi legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, contracts.ClaudeModeDefault, contracts.KimiModeManual},
		{"kimi explicit plan", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, contracts.KimiModePlan, contracts.KimiModePlan},
		{"qwen empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, "", contracts.QwenModeDefault},
		{"qwen explicit yolo", leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, contracts.QwenModeYolo, contracts.QwenModeYolo},
		{"grok empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD, "", contracts.GrokModeDefault},
		{"grok explicit plan", leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD, contracts.GrokModePlan, contracts.GrokModePlan},
		{"kiro empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO, "", contracts.KiroModeDefault},
		{"kiro explicit plan", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO, contracts.KiroModePlan, contracts.KiroModePlan},
		{"oh my pi empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, "", contracts.OhMyPiApprovalModeWrite},
		{"oh my pi explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, contracts.OhMyPiApprovalModeAlwaysAsk, contracts.OhMyPiApprovalModeAlwaysAsk},
		{"mimo empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, "", contracts.MiMoModeBuild},
		{"mimo legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, contracts.ClaudeModeDefault, contracts.MiMoModeBuild},
		{"mimo explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, contracts.MiMoModePlan, contracts.MiMoModePlan},
		{"amp empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "", contracts.AmpPermissionModeAsk},
		{"amp legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, contracts.ClaudeModeDefault, contracts.AmpPermissionModeAsk},
		{"amp explicit", leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, contracts.AmpPermissionModeAllowAll, contracts.AmpPermissionModeAllowAll},
		{"cline empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, "", contracts.ClinePermissionModeAct},
		{"cline legacy db default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, contracts.ClaudeModeDefault, contracts.ClinePermissionModeAct},
		{"cline explicit plan", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, contracts.ClinePermissionModePlan, contracts.ClinePermissionModePlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, registry.PermissionModeOrDefault(tc.provider, tc.mode))
		})
	}
}
