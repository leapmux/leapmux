package agenttest

import (
	"errors"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type PlanUpdateRecord struct {
	Content     []byte
	Compression leapmuxv1.ContentCompression
	Title       string
}
type sinkPlanWriter struct {
	sink  *Sink
	owner *testTranscriptOwner
}

func (sink *Sink) PlanWriterFor(captured agent.CapturedTranscript) (agent.CapturedPlanWriter, error) {
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nil, errors.New("the captured plan has no publication owner")
	}
	owner, ok := content.Publication.Owner().(*testTranscriptOwner)
	if !ok || owner == nil || owner.sink != sink || owner.key.session == nil || content.AgentSessionID != owner.key.sessionID || content.AgentSessionID != owner.key.session.id {
		return nil, errors.New("the captured plan belongs to another source")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !owner.currentSessionLocked() {
		return nil, errors.New("the captured plan expired")
	}
	return &sinkPlanWriter{sink: sink, owner: owner}, nil
}

func (writer *sinkPlanWriter) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) error {
	sink := writer.sink
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !writer.owner.currentSessionLocked() {
		return errors.New("the captured plan expired")
	}
	sink.updatePlanLocked(content, compression, title)
	return nil
}

func (sink *Sink) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.updatePlanLocked(content, compression, title)
}
func (sink *Sink) updatePlanLocked(content []byte, compression leapmuxv1.ContentCompression, title string) {
	sink.planUpdates = append(sink.planUpdates, PlanUpdateRecord{Content: slices.Clone(content), Compression: compression, Title: title})
}
func (sink *Sink) PlanUpdateCount() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return len(sink.planUpdates)
}
func (sink *Sink) LastPlanUpdate() PlanUpdateRecord {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.planUpdates) == 0 {
		return PlanUpdateRecord{}
	}
	record := sink.planUpdates[len(sink.planUpdates)-1]
	record.Content = slices.Clone(record.Content)
	return record
}

type nopPlanWriter struct{}

func (nop) PlanWriterFor(agent.CapturedTranscript) (agent.CapturedPlanWriter, error) {
	return nopPlanWriter{}, nil
}
func (nopPlanWriter) UpdatePlan([]byte, leapmuxv1.ContentCompression, string) error { return nil }
