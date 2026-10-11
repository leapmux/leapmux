package service

import (
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type providerTurnAdmission struct {
	sink      *agentOutputSink
	activity  *agentActivity
	scope     *activityScope
	publisher *agentOutputSink
	session   *nativeSessionFact
	sequence  uint64
	state     agent.TurnState
}

type providerTurnLease struct {
	once     sync.Once
	mutation *sync.RWMutex
}

func (lease *providerTurnLease) Release() {
	if lease != nil {
		lease.once.Do(lease.mutation.RUnlock)
	}
}

// registeredCurrent uses the existing sink maps as the registration authority.
func (sink *agentOutputSink) registeredCurrent() bool {
	root, exists := sink.h.rootSinks.Load(sink.rootAgentID)
	if !exists || root != sink.turnPublisherSink() {
		return false
	}
	registered, exists := sink.h.sinksByAgent.Load(sink.agentID)
	return exists && registered == sink
}

func (admission *providerTurnAdmission) currentLocked(exactSequence bool) bool {
	if !admission.sink.registeredCurrent() || admission.sink.turnPublisherSink() != admission.publisher {
		return false
	}
	current, exists := admission.sink.h.activity.Load(admission.sink.agentID)
	if !exists || current != admission.activity || admission.activity.scope != admission.scope ||
		admission.sink.currentMessageSessionFact() != admission.session || !admission.sink.ownsControlPublisher() {
		return false
	}
	sequence := admission.activity.turnSeq
	if exactSequence && sequence != admission.sequence {
		return false
	}
	return sequence >= admission.sequence && admission.activity.turnActive == admission.state.Active &&
		admission.activity.turnSteerable == admission.state.Steerable
}

func (admission *providerTurnAdmission) Acquire() (agent.TurnState, agent.TurnStateLease) {
	mutation := admission.sink.h.transcriptMutationMutex(admission.sink.rootAgentID)
	mutation.RLock()
	admission.activity.mu.Lock()
	current := admission.currentLocked(true)
	admission.activity.mu.Unlock()
	if !current {
		mutation.RUnlock()
		return agent.TurnState{}, nil
	}
	return admission.state, &providerTurnLease{mutation: mutation}
}

func (admission *providerTurnAdmission) IsCurrent() bool {
	mutation := admission.sink.h.transcriptMutationMutex(admission.sink.rootAgentID)
	mutation.RLock()
	defer mutation.RUnlock()
	admission.activity.mu.Lock()
	defer admission.activity.mu.Unlock()
	return admission.currentLocked(false)
}
