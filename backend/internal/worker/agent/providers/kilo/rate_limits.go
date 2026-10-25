package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kiloProviderUsageRoute is the daemon route that reports the usage of the
// reader's provider plans: a coding plan that Kilo manages, and a plan of
// Codex or MiniMax that the reader signed in to directly. Kilo keeps each
// source's answer for a minute, so a read after each turn reaches the plan's
// own service at most once a minute.
const kiloProviderUsageRoute = "/kilocode/provider-usage"

// kiloUsageReadTimeout limits one read of the route. Kilo waits up to five
// seconds for each source.
const kiloUsageReadTimeout = 30 * time.Second

// The window states of Kilo's usage report.
const (
	kiloWindowExhausted = "exhausted"
	kiloWindowUnlimited = "unlimited"
	kiloWindowNotInPlan = "not_in_plan"
)

// The orientations of a window's numbers.
const (
	kiloUsedPercent      = "used_percent"
	kiloRemainingPercent = "remaining_percent"
)

// kiloPlanResource is the resource of a window that covers the whole plan.
// A window of a named resource states that resource in its type.
const kiloPlanResource = "subscription"

// kiloUsageReport is the part of the route's answer that LeapMux reads.
type kiloUsageReport struct {
	Items *[]kiloUsageSnapshot `json:"items"`
}

// kiloUsageSnapshot is the usage of one plan.
type kiloUsageSnapshot struct {
	ID      string            `json:"id"`
	Windows []kiloUsageWindow `json:"windows"`
}

// kiloUsageWindow is one quota window of a plan.
type kiloUsageWindow struct {
	ID          string           `json:"id"`
	Resource    string           `json:"resource"`
	Orientation string           `json:"orientation"`
	Used        *float64         `json:"used"`
	Remaining   *float64         `json:"remaining"`
	Limit       *float64         `json:"limit"`
	Period      *kiloUsagePeriod `json:"period"`
	ResetAt     string           `json:"resetAt"`
	State       string           `json:"state"`
}

type kiloUsagePeriod struct {
	Unit  string `json:"unit"`
	Value int    `json:"value"`
}

// kiloRateLimitValues converts the plans of a usage report into rate-limit
// entries, keyed by plan and window.
func kiloRateLimitValues(snapshots []kiloUsageSnapshot) map[string]any {
	values := map[string]any{}
	for _, snapshot := range snapshots {
		for _, window := range snapshot.Windows {
			if info := kiloRateLimitWindow(window); info != nil {
				values[snapshot.ID+"/"+window.ID] = info
			}
		}
	}
	return values
}

// kiloRateLimitWindow is one entry, or nil for a window that limits nothing
// (unlimited, or not in the plan) or that states no use.
func kiloRateLimitWindow(window kiloUsageWindow) map[string]any {
	if window.ID == "" || window.State == kiloWindowUnlimited || window.State == kiloWindowNotInPlan {
		return nil
	}
	utilization, known := kiloWindowUtilization(window)
	exhausted := window.State == kiloWindowExhausted
	if !known && !exhausted {
		return nil
	}
	info := map[string]any{contracts.RateLimitFieldRateLimitType: kiloWindowType(window)}
	status := providerkit.RateLimitStatusForUtilization(utilization)
	if exhausted {
		status = contracts.RateLimitStatusExceeded
	}
	info[contracts.RateLimitFieldStatus] = status
	if known {
		info[contracts.RateLimitFieldUtilization] = utilization
	}
	if reset, err := time.Parse(time.RFC3339Nano, window.ResetAt); err == nil {
		info[contracts.RateLimitFieldResetsAt] = reset.Unix()
	}
	return info
}

// kiloWindowUtilization is the used share of a window, from 0 upward. It reads
// the amounts against the limit first, and the percentages otherwise. Use
// above the limit is overage, so it keeps its value and reads as exceeded.
func kiloWindowUtilization(window kiloUsageWindow) (float64, bool) {
	valid := func(value *float64) bool {
		return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
	}
	if valid(window.Limit) && *window.Limit > 0 {
		if valid(window.Used) {
			return *window.Used / *window.Limit, true
		}
		if valid(window.Remaining) {
			return math.Max(0, 1 - *window.Remaining / *window.Limit), true
		}
	}
	switch window.Orientation {
	case kiloUsedPercent:
		if valid(window.Used) {
			return *window.Used / 100, true
		}
	case kiloRemainingPercent:
		if valid(window.Remaining) {
			return math.Max(0, 1-*window.Remaining/100), true
		}
	}
	return 0, false
}

// kiloWindowType is the type of a window, which its period decides, as for
// the other providers: `five_hour`, `seven_day` and `one_month` for the three
// periods that LeapMux labels, and `<value>_<unit>` for any other period. A
// window that a resource other than the plan limits puts the resource first.
func kiloWindowType(window kiloUsageWindow) string {
	name := "quota"
	if period := window.Period; period != nil && period.Value > 0 && period.Unit != "" {
		switch {
		case period.Unit == "hour" && period.Value == 5:
			name = contracts.RateLimitTypeFiveHour
		case (period.Unit == "day" && period.Value == 7) || (period.Unit == "week" && period.Value == 1):
			name = contracts.RateLimitTypeSevenDay
		case period.Unit == "month" && period.Value == 1:
			name = contracts.RateLimitTypeOneMonth
		default:
			name = fmt.Sprintf("%d_%s", period.Value, period.Unit)
		}
	}
	if window.Resource != "" && window.Resource != kiloPlanResource {
		return window.Resource + "_" + name
	}
	return name
}

// requestUsageRefresh asks the refresh loop for the plan usage. It never
// waits: a request while another one waits joins it. An agent without the
// channel has no loop, and the request does nothing.
func (a *Agent) requestUsageRefresh() {
	if a.usageRefresh == nil {
		return
	}
	select {
	case a.usageRefresh <- struct{}{}:
	default:
	}
}

// refreshUsageLoop reads the plan usage once per request until the process ends.
func (a *Agent) refreshUsageLoop() {
	for {
		select {
		case <-a.usageRefresh:
			a.refreshUsage()
		case <-a.ProcessDone():
			return
		case <-a.Context().Done():
			return
		}
	}
}

// refreshUsage reads the plan usage and publishes its windows in place of the
// last ones. A failed or malformed read publishes nothing, so the last state
// stays. A report without plans clears the windows: the reader has none.
func (a *Agent) refreshUsage() {
	ctx, cancel := context.WithTimeout(a.Context(), kiloUsageReadTimeout)
	defer cancel()
	raw, err := a.ReadDaemon(ctx, kiloProviderUsageRoute)
	if err != nil {
		slog.Debug("kilo could not read the provider usage", "agent_id", a.AgentID(), "error", err)
		return
	}
	var report kiloUsageReport
	if err := json.Unmarshal(raw, &report); err != nil || report.Items == nil {
		slog.Warn("kilo: unreadable provider usage", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Sink().BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: kiloRateLimitValues(*report.Items),
	}})
}
