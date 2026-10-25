package zcode

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Local reads detect sign-in changes even when the app-server offers no models
// and therefore makes no runtime credential requests. They send no billing request
// unless a credential changes, a pending plan becomes effective, or a retry is due.
const zcodeAccountPollInterval = 30 * time.Second

var zcodeStartPlanRecheckDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

const zcodeStartPlanRecheckTimerTag = "zcode-start-plan-recheck"

func (a *Agent) refreshAccountSnapshot(state *zcodeAccountState) bool {
	if state.unpushed() {
		if err := a.sendAccountSnapshot(state, a.APITimeout()); err != nil {
			slog.Warn("zcode account provider update failed", "agent_id", a.AgentID(), "error", err)
			return false
		}
	}
	state.mu.Lock()
	generation, refreshed := state.pushedGeneration, state.catalogGeneration
	state.mu.Unlock()
	if generation != refreshed {
		if err := a.refreshZCodeModelCatalog(); err != nil {
			slog.Warn("zcode model catalog refresh failed", "agent_id", a.AgentID(), "error", err)
		} else {
			state.mu.Lock()
			state.catalogGeneration = generation
			state.mu.Unlock()
			a.refreshZCodeUsageFromSession()
		}
	}
	return true
}

// recheckStartPlans is the sole refresh owner after the initial snapshot. It
// invalidates old credentials before checking replacements and serializes every
// snapshot publication. A result for an obsolete credential never replaces a new one.
func (a *Agent) recheckStartPlans(state *zcodeAccountState) {
	checker := a.startPlanChecker(state.canonical)
	for a.Context().Err() == nil {
		bindings := a.readAccountBindings(state.rules)
		state.reconcileBindings(bindings, a.Clock().Now())
		if !a.refreshAccountSnapshot(state) {
			if !a.waitForAccountRefresh(state, zcodeAccountPollInterval) {
				return
			}
			continue
		}
		due := state.dueStartPlans(a.Clock().Now())
		if len(due) > 0 {
			results := checker.start(a.Context(), zcodeStartPlanTargets(due), zcodeStartPlanCheckTimeout).wait()
			if a.Context().Err() != nil {
				return
			}
			// A sign-in can change during HTTP. Reconcile before admitting results.
			bindings = a.readAccountBindings(state.rules)
			state.reconcileBindings(bindings, a.Clock().Now())
			state.completeChecks(due, results, a.Clock().Now())
			// The next iteration publishes through the same path as invalidations
			// and retries, including the session refresh after a delayed acknowledgement.
			continue
		}
		delay := state.nextDelay(a.Clock().Now())
		if delay > 0 && !a.waitForAccountRefresh(state, delay) {
			return
		}
	}
}

func (a *Agent) waitForAccountRefresh(state *zcodeAccountState, delay time.Duration) bool {
	timer := a.Clock().NewTimer(delay, zcodeStartPlanRecheckTimerTag)
	select {
	case <-timer.C:
		return true
	case <-state.refresh:
		timer.Stop(zcodeStartPlanRecheckTimerTag)
		return true
	case <-a.Context().Done():
		timer.Stop(zcodeStartPlanRecheckTimerTag)
		return false
	}
}

func (a *Agent) startPlanRequestAuth(ctx context.Context, state *zcodeAccountState, binding zcodeAccountBinding, usage bool) (zcodeRequestAuth, error) {
	for {
		credential := binding.startPlanCredential()
		if credential == "" {
			return zcodeRequestAuth{}, zcodeMissingCredentialError(binding.accountID)
		}
		accepted, changed := state.credentialAccepted(binding.legacyID, credential, usage)
		if accepted {
			return zcodeRequestAuth{APIKey: credential}, nil
		}
		if changed == nil {
			return zcodeRequestAuth{}, fmt.Errorf("the ZCode Start Plan for provider %q is not available; check the plan in the ZCode application", binding.accountID)
		}
		state.requestRefresh()
		select {
		case <-ctx.Done():
			return zcodeRequestAuth{}, ctx.Err()
		case <-changed:
		}
		var ok bool
		binding, ok = state.binding(binding.accountID)
		if !ok {
			return zcodeRequestAuth{}, errZCodeScopeInvalidated
		}
		credentials := a.readAccountCredentials()
		session, _, err := credentials.session(binding.family())
		a.warnCredentialStoreError(err)
		binding.session = session
	}
}
