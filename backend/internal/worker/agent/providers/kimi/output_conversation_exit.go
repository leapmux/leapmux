package kimi

import (
	"cmp"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// settleConversationAfterExit writes only observed native items. The local exit
// classification supplies their completion without inventing a native outcome.
func (a *Agent) settleConversationAfterExit(run *kimiRun, completion agent.MessageCompletion) {
	output := run.conversation
	if output == nil {
		return
	}
	if a.IsDiscardingOutput() {
		// A restart discards buffered output, but still releases the spans
		// opened by requests the Worker already accepted.
		for _, publication := range output.turns {
			publication.scope.ResetSpans()
		}
		run.conversation = nil
		return
	}
	var unfinished []*kimiConversationTurnState
	for turnID, turn := range output.document.turns {
		if turn.header.Status == contracts.KimiConversationStatusRunning && output.turns[turnID] != nil {
			unfinished = append(unfinished, turn)
		}
	}
	slices.SortFunc(unfinished, func(left, right *kimiConversationTurnState) int {
		return cmp.Compare(*left.header.Ordinal, *right.header.Ordinal)
	})
	frame := kimiConversationFrame{SessionID: output.document.sessionID, AgentID: run.agentID}
	for _, turn := range unfinished {
		publication := output.turns[turn.header.TurnID]
		selection := kimiConversationSelection{flushText: true, localCompletion: completion}
		for _, itemID := range turn.order {
			item := turn.items[itemID]
			if item == nil || item.data == nil || item.data.Status != contracts.KimiConversationStatusRunning {
				continue
			}
			switch item.identity.kind {
			case contracts.KimiConversationItemKindAssistantMessage, contracts.KimiConversationItemKindReasoning:
				if item.text == nil || item.text.Len() == 0 {
					continue
				}
			case contracts.KimiConversationItemKindToolCall:
				_, opened := publication.requests[itemID]
				_, closed := publication.results[itemID]
				if !opened || closed {
					continue
				}
			default:
				continue
			}
			selection.items = append(selection.items, itemID)
		}
		if len(selection.items) != 0 {
			a.persistConversationItems(run, turn, publication, frame, selection)
		}
	}
	output.activeTurnID = ""
}
