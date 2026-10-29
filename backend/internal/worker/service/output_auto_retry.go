package service

import (
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// capturedAutoRetryWriter keeps the provider owner observed before an output
// queue delays a retry operation. Queue admission validates that same owner.
type capturedAutoRetryWriter struct{ owner *transcriptOwner }

func (s *agentOutputSink) CaptureAutoRetry() agent.AutoRetryWriter {
	content := s.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	return &capturedAutoRetryWriter{owner: content.Publication.Owner().(*transcriptOwner)}
}

func (writer *capturedAutoRetryWriter) Acquire() agent.AutoRetryLease {
	owner := writer.owner
	mutation := owner.sink.h.transcriptMutationMutex(owner.sink.rootAgentID)
	mutation.RLock()
	owner.activity.mu.Lock()
	current := owner.currentLocked()
	owner.activity.mu.Unlock()
	if !current {
		mutation.RUnlock()
		return nil
	}
	return &providerMutationLease{mutation: mutation}
}

func (writer *capturedAutoRetryWriter) ScheduleUsageLimitRetry(resetAt time.Time, sourcePayload []byte) {
	sink := writer.owner.sink
	sink.h.scheduleAutoRetryRequest(sink.agentID, agent.AutoRetrySchedule{
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		ResetAt: resetAt, SourcePayload: sourcePayload, Admission: writer,
	})
}

func (writer *capturedAutoRetryWriter) ScheduleServerErrorRetry(sourcePayload []byte) {
	sink := writer.owner.sink
	sink.h.scheduleAutoRetryRequest(sink.agentID, agent.AutoRetrySchedule{
		Kind:          leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		SourcePayload: sourcePayload, Admission: writer,
	})
}

func (writer *capturedAutoRetryWriter) CancelUsageLimitRetry(reason string) {
	sink := writer.owner.sink
	sink.h.cancelAutoRetryKind(sink.agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, reason, writer)
}

func (writer *capturedAutoRetryWriter) CancelServerErrorRetry(reason string) {
	sink := writer.owner.sink
	sink.h.cancelAutoRetryKind(sink.agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, reason, writer)
}
