package kiro

import (
	"context"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// kiroBaseArgs start Kiro's Agent Client Protocol server on its v3 engine.
//
//   - `--agent-engine v3` states the engine, because the default is the v2
//     engine, and a setting of the user's (`chat.agentEngine`) can change it.
//     Only v3 raises questions and MCP forms, runs workflows and goals, and
//     reports config options for the model, the effort and the autopilot.
//   - `--auth-method cli` makes the CLI answer the engine's token requests
//     itself. Without it the engine asks the client for a token
//     (`_kiro/auth/getAccessToken`), and session/new waits for an answer that
//     LeapMux does not give.
var kiroBaseArgs = []string{"acp", "--agent-engine", "v3", "--auth-method", "cli"}

// Start starts a Kiro ACP agent process and performs the handshake.
//
// The model and the effort are not launch flags: the base writes them with
// session/set_config_option after the handshake, which reports the result. The
// model write goes out even for the model that the session already runs,
// because Kiro reports the effort axis of a model only after that write.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration:  Registration(),
		ProviderName:  "kiro",
		BaseArgs:      kiroBaseArgs,
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionLoad},
		NewAgent:      func() *Agent { return &Agent{} },
		Base:          func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure(opts)
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			if err := a.ApplyPermissionModeStartup(handshake, opts, contracts.KiroModeDefault, opts.Model()); err != nil {
				return err
			}
			// A goal of a resumed session can still run or wait in Kiro's own
			// store. Its controls need the run's id, which only Kiro knows.
			a.recoverGoalRun(opts)
			return nil
		},
	})
}

// configure returns the hooks of one agent. The policy preset must be known
// before session/new, which states it, so configure also records the preset
// that the launch asks for.
func (a *Agent) configure(opts agent.Options) acp.Hooks {
	a.stateMu.Lock()
	a.policy.current = initialPolicyPreset(opts.Get(contracts.KiroOptionPolicyPreset))
	a.stateMu.Unlock()
	return acp.Hooks{
		ModeChannel:    acp.ModeChannelPermissionMode,
		EffortConfigID: contracts.KiroConfigEffortLevel,
		// Kiro reports the effort axis of the session's model only after a
		// model write, so the start writes the model that the session runs.
		ModelWriteRevealsOptions: true,
		// Kiro runs its shell commands itself. A client that offers a host
		// terminal makes Kiro ask for the shell type while it opens a session,
		// and a session that LeapMux cannot answer that for fails to open.
		DisableHostTerminal:  true,
		ClientCapabilityMeta: kiroClientCapabilities(),
		SessionParams:        a.adjustSessionParams,
		ModelDecorator:       decorateModel,
		LocalOptionGroups:    a.localOptionGroups,
		ApplyLocalOption:     a.applyLocalOption,
		ExtraMethod:          a.handleExtraMethod,
		// Kiro states a turn's markers, its usage and its errors on
		// session_info_update, and the updates of a turn it started by itself
		// carry the mark of that turn.
		SessionMetadataHandler: a.handleSessionMetadata,
		// A prompt holds the display error of its failure until its end, whose
		// JSON-RPC error can state the same text.
		PromptEnded: a.handlePromptEnded,
		// Two answers of the model can arrive in one turn with no update
		// between them: the plan mode's last answer, and the first answer of
		// the mode that runs the plan. Each chunk states its answer.
		ChunkMessageID: chunkMessageID,
		// A subagent streams in the parent session, and each update of it
		// carries the id of its subtask.
		ChildUpdateRoute: a.childUpdateRoute,
		// A workflow step runs in a session of its own, and the first message
		// of that session is the instruction of the step.
		ChildUserMessages:          true,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		ControlRequestObserver:     a.observeControlRequest,
		ToolOutputComplete:         a.forgetToolOutput,
		// Kiro steers through `_session/steer`, which its initialize response
		// does not advertise.
		SteersByOwnRoute:   true,
		ClearProviderState: a.clearProviderState,
		RetireSession:      a.retireSession,
	}
}

// clearProviderState drops what the ids of the outgoing session's tool calls
// key, when a context clear opens a new session. The policy belongs to the
// process, and the workflow runs belong to a session, which retireSession
// ends.
func (a *Agent) clearProviderState() {
	a.stateMu.Lock()
	a.turns = turnState{}
	a.children = childState{}
	a.controls = controlIndex{}
	a.output = toolOutputState{}
	// A pending compaction belongs to the old session, and the swap ended the
	// turn that it held. Its late answer then finds no compaction of its own.
	a.compaction = compactionState{lastID: a.compaction.lastID}
	a.stateMu.Unlock()
}

// retireSession ends the workflow runs of a session that a context clear
// replaced (Hooks.RetireSession). A run of the outgoing session -- a goal
// among them -- would go on in Kiro with no card and no row to show or stop
// it, because every notification of it states the old session. So each run
// that did not end is cancelled: the runs that live notifications reported,
// and the goal run that the start recovered from Kiro's store.
//
// The base calls it only for a session that the clear replaced. An agent that
// answers session/new with the outgoing id serves that session still, and its
// runs go on.
func (a *Agent) retireSession(string) {
	a.goalRestoreMu.Lock()
	a.stateMu.Lock()
	runs := a.unfinishedRunsLocked()
	if a.goal.runID != "" && !slices.Contains(runs, a.goal.runID) {
		runs = append(runs, a.goal.runID)
	}
	a.workflows = workflowState{}
	a.goal = goalState{}
	a.stateMu.Unlock()
	a.goalRestoreMu.Unlock()
	slices.Sort(runs)
	for _, workflowID := range runs {
		a.cancelRunDetached(workflowID)
	}
}
