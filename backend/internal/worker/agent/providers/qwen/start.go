package qwen

import (
	"context"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// qwenNoRelaunchEnv stops Qwen's CLI from relaunching itself with a larger
// heap. The relaunch adds a process between LeapMux and the agent, and a stop
// that reaches only the outer process would leave the inner one running.
const qwenNoRelaunchEnv = "QWEN_CODE_NO_RELAUNCH=1"

// qwenDisableCronEnv turns off Qwen's scheduled prompts: the `cron_*` tools,
// `loop_wakeup`, and the `/loop` skill that uses them. Qwen's own ACP bridge
// for chat channels sets it for the same reason.
//
// Qwen runs a scheduled prompt as a turn of its own, and states no start and no
// end for it: `#executeCronPromptInner` in Session.ts sends neither
// `_qwencode/start_turn` nor `_qwencode/end_turn`, and only an echo of the
// prompt marks it. So LeapMux would show the agent idle while the turn runs,
// and would send the next queued message as a session/prompt, which aborts the
// scheduled turn and empties Qwen's whole queue of them. Qwen's active-work
// report is no bracket either: it merges a scheduled turn with a goal round
// into one hold, reports on its own cadence, and is ordered with no update.
const qwenDisableCronEnv = "QWEN_CODE_DISABLE_CRON=1"

// Start starts a Qwen Code ACP agent process and performs the handshake.
//
// The launch flag states the approval mode, because Qwen's own default is
// `auto`: a classifier model would approve tool calls before the handshake
// set the mode that LeapMux shows.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration:  Registration(),
		ProviderName:  "qwen",
		BaseArgs:      []string{"--acp", "--approval-mode", launchApprovalMode(opts.PermissionMode())},
		PinnedEnv:     []string{qwenNoRelaunchEnv, qwenDisableCronEnv},
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:      func() *Agent { return &Agent{} },
		Base:          func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure(quartz.NewReal())
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.QwenModeDefault, opts.Model())
		},
	})
}

// configure returns the hooks of one agent, with the clock that paces its
// readers of background transcripts.
func (a *Agent) configure(clock quartz.Clock) acp.Hooks {
	a.stateMu.Lock()
	a.clock = clock
	a.stateMu.Unlock()
	return acp.Hooks{
		ModeChannel:    acp.ModeChannelPermissionMode,
		EffortConfigID: contracts.QwenConfigReasoningEffort,
		ModelDecorator: decorateModel,
		ExtraMethod:    a.handleExtraMethod,
		// Qwen streams a foreground subagent in the parent session, tagged with
		// the tool call that spawned it.
		ChildUpdateRoute:           childUpdateRoute,
		SessionMetadataHandler:     a.handleSessionMetadata,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		ControlRequestObserver:     a.observeControlRequest,
		// Qwen pulls steered input between two tool batches; no handshake
		// advertises that.
		SteersByOwnRoute:   true,
		FollowUpPrompt:     a.followUpPrompt,
		ClearProviderState: a.clearProviderState,
		// Qwen advertises no session/close, so the provider stops the background
		// work of a session that a context clear replaced itself.
		RetireSession: a.retireSession,
	}
}

// clearProviderState drops what the outgoing session keys, when a context
// clear replaces the session. The readers of its background transcripts stop,
// and input queued for its turn goes with the turn.
func (a *Agent) clearProviderState() {
	a.stopAllBackgroundTranscripts()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.children.spawns = nil
	a.tools = toolInputs{}
	a.steer = steerQueue{}
}

// Stop stops the readers of background transcripts, then the process.
func (a *Agent) Stop() {
	a.stopAllBackgroundTranscripts()
	a.Base.Stop()
}

// Wait waits for the process, and stops the readers of background transcripts
// once it exited, because no later record can reach an agent that ended.
func (a *Agent) Wait() error {
	err := a.Base.Wait()
	a.stopAllBackgroundTranscripts()
	return err
}
