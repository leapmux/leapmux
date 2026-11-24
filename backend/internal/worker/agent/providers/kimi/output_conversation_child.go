package kimi

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// A child's native turn controls its activity. Its coordinator task retains an
// independent outcome, including when the child finishes successfully.
func (a *Agent) finishConversationChildTurn(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput) {
	output := run.conversation
	if output.activeTurnID != "" && output.activeTurnID != turn.header.TurnID || *turn.header.Ordinal < output.latestOrdinal {
		return
	}
	child, linked := a.children.get(run.agentID)
	if !linked {
		return
	}
	wasActive := output.activeTurnID == turn.header.TurnID
	output.latestOrdinal = *turn.header.Ordinal
	a.finishConversationActivity(run, turn.header.TurnID)
	if !wasActive {
		// Completed history still restores the child tab's readiness before cleanup.
		a.publishChildInputState(run)
	}
	a.Mu.Lock()
	run.userTurn = false
	a.Mu.Unlock()
	status := bgtask.StatusFailed
	switch turn.header.Outcome {
	case contracts.KimiConversationOutcomeCompleted:
		status = bgtask.StatusSucceeded
		if !publication.userTurn {
			status = bgtask.StatusEndedWithUnknownOutcome
		}
	case contracts.KimiConversationOutcomeInterrupted:
		status = bgtask.StatusStopped
	}
	if agent.OutputRetained(a.closeSubagentRegistryRow(run.agentID, child, status)) {
		a.queueChildCleanup(run)
	}
}

func (a *Agent) finishWithdrawnConversationTurn(run *kimiRun, turnID string) {
	if run.conversation.activeTurnID != turnID {
		return
	}
	a.finishConversationActivity(run, turnID)
	if run.agentID == kimiMainAgentID {
		return
	}
	a.Mu.Lock()
	run.userTurn = false
	a.Mu.Unlock()
	child, linked := a.children.get(run.agentID)
	if !linked {
		return
	}
	if agent.OutputRetained(a.closeSubagentRegistryRow(run.agentID, child, bgtask.StatusEndedWithUnknownOutcome)) {
		a.queueChildCleanup(run)
	}
}

func (a *Agent) reviveNativeChildRow(run *kimiRun) bool {
	child, linked := a.children.get(run.agentID)
	if !linked || run.childLifetime == nil {
		return false
	}
	if !child.ended {
		return true
	}
	if err := a.queueRegistryRevive(run.childLifetime.parent, child.rowKey); !agent.OutputRetained(err) {
		return false
	}
	a.children.update(run.agentID, func(current *kimiChild) { current.ended = false })
	return true
}
