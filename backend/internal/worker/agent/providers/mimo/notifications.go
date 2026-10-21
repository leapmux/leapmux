package mimo

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// mimoQueuedNotification retains a notice until its destination is known or its writer accepts it.
// A held notice has no writer yet. An unattributed failure can still put an earlier divider in the main transcript.
// Captured writes retain their payload and destination across refused attempts.
type mimoQueuedNotification struct {
	content       agent.MessageContent
	write         agent.CapturedTranscript
	held          bool
	done          bool
	workerWritten bool
}

// mimoWorkerNotifications keeps retry resolutions on the worker reduction path, behind any held native notice.
type mimoWorkerNotifications struct {
	agent *Agent
	sink  agent.ProviderServices
	main  bool
}

func (sink mimoWorkerNotifications) PersistLeapMuxNotification(content map[string]interface{}) {
	raw, err := json.Marshal(content)
	if err != nil {
		slog.Error("encode a MiMo worker notification", "agent_id", sink.agent.AgentID(), "error", err)
		return
	}
	sink.agent.persistNotice(sink.sink, agent.MessageContent{Original: raw}, sink.main && sink.agent.rootHoldsNotifications(), true)
}

func writeMiMoNotification(write agent.CapturedTranscript, workerWritten bool) (bool, error) {
	if workerWritten {
		return write.PersistWorkerNotification()
	}
	return write.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
}

// queueNotification retains one observation in native arrival order.
func (a *Agent) queueNotification(content agent.MessageContent, write agent.CapturedTranscript, held, workerWritten bool) {
	a.notifMu.Lock()
	defer a.notifMu.Unlock()
	a.notificationQueue = append(a.notificationQueue, &mimoQueuedNotification{content: content, write: write, held: held, workerWritten: workerWritten})
}

// rootHoldsNotifications reports whether an unattributed failure can still belong to the main transcript.
// Until it identifies its message, a root notice could precede an earlier failure's divider.
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

// persistCapturedNotification writes a native notice or retains it until its destination settles.
// The shared output queue owns accepted writes, including failed storage attempts.
func (a *Agent) persistCapturedNotification(sink agent.ProviderServices, content agent.MessageContent, rootHeld bool) {
	a.persistNotice(sink, content, rootHeld, false)
}

func (a *Agent) persistNotice(sink agent.ProviderServices, content agent.MessageContent, rootHeld, workerWritten bool) {
	if rootHeld {
		// Keep the bytes until the earlier failure resolves its destination.
		a.queueNotification(content.Clone(), agent.CapturedTranscript{}, true, workerWritten)
		return
	}
	captured := agent.CaptureTranscript(sink, content, agent.SpanInfo{})
	if _, err := writeMiMoNotification(captured, workerWritten); !agent.OutputRetained(err) {
		slog.Error("mimo persist captured notification", "agent_id", a.AgentID(), "error", err)
		a.queueNotification(agent.MessageContent{}, captured, false, workerWritten)
	}
}

// flushCapturedNotifications submits retained notices in order and stops at a refused write.
// A held notice receives its owner here, after the earlier failure selected a child transcript or wrote its main divider.
// The caller serializes dispatch. The queue lock protects concurrent additions and releases completed captures.
func (a *Agent) flushCapturedNotifications() {
	for {
		a.notifMu.Lock()
		for len(a.notificationQueue) > 0 && a.notificationQueue[0].done {
			a.notificationQueue[0] = nil
			a.notificationQueue = a.notificationQueue[1:]
		}
		var next *mimoQueuedNotification
		if len(a.notificationQueue) > 0 {
			next = a.notificationQueue[0]
		} else {
			a.notificationQueue = nil
		}
		a.notifMu.Unlock()
		if next == nil {
			return
		}
		if next.held {
			captured := agent.CaptureTranscript(a.sink, next.content, agent.SpanInfo{})
			if _, err := writeMiMoNotification(captured, next.workerWritten); !agent.OutputRetained(err) {
				slog.Error("mimo persist held notification", "agent_id", a.AgentID(), "error", err)
				next.write = captured
				next.held = false
				return
			}
			next.done = true
			continue
		}
		if _, err := writeMiMoNotification(next.write, next.workerWritten); !agent.OutputRetained(err) {
			slog.Error("mimo persist retained notification", "agent_id", a.AgentID(), "error", err)
			return
		}
		next.done = true
	}
}
