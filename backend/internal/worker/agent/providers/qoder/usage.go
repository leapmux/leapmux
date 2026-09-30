package qoder

import (
	"encoding/json"
	"math"

	"github.com/leapmux/leapmux/generated/contracts"
)

// broadcastContextUsage projects Qoder's native window ratio onto session info.
func (a *Agent) broadcastContextUsage(raw []byte) {
	var result struct {
		Usage struct {
			ContextUsageRatio *float64 `json:"context_usage_ratio"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Usage.ContextUsageRatio == nil {
		return
	}
	ratio := *result.Usage.ContextUsageRatio
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return
	}
	a.sink.BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyContextUsage: map[string]any{
			contracts.ContextUsageFieldUsagePercent: math.Min(ratio, 1) * 100,
		},
	})
}
