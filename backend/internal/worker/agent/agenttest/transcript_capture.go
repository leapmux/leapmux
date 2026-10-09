package agenttest

import (
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
