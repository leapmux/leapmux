package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// handleRateLimitsUpdated publishes account state to live session surfaces and
// schedules automatic continuation. Account snapshots never become transcript rows.
func (a *Agent) handleRateLimitsUpdated(content []byte, params json.RawMessage) {
	var notif struct {
		RateLimits struct {
			Primary              *codexRateLimitTier `json:"primary"`
			Secondary            *codexRateLimitTier `json:"secondary"`
			RateLimitReachedType *string             `json:"rateLimitReachedType"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex rate limit unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	reachedType := ""
	if notif.RateLimits.RateLimitReachedType != nil {
		reachedType = *notif.RateLimits.RateLimitReachedType
	}
	summary := summarizeCodexRateLimits([]*codexRateLimitTier{notif.RateLimits.Primary, notif.RateLimits.Secondary}, reachedType)
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRateLimits: map[string]interface{}{
			contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
			contracts.RateLimitUpdateFieldValues: summary.rateLimits,
		},
	})

	if resumeReset := codexRateLimitResumeReset(reachedType, summary); resumeReset != nil {
		a.sink.ScheduleAutoContinue(agent.AutoContinueSchedule{
			Reason:        agent.AutoContinueReasonRateLimit,
			DueAt:         *resumeReset,
			SourcePayload: append([]byte(nil), content...),
		})
	} else {
		a.sink.CancelAutoContinue(agent.AutoContinueReasonRateLimit)
	}
}

type codexRateLimitSummary struct {
	// The session-info map includes a stable account-block member. An empty
	// member clears a prior billing or workspace block in the frontend store.
	rateLimits map[string]interface{}
	// These three resets keep scheduling policy separate from presentation.
	latestExceededReset *time.Time
	latestReset         *time.Time
	bindingReset        *time.Time
}

func summarizeCodexRateLimits(tiers []*codexRateLimitTier, reachedType string) codexRateLimitSummary {
	summary := codexRateLimitSummary{rateLimits: map[string]interface{}{}}
	anyExceeded := false
	var bindingTierKey string
	bindingPct := -1.0
	for _, tier := range tiers {
		if tier == nil {
			continue
		}
		rateLimitType := codexWindowToType(tier.WindowDurationMins)
		utilization := tier.UsedPercent / 100
		status := providerkit.RateLimitStatusForUtilization(utilization)
		if status == contracts.RateLimitStatusExceeded {
			anyExceeded = true
		}
		info := map[string]interface{}{
			contracts.RateLimitFieldRateLimitType: rateLimitType,
			contracts.RateLimitFieldUtilization:   utilization,
			contracts.RateLimitFieldStatus:        status,
		}
		var tierReset *time.Time
		if tier.ResetsAt != nil {
			resetAt := time.Unix(*tier.ResetsAt, 0).UTC()
			tierReset = &resetAt
			info[contracts.RateLimitFieldResetsAt] = *tier.ResetsAt
			if summary.latestReset == nil || resetAt.After(*summary.latestReset) {
				summary.latestReset = &resetAt
			}
			if status == contracts.RateLimitStatusExceeded &&
				(summary.latestExceededReset == nil || resetAt.After(*summary.latestExceededReset)) {
				summary.latestExceededReset = &resetAt
			}
		}
		if tier.UsedPercent > bindingPct {
			bindingPct = tier.UsedPercent
			bindingTierKey = rateLimitType
			summary.bindingReset = tierReset
		}
		summary.rateLimits[rateLimitType] = info
	}

	if reachedType == codexRateLimitReachedTimeWindow && !anyExceeded && bindingTierKey != "" {
		if info, ok := summary.rateLimits[bindingTierKey].(map[string]interface{}); ok {
			info[contracts.RateLimitFieldStatus] = contracts.RateLimitStatusExceeded
		}
	}
	accountBlock := map[string]interface{}{}
	if reachedType != "" && reachedType != codexRateLimitReachedTimeWindow {
		accountBlock[contracts.RateLimitFieldRateLimitType] = reachedType
		accountBlock[contracts.RateLimitFieldStatus] = contracts.RateLimitStatusExceeded
	}
	summary.rateLimits[codexRateLimitAccountBlockKey] = accountBlock
	return summary
}

type codexRateLimitTier struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// codexRateLimitReachedTimeWindow identifies the Codex limit that the rolling-window timer clears.
// The other limits require a billing or usage change.
const codexRateLimitReachedTimeWindow = "rate_limit_reached"

// codexRateLimitAccountBlockKey identifies a billing or workspace block independently from the rolling windows.
const codexRateLimitAccountBlockKey = "account_block"

func codexRateLimitResumeReset(reachedType string, summary codexRateLimitSummary) *time.Time {
	switch reachedType {
	case "":
		return summary.latestExceededReset
	case codexRateLimitReachedTimeWindow:
		if summary.latestExceededReset != nil {
			return summary.latestExceededReset
		}
		if summary.bindingReset != nil {
			return summary.bindingReset
		}
		return summary.latestReset
	default:
		return nil
	}
}

func codexWindowToType(mins int) string {
	switch mins {
	case 300:
		return contracts.RateLimitTypeFiveHour
	case 10080:
		return contracts.RateLimitTypeSevenDay
	default:
		if mins >= 1440 {
			return fmt.Sprintf("%d_day", (mins+720)/1440)
		}
		return fmt.Sprintf("%d_hour", (mins+30)/60)
	}
}
