package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kiro normalizes its account service through this process-wide extension.
const kiroAccountUsageMethod = "_kiro/account/getUsage"

type kiroUsageReply struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Data    *kiroUsageData `json:"data"`
}

type kiroUsageData struct {
	PlanName          string               `json:"planName"`
	BillingCycleReset string               `json:"billingCycleReset"`
	UsageBreakdowns   []kiroUsageBreakdown `json:"usageBreakdowns"`
}

type kiroUsageBreakdown struct {
	ResourceType string   `json:"resourceType"`
	DisplayName  string   `json:"displayName"`
	Used         *float64 `json:"used"`
	Limit        *float64 `json:"limit"`
	HasLimit     *bool    `json:"hasLimit"`
}

// kiroRateLimitValues reports recurring account limits. Bonus and add-on credit
// expiry dates do not describe billing resets, so they supply no quota window.
func kiroRateLimitValues(usage kiroUsageData) map[string]any {
	values := make(map[string]any)
	counts := make(map[string]int)
	for _, resource := range usage.UsageBreakdowns {
		counts[strings.TrimSpace(resource.ResourceType)]++
	}
	reset, err := time.Parse(time.DateOnly, usage.BillingCycleReset)
	validReset := err == nil && reset.Year() > 0
	for _, resource := range usage.UsageBreakdowns {
		kind := strings.TrimSpace(resource.ResourceType)
		if kind == "" || counts[kind] != 1 || resource.HasLimit == nil || !*resource.HasLimit || resource.Used == nil || resource.Limit == nil {
			continue
		}
		used, limit := *resource.Used, *resource.Limit
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || math.IsNaN(limit) || math.IsInf(limit, 0) || limit <= 0 {
			continue
		}
		utilization := used / limit
		if math.IsInf(utilization, 0) {
			continue
		}
		label := strings.TrimSpace(resource.DisplayName)
		if label == "" {
			label = kind
		}
		if plan := strings.TrimSpace(usage.PlanName); plan != "" {
			label = plan + " — " + label
		}
		info := map[string]any{
			contracts.RateLimitFieldRateLimitType: kind,
			contracts.RateLimitFieldLabel:         label,
			contracts.RateLimitFieldUtilization:   utilization,
			contracts.RateLimitFieldStatus:        providerkit.RateLimitStatusForUtilization(utilization),
		}
		if validReset {
			info[contracts.RateLimitFieldResetsOn] = usage.BillingCycleReset
		}
		values[kind] = info
	}
	return values
}

// A successful empty list clears stale windows. Missing data and refused reads
// keep the last state, because they do not report an empty account quota.
func decodeKiroUsage(raw json.RawMessage) (map[string]any, error) {
	var reply kiroUsageReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("decode the Kiro account usage: %w", err)
	}
	if !reply.Success || reply.Data == nil || reply.Data.UsageBreakdowns == nil {
		return nil, fmt.Errorf("the Kiro agent reported no account usage: %s", reply.Message)
	}
	values := kiroRateLimitValues(*reply.Data)
	for _, resource := range reply.Data.UsageBreakdowns {
		kind := strings.TrimSpace(resource.ResourceType)
		if kind == "" || resource.HasLimit == nil || *resource.HasLimit && values[kind] == nil {
			return nil, fmt.Errorf("the Kiro agent reported an invalid account resource %q", kind)
		}
	}
	return values, nil
}

// startUsageRefresh starts one process-owned loop after startup. A buffered
// request during a read schedules one further read without delaying the reader.
func (a *Agent) startUsageRefresh() {
	a.stateMu.Lock()
	if a.usageStarted {
		a.stateMu.Unlock()
		return
	}
	a.usageStarted = true
	requests, done := a.usageRefresh, a.usageDone
	a.stateMu.Unlock()
	go func() {
		defer close(done)
		for {
			select {
			case <-a.Context().Done():
				return
			case <-a.ProcessDone():
				return
			case <-requests:
				if a.IsStopped() || a.Context().Err() != nil {
					return
				}
				a.refreshUsage()
			}
		}
	}()
	a.requestUsageRefresh()
}

func (a *Agent) requestUsageRefresh() {
	a.stateMu.Lock()
	started, requests := a.usageStarted, a.usageRefresh
	a.stateMu.Unlock()
	if !started {
		return
	}
	select {
	case requests <- struct{}{}:
	default:
	}
}

// Wait joins the loop after the process has released its pending RPC replies.
func (a *Agent) waitUsageRefresh() {
	a.stateMu.Lock()
	started, done := a.usageStarted, a.usageDone
	a.stateMu.Unlock()
	if started {
		<-done
	}
}

func (a *Agent) refreshUsage() {
	raw, err := a.SendRequest(kiroAccountUsageMethod, json.RawMessage(`{}`), a.APITimeout())
	if err != nil {
		slog.Debug("kiro could not read account usage", "agent_id", a.AgentID(), "error", err)
		return
	}
	values, err := decodeKiroUsage(raw)
	if err != nil {
		slog.Debug("kiro reported no readable account usage", "agent_id", a.AgentID(), "error", err)
		return
	}
	if a.IsStopped() || a.Context().Err() != nil {
		return
	}
	a.Sink().BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}
