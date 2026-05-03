package agent

import (
	"encoding/json"
	"log/slog"
	"maps"
	"time"
)

const piSessionStatsMaxWait = 2 * time.Second

type piAssistantUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
	Cost        struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cacheRead"`
		CacheWrite float64 `json:"cacheWrite"`
		Total      float64 `json:"total"`
	} `json:"cost"`
}

type piSessionStats struct {
	SessionFile string `json:"sessionFile"`
	SessionID   string `json:"sessionId"`
	Tokens      struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
		Total      int64 `json:"total"`
	} `json:"tokens"`
	Cost         float64 `json:"cost"`
	ContextUsage *struct {
		Tokens        *int64   `json:"tokens"`
		ContextWindow int64    `json:"contextWindow"`
		Percent       *float64 `json:"percent"`
	} `json:"contextUsage"`
}

// piUsageSnapshot captures the cost/context-usage state to ship to a
// single broadcast call. The snapshot is single-use: callers consume it
// once via sessionInfo() (broadcast payload) or piAugmentRawWithSnapshot
// (persisted envelope) and drop it. The ContextUsage map is owned by
// the snapshot — sessionInfo() and mutatePiUsageFields hand the map
// directly to the consuming payload, so callers must not mutate it
// after observation. The snapshot constructors clone from the agent's
// latestContextUsage to keep the agent's own state isolated.
type piUsageSnapshot struct {
	TotalCostUsd float64
	HasTotalCost bool
	ContextUsage map[string]any
}

func piSessionStatsTimeout(base time.Duration) time.Duration {
	if base <= 0 || base > piSessionStatsMaxWait {
		return piSessionStatsMaxWait
	}
	return base
}

// mutatePiUsageFields injects the broadcast-shaped usage fields into an
// already-decoded Pi envelope. Both augmentPiMessageEnd and
// piAugmentRawWithSnapshot funnel through this helper so the field set
// stays consistent across the message_end and agent_end persistence
// paths. The injected keys are snake_case to match the rest of the
// platform's session-info wire format (Claude/ACP/Pi all emit
// `total_cost_usd` and `context_usage` on both broadcast and persisted
// surfaces); the frontend reads them via these names exclusively. The
// snapshot's ContextUsage is aliased into obj — both callers immediately
// json.Marshal the result and discard, so a clone would be redundant
// (snapshotLocked already isolates the map from the agent's state).
func mutatePiUsageFields(obj map[string]any, snap piUsageSnapshot) {
	if snap.HasTotalCost {
		obj["total_cost_usd"] = snap.TotalCostUsd
	}
	if len(snap.ContextUsage) > 0 {
		obj["context_usage"] = snap.ContextUsage
	}
}

func (s piUsageSnapshot) sessionInfo() map[string]interface{} {
	info := map[string]interface{}{}
	if s.HasTotalCost {
		info["total_cost_usd"] = s.TotalCostUsd
	}
	if len(s.ContextUsage) > 0 {
		// Single-use snapshot: hand the ContextUsage map directly to
		// the broadcast payload. The snapshot was built from a cloned
		// map, so the agent's latestContextUsage stays isolated.
		info["context_usage"] = s.ContextUsage
	}
	return info
}

func piContextUsageFromAssistantUsage(usage piAssistantUsage, contextWindow int64) map[string]any {
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 {
		return nil
	}
	ctx := map[string]any{
		"input_tokens":                usage.Input,
		"cache_creation_input_tokens": usage.CacheWrite,
		"cache_read_input_tokens":     usage.CacheRead,
		"output_tokens":               usage.Output,
	}
	if contextWindow > 0 {
		ctx["context_window"] = contextWindow
	}
	return ctx
}

func piSnapshotFromStats(stats piSessionStats) piUsageSnapshot {
	snap := piUsageSnapshot{}
	if stats.Cost > 0 {
		snap.TotalCostUsd = stats.Cost
		snap.HasTotalCost = true
	}
	if stats.ContextUsage != nil && stats.ContextUsage.Tokens != nil && *stats.ContextUsage.Tokens > 0 {
		ctx := map[string]any{
			"input_tokens":                int64(0),
			"cache_creation_input_tokens": int64(0),
			"cache_read_input_tokens":     int64(0),
			"output_tokens":               int64(0),
			"context_tokens":              *stats.ContextUsage.Tokens,
		}
		if stats.ContextUsage.ContextWindow > 0 {
			ctx["context_window"] = stats.ContextUsage.ContextWindow
		}
		snap.ContextUsage = ctx
	}
	return snap
}

// piAugmentRawWithSnapshot decodes raw, injects the broadcast-shaped
// usage fields via mutatePiUsageFields, and re-marshals. Used by
// persistPiAgentEnd; augmentPiMessageEnd inlines the same flow because
// it needs to read `message.usage` from the same decoded map.
func piAugmentRawWithSnapshot(raw []byte, snap piUsageSnapshot) []byte {
	if !snap.HasTotalCost && len(snap.ContextUsage) == 0 {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	mutatePiUsageFields(obj, snap)
	augmented, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return augmented
}

func (a *PiAgent) canRequestPiSessionStats() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stdin != nil && !a.stopped
}

