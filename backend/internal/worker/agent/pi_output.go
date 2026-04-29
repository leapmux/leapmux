package agent

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// piMessageUpdateEnvelope captures the bits of a `message_update` event we
// need to drive UI streaming. Pi's full envelope contains the entire partial
// message which is large; we unmarshal lazily into this small shape so the
// hot delta path is cheap.
type piMessageUpdateEnvelope struct {
	AssistantMessageEvent struct {
		Type         string `json:"type"`
		Delta        string `json:"delta"`
		ContentIndex int    `json:"contentIndex"`
	} `json:"assistantMessageEvent"`
}

// piToolExecutionEnvelope captures `tool_execution_*` event headers.
type piToolExecutionEnvelope struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
}

// piToolUpdateEnvelope adds the partialResult content blocks consumed when
// computing the streaming delta for `tool_execution_update`.
type piToolUpdateEnvelope struct {
	ToolCallID    string `json:"toolCallId"`
	PartialResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"partialResult"`
}

// piExtensionUIRequestHeader captures the routing fields of an
// extension_ui_request event. The full payload is forwarded verbatim to the
// frontend through PersistControlRequest / PersistLeapMuxNotification so renderers
// can read every method-specific field.
type piExtensionUIRequestHeader struct {
	ID         string          `json:"id"`
	Method     string          `json:"method"`
	StatusKey  string          `json:"statusKey"`
	StatusText *string         `json:"statusText"`
	WidgetKey  string          `json:"widgetKey"`
	NotifyType string          `json:"notifyType"`
	Message    string          `json:"message"`
	Title      string          `json:"title"`
	Text       string          `json:"text"`
	Lines      json.RawMessage `json:"widgetLines"`
	Placement  string          `json:"widgetPlacement"`
}

// piQueueUpdateEnvelope captures the queue depths we surface as session info.
type piQueueUpdateEnvelope struct {
	Steering []json.RawMessage `json:"steering"`
	FollowUp []json.RawMessage `json:"followUp"`
}

// piDialogMethods is the set of extension UI methods that block waiting for an
// extension_ui_response. These are surfaced as control requests so the
// frontend can render a dialog and ship a response back.
var piDialogMethods = map[string]struct{}{
	PiDialogMethodSelect:  {},
	PiDialogMethodConfirm: {},
	PiDialogMethodInput:   {},
	PiDialogMethodEditor:  {},
}

// handlePiOutput dispatches a single parsed Pi event line.
func handlePiOutput(a *PiAgent, line *parsedLine) {
	slog.Debug("pi HandleOutput", "agent_id", a.agentID, "type", line.Type, "len", len(line.Raw))

	switch line.Type {
	case PiEventAgentStart:
		a.handlePiAgentStart()
	case PiEventAgentEnd:
		a.handlePiAgentEnd(line.Raw)
	case PiEventTurnStart, PiEventTurnEnd, PiEventMessageStart:
		// Lifecycle markers; no UI state change required.
	case PiEventMessageUpdate:
		a.handlePiMessageUpdate(line.Raw)
	case PiEventMessageEnd:
		a.handlePiMessageEnd(line.Raw)
	case PiEventToolExecutionStart:
		a.handlePiToolExecutionStart(line.Raw)
	case PiEventToolExecutionUpdate:
		a.handlePiToolExecutionUpdate(line.Raw)
	case PiEventToolExecutionEnd:
		a.handlePiToolExecutionEnd(line.Raw)
	case PiEventQueueUpdate:
		a.handlePiQueueUpdate(line.Raw)
	case PiEventCompactionStart, PiEventCompactionEnd,
		PiEventAutoRetryStart, PiEventAutoRetryEnd,
		PiEventExtensionError:
		// Pi-emitted lifecycle / extension events — SYSTEM role per the
		// proto rule (LEAPMUX is reserved for worker-synthesized envelopes).
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, line.Raw); err != nil {
			slog.Error("pi persist notification", "agent_id", a.agentID, "type", line.Type, "error", err)
		}
	case PiEventExtensionUIRequest:
		a.handlePiExtensionUIRequest(line.Raw)
	case PiEventResponse:
		// Should have been intercepted by handlePiResponse; reaching here means
		// no caller was waiting on this id. Log and drop.
		slog.Warn("pi orphan response line", "agent_id", a.agentID, "len", len(line.Raw))
	default:
		// Persist unknown event types so the user can still see them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("pi persist unknown event", "agent_id", a.agentID, "type", line.Type, "error", err)
		}
	}
}

func (a *PiAgent) handlePiAgentStart() {
	a.mu.Lock()
	a.currentTurnActive = true
	a.mu.Unlock()
}

func (a *PiAgent) handlePiAgentEnd(raw []byte) {
	a.mu.Lock()
	a.currentTurnActive = false
	a.turnToolUses = 0
	a.mu.Unlock()
	// Recover from any tool calls that didn't get a matching
	// tool_execution_end (e.g. aborted turn). Otherwise the map retains the
	// cumulative result text indefinitely across sessions.
	a.resetCumulativeDeltas()

	// Persist the divider immediately with the latest locally observed usage so
	// chat ordering stays stable even if the user sends the next prompt right
	// away. Then refresh Pi's authoritative session stats asynchronously for the
	// live popover; the stdout read loop must remain free to deliver that RPC
	// response.
	a.persistPiAgentEnd(raw, a.currentPiUsageSnapshot())
	a.sink.ResetSpans()
	a.sink.BroadcastSessionInfo(map[string]any{
		"pi_turn_active": false,
	})
	if a.canRequestPiSessionStats() {
		go func() {
			_, _ = a.refreshPiSessionStats(piSessionStatsTimeout(a.APITimeout()))
		}()
	}
}

func (a *PiAgent) handlePiMessageEnd(raw []byte) {
	augmented := a.augmentPiMessageEnd(raw)
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, augmented, SpanInfo{}); err != nil {
		slog.Error("pi persist message_end", "agent_id", a.agentID, "error", err)
	}
}

func (a *PiAgent) handlePiMessageUpdate(raw []byte) {
	var env piMessageUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi message_update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	switch env.AssistantMessageEvent.Type {
	case PiAssistantEventTextDelta:
		if env.AssistantMessageEvent.Delta == "" {
			return
		}
		a.sink.BroadcastStreamChunk([]byte(env.AssistantMessageEvent.Delta), "", PiAssistantEventTextDelta)
	case PiAssistantEventThinkingDelta:
		if env.AssistantMessageEvent.Delta == "" {
			return
		}
		a.sink.BroadcastStreamChunk([]byte(env.AssistantMessageEvent.Delta), "", PiAssistantEventThinkingDelta)
	default:
		// All other delta sub-types (text_start/end, thinking_start/end,
		// toolcall_*, start, done, error) are handled via message_end and
		// tool_execution_* events; ignore here to avoid double-rendering.
	}
}

func (a *PiAgent) handlePiToolExecutionStart(raw []byte) {
	var env piToolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("pi tool_execution_start unmarshal failed",
			"agent_id", a.agentID, "error", err)
		return
	}

	spanColor := a.sink.ReserveSpanColor(env.ToolCallID, "")
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, raw, SpanInfo{
		SpanID:    env.ToolCallID,
		SpanType:  env.ToolName,
		SpanColor: spanColor,
	}); err != nil {
		slog.Error("pi persist tool_execution_start", "agent_id", a.agentID, "error", err)
	}
	a.sink.SetSpanType(env.ToolCallID, env.ToolName)
	a.sink.OpenSpan(env.ToolCallID, "")
}

// handlePiToolExecutionUpdate ships only the new text added since the
// previous update. Pi's partialResult is cumulative — broadcasting the
// raw envelope would let the frontend concatenate the same growing text
// into one quadratically-bloating buffer. The handler walks content
// blocks once to compute total length, records it, then walks once more
// building only the tail bytes — avoiding the O(N) full-string
// allocation per update that would itself be quadratic over a stream.
func (a *PiAgent) handlePiToolExecutionUpdate(raw []byte) {
	var env piToolUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		return
	}

	totalLen := 0
	for _, c := range env.PartialResult.Content {
		if c.Type == PiContentBlockText {
			totalLen += len(c.Text)
		}
	}

	prevLen, reset := a.recordCumulativeLength(env.ToolCallID, totalLen)
	if !reset && totalLen <= prevLen {
		return
	}

	var delta strings.Builder
	delta.Grow(totalLen - prevLen)
	seen := 0
	for _, c := range env.PartialResult.Content {
		if c.Type != PiContentBlockText {
			continue
		}
		blockLen := len(c.Text)
		if seen+blockLen <= prevLen {
			seen += blockLen
			continue
		}
		if seen >= prevLen {
			delta.WriteString(c.Text)
		} else {
			delta.WriteString(c.Text[prevLen-seen:])
		}
		seen += blockLen
	}
	if delta.Len() == 0 {
		return
	}
	a.sink.BroadcastStreamChunk([]byte(delta.String()), env.ToolCallID, PiEventToolExecutionUpdate)
}

func (a *PiAgent) handlePiToolExecutionEnd(raw []byte) {
	var env piToolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("pi tool_execution_end unmarshal failed",
			"agent_id", a.agentID, "error", err)
		return
	}

	a.mu.Lock()
	a.turnToolUses++
	a.mu.Unlock()
	a.clearCumulativeDelta(env.ToolCallID)

	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, raw, SpanInfo{
		SpanID:   env.ToolCallID,
		SpanType: env.ToolName,
		Closing:  true,
	}); err != nil {
		slog.Error("pi persist tool_execution_end", "agent_id", a.agentID, "error", err)
	}
	a.sink.BroadcastStreamEnd(env.ToolCallID)
	a.sink.CloseSpan(env.ToolCallID)
}

func (a *PiAgent) handlePiQueueUpdate(raw []byte) {
	var env piQueueUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi queue_update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	a.sink.BroadcastSessionInfo(map[string]any{
		"pi_queue_depth":     len(env.Steering) + len(env.FollowUp),
		"pi_steering_depth":  len(env.Steering),
		"pi_follow_up_depth": len(env.FollowUp),
	})
}

// handlePiExtensionUIRequest routes a Pi extension_ui_request event to either
// a control request (dialog methods) or a session-info / notification
// broadcast (fire-and-forget methods).
func (a *PiAgent) handlePiExtensionUIRequest(raw []byte) {
	var head piExtensionUIRequestHeader
	if err := json.Unmarshal(raw, &head); err != nil {
		slog.Warn("pi extension_ui_request unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	if _, isDialog := piDialogMethods[head.Method]; isDialog {
		if head.ID == "" {
			slog.Warn("pi extension_ui_request dialog missing id",
				"agent_id", a.agentID, "method", head.Method)
			return
		}
		a.sink.PersistControlRequest(head.ID, raw)
		a.sink.BroadcastControlRequest(head.ID, raw)
		return
	}

	switch head.Method {
	case PiExtensionMethodNotify:
		// Persist the raw extension_ui_request envelope as SYSTEM. The
		// frontend's Pi notification renderer derives level/message from
		// `notifyType`/`message` on the raw payload — no synthesis needed.
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, raw); err != nil {
			slog.Error("pi persist notify", "agent_id", a.agentID, "error", err)
		}
	case PiExtensionMethodSetStatus:
		statusValue := any(nil)
		if head.StatusText != nil {
			statusValue = *head.StatusText
		}
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_status": map[string]any{head.StatusKey: statusValue},
		})
	case PiExtensionMethodSetWidget:
		widget := map[string]any{
			"placement": cmp.Or(head.Placement, "aboveEditor"),
		}
		if len(head.Lines) > 0 {
			widget["lines"] = head.Lines
		} else {
			widget["lines"] = nil
		}
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_widget": map[string]any{head.WidgetKey: widget},
		})
	case PiExtensionMethodSetTitle:
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_terminal_title": head.Title,
		})
	case PiExtensionMethodSetEditorText:
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_editor_text": head.Text,
		})
	default:
		// Unknown extension UI method — record so the user can see it.
		// Pi-emitted, so SYSTEM role.
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, raw); err != nil {
			slog.Error("pi persist unknown extension_ui_request",
				"agent_id", a.agentID, "method", head.Method, "error", err)
		}
	}
}
