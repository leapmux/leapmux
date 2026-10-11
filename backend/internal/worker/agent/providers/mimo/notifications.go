package mimo

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// mimoQueuedNotification is one native observation retained for a later
// write. A FAILED write keeps its captured write, so a retry repeats no side
// effect and never rewrites a row that succeeded. A HELD entry has not been
// attempted at all: the root destination is unsettled while a native failure
// can still claim the main transcript, and an observation written before its
// neighbor's destination resolves would order itself ahead of the divider
// that belongs there instead.
type mimoQueuedNotification struct {
	content agent.MessageContent
	write   agent.CapturedTranscript
	held    bool
	done    bool
}

// queueNotification retains one observation in native arrival order.
func (a *Agent) queueNotification(content agent.MessageContent, write agent.CapturedTranscript, held bool) {
	a.notifMu.Lock()
	defer a.notifMu.Unlock()
	a.notificationQueue = append(a.notificationQueue, &mimoQueuedNotification{content: content, write: write, held: held})
}

// rootHoldsNotifications reports whether a native failure without an
// attributed owner can still claim the main transcript: until the failure
// names its message, a root notification written now could precede the
// divider that the same failure later puts in front of it.
func (a *Agent) rootHoldsNotifications() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, failure := range a.pendingFailures {
		if failure.actor == nil && !failure.main && !failure.notificationReady {
			return true
		}
	}
	return false
}

// persistCapturedNotification writes one native observation to its exact
// destination now, and retains it for an ordered retry when the write fails
// or the destination itself is still unsettled.
func (a *Agent) persistCapturedNotification(sink agent.ProviderServices, content agent.MessageContent, rootHeld bool) {
	if rootHeld {
		// The destination is the main transcript only until the pending
		// failure resolves; hold the bytes and attempt them after it does.
		a.queueNotification(content.Clone(), agent.CapturedTranscript{}, true)
		return
	}
	captured := agent.CaptureTranscript(sink, content, agent.SpanInfo{})
	if _, err := captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
		slog.Error("mimo persist captured notification", "agent_id", a.AgentID(), "error", err)
		a.queueNotification(agent.MessageContent{}, captured, false)
	}
}

// flushCapturedNotifications retries the retained observations in their
// native order. The first entry that still fails stops the walk, so a later
// observation can never write itself ahead of an earlier one that has not
// landed. A held entry resolves its destination at flush time: the pending
// failure that held it has either claimed the child transcript or written
// its divider to the main one before this walk runs.
func (a *Agent) flushCapturedNotifications() {
	for {
		a.notifMu.Lock()
		var next *mimoQueuedNotification
		for _, queued := range a.notificationQueue {
			if !queued.done {
				next = queued
				break
			}
		}
		a.notifMu.Unlock()
		if next == nil {
			return
		}
		if next.held {
			captured := agent.CaptureTranscript(a.sink, next.content, agent.SpanInfo{})
			if _, err := captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
				slog.Error("mimo persist held notification", "agent_id", a.AgentID(), "error", err)
				next.write = captured
				next.held = false
				return
			}
			next.done = true
			continue
		}
		if _, err := next.write.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
			slog.Error("mimo persist retained notification", "agent_id", a.AgentID(), "error", err)
			return
		}
		next.done = true
	}
}
