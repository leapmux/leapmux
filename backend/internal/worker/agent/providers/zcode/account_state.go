package zcode

import (
	"slices"
	"strconv"
	"sync"
	"time"
)

// zcodeAccountState keeps the latest account inputs and the generation that the
// app-server accepted. One refresh goroutine changes inputs and sends snapshots.
// Runtime credential requests read this state and request an early refresh.
type zcodeAccountState struct {
	basedOn   string
	rules     []zcodeBuiltinProviderRule
	canonical map[string]string

	mu                sync.Mutex
	bindings          []zcodeAccountBinding
	plans             map[string]*zcodeStartPlanState
	generation        int
	pushedGeneration  int
	catalogGeneration int
	changed           chan struct{}
	refresh           chan struct{}
}

type zcodeStartPlanState struct {
	credentialFingerprint string
	// requiredGeneration first carries the current credential and entitlement.
	requiredGeneration int
	entitlement        zcodeAccountEntitlement
	retryIndex         int
	nextCheck          *time.Time
}

func newZCodeAccountState(inputs zcodeAccountSnapshotInputs, results map[string]zcodeAccountEntitlement, now time.Time) *zcodeAccountState {
	s := &zcodeAccountState{
		basedOn: inputs.basedOn, rules: inputs.rules, canonical: inputs.canonical,
		bindings: append([]zcodeAccountBinding(nil), inputs.bindings...),
		plans:    map[string]*zcodeStartPlanState{},
		changed:  make(chan struct{}), refresh: make(chan struct{}, 1),
	}
	for _, binding := range s.bindings {
		if !binding.rule.isStartPlan() {
			continue
		}
		entitlement, ok := results[binding.legacyID]
		if !ok {
			entitlement = zcodeUnknownEntitlement
		}
		plan := &zcodeStartPlanState{
			credentialFingerprint: zcodeSHA256Hex(binding.startPlanCredential()),
			entitlement:           entitlement,
		}
		plan.scheduleNext(now)
		s.plans[binding.legacyID] = plan
	}
	return s
}

func (s *zcodeAccountState) binding(accountID string) (zcodeAccountBinding, bool) {
	if s == nil {
		return zcodeAccountBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		if binding.accountID == accountID {
			return binding, true
		}
	}
	return zcodeAccountBinding{}, false
}

func (s *zcodeAccountState) bindingsSnapshot() []zcodeAccountBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]zcodeAccountBinding(nil), s.bindings...)
}

func (s *zcodeAccountState) broadcastLocked() {
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}

func (s *zcodeAccountState) changedLocked() {
	s.generation++
	s.broadcastLocked()
}

// reconcileBindings invalidates results when their effective credential changes.
// It also admits newly signed-in plans and removes plans that lost all credentials.
func (s *zcodeAccountState) reconcileBindings(bindings []zcodeAccountBinding, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.EqualFunc(s.bindings, bindings, func(a, b zcodeAccountBinding) bool {
		return a.accountID == b.accountID && a.legacyID == b.legacyID && a.current == b.current
	}) {
		s.changedLocked()
	}
	s.bindings = bindings
	active := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if !binding.rule.isStartPlan() {
			continue
		}
		active[binding.legacyID] = true
		fingerprint := zcodeSHA256Hex(binding.startPlanCredential())
		plan := s.plans[binding.legacyID]
		if plan != nil && plan.credentialFingerprint == fingerprint {
			continue
		}
		s.changedLocked()
		deadline := now
		s.plans[binding.legacyID] = &zcodeStartPlanState{
			credentialFingerprint: fingerprint, requiredGeneration: s.generation,
			entitlement: zcodeUnknownEntitlement, nextCheck: &deadline,
		}
	}
	for id := range s.plans {
		if !active[id] {
			delete(s.plans, id)
			s.changedLocked()
		}
	}
}

func zcodeSameEntitlement(a, b zcodeAccountEntitlement) bool {
	if a.Availability != b.Availability || a.UnavailableReason != b.UnavailableReason ||
		(a.Models == nil) != (b.Models == nil) || !slices.Equal(a.Models, b.Models) ||
		(a.EffectiveAt == nil) != (b.EffectiveAt == nil) {
		return false
	}
	return a.EffectiveAt == nil || *a.EffectiveAt == *b.EffectiveAt
}

func (s *zcodeAccountState) learnLocked(id string, entitlement zcodeAccountEntitlement) {
	plan := s.plans[id]
	if plan != nil && !zcodeSameEntitlement(plan.entitlement, entitlement) {
		plan.entitlement = entitlement
		s.changedLocked()
		plan.requiredGeneration = s.generation
	}
}

