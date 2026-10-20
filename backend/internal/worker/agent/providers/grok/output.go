package grok

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// turnState follows which prompt Grok runs now. Guarded by Agent.stateMu.
//
// Grok starts turns by itself: a background subagent or command that finished
// wakes the parent, a goal runs its next round, and a steer that reached an
// idle session becomes a front-of-queue prompt. None of them has a
// session/prompt, so no JSON-RPC response ends them. The queue notification
// states that a prompt started, with its id, and `turn_completed` states that
// it ended -- the only end such a turn has.
//
// LeapMux states the id of each prompt that it sends (adjustPromptParams), so a
// running prompt whose id it did not state is one that Grok started.
type turnState struct {
	// running is the prompt id Grok reported running last, in the main session.
	running string
	// own holds the ids of LeapMux's prompts that did not complete yet.
	own map[string]bool
	// agentTurn is the prompt id of the turn that GROK started, while it runs
	// or waits for the end of LeapMux's prompt before it.
	agentTurn string
}

// grokPromptIDKey is the `_meta` key of a prompt's id, which Grok takes from
// the client when the client states one.
const grokPromptIDKey = "promptId"

// adjustPromptParams states the id of a prompt that LeapMux sends.
func (a *Agent) adjustPromptParams(params map[string]any) {
	id := uuid.NewString()
	a.stateMu.Lock()
	if a.turns.own == nil {
		a.turns.own = make(map[string]bool)
	}
	a.turns.own[id] = true
	a.stateMu.Unlock()
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[grokPromptIDKey] = id
	params["_meta"] = meta
}

// handleQueueChanged reads the start of a turn. A prompt whose id LeapMux did
// not state is a turn the agent started by itself, so the base opens it: the
// reader sees the agent working, and the worker queues input behind it rather
// than starting a second turn inside it.
func (a *Agent) handleQueueChanged(params json.RawMessage) {
	var queue struct {
		SessionID       string `json:"sessionId"`
		RunningPromptID string `json:"runningPromptId"`
	}
	if err := json.Unmarshal(params, &queue); err != nil {
		slog.Warn("grok queue notification unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if queue.RunningPromptID == "" || !a.IsCurrentSession(queue.SessionID) {
		return
	}
	a.stateMu.Lock()
	if queue.RunningPromptID == a.turns.running {
		a.stateMu.Unlock()
		return
	}
	// A new prompt runs, so the one before it is over whatever reported it.
	delete(a.turns.own, a.turns.running)
	a.turns.running = queue.RunningPromptID
	own := a.turns.own[queue.RunningPromptID]
	a.stateMu.Unlock()
	if own {
		// A prompt of LeapMux's. Its own response ends the turn.
		return
	}
	// Begin outside the lock: it publishes the turn state, which broadcasts.
	if !a.BeginAgentTurn() {
		return
	}
	a.stateMu.Lock()
	a.turns.agentTurn = queue.RunningPromptID
	a.stateMu.Unlock()
}

// handleTurnCompleted reads the end of a turn. In the main session it ends a
// turn that Grok started; the response of a prompt LeapMux sent ends that one.
// In a child session it ends the turn of a subagent, whose text then reaches
// its transcript.
func (a *Agent) handleTurnCompleted(notification grokNotification, raw []byte, main bool) {
	var completed struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(notification.Update, &completed); err != nil {
		slog.Warn("grok turn_completed unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !main {
		if rowKey := a.childRowForSession(notification.SessionID); rowKey != "" {
			a.FinishChildTurn(rowKey)
		}
		return
	}
	a.stateMu.Lock()
	delete(a.turns.own, completed.PromptID)
	agentTurn := a.turns.agentTurn
	if agentTurn != "" && (completed.PromptID == "" || completed.PromptID == agentTurn) {
		a.turns.agentTurn = ""
	} else {
		agentTurn = ""
	}
	a.stateMu.Unlock()
	if agentTurn == "" {
		return
	}
	// The agent's own frame is the turn-end row. The browser plugin reads its
	// stop reason into the divider, as it reads the prompt response of a turn
	// LeapMux started.
	a.EndAgentTurn(json.RawMessage(raw))
}

// handleResponseCompleted broadcasts the token counts of one model call. The
// input, the cached input and the output of the LAST call are the context that
// the conversation fills now, so the browser derives the fill from them.
func (a *Agent) handleResponseCompleted(update json.RawMessage) {
	var completed struct {
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(update, &completed) != nil || completed.Usage == nil {
		return
	}
	usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:      completed.Usage.InputTokens,
		CacheWrite: completed.Usage.CacheCreationInputTokens,
		CacheRead:  completed.Usage.CacheReadInputTokens,
		Output:     completed.Usage.OutputTokens,
	})
	a.Sink().BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
}

// reportCompaction reads one automatic compaction frame. The start is live
// state only: the running indicator draws it, and no row spins until an end
// arrives. Each end closes the buffered text, ends the live state, and stores
// its native frame as the boundary the reader sees.
func (a *Agent) reportCompaction(updateType string, update json.RawMessage, raw []byte) {
	switch updateType {
	case grokUpdateAutoCompactStarted:
		providerkit.PublishCompaction(a.Sink(), "auto")
	case contracts.GrokNotificationAutoCompactCompleted,
		contracts.GrokNotificationAutoCompactFailed,
		contracts.GrokNotificationAutoCompactCancelled:
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		sessionID := a.CurrentSessionID()
		if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: sessionID}); err != nil {
			slog.Error("grok persist compaction frame", "agent_id", a.AgentID(), "error", err)
		}
		providerkit.EndCompaction(a.Sink())
	default:
	}
}

