package service

import (
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// activityScope owns one turn between changed turn publications or process changes.
// agentActivity.mu guards every field. Repeated turn publications keep the scope.
type activityScope struct {
	publisher         any
	attempts          map[*stopAttempt]struct{}
	controlsStopped   bool
	transcriptThreads map[transcriptOwnerKey]*transcriptThread
}

type stopDisposition uint8

const (
	stopPending stopDisposition = iota
	stopDelivered
	stopIgnored
	stopFailed
)

// stopAttempt retains its exact disposition after the scope removes its active mark.
// agentActivity.mu guards the disposition. The allocation supplies its unique identity.
type stopAttempt struct{ disposition stopDisposition }

type ownedControlRequest struct {
	claimToken string
	scope      *activityScope
}

type agentStopRequest struct {
	h           *OutputHandler
	activity    *agentActivity
	agentID     string
	rootAgentID string
	scope       *activityScope
	attempt     *stopAttempt
	controls    []controlRequestInstance
}

func (s *agentOutputSink) ownsControlPublisher() bool {
	publisher, adopted := s.h.turnPublisher.Load(s.rootAgentID)
	return !adopted || publisher == s.turnPublisher()
}

// lockControlMutation retains one activity entry before it waits for the mutation mutex.
// Entry deletion and admission check the same activity mutex and map identity.
func (h *OutputHandler) lockControlMutation(agentID, rootAgentID string) (*agentActivity, *activityScope, func()) {
	for {
		st := h.activityFor(agentID, rootAgentID)
		st.mu.Lock()
		current, exists := h.activity.Load(agentID)
		if !exists || current != st {
			st.mu.Unlock()
			continue
		}
		rootAgentID = st.rootAgentID
		scope := h.scopeLocked(st, rootAgentID)
		st.controlMutations++
		st.mu.Unlock()
		st.controlMu.Lock()
		return st, scope, func() {
			st.controlMu.Unlock()
			st.mu.Lock()
			st.controlMutations--
			retire := rootAgentID != agentID && st.hasPublished && st.published != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING && st.retirableLocked()
			st.mu.Unlock()
			if retire {
				h.retireActivity(agentID, st)
			}
		}
	}
}

func (h *OutputHandler) scopeLocked(st *agentActivity, rootAgentID string) *activityScope {
	publisher, _ := h.turnPublisher.Load(rootAgentID)
	if st.scope == nil || st.scope.publisher != publisher {
		st.scope = &activityScope{publisher: publisher}
	}
	return st.scope
}

func (st *agentActivity) stopRequestedLocked() bool {
	return st.scope != nil && len(st.scope.attempts) > 0
}

// NoteAgentStopRequested publishes a stop before queue bookkeeping or native delivery.
// The returned request owns only its turn scope and its exact control instances.
func (h *OutputHandler) NoteAgentStopRequested(agentID, rootAgentID string) *agentStopRequest {
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	scope := h.scopeLocked(st, rootAgentID)
	attempt := &stopAttempt{}
	if scope.attempts == nil {
		scope.attempts = make(map[*stopAttempt]struct{})
	}
	scope.attempts[attempt] = struct{}{}
	request := &agentStopRequest{
		h: h, activity: st, agentID: agentID, rootAgentID: rootAgentID,
		scope: scope, attempt: attempt,
	}
	for requestID, control := range st.pendingControl {
		if control.scope == scope {
			request.controls = append(request.controls, controlRequestInstance{RequestID: requestID, ClaimToken: control.claimToken})
		}
	}
	st.mu.Unlock()
	h.publishStopMark(agentID, rootAgentID)
	return request
}

func (r *agentStopRequest) Context() agent.StopContext {
	// Capture the attempt identity without retaining the control snapshot.
	h, st, scope, attempt := r.h, r.activity, r.scope, r.attempt
	agentID, rootAgentID := r.agentID, r.rootAgentID
	return agent.NewStopContext(func() {
		h.withdrawStopAttempt(st, scope, attempt, stopIgnored, agentID, rootAgentID)
	})
}

func (r *agentStopRequest) Failed() {
	r.h.withdrawStopAttempt(r.activity, r.scope, r.attempt, stopFailed, r.agentID, r.rootAgentID)
}

func (h *OutputHandler) withdrawStopAttempt(st *agentActivity, scope *activityScope, attempt *stopAttempt, disposition stopDisposition, agentID, rootAgentID string) {
	st.mu.Lock()
	_, exists := scope.attempts[attempt]
	attempt.disposition = disposition
	delete(scope.attempts, attempt)
	accepted := false
	for other := range scope.attempts {
		accepted = accepted || other.disposition == stopDelivered
	}
	scope.controlsStopped = accepted
	current := st.scope == scope
	st.mu.Unlock()
	if exists && current {
		h.publishStopMark(agentID, rootAgentID)
	}
}

func (r *agentStopRequest) IsCurrent() bool {
	r.activity.mu.Lock()
	defer r.activity.mu.Unlock()
	return r.activity.scope == r.scope
}

// Delivered withdraws the captured turn's controls after successful native delivery.
// Exact claim tokens protect replacement controls that reuse the same request ID.
func (r *agentStopRequest) Delivered(target agent.StopTarget) {
	if !target.IsCurrent() {
		r.Failed()
		return
	}
	st, _, release := r.h.lockControlMutation(r.agentID, r.rootAgentID)
	publication := controlPublication{}
	defer func() { r.h.finishControlPublication(r.agentID, r.rootAgentID, publication, release) }()
	r.activity.mu.Lock()
	if r.attempt.disposition == stopIgnored || r.attempt.disposition == stopFailed {
		r.activity.mu.Unlock()
		return
	}
	r.attempt.disposition = stopDelivered
	r.scope.controlsStopped = true
	r.activity.mu.Unlock()
	controls := append([]controlRequestInstance(nil), r.controls...)
	st.mu.Lock()
	for requestID, control := range st.pendingControl {
		if control.scope == r.scope {
			controls = append(controls, controlRequestInstance{RequestID: requestID, ClaimToken: control.claimToken})
		}
	}
	st.mu.Unlock()
	seen := make(map[controlRequestInstance]struct{}, len(controls))
	for _, control := range controls {
		if _, repeated := seen[control]; repeated {
			continue
		}
		seen[control] = struct{}{}
		request, err := r.h.queries.DeleteControlRequestInstance(bgCtx(), db.DeleteControlRequestInstanceParams{
			AgentID: r.agentID, RequestID: control.RequestID, ClaimToken: control.ClaimToken,
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			publication.recordFailure("cancel the stopped turn's control request", "agent_id", r.agentID, "request_id", control.RequestID, "error", err)
			continue
		}
		publication.enqueueCancellation(r.h, r.agentID, request.RequestID, request.ClaimToken)
	}
}
