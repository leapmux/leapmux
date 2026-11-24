package agenttest

import (
	"errors"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type sinkSessionObservationWriter struct {
	sink  *Sink
	owner *testTranscriptOwner
}

func (sink *Sink) SessionObservationWriterFor(captured agent.CapturedTranscript) (agent.CapturedSessionObservationWriter, error) {
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nil, errors.New("the captured session observation has no owner")
	}
	owner, ok := content.Publication.Owner().(*testTranscriptOwner)
	if !ok || owner == nil || owner.sink != sink || owner.key.session == nil ||
		content.AgentSessionID == "" || content.AgentSessionID != owner.key.session.id {
		return nil, errors.New("the captured session observation belongs to another source")
	}
	release := sink.lockNativeSessionSource()
	defer release()
	if !owner.currentNativeStateSourceLocked() {
		return nil, errors.New("the captured session observation expired")
	}
	writer := &sinkSessionObservationWriter{sink: sink, owner: owner}
	sink.sessionObservation = writer
	return writer, nil
}

func (writer *sinkSessionObservationWriter) currentLocked() bool {
	return writer.sink.sessionObservation == writer && writer.owner.currentNativeStateSourceLocked()
}

func (writer *sinkSessionObservationWriter) PersistSettingsRefresh(refresh optionmap.Map) {
	release := writer.sink.lockNativeSessionSource()
	defer release()
	if writer.currentLocked() {
		writer.sink.persistSettingsRefreshLocked(refresh)
	}
}

func (writer *sinkSessionObservationWriter) PublishSessionInfo(info map[string]interface{}) bool {
	release := writer.sink.lockNativeSessionSource()
	defer release()
	if !writer.currentLocked() {
		return false
	}
	writer.sink.sessionInfos = append(writer.sink.sessionInfos, info)
	return true
}

func (writer *sinkSessionObservationWriter) Retire() {
	writer.sink.mu.Lock()
	if writer.sink.sessionObservation == writer {
		writer.sink.sessionObservation = nil
	}
	writer.sink.mu.Unlock()
}

type nopSessionObservationWriter struct{}

func (nop) SessionObservationWriterFor(agent.CapturedTranscript) (agent.CapturedSessionObservationWriter, error) {
	return nopSessionObservationWriter{}, nil
}
func (nopSessionObservationWriter) PersistSettingsRefresh(optionmap.Map)           {}
func (nopSessionObservationWriter) PublishSessionInfo(map[string]interface{}) bool { return false }
func (nopSessionObservationWriter) Retire()                                        {}

type testSessionFact struct{ id string }
type testTranscriptScope struct{ active bool }
type testTranscriptKey struct {
	session   *testSessionFact
	sessionID string
	scope     *testTranscriptScope
}
type testTranscriptOwner struct {
	childLease       *testChildSinkLifetime
	activationLease  *testChildSinkLifetime
	historical       bool
	publisherSession *testSessionFact
	sink             *Sink
	key              testTranscriptKey
	spans            []SpanOpen
}

