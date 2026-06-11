package agent

import (
	"context"
	"encoding/json"

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
func StartReasonix(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// The model is selected at startup via `reasonix acp --model <name>`; there
	// is no runtime model RPC. An empty model lets Reasonix use its config
	// default_model (deepseek-flash).
	baseArgs := []string{"acp"}
	if opts.Model != "" {
		baseArgs = append(baseArgs, "--model", opts.Model)
	}

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		BinaryName: "reasonix",
		BaseArgs:   baseArgs,
		WorkingDir: opts.WorkingDir,
	})

	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &ReasonixAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "reasonix", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       opts.Model,
		},
	}
	// modeChannel stays modeChannelUnmapped (the zero value): Reasonix exposes no
	// modes/configOptions channel, so there is nothing to reapply or refresh on a
	// session/new -- reapplySettings and refreshFromSession are left nil.
	a.promptFunc = a.doSendPrompt

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := acpStandardInitParams()
	if err != nil {
		return nil, err
	}
	// The handshake establishes and activates the session (stores sessionID,
	// broadcasts active status). Reasonix reports no models/modes, so there is no
	// applyPermissionModeStartup step -- the manager serves the static model
	// catalog and the model already lives on a.model from construction.
	if _, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams, acpDefaultSessionConfig); err != nil {
		return nil, err
	}

	return a, nil
}

func (a *ReasonixAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
}

// AvailableOptionGroups returns nil: Reasonix has no runtime permission-mode or
// config-option groups (its model is the only setting, served separately as the
// model catalog).
func (a *ReasonixAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return nil
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
