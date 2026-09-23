package cursor

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start starts a Cursor CLI ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "cursor",
		BaseArgs:     []string{"acp"},
		NewAgent:     func() *Agent { return &Agent{} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			storeQuery := agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}
			transcript := newCursorToolTranscriptWithObserver(
				ctx,
				sink,
				func() string { return cursorACPStorePath(storeQuery, a.CurrentSessionID()) },
				a.observeCursorTaskRecord,
			)
			a.transcript = transcript
			return acp.Hooks{
				Sink: transcript,
				// Cursor stores the normalized (display) model id, not the wire form. The
				// registration hands the same normalizeCursorModelID to NormalizeModelID, so
				// the offline-label and live paths can't diverge.
				InitialModel:      normalizeCursorModelID(opts.Model()),
				ModelIDNormalizer: normalizeCursorModelID,
				// Cursor writes models through setCursorModel (id -> wire form); the base
				// UpdateSettings / reapply / refresh use this via effectiveSetModel, so Cursor
				// needs no overrides of its own.
				ModelSetter:    a.setCursorModel,
				ModelDecorator: decorateCursorModel,
				ModeChannel:    acp.ModeChannelPermissionMode,
				ExtraMethod:    a.handleExtraMethod,
				// Cursor's Task tool supplies the prompt. Its local store supplies the
				// report that ACP omits. The tool-call id links both to one child.
				SubagentFromToolCall:       a.spawnObservation,
				SubagentFromToolCallUpdate: a.finishedObservation,
				ClearProviderState: func() {
					a.clearTaskToolCalls()
					transcript.Reset()
				},
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, ModeAgent, normalizeCursorModelID(opts.Model()))
		},
	})
}
