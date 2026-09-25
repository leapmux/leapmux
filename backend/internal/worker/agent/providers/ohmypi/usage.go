package ohmypi

import (
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// sessionStatsMaxWait limits the get_session_stats read that follows a turn.
const sessionStatsMaxWait = 2 * time.Second

// usageState is the session's cost and context usage. Guarded by Process.Mu.
type usageState struct {
	costUsd   float64
	costKnown bool
	context   map[string]any
	// generation counts the assistant messages that updated the usage, so a
	// session-stats read that a newer message overtook does not replace it.
	generation uint64
	// statsRunning is true while a get_session_stats read runs; one at a time.
	statsRunning bool
}

// reset drops the usage of a session the agent replaced.
func (u *usageState) reset() {
	u.costUsd = 0
	u.costKnown = false
	u.context = nil
	u.generation++
}

// usageSnapshot is the usage one row or one broadcast carries. The context map
// is a copy, so a consumer cannot change the agent's state.
type usageSnapshot struct {
	TotalCostUsd float64
	HasTotalCost bool
	ContextUsage map[string]any
}

// snapshotLocked copies the usage. The caller holds a.Mu.
func (a *Agent) snapshotLocked() usageSnapshot {
	snap := usageSnapshot{ContextUsage: maps.Clone(a.usage.context)}
	if a.usage.costKnown && a.usage.costUsd > 0 {
		snap.TotalCostUsd = a.usage.costUsd
		snap.HasTotalCost = true
	}
	return snap
}

func (a *Agent) currentUsageSnapshot() usageSnapshot {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.snapshotLocked()
}

// sessionInfo is the session-info broadcast of a snapshot.
func (s usageSnapshot) sessionInfo() map[string]any {
	info := map[string]any{}
	if s.HasTotalCost {
		info[contracts.SessionInfoKeyTotalCostUsd] = s.TotalCostUsd
	}
	if len(s.ContextUsage) > 0 {
		info[contracts.SessionInfoKeyContextUsage] = s.ContextUsage
	}
	return info
}

// usageMetadata encodes the fields LeapMux calculates for a row: the usage, and
// the turn's duration.
func usageMetadata(snap usageSnapshot, durationMs *int64) []byte {
	fields := snap.sessionInfo()
	if durationMs != nil {
		fields[contracts.MessageMetadataFieldDurationMs] = *durationMs
	}
	if len(fields) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("omp encode usage metadata", "error", err)
		return nil
	}
	return encoded
}

// agentEndContent is an agent_end row with the usage and the duration LeapMux
// measured beside omp's own frame.
func agentEndContent(raw []byte, snap usageSnapshot, durationMs *int64) agent.MessageContent {
	return agent.MessageContent{Original: raw, Metadata: usageMetadata(snap, durationMs)}
}

// assistantUsage is the `usage` of one assistant message.
type assistantUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Cost       struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// assistantMessageContent records one assistant message's usage and returns its
// row, with the session's usage beside omp's own frame.
func (a *Agent) assistantMessageContent(raw []byte) agent.MessageContent {
	content := agent.MessageContent{Original: raw}
	var envelope struct {
		Message struct {
			Usage *assistantUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Usage == nil {
		return content
	}
	usage := *envelope.Message.Usage
	contextWindow := a.currentContextWindow()
	a.Mu.Lock()
	if usage.Cost.Total > 0 {
		a.usage.costUsd += usage.Cost.Total
		a.usage.costKnown = true
	}
	if context := contextUsageFrom(usage, contextWindow); len(context) > 0 {
		a.usage.context = context
	}
	a.usage.generation++
	snap := a.snapshotLocked()
	a.Mu.Unlock()
	content.Metadata = usageMetadata(snap, nil)
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
	return content
}

// contextUsageFrom builds the context usage one request's token counts state, or
// nil for a message that states no tokens (an aborted or failed request).
func contextUsageFrom(usage assistantUsage, contextWindow int64) map[string]any {
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 {
		return nil
	}
	context := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:      usage.Input,
		CacheWrite: usage.CacheWrite,
		CacheRead:  usage.CacheRead,
		Output:     usage.Output,
	})
	if contextWindow > 0 {
		context[contracts.ContextUsageFieldContextWindow] = contextWindow
	}
	return context
}

