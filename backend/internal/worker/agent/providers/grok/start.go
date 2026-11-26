package grok

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// grokFolderTrustCapability is the client capability that makes Grok ASK
// whether a repository's own configuration may load. Without it Grok keeps
// every repository untrusted and never says so: the repository's instructions,
// MCP servers and hooks silently stay off.
const grokFolderTrustCapability = "x.ai/folderTrust"

// grokDisableAutoUpdaterEnv turns off Grok's background update. `grok agent
// stdio` starts `grok update --trigger=auto_background` when the running
// executable is the managed install ($GROK_HOME/bin/grok, which the install
// script creates), and the update rewrites that install and its symlinks in the
// middle of the session. Any value except an empty one, `0`, `false`, `off` or `no`
// turns it off. `--no-auto-update` cannot replace it: `agent` does not accept the
// flag.
const grokDisableAutoUpdaterEnv = "GROK_DISABLE_AUTOUPDATER=1"

// The child message route and its opaque addresses require this native feature.
// Pin it before initialization so each advertised child send has native support.
const grokActiveAgentMessagesEnv = "GROK_ACTIVE_AGENT_MESSAGES=1"

var grokStoreProfile = envprobe.Request{
	PathVariables: map[string]string{"GROK_HOME": "", "HOME": "", "USERPROFILE": ""},
}

// Start starts a Grok Build ACP agent process and performs the handshake.
//
// `--no-leader` keeps the process to itself. Without it, a user config.toml
// that sets `[cli] use_leader = true` attaches this process to a shared leader
// process that other clients also reach. The sessions then share the state of
// that process, and it ignores the flags that this process states.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "grok",
		BaseArgs:     []string{"agent", "--no-leader", "stdio"},
		PinnedEnv:    []string{grokDisableAutoUpdaterEnv, grokActiveAgentMessagesEnv},
		ObserveEnvironment: &launch.ObservationRequest{
			PathVariables: grokStoreProfile.PathVariables,
		},
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
	a.steerReplies.home = grokHome(agent.StoredSessionQuery{HomeDir: opts.HomeDir})
	a.steerReplies.workingDir = opts.WorkingDir
	return acp.Hooks{
		// A dismissed question lets Grok resume its model loop.
		CancelBeforeControlWithdrawal: true,
		WithBypassPermission: func(claim func()) {
			a.stateMu.Lock()
			defer a.stateMu.Unlock()
			if a.approval.current == contracts.GrokApprovalModeAlwaysApprove {
				claim()
			}
		},
		PermissionAllowsBypass: func(*providerkit.ParsedLine) bool { return true },
		ModeChannel:            acp.ModeChannelPermissionMode,
		EffortConfigID:         contracts.GrokConfigReasoningEffort,
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
		InitializeResponse: func(response []byte) {
			a.useGrokStoreProfile()
			a.seedAvailableCommands(response)
		},
		SessionParams:     a.adjustSessionParams,
		PromptParams:      a.adjustPromptParams,
		ModelDecorator:    decorateModel,
		LocalOptionGroups: a.localOptionGroups,
		ApplyLocalOption:  a.applyLocalOption,
		ExtraMethod:       a.handleExtraMethod,
		ControlRequestObserver: func(sessionID string, line *providerkit.ParsedLine) {
			a.observeControlRequest(sessionID, line)
			a.observeGrokSteerControl(sessionID)
		},
		ControlOwner: a.grokControlOwner,
		// Folder trust is Grok's one request with no cancel answer, and it asks
		// about the repository rather than the turn. See grokControlCancelAnswer.
		AnswerlessControlsOutliveTurns: true,
		// Grok steers through `_x.ai/interject`, which its initialize response
		// does not advertise.
		SteersByOwnRoute:           true,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		// A final tool update ends the call its badge watched.
		ToolOutputComplete: a.completeToolOutput,
		// A stopped turn clears every permission-check badge for the current
		// session; normal and error turns keep badges for open spans alive
		// until those calls finish. Every prompt end forgets a pending retry row.
		PromptEnded: func(_ error, stopped bool) acp.PromptEnding {
			if stopped {
				a.clearPermissionChecks(a.CurrentSessionID())
			}
			a.finishLingeringPromptSteer()
			a.retryResolution.Reset()
			a.stateMu.Lock()
			a.chunkStreamStart = 0
			a.stateMu.Unlock()
			// Every prompt end clears a live compaction: a native end frame
			// usually arrived first, and a stop or a failure never gets one.
			providerkit.EndCompaction(a.Sink())
			return acp.PromptEnding{}
		},
		IsCancelledTurnEnd: grokIsCancelledTurnEnd,
		// The first genuine model output after a retry resolves its row. The
		// handler claims no update.
		SessionUpdateHandler: a.observeRetryContent,
		// The first chunk of a new model request drops the text that the
		// previous request left buffered, which a retry discarded (see
		// observeNotificationMetadata).
		SessionNotificationMetadata: a.observeNotificationMetadata,
		UpdateMessageID:             a.grokUpdateMessageID,
		AssistantTextKey:            a.grokAssistantTextKey,
		CaptureReplyReconciliation:  a.captureGrokSteerReconciliation,
		ClearProviderState:          a.clearProviderState,
		BeforeWaitCleanup:           a.finishGrokSteerJobs,
	}
}

func (a *Agent) useGrokStoreProfile() {
	snapshot, ok := grokStoreProfile.DecodeObservation(a.PreambleMetaValue(envprobe.ObservationKey))
	if !ok {
		return
	}
	home := snapshot.Paths["GROK_HOME"]
	if home == "" {
		homeVariable := "HOME"
		if runtime.GOOS == "windows" {
			homeVariable = "USERPROFILE"
		}
		if base := snapshot.Paths[homeVariable]; base != "" {
			home = filepath.Join(base, ".grok")
		}
	}
	if home != "" {
		a.steerReplies.home = home
	}
	if snapshot.WorkingDir != "" {
		a.steerReplies.workingDir = snapshot.WorkingDir
	}
}

// clearProviderState drops what the outgoing session's ids key, when a context
// clear replaces the session. The approval mode belongs to the process and
// stays.
func (a *Agent) clearProviderState() {
	a.abandonGrokSteerReplies()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.children.reset()
	a.controls = controlIndex{}
	a.turns = turnState{}
}
