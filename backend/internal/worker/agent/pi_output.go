package agent

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

// piToolExecutionEnvelope captures `tool_execution_*` event headers. Input is
// the start payload (carrying the prompt/description used as the registry
// title); Result is the end payload.
type piToolExecutionEnvelope struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	Result     json.RawMessage `json:"result"`
}

// piToolUpdateEnvelope adds the partialResult content blocks consumed when
// computing the streaming delta for `tool_execution_update`, plus the Details
// json.RawMessage the pi-subagents extension carries (subagent status/activity).
type piToolUpdateEnvelope struct {
	ToolCallID    string `json:"toolCallId"`
	PartialResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Details carries provider-specific structured data. For the
		// pi-subagents extension it holds {status, activity, agentId} for a
		// running subagent, parsed by shape in handlePiToolExecutionUpdate.
		Details json.RawMessage `json:"details"`
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

// piAgentEndEnvelope captures the per-message stop info on agent_end so we
// can inspect the final assistant turn's outcome and decide whether to
// auto-continue.
type piAgentEndEnvelope struct {
	Messages []struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"messages"`
}

// piRetryableWebSocketError is the exact errorMessage Pi emits for transient
// WebSocket disconnects that we auto-retry via the auto-continue pipeline.
const piRetryableWebSocketError = "WebSocket error"

// piDialogMethods is the set of extension UI methods that block waiting for an
// extension_ui_response. These are surfaced as control requests so the
// frontend can render a dialog and ship a response back.
var piDialogMethods = map[string]struct{}{
	contracts.PiDialogMethodSelect:  {},
	contracts.PiDialogMethodConfirm: {},
	contracts.PiDialogMethodInput:   {},
	contracts.PiDialogMethodEditor:  {},
}

// handlePiOutput dispatches a single parsed Pi event line.
func handlePiOutput(a *PiAgent, line *parsedLine) {
	slog.Debug("pi HandleOutput", "agent_id", a.agentID, "type", line.Type, "len", len(line.Raw))

	switch line.Type {
	case contracts.PiEventAgentStart:
		a.handlePiAgentStart()
	case contracts.PiEventAgentEnd:
		a.handlePiAgentEnd(line.Raw)
	case contracts.PiEventTurnStart, contracts.PiEventTurnEnd, contracts.PiEventMessageStart:
		// Lifecycle markers; no UI state change required.
	case contracts.PiEventMessageUpdate:
		a.handlePiMessageUpdate(line.Raw)
	case contracts.PiEventMessageEnd:
		a.handlePiMessageEnd(line.Raw)
	case contracts.PiEventToolExecutionStart:
		a.handlePiToolExecutionStart(line.Raw)
	case contracts.PiEventToolExecutionUpdate:
		a.handlePiToolExecutionUpdate(line.Raw)
	case contracts.PiEventToolExecutionEnd:
		a.handlePiToolExecutionEnd(line.Raw)
	case contracts.PiEventQueueUpdate:
		a.handlePiQueueUpdate(line.Raw)
	case contracts.PiEventCompactionStart, contracts.PiEventCompactionEnd,
		contracts.PiEventAutoRetryStart, contracts.PiEventAutoRetryEnd,
		contracts.PiEventExtensionError:
		// Pi-emitted lifecycle / extension events — AGENT source per the
		// proto rule (LEAPMUX is reserved for worker-synthesized envelopes).
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw); err != nil {
			slog.Error("pi persist notification", "agent_id", a.agentID, "type", line.Type, "error", err)
		}
	case contracts.PiEventExtensionUIRequest:
		a.handlePiExtensionUIRequest(line.Raw)
	case contracts.PiEventResponse:
		// Should have been intercepted by handlePiResponse; reaching here means
		// no caller was waiting on this id. Log and drop.
		slog.Warn("pi orphan response line", "agent_id", a.agentID, "len", len(line.Raw))
	default:
		// Persist unknown event types so the user can still see them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("pi persist unknown event", "agent_id", a.agentID, "type", line.Type, "error", err)
		}
	}
}

func (a *PiAgent) handlePiAgentStart() {
	a.mu.Lock()
	a.currentTurnActive = true
	a.mu.Unlock()
	// A fresh turn begins: start the thinking-token estimate from zero.
	a.thinkingTokens.reset()
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
	scheduleOrCancelAPIErrorAutoContinue(a.sink, isRetryablePiAgentEndFailure(raw), raw)
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

func isRetryablePiAgentEndFailure(raw []byte) bool {
	var env piAgentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	// Walk from the end: only the final assistant message reflects the
	// turn's final outcome; earlier assistant entries are intra-turn.
	for i := len(env.Messages) - 1; i >= 0; i-- {
		msg := env.Messages[i]
		if msg.Role != PiRoleAssistant {
			continue
		}
		return msg.StopReason == PiStopReasonError && msg.ErrorMessage == piRetryableWebSocketError
	}
	return false
}

func (a *PiAgent) handlePiMessageEnd(raw []byte) {
	augmented := a.augmentPiMessageEnd(raw)
	// The pi-subagents extension emits a customType:"subagent-notification"
	// message carrying registry details (status / activity / agentId, and
	// optionally details.others[] for group nudges). Update/close the registry
	// from it BEFORE the generic persist; the message itself STILL persists to
	// the parent transcript (it is real conversational context).
	piApplySubagentNotification(a.sink, augmented)
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, augmented, SpanInfo{}); err != nil {
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
	case contracts.PiAssistantEventTextDelta, contracts.PiAssistantEventThinkingDelta:
		// Text and thinking deltas are handled identically: stream the chunk under
		// its own event type, then feed it to the live token estimate. Pi persists
		// both as one message_end with no per-phase split, so a thinking->text
		// transition inside a single message shares one thinking-token phase by
		// design. The broadcast method is taken from the event type so the frontend
		// still distinguishes the two stream kinds.
		if env.AssistantMessageEvent.Delta == "" {
			return
		}
		a.sink.BroadcastStreamChunk([]byte(env.AssistantMessageEvent.Delta), "", env.AssistantMessageEvent.Type)
		a.thinkingTokens.observe(a.sink, env.AssistantMessageEvent.Delta)
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

	// Record the tool's input description (the pi-subagents extension carries
	// the spawn prompt here) for the background-task registry title.
	if desc := piExtractDescription(env.Input, env.ToolName); desc != "" {
		a.mu.Lock()
		if a.toolCallDescriptions == nil {
			a.toolCallDescriptions = make(map[string]string)
		}
		a.toolCallDescriptions[env.ToolCallID] = desc
		a.mu.Unlock()
	}
	// The spawn prompt, kept whole for the child transcript's first message.
	// pi-subagents declares it `prompt` on the nested-agent tool
	// (src/nested-tools.ts); a non-subagent tool simply has none.
	if prompt := piExtractPrompt(env.Input); prompt != "" {
		a.mu.Lock()
		a.toolCallPrompts.remember(env.ToolCallID, prompt)
		a.mu.Unlock()
	}

	// A subagent spawn owns no span, so it reserves no color either. The
	// subagent's output lands in its own child transcript, so a rail held open
	// for the whole run only pushes every concurrent tool one column right.
	//
	// Pi never reads the recorded span type back -- tool_execution_end carries
	// its own toolName -- but openToolSpan records it for every provider, so a
	// closing message that DOES read it (Claude, ACP) finds it.
	spawns := env.ToolName == PiToolAgent
	if err := openToolSpan(a.sink, raw, env.ToolCallID, env.ToolName, spawns); err != nil {
		slog.Error("pi persist tool_execution_start", "agent_id", a.agentID, "error", err)
	}
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
	a.sink.BroadcastStreamChunk([]byte(delta.String()), env.ToolCallID, contracts.PiEventToolExecutionUpdate)

	// pi-subagents extension: when partialResult.details parses to the
	// subagent shape {status, activity} (shape detection), upsert a running
	// registry row keyed by details.agentId (fallback toolCallId).
	if obs := piSubagentFromDetails(env.PartialResult.Details, env.ToolCallID, a.toolCallTitle(env.ToolCallID)); obs != nil {
		if err := a.sink.UpsertBackgroundTask(*obs); err != nil {
			slog.Warn("pi subagent upsert failed", "agent_id", a.agentID, "tool_call", env.ToolCallID, "error", err)
		}
	}
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

	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, SpanInfo{
		SpanID:   env.ToolCallID,
		SpanType: env.ToolName,
		Closing:  true,
	}); err != nil {
		slog.Error("pi persist tool_execution_end", "agent_id", a.agentID, "error", err)
	}
	a.sink.BroadcastStreamEnd(env.ToolCallID)
	a.sink.CloseSpan(env.ToolCallID)

	// pi-subagents extension: parse the result details for final status, or
	// a background re-key. The row key may change from toolCallId to
	// details.agentId here (the agent id surfaces only at completion).
	piApplySubagentEnd(a.sink, env.Result, env.ToolCallID, a.toolCallTitle(env.ToolCallID), a.toolCallPrompts.take(env.ToolCallID))
	a.mu.Lock()
	delete(a.toolCallDescriptions, env.ToolCallID)
	a.mu.Unlock()
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
		claimToken := a.sink.PersistControlRequest(head.ID, raw)
		a.sink.BroadcastControlRequest(head.ID, raw, claimToken)
		return
	}

	switch head.Method {
	case contracts.PiExtensionMethodNotify:
		// Persist the raw extension_ui_request envelope as AGENT. The
		// frontend's Pi notification renderer derives level/message from
		// `notifyType`/`message` on the raw payload — no synthesis needed.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
			slog.Error("pi persist notify", "agent_id", a.agentID, "error", err)
		}
	case contracts.PiExtensionMethodSetStatus:
		statusValue := any(nil)
		if head.StatusText != nil {
			statusValue = *head.StatusText
		}
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_status": map[string]any{head.StatusKey: statusValue},
		})
	case contracts.PiExtensionMethodSetWidget:
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
	case contracts.PiExtensionMethodSetTitle:
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_terminal_title": head.Title,
		})
	case contracts.PiExtensionMethodSetEditorText:
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_editor_text": head.Text,
		})
	default:
		// Unknown extension UI method — record so the user can see it.
		// Pi-emitted, so AGENT source.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
			slog.Error("pi persist unknown extension_ui_request",
				"agent_id", a.agentID, "method", head.Method, "error", err)
		}
	}
}

// --- pi-subagents extension: background-task registry helpers ---

// toolCallTitle returns the description recorded at tool_execution_start for
// the registry title (empty when none was recorded).
func (a *PiAgent) toolCallTitle(toolCallID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.toolCallDescriptions[toolCallID]
}

// logUpsertRefusal records a background-task row the registry REFUSED.
//
// A thin name over the shared `logRegistryRefusal`, kept because these seven
// call sites read better without the two constant arguments repeated at each
// one. The RULE is the shared helper's; this only names the provider once.
func logUpsertRefusal(err error) {
	logRegistryRefusal("pi", "upsert", err)
}

// piExtractDescription pulls a human label out of a tool_execution_start input.
// The pi-subagents extension carries the spawn prompt as `description` (and a
// `prompt`); fall back to the tool name.
func piExtractDescription(input json.RawMessage, toolName string) string {
	if len(input) == 0 {
		return toolName
	}
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if json.Unmarshal(input, &in) == nil {
		// Both branches take the same cap. The description arrives as a
		// label the model wrote, so it is no more bounded than the prompt is,
		// and a caller that reads one branch must not have to know which.
		//
		// CLEAN FIRST, THEN TEST. A field that holds only characters a reader
		// cannot see -- a run of zero-width spaces, a lone bidirectional mark --
		// is non-empty as bytes and empty as text, so testing the RAW field
		// entered the branch and then returned "": the row lost the prompt
		// fallback AND the tool-name fallback, and a Pi subagent appeared in the
		// sidebar with no label at all. `acpBridge.terminal/create` orders these
		// the same way.
		if desc := bgtask.CleanTitleRunes(bgtask.FirstLine(in.Description), 80); desc != "" {
			return desc
		}
		if prompt := bgtask.CleanTitleRunes(bgtask.FirstLine(in.Prompt), 80); prompt != "" {
			return prompt
		}
	}
	return toolName
}

// piExtractPrompt pulls the whole spawn prompt out of a tool_execution_start
// input, or "" when the tool carries none. Distinct from
// piExtractDescription, which wants a short label and truncates to one line.
func piExtractPrompt(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	return in.Prompt
}

// piSubagentDetails is the shape the pi-subagents extension carries in
// partialResult.details (update) and result.details (end): a status + activity
// (+ agentId at completion). Shape detection: a details blob with a non-empty
// status is treated as a subagent observation.
type piSubagentDetails struct {
	Status   string `json:"status"`
	Activity string `json:"activity"`
	AgentID  string `json:"agentId"`
}

// piSubagentFromDetails upserts a running registry row when details parse to
// the subagent shape. Returns nil for a non-subagent details blob.
func piSubagentFromDetails(details json.RawMessage, toolCallID, title string) *bgtask.Upsert {
	if len(details) == 0 {
		return nil
	}
	var d piSubagentDetails
	if json.Unmarshal(details, &d) != nil || d.Status == "" {
		return nil
	}
	rowKey := d.AgentID
	if rowKey == "" {
		rowKey = toolCallID
	}
	return &bgtask.Upsert{
		RowKey:     rowKey,
		Kind:       bgtask.KindSubagent,
		Title:      title,
		ActiveForm: d.Activity,
		Status:     bgtask.StatusRunning,
	}
}

// piFinalStatus maps a pi-subagents result status to the registry final
// status. completed/steered→Completed, error→Failed, stopped/aborted→Stopped.
func piFinalStatus(s string) (bgtask.Status, bool) {
	switch s {
	case "completed", "steered":
		return bgtask.StatusCompleted, true
	case "error":
		return bgtask.StatusFailed, true
	case "stopped", "aborted":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusCompleted, false
	}
}

// piAgentIDRe matches a standalone "Agent ID: <id>" line in a Pi tool result.
// Anchored to a line start so free-form model prose that merely mentions
// "Agent ID:" mid-sentence does not produce a phantom registry row.
var piAgentIDRe = regexp.MustCompile(`(?m)^Agent ID: (\S+)\s*$`)

// piApplySubagentEnd parses a tool_execution_end result for final status or
// a background re-key. status:"background" re-keys the row to details.agentId
// and leaves it Running (fallback: regex "Agent ID: (\S+)" over result text).
func piApplySubagentEnd(sink OutputSink, result json.RawMessage, toolCallID, title, prompt string) {
	if len(result) == 0 {
		return
	}
	// result may be a JSON object with details, or a raw string.
	var d piSubagentDetails
	if json.Unmarshal(result, &d) == nil && d.Status != "" {
		if d.Status == "background" {
			if agentID := d.AgentID; agentID != "" && agentID != toolCallID {
				// Re-key: upsert under the agent id and close the toolCallId row.
				// The close is mandatory -- without it the old toolCallId row stays
				// Running forever and pins the parent's thinking indicator. The
				// upsert links the child transcript (EnsureChildAgent) so the row is
				// openable as a child tab, and the two writes share one swallowed-
				// error discipline so a close failure is visible in the log rather
				// than silently leaving a duplicate Running row.
				if childID, err := sink.EnsureChildAgent(toolCallID, agentID, title); err != nil {
					slog.Warn("pi background re-key ensure child failed", "tool_call_id", toolCallID, "agent_id", agentID, "error", err)
				} else {
					// The child transcript exists only from here, so this is where
					// the spawn prompt becomes its first message.
					if err := sink.PersistChildPrompt(childID, prompt); err != nil {
						slog.Warn("pi background re-key persist prompt failed", "tool_call_id", toolCallID, "error", err)
					}
					logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{
						RowKey:       agentID,
						Kind:         bgtask.KindSubagent,
						ChildAgentID: childID,
						Title:        title,
						Status:       bgtask.StatusRunning,
					}))
				}
				if err := sink.CloseBackgroundTask(toolCallID, bgtask.StatusCompleted); err != nil {
					slog.Warn("pi background re-key close failed", "tool_call_id", toolCallID, "error", err)
				}
			}
			// No agent id: the row stays keyed by toolCallID as-is (still running).
			return
		}
		// An unrecognized status must NOT give a final status to the row (piFinalStatus
		// returns ok=false for it). Upsert as Running so a future final event
		// can still close it, matching piApplySubagentNotification's contract.
		status, ok := piFinalStatus(d.Status)
		rowKey := d.AgentID
		if rowKey == "" {
			rowKey = toolCallID
		}
		if ok {
			// A final-status upsert already stamps ended_at and the monotonic-final
			// guard makes the row absorbing; no separate CloseBackgroundTask needed
			// (it would early-return on the now-finished row).
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, Status: status}))
		} else {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, ActiveForm: d.Activity, Status: bgtask.StatusRunning}))
		}
		return
	}
	// Fallback: regex over the result text for an Agent ID (a background agent
	// whose details did not parse). Key the row off the deterministic toolCallID
	// so a later final event can close it; the captured agent id only refines
	// the title. Free-form prose that mentions "Agent ID:" mid-sentence does not
	// match the anchored regex. The result may be a JSON-encoded string, so
	// decode it first; fall back to the raw text if it is not a string.
	s := string(result)
	var asString string
	if json.Unmarshal(result, &asString) == nil {
		s = asString
	}
	if strings.Contains(s, "Agent ID:") {
		if m := piAgentIDRe.FindStringSubmatch(s); len(m) > 1 {
			rowTitle := title
			if rowTitle == "" {
				rowTitle = "background agent " + m[1]
			}
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: toolCallID, Kind: bgtask.KindSubagent, Title: rowTitle, Status: bgtask.StatusRunning,
			}))
		}
	}
}

// piApplySubagentNotification sniffs a customType:"subagent-notification"
// message and updates/closes the registry from its details (including
// details.others[] for group nudges). The message itself still persists.
func piApplySubagentNotification(sink OutputSink, raw []byte) {
	var msg struct {
		CustomType string          `json:"customType"`
		Details    json.RawMessage `json:"details"`
	}
	if json.Unmarshal(raw, &msg) != nil || msg.CustomType != "subagent-notification" {
		return
	}
	applyOne := func(details json.RawMessage) {
		if len(details) == 0 {
			return
		}
		var d piSubagentDetails
		if json.Unmarshal(details, &d) != nil || d.Status == "" {
			return
		}
		rowKey := d.AgentID
		if rowKey == "" {
			return
		}
		if status, ok := piFinalStatus(d.Status); ok {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Status: status}))
		} else {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, ActiveForm: d.Activity, Status: bgtask.StatusRunning}))
		}
	}
	applyOne(msg.Details)
	// Group nudges: details.others[] carries sibling agent ids.
	var grp struct {
		Others []struct {
			AgentID string `json:"agentId"`
			Status  string `json:"status"`
		} `json:"others"`
	}
	if json.Unmarshal(msg.Details, &grp) == nil {
		for _, o := range grp.Others {
			if o.AgentID == "" || o.Status == "" {
				continue
			}
			if status, ok := piFinalStatus(o.Status); ok {
				logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: o.AgentID, Kind: bgtask.KindSubagent, Status: status}))
			}
		}
	}
}
