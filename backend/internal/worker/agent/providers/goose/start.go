package goose

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start starts a Goose CLI ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "goose",
		BaseArgs:     []string{"acp"},
		NewAgent:     func() *Agent { return &Agent{} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return acp.Hooks{
				ModeChannel: acp.ModeChannelPermissionMode,
				// Smart Approve is Goose's safe new-session mode, so it leads every rebuilt
				// list and is the mode the group badges as its default.
				PreferredFirstMode: contracts.GooseModeSmartApprove,
				// Goose's reasoning-effort axis is the convention id "thinking_effort", not the
				// well-known "effort" -- declare it so the env-effort override maps onto it.
				EffortConfigID: contracts.GooseConfigThinkingEffort,
				// Subagent tool-request observations: Goose surfaces tool REQUESTS
				// (never results) over ACP via _meta.toolNotification, so the hook
				// runs on tool_call_update. The spawn tool_call's final update
				// closes the registry row. The spawn tool_call itself carries
				// _meta.goose.toolCall {toolName:"delegate", extensionName:"summon"}.
				SubagentFromToolCall:       gooseSubagentFromToolCall,
				SubagentFromToolCallUpdate: gooseSubagentFromToolCallUpdate,
				ToolOutput:                 a.gooseToolOutput,
				ToolNotification:           a.observeGooseToolNotification,
				// Goose sends its live status and its usage totals ONLY to a client
				// that asks for them, on `_goose/unstable/session/update`. Without the
				// advertisement the counters stayed empty and every status line was
				// lost. Verified against goose 1.50.1: the handshake accepts the flag
				// and answers with its own `_meta.goose` capabilities.
				ClientCapabilityMeta: map[string]any{
					gooseSteerNamespace: map[string]any{"customNotifications": true},
				},
				ExtraMethod:            a.handleGooseExtraMethod,
				ToolOutputComplete:     a.clearGooseToolOutput,
				SessionMetadataHandler: a.captureSteerRunID,
				AdvertisedSteerMethod: func(response []byte) string {
					return acp.ParseAdvertisedMethod(response, gooseSteerNamespace, gooseSteerMethod)
				},
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.GooseModeAuto, opts.Model())
		},
	})
}
