package letta

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// publishLettaUsage reports the counts that a native usage_statistics delta states.
// The delta gives no model context window or current context size.
func publishLettaUsage(target agent.ProviderServices, delta *lettaDelta) {
	if target == nil || delta.PromptTokens == nil || delta.CompletionTokens == nil ||
		*delta.PromptTokens < 0 || *delta.CompletionTokens < 0 ||
		(delta.TotalTokens != nil && *delta.TotalTokens < 0) {
		return
	}
	usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:  *delta.PromptTokens,
		Output: *delta.CompletionTokens,
	})
	target.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
}
