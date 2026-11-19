package opencode

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const openCodeMaxExactTokenCount = 1<<53 - 1

// Native counters must retain their exact value in Go and the browser.
func openCodeUsageCounts(raw json.RawMessage) (providerkit.ContextTokenCounts, error) {
	var tokens struct {
		Input     *float64 `json:"input"`
		Output    *float64 `json:"output"`
		Reasoning *float64 `json:"reasoning"`
		Cache     struct {
			Read  *float64 `json:"read"`
			Write *float64 `json:"write"`
		} `json:"cache"`
	}
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return providerkit.ContextTokenCounts{}, err
	}
	for _, value := range []*float64{tokens.Input, tokens.Output, tokens.Reasoning, tokens.Cache.Read, tokens.Cache.Write} {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > openCodeMaxExactTokenCount || math.Trunc(*value) != *value {
			return providerkit.ContextTokenCounts{}, errors.New("the native token counts must be nonnegative exact integers")
		}
	}
	if *tokens.Output+*tokens.Reasoning > openCodeMaxExactTokenCount {
		return providerkit.ContextTokenCounts{}, errors.New("the native output and reasoning total exceeds the exact integer range")
	}
	return providerkit.ContextTokenCounts{
		Input: int64(*tokens.Input), Output: int64(*tokens.Output + *tokens.Reasoning),
		CacheRead: int64(*tokens.Cache.Read), CacheWrite: int64(*tokens.Cache.Write),
	}, nil
}

func (run *openCodeRun) publishNativeStepUsage(raw json.RawMessage) {
	var data struct {
		MessageID string          `json:"assistantMessageID"`
		Tokens    json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	var selected *openCodeModelRef
	if model, ok := run.stepModels[data.MessageID]; ok {
		selected = &model
	}
	delete(run.stepModels, data.MessageID)
	run.publishNativeUsage(data.Tokens, selected)
}

func (run *openCodeRun) publishNativeUsage(raw json.RawMessage, model *openCodeModelRef) {
	if !providerkit.RawPresent(raw) || run.ctx.Err() != nil {
		return
	}
	counts, err := openCodeUsageCounts(raw)
	if err != nil {
		slog.Warn("OpenCode reported invalid token counts", "session_id", run.id, "error", err)
		return
	}
	usage := providerkit.ContextUsageMap(counts)
	run.mu.Lock()
	catalog := run.catalog
	if model == nil {
		if effective, err := catalog.effectiveModel(run.selectedModel, run.selectedAgentID); err == nil {
			model = &effective
		}
	}
	run.mu.Unlock()
	if model != nil {
		if info := catalog.findModel(openCodeModelKey(*model)); info != nil && info.Limit.Context > 0 {
			usage[contracts.ContextUsageFieldContextWindow] = info.Limit.Context
		}
	}
	run.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
}

func (run *openCodeRun) publishNativeTotalCost(raw json.RawMessage) {
	var data struct {
		Cost *float64 `json:"cost"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Cost == nil || math.IsNaN(*data.Cost) || math.IsInf(*data.Cost, 0) || *data.Cost < 0 || run.ctx.Err() != nil {
		return
	}
	run.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyTotalCostUsd: *data.Cost})
}