// snapshotLocked builds a usage snapshot from the agent's current
// state. Caller must hold a.mu.
func (a *PiAgent) snapshotLocked() piUsageSnapshot {
	snap := piUsageSnapshot{ContextUsage: maps.Clone(a.latestContextUsage)}
	if a.sessionCostKnown && a.sessionCostUsd > 0 {
		snap.TotalCostUsd = a.sessionCostUsd
		snap.HasTotalCost = true
	}
	return snap
}

func (a *PiAgent) currentPiUsageSnapshot() piUsageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *PiAgent) currentPiContextWindow() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cw, _ := a.latestContextUsage["context_window"].(int64); cw > 0 {
		return cw
	}
	for _, model := range a.availableModels {
		if model.GetId() == a.model && model.GetContextWindow() > 0 {
			return model.GetContextWindow()
		}
	}
	return 0
}

func (a *PiAgent) currentPiUsageGeneration() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usageGeneration
}

func (a *PiAgent) recordPiAssistantUsage(usage piAssistantUsage, contextUsage map[string]any) piUsageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	if usage.Cost.Total > 0 {
		a.sessionCostUsd += usage.Cost.Total
		a.sessionCostKnown = true
	}
	if len(contextUsage) > 0 {
		a.latestContextUsage = maps.Clone(contextUsage)
	}
	a.usageGeneration++

	return a.snapshotLocked()
}

func (a *PiAgent) applyPiSessionStats(snap piUsageSnapshot, generation uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.usageGeneration != generation {
		// A newer assistant message was observed while the RPC was in flight.
		// Avoid overwriting local live usage with a potentially stale stats
		// response from the previous turn.
		return false
	}
	a.sessionCostUsd = snap.TotalCostUsd
	a.sessionCostKnown = true
	if len(snap.ContextUsage) > 0 {
		a.latestContextUsage = maps.Clone(snap.ContextUsage)
	}
	return true
}

func (a *PiAgent) fetchPiSessionStats(timeout time.Duration) (piSessionStats, bool) {
	var stats piSessionStats
	raw, err := a.sendPiCommand(PiCommandGetSessionStats, nil, timeout)
	if err != nil {
		slog.Warn("pi get_session_stats failed", "agent_id", a.agentID, "error", err)
		return stats, false
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stats, false
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		slog.Warn("pi get_session_stats unmarshal failed", "agent_id", a.agentID, "error", err)
		return stats, false
	}
	return stats, true
}

func (a *PiAgent) refreshPiSessionStats(timeout time.Duration) (piUsageSnapshot, bool) {
	generation := a.currentPiUsageGeneration()
	stats, ok := a.fetchPiSessionStats(timeout)
	if !ok {
		return piUsageSnapshot{}, false
	}
	snap := piSnapshotFromStats(stats)
	if !a.applyPiSessionStats(snap, generation) {
		return piUsageSnapshot{}, false
	}
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
	return snap, true
}

// augmentPiMessageEnd parses an assistant message_end envelope once,
// extracts the typed `message.usage` from the decoded map, records the
// usage delta on the agent, and re-marshals the same map with the
// broadcast-shaped fields injected.
func (a *PiAgent) augmentPiMessageEnd(raw []byte) []byte {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	if t, _ := obj["type"].(string); t != PiEventMessageEnd {
		return raw
	}
	message, ok := obj["message"].(map[string]any)
	if !ok {
		return raw
	}
	if role, _ := message["role"].(string); role != "assistant" {
		return raw
	}
	usageMap, ok := message["usage"].(map[string]any)
	if !ok {
		return raw
	}
	// Re-encode just the usage submap and decode into the typed struct.
	// Cheaper than a second full-envelope Unmarshal and avoids hand-rolled
	// json.Number/float coercion helpers.
	usageBytes, err := json.Marshal(usageMap)
	if err != nil {
		return raw
	}
	var usage piAssistantUsage
	if err := json.Unmarshal(usageBytes, &usage); err != nil {
		return raw
	}

	contextUsage := piContextUsageFromAssistantUsage(usage, a.currentPiContextWindow())
	snap := a.recordPiAssistantUsage(usage, contextUsage)
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
	if !snap.HasTotalCost && len(snap.ContextUsage) == 0 {
		return raw
	}
	mutatePiUsageFields(obj, snap)
	augmented, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return augmented
}

func (a *PiAgent) persistPiAgentEnd(raw []byte, snap piUsageSnapshot) {
	augmented := piAugmentRawWithSnapshot(raw, snap)
	if err := a.sink.PersistTurnEnd(augmented, SpanInfo{}); err != nil {
		slog.Error("pi persist agent_end", "agent_id", a.agentID, "error", err)
	}
}