// completeChecks accepts only results for the credential that remains current.
func (s *zcodeAccountState) completeChecks(bindings []zcodeAccountBinding, results map[string]zcodeAccountEntitlement, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range bindings {
		plan := s.plans[binding.legacyID]
		if plan == nil || plan.credentialFingerprint != zcodeSHA256Hex(binding.startPlanCredential()) {
			continue
		}
		entitlement, ok := results[binding.legacyID]
		if !ok {
			entitlement = zcodeUnknownEntitlement
		}
		s.learnLocked(binding.legacyID, entitlement)
		plan.scheduleNext(now)
		// Scheduling can end an immediate check without changing its unknown
		// result. Wake requests waiting for that check as well as snapshot changes.
		s.broadcastLocked()
	}
}

// scheduleNext retains the finite retry budget for transient failures. A future
// effective time is a separate trigger and starts a new budget when it arrives.
func (p *zcodeStartPlanState) scheduleNext(now time.Time) {
	p.nextCheck = nil
	switch p.entitlement.Availability {
	case zcodeAvailabilityAvailable, zcodeAvailabilityUnavailable:
		return
	case zcodeAvailabilityPending:
		if p.entitlement.EffectiveAt != nil && *p.entitlement.EffectiveAt > zcodeTimeSeconds(now) {
			p.retryIndex = 0
			return
		}
	}
	if p.retryIndex < len(zcodeStartPlanRecheckDelays) {
		deadline := now.Add(zcodeStartPlanRecheckDelays[p.retryIndex])
		p.retryIndex++
		p.nextCheck = &deadline
	}
}

func zcodeTimeSeconds(now time.Time) float64 { return float64(now.UnixMilli()) / 1000 }

func (p *zcodeStartPlanState) due(now time.Time) bool {
	if p.nextCheck != nil {
		return !now.Before(*p.nextCheck)
	}
	return p.entitlement.Availability == zcodeAvailabilityPending && p.retryIndex == 0 &&
		p.entitlement.EffectiveAt != nil && *p.entitlement.EffectiveAt <= zcodeTimeSeconds(now)
}

func (s *zcodeAccountState) dueStartPlans(now time.Time) []zcodeAccountBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []zcodeAccountBinding
	for _, binding := range s.bindings {
		if plan := s.plans[binding.legacyID]; plan != nil && plan.due(now) {
			due = append(due, binding)
		}
	}
	return due
}

func (s *zcodeAccountState) nextDelay(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	delay := zcodeAccountPollInterval
	for _, plan := range s.plans {
		if plan.due(now) {
			return 0
		}
		if plan.nextCheck != nil {
			delay = min(delay, max(0, plan.nextCheck.Sub(now)))
		}
		if plan.entitlement.Availability == zcodeAvailabilityPending && plan.entitlement.EffectiveAt != nil {
			seconds := *plan.entitlement.EffectiveAt - zcodeTimeSeconds(now)
			// Compare before converting, so a remote far-future time cannot overflow.
			if seconds > 0 && seconds < delay.Seconds() {
				delay = time.Duration(seconds * float64(time.Second))
			}
		}
	}
	return delay
}

func (s *zcodeAccountState) credentialAccepted(id, credential string, usage bool) (bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plans[id]
	if plan == nil {
		return false, nil
	}
	if plan.credentialFingerprint != zcodeSHA256Hex(credential) || s.pushedGeneration < plan.requiredGeneration {
		return false, s.changed
	}
	if usage || (plan.entitlement.Availability == zcodeAvailabilityAvailable && (plan.entitlement.Models == nil || len(plan.entitlement.Models) > 0)) {
		return true, s.changed
	}
	if plan.entitlement.Availability == zcodeAvailabilityUnknown && plan.retryIndex == 0 && plan.nextCheck != nil {
		return false, s.changed
	}
	return false, nil
}

func (s *zcodeAccountState) requestRefresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

func (s *zcodeAccountState) unpushed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation != s.pushedGeneration
}

// payload identifies every changed generation independently of acknowledgement
// success. Retrying the same generation is idempotent.
func (s *zcodeAccountState) payload(baseRevision string) (zcodeAccountProviderPayload, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := baseRevision
	if s.generation > 0 {
		revision += "#" + strconv.Itoa(s.generation)
	}
	entitlements := make(map[string]zcodeAccountEntitlement, len(s.plans))
	for id, plan := range s.plans {
		entitlements[id] = plan.entitlement
	}
	return newZCodeAccountProviderPayload(s.bindings, s.basedOn, revision, entitlements), s.generation
}

func (s *zcodeAccountState) accepted(generation int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushedGeneration = max(s.pushedGeneration, generation)
	s.broadcastLocked()
}
