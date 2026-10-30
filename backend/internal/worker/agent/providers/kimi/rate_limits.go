package kimi

import (
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
//
// A main turn that fails on a usage limit with no reset instant hands its
// retry verdict to the next read, which states the reset. See
// kimiPendingUsageLimit.

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

// kimiExhaustedResetAt is the latest reset of a window that the plan used up,
// or zero when no used-up window states a reset. The account can send again
// only after every used-up window resets.
func kimiExhaustedResetAt(usages kimiPlanUsages) time.Time {
	var latest time.Time
	for _, window := range []*kimiQuotaWindow{usages.Limit5h, usages.Limit7d, usages.MonthTotal} {
		if window == nil || window.UsedRatio == nil || *window.UsedRatio < 1 {
			continue
		}
		reset, err := time.Parse(time.RFC3339Nano, window.ResetAt)
		if err == nil && reset.After(latest) {
			latest = reset
		}
	}
	return latest
}

// kimiPendingUsageLimit is the verdict of a main turn that failed on a usage
// limit with no reset instant, while the session ran a model of the plan.
//
// The event states no reset, and the plan-usage read does: the latest reset of
// a window that the plan used up. The turn end stores this record before it
// requests the read, and the read takes it when it starts. A read that runs
// already took nothing, and the repeat that usageWork owes it takes the record.
//
// The read can end after the state that the record belongs to changed. The
// record therefore keeps that state, and the read drops a record whose
// usageTurnSeq, session, or model differs: a new turn, a model change, or a
// context clear replaced the turn that failed.
type kimiPendingUsageLimit struct {
	seq       uint64
	turnID    int64
	sessionID string
	model     string
	failure   providerkit.TurnFailure
	payload   []byte
	// writer is the retry writer of the transcript that the turn ended in,
	// captured at the turn end because the verdict applies after the read.
	writer agent.AutoRetryWriter
}

// deferUsageLimit stores the verdict of a main turn for the next plan-usage
// read when that read can state its reset: a usage limit with no reset instant,
// on a model of the plan. It reports whether it stored the verdict. The caller
// applies every other verdict at once.
//
// The caller reserves the completion sequence while it clears the turn flag. The writer
// keeps the original authority through that exact inactive publication. Its
// operations wait behind retained transcript rows, and a newer publication
// rejects them.
func (a *Agent) deferUsageLimit(failure providerkit.TurnFailure, payload []byte, writer agent.AutoRetryWriter) bool {
	if failure.Kind != providerkit.TurnFailureUsageLimited || !failure.ResetAt.IsZero() {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if !a.catalog.managed(a.settings.model) {
		return false
	}
	a.pendingUsageLimit = &kimiPendingUsageLimit{
		seq:       a.usageTurnSeq,
		sessionID: a.sessionID,
		model:     a.settings.model,
		failure:   failure,
		payload:   slices.Clone(payload),
		writer:    writer,
	}
	if run := a.runs[kimiMainAgentID]; run != nil {
		a.pendingUsageLimit.turnID = run.turnID
	}
	return true
}

// requestUsageRefresh asks for a read of the plan usage. It never waits.
func (a *Agent) requestUsageRefresh() {
	a.usageWork.Run(kimiUsageWorkKey, a.refreshUsage)
}

// refreshUsage publishes the quota windows that limit the current model. A
// model of another provider has none, so its read clears the windows, and the
// sink drops each repeat of that empty state. A failed or malformed read, or
// an answer of the error kind, publishes nothing, so the last windows stay.
//
// It takes the pending usage limit when it starts, and settles it on every
// exit. See kimiPendingUsageLimit.
func (a *Agent) refreshUsage() {
	a.Mu.Lock()
	pending := a.pendingUsageLimit
	a.pendingUsageLimit = nil
	managed := a.catalog.managed(a.settings.model)
	a.Mu.Unlock()
	if !managed {
		a.publishRateLimits(map[string]any{})
		a.settleUsageLimit(pending, time.Time{})
		return
	}
	usages, ok := a.readPlanUsage()
	if !ok {
		a.settleUsageLimit(pending, time.Time{})
		return
	}
	a.publishRateLimits(kimiRateLimitValues(usages))
	a.settleUsageLimit(pending, kimiExhaustedResetAt(usages))
}

// readPlanUsage reads the quota windows of the plan. ok is false for a failed
// request, a malformed answer, and an answer of the error kind.
func (a *Agent) readPlanUsage() (kimiPlanUsages, bool) {
	ctx, cancel := a.requestContext()
	defer cancel()
	var usage kimiManagedUsage
	if err := a.api.get(ctx, kimiRouteManagedUsage, &usage); err != nil {
		slog.Debug("kimi could not read the plan usage", "agent_id", a.AgentID(), "error", err)
		return kimiPlanUsages{}, false
	}
	if usage.Kind != kimiUsageKindOK || usage.Quota == nil {
		slog.Debug("kimi reported no plan usage", "agent_id", a.AgentID(), "kind", usage.Kind, "message", usage.Message)
		return kimiPlanUsages{}, false
	}
	return usage.Quota.Usages, true
}

// settleUsageLimit applies a pending usage limit after a plan-usage read.
// resetAt is the latest reset of a window that the plan used up, or zero when
// the read failed or found no such window. A valid record schedules the retry
// at resetAt, and otherwise applies the verdict that the turn end stored.
//
// dispatchMu holds the event path off from the check to the apply, so a turn
// that starts meanwhile cannot receive the verdict of the turn it replaced.
func (a *Agent) settleUsageLimit(pending *kimiPendingUsageLimit, resetAt time.Time) {
	if pending == nil {
		return
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	current := pending.seq == a.usageTurnSeq && pending.sessionID == a.sessionID && pending.model == a.settings.model
	stopped := a.stopRequested != nil && a.stopRequested.sessionID == pending.sessionID && a.stopRequested.turnID == pending.turnID
	a.Mu.Unlock()
	if !current {
		slog.Debug("kimi dropped the usage limit of a replaced turn", "agent_id", a.AgentID(), "session_id", pending.sessionID, "model", pending.model)
		return
	}
	failure := pending.failure
	if stopped || a.IntentionalStopRequested() {
		providerkit.ApplyTurnFailure(pending.writer, providerkit.TurnFailure{}, nil)
		return
	}
	if !resetAt.IsZero() {
		failure = providerkit.WithResetHint(failure, resetAt, a.clock.Now())
	}
	providerkit.ApplyTurnFailure(pending.writer, failure, pending.payload)
}

// publishRateLimits replaces the published windows with values.
func (a *Agent) publishRateLimits(values map[string]any) {
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}
