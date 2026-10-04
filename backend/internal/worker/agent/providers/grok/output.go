package grok

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
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

// reportCompaction states a compaction of the context in the transcript. Grok
// compacts on `/compact` and on its own at a threshold, and the reader sees
// neither in the conversation otherwise.
func (a *Agent) reportCompaction(updateType string, update json.RawMessage) {
	var compaction struct {
		TokensUsed    int64  `json:"tokens_used"`
		ContextWindow int64  `json:"context_window"`
		Percentage    int    `json:"percentage"`
		TokensBefore  *int64 `json:"tokens_before"`
		TokensAfter   int64  `json:"tokens_after"`
		Error         string `json:"error"`
	}
	if err := json.Unmarshal(update, &compaction); err != nil {
		slog.Warn("grok compaction notification unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	var text string
	switch updateType {
	case grokUpdateAutoCompactStarted:
		text = fmt.Sprintf("Compacting the context (%d%% of the window used)", compaction.Percentage)
	case grokUpdateAutoCompactCompleted:
		if compaction.TokensBefore != nil {
			text = fmt.Sprintf("Context compacted from %d to %d tokens", *compaction.TokensBefore, compaction.TokensAfter)
		} else {
			text = fmt.Sprintf("Context compacted to %d tokens", compaction.TokensAfter)
		}
	case grokUpdateAutoCompactFailed:
		text = "Context compaction failed"
		if detail := strings.TrimSpace(compaction.Error); detail != "" {
			text += ": " + detail
		}
	default:
		return
	}
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: text,
	})
}

// reportRetry states a model request that Grok retries. The turn looks idle
// while Grok waits out its backoff, so the reader must see why.
//
// Only a retry in progress is stated. An exhausted or failed retry ends the
// turn with an error, which the turn's own end reports.
func (a *Agent) reportRetry(update json.RawMessage) {
	var retry struct {
		Type       string `json:"type"`
		Attempt    int    `json:"attempt"`
		MaxRetries int    `json:"max_retries"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(update, &retry); err != nil || retry.Type != "retrying" {
		return
	}
	text := fmt.Sprintf("Retrying the model request (attempt %d of %d)", retry.Attempt, retry.MaxRetries)
	if reason := strings.TrimSpace(retry.Reason); reason != "" {
		text += ": " + reason
	}
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: text,
	})
}
