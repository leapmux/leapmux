package kimi

import "github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"

// Native agent state carries compaction independently from model turn activity.
// It supplies no trigger, so live progress must not invent manual or auto origin.
func (a *Agent) applyNativeCompactingState(state *kimiAgentState) {
	if a.IsDiscardingOutput() {
		return
	}
	if state.AgentID != kimiMainAgentID {
		if _, linked := a.children.get(state.AgentID); !linked {
			return
		}
	}
	run := a.run(state.AgentID)
	a.Mu.Lock()
	changed := run.compacting != *state.Compacting
	run.compacting = *state.Compacting
	a.Mu.Unlock()
	if !changed {
		return
	}
	if sink := a.nativeStateSink(run); sink != nil {
		if *state.Compacting {
			providerkit.PublishCompaction(sink, "")
		} else {
			providerkit.EndCompaction(sink)
		}
	}
	if run.agentID == kimiMainAgentID {
		a.PublishTurnActive()
	} else {
		a.publishChildInputState(run)
	}
	if !*state.Compacting {
		a.queueIdleStateCleanup(run)
	}
}
