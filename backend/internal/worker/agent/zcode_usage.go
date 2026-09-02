package agent

import (
	"encoding/json"
	"log/slog"
	"maps"

	"github.com/leapmux/leapmux/generated/contracts"
)

// ZCode reports usage from two places, and they answer different questions.
//
//   - `session.updated` carries a PER-REQUEST usage object while the turn runs, and
//     `turn.completed` carries the turn's total. Both are token counts for the work
//     just done.
//   - `runtime.contextUsage` carries what the CONTEXT holds now, plus the accrued
//     cost. It is the authoritative readout, and it is the only one that states a
//     cost at all.
//
// So the context readout prefers contextUsage and falls back to the token counts,
// which is what makes the number correct after a compaction: the tokens of the last
// request say nothing about how full the context is once history was summarized.

// zcodeUsage is the token-count object session.updated and turn.completed share.
type zcodeUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	TotalTokens      int64 `json:"totalTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

func (u zcodeUsage) empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheWriteTokens == 0
}

// zcodeRuntimeState is the `runtime` object of a state snapshot or patch.
type zcodeRuntimeState struct {
	EventSeq      int64              `json:"eventSeq"`
	ActiveTurnID  string             `json:"activeTurnId"`
	ContextUsage  *zcodeContextUsage `json:"contextUsage"`
	APIRetry      *zcodeAPIRetry     `json:"apiRetry"`
	StateRevision int64              `json:"stateRevision"`
}

// zcodeContextUsage is the app-server's authoritative context readout.
type zcodeContextUsage struct {
	Used  int64      `json:"used"`
	Size  int64      `json:"size"`
	Cost  *zcodeCost `json:"cost"`
	Cache *struct {
		InputTokens      int64 `json:"inputTokens"`
		CacheReadTokens  int64 `json:"cacheReadTokens"`
		CacheWriteTokens int64 `json:"cacheWriteTokens"`
	} `json:"cache"`
}

// zcodeCost is the accrued session cost the app-server reports. Named rather than
// anonymous so the field and costOrNil's return type cannot drift: an anonymous struct
// must be re-declared verbatim, field order and tags included, at both.
type zcodeCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// zcodeAPIRetry is the in-flight provider retry the app-server reports while it
// backs off. Surfaced as session info so the user sees WHY a turn is idle.
type zcodeAPIRetry struct {
	Attempt      int    `json:"attempt"`
	MaxRetries   int    `json:"maxRetries"`
	RetryDelayMs int64  `json:"retryDelayMs"`
	ErrorStatus  *int   `json:"errorStatus"`
	Error        string `json:"error"`
}

// zcodeUsageCurrencyUSD is the only currency LeapMux's `total_cost_usd` field can
// carry. A cost in any other currency is DROPPED rather than reported as dollars,
// because a wrong number is worse than no number.
const zcodeUsageCurrencyUSD = "USD"

// zcodeContextUsageMap projects token counts into the broadcast-shaped context-usage
// map every provider emits. Returns nil for an empty usage object, so a caller does
// not overwrite a real reading with zeros.
func zcodeContextUsageMap(usage zcodeUsage, contextWindow int64) map[string]any {
	if usage.empty() {
		return nil
	}
	out := map[string]any{}
	zcodeTokenCounts{
		Input:      usage.InputTokens,
		CacheWrite: usage.CacheWriteTokens,
		CacheRead:  usage.CacheReadTokens,
		Output:     usage.OutputTokens,
	}.into(out)
	if contextWindow > 0 {
		out[contracts.ContextUsageFieldContextWindow] = contextWindow
	}
	return out
}

// zcodeContextUsageFromRuntime projects the authoritative context readout.
//
// `context_tokens` is what the context HOLDS, which the frontend prefers over the
// summed token counts for the fill gauge. The per-request counts still ride along
// (from `cache`, when present) so the popover's breakdown is not blank.
func zcodeContextUsageFromRuntime(usage *zcodeContextUsage) map[string]any {
	if usage == nil || usage.Used <= 0 {
		return nil
	}
	// The per-request counts default to zero, so the popover's breakdown is present even
	// when the readout carries no `cache` block. `output_tokens` has no source here.
	var counts zcodeTokenCounts
	if c := usage.Cache; c != nil {
		counts.Input = c.InputTokens
		counts.CacheRead = c.CacheReadTokens
		counts.CacheWrite = c.CacheWriteTokens
	}
	out := map[string]any{contracts.ContextUsageFieldContextTokens: usage.Used}
	counts.into(out)
	if usage.Size > 0 {
		out[contracts.ContextUsageFieldContextWindow] = usage.Size
	}
	return out
}

// zcodeTokenCounts is the four per-request token counts, and the ONE place their
// broadcast key names are written.
//
// Three sites need the same four keys: the two projections and the merge that refreshes
// them over an authoritative reading. Spelling them out at each one meant a fifth count
// could be added to the projections and silently dropped by the merge.
type zcodeTokenCounts struct {
	Input      int64
	CacheWrite int64
	CacheRead  int64
	Output     int64
}

// into writes the counts under their broadcast key names.
func (t zcodeTokenCounts) into(out map[string]any) {
	out[contracts.ContextUsageFieldInputTokens] = t.Input
	out[contracts.ContextUsageFieldCacheCreationInputTokens] = t.CacheWrite
	out[contracts.ContextUsageFieldCacheReadInputTokens] = t.CacheRead
	out[contracts.ContextUsageFieldOutputTokens] = t.Output
}

// tokenCountsFrom reads the four counts back out of a projected map. Paired with
// `into`, so the merge below refreshes exactly the keys the projections write.
func tokenCountsFrom(m map[string]any) zcodeTokenCounts {
	pick := func(k string) int64 {
		v, _ := m[k].(int64)
		return v
	}
	return zcodeTokenCounts{
		Input:      pick(contracts.ContextUsageFieldInputTokens),
		CacheWrite: pick(contracts.ContextUsageFieldCacheCreationInputTokens),
		CacheRead:  pick(contracts.ContextUsageFieldCacheReadInputTokens),
		Output:     pick(contracts.ContextUsageFieldOutputTokens),
	}
}

// applyZCodeRuntimeState records the runtime readout and broadcasts it.
func (a *zcodeAgent) applyZCodeRuntimeState(runtime *zcodeRuntimeState) {
	if runtime == nil {
		return
	}
	info := map[string]any{}

	if usage := zcodeContextUsageFromRuntime(runtime.ContextUsage); len(usage) > 0 {
		a.mu.Lock()
		a.latestContextUsage = usage
		if runtime.ContextUsage.Size > 0 {
			a.contextWindow = runtime.ContextUsage.Size
		}
		a.mu.Unlock()
		info[contracts.SessionInfoKeyContextUsage] = maps.Clone(usage)
	}
	// A cost in another currency is omitted, not converted. `total_cost_usd` states
	// its unit, and filling it from a non-dollar amount would be a wrong number.
	if c := runtime.ContextUsage.costOrNil(); c != nil && c.Currency == zcodeUsageCurrencyUSD {
		a.mu.Lock()
		a.sessionCostUsd = c.Amount
		a.sessionCostKnown = true
		a.mu.Unlock()
		info[contracts.SessionInfoKeyTotalCostUsd] = c.Amount
	}
	// No browser code reads this key, so it stays out of
	// contracts/session-info.json and a backing-off ZCode turn currently reads as
	// idle. Surfacing it means an agent-level retry indicator (it is
	// session-scoped, so it does not fit the span-keyed running_tool key):
	// https://github.com/leapmux/leapmux/issues/434
	if r := runtime.APIRetry; r != nil {
		info["zcode_api_retry"] = map[string]any{
			"attempt":        r.Attempt,
			"max_retries":    r.MaxRetries,
			"retry_delay_ms": r.RetryDelayMs,
			"error":          r.Error,
		}
	} else {
		// An absent apiRetry means the retry finished. Reporting nil clears the
		// indicator, which a missing key would leave stuck on the last attempt.
		info["zcode_api_retry"] = nil
	}
	if runtime.EventSeq > 0 {
		a.mu.Lock()
		if runtime.EventSeq > a.lastSeq {
			a.lastSeq = runtime.EventSeq
		}
		a.mu.Unlock()
	}
	if len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}

// costOrNil reads the cost off a possibly-absent context usage, so the caller needs
// no nil check of its own.
func (u *zcodeContextUsage) costOrNil() *zcodeCost {
	if u == nil {
		return nil
	}
	return u.Cost
}

// recordZCodeUsage folds a token-count usage object into the agent's readout and
// broadcasts it.
//
// It NEVER replaces a context reading that came from `runtime.contextUsage`: that
// one states what the context holds, and these counts state what one request cost.
// Overwriting the first with the second is what makes the fill gauge jump backwards
// after a compaction.
func (a *zcodeAgent) recordZCodeUsage(usage zcodeUsage) {
	contextUsage := zcodeContextUsageMap(usage, a.currentZCodeContextWindow())
	if len(contextUsage) == 0 {
		return
	}
	a.mu.Lock()
	if _, authoritative := a.latestContextUsage[contracts.ContextUsageFieldContextTokens]; authoritative {
		// Keep the authoritative window and total; refresh only the per-request
		// counts, which are what this event actually knows.
		tokenCountsFrom(contextUsage).into(a.latestContextUsage)
	} else {
		a.latestContextUsage = contextUsage
	}
	broadcast := maps.Clone(a.latestContextUsage)
	a.mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: broadcast})
}

// refreshZCodeUsageFromSession re-reads the session's own state after a turn ends,
// so the persisted readout is the app-server's and not the last event's.
//
// Best-effort and asynchronous by contract: the read loop must stay free to deliver
// the response, so this may not run on the read-loop goroutine.
func (a *zcodeAgent) refreshZCodeUsageFromSession() {
	a.mu.Lock()
	sessionID, stopped := a.sessionID, a.stopped
	a.mu.Unlock()
	if sessionID == "" || stopped {
		return
	}
	raw, err := a.sendZCodeRequest(ZCodeMethodSessionRead, map[string]any{"sessionId": sessionID}, a.APITimeout())
	if err != nil {
		// A build without session/read is a missing capability, not a failure: the
		// event-driven readout stands.
		if !zcodeIsMethodNotFound(err) {
			slog.Debug("zcode session/read failed", "agent_id", a.agentID, "error", err)
		}
		return
	}
	var state struct {
		Runtime  *zcodeRuntimeState     `json:"runtime"`
		Settings *zcodeSettingsSnapshot `json:"settings"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("zcode session/read unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	a.applyZCodeRuntimeState(state.Runtime)
	if state.Settings != nil {
		a.mu.Lock()
		a.applySettingsSnapshotLocked(state.Settings)
		a.mu.Unlock()
	}
}

// zcodeUsageSnapshot is the usage state one persisted envelope carries.
type zcodeUsageSnapshot struct {
	ContextUsage map[string]any
	CostUSD      float64
	HasCost      bool
}

// usageSnapshot copies the agent's current usage readout.
func (a *zcodeAgent) usageSnapshot() zcodeUsageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return zcodeUsageSnapshot{
		ContextUsage: maps.Clone(a.latestContextUsage),
		CostUSD:      a.sessionCostUsd,
		HasCost:      a.sessionCostKnown,
	}
}

// zcodeAugmentWithUsage injects the broadcast-shaped usage fields into an event
// envelope before it is persisted, so a reconnecting frontend rehydrates the cost
// and context readout from the stored turn end rather than waiting for a live
// broadcast that already happened.
func zcodeAugmentWithUsage(raw []byte, snap zcodeUsageSnapshot) []byte {
	if len(snap.ContextUsage) == 0 && !snap.HasCost {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	if len(snap.ContextUsage) > 0 {
		obj[contracts.SessionInfoKeyContextUsage] = snap.ContextUsage
	}
	if snap.HasCost {
		obj[contracts.SessionInfoKeyTotalCostUsd] = snap.CostUSD
	}
	augmented, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return augmented
}
