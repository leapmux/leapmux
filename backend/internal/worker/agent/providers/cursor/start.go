package cursor

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start starts a Cursor CLI ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	bypass, err := opts.Options.Bool(contracts.CursorOptionBypassPermissions)
	if err != nil {
		return nil, err
	}
	if opts.ResumeSessionID != "" {
		storedBypass, err := readCursorBypassPolicy(ctx, agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}, opts.ResumeSessionID)
		if err != nil {
			return nil, err
		}
		bypass = bypass || storedBypass
	}
	args := []string{"acp"}
	if bypass {
		args = []string{"--force", "acp"}
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "cursor",
		BaseArgs:     args,
		NewAgent:     func() *Agent { return &Agent{bypassPermissions: bypass} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			return a.configure(ctx, opts, sink)
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, ModeAgent, normalizeCursorModelID(opts.Model()))
		},
	})
}

// configure returns the hooks of one agent, the ones that Start installs.
func (a *Agent) configure(ctx context.Context, opts agent.Options, sink agent.ProviderServices) acp.Hooks {
	storeQuery := agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}
	transcript := newCursorToolTranscriptWithObserver(
		ctx,
		sink,
		func() string { return cursorACPStorePath(storeQuery, a.CurrentSessionID()) },
		a.captureCursorTaskRecordObserver,
	)
	a.transcript = transcript
	return acp.Hooks{
		LocalOptionGroups:      a.localOptionGroups,
		Sink:                   transcript,
		ClientCapabilityMeta:   map[string]any{"subagents": true, "parameterizedModelPicker": true},
		SessionMetadataHandler: a.handleCursorSubagentLifecycle,
		SessionUpdateHandler:   a.handleCursorSessionUpdate,
		// Cursor stores the normalized (display) model id, not the wire form. The
		// registration hands the same normalizeCursorModelID to NormalizeModelID, so
		// the offline-label and live paths can't diverge.
		InitialModel:      normalizeCursorModelID(opts.Model()),
		ModelIDNormalizer: normalizeCursorModelID,
		// Cursor writes models through setCursorModel (id -> wire form); the base
		// UpdateSettings / reapply / refresh use this via effectiveSetModel, so Cursor
		// needs no model-specific lifecycle overrides.
		ModelSetter: a.setCursorModel,
		// Native model parameters are separate selectors. Keep the model's supplied display name.
		ModelDecorator: func(m *agent.ModelInfo, _ json.RawMessage) { decorateCursorModel(m) },
		ModeChannel:    acp.ModeChannelPermissionMode,
		ExtraMethod:    a.handleExtraMethod,
		// Cursor's Task tool supplies the prompt. Its local store supplies the
		// report that ACP omits. The tool-call id links both to one child.
		SubagentFromToolCall:       a.spawnObservation,
		SubagentFromToolCallUpdate: a.finishedObservation,
		// The last root chunk of a prompt states its failure, and the
		// prompt's end decides the retry verdict from it. A new prompt
		// forgets the chunk of the last one.
		PromptPrepared: a.clearCursorChunk,
		PromptEnded:    a.finishCursorPrompt,
		ClearProviderState: func() {
			a.clearTaskToolCalls()
			a.clearCursorChunk()
			transcript.Reset()
		},
	}
}
