package kilo

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kiloQuestionToolEnv enables the native question tool for ACP sessions.
const kiloQuestionToolEnv = "KILO_ENABLE_QUESTION_TOOL"

// kiloDisableAutoUpdateEnv is Kilo's spelling of openCodeDisableAutoUpdateEnv.
// Kilo's TUI installs a patch release through npm, yarn, pnpm or bun after the
// same one-second delay, and `kilo acp` never starts the check.
const kiloDisableAutoUpdateEnv = "KILO_DISABLE_AUTOUPDATE"

var _ agent.StartFunc = Start

// Start starts a Kilo ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	// kiloLaunchEnv below refuses the same invalid value, before the process starts.
	swarm, err := kiloSwarmFromOptions(opts.Options)
	if err != nil {
		return nil, fmt.Errorf("kilo swarm: %w", err)
	}
	var started *Agent
	create := func() *Agent {
		started = newAgent(swarm)
		return started
	}
	autoApprove, err := autoApproveFromOptions(opts.Options)
	if err != nil {
		return nil, fmt.Errorf("auto-approve: %w", err)
	}
	pinned, err := kiloLaunchEnv(opts)
	if err != nil {
		return nil, err
	}
	pinned = append(pinned, kiloQuestionToolEnv+"=1", kiloDisableAutoUpdateEnv+"=1")
	ag, err := acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(), ProviderName: "kilo", BaseArgs: ACPArgs(), RCMarkerEnvKey: "KILO_CLIENT", PinnedEnv: pinned,
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:      create, Base: func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			a.configureQuestions(sink)
			a.setAutoApprove(autoApprove)
			return kiloHooks(a)
		},
		AfterHandshake: func(a *Agent, result *acp.SessionResult, opts agent.Options) error {
			a.Questions.Begin(a.Context(), a.Clock(), a.AgentID(), a.Cmd().Process.Pid)
			return a.ApplyPrimaryAgentStartup(result, opts, PrimaryAgentCode)
		},
	})
	if err != nil {
		return nil, err
	}
	// The loop ends with the process. A request that the daemon address made
	// before the loop started waits in the channel.
	go started.refreshUsageLoop()
	return ag, nil
}

func kiloHooks(f *Agent) acp.Hooks {
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
			f.requestUsageRefresh()
			verdict := f.TurnEndVerdict(err, stopped)
			return acp.PromptEnding{Retry: verdict}
		},
		ModeChannel:              acp.ModeChannelPrimaryAgent,
		PrimaryAgentHiddenFilter: IsHiddenPrimaryAgent,
		// Kilo accepts a second prompt on the same session. Both user prompts
		// and native turns must advertise that route to the input queue.
		SteersByOwnRoute: true,
		// Loading a child session subscribes the ACP connection to its output.
		SubagentFromToolCall:       SubagentFromToolCall,
		SubagentFromToolCallUpdate: f.subagentFromToolCallUpdate,
		// Auto-approve answers each permission request once, as the
		// native client's --auto option does.
		WithBypassPermission:   f.withAutoApprove,
		PermissionAllowsBypass: permissionOffersAlways,
		LocalOptionGroups:      f.localOptionGroups,
		ApplyLocalOption:       f.applyLocalOption,
	}
}
