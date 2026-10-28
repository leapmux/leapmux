package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// nativeSessionFact is the sink's sole native-session identity.
// A changed session receives a new fact. A repeated session keeps its fact.
type nativeSessionFact struct{ id string }

type transcriptOwnerKey struct {
	sink      *agentOutputSink
	session   *nativeSessionFact
	sessionID string
}

// transcriptOwner retains the existing activity authority and its native session.
// The notification mutex protects its committed thread reference.
type transcriptOwner struct {
	sink             *agentOutputSink
	activity         *agentActivity
	scope            *activityScope
	session          *nativeSessionFact
	sessionID        string
	thread           *transcriptThread
	depth            int32
	spanLines        string
	spanColor        int32
	passthroughLines string
	span             agent.SpanInfo
	// beforeSessionInfoAdmission controls the cache/admission lock boundary in tests.
	beforeSessionInfoAdmission func()
}

// transcriptThread shares only its committed notification reference.
// The per-agent notification mutex protects the reference.
type transcriptThread struct{ reference *notifThreadRef }

// transcriptPublication reports progress through its exact immutable record owner.
type transcriptPublication struct {
	owner *transcriptOwner
}

func (publication *transcriptPublication) Owner() agent.TranscriptOwner { return publication.owner }

// sameLiveAuthority compares immutable facts without reading current state or taking a lock.
func (owner *transcriptOwner) sameLiveAuthority(other *transcriptOwner) bool {
	if owner == nil || other == nil {
		return owner == other
	}
	return owner.sink == other.sink && owner.activity == other.activity && owner.scope == other.scope &&
		owner.session == other.session && owner.sessionID == other.sessionID
}

func (owner *transcriptOwner) currentLocked() bool {
	if !owner.sink.registeredCurrent() {
		return false
	}
	current, exists := owner.sink.h.activity.Load(owner.sink.agentID)
	if !exists || current != owner.activity || owner.activity.scope != owner.scope {
		return false
	}
	if !owner.sink.ownsControlPublisher() || owner.sink.currentMessageSessionFact() != owner.session || owner.session.id != owner.sessionID {
		return false
	}
	rootProgress := owner.sink.turnPublisherSink().progress
	if rootProgress == nil {
		return false
	}
	rootProgress.mu.Lock()
	closed := rootProgress.closed
	rootProgress.mu.Unlock()
	return !closed
}

func (owner *transcriptOwner) IsCurrent() bool {
	owner.activity.mu.Lock()
	defer owner.activity.mu.Unlock()
	return owner.currentLocked()
}

func (publication *transcriptPublication) ReportProgress(update agent.ProgressUpdate) {
	owner := publication.owner
	owner.activity.mu.Lock()
	defer owner.activity.mu.Unlock()
	if owner.currentLocked() {
		owner.sink.progress.reportOwned(update, owner)
	}
}

func (owner *transcriptOwner) PublishSessionInfo(info map[string]interface{}) bool {
	encoded := make(map[string][]byte, len(info))
	for key, value := range info {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		encoded[key] = data
	}
	sink := owner.sink
	if owner.beforeSessionInfoAdmission != nil {
		owner.beforeSessionInfoAdmission()
	}
	// Turn completion and goal publication take activity admission before the session-info cache.
	// Use the same order so a pending progress update cannot block their completion.
	owner.activity.mu.Lock()
	if !owner.currentLocked() {
		owner.activity.mu.Unlock()
		return false
	}
	sink.sessionInfoMu.Lock()
	changed := make(map[string]interface{}, len(info))
	for key, data := range encoded {
		_, exempt := dedupExemptSessionInfoKeys[key]
		progress := key == contracts.SessionInfoKeyGenerationProgressRevision || key == contracts.SessionInfoKeyThinkingTokens || key == contracts.SessionInfoKeyOutputBytes || key == contracts.SessionInfoKeyOutputBytesMinimum
		if exempt || progress || !bytes.Equal(sink.lastSessionInfo[key], data) {
			changed[key] = json.RawMessage(data)
		}
	}
	event := sessionInfoEvent(sink.agentID, changed)
	if len(changed) == 0 || event == nil {
		sink.sessionInfoMu.Unlock()
		owner.activity.mu.Unlock()
		return false
	}
	if sink.lastSessionInfo == nil {
		sink.lastSessionInfo = make(map[string][]byte)
	}
	for key := range changed {
		_, exempt := dedupExemptSessionInfoKeys[key]
		progress := key == contracts.SessionInfoKeyGenerationProgressRevision || key == contracts.SessionInfoKeyThinkingTokens || key == contracts.SessionInfoKeyOutputBytes || key == contracts.SessionInfoKeyOutputBytesMinimum
		if !exempt && !progress {
			sink.lastSessionInfo[key] = encoded[key]
		}
	}
	sink.h.watcher.EnqueueAgentEvent(sink.agentID, event)
	sink.sessionInfoMu.Unlock()
	owner.activity.mu.Unlock()
	sink.h.watcher.DrainAgentEvents(sink.agentID)
	return true
}

