package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen states the metadata of a session on the `_meta` of an agent message,
// most often an EMPTY one that carries nothing else: the token usage of each
// model call, the goal, the progress of a compaction, and the background work
// that finished. These are the keys of each.
const (
	qwenMetaUsage              = "usage"
	qwenMetaGoalState          = "goalState"
	qwenMetaContextCompression = "contextCompression"
	qwenMetaSource             = "source"
	qwenMetaBackgroundTask     = "backgroundTask"
)

// Qwen's words for the source of a message that it writes itself.
const (
	// qwenSourceBackgroundTaskCompleted marks the notice that a background
	// subagent or command finished.
	qwenSourceBackgroundTaskCompleted = "background_task_completed"
	// qwenSourceBackgroundNotification marks the same notice, repeated as the
	// first message of the turn that answers it.
	qwenSourceBackgroundNotification = "background_notification"
)

// handleSessionMetadata reads the `_meta` of one session update. It answers
// true for an update that it consumed: a notice that the registry or a status
// line of the transcript states in its own way.
func (a *Agent) handleSessionMetadata(_ string, metadata map[string]json.RawMessage, _ json.RawMessage) bool {
	if raw, ok := metadata[qwenMetaUsage]; ok {
		a.broadcastUsage(raw)
	}
	if raw, ok := metadata[qwenMetaGoalState]; ok {
		a.handleGoalState(raw)
	}
	if raw, ok := metadata[qwenMetaContextCompression]; ok {
		a.reportCompression(raw)
		return true
	}
	var source string
	_ = json.Unmarshal(metadata[qwenMetaSource], &source)
	switch source {
	case qwenSourceBackgroundTaskCompleted:
		// The row of a background subagent or command states the end. Every
		// other notice -- a monitor, a workflow run, work that no row tracks --
		// becomes a status line, so the reader sees what ended and why the turn
		// that answers it starts.
		task := a.readBackgroundTask(metadata)
		if !a.finishBackgroundTask(task) {
			a.reportStatus(backgroundNoticeText(task))
		}
		return true
	case qwenSourceBackgroundNotification:
		// The same notice again, as the first message of the turn that answers
		// it, and the transcript holds the first one already. Only the overflow
		// of Qwen's notification queue is stated here and nowhere else.
		if task := a.readBackgroundTask(metadata); task.Kind == qwenBackgroundQueue {
			a.reportStatus(queueOverflowText(task.Status))
		}
		return true
	}
	return false
}

// readBackgroundTask reads the background task that a notice concerns. An
// unreadable one yields the zero task, which no row tracks.
func (a *Agent) readBackgroundTask(metadata map[string]json.RawMessage) qwenBackgroundTask {
	var task qwenBackgroundTask
	if err := json.Unmarshal(metadata[qwenMetaBackgroundTask], &task); err != nil {
		slog.Warn("qwen background task notice unreadable", "agent_id", a.AgentID(), "error", err)
		return qwenBackgroundTask{}
	}
	return task
}

// backgroundNoticeText words the notice about one background task, from the
// structured fields of the notice, as reportCompression words a compaction.
// Qwen's own words for the notice are out of reach: the hook that reads it
// (Hooks.SessionMetadataHandler) receives the `_meta` of the update and not its
// content.
func backgroundNoticeText(task qwenBackgroundTask) string {
	noun := "Background task"
	switch task.Kind {
	case qwenBackgroundAgent:
		noun = "Background agent"
	case qwenBackgroundShell:
		noun = "Background command"
	case qwenBackgroundMonitor:
		noun = "Background monitor"
	case qwenBackgroundWorkflow:
		noun = "Background workflow"
	}
	subject := strings.TrimSpace(task.Description)
	if subject == "" {
		subject = strings.TrimSpace(task.CommandLabel)
	}
	if subject != "" {
		subject = `"` + subject + `"`
	} else {
		subject = strings.TrimSpace(task.TaskID)
	}
	var verb string
	switch task.Status {
	case "completed":
		verb = "completed"
	case "failed":
		verb = "failed"
	case "cancelled":
		verb = "was stopped"
	default:
		verb = "ended"
	}
	parts := []string{noun}
	if subject != "" {
		parts = append(parts, subject)
	}
	return strings.Join(append(parts, verb), " ")
}

// queueOverflowText words Qwen's notice that its notification queue overflowed.
// `recorded` states that Qwen kept the results in the session although it did
// not announce them; any other status states that it dropped them.
func queueOverflowText(status string) string {
	if status == "recorded" {
		return "Qwen Code recorded background results but did not deliver them live, because its notification queue was full"
	}
	return "Qwen Code dropped background notifications, because its notification queue was full"
}

// reportStatus states one line in the transcript, in the shape that every
// provider's status line takes there.
func (a *Agent) reportStatus(text string) {
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: text,
	})
}

// qwenUsage is the token usage of one model call.
type qwenUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CachedReadTokens int64 `json:"cachedReadTokens"`
}

// broadcastUsage broadcasts the token counts of one model call. Qwen counts the
// cached tokens inside the input tokens, so the input count states the rest.
func (a *Agent) broadcastUsage(raw json.RawMessage) {
	var usage qwenUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		slog.Debug("qwen usage unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	input := usage.InputTokens - usage.CachedReadTokens
	if input < 0 {
		input = 0
	}
	a.Sink().BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyContextUsage: providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
			Input:     input,
			CacheRead: usage.CachedReadTokens,
			Output:    usage.OutputTokens,
		}),
	})
}

// qwenCompression is the progress of a compaction.
type qwenCompression struct {
	Phase              string `json:"phase"`
	OriginalTokenCount int64  `json:"originalTokenCount"`
	NewTokenCount      int64  `json:"newTokenCount"`
	Warning            string `json:"warning"`
}

// reportCompression states a compaction in the transcript, in the words that
// every provider's compaction takes there.
func (a *Agent) reportCompression(raw json.RawMessage) {
	var compression qwenCompression
	if err := json.Unmarshal(raw, &compression); err != nil {
		slog.Debug("qwen compression progress unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	var text string
	switch compression.Phase {
	case "progress":
		text = "Compacting the context"
	case "done":
		text = fmt.Sprintf("Context compacted from %d to %d tokens", compression.OriginalTokenCount, compression.NewTokenCount)
		if compression.Warning != "" {
			text += ": " + compression.Warning
		}
	default:
		return
	}
	a.reportStatus(text)
}
