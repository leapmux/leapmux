package qoder

import (
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// usageInfoTimeout limits a get_usage_info request. Qoder reads the account
// quota service, or a cached reply of the last 15 seconds.
const usageInfoTimeout = 30 * time.Second

// The account quotas of a get_usage_info reply, by Qoder's own names. Each
// becomes one rate-limit window of that name.
const (
	qoderQuotaUser    = "user_quota"
	qoderQuotaAddOn   = "add_on_quota"
	qoderQuotaOrg     = "org_resource_package"
	qoderQuotaAccount = "account"
)

// qoderUsageReply is the part of a get_usage_info reply that LeapMux reads.
// Qoder reports `usage` as null when the quota service does not answer.
type qoderUsageReply struct {
	Usage *qoderQuotaUsage `json:"usage"`
}

// qoderQuotaUsage is Qoder's normalized account quota. Each percentage is a
// percent, from 0 upward.
//
// The reply also states `expiresAt`. LeapMux shows no reset time from it:
// Qoder 1.1.78 gives it no meaning of its own, its `/usage` view shows the
// plan's end date instead, and the field can hold the end of the current
// credits rather than a reset. Dedicated resource packages are not shown:
// their time is an expiry, and each applies to some models only.
type qoderQuotaUsage struct {
	IsQuotaExceeded    bool        `json:"isQuotaExceeded"`
	UserQuota          *qoderQuota `json:"userQuota"`
	AddOnQuota         *qoderQuota `json:"addOnQuota"`
	OrgResourcePackage *qoderQuota `json:"orgResourcePackage"`
}

type qoderQuota struct {
	Percentage *float64 `json:"percentage"`
}

// qoderRateLimitValues converts the account quota into rate-limit windows.
func qoderRateLimitValues(usage *qoderQuotaUsage) map[string]any {
	values := map[string]any{}
	for _, quota := range []struct {
		name  string
		quota *qoderQuota
	}{
		{qoderQuotaUser, usage.UserQuota},
		{qoderQuotaAddOn, usage.AddOnQuota},
		{qoderQuotaOrg, usage.OrgResourcePackage},
	} {
		if info := qoderQuotaWindow(quota.name, quota.quota); info != nil {
			values[quota.name] = info
		}
	}
	if usage.IsQuotaExceeded {
		values[qoderQuotaAccount] = map[string]any{
			contracts.RateLimitFieldRateLimitType: qoderQuotaAccount,
			contracts.RateLimitFieldStatus:        contracts.RateLimitStatusExceeded,
		}
	}
	return values
}

// qoderQuotaWindow is one window, or nil for an absent quota or a percentage
// that is not a number from 0 upward. Use above 100 percent is overage, so it
// keeps its value and reads as exceeded.
func qoderQuotaWindow(name string, quota *qoderQuota) map[string]any {
	if quota == nil || quota.Percentage == nil {
		return nil
	}
	percent := *quota.Percentage
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 {
		return nil
	}
	utilization := percent / 100
	return map[string]any{
		contracts.RateLimitFieldRateLimitType: name,
		contracts.RateLimitFieldUtilization:   utilization,
		contracts.RateLimitFieldStatus:        providerkit.RateLimitStatusForUtilization(utilization),
	}
}

// requestUsageRefresh asks the refresh loop for the account quota. It never
// waits: a request while another one waits joins it. An agent without a
// running process has no loop, and the request does nothing.
func (a *Agent) requestUsageRefresh() {
	if a.usageRefresh == nil {
		return
	}
	select {
	case a.usageRefresh <- struct{}{}:
	default:
	}
}

// refreshUsageLoop sends one get_usage_info at a time until the process ends.
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

// refreshUsage reads the account quota and publishes its windows. A refused
// request, a reply without a quota, or a malformed reply publishes nothing, so
// the last state stays.
func (a *Agent) refreshUsage() {
	body, err := json.Marshal(map[string]any{"subtype": contracts.QoderControlRequestSubtypeGetUsageInfo})
	if err != nil {
		return
	}
	result, err := a.sendControlAndWait(string(body), usageInfoTimeout)
	if err != nil {
		slog.Debug("qoder could not read the account quota", "agent_id", a.AgentID(), "error", err)
		return
	}
	var reply qoderUsageReply
	if err := json.Unmarshal(result.RawResponse, &reply); err != nil {
		slog.Warn("qoder: unreadable account quota", "agent_id", a.AgentID(), "error", err)
		return
	}
	if reply.Usage == nil {
		return
	}
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: qoderRateLimitValues(reply.Usage),
	}})
}
