package providerkit

import "github.com/leapmux/leapmux/generated/contracts"

// ContextTokenCounts is the four per-request token counts of the `context_usage`
// broadcast, and the ONE place their key names are written.
//
// Every provider that reports usage projects the same four keys: Claude, Codex,
// Pi, ZCode, and the ACP base. Each one spelled the four out at its own site,
// so a fifth count could reach some of them and not the others. The counts a
// provider does not measure stay zero, which is what the frontend's breakdown
// expects -- an absent key would blank a row rather than show a zero.
//
// A caller adds `context_window` and `context_tokens` at its own site. Those two
// are conditional and provider-specific, so they are not part of this projection.
type ContextTokenCounts struct {
	Input      int64
	CacheWrite int64
	CacheRead  int64
	Output     int64
}

// Into writes the counts under their broadcast key names.
func (t ContextTokenCounts) Into(out map[string]any) {
	out[contracts.ContextUsageFieldInputTokens] = t.Input
	out[contracts.ContextUsageFieldCacheCreationInputTokens] = t.CacheWrite
	out[contracts.ContextUsageFieldCacheReadInputTokens] = t.CacheRead
	out[contracts.ContextUsageFieldOutputTokens] = t.Output
}

// ContextUsageMap builds a fresh `context_usage` map that holds the four counts.
// The convenience form for a caller that has no other key to add first.
func ContextUsageMap(t ContextTokenCounts) map[string]any {
	out := map[string]any{}
	t.Into(out)
	return out
}

// TokenCountsFrom reads the four counts back out of a projected map. Paired with
// `into`, so a merge over an authoritative reading refreshes exactly the keys
// the projections write.
func TokenCountsFrom(m map[string]any) ContextTokenCounts {
	pick := func(k string) int64 {
		v, _ := m[k].(int64)
		return v
	}
	return ContextTokenCounts{
		Input:      pick(contracts.ContextUsageFieldInputTokens),
		CacheWrite: pick(contracts.ContextUsageFieldCacheCreationInputTokens),
		CacheRead:  pick(contracts.ContextUsageFieldCacheReadInputTokens),
		Output:     pick(contracts.ContextUsageFieldOutputTokens),
	}
}
