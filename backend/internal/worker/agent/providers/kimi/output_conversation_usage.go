package kimi

import (
	"iter"
	"math"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Project the newest measured request of the current main turn. The header's
// aggregate covers the whole turn. The separate usage feed owns context size.
func (a *Agent) publishConversationStepUsage(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, steps iter.Seq[kimiConversationStep]) {
	if run.agentID != kimiMainAgentID || turn.header.Kind != contracts.KimiConversationTurnKindAgent ||
		run.conversation.activeTurnID != turn.header.TurnID || publication.historical || publication.completed {
		return
	}
	var counts providerkit.ContextTokenCounts
	var measured bool
	index := publication.usageStep
	for step := range steps {
		if step.Index < index {
			continue
		}
		if native, ok := step.Usage.tokenCounts(); ok {
			index, counts, measured = step.Index, native, true
		}
	}
	if measured {
		publication.usageStep = index
		a.publishContextTokenCounts(counts)
	}
}

func (usage *kimiConversationUsage) tokenCounts() (providerkit.ContextTokenCounts, bool) {
	if usage == nil {
		return providerkit.ContextTokenCounts{}, false
	}
	var counts [4]int64
	for i, value := range []*float64{usage.InputOther, usage.Output, usage.InputCacheRead, usage.InputCacheCreation} {
		// Counts originate in JavaScript. Avoid rounding a fraction or a number
		// that JavaScript cannot represent as an exact integer.
		if value == nil || *value < 0 || *value > 1<<53-1 || math.Trunc(*value) != *value {
			return providerkit.ContextTokenCounts{}, false
		}
		counts[i] = int64(*value)
	}
	return providerkit.ContextTokenCounts{Input: counts[0], Output: counts[1], CacheRead: counts[2], CacheWrite: counts[3]}, true
}
