package muse

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// nativeCounter distinguishes an absent field from an actual zero.
type nativeCounter struct {
	value   int64
	present bool
}

func (counter *nativeCounter) UnmarshalJSON(raw []byte) error {
	var value int64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value < 0 {
		return fmt.Errorf("the Muse usage counter must be a nonnegative integer")
	}
	counter.value = value
	counter.present = true
	return nil
}

type nativeContextUsage struct {
	SessionID    string        `json:"sessionId"`
	UsedTokens   nativeCounter `json:"usedTokens"`
	WindowTokens nativeCounter `json:"windowTokens"`
}

type nativeTokenCounts struct {
	InputTokens      nativeCounter `json:"inputTokens"`
	OutputTokens     nativeCounter `json:"outputTokens"`
	CachedTokens     nativeCounter `json:"cachedTokens"`
	ReasoningTokens  nativeCounter `json:"reasoningTokens"`
	CacheReadTokens  nativeCounter `json:"cacheReadTokens"`
	CacheWriteTokens nativeCounter `json:"cacheWriteTokens"`
}

type nativeTokenUsage struct {
	SessionID    string            `json:"sessionId"`
	TurnID       string            `json:"turnId"`
	PromptTokens nativeCounter     `json:"promptTokens"`
	TotalTokens  nativeCounter     `json:"totalTokens"`
	Usage        nativeTokenCounts `json:"usage"`
	Cumulative   struct {
		PromptTokens     nativeCounter `json:"promptTokens"`
		OutputTokens     nativeCounter `json:"outputTokens"`
		TotalTokens      nativeCounter `json:"totalTokens"`
		CacheReadTokens  nativeCounter `json:"cacheReadTokens"`
		CacheWriteTokens nativeCounter `json:"cacheWriteTokens"`
	} `json:"cumulative"`
}

// The caller holds stateMu. Child sessions keep their own current state.
func (a *Agent) currentSessionState(id string, state *sessionState) bool {
	return state != nil && !state.retired && a.sessions[id] == state && (id == a.sessionID || state.childID != "")
}

func (state *sessionState) normalizedContextUsage() map[string]any {
	reading := make(map[string]any)
	if context := state.contextUsage; context != nil {
		reading[contracts.ContextUsageFieldContextTokens] = context.UsedTokens.value
		if context.WindowTokens.present {
			reading[contracts.ContextUsageFieldContextWindow] = context.WindowTokens.value
		}
	}
	if tokens := state.tokenUsage; tokens != nil {
		reading[contracts.ContextUsageFieldInputTokens] = tokens.Usage.InputTokens.value
		reading[contracts.ContextUsageFieldOutputTokens] = tokens.Usage.OutputTokens.value
		if tokens.Usage.CacheReadTokens.present {
			reading[contracts.ContextUsageFieldCacheReadInputTokens] = tokens.Usage.CacheReadTokens.value
		}
		if tokens.Usage.CacheWriteTokens.present {
			reading[contracts.ContextUsageFieldCacheCreationInputTokens] = tokens.Usage.CacheWriteTokens.value
		}
	}
	return reading
}

func (a *Agent) handleContextUsage(raw []byte, state *sessionState) {
	var event nativeContextUsage
	if json.Unmarshal(raw, &event) != nil || !event.UsedTokens.present {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(event.SessionID, state) {
		a.stateMu.Unlock()
		return
	}
	state.contextUsage = &event
	reading := state.normalizedContextUsage()
	a.stateMu.Unlock()
	state.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: reading})
}

func (a *Agent) handleTokenUsage(raw []byte, state *sessionState) {
	var event nativeTokenUsage
	if json.Unmarshal(raw, &event) != nil || event.TurnID == "" || !event.PromptTokens.present || !event.TotalTokens.present ||
		!event.Usage.InputTokens.present || !event.Usage.OutputTokens.present || !event.Usage.CachedTokens.present || !event.Usage.ReasoningTokens.present ||
		!event.Cumulative.PromptTokens.present || !event.Cumulative.OutputTokens.present || !event.Cumulative.TotalTokens.present {
		return
	}
	a.stateMu.Lock()
	if !a.currentSessionState(event.SessionID, state) {
		a.stateMu.Unlock()
		return
	}
	state.tokenUsage = &event
	reading := state.normalizedContextUsage()
	a.stateMu.Unlock()
	state.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: reading})
}

type nativeQuotaWindow struct {
	UsedPercent        nativeCounter `json:"usedPercent"`
	ResetsAtMs         nativeCounter `json:"resetsAtMs"`
	WindowDurationMins nativeCounter `json:"windowDurationMins"`
}

func (window *nativeQuotaWindow) valid() bool {
	return window != nil && window.UsedPercent.present && window.ResetsAtMs.present
}

func (window *nativeQuotaWindow) normalized(kind string) map[string]any {
	return map[string]any{
		contracts.RateLimitFieldRateLimitType: kind,
		contracts.RateLimitFieldUtilization:   float64(window.UsedPercent.value) / 100,
		contracts.RateLimitFieldResetsAt:      float64(window.ResetsAtMs.value) / 1000,
	}
}

func (a *Agent) handleUsage(raw []byte, sink agent.ProviderServices) {
	var event struct {
		ObservedAtMs nativeCounter      `json:"observedAtMs"`
		Weekly       *nativeQuotaWindow `json:"weekly"`
		Window       *nativeQuotaWindow `json:"window"`
	}
	if json.Unmarshal(raw, &event) != nil || !event.ObservedAtMs.present || !event.Weekly.valid() || !event.Window.valid() ||
		!event.Window.WindowDurationMins.present || event.Window.WindowDurationMins.value == 0 {
		return
	}
	sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode: contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: map[string]any{
			"five_hour": event.Window.normalized("five_hour"),
			"seven_day": event.Weekly.normalized("seven_day"),
		},
	}})
}
