package kimi

import (
	"cmp"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Keep the prior item views until the dispatcher validates and applies removal.
// A live item removal mutates its turn in place, so capture that item separately.
func conversationTurnsBeforeRemoval(document *kimiConversationState, frame kimiConversationFrame) []*kimiConversationTurnState {
	switch frame.Type {
	case contracts.KimiFrameTurns:
		return slices.Collect(maps.Values(document.turns))
	case contracts.KimiFrameTurnsRemoved:
		turns := make([]*kimiConversationTurnState, 0, len(frame.TurnIDs))
		for _, id := range frame.TurnIDs {
			if turn := document.turns[id]; turn != nil {
				turns = append(turns, turn)
			}
		}
		return turns
	case contracts.KimiFrameItemRemoved:
		if turn := document.turns[frame.TurnID]; turn != nil {
			if item := turn.items[frame.ItemID]; item != nil && item.identity.kind == contracts.KimiConversationItemKindToolCall {
				return []*kimiConversationTurnState{{header: turn.header, steps: turn.steps,
					items: map[string]*kimiConversationItemState{frame.ItemID: item}, order: []string{frame.ItemID}}}
			}
		}
	}
	return nil
}

func (a *Agent) closeWithdrawnConversationSpans(run *kimiRun, output *kimiConversationOutput, frame kimiConversationFrame, previous []*kimiConversationTurnState) {
	slices.SortFunc(previous, func(left, right *kimiConversationTurnState) int {
		return cmp.Compare(*left.header.Ordinal, *right.header.Ordinal)
	})
	for _, turn := range previous {
		turnID := turn.header.TurnID
		publication := output.turns[turnID]
		if publication == nil || len(publication.requests) == 0 {
			continue
		}
		current := output.document.turns[turnID]
		selection := kimiConversationSelection{localCompletion: agent.MessageCompletionInterrupted}
		for _, id := range turn.order {
			if current != nil && current.items[id] != nil {
				continue
			}
			_, opened := publication.requests[id]
			_, closed := publication.results[id]
			if opened && !closed {
				selection.items = append(selection.items, id)
			}
		}
		if len(selection.items) != 0 {
			a.persistConversationItems(run, turn, publication, frame, selection)
		}
	}
}
