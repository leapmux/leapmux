package service

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// nativeSessionSource retains the existing registration and native-session facts.
// A nonnil observation requires catalogMu when its authority is read or changed.
type nativeSessionSource struct {
	sink             *agentOutputSink
	publisher        *agentOutputSink
	session          *nativeSessionFact
	publisherSession *nativeSessionFact
	observation      *sessionObservationIdentity
}

// lock follows the lease order of NewRootSink. Release both leases before callbacks.
func (source nativeSessionSource) lock() func() {
	mutation := source.sink.h.transcriptMutationMutex(source.sink.rootAgentID)
	mutation.RLock()
	source.sink.h.rootSinkMu.RLock()
	return func() { source.sink.h.rootSinkMu.RUnlock(); mutation.RUnlock() }
}

func (source nativeSessionSource) currentLocked() bool {
	if source.sink == nil || source.publisher == nil || source.session == nil || source.publisherSession == nil {
		return false
	}
	if !source.sink.registeredCurrent() || source.sink.turnPublisherSink() != source.publisher {
		return false
	}
	if source.sink.currentMessageSessionFact() != source.session || source.publisher.currentMessageSessionFact() != source.publisherSession {
		return false
	}
	return source.observation == nil || source.sink.sessionObservation == source.observation
}

// An explicit unknown session retains its fact and expires when the sink learns an identity.
func (sink *agentOutputSink) capturedNativeSessionSource(captured agent.CapturedTranscript) (nativeSessionSource, error) {
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nativeSessionSource{}, errors.New("the captured write has no native publication owner")
	}
	owner, ok := content.Publication.Owner().(*transcriptOwner)
	if !ok || owner == nil || owner.sink != sink || owner.session == nil || owner.publisherSession == nil || content.AgentSessionID != owner.session.id {
		return nativeSessionSource{}, errors.New("the captured write belongs to another native source")
	}
	return nativeSessionSource{sink: sink, publisher: sink.turnPublisherSink(), session: owner.session, publisherSession: owner.publisherSession}, nil
}
