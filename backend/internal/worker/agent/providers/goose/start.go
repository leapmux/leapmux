package goose

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start starts a Goose CLI ACP agent process and performs the handshake.
//
// The Native Commands option withholds the host terminal: Goose then runs
// each command itself and saves long output to a file it reports, instead of
// routing commands to the LeapMux terminal. Goose reads the capability only
// in the initialize handshake, so a change restarts the agent.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	nativeCommands, err := opts.Options.Bool(OptionNativeCommands)
	if err != nil {
		return nil, err
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "goose",
		BaseArgs:     []string{"acp"},
		NewAgent:     func() *Agent { return &Agent{nativeCommands: nativeCommands} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure()
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.GooseModeAuto, opts.Model())
		},
	})
}

// configure returns the hooks of one agent, the ones that Start installs.
func (a *Agent) configure() acp.Hooks {
	return acp.Hooks{
		WithBypassPermission:   func(claim func()) { a.WithOptionValue(agent.OptionIDPermissionMode, contracts.GooseModeAuto, claim) },
		PermissionAllowsBypass: func(*providerkit.ParsedLine) bool { return true },
		ModeChannel:            acp.ModeChannelPermissionMode,
		// Smart Approve is Goose's safe new-session mode, so it leads every rebuilt
		// list and is the mode the group badges as its default.
		PreferredFirstMode: contracts.GooseModeSmartApprove,
		// Goose's reasoning-effort axis is the convention id "thinking_effort", not the
		// well-known "effort" -- declare it so the env-effort override maps onto it.
		EffortConfigID: contracts.GooseConfigThinkingEffort,
		// Subagent tool-request observations: Goose surfaces tool REQUESTS
		// (never results) over ACP via _meta.toolNotification, so the hook
		// runs on tool_call_update. The spawn tool_call's final update is
		// the delegate ACK: it renames the row to the native session id
		// and keeps it running (observeGooseSubagentUpdate). The spawn
		// tool_call itself carries _meta.goose.toolCall
		// {toolName:"delegate", extensionName:"summon"}.
		SubagentFromToolCall:       gooseSubagentFromToolCall,
		SubagentFromToolCallUpdate: a.observeGooseSubagentUpdate,
		ToolNotification:           a.observeGooseToolNotification,
		// Goose sends its live status and its usage totals ONLY to a client
		// that asks for them, on `_goose/unstable/session/update`. Without the
		// advertisement the counters stayed empty and every status line was
		// lost. Verified against goose 1.50.1: the handshake accepts the flag
		// and answers with its own `_meta.goose` capabilities.
		ClientCapabilityMeta: map[string]any{
			gooseSteerNamespace: map[string]any{"customNotifications": true},
		},
		DisableHostTerminal:    a.nativeCommands,
		LocalOptionGroups:      a.nativeCommandGroups,
		ExtraMethod:            a.handleGooseExtraMethod,
		ToolOutputComplete:     a.clearGooseToolOutput,
		SessionMetadataHandler: a.captureSteerRunID,
		// The last agent message of a prompt decides the goal's end and the
		// retry verdict of the turn. A new prompt forgets the message and the
		// compaction mark of the last one.
		SessionUpdateHandler:  a.observeGooseSessionUpdate,
		PromptPrepared:        a.prepareGoosePrompt,
		PromptEnded:           a.finishGoosePrompt,
		AdvertisedSteerMethod: gooseSteeringMethod,
	}
}