// reportRetry reads a model request that Grok retries. The turn looks idle
// while Grok waits out its backoff, so the native frame itself becomes the
// retry row the reader watches. The failed attempt's buffered text is
// discarded, and the first genuine model output after the retry resolves the
// row (observeRetryContent).
//
// Only a retry in progress is stated. An exhausted or failed retry ends the
// turn with an error, which the turn's own end reports.
func (a *Agent) reportRetry(update json.RawMessage, raw []byte) {
	var retry struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(update, &retry); err != nil || retry.Type != "retrying" {
		return
	}
	a.DiscardBufferedOutput()
	sessionID := a.CurrentSessionID()
	if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: sessionID}); err != nil {
		slog.Error("grok persist retry frame", "agent_id", a.AgentID(), "error", err)
	}
	a.retryResolution.NoteRetry()
}

// grokStreamStartMeta names the field of a chunk's notification metadata that
// states the start time of the model request that produced it. Every request
// of a turn has its own value, and a retry starts a new request.
const grokStreamStartMeta = "streamStartMs"

// observeNotificationMetadata drops the buffered text of a superseded model
// request. Grok can flush the partial text of a failed attempt only after it
// sent its retry_state frame, so the discard that the frame itself runs can
// meet an empty buffer while the partial text still arrives. The first chunk
// of the NEXT request closes that window: its stream start differs, and
// whatever the previous request left buffered is then the failed attempt's
// text, which the retried request does not see either. It also resolves a
// pending retry row, because the chunk that reaches this boundary is genuine
// model output.
//
// Only a message chunk names a stream: an available-commands update repeats
// the field with no request behind it, and it must not drop any text.
func (a *Agent) observeNotificationMetadata(updateType string, metadata map[string]json.RawMessage) {
	if updateType != contracts.ACPUpdateAgentMessageChunk && updateType != contracts.ACPUpdateAgentThoughtChunk {
		return
	}
	stream, present := providerkit.RawInt64(metadata[grokStreamStartMeta])
	if !present {
		return
	}
	a.stateMu.Lock()
	previous := a.chunkStreamStart
	a.chunkStreamStart = stream
	a.stateMu.Unlock()
	if previous != stream {
		a.DiscardBufferedOutput()
		a.retryResolution.Resolve(a.Sink())
	}
}

// observeRetryContent resolves a pending retry row at the first genuine model
// output of the attempt that follows it: text, a thought, or a tool call. It
// claims no update.
func (a *Agent) observeRetryContent(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	if !a.IsCurrentSession(sessionID) {
		return false
	}
	var header struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &header) != nil {
		return false
	}
	switch header.SessionUpdate {
	case contracts.ACPUpdateAgentMessageChunk, contracts.ACPUpdateAgentThoughtChunk:
		if strings.TrimSpace(header.Content.Text) != "" {
			a.retryResolution.Resolve(a.Sink())
		}
	case contracts.ACPUpdateToolCall:
		a.retryResolution.Resolve(a.Sink())
	}
	return false
}
