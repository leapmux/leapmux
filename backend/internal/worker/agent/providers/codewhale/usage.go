package codewhale

import (
	"encoding/json"
	"log/slog"
	"maps"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codewhale reports usage from two places, and they answer different questions.
//
//   - `turn.usage` states what ONE model request cost, once for each request of
//     a turn. The last one's prompt is the best reading of how full the context
//     was, and it is the only reading 0.9.13 gives.
//   - GET /v1/threads/{id}/context, from 0.10.0, states the context window and
//     the runtime's own estimate of what the context holds. It is authoritative,
//     so a reading from it is never overwritten by a request's counts.

// codewhaleUsage is the agent's latest context reading. The caller holds Mu.
type codewhaleUsage struct {
	// contextUsage is the broadcast-shaped map, kept so the turn end can carry it
	// for a frontend that reconnects.
	contextUsage map[string]any
}

// requestUsage is the `usage` object of turn.usage. The cache fields are
// optional: a route that reports no cache states neither.
type requestUsage struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheHitTokens   *int64 `json:"prompt_cache_hit_tokens"`
	CacheMissTokens  *int64 `json:"prompt_cache_miss_tokens"`
	CacheWriteTokens *int64 `json:"prompt_cache_write_tokens"`
}

// counts projects one request's usage onto the four broadcast counts.
//
// The runtime's `input_tokens` is the WHOLE prompt on a route that reports a
// cache split (DeepSeek states hit + miss), while the broadcast's input count is
// the part the cache did not serve. So the uncached input is the miss count when
// the route states one, and the prompt minus the cached parts otherwise.
func (u requestUsage) counts() providerkit.ContextTokenCounts {
	var hit, write int64
	if u.CacheHitTokens != nil {
		hit = *u.CacheHitTokens
	}
	if u.CacheWriteTokens != nil {
		write = *u.CacheWriteTokens
	}
	input := u.InputTokens - hit - write
	if u.CacheMissTokens != nil {
		input = *u.CacheMissTokens
	}
	if input < 0 {
		input = 0
	}
	return providerkit.ContextTokenCounts{Input: input, CacheWrite: write, CacheRead: hit, Output: u.OutputTokens}
}

// handleTurnUsage records one request's usage and broadcasts it.
func (a *Agent) handleTurnUsage(env codewhaleEnvelope) {
	var payload struct {
		Usage *requestUsage `json:"usage"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Usage == nil {
		return
	}
	counts := payload.Usage.counts()
	if counts == (providerkit.ContextTokenCounts{}) {
		return
	}
	a.Mu.Lock()
	// The event knows the four per-request counts, and every request states all
	// four, so they replace the counts of the request before. The window and the
	// context total come from the context report alone, and they stay as it
	// stated them: a report can state the window with no total yet.
	if a.usage.contextUsage == nil {
		a.usage.contextUsage = make(map[string]any)
	}
	counts.Into(a.usage.contextUsage)
	broadcast := maps.Clone(a.usage.contextUsage)
	a.Mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: broadcast})
}

// threadContextReport is the part of GET /v1/threads/{id}/context that the
// provider reads. Both fields are null for a route that has no known window
// limit.
type threadContextReport struct {
	WindowTokens *int64 `json:"window_tokens"`
	InputTokens  *int64 `json:"input_tokens"`
}

// refreshContextUsage reads the runtime's context report, when the runtime has
// the route. It runs on its own goroutine: the dispatch must not wait for a
// request.
func (a *Agent) refreshContextUsage() {
	if !a.runtime.hasContextRoute {
		return
	}
	threadID := a.currentThreadID()
	if threadID == "" {
		return
	}
	go func() {
		raw, err := a.readThreadContext(threadID)
		if err != nil {
			slog.Debug("codewhale read the context report", "agent_id", a.AgentID(), "error", err)
			return
		}
		a.applyContextReport(raw)
	}()
}

// applyContextReport records the runtime's context report and broadcasts it.
func (a *Agent) applyContextReport(raw json.RawMessage) {
	var report threadContextReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return
	}
	if report.InputTokens == nil && report.WindowTokens == nil {
		return
	}
	a.Mu.Lock()
	usage := a.usage.contextUsage
	if usage == nil {
		usage = providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
	}
	if report.InputTokens != nil && *report.InputTokens > 0 {
		usage[contracts.ContextUsageFieldContextTokens] = *report.InputTokens
	}
	if report.WindowTokens != nil && *report.WindowTokens > 0 {
		usage[contracts.ContextUsageFieldContextWindow] = *report.WindowTokens
	}
	a.usage.contextUsage = usage
	broadcast := maps.Clone(usage)
	a.Mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: broadcast})
}

// turnEndContent is the turn-end row: the runtime's own event, with the turn's
// tool count and the latest context reading as worker metadata, so a frontend
// that reconnects reads both back.
func (a *Agent) turnEndContent(raw []byte) agent.MessageContent {
	content := agent.MessageContent{Original: raw}
	a.Mu.Lock()
	usage := maps.Clone(a.usage.contextUsage)
	count := a.TurnToolUses
	a.Mu.Unlock()
	if len(usage) > 0 {
		metadata, err := json.Marshal(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
		if err != nil {
			slog.Warn("codewhale encode the usage of a turn end", "error", err)
		} else {
			content.Metadata = metadata
		}
	}
	return agent.WithToolUseCount(content, count)
}
