package qwen

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen often puts session metadata on an empty agent-message chunk.
// Only the worker reads these keys:
//   - Token usage for each model call.
//   - The session goal.
//   - Completed background work.
//   - The lifecycle of each model request.
//
// Shared compaction metadata uses contract constants.
const (
	qwenMetaUsage              = "usage"
	qwenMetaGoalState          = "goalState"
	qwenMetaBackgroundTask     = "backgroundTask"
	qwenMetaExecutionLifecycle = "executionLifecycle"
)

// handleSessionMetadata receives the complete update and its metadata.
// It consumes known notices and lifecycle frames. Other updates continue through the shared conversation.
func (a *Agent) handleSessionMetadata(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if raw, ok := metadata[qwenMetaUsage]; ok {
		a.broadcastUsage(raw)
	}
	if raw, ok := metadata[qwenMetaGoalState]; ok {
		a.handleGoalState(raw)
	}
	if raw, ok := metadata[qwenMetaExecutionLifecycle]; ok {
		a.handleExecutionLifecycle(raw)
		return true
	}
	if raw, ok := metadata[contracts.QwenMetaContextCompression]; ok {
		a.reportCompression(raw, update)
		return true
	}
	var source string
	_ = json.Unmarshal(metadata[contracts.QwenMetaSource], &source)
	switch source {
	case contracts.QwenMessageSourceBackgroundTaskCompleted:
		// A tracked background task states its end in its registry row.
		// An untracked task gets a status line that explains why the next turn starts.
		task := a.readBackgroundTask(metadata)
		if !a.finishBackgroundTask(task) {
			a.reportStatusAfterOutput(backgroundNoticeText(task))
		}
		return true
	case contracts.QwenMessageSourceBackgroundNotification:
		// The first message of a response turn repeats the previous completion notice.
		// Only a queue overflow supplies new information here.
		if task := a.readBackgroundTask(metadata); task.Kind == qwenBackgroundQueue {
			a.reportStatusAfterOutput(queueOverflowText(task.Status))
		}
		return true
	}
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	text := ""
	if json.Unmarshal(update, &chunk) == nil {
		text = chunk.Content.Text
	}
	hook := providerkit.RawPresent(metadata[contracts.QwenMetaStopHookLoop])
	if updateType == contracts.ACPUpdateAgentMessageChunk &&
		(hook || (qwenDiscreteMessage(metadata) && source != contracts.QwenMessageSourceBackgroundResponse)) {
		if !hook && strings.TrimSpace(text) == "" {
			return true
		}
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		a.persistNotification(update)
		return true
	}
	switch updateType {
	case contracts.ACPUpdateToolCall:
		a.retryResolution.Resolve(a.Sink())
	case contracts.ACPUpdateAgentMessageChunk, contracts.ACPUpdateAgentThoughtChunk:
		if text != "" {
			// Resolve before the base can flush reasoning or persist a tool row.
			a.retryResolution.Resolve(a.Sink())
			if updateType == contracts.ACPUpdateAgentMessageChunk {
				a.stateMu.Lock()
				a.requestHasText = true
				a.stateMu.Unlock()
			}
		}
	}
	return false
}

func (a *Agent) handleExecutionLifecycle(raw json.RawMessage) {
	var lifecycle struct {
		Kind       string `json:"kind"`
		Phase      string `json:"phase"`
		Outcome    string `json:"outcome"`
		SessionID  string `json:"sessionId"`
		SubagentID string `json:"subagentId"`
	}
	if err := json.Unmarshal(raw, &lifecycle); err != nil {
		slog.Debug("decode Qwen execution lifecycle", "agent_id", a.AgentID(), "error", err)
		return
	}
	if lifecycle.Kind != "request" || lifecycle.SubagentID != "" ||
		(lifecycle.SessionID != "" && !a.IsCurrentSession(lifecycle.SessionID)) {
		return
	}
	switch lifecycle.Phase {
	case "started":
		a.stateMu.Lock()
		a.requestHasText = false
		a.stateMu.Unlock()
	case "ended":
		switch lifecycle.Outcome {
		case "error":
			a.stateMu.Lock()
			a.retryCount = agent.SaturatingAdd(a.retryCount, 1)
			count := a.retryCount
			hasText := a.requestHasText
			a.stateMu.Unlock()
			if !hasText {
				a.DiscardBufferedOutput(agent.AssembledMessageKindReasoning)
			}
			a.Sink().PersistLeapMuxNotification(map[string]any{
				contracts.NotificationFieldType:  contracts.NotificationTypeApiRetry,
				contracts.NotificationFieldCount: count,
			})
			a.retryResolution.NoteRetry()
		case "success":
			a.retryResolution.Resolve(a.Sink())
			a.resetRequestRetries()
		case "cancelled", "interrupted":
			// Qwen's RequestLifecycleOutcome includes cancellation; it does not prove a successful retry.
			a.resetRequestRetries()
		}
	}
}

