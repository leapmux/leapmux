package junie

import (
	"context"
	"path/filepath"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// The worker disables Junie's update checks and anonymous statistics.
// E2E sets JUNIE_HOME to isolate the native store.
// E2E sets JUNIE_DATA to select the installed binary.
const (
	junieSkipUpdateEnv      = "JUNIE_SKIP_UPDATE_CHECK=1"
	junieShareStatisticsEnv = "JUNIE_SHARE_ANONYMOUS_STATISTICS=false"
)

var _ agent.StartFunc = Start

// Start starts a Junie ACP process and performs the handshake.
//
// The flags disable Junie's default config locations. An explicit project MCP
// directory stays available. The model can be a custom profile, an account
// model, or a configured proxy model.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	args := junieBaseArgs(opts)
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration:  Registration(),
		ProviderName:  "junie",
		BaseArgs:      args,
		PinnedEnv:     []string{junieSkipUpdateEnv, junieShareStatisticsEnv},
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:      func() *Agent { return &Agent{} },
		Base:          func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			a.launchEffortOverride = opts.Effort()
			a.goalStartedAt = time.Now()
			a.homeDir = opts.HomeDir
			a.workingDir = opts.WorkingDir
			a.clock = quartz.NewReal()
			transcript := newJunieToolTranscript(ctx, sink, junieHome(agent.StoredSessionQuery{HomeDir: opts.HomeDir}), opts.WorkingDir)
			return acp.Hooks{
				Sink: transcript,
				ClientCapabilityMeta: map[string]any{
					"jetbrains": map[string]any{"air": map[string]any{
						"version": 1, "capabilities": []string{"nativeSubagentSessions"},
					}},
				},
				InitialModel:     opts.Model(),
				ModeChannel:      acp.ModeChannelPermissionMode,
				SteersByOwnRoute: true,
				// Junie rejects session/set_mode ("use session/set_config_option
				// with configId=\"mode\""), so the mode write takes the config
				// option route instead of the base session/set_mode write.
				ModeSetter: a.SetModeViaConfigOption,
				// Junie reports a custom profile by its decorated wire id. Reads
				// use the plain profile id, and writes restore the decoration.
				// Proxy model ids keep their provider-qualified wire form.
				ModelIDNormalizer: normalizeJunieModelID,
				ModelSetter:       a.setJunieModel,
				// Junie reports its goal in session_info_update under _meta.goal.
				// It reports child lifecycle through subagent_spawned and subagent_state_update.
				SessionMetadataHandler: a.handleSessionMetadata,
				ClearProviderState: func() {
					a.stopChildTails()
					transcript.Reset()
				},
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.JunieModeDefault, junieStartupModelSelection(handshake, opts.Model()))
		},
	})
}

// junieStartupModelSelection keeps Junie's selected provider when a bare CLI
// model matches it. Live model writes use exact option IDs instead.
func junieStartupModelSelection(handshake *acp.SessionResult, requested string) string {
	current := handshake.CurrentModelID
	if current == "" {
		for _, option := range handshake.ConfigOptions {
			if option.ID == acp.ConfigOptionIDModel {
				current = option.CurrentValue
				break
			}
		}
	}
	if junieAlreadySelectedModel(current, requested) {
		return current
	}
	return requested
}

// junieBaseArgs keeps the project MCP location explicit while Junie's default
// locations stay disabled.
func junieBaseArgs(opts agent.Options) []string {
	model := opts.Model()
	args := []string{
		"--acp=true",
		"--config-default-locations=false",
		"--model-default-locations=false",
		"--mcp-default-locations=false",
		"--skill-default-locations=false",
		"--command-default-location=false",
		"--agent-default-location=false",
		"--skip-update-check",
	}
	if opts.WorkingDir != "" {
		args = append(args, "--mcp-location", filepath.Join(opts.WorkingDir, ".junie", "mcp"))
	}
	if model != "" {
		// A configured proxy keeps its identity in the decorated model ID.
		// --provider accepts only fixed BYOK names, not configured proxies.
		args = append(args, "--model", model)
	}
	if effort := opts.Effort(); effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}
