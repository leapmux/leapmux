package kimi

import (
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Rate-limit state.
//
// The plan of a Kimi Code account limits the turns of the models that the
// account supplies, which belong to the provider `managed:kimi-code`.
// `kimi web` reports the quota windows of that plan (GET /oauth/usage), and
// reads them from Kimi's service for each request. Kimi Code's own `/usage`
// shows them only while the current model belongs to that provider, and
// LeapMux keeps the same rule: the plan does not limit a model of another
// provider, so that model shows no window.
//
// The agent reads the windows after the session opens, after each turn of the
// main agent, and after the model changes. Each read runs on usageWork, off
// the event path, and a request during a read makes one more read after it.

// kimiManagedProvider is the provider key of the models that a Kimi Code
// account supplies.
const kimiManagedProvider = "managed:kimi-code"

// kimiRouteManagedUsage reports the quota windows of the Kimi Code account.
// Without a provider in the query, it reports the managed provider.
const kimiRouteManagedUsage = kimiAPIPrefix + "/oauth/usage"

// kimiUsageWorkKey is the usageWork key of the read.
const kimiUsageWorkKey = "plan-usage"

// kimiUsageKindOK is the kind of a usage answer that holds the windows. The
// other kind, `error`, states why Kimi Code could not read them: no account,
// a refused token, or a failed request to Kimi's service.
const kimiUsageKindOK = "ok"

// kimiManagedUsage is the data of GET /oauth/usage.
type kimiManagedUsage struct {
	Kind    string         `json:"kind"`
	Message string         `json:"message"`
	Quota   *kimiPlanQuota `json:"quota"`
}

// kimiPlanQuota is the quota of the plan.
type kimiPlanQuota struct {
	Usages kimiPlanUsages `json:"usages"`
}

// kimiPlanUsages holds the quota windows of the plan. Kimi Code also reports
// `monthCode`: the share of the monthly window that Kimi Code itself used. That
// share is part of `monthTotal` and limits nothing by itself, so LeapMux does
// not read it.
type kimiPlanUsages struct {
	Limit5h    *kimiQuotaWindow `json:"limit5h"`
	Limit7d    *kimiQuotaWindow `json:"limit7d"`
	MonthTotal *kimiQuotaWindow `json:"monthTotal"`
}

// kimiQuotaWindow is one window: the used share, where 1 is the whole window,
// and the time of its reset.
type kimiQuotaWindow struct {
	UsedRatio *float64 `json:"usedRatio"`
	ResetAt   string   `json:"resetAt"`
}

// kimiRateLimitValues converts the windows of the plan into rate-limit
// entries, keyed by type.
func kimiRateLimitValues(usages kimiPlanUsages) map[string]any {
	values := map[string]any{}
	for _, entry := range []struct {
		rateLimitType string
		window        *kimiQuotaWindow
	}{
		{contracts.RateLimitTypeFiveHour, usages.Limit5h},
		{contracts.RateLimitTypeSevenDay, usages.Limit7d},
		{contracts.RateLimitTypeOneMonth, usages.MonthTotal},
	} {
		if info := kimiRateLimitWindow(entry.rateLimitType, entry.window); info != nil {
			values[entry.rateLimitType] = info
		}
	}
	return values
}

// kimiRateLimitWindow is one entry, or nil for an absent window or a negative
// used share. A share above 1 is overage, so it keeps its value and reads as
// exceeded. A reset time that is not RFC 3339 is left out.
func kimiRateLimitWindow(rateLimitType string, window *kimiQuotaWindow) map[string]any {
	// A JSON number is always finite, so the share needs no NaN or infinity
	// check: a value out of the float64 range makes the whole read fail.
	if window == nil || window.UsedRatio == nil || *window.UsedRatio < 0 {
		return nil
	}
	used := *window.UsedRatio
	info := map[string]any{
		contracts.RateLimitFieldRateLimitType: rateLimitType,
		contracts.RateLimitFieldUtilization:   used,
		contracts.RateLimitFieldStatus:        providerkit.RateLimitStatusForUtilization(used),
	}
	if reset, err := time.Parse(time.RFC3339Nano, window.ResetAt); err == nil {
		info[contracts.RateLimitFieldResetsAt] = reset.Unix()
	}
	return info
}

// requestUsageRefresh asks for a read of the plan usage. It never waits.
func (a *Agent) requestUsageRefresh() {
	a.usageWork.Run(kimiUsageWorkKey, a.refreshUsage)
}

// refreshUsage publishes the quota windows that limit the current model. A
// model of another provider has none, so its read clears the windows, and the
// sink drops each repeat of that empty state. A failed or malformed read, or
// an answer of the error kind, publishes nothing, so the last windows stay.
func (a *Agent) refreshUsage() {
	a.Mu.Lock()
	managed := a.catalog.managed(a.settings.model)
	a.Mu.Unlock()
	if !managed {
		a.publishRateLimits(map[string]any{})
		return
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	var usage kimiManagedUsage
	if err := a.api.get(ctx, kimiRouteManagedUsage, &usage); err != nil {
		slog.Debug("kimi could not read the plan usage", "agent_id", a.AgentID(), "error", err)
		return
	}
	if usage.Kind != kimiUsageKindOK || usage.Quota == nil {
		slog.Debug("kimi reported no plan usage", "agent_id", a.AgentID(), "kind", usage.Kind, "message", usage.Message)
		return
	}
	a.publishRateLimits(kimiRateLimitValues(usage.Quota.Usages))
}

// publishRateLimits replaces the published windows with values.
func (a *Agent) publishRateLimits(values map[string]any) {
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}
