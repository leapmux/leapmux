package junie

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Junie reads these environment variables for isolation. The worker pins the
// two update/telemetry switches so a test machine never phones home, and the
// E2E recipe sets JUNIE_HOME and JUNIE_DATA to point the store and the install
// root.
const (
	junieSkipUpdateEnv      = "JUNIE_SKIP_UPDATE_CHECK=1"
	junieShareStatisticsEnv = "JUNIE_SHARE_ANONYMOUS_STATISTICS=false"
)

// Start starts a Junie ACP process and performs the handshake.
//
// `junie --acp=true` is the launch. The flags keep Junie off the user's own
// configuration, skills, commands and model profiles unless the environment
// supplies a model location: a LeapMux agent must not read the developer's
// JetBrains setup. The model is a `custom:` profile id when LeapMux routes the
// model, and an account model otherwise.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
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
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort := opts.Effort(); effort != "" {
		args = append(args, "--effort", effort)
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration:  Registration(),
		ProviderName:  "junie",
		BaseArgs:      args,
		PinnedEnv:     []string{junieSkipUpdateEnv, junieShareStatisticsEnv},
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:      func() *Agent { return &Agent{} },
		Base:          func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return acp.Hooks{
				InitialModel:     model,
				ModeChannel:      acp.ModeChannelPermissionMode,
				SteersByOwnRoute: true,
				// Junie rejects session/set_mode ("use session/set_config_option
				// with configId=\"mode\""), so the mode write takes the config
				// option route instead of the base session/set_mode write.
				ModeSetter: a.SetModeViaConfigOption,
				// Junie reports a custom model profile by its decorated wire id
				// and rejects the plain profile id on a model write, so reads
				// normalize to the profile id and writes decorate it (the Cursor
				// pattern).
				ModelIDNormalizer:    normalizeJunieModelID,
				ModelSetter:          a.setJunieModel,
				SubagentFromToolCall: junieSubagentFromToolCall,
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.JunieModeDefault, opts.Model())
		},
	})
}
