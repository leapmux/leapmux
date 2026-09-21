package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

// handleRateLimitsUpdated publishes account state to live session surfaces and
// schedules automatic continuation. Account snapshots never become transcript rows.
func (a *CodexAgent) handleRateLimitsUpdated(content []byte, params json.RawMessage) {
	var notif struct {
		RateLimits struct {
			Primary              *codexRateLimitTier `json:"primary"`
			Secondary            *codexRateLimitTier `json:"secondary"`
			RateLimitReachedType *string             `json:"rateLimitReachedType"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex rate limit unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	reachedType := ""
	if notif.RateLimits.RateLimitReachedType != nil {
		reachedType = *notif.RateLimits.RateLimitReachedType
	}
	summary := summarizeCodexRateLimits([]*codexRateLimitTier{notif.RateLimits.Primary, notif.RateLimits.Secondary}, reachedType)
	a.sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyRateLimits: summary.rateLimits})

	if resumeReset := codexRateLimitResumeReset(reachedType, summary); resumeReset != nil {
		a.sink.ScheduleAutoContinue(AutoContinueSchedule{
			Reason:        AutoContinueReasonRateLimit,
			DueAt:         *resumeReset,
			SourcePayload: append([]byte(nil), content...),
		})
	} else {
		a.sink.CancelAutoContinue(AutoContinueReasonRateLimit)
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
		status := codexTierStatus(tier.UsedPercent)
		if status == codexRateLimitStatusExceeded {
			anyExceeded = true
		}
		info := map[string]interface{}{
			contracts.RateLimitFieldRateLimitType: rateLimitType,
			contracts.RateLimitFieldUtilization:   tier.UsedPercent / 100,
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
			if status == codexRateLimitStatusExceeded &&
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
			info[contracts.RateLimitFieldStatus] = codexRateLimitStatusExceeded
		}
	}
	accountBlock := map[string]interface{}{}
	if reachedType != "" && reachedType != codexRateLimitReachedTimeWindow {
		accountBlock[contracts.RateLimitFieldRateLimitType] = reachedType
		accountBlock[contracts.RateLimitFieldStatus] = codexRateLimitStatusExceeded
	}
	summary.rateLimits[contracts.CodexRateLimitAccountBlockKey] = accountBlock
	return summary
}

type codexRateLimitTier struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

const (
	codexRateLimitStatusAllowed        = "allowed"
	codexRateLimitStatusAllowedWarning = "allowed_warning"
	codexRateLimitStatusExceeded       = "exceeded"
	codexRateLimitReachedTimeWindow    = contracts.CodexRateLimitReachedTimeWindow
)

func codexTierStatus(usedPercent float64) string {
	switch {
	case usedPercent >= 100:
		return codexRateLimitStatusExceeded
	case usedPercent >= 80:
		return codexRateLimitStatusAllowedWarning
	default:
		return codexRateLimitStatusAllowed
	}
}

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
		return "five_hour"
	case 10080:
		return "seven_day"
	default:
		if mins >= 1440 {
			return fmt.Sprintf("%d_day", (mins+720)/1440)
		}
		return fmt.Sprintf("%d_hour", (mins+30)/60)
	}
}
