package agenttest

import (
	"errors"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type testSessionFact struct{ id string }
type testTranscriptScope struct{ active bool }
type testTranscriptKey struct {
	session   *testSessionFact
	sessionID string
	scope     *testTranscriptScope
}
type testTranscriptOwner struct {
	sink  *Sink
	key   testTranscriptKey
	spans []SpanOpen
}

func (sink *Sink) CaptureMessage(content agent.MessageContent, _ agent.SpanInfo) agent.MessageContent {
	content = content.Clone()
	if content.Publication != nil {
		return content
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.sessionFact == nil {
		sink.sessionFact = &testSessionFact{id: sink.currentSessionIDLocked()}
	}
	if sink.transcriptScope == nil {
		sink.transcriptScope = &testTranscriptScope{}
	}
	if content.AgentSessionID == "" {
		content.AgentSessionID = sink.sessionFact.id
	}
	key := testTranscriptKey{session: sink.sessionFact, sessionID: content.AgentSessionID, scope: sink.transcriptScope}
	owner := &testTranscriptOwner{sink: sink, key: key, spans: sink.liveSpansLocked()}
	content.Publication = owner
	return content
}

func (owner *testTranscriptOwner) currentLocked() bool {
	return owner.sink.transcriptScope == owner.key.scope && owner.sink.sessionFact == owner.key.session && owner.key.sessionID == owner.key.session.id
}

func (owner *testTranscriptOwner) Owner() agent.TranscriptOwner { return owner }

func (owner *testTranscriptOwner) IsCurrent() bool {
	owner.sink.mu.Lock()
	defer owner.sink.mu.Unlock()
	return owner.currentLocked()
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
	if !owner.currentLocked() {
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
	if !writer.owner.IsCurrent() {
		return errors.New("the captured goal write's publication expired")
	}
	writer.root.UpsertGoal(update)
	return nil
}

func (writer *sinkGoalWriter) ClearGoal() error {
	if !writer.owner.IsCurrent() {
		return errors.New("the captured goal write's publication expired")
	}
	writer.root.ClearGoal(false)
	return nil
}
