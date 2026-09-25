package opencode

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// FamilySpec states what one member of the OpenCode family changes about the
// start that the family shares. OpenCode and Kilo run the same daemon code, so
// everything else about the start is the same for both.
type FamilySpec struct {
	// ProviderName is the name of the process in the log.
	ProviderName string
	// RCMarkerEnvKey is the variable that tells the daemon that a client
	// launched it. The start removes an inherited value, and sets it again for a
	// login shell only.
	RCMarkerEnvKey string
	// QuestionToolEnv is the spelling of the member for the flag that turns ON
	// the question tool of the daemon, which the question bridge answers. The
	// start pins it to 1.
	QuestionToolEnv string
	// DefaultPrimaryAgent is the primary agent that a session uses when the
	// launch options state none.
	DefaultPrimaryAgent string
}

// StartFamily starts one member of the OpenCode family and performs the
// handshake. T is the agent type of the member, and family returns the
// FamilyBase that T embeds.
func StartFamily[T any](
	ctx context.Context,
	opts agent.Options,
	sink agent.ProviderServices,
	registration agent.Registration,
	spec FamilySpec,
	newAgent func() *T,
	family func(*T) *FamilyBase,
) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[T]{
		Registration:   registration,
		ProviderName:   spec.ProviderName,
		BaseArgs:       ACPArgs(),
		RCMarkerEnvKey: spec.RCMarkerEnvKey,
		PinnedEnv:      []string{spec.QuestionToolEnv + "=1"},
		SessionConfig:  acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:       newAgent,
		Base:           func(a *T) *acp.Base { return &family(a).Base },
		Configure: func(a *T, sink agent.ProviderServices) acp.Hooks {
			family(a).Questions.Configure(sink)
			return FamilyHooks()
		},
		AfterHandshake: func(a *T, handshake *acp.SessionResult, opts agent.Options) error {
			f := family(a)
			// The launched process is the shell, which a POSIX shell replaces with the
			// daemon and PowerShell does not. Discovery walks below it for that case.
			f.Questions.Begin(f.Context(), f.AgentID(), f.Cmd().Process.Pid)
			return f.ApplyPrimaryAgentStartup(handshake, opts, spec.DefaultPrimaryAgent)
		},
	})
}

// FamilyHooks returns what every member of the OpenCode family changes about the
// ACP base. It holds no state, so the start and a test read the same hooks.
func FamilyHooks() acp.Hooks {
	return acp.Hooks{
		ModeChannel:              acp.ModeChannelPrimaryAgent,
		PrimaryAgentHiddenFilter: IsHiddenPrimaryAgent,
		// This family steers with a plain second session/prompt on the SAME
		// session, which is the Agent Client Protocol's own mechanism and not
		// one provider's extension -- so it needs no advertised steer method.
		// The hook sits on the FAMILY start, not on one member, so Kilo, which
		// runs the same daemon, steers too. It is a hook rather than an override
		// of SupportsSteering, because the base reads the same answer for the
		// steerable flag of each turn, and an override reached only one of the
		// two: a turn that the daemon started was published as not steerable,
		// and the queue held a steer back behind it.
		SteersByOwnRoute: true,
		// The family omits child-session events from the root ACP stream. The
		// prompt and the final task result still form an inspectable transcript.
		SubagentFromToolCall:       SubagentFromToolCall,
		SubagentFromToolCallUpdate: SubagentFromToolCallUpdate,
	}
}
