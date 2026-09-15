package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// piMessageUpdateEnvelope captures the fields that model progress needs.
// Pi's full envelope contains the large partial message, so this small shape
// keeps delta processing cheap.
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
	Args       json.RawMessage `json:"args"`
	Input      json.RawMessage `json:"input"`
	Result     json.RawMessage `json:"result"`
	IsError    bool            `json:"isError"`
}

// piToolUpdateEnvelope adds the cumulative output and the structured details
// that the pi-subagents extension carries.
type piToolUpdateEnvelope struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	PartialResult json.RawMessage `json:"partialResult"`
}

type piPartialResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// Details carries provider-specific structured data. For the
	// pi-subagents extension it holds {status, activity, agentId}.
	Details json.RawMessage `json:"details"`
}

type piToolState struct {
	ToolName string
	Args     json.RawMessage
	// StartFrame is the tool_execution_start event Pi sent, byte for byte. A turn
	// that ends while the call runs stores THAT frame, so the transcript never holds
	// an event Pi did not send.
	StartFrame    []byte
	PartialResult json.RawMessage
	Description   string
	Order         uint64
}

// piExtensionUIRequestHeader captures the routing fields of an
// extension_ui_request event. The full payload is forwarded verbatim to the
// frontend through PublishControlRequest / PersistLeapMuxNotification so renderers
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
//
// WillRetry is Pi's own statement that it restarts this run itself. Pi's
// session layer stamps it on every agent_end. It is true only for three
// conditions together: retries are enabled, the retry budget is unspent, and
// the last assistant message failed with a transient error. An older Pi omits
// the field, which decodes to false -- how LeapMux behaved before it read it.
type piAgentEndEnvelope struct {
	Messages []struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"messages"`
	WillRetry bool `json:"willRetry"`
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
	case contracts.PiEventTurnStart, contracts.PiEventTurnEnd,
		contracts.PiEventMessageStart, contracts.PiEventAgentSettled:
		// Lifecycle markers; no UI state change required. `agent_settled` says
		// only that Pi will not continue on its own after the agent_end that
		// already drew the divider, so it adds nothing to the transcript.
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
	case contracts.PiEventEntryAppended:
		var event struct {
			Entry struct {
				CustomType string `json:"customType"`
			} `json:"entry"`
		}
		if json.Unmarshal(line.Raw, &event) == nil && event.Entry.CustomType == "pi-goal-focus" {
			a.schedulePiGoalRefresh(false)
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: line.Raw}, SpanInfo{}); err != nil {
			slog.Error("persist Pi session entry", "agent_id", a.agentID, "error", err)
		}
	case contracts.PiEventResponse:
		// Should have been intercepted by handlePiResponse; reaching here means
		// no caller was waiting on this id. Log and drop.
		slog.Warn("pi orphan response line", "agent_id", a.agentID, "len", len(line.Raw))
	default:
		// Persist unknown event types so the user can still see them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: line.Raw}, SpanInfo{}); err != nil {
			slog.Error("pi persist unknown event", "agent_id", a.agentID, "type", line.Type, "error", err)
		}
	}
}

// PublishTurnActive republishes the Worker-visible turn state from
// currentTurnActive, the single source. Call it after EVERY critical section
// that writes that field.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and a missing call is the only way the two can drift.
// Never called with a.mu held: the sink broadcasts, and a broadcast can block on
// a slow transport.
//
// Pi keeps the turn OPEN across a retry it drives itself (agent_end with
// willRetry), so the state stays busy for the whole backoff -- a stretch where
// nothing streams and no envelope arrives, and where a client that inferred
// idleness would drop the spinner and hide the Interrupt button on a run that is
// still going.
func (a *PiAgent) PublishTurnActive() TurnState {
	a.mu.Lock()
	active := a.currentTurnActive
	seq := a.nextTurnSeq()
	a.mu.Unlock()
	return publishSteerableTurnActiveTo(a.sink, active, seq)
}

func (a *PiAgent) handlePiAgentStart() {
	// Read the clock before the lock, so an injected clock never runs under mu.
	startedAt := a.now()
	a.mu.Lock()
	a.currentTurnActive = true
	// A retried run continues the turn that the first agent_start began, so the
	// mark survives it and the divider reports the whole elapsed time instead of
	// the last attempt alone. handlePiAgentEnd clears it when the turn ends.
	firstAttempt := a.turnStartedAt.IsZero()
	if firstAttempt {
		a.turnStartedAt = startedAt
		// A NEW turn begins here, so an interrupt note that no agent_end spent is
		// stale. A retried run keeps the mark, and keeps the note with it.
		a.interruptRequested = false
	}
	a.mu.Unlock()
	a.PublishTurnActive()
	// A fresh turn begins with empty progress counters.
	a.sink.ReportProgress(ResetModelProgress())
	// Extensions can start a replacement session without a worker new_session request.
	// A retry continues the same turn, so it cannot have replaced the session: one
	// probe for each turn is enough, and Pi can retry a run many times.
	if firstAttempt && a.canRequestPiSessionStats() {
		go func() {
			_, _ = a.refreshPiSessionStats(piSessionStatsTimeout(a.APITimeout()))
		}()
	}
}

func (a *PiAgent) handlePiAgentEnd(raw []byte) {
	// Decode once. The retry decision, the willRetry routing, and the auto-
	// continue decision all read this same envelope.
	var env piAgentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi agent_end unmarshal failed", "agent_id", a.agentID, "error", err)
	}

	endedAt := a.now()
	a.mu.Lock()
	// A retry keeps the turn open: Pi restarts the run itself, so Interrupt must
	// still send an abort and a user message must still steer the running turn.
	a.currentTurnActive = env.WillRetry
	// Read the mark BEFORE the clear below consumes it, or every turn that
	// really ends measures from the zero time and reports no duration at all.
	startedAt := a.turnStartedAt
	toolUses := a.turnToolUses
	if !env.WillRetry {
		// Retries retain the count until the complete turn ends.
		a.turnToolUses = 0
		a.turnStartedAt = time.Time{}
	}
	a.mu.Unlock()
	// Publish the inactive state after the sink receives the completed tool count.
	// This order prevents a completion sound for a turn that used no tools.
	defer a.PublishTurnActive()
	// A retried run keeps the turn open, so the note must survive to the
	// agent_end that really ends it.
	interrupted := !env.WillRetry && a.takeInterruptRequest()
	if env.WillRetry {
		a.generationBuffer.Reset()
		a.discardIncompletePiTools()
		a.sink.ReportProgress(ResetProgress())
	} else {
		completion := env.retainedCompletion()
		// The stop LeapMux asked for outranks the stop reason Pi reports, which
		// spells one interruption as an error. See noteInterruptRequested.
		if interrupted {
			completion = MessageCompletionInterrupted
		}
		a.flushPiGeneration(completion)
		a.persistIncompletePiTools(completion)
	}
	// Recover from any tool calls that didn't get a matching
	// tool_execution_end (e.g. aborted turn). Otherwise the map retains the
	// cumulative result text indefinitely across sessions.
	a.resetCumulativeOutput()

	// Persist the divider immediately with the latest locally observed usage so
	// chat ordering stays stable even if the user sends the next prompt right
	// away. Then refresh Pi's authoritative session stats asynchronously for the
	// live popover; the stdout read loop must remain free to deliver that RPC
	// response.
	content := piAgentEndContent(raw, a.currentPiUsageSnapshot(), piTurnDurationMs(startedAt, endedAt))
	if interrupted {
		content.Completion = MessageCompletionInterrupted
	}
	a.persistPiAgentEnd(withToolUseCount(content, toolUses), env.WillRetry)
	// Pi retries the run itself when it says willRetry, so LeapMux must not send
	// a second continuation for the same failure. Pi reports false once its own
	// retry budget is spent, which is where LeapMux's auto-continue takes over
	// as the last resort.
	scheduleOrCancelAPIErrorAutoContinue(a.sink, !env.WillRetry && env.isRetryableFailure(), raw)
	// The failed attempt's spans are dead either way: a retried run reopens its
	// own, so the reset is unconditional.
	a.sink.ResetSpans()
	if a.canRequestPiSessionStats() {
		go func() {
			_, _ = a.refreshPiSessionStats(piSessionStatsTimeout(a.APITimeout()))
		}()
	}
}

// piTurnDurationMs measures one Pi turn in milliseconds. Pi's agent_end carries
// no duration of its own, so the worker brackets the turn instead.
//
// It returns nil for a turn whose start this worker never saw, and for a clock
// that moved backwards. The envelope then carries no duration at all, rather
// than a false 0. The frontend tells those two apart: it draws no time for an
// absent field, and "(0ms)" for a real zero.
func piTurnDurationMs(startedAt, endedAt time.Time) *int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return nil
	}
	ms := endedAt.Sub(startedAt).Milliseconds()
	return &ms
}

// isRetryableFailure reports whether the turn ended on the one transient
// failure LeapMux itself auto-continues past.
func (env piAgentEndEnvelope) isRetryableFailure() bool {
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

func (env piAgentEndEnvelope) retainedCompletion() MessageCompletion {
	for i := len(env.Messages) - 1; i >= 0; i-- {
		if env.Messages[i].Role != PiRoleAssistant {
			continue
		}
		if env.Messages[i].StopReason == PiStopReasonError {
			return MessageCompletionError
		}
		break
	}
	return MessageCompletionInterrupted
}

func (a *PiAgent) handlePiMessageEnd(raw []byte) {
	content := a.piMessageEndContent(raw)
	// Update child status from the nested custom message before persisting it.
	piApplySubagentNotification(a.sink, raw)
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{}); err != nil {
		slog.Error("pi persist message_end", "agent_id", a.agentID, "error", err)
		return
	}
	a.generationBuffer.Reset()
}

func (a *PiAgent) handlePiMessageUpdate(raw []byte) {
	var env piMessageUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi message_update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	switch env.AssistantMessageEvent.Type {
	case contracts.PiAssistantEventTextDelta, contracts.PiAssistantEventThinkingDelta:
		if env.AssistantMessageEvent.Delta == "" {
			return
		}
		a.sink.ReportProgress(ModelTextProgress("pi:model", env.AssistantMessageEvent.Delta))
		kind := AssembledMessageKindText
		if env.AssistantMessageEvent.Type == contracts.PiAssistantEventThinkingDelta {
			kind = AssembledMessageKindReasoning
		}
		scopeID := fmt.Sprintf("pi:content:%d", env.AssistantMessageEvent.ContentIndex)
		a.generationBuffer.Append(scopeID, kind, env.AssistantMessageEvent.Delta, joinVerbatim)
	default:
		// All other delta sub-types (text_start/end, thinking_start/end,
		// toolcall_*, start, done, error) are handled via message_end and
		// tool_execution_* events; ignore here to avoid double-rendering.
	}
}

func (a *PiAgent) flushPiGeneration(completion MessageCompletion) {
	if a.isDiscardingOutput() {
		a.generationBuffer.Reset()
		return
	}
	if err := a.generationBuffer.PersistAll(completion, func(raw []byte) error {
		return a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: raw}, SpanInfo{})
	}); err != nil {
		slog.Error("pi persist partial generation", "agent_id", a.agentID, "error", err)
	}
}

func (a *PiAgent) handlePiToolExecutionStart(raw []byte) {
	var env piToolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("pi tool_execution_start unmarshal failed",
			"agent_id", a.agentID, "error", err)
		return
	}

	input := env.Args
	if len(input) == 0 {
		input = env.Input
	}
	description := piExtractDescription(input, env.ToolName)
	a.mu.Lock()
	if a.toolStates == nil {
		a.toolStates = make(map[string]*piToolState)
	}
	a.toolStates[env.ToolCallID] = &piToolState{
		ToolName:    env.ToolName,
		Args:        append(json.RawMessage(nil), input...),
		StartFrame:  append([]byte(nil), raw...),
		Description: description,
		Order:       a.nextToolOrder,
	}
	a.nextToolOrder++
	a.mu.Unlock()
	// The spawn prompt, kept whole for the child transcript's first message.
	// pi-subagents declares it `prompt` on the nested-agent tool
	// (src/nested-tools.ts); a non-subagent tool simply has none.
	if prompt := piExtractPrompt(input); prompt != "" {
		a.toolCallPrompts.remember(env.ToolCallID, prompt)
	}

	// A subagent spawn owns no span, so it reserves no color either. The
	// subagent's output lands in its own child transcript, so a rail held open
	// for the whole run only pushes every concurrent tool one column right.
	//
	// Pi never reads the recorded span type back -- tool_execution_end carries
	// its own toolName -- but openToolSpan records it for every provider, so a
	// closing message that DOES read it (Claude, ACP) finds it.
	spawns := env.ToolName == contracts.PiToolAgent || env.ToolName == contracts.PiToolSubagentWorkflow
	if err := openToolSpan(a.sink, MessageContent{Original: raw}, env.ToolCallID, env.ToolName, spawns); err != nil {
		slog.Error("pi persist tool_execution_start", "agent_id", a.agentID, "error", err)
	}
}

// handlePiToolExecutionUpdate counts Pi's cumulative partial result.
func (a *PiAgent) handlePiToolExecutionUpdate(raw []byte) {
	var env piToolUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		return
	}
	var partial piPartialResult
	if len(env.PartialResult) > 0 && json.Unmarshal(env.PartialResult, &partial) == nil {
		a.mu.Lock()
		if a.toolStates == nil {
			a.toolStates = make(map[string]*piToolState)
		}
		tool := a.toolStates[env.ToolCallID]
		if tool == nil {
			tool = &piToolState{Order: a.nextToolOrder}
			a.nextToolOrder++
			a.toolStates[env.ToolCallID] = tool
		}
		// json.Unmarshal already copied the RawMessage from the input line. Move
		// that owned slice into the recovery state without a second full copy.
		tool.PartialResult = env.PartialResult
		a.mu.Unlock()
	}

	var full strings.Builder
	for _, c := range partial.Content {
		if c.Type == PiContentBlockText {
			full.WriteString(c.Text)
		}
	}
	var details struct {
		Truncation *struct {
			TotalBytes int64 `json:"totalBytes"`
			Truncated  bool  `json:"truncated"`
		} `json:"truncation"`
	}
	if json.Unmarshal(partial.Details, &details) == nil && details.Truncation != nil && details.Truncation.TotalBytes > 0 {
		// totalBytes counts the full output even when the retained content is truncated.
		a.sink.ReportProgress(OutputExactTotalProgress(env.ToolCallID, details.Truncation.TotalBytes))
	} else if full.Len() > 0 {
		observed := a.observeCumulativeOutput(env.ToolCallID, full.String(), false)
		a.sink.ReportProgress(OutputTotalProgress(env.ToolCallID, observed.Total, observed.Minimum))
	}

	a.mu.Lock()
	toolName := env.ToolName
	if tool := a.toolStates[env.ToolCallID]; tool != nil && tool.ToolName != "" {
		toolName = tool.ToolName
	}
	a.mu.Unlock()
	// Other extensions also use status fields. Only Agent describes a child here.
	if toolName == contracts.PiToolAgent {
		if obs := piSubagentFromDetails(partial.Details, env.ToolCallID, a.toolCallTitle(env.ToolCallID)); obs != nil {
			if err := a.sink.UpsertBackgroundTask(*obs); err != nil {
				slog.Warn("pi subagent upsert failed", "agent_id", a.agentID, "tool_call", env.ToolCallID, "error", err)
			}
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
	tool := a.toolStates[env.ToolCallID]
	delete(a.toolStates, env.ToolCallID)
	a.clearPiQuestionToolLocked(env.ToolCallID)
	title := ""
	if tool != nil {
		title = tool.Description
	}
	a.mu.Unlock()
	a.clearCumulativeOutput(env.ToolCallID)
	a.sink.ReportProgress(CompleteOutputProgress(env.ToolCallID))

	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: raw}, SpanInfo{
		SpanID:   env.ToolCallID,
		SpanType: env.ToolName,
		Closing:  true,
	}); err != nil {
		slog.Error("pi persist tool_execution_end", "agent_id", a.agentID, "error", err)
	}
	a.sink.CloseSpan(env.ToolCallID)
	a.reportPiGoalResult(env.ToolName, env.Result)

	prompt := a.toolCallPrompts.take(env.ToolCallID)
	if env.ToolName == contracts.PiToolAgent {
		piApplySubagentEnd(a.sink, env.Result, env.ToolCallID, title, prompt)
	} else if env.ToolName == contracts.PiToolSubagentWorkflow && !env.IsError {
		var result struct {
			Details struct {
				TaskID string `json:"taskId"`
			} `json:"details"`
		}
		if json.Unmarshal(env.Result, &result) == nil && result.Details.TaskID != "" {
			logUpsertRefusal(a.sink.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: result.Details.TaskID, Kind: bgtask.KindSubagent, Title: title, Status: bgtask.StatusRunning,
			}))
		}
	}
}

func (a *PiAgent) discardIncompletePiTools() {
	a.mu.Lock()
	a.clearPiQuestionStateLocked()
	a.toolStates = nil
	a.nextToolOrder = 0
	a.mu.Unlock()
	a.toolCallPrompts.clear()
}

func (a *PiAgent) persistIncompletePiTools(completion MessageCompletion) {
	if a.isDiscardingOutput() {
		a.discardIncompletePiTools()
		return
	}
	a.mu.Lock()
	toolCallIDs := make([]string, 0, len(a.toolStates))
	tools := make(map[string]piToolState, len(a.toolStates))
	for toolCallID, tool := range a.toolStates {
		if tool == nil {
			continue
		}
		toolCallIDs = append(toolCallIDs, toolCallID)
		tools[toolCallID] = *tool
	}
	a.toolStates = nil
	a.clearPiQuestionStateLocked()
	a.nextToolOrder = 0
	a.mu.Unlock()
	for _, toolCallID := range toolCallIDs {
		a.toolCallPrompts.take(toolCallID)
	}
	sort.Slice(toolCallIDs, func(left, right int) bool {
		leftTool, rightTool := tools[toolCallIDs[left]], tools[toolCallIDs[right]]
		if leftTool.Order != rightTool.Order {
			return leftTool.Order < rightTool.Order
		}
		return toolCallIDs[left] < toolCallIDs[right]
	})

	for _, toolCallID := range toolCallIDs {
		tool := tools[toolCallID]
		// The cumulative text and the live counter belong to the CALL, not to the row.
		// tool_execution_partial observes output for a call whose start frame this
		// worker never saw, and CumulativeOutputCounter then retains the whole text --
		// so the release must run before the row guard below, or that text stays for
		// the life of the process on every path that does not end in agent_end.
		a.clearCumulativeOutput(toolCallID)
		a.sink.ReportProgress(CompleteOutputProgress(toolCallID))
		// An agent that never announced the call opened no row and reserved no span.
		if len(tool.StartFrame) == 0 {
			continue
		}
		supplement, err := buildPiIncompleteToolSupplement(toolCallID, tool)
		if err != nil {
			slog.Warn("marshal incomplete pi tool", "agent_id", a.agentID, "tool_call_id", toolCallID, "error", err)
			continue
		}
		// The row is the agent's own start frame. The partial result Pi did report is
		// recovered provider data, and LeapMux's completion column states that the
		// call did not finish -- an earlier build declared `isError: true` instead,
		// which claimed a failure no event reported.
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{
			Original: tool.StartFrame, Supplemental: supplement, Completion: completion,
		}, SpanInfo{
			SpanID: toolCallID, SpanType: tool.ToolName, Closing: true,
		}); err != nil {
			slog.Error("persist incomplete pi tool", "agent_id", a.agentID, "tool_call_id", toolCallID, "error", err)
		}
		a.sink.CloseSpan(toolCallID)
	}
}

func (a *PiAgent) handlePiQueueUpdate(raw []byte) {
	var env piQueueUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi queue_update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	// No browser code reads these keys, nor any other pi_* session-info key, so
	// they stay out of contracts/session-info.json (which holds only the tokens
	// both sides read). Render them or delete the broadcasts:
	// https://github.com/leapmux/leapmux/issues/433
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
		question, answered := a.preparePiQuestionDialog(head.ID, raw)
		if answered {
			return
		}
		if err := a.sink.PublishControlRequest(ControlRequest{RequestID: head.ID, Payload: raw, SourceSeq: a.piControlSourceSeq(question)}); err != nil {
			slog.Error("publish pi control request", "agent_id", a.agentID, "request_id", head.ID, "error", err)
			// Pi offers cancellation but no error response for extension dialogs.
			response, marshalErr := json.Marshal(map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": head.ID, "cancelled": true})
			if marshalErr != nil {
				slog.Error("encode pi control cancellation", "agent_id", a.agentID, "error", marshalErr)
				return
			}
			if err := a.SendRawInput(response); err != nil {
				slog.Warn("send pi control cancellation", "agent_id", a.agentID, "error", err)
			}
		}
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
	// No browser code reads pi_status, pi_widget, pi_terminal_title or
	// pi_editor_text, so they stay out of contracts/session-info.json. Render them
	// or delete the broadcasts: https://github.com/leapmux/leapmux/issues/433
	case contracts.PiExtensionMethodSetStatus:
		if head.StatusKey == piGoalDisplayKey {
			a.schedulePiGoalRefresh(false)
		}
		statusValue := any(nil)
		if head.StatusText != nil {
			statusValue = *head.StatusText
		}
		a.sink.BroadcastSessionInfo(map[string]any{
			"pi_status": map[string]any{head.StatusKey: statusValue},
		})
	case contracts.PiExtensionMethodSetWidget:
		if head.WidgetKey == piGoalDisplayKey {
			a.schedulePiGoalRefresh(false)
		}
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
