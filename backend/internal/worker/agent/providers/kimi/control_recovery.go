package kimi

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
)

// kimiControlReplay retains old instances independently of conversation recovery.
// dispatchMu protects this replay and its observed native interaction identities.
type kimiControlReplay struct {
	sessionID string
	pending   map[string]*kimiPendingControl
	seen      map[string]struct{}
}

func (pending *kimiPendingControl) matchesInteraction(interaction kimiInteraction) bool {
	return pending.sessionID == interaction.SessionID && pending.agentID == interaction.agent() &&
		(pending.kind == kimiControlApproval && interaction.Kind == contracts.KimiInteractionKindApproval ||
			pending.kind == kimiControlQuestion && interaction.Kind == contracts.KimiInteractionKindQuestion) &&
		(pending.toolCallID == "" || interaction.ToolCallID == "" || pending.toolCallID == interaction.ToolCallID)
}

// The first subscribe response precedes its pending interactions in dispatch.
func (a *Agent) captureControlReplay(sessionID string) {
	replay := &kimiControlReplay{sessionID: sessionID, pending: make(map[string]*kimiPendingControl), seen: make(map[string]struct{})}
	a.Mu.Lock()
	for id, pending := range a.controls {
		if pending.sessionID == sessionID {
			replay.pending[id] = pending
		}
	}
	a.Mu.Unlock()
	a.controlReplay = replay
}

func (a *Agent) observeControlReplay(interaction kimiInteraction) {
	replay := a.controlReplay
	if replay == nil || replay.sessionID != interaction.SessionID {
		return
	}
	a.Mu.Lock()
	pending := replay.pending[interaction.InteractionID]
	if pending != nil && a.controls[interaction.InteractionID] == pending && pending.matchesInteraction(interaction) {
		replay.seen[interaction.InteractionID] = struct{}{}
	}
	a.Mu.Unlock()
}

// Kimi serializes subscribe commands. Its first command sends pending interactions
// immediately after its response, before the next command can send a response.
// The second response therefore proves that the first control replay is complete.
func (a *Agent) confirmControlReplay(ctx context.Context, sessionID string) {
	a.dispatchMu.Lock()
	replay := a.controlReplay
	a.finishDispatch()
	if replay == nil || replay.sessionID != sessionID {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, a.APITimeout())
	defer cancel()
	var observed atomic.Bool
	complete := func(accepted bool) {
		if observed.CompareAndSwap(false, true) {
			a.stream.queueResponseBarrier(func() { a.finishControlReplay(replay, accepted) })
		}
	}
	err := a.stream.sendSessionSubscribe(ctx, sessionID, func(frame kimiFrame) {
		complete(frame.Type == kimiFrameTaskResponse && frame.Code == kimiCodeOK)
	})
	if err != nil {
		complete(false)
		slog.Warn("confirm the Kimi pending-control replay", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}

func (a *Agent) finishControlReplay(replay *kimiControlReplay, accepted bool) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	if a.controlReplay != replay {
		return
	}
	a.controlReplay = nil
	a.Mu.Lock()
	if !accepted || a.sessionID != replay.sessionID || a.StoppedLocked() || a.ProcessExitedLocked() {
		a.Mu.Unlock()
		return
	}
	var withdrawn []string
	for id, pending := range replay.pending {
		_, seen := replay.seen[id]
		if seen || a.controls[id] != pending {
			continue
		}
		delete(a.controls, id)
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		if pending.published && !pending.answered {
			withdrawn = append(withdrawn, id)
		}
	}
	a.Mu.Unlock()
	sort.Strings(withdrawn)
	for _, id := range withdrawn {
		a.sink.CancelControlRequest(id)
	}
}
