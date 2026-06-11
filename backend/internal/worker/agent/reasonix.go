package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ReasonixAgent manages a single Reasonix (DeepSeek) ACP process.
//
// Reasonix is an unusually minimal ACP agent: session/new returns only a
// sessionId (no models/modes/configOptions channel), the model is fixed at
// startup via the `--model` flag rather than a runtime RPC, and permission
// mode is config-driven (reasonix.toml) and not switchable over ACP. So
// Reasonix is a model-only provider -- it leaves modeChannel unmapped, sets no
// reapply/refresh hooks, exposes no option groups, and relaunches (rather than
// live-applies) when the model changes.
type ReasonixAgent struct {
	acpBase
}

// StartReasonix starts a Reasonix ACP agent process and performs the handshake.
//
// Unlike the other ACP providers, Reasonix has no afterHandshake apply step:
// session/new returns only a sessionId (no models/modes to apply), so the
// handshake itself activates the session and the manager serves the static
// model catalog.
//
// Reasonix selects its model only at launch via `reasonix acp --model <name>`
// and reports it nowhere over ACP, so LeapMux always pins it: the model is
// resolved to the provider default (the IsDefault catalog entry, or the
// LEAPMUX_REASONIX_DEFAULT_MODEL override) when the caller leaves it unset, and
// the flag is always passed. This keeps the stored model in sync with the
// running process (it can never be empty/unknown) and gives LeapMux full
// control over the model rather than deferring to reasonix.toml's default_model
// -- matching the service, which already resolves the model before launch.
// It inherits the default prompt sender and the default nil-group
// AvailableOptionGroups from acpBase.
func StartReasonix(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	model := opts.Model
	if model == "" {
		model = DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX)
	}
	return acpStart(ctx, opts, sink, acpStartSpec[ReasonixAgent]{
		providerName: "reasonix",
		binaryName:   "reasonix",
		baseArgs:     []string{"acp", "--model", model},
		newAgent:     func() *ReasonixAgent { return &ReasonixAgent{} },
		base:         func(a *ReasonixAgent) *acpBase { return &a.acpBase },
		configure: func(a *ReasonixAgent) {
			// Pin the stored model to the launched one (acpStart set it from the
			// possibly-empty opts.Model). modeChannel stays unmapped and
			// reapply/refresh stay nil: Reasonix exposes no modes/configOptions
			// channel. modelFixedAtLaunch makes a stray config_option_update model
			// select a no-op so the stored model can't drift from the launch value.
			a.model = model
			a.modelFixedAtLaunch = true
		},
	})
}

// UpdateSettings handles a live settings change. Reasonix fixes its model at
// startup via the `--model` flag and cannot switch it over ACP, so a model
// change returns false to make the service fall back to a stop+restart, which
// relaunches the process with the new --model. Every other change (Reasonix has
// none) is a no-op that needs no restart.
func (a *ReasonixAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	requested := s.GetModel()
	if requested == "" {
		return true
	}
	a.mu.Lock()
	current := a.model
	a.mu.Unlock()
	// Returning false signals "can't apply live" -> the service relaunches with
	// the new model in opts.Model.
	return requested == current
}

// reasonixAvailableModels is the static catalog of Reasonix's built-in provider
// entries (reasonix/internal/config/config.go). The id is the provider-entry
// name Reasonix's `--model` flag accepts (cfg.ResolveModel resolves it to the
// concrete model). deepseek-flash is Reasonix's own default_model.
var reasonixAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "deepseek-flash", DisplayName: "DeepSeek Flash", Description: "Fast, economical DeepSeek model", IsDefault: true, ContextWindow: 1_000_000},
	{Id: "deepseek-pro", DisplayName: "DeepSeek Pro", Description: "Most capable DeepSeek model for complex work", ContextWindow: 1_000_000},
	{Id: "mimo-pro", DisplayName: "MiMo Pro", Description: "Xiaomi MiMo, most capable (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
	{Id: "mimo-flash", DisplayName: "MiMo Flash", Description: "Xiaomi MiMo, fast and economical (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartReasonix(ctx, opts, sink)
		},
		reasonixAvailableModels,
		nil, // no permission-mode / config-option groups
		"LEAPMUX_REASONIX_DEFAULT_MODEL",
		"",
		"reasonix",
	)
}