func (sink *Sink) CaptureMessage(content agent.MessageContent, _ agent.SpanInfo) agent.MessageContent {
	content = content.Clone()
	if content.Publication != nil {
		return content
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.captureMessageLocked(content)
}

func (sink *Sink) captureMessageLocked(content agent.MessageContent) agent.MessageContent {
	session := sink.sessionFact.Load()
	if session == nil {
		sink.sessionFact.CompareAndSwap(nil, &testSessionFact{})
		session = sink.sessionFact.Load()
	}
	publisher := sink.registry()
	publisherSession := publisher.sessionFact.Load()
	if publisherSession == nil {
		publisher.sessionFact.CompareAndSwap(nil, &testSessionFact{})
		publisherSession = publisher.sessionFact.Load()
	}
	if sink.transcriptScope == nil {
		sink.transcriptScope = &testTranscriptScope{}
	}
	if content.AgentSessionID == "" {
		content.AgentSessionID = session.id
	}
	key := testTranscriptKey{session: session, sessionID: content.AgentSessionID, scope: sink.transcriptScope}
	owner := &testTranscriptOwner{sink: sink, key: key, childLease: sink.childLifetime, publisherSession: publisherSession, spans: sink.liveSpansLocked()}
	content.Publication = owner
	return content
}

func (owner *testTranscriptOwner) outputProgressCurrentLocked() bool {
	return !owner.historical && owner.currentSessionLocked() &&
		owner.sink.childLifetime == owner.childLease &&
		(owner.activationLease == nil || owner.sink.childLifetime == owner.activationLease)
}

func (owner *testTranscriptOwner) currentLocked() bool {
	return owner.sink.transcriptScope == owner.key.scope && owner.currentSessionLocked() && (owner.activationLease == nil || owner.sink.childLifetime == owner.activationLease)
}

func (owner *testTranscriptOwner) currentSessionLocked() bool {
	return owner.key.session != nil && owner.sink.sessionFact.Load() == owner.key.session && owner.key.sessionID == owner.key.session.id
}

func (owner *testTranscriptOwner) currentNativeStateSourceLocked() bool {
	return owner.currentSessionLocked() && owner.publisherSession != nil && owner.sink.registry().sessionFact.Load() == owner.publisherSession
}

func (owner *testTranscriptOwner) Owner() agent.TranscriptOwner { return owner }

func (owner *testTranscriptOwner) IsCurrent() bool {
	owner.sink.mu.Lock()
	defer owner.sink.mu.Unlock()
	return owner.currentLocked()
}

func (owner *testTranscriptOwner) IsSessionCurrent() bool {
	owner.sink.mu.Lock()
	defer owner.sink.mu.Unlock()
	return owner.currentSessionLocked()
}

func (owner *testTranscriptOwner) PublishSessionInfo(info map[string]interface{}) bool {
	owner.sink.mu.Lock()
	defer owner.sink.mu.Unlock()
	if !owner.currentLocked() {
		return false
	}
	owner.sink.sessionInfos = append(owner.sink.sessionInfos, info)
	return true
}

func (owner *testTranscriptOwner) ReportProgress(update agent.ProgressUpdate) {
	owner.sink.mu.Lock()
	defer owner.sink.mu.Unlock()
	admitted := owner.currentLocked()
	if agent.IsOutputProgress(update.Operation) {
		admitted = owner.outputProgressCurrentLocked()
	}
	if !admitted {
		return
	}
	owner.sink.progress = append(owner.sink.progress, update)
	snapshot, changed := owner.sink.progressCount.Apply(update)
	if changed {
		owner.sink.sessionInfos = append(owner.sink.sessionInfos, map[string]interface{}{
			contracts.SessionInfoKeyThinkingTokens:     snapshot.ThinkingTokens,
			contracts.SessionInfoKeyOutputBytes:        snapshot.OutputBytes,
			contracts.SessionInfoKeyOutputBytesMinimum: snapshot.OutputBytesMinimum,
		})
	}
}

// GoalWriterFor builds the goal writer of one captured publication, mirroring
// the worker sink's factory: the capture freezes the session fact at
// observation, the writer refuses once a session replacement retires it, and
// ordinary later turns in the same session keep it valid. The write lands on
// the ROOT sink, whose registry the whole tree shares.
func (sink *Sink) GoalWriterFor(captured agent.CapturedTranscript) (agent.CapturedGoalWriter, error) {
	if !sink.ownsGoal() {
		return nil, errors.New("a child transcript owns no session goal")
	}
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nil, errors.New("the captured goal write has no publication owner")
	}
	owner, ok := content.Publication.Owner().(*testTranscriptOwner)
	if !ok || owner == nil {
		return nil, errors.New("the captured goal write has a foreign publication owner")
	}
	if owner.sink != sink {
		return nil, errors.New("the captured goal write belongs to another sink")
	}
	if content.AgentSessionID == "" || content.AgentSessionID != owner.key.sessionID {
		return nil, errors.New("the captured goal write's expected session does not match its native session fact")
	}
	return &sinkGoalWriter{root: sink.registry(), owner: owner}, nil
}

// sinkGoalWriter applies goal writes under one observation-time session fact.
type sinkGoalWriter struct {
	root  *Sink
	owner *testTranscriptOwner
}

func (writer *sinkGoalWriter) UpsertGoal(update agent.GoalUpdate) error {
	return writer.apply(&update)
}

func (writer *sinkGoalWriter) ClearGoal() error {
	return writer.apply(nil)
}

func (writer *sinkGoalWriter) apply(update *agent.GoalUpdate) error {
	writer.root.mu.Lock()
	defer writer.root.mu.Unlock()
	// Goals retain session authority across ordinary turn-scope changes.
	// Check and apply under the same mutex, as the worker's mutation lease does.
	if writer.owner.sink != writer.root || !writer.owner.currentSessionLocked() {
		return errors.New("the captured goal write's publication expired")
	}
	if update == nil {
		writer.root.clearGoalLocked(false)
	} else {
		writer.root.upsertGoalLocked(*update)
	}
	return nil
}

// lockNativeSessionSource keeps root adoption and child state commits in one
// order. Atomic session facts let ordinary child captures avoid reverse locks.
func (sink *Sink) lockNativeSessionSource() func() {
	root := sink.registry()
	root.mu.Lock()
	if root == sink {
		return root.mu.Unlock
	}
	sink.mu.Lock()
	return func() { sink.mu.Unlock(); root.mu.Unlock() }
}