// transcriptMutationLock keeps the original mutex as the only admission authority.
// writerAdmissions observes current and waiting writers for deterministic lifecycle tests.
type transcriptMutationLock struct {
	sync.RWMutex
	writerAdmissions atomic.Int64
}

func (lock *transcriptMutationLock) Lock() {
	lock.writerAdmissions.Add(1)
	lock.RWMutex.Lock()
}

func (lock *transcriptMutationLock) Unlock() {
	lock.RWMutex.Unlock()
	lock.writerAdmissions.Add(-1)
}

func (lock *transcriptMutationLock) TryLock() bool {
	lock.writerAdmissions.Add(1)
	if lock.RWMutex.TryLock() {
		return true
	}
	lock.writerAdmissions.Add(-1)
	return false
}

func (h *OutputHandler) transcriptMutationMutex(rootAgentID string) *transcriptMutationLock {
	value, _ := h.transcriptMu.LoadOrStore(rootAgentID, &transcriptMutationLock{})
	return value.(*transcriptMutationLock)
}

func readCapturedOwner(content agent.MessageContent) (*transcriptOwner, error) {
	if content.Publication == nil {
		return nil, nil
	}
	owner, valid := content.Publication.Owner().(*transcriptOwner)
	if !valid || owner == nil || owner.sink == nil || owner.activity == nil || owner.scope == nil || owner.session == nil || owner.thread == nil || owner.sessionID != content.AgentSessionID {
		return nil, errors.New("the captured transcript owner does not match its destination")
	}
	return owner, nil
}

func (sink *agentOutputSink) capturedOwner(content agent.MessageContent) (*transcriptOwner, error) {
	owner, err := readCapturedOwner(content)
	if err != nil {
		return nil, err
	}
	if owner != nil && owner.sink != sink {
		return nil, errors.New("the captured transcript owner does not match its destination")
	}
	return owner, nil
}

func (owner *transcriptOwner) enqueueMessage(message *leapmuxv1.AgentChatMessage) {
	owner.activity.mu.Lock()
	message.TranscriptOnly = message.TranscriptOnly || !owner.currentLocked()
	owner.sink.h.enqueueMessage(owner.sink.agentID, message)
	owner.activity.mu.Unlock()
}

func (owner *transcriptOwner) dropOutputTail(spanID string) {
	owner.activity.mu.Lock()
	defer owner.activity.mu.Unlock()
	if owner.currentLocked() {
		owner.sink.progress.dropTail(spanID)
	}
}

func (sink *agentOutputSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	content = content.Clone()
	if content.Publication != nil {
		return content
	}
	mutation := sink.h.transcriptMutationMutex(sink.rootAgentID)
	mutation.RLock()
	defer mutation.RUnlock()
	session := sink.currentMessageSessionFact()
	if content.AgentSessionID == "" {
		content.AgentSessionID = session.id
	}
	current := sink.registeredCurrent()
	var activity *agentActivity
	if current {
		activity = sink.h.activityFor(sink.agentID, sink.rootAgentID)
	} else {
		sink.childMu.Lock()
		if sink.historicalActivity == nil {
			sink.historicalActivity = &agentActivity{rootAgentID: sink.rootAgentID, scope: &activityScope{publisher: sink.turnPublisher()}}
		}
		activity = sink.historicalActivity
		sink.childMu.Unlock()
	}
	activity.mu.Lock()
	scope := activity.scope
	if current {
		scope = sink.h.scopeLocked(activity, sink.rootAgentID)
	}
	key := transcriptOwnerKey{sink: sink, session: session, sessionID: content.AgentSessionID}
	if scope.transcriptThreads == nil {
		scope.transcriptThreads = make(map[transcriptOwnerKey]*transcriptThread)
	}
	thread := scope.transcriptThreads[key]
	if thread == nil {
		thread = &transcriptThread{}
		scope.transcriptThreads[key] = thread
	}
	activity.mu.Unlock()
	owner := &transcriptOwner{sink: sink, activity: activity, scope: scope, session: session, sessionID: content.AgentSessionID,
		thread: thread}
	content.Publication = &transcriptPublication{owner: owner.withSpanSnapshot(sink.tracker, span)}
	return content
}

// withSpanSnapshot keeps immutable authority and replaces only span geometry.
func (owner *transcriptOwner) withSpanSnapshot(tracker *SpanTracker, span agent.SpanInfo) *transcriptOwner {
	next := *owner
	connector := resolveConnectorSpanID(span.SpanID, span.ConnectorSpanID, span.ParentSpanID, span.Closing)
	next.depth, next.spanLines, next.spanColor = tracker.Snapshot(span.ParentSpanID, connector, span.Closing)
	if span.SpanColor != 0 || span.NoSpan {
		next.spanColor = span.SpanColor
	}
	_, next.passthroughLines, _ = tracker.Snapshot("", "", false)
	next.span = span
	return &next
}
