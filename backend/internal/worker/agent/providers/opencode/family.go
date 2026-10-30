package opencode

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	// DisableAutoUpdateEnv is the spelling of the member for the flag that turns
	// OFF the upgrade of the member's own install. The start pins it to 1, so an
	// `opencode` that the agent's own tool starts does not upgrade the install
	// either: the variable reaches each child, and an inline config key does not.
	DisableAutoUpdateEnv string
	// DefaultPrimaryAgent is the primary agent that a session uses when the
	// launch options state none.
	DefaultPrimaryAgent string
	// LaunchEnv returns the "KEY=value" assignments that one member derives
	// from the launch options, such as a feature switch that the member reads
	// only at start. The start pins them as it pins the flags above. An error
	// refuses the start. It may be nil.
	LaunchEnv func(opts agent.Options) ([]string, error)
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
	autoApprove, err := autoApproveFromOptions(opts.Options)
	if err != nil {
		return nil, fmt.Errorf("auto-approve: %w", err)
	}
	pinned := []string{spec.QuestionToolEnv + "=1", spec.DisableAutoUpdateEnv + "=1"}
	if spec.LaunchEnv != nil {
		memberEnv, err := spec.LaunchEnv(opts)
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, memberEnv...)
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[T]{
		Registration:   registration,
		ProviderName:   spec.ProviderName,
		BaseArgs:       ACPArgs(),
		RCMarkerEnvKey: spec.RCMarkerEnvKey,
		PinnedEnv:      pinned,
		SessionConfig:  acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:       newAgent,
		Base:           func(a *T) *acp.Base { return &family(a).Base },
		Configure: func(a *T, sink agent.ProviderServices) acp.Hooks {
			f := family(a)
			f.configureQuestions(sink)
			// Before the process starts, so no request arrives before the value.
			f.setAutoApprove(autoApprove)
			return FamilyHooks(f)
		},
		AfterHandshake: func(a *T, handshake *acp.SessionResult, opts agent.Options) error {
			f := family(a)
			// The launched process is the shell, which a POSIX shell replaces with the
			// daemon and PowerShell does not. Discovery walks below it for that case.
			f.Questions.Begin(f.Context(), f.Clock(), f.AgentID(), f.Cmd().Process.Pid)
			return f.ApplyPrimaryAgentStartup(handshake, opts, spec.DefaultPrimaryAgent)
		},
	})
}

// FamilyHooks binds the shared protocol hooks to one member's runtime state.
func FamilyHooks(f *FamilyBase) acp.Hooks {
	return acp.Hooks{
		SegmentText:              f.segmentText,
		CaptureTurnOutputCleanup: f.captureDaemonCleanup,
		PromptPrepared:           f.resetDaemonGeneration,
		PreparePrompt:            f.prepareAudioPrompt,
		ClearProviderState:       f.clearSessionState,
		PromptEnded: func(err error, stopped bool) acp.PromptEnding {
			f.retryResolution.Reset()
			if err != nil || stopped {
				providerkit.EndCompaction(f.Sink())
			}
			if f.TurnEnded != nil {
				f.TurnEnded()
			}
			verdict := f.TurnEndVerdict(err, stopped)
			return acp.PromptEnding{Retry: verdict}
		},
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
		// family base loads each running child session into the agent, so the
		// child's own updates reach its transcript (FamilyBase.followChildSession).
		SubagentFromToolCall:       SubagentFromToolCall,
		SubagentFromToolCallUpdate: f.subagentFromToolCallUpdate,
		// Auto-approve answers each permission request once, as the
		// family's own --auto option does in its clients (AutoApproveGroup).
		WithBypassPermission:   f.withAutoApprove,
		PermissionAllowsBypass: permissionOffersAlways,
		LocalOptionGroups:      f.localOptionGroups,
		ApplyLocalOption:       f.applyLocalOption,
	}
}