func (a *Agent) resetRequestRetries() {
	a.stateMu.Lock()
	a.retryCount = 0
	a.requestHasText = false
	a.stateMu.Unlock()
	a.retryResolution.Reset()
}

// The refusal detail comes from qwen/logs/r2-compress-1.log.
const qwenCompressNotBeneficial = "Compression was not beneficial for this history size."

func (a *Agent) handlePromptEnded(err error, stopped bool) acp.PromptEnding {
	a.resetRequestRetries()
	a.stateMu.Lock()
	manual := a.compressPending != nil
	a.compressPending = nil
	a.stateMu.Unlock()
	// A stopped or failed prompt can end compaction without a native completion frame.
	providerkit.EndCompaction(a.Sink())
	var responseError *providerkit.JSONRPCResponseError
	if manual && !stopped && errors.As(err, &responseError) && responseError.Code == -32603 &&
		providerkit.StringField(responseError.Data, "details") == qwenCompressNotBeneficial {
		a.reportStatus("Qwen Code did not compact the context: the compression was not beneficial for this history size.")
		return acp.PromptEnding{NoteWritten: true}
	}
	return acp.PromptEnding{}
}

func qwenDiscreteMessage(metadata map[string]json.RawMessage) bool {
	var discrete bool
	return json.Unmarshal(metadata[contracts.QwenMetaDiscreteMessage], &discrete) == nil && discrete
}

// chunkMessageID keeps each discrete model response separate from the surrounding reply.
func (a *Agent) chunkMessageID(kind agent.AssembledMessageKind, metadata map[string]json.RawMessage) string {
	if kind != agent.AssembledMessageKindText {
		return ""
	}
	if !qwenDiscreteMessage(metadata) {
		return "qwen:reply"
	}
	a.stateMu.Lock()
	a.discreteMessageID++
	id := a.discreteMessageID
	a.stateMu.Unlock()
	return fmt.Sprintf("qwen:discrete:%d", id)
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

// backgroundNoticeText describes one background task from its structured fields.
// The metadata hook receives the whole update. This reader uses the task fields for a consistent status line.
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

// queueOverflowText describes a full native notification queue.
// A recorded status means Qwen kept the results without a live notice.
// Other statuses mean Qwen dropped the results.
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

// reportStatusAfterOutput keeps a mid-turn notice after the model output that preceded it.
func (a *Agent) reportStatusAfterOutput(text string) {
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.reportStatus(text)
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

// reportCompression publishes a live start and preserves the agent's native end frame.
func (a *Agent) reportCompression(raw, update json.RawMessage) {
	var compression struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &compression); err != nil {
		slog.Debug("qwen compression progress unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	switch compression.Phase {
	case contracts.QwenCompressionPhaseProgress:
		providerkit.PublishCompaction(a.Sink(), "")
	case contracts.QwenCompressionPhaseDone:
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		a.persistNotification(update)
		providerkit.EndCompaction(a.Sink())
	default:
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		if err := a.Sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}, agent.SpanInfo{}); err != nil {
			slog.Error("persist unknown Qwen compaction", "agent_id", a.AgentID(), "error", err)
		}
	}
}

func (a *Agent) persistNotification(update json.RawMessage) {
	if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}); err != nil {
		slog.Error("persist Qwen notice", "agent_id", a.AgentID(), "error", err)
	}
}
