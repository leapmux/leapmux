package claude

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// thinkingOffCapabilities retains explicit native answers, including false.
// An absent capability keeps the toggle available for catalogs that do not report it.
func thinkingOffCapabilities(models []claudeCodeModelInfo) map[string]bool {
	result := make(map[string]bool)
	seen := make(map[string]bool)
	for _, model := range models {
		id := normalizeClaudeCodeModel(model.Value)
		if id == "" || model.Disabled || seen[id] || (id == agent.DefaultModelSentinel && !isDefaultSentinel(model.Value)) {
			continue
		}
		seen[id] = true
		if model.SupportsThinkingOff != nil {
			result[id] = *model.SupportsThinkingOff
		}
	}
	return result
}

func (a *Agent) supportsThinkingOff(model string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.supportsThinkingOffLocked(model)
}

func (a *Agent) supportsThinkingOffLocked(model string) bool {
	model = normalizeClaudeCodeModel(model)
	if supported, known := a.thinkingOffSupport[model]; known {
		return supported
	}
	if model != "" && model == a.accountDefaultModel {
		if supported, known := a.thinkingOffSupport[agent.DefaultModelSentinel]; known {
			return supported
		}
	}
	return true
}

func (a *Agent) thinkingGroup(model, current string) *leapmuxv1.AvailableOptionGroup {
	group := thinkingGroupForModel(model, current)
	if !a.supportsThinkingOff(model) {
		group.Options = group.Options[:1]
		group.Mutable = false
		group.ReadOnlyReason = "This model requires extended thinking."
		if current != "" {
			group.CurrentValue = AlwaysThinkingOn
		}
	}
	return group
}

func (a *Agent) modelSubGroups(model *agent.ModelInfo) []*leapmuxv1.AvailableOptionGroup {
	groups := agent.EffortSubGroups(model)
	if model != nil {
		groups = append(groups, a.thinkingGroup(model.Id, ""))
	}
	return groups
}
