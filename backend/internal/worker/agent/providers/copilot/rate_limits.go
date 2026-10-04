package copilot

import (
	"encoding/json"
	"math"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type copilotQuotaSnapshot struct {
	IsUnlimitedEntitlement           bool     `json:"isUnlimitedEntitlement"`
	RemainingPercentage              *float64 `json:"remainingPercentage"`
	EntitlementRequests              *float64 `json:"entitlementRequests"`
	UsedRequests                     *float64 `json:"usedRequests"`
	HasQuota                         *bool    `json:"hasQuota"`
	UsageAllowedWithExhaustedQuota   bool     `json:"usageAllowedWithExhaustedQuota"`
	OverageAllowedWithExhaustedQuota *bool    `json:"overageAllowedWithExhaustedQuota"`
	Overage                          *float64 `json:"overage"`
	ResetDate                        string   `json:"resetDate"`
}

func validCopilotQuotaCount(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0)
}

// copilotQuotaInfo converts one actual native resource without inventing absent counters.
func copilotQuotaInfo(resource string, raw json.RawMessage) map[string]interface{} {
	if resource == "" {
		return nil
	}
	var snapshot copilotQuotaSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || !validCopilotQuotaCount(snapshot.Overage) {
		return nil
	}
	info := map[string]interface{}{
		contracts.RateLimitFieldRateLimitType: resource,
		contracts.RateLimitFieldStatus:        "allowed",
	}
	if snapshot.IsUnlimitedEntitlement {
		return info
	}
	remaining := snapshot.RemainingPercentage
	if remaining == nil || !validCopilotQuotaCount(remaining) || *remaining > 100 ||
		!validCopilotQuotaCount(snapshot.EntitlementRequests) || !validCopilotQuotaCount(snapshot.UsedRequests) {
		return nil
	}
	info[contracts.RateLimitFieldUtilization] = (100 - *remaining) / 100
	blocked := *remaining == 0 || (snapshot.HasQuota != nil && !*snapshot.HasQuota)
	switch {
	case blocked && !snapshot.UsageAllowedWithExhaustedQuota:
		info[contracts.RateLimitFieldStatus] = "exceeded"
	case blocked || *remaining <= 20:
		info[contracts.RateLimitFieldStatus] = "allowed_warning"
	}
	if snapshot.Overage != nil {
		info[contracts.RateLimitFieldIsUsingOverage] = *snapshot.Overage > 0
	}
	if snapshot.OverageAllowedWithExhaustedQuota != nil && *snapshot.OverageAllowedWithExhaustedQuota {
		info[contracts.RateLimitFieldOverageStatus] = "allowed"
	}
	if reset, err := time.Parse(time.RFC3339Nano, snapshot.ResetDate); err == nil {
		info[contracts.RateLimitFieldResetsAt] = reset.Unix()
		if snapshot.Overage != nil && *snapshot.Overage > 0 {
			info[contracts.RateLimitFieldOverageResetsAt] = reset.Unix()
		}
	}
	return info
}

// reportNativeQuota publishes the native account snapshot to the transcript that owns its event.
func (a *Agent) reportNativeQuota(sink agent.ProviderServices, data json.RawMessage) {
	var event struct {
		Snapshots map[string]json.RawMessage `json:"quotaSnapshots"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	values := map[string]interface{}{}
	for resource, raw := range event.Snapshots {
		if info := copilotQuotaInfo(resource, raw); info != nil {
			values[resource] = info
		}
	}
	if len(values) == 0 {
		return
	}
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRateLimits: map[string]interface{}{
			contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeMerge,
			contracts.RateLimitUpdateFieldValues: values,
		},
	})
}
