package service

import (
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// capturedAutoRetryWriter keeps the provider owner observed before an output
// queue delays a retry operation. Queue admission validates that same owner.
type capturedAutoRetryWriter struct {
	owner         *transcriptOwner
	completionSeq uint64
}

func (s *agentOutputSink) CaptureAutoRetry() agent.AutoRetryWriter {
	return s.CaptureAutoRetryThroughTurn(0)
}

func (s *agentOutputSink) CaptureAutoRetryOperation(completionSeq uint64, apply func(agent.AutoRetryWriter)) func() error {
	if apply == nil {
		return nil
	}
	writer := s.CaptureAutoRetryThroughTurn(completionSeq)
	return func() error { apply(writer); return nil }
}

func (s *agentOutputSink) CaptureAutoRetryThroughTurn(completionSeq uint64) agent.AutoRetryWriter {
	content := s.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	return &capturedAutoRetryWriter{owner: content.Publication.Owner().(*transcriptOwner), completionSeq: completionSeq}
}

func (writer *capturedAutoRetryWriter) currentLocked() bool {
	owner := writer.owner
	if !owner.sessionCurrentLocked() {
		return false
	}
	activity := owner.activity
	if writer.completionSeq != 0 && activity.turnSeq > writer.completionSeq {
		return false
	}
	if activity.scope == owner.scope {
		return true
	}
	return writer.completionSeq != 0 && !activity.turnActive && activity.turnSeq == writer.completionSeq &&
		activity.scope != nil && activity.scope.previousScope == owner.scope
}

func (writer *capturedAutoRetryWriter) Acquire() agent.AutoRetryLease {
	return acquireAutoRetryLease(writer.owner, writer.currentLocked)
}

// capturedAutoRetryKeyAdmission permits cleanup across turns within the same
// provider instance and native session. The durable key selects its one row.
type capturedAutoRetryKeyAdmission struct{ owner *transcriptOwner }

func (admission *capturedAutoRetryKeyAdmission) Acquire() agent.AutoRetryLease {
	return acquireAutoRetryLease(admission.owner, admission.owner.sessionCurrentLocked)
}

func acquireAutoRetryLease(owner *transcriptOwner, currentLocked func() bool) agent.AutoRetryLease {
	mutation := owner.sink.h.transcriptMutationMutex(owner.sink.rootAgentID)
	mutation.RLock()
	owner.activity.mu.Lock()
	current := currentLocked()
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

func (writer *capturedAutoRetryWriter) ScheduleUsageLimitRetryWithKey(key string, resetAt time.Time, sourcePayload []byte) {
	sink := writer.owner.sink
	sink.h.scheduleAutoRetryRequest(sink.agentID, agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		Key:  key, ResetAt: resetAt, SourcePayload: slices.Clone(sourcePayload), Admission: writer,
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

func (writer *capturedAutoRetryWriter) CancelUsageLimitRetryWithKey(key, reason string) {
	sink := writer.owner.sink
	sink.h.cancelUsageRetryWithKey(sink.agentID, key, reason, &capturedAutoRetryKeyAdmission{owner: writer.owner})
}

func (writer *capturedAutoRetryWriter) CancelServerErrorRetry(reason string) {
	sink := writer.owner.sink
	sink.h.cancelAutoRetryKind(sink.agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, reason, writer)
}
