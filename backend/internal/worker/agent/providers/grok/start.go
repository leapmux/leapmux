package grok

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// grokFolderTrustCapability is the client capability that makes Grok ASK
// whether a repository's own configuration may load. Without it Grok keeps
// every repository untrusted and never says so: the repository's instructions,
// MCP servers and hooks silently stay off.
const grokFolderTrustCapability = "x.ai/folderTrust"

// Start starts a Grok Build ACP agent process and performs the handshake.
//
// `--no-leader` keeps the process to itself. Without it, a user config.toml
// that sets `[cli] use_leader = true` attaches this process to a shared leader
// process that other clients also reach. The sessions then share the state of
// that process, and it ignores the flags that this process states.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration:  Registration(),
		ProviderName:  "grok",
		BaseArgs:      []string{"agent", "--no-leader", "stdio"},
		SessionConfig: acp.SessionConfig{NewMethod: acp.MethodSessionNew, ResumeMethod: acp.MethodSessionResume},
		NewAgent:      func() *Agent { return &Agent{} },
		Base:          func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure(opts)
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.GrokModeDefault, opts.Model())
		},
	})
}

// configure returns the hooks of one agent. The approval mode must be known
// before session/new, which states it, so configure also records the approval
// mode that the launch asks for.
func (a *Agent) configure(opts agent.Options) acp.Hooks {
	a.stateMu.Lock()
	a.approval.current = initialApprovalMode(opts.Get(contracts.GrokOptionApprovalMode))
	a.stateMu.Unlock()
	return acp.Hooks{
		ModeChannel:    acp.ModeChannelPermissionMode,
		EffortConfigID: contracts.GrokConfigReasoningEffort,
		// Grok runs its shell commands itself. Through the host terminal it would
		// lose its own background commands and their completion events.
		DisableHostTerminal: true,
		InitializeMeta: map[string]any{
			"clientType":       grokClientIdentifier,
			"clientIdentifier": grokClientIdentifier,
		},
		ClientCapabilityMeta: map[string]any{
			grokFolderTrustCapability: map[string]any{"interactive": true},
		},
		InitializeResponse:     a.seedAvailableCommands,
		SessionParams:          a.adjustSessionParams,
		PromptParams:           a.adjustPromptParams,
		ModelDecorator:         decorateModel,
		LocalOptionGroups:      a.localOptionGroups,
		ApplyLocalOption:       a.applyLocalOption,
		ExtraMethod:            a.handleExtraMethod,
		ControlRequestObserver: a.observeControlRequest,
		// Folder trust is Grok's one request with no cancel answer, and it asks
		// about the repository rather than the turn. See grokControlCancelAnswer.
		AnswerlessControlsOutliveTurns: true,
		// Grok steers through `_x.ai/interject`, which its initialize response
		// does not advertise.
		SteersByOwnRoute:           true,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		ClearProviderState:         a.clearProviderState,
	}
}

// clearProviderState drops what the outgoing session's ids key, when a context
// clear replaces the session. The approval mode belongs to the process and
// stays.
func (a *Agent) clearProviderState() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.children.reset()
	a.controls = controlIndex{}
	a.turns = turnState{}
}
