package dirac

import (
	"context"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// diracNoAutoUpdateEnv stops the auto-updater. The worker pins it so a test
// machine never downloads a release in the middle of a run.
const diracNoAutoUpdateEnv = "DIRAC_NO_AUTO_UPDATE=1"

// Start starts a Dirac ACP process and performs the handshake.
//
// `dirac --acp` is the launch. The provider route and the model come from the
// DIRAC_PROVIDER / DIRAC_MODEL / DIRAC_API_KEY / DIRAC_BASE_URL environment,
// which the E2E isolation recipe and a real installation both set. A resume
// goes through `session/load`, because `session/resume` is unregistered in the
// releases this package was written against.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "dirac",
		BaseArgs:     []string{"--acp"},
		PinnedEnv:    []string{diracNoAutoUpdateEnv},
		SessionConfig: acp.SessionConfig{
			NewMethod:    acp.MethodSessionNew,
			ResumeMethod: acp.MethodSessionLoad,
		},
		NewAgent: func() *Agent { return &Agent{} },
		Base:     func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure(quartz.NewReal())
		},
	})
}

// configure returns the hooks of one agent.
func (a *Agent) configure(_ quartz.Clock) acp.Hooks {
	return acp.Hooks{
		ModeChannel:    acp.ModeChannelPermissionMode,
		EffortConfigID: contracts.DiracConfigReasoningEffort,
		// Dirac advertises `dev.dirac/whisper` in the initialize `_meta` as a
		// bare capability flag, not the sessionSteer.method shape the shared
		// parser reads, so the advertised steer method is a constant of this
		// package.
		AdvertisedSteerMethod: func(response []byte) string {
			return diracAdvertisedSteerMethod(response)
		},
		ExtraMethod:          a.handleExtraMethod,
		SubagentFromToolCall: diracSubagentFromToolCall,
		// Dirac's `respond` tool is the control plane: a question rides
		// elicitation and a plan defers its approval to the next prompt. The
		// base answers both, so no provider control reader is needed.
	}
}
