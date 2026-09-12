package agent

import (
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// piUsageSnapshot holds cumulative cost and context usage.
// Constructors copy the context map so publication cannot change agent state.
// Serialize metadata before sending its context map to a consumer.
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

func (s piUsageSnapshot) sessionInfo() map[string]interface{} {
	info := map[string]interface{}{}
	if s.HasTotalCost {
		info[contracts.SessionInfoKeyTotalCostUsd] = s.TotalCostUsd
	}
	if len(s.ContextUsage) > 0 {
		// Single-use snapshot: hand the ContextUsage map directly to
		// the broadcast payload. The snapshot was built from a cloned
		// map, so the agent's latestContextUsage stays isolated.
		info[contracts.SessionInfoKeyContextUsage] = s.ContextUsage
	}
	return info
}

func piContextUsageFromAssistantUsage(usage piAssistantUsage, contextWindow int64) map[string]any {
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 {
		return nil
	}
	ctx := contextUsageMap(contextTokenCounts{
		Input:      usage.Input,
		CacheWrite: usage.CacheWrite,
		CacheRead:  usage.CacheRead,
		Output:     usage.Output,
	})
	if contextWindow > 0 {
		ctx[contracts.ContextUsageFieldContextWindow] = contextWindow
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
		// The session stats give the total the context holds and no per-request
		// breakdown, so the four counts stay zero and the popover shows a row
		// rather than a blank.
		ctx := contextUsageMap(contextTokenCounts{})
		ctx[contracts.ContextUsageFieldContextTokens] = *stats.ContextUsage.Tokens
		if stats.ContextUsage.ContextWindow > 0 {
			ctx[contracts.ContextUsageFieldContextWindow] = stats.ContextUsage.ContextWindow
		}
		snap.ContextUsage = ctx
	}
	return snap
}

// piAgentEndContent stores measured usage separately from the native turn envelope.
func piAgentEndContent(raw []byte, snap piUsageSnapshot, durationMs *int64) MessageContent {
	content := MessageContent{Original: raw}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Type != contracts.PiEventAgentEnd {
		return content
	}
	content.Metadata = piUsageMetadata(snap, durationMs)
	return content
}

// piUsageMetadata encodes only fields that LeapMux calculates.
func piUsageMetadata(snap piUsageSnapshot, durationMs *int64) []byte {
	fields := snap.sessionInfo()
	if durationMs != nil {
		fields[contracts.MessageMetadataFieldDurationMs] = *durationMs
	}
	if len(fields) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("encode pi usage supplement", "error", err)
		return nil
	}
	return encoded
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
	if cw, _ := a.latestContextUsage[contracts.ContextUsageFieldContextWindow].(int64); cw > 0 {
		return cw
	}
	for _, model := range a.availableModels {
		if model.GetId() == a.model && model.GetContextWindow() > 0 {
			return model.GetContextWindow()
		}
	}
	return 0
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

func (a *PiAgent) applyPiSessionStats(stats piSessionStats, generation uint64, expectedSessionID string) (piUsageSnapshot, bool) {
	a.goal.publishMu.Lock()
	defer a.goal.publishMu.Unlock()
	a.mu.Lock()
	if a.goal.stopping || a.stopped || a.isDiscardingOutput() || (a.sessionID != expectedSessionID && stats.SessionID != a.sessionID) {
		a.mu.Unlock()
		return piUsageSnapshot{}, false
	}
	newSession := stats.SessionID != "" && stats.SessionID != a.sessionID
	if !newSession && a.usageGeneration != generation {
		// A newer assistant message was observed while the RPC was in flight.
		// Avoid overwriting local live usage with a potentially stale stats
		// response from the previous turn.
		a.mu.Unlock()
		return piUsageSnapshot{}, false
	}
	changed := a.applyPiSessionIdentityLocked(stats.SessionID, stats.SessionFile)
	handle := a.sessionHandleLocked()
	snap := piSnapshotFromStats(stats)
	if newSession {
		// The replacement session's snapshot supersedes counters from the old session.
		a.latestContextUsage = nil
		a.usageGeneration++
	}
	a.sessionCostUsd = snap.TotalCostUsd
	a.sessionCostKnown = true
	if len(snap.ContextUsage) > 0 {
		a.latestContextUsage = maps.Clone(snap.ContextUsage)
	}
	a.mu.Unlock()
	if changed {
		a.sink.UpdateSessionID(handle)
		a.schedulePiGoalRefresh(true)
	}
	return snap, true
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
	// Serialize native snapshots so a late reply cannot restore an older session.
	a.sessionStatsMu.Lock()
	defer a.sessionStatsMu.Unlock()
	a.mu.Lock()
	generation, sessionID := a.usageGeneration, a.sessionID
	a.mu.Unlock()
	stats, ok := a.fetchPiSessionStats(timeout)
	if !ok {
		return piUsageSnapshot{}, false
	}
	snap, applied := a.applyPiSessionStats(stats, generation, sessionID)
	if !applied {
		return piUsageSnapshot{}, false
	}
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
	return snap, true
}

// piMessageEndContent observes assistant usage without decoding or replacing the message body.
func (a *PiAgent) piMessageEndContent(raw []byte) MessageContent {
	content := MessageContent{Original: raw}
	var envelope struct {
		Type    string `json:"type"`
		Message *struct {
			Role  string            `json:"role"`
			Usage *piAssistantUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != contracts.PiEventMessageEnd ||
		envelope.Message == nil || envelope.Message.Role != PiRoleAssistant || envelope.Message.Usage == nil {
		return content
	}
	usage := *envelope.Message.Usage
	contextUsage := piContextUsageFromAssistantUsage(usage, a.currentPiContextWindow())
	snap := a.recordPiAssistantUsage(usage, contextUsage)
	content.Metadata = piUsageMetadata(snap, nil)
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
	return content
}

// persistPiAgentEnd writes the agent_end divider.
//
// A run that Pi will retry is NOT a turn end. It persists as a plain agent
// message, so the divider still draws. The turn-end event and the git-status
// refresh then wait for the run that really ends the turn. That event drives
// the completion sound and the off-screen tab's notification dot.
func (a *PiAgent) persistPiAgentEnd(content MessageContent, willRetry bool) {
	var err error
	if willRetry {
		err = a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{})
	} else {
		err = a.sink.PersistTurnEnd(content, SpanInfo{})
	}
	if err != nil {
		slog.Error("pi persist agent_end", "agent_id", a.agentID, "error", err)
	}
}