// currentContextWindow is the context window of the running model, from the
// catalog.
func (a *Agent) currentContextWindow() int64 {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return agent.FindAvailableModel(a.availableModels, a.model).GetContextWindow()
}

// sessionStats takes the fields of a get_session_stats response.
type sessionStats struct {
	SessionFile  string  `json:"sessionFile"`
	SessionID    string  `json:"sessionId"`
	Cost         float64 `json:"cost"`
	ContextUsage *struct {
		Tokens        *int64 `json:"tokens"`
		ContextWindow int64  `json:"contextWindow"`
	} `json:"contextUsage"`
}

// refreshSessionStatsAsync reads omp's session stats on a goroutine, so the read
// loop stays free to deliver the response. One read runs at a time; a request
// while one runs is dropped, because the running read answers it.
func (a *Agent) refreshSessionStatsAsync() {
	a.Mu.Lock()
	if a.usage.statsRunning || a.StoppedLocked() || !a.HasStdin() {
		a.Mu.Unlock()
		return
	}
	a.usage.statsRunning = true
	generation, sessionID := a.usage.generation, a.sessionID
	a.Mu.Unlock()
	go func() {
		defer func() {
			a.Mu.Lock()
			a.usage.statsRunning = false
			a.Mu.Unlock()
		}()
		timeout := a.APITimeout()
		if timeout <= 0 || timeout > sessionStatsMaxWait {
			timeout = sessionStatsMaxWait
		}
		raw, err := a.sendCommand(CommandGetSessionStats, nil, timeout)
		if err != nil {
			if !a.IsStopped() {
				slog.Debug("omp get_session_stats failed", "agent_id", a.AgentID(), "error", err)
			}
			return
		}
		var stats sessionStats
		if len(raw) == 0 || json.Unmarshal(raw, &stats) != nil {
			return
		}
		a.applySessionStats(stats, generation, sessionID)
	}()
}

// applySessionStats folds one session-stats response into the usage and the
// session identity, and broadcasts the usage.
//
// A response that a newer assistant message overtook keeps the cost but not the
// context usage, which the message already updated. A response for a session the
// agent left is dropped whole.
func (a *Agent) applySessionStats(stats sessionStats, generation uint64, sessionID string) {
	a.Mu.Lock()
	if a.StoppedLocked() || a.IsDiscardingOutput() || (stats.SessionID != "" && stats.SessionID != a.sessionID && a.sessionID != sessionID) {
		a.Mu.Unlock()
		return
	}
	// A new session id is a new session: an extension replaced it, and its usage
	// starts over. A new file for the same id is the same session, and keeps its
	// usage; only the resume handle moves.
	sessionReplaced := stats.SessionID != "" && stats.SessionID != a.sessionID
	identityChanged := a.applySessionIdentityLocked(stats.SessionID, stats.SessionFile)
	if sessionReplaced {
		a.usage.reset()
	}
	if stats.Cost > 0 {
		a.usage.costUsd = stats.Cost
		a.usage.costKnown = true
	}
	if a.usage.generation == generation || sessionReplaced {
		if stats.ContextUsage != nil && stats.ContextUsage.Tokens != nil && *stats.ContextUsage.Tokens > 0 {
			// The stats state the total the context holds and no per-request
			// breakdown, so the four counts stay zero.
			context := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
			context[contracts.ContextUsageFieldContextTokens] = *stats.ContextUsage.Tokens
			if stats.ContextUsage.ContextWindow > 0 {
				context[contracts.ContextUsageFieldContextWindow] = stats.ContextUsage.ContextWindow
			}
			a.usage.context = context
		}
	}
	snap := a.snapshotLocked()
	handle := a.sessionHandleLocked()
	a.Mu.Unlock()
	if identityChanged {
		a.sink.UpdateSessionID(handle)
	}
	if info := snap.sessionInfo(); len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}
