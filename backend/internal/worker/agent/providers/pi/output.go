package pi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// piNotificationEvents reads the membership that the browser reads also.
var piNotificationEvents = func() map[string]struct{} {
	events := make(map[string]struct{}, len(contracts.PiNotificationEventMembers))
	for _, event := range contracts.PiNotificationEventMembers {
		events[event] = struct{}{}
	}
	return events
}()

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
	contracts.PiToolCallIdentity
	ParentToolCallID string          `json:"parentToolCallId"`
	Args             json.RawMessage `json:"args"`
	Input            json.RawMessage `json:"input"`
	Result           json.RawMessage `json:"result"`
	IsError          bool            `json:"isError"`
}

// piToolUpdateEnvelope adds the cumulative output and the structured details
// that the pi-subagents extension carries.
type piToolUpdateEnvelope struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	PartialResult json.RawMessage `json:"partialResult"`
}

type piPartialResult struct {
	Content []piContentBlock `json:"content"`
	// Details carries provider-specific structured data. For the
	// pi-subagents extension it holds {status, activity, agentId}.
	Details json.RawMessage `json:"details"`
}

// piContentBlock is one block of a partial result or of a message's content. A
// block whose Type is ContentBlockText carries Text, and a block whose Type is
// ContentBlockThinking carries Thinking. The other types carry neither.
//
// It is a NAMED type so piJoinOutputTail can take the slice. The anonymous struct
// it replaced could not cross a function boundary without a second spelling of the
// same fields.
type piContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

type piToolState struct {
	nested   bool
	ToolName string
	Args     json.RawMessage
	// StartFrame is the tool_execution_start event Pi sent, byte for byte. A turn
	// that ends while the call runs stores THAT frame, so the transcript never holds
	// an event Pi did not send.
	StartFrame    []byte
	PartialResult json.RawMessage
	Description   string
	Order         uint64
	rowKey        string
	resumed       bool
}

// piExtensionUIRequestHeader captures the routing fields of an
// extension_ui_request event. The full payload is forwarded verbatim through
// PublishControlRequest / PersistLeapMuxNotification, so ReadControlRequest and
// the browser's renderers can read every method-specific field.
type piExtensionUIRequestHeader struct {
	ID        string `json:"id"`
	Method    string `json:"method"`
	StatusKey string `json:"statusKey"`
	WidgetKey string `json:"widgetKey"`
	// Timeout is the wait in milliseconds after which Pi answers a dialog itself,
	// with its default. Zero states no limit.
	Timeout float64 `json:"timeout"`
}

// deadline returns the wait after which Pi answers the dialog itself, or zero
// when Pi waits with no limit.
func (h piExtensionUIRequestHeader) deadline() time.Duration {
	return providerkit.DialogTimeoutDuration(h.Timeout)
}

// piAgentEndMessage holds the native outcome fields of one assistant message.
type piAgentEndMessage struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// piAgentEndMessages requires an array when the native field is present.
// An omitted field keeps the supported empty-message outcome.
type piAgentEndMessages []piAgentEndMessage

func (messages *piAgentEndMessages) UnmarshalJSON(raw []byte) error {
	if providerkit.RawKindOf(raw) == providerkit.RawNull {
		return fmt.Errorf("the Pi messages field must be an array")
	}
	var decoded []piAgentEndMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*messages = decoded
	return nil
}

// piRetryFlag accepts an optional native boolean. An absent field keeps false.
// JSON null is not a boolean and must not clear an active turn.
type piRetryFlag bool

func (flag *piRetryFlag) UnmarshalJSON(raw []byte) error {
	if providerkit.RawKindOf(raw) == providerkit.RawNull {
		return fmt.Errorf("the Pi willRetry field must be a boolean")
	}
	var retry bool
	if err := json.Unmarshal(raw, &retry); err != nil {
		return err
	}
	*flag = piRetryFlag(retry)
	return nil
}

// piAgentEndEnvelope supplies the final assistant outcome and the retry decision.
//
// Pi stamps WillRetry on each agent_end. It is true only when all these conditions hold:
//   - Retries are enabled.
//   - The retry budget remains available.
//   - The last assistant message failed with a transient error.
//
// An absent field decodes to false.
type piAgentEndEnvelope struct {
	Messages  piAgentEndMessages `json:"messages"`
	WillRetry piRetryFlag        `json:"willRetry"`
}

// piRetryableWebSocketError is the exact errorMessage Pi emits for transient
// WebSocket disconnects, which the input queue retries automatically.
const piRetryableWebSocketError = "WebSocket error"

// piDialogMethods is the set of extension UI methods that block waiting for an
// extension_ui_response. These are surfaced as control requests so the
// frontend can render a dialog and send a decision back.
var piDialogMethods = map[string]struct{}{
	contracts.PiDialogMethodSelect:  {},
	contracts.PiDialogMethodConfirm: {},
	contracts.PiDialogMethodInput:   {},
	contracts.PiDialogMethodEditor:  {},
}

// handlePiOutput dispatches a single parsed Pi event line.
func handlePiOutput(a *Agent, line *providerkit.ParsedLine) {
	var observedAt time.Time
	if line.Type == contracts.PiEventAgentStart || line.Type == contracts.PiEventAgentSettled {
		// Read the clock before enqueue and outside Mu. Replay keeps this time.
		observedAt = a.Clock().Now()
	}
	a.interruptOutput.enqueue(line, observedAt, a.dispatchPiOutput)
}

func handlePiOutputObserved(a *Agent, line *providerkit.ParsedLine, observedAt time.Time) {
	slog.Debug("pi HandleOutput", "agent_id", a.AgentID(), "type", line.Type, "len", len(line.Raw))

	if line.Type == contracts.PiEventResponse && a.handlePiReplyResponse(line) {
		return
	}
	switch line.Type {
	case contracts.PiEventAgentStart:
		a.handlePiAgentStartObserved(observedAt)
	case contracts.PiEventAgentEnd:
		a.handlePiAgentEnd(line.Raw)
	case contracts.PiEventAgentSettled:
		a.handlePiAgentSettled(line.Raw, observedAt)
	case contracts.PiEventTurnEnd:
		a.updatePiAssistantBoundary(line.Raw, true)
	case contracts.PiEventMessageStart:
		a.updatePiAssistantBoundary(line.Raw, false)
	case contracts.PiEventTurnStart:
		// This native lifecycle marker requires no UI state change.
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
	case piEventQueueUpdate:
		// Queue depth values are unused by the browser. Consume the native
		// event so the unknown-event route never persists it.
		// https://github.com/leapmux/leapmux/issues/433
	case contracts.PiEventCompactionStart:
		a.Mu.Lock()
		a.compactionLive = true
		a.Mu.Unlock()
		providerkit.PublishCompaction(a.sink, providerkit.StringField(line.Raw, "reason"))
	case contracts.PiEventThinkingLevelChanged:
		a.handlePiThinkingLevelChanged(line.Raw)
	case contracts.PiEventSessionInfoChanged:
		// Pi's own session NAME. LeapMux gives its tabs their own names and never
		// shows this one, so the row would say nothing a reader can act on.
	case contracts.PiEventBashExecutionUpdate:
		// One delta of a `bash` command an RPC client issued, and Pi emits one event
		// per CHUNK. The `default` branch persisted each of them as its own message,
		// so a single command wrote a row for every chunk of its output.
		//
		// Two facts make the drop right, and each one is enough by itself.
		//
		// LeapMux starts no shell of its own. Every command it builds reaches Pi
		// through beginPiCommand, whose method is one of the ten PiCommand* constants
		// in protocol.go, and none of them is `bash`. The other stdin path,
		// SendRawInput, carries an ANSWER to a request Pi published.
		//
		// No record can attribute the text to a tool call either.
		// A Pi tool span uses the toolCallId from tool_execution_start.
		// contracts/pi-protocol.json holds only bash_execution_update for shell output.
		// No start or end frame pairs that shell with a tool call.
	case contracts.PiEventExtensionUIRequest:
		a.handlePiExtensionUIRequest(line.Raw)
	case contracts.PiEventEntryAppended:
		a.handlePiEntryAppended(line.Raw)
	case contracts.PiEventResponse:
		// Should have been intercepted by handlePiResponse; reaching here means
		// no caller was waiting on this id. Log and drop.
		slog.Warn("pi orphan response line", "agent_id", a.AgentID(), "len", len(line.Raw))
	default:
		if _, notification := piNotificationEvents[line.Type]; notification {
			if _, err := a.orderedSink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw}); err != nil {
				slog.Error("pi persist notification", "agent_id", a.AgentID(), "type", line.Type, "error", err)
			}
			if line.Type == contracts.PiEventCompactionEnd {
				a.endPiCompaction()
			}
			return
		}
		// Persist unknown event types so the user can still see them.
		if err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw}, agent.SpanInfo{}); err != nil {
			slog.Error("pi persist unknown event", "agent_id", a.AgentID(), "type", line.Type, "error", err)
		}
	}
}

func (a *Agent) endPiCompaction() {
	a.Mu.Lock()
	a.compactionLive = false
	a.Mu.Unlock()
	providerkit.EndCompaction(a.sink)
}

// PublishTurnActive republishes the Worker-visible turn state from
// currentTurnActive, the single source. Call it after EVERY critical section
// that writes that field.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and a missing call is the only way the two can drift.
// Never called with a.Mu held: the sink broadcasts, and a broadcast can block on
// a slow transport.
//
// An agent_end with willRetry keeps the turn open across Pi's own retry.
// The state stays busy during the full backoff, even when no output or envelope arrives.
// Inferring idle would hide the spinner and Interrupt button while the run still remains active.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.currentTurnActive
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(a.orderedSink(), active, seq)
}

func (a *Agent) handlePiAgentStartObserved(startedAt time.Time) {
	a.Mu.Lock()
	firstRun := !a.currentTurnActive
	a.currentTurnActive = true
	a.heldDivider = nil
	if firstRun {
		a.turnStartedAt = startedAt
		a.interruptRequests = nil
		a.turnGeneration++
		if run := a.replyRun; run != nil {
			run.started = run.receipt.SessionID == a.sessionHandleLocked()
		}
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetModelProgress())
	// Only a new turn needs a probe for a session that an extension replaced.
	if firstRun && a.canRequestPiSessionStats() {
		go func() { _, _ = a.refreshPiSessionStats(piSessionStatsTimeout(a.APITimeout())) }()
	}
}

func (a *Agent) handlePiAgentEnd(raw []byte) {
	var env piAgentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi agent_end unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	willRetry := bool(env.WillRetry)
	interrupted := !willRetry && a.takePiInterruptOutcome(env)
	if willRetry {
		providerkit.DiscardGenerationScopes(&a.generationBuffer, a.sink, a.generationBuffer.ScopeIDs())
		a.sink.ReportProgress(agent.ResetModelProgress())
		a.Mu.Lock()
		a.clearPiQuestionStateLocked()
		a.Mu.Unlock()
	} else {
		completion := env.retainedCompletion()
		if interrupted {
			completion = agent.MessageCompletionInterrupted
		}
		a.flushPiGeneration(completion)
		if interrupted {
			a.persistIncompletePiTools(completion)
			a.ResetCumulativeOutput()
		} else {
			a.Mu.Lock()
			a.clearPiQuestionStateLocked()
			a.Mu.Unlock()
			a.clearStartlessPiTools()
		}
	}
	a.Mu.Lock()
	a.heldDivider = &piHeldDivider{raw: append([]byte(nil), raw...), env: env, willRetry: willRetry, interrupted: interrupted}
	a.Mu.Unlock()
	// Another run can continue this turn. Only agent_settled writes its final divider.
	if a.canRequestPiSessionStats() {
		go func() { _, _ = a.refreshPiSessionStats(piSessionStatsTimeout(a.APITimeout())) }()
	}
}

// Only Pi's explicit aborted stop reason resolves an uncertain write.
// A generic error or a normal completion does not prove that the abort reached Pi.
func (env piAgentEndEnvelope) statesNativeAbort() bool {
	last := env.lastAssistantMessage()
	return last != nil && last.StopReason == contracts.PiStopReasonAborted
}

func (env piAgentEndEnvelope) lastAssistantMessage() *piAgentEndMessage {
	for i := len(env.Messages) - 1; i >= 0; i-- {
		if env.Messages[i].Role == contracts.PiRoleAssistant {
			return &env.Messages[i]
		}
	}
	return nil
}

func (env piAgentEndEnvelope) statesNativeCompletion() bool {
	last := env.lastAssistantMessage()
	// A tool-use message precedes a running tool and does not prove turn completion.
	return last != nil && (last.StopReason == contracts.PiStopReasonStop || last.StopReason == contracts.PiStopReasonLength)
}

func (a *Agent) takePiInterruptOutcome(env piAgentEndEnvelope) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	interrupted := env.statesNativeAbort() || a.piInterruptDeliveredLocked()
	if env.statesNativeCompletion() {
		interrupted = false
	}
	// A write or acknowledgement can reach Pi after it naturally completed.
	// The native completion therefore wins over every local delivery state.
	a.interruptRequests = nil
	return interrupted
}

// isRetryableFailure reports whether the turn ended on the one transient
// failure that LeapMux itself retries automatically.
func (env piAgentEndEnvelope) isRetryableFailure() bool {
	// Walk from the end: only the final assistant message reflects the
	// turn's final outcome; earlier assistant entries are intra-turn.
	last := env.lastAssistantMessage()
	return last != nil && last.StopReason == contracts.PiStopReasonError && last.ErrorMessage == piRetryableWebSocketError
}

func (env piAgentEndEnvelope) retainedCompletion() agent.MessageCompletion {
	last := env.lastAssistantMessage()
	if last != nil && last.StopReason == contracts.PiStopReasonError {
		return agent.MessageCompletionError
	}
	return agent.MessageCompletionInterrupted
}

// handlePiMessageEnd persists one finished message. The thinking of an assistant
// message becomes a row of its own before it (see persistPiThinking).
func (a *Agent) handlePiMessageEnd(raw []byte) {
	var header struct {
		Message map[string]json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &header) == nil {
		role := providerkit.ObjectString(header.Message, "role")
		switch role {
		case piRoleUser:
			if a.observePiSteerEcho(piSteerMessageInputID(header.Message)) {
				return
			}
			if text, ok := piUserMessageTextFromContent(header.Message["content"]); ok && a.steers.EchoKey(text) {
				a.steers.Settle(a.orderedSink())
			}
			return
		case piRoleToolResult, piRoleSystem, piRoleBashExecution:
			return
		case contracts.PiRoleAssistant, contracts.PiRoleCustom:
			// Both roles carry transcript content.
		default:
			// Preserve an unknown role for inspection.
		}
	}
	content := a.piMessageEndContent(raw)
	content.Completion = a.piMessageTextCompletion(raw)
	// Update child status from the nested custom message before persisting it.
	piApplySubagentNotificationTracked(a.sink, raw, a.trackOpenBgTask, a.untrackOpenBgTask)
	a.persistPiThinking(raw)
	if err := a.persistPiReplyRow(content, agent.SpanInfo{}); err != nil {
		slog.Error("pi persist message_end", "agent_id", a.AgentID(), "error", err)
		if !agent.OutputRetained(err) {
			return
		}
	}
	providerkit.DiscardGenerationScopes(&a.generationBuffer, a.sink, a.generationBuffer.ScopeIDs())
}

// piUserMessageText reads the text of a user echo and rejects malformed content.
// A valid empty text can match a steer that carries only images.
func piUserMessageText(raw []byte) (string, bool) {
	var envelope struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", false
	}
	return piUserMessageTextFromContent(envelope.Message.Content)
}

func piUserMessageTextFromContent(content json.RawMessage) (string, bool) {
	if text, ok := providerkit.RawString(content); ok {
		return text, true
	}
	var blocks []*piContentBlock
	if json.Unmarshal(content, &blocks) != nil || blocks == nil {
		return "", false
	}
	var text strings.Builder
	for _, block := range blocks {
		if block == nil || block.Type == "" {
			return "", false
		}
		if block.Type == ContentBlockText {
			text.WriteString(block.Text)
		}
	}
	return text.String(), true
}

// piMessageTextCompletion distinguishes failed output from output that a delivered stop interrupted.
// A message with no visible text needs no completion marker.
func (a *Agent) piMessageTextCompletion(raw []byte) agent.MessageCompletion {
	var envelope struct {
		Message struct {
			Role       string           `json:"role"`
			StopReason string           `json:"stopReason"`
			Content    []piContentBlock `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Role != contracts.PiRoleAssistant {
		return ""
	}
	completion := agent.MessageCompletionInterrupted
	switch envelope.Message.StopReason {
	case contracts.PiStopReasonAborted:
	case contracts.PiStopReasonError:
		a.Mu.Lock()
		interrupted := a.piInterruptDeliveredLocked()
		a.Mu.Unlock()
		if !interrupted {
			completion = agent.MessageCompletionError
		}
	default:
		return ""
	}
	for _, block := range envelope.Message.Content {
		if block.Type == ContentBlockText && strings.TrimSpace(block.Text) != "" {
			return completion
		}
	}
	return ""
}

// persistPiThinking persists the visible thinking of one assistant message as a
// reasoning row of its own, before the message's own row.
//
// Pi states a reasoning model's reply as ONE message whose content holds thinking
// blocks beside the text blocks and the tool calls. A transcript row draws one kind
// of text, so a row that drew the message would lose one of the two. The thinking
// takes LeapMux's assembled-message envelope, which the transcript draws as
// thinking for every provider, and which the worker already writes for thinking
// that no message_end completed (flushPiGeneration). The message's own row keeps
// Pi's frame and the usage, and the browser draws its text alone.
func (a *Agent) persistPiThinking(raw []byte) {
	thinking := piMessageThinking(raw)
	if thinking == "" {
		return
	}
	row, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindReasoning, thinking, agent.MessageCompletionComplete)
	if err != nil {
		slog.Error("pi encode thinking", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row}, agent.SpanInfo{}); err != nil {
		slog.Error("pi persist thinking", "agent_id", a.AgentID(), "error", err)
	}
}

// piMessageThinking joins the visible thinking blocks of an assistant
// message_end into paragraphs, or returns "" when the frame holds none.
//
// A thinking block whose text is blank carries only a signature for the model
// provider, so it shows nothing. A block that a safety filter redacted carries
// Pi's placeholder text, which Pi itself shows, so it stays.
func piMessageThinking(raw []byte) string {
	var envelope struct {
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Role != contracts.PiRoleAssistant {
		return ""
	}
	// A string content holds no blocks, so it holds no thinking.
	var blocks []piContentBlock
	if json.Unmarshal(envelope.Message.Content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == ContentBlockThinking && strings.TrimSpace(block.Thinking) != "" {
			parts = append(parts, block.Thinking)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (a *Agent) handlePiMessageUpdate(raw []byte) {
	var env piMessageUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("pi message_update unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	switch env.AssistantMessageEvent.Type {
	case contracts.PiAssistantEventTextDelta, contracts.PiAssistantEventThinkingDelta:
		if env.AssistantMessageEvent.Delta == "" {
			return
		}
		kind := agent.AssembledMessageKindText
		if env.AssistantMessageEvent.Type == contracts.PiAssistantEventThinkingDelta {
			kind = agent.AssembledMessageKindReasoning
		}
		scopeID := fmt.Sprintf("pi:content:%d", env.AssistantMessageEvent.ContentIndex)
		a.generationBuffer.Append(scopeID, kind, env.AssistantMessageEvent.Delta, providerkit.JoinVerbatim)
		a.sink.ReportProgress(agent.ModelTextProgress(scopeID, env.AssistantMessageEvent.Delta))
	default:
		// All other delta sub-types (text_start/end, thinking_start/end,
		// toolcall_*, start, done, error) are handled via message_end and
		// tool_execution_* events; ignore here to avoid double-rendering.
	}
}

func (a *Agent) flushPiGeneration(completion agent.MessageCompletion) {
	for _, scopeID := range a.generationBuffer.ScopeIDs() {
		kind, text, open := a.generationBuffer.ScopeSnapshot(scopeID)
		if open && kind == agent.AssembledMessageKindText && agent.TrimJavaScriptSpace(text) != "" {
			// The transcript can retain this prefix, but Pi never committed
			// it as a message_end in the reply.
			a.noteUnfinishedPiReplyText()
			break
		}
	}
	if err := providerkit.FlushGeneration(&a.generationBuffer, a.sink, completion, a.IsDiscardingOutput()); err != nil {
		slog.Error("pi failed to persist partial generation", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) handlePiToolExecutionStart(raw []byte) {
	var env piToolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("pi tool_execution_start unmarshal failed",
			"agent_id", a.AgentID(), "error", err)
		return
	}

	input := env.Args
	if len(input) == 0 {
		input = env.Input
	}
	description := piExtractDescription(input, env.ToolName)
	a.Mu.Lock()
	a.closedSpans.Reopen(env.ToolCallID)
	if a.toolStates == nil {
		a.toolStates = make(map[string]*piToolState)
	}
	parentSpanID := ""
	parent := a.toolStates[env.ParentToolCallID]
	if env.ParentToolCallID != env.ToolCallID && parent != nil && len(parent.StartFrame) > 0 &&
		parent.ToolName != contracts.PiToolAgent && parent.ToolName != contracts.PiToolSubagentWorkflow {
		parentSpanID = env.ParentToolCallID
	}
	initialRowKey := ""
	if env.ToolName == contracts.PiToolAgent {
		initialRowKey = env.ToolCallID
	}
	a.toolStates[env.ToolCallID] = &piToolState{
		nested:      parentSpanID != "",
		ToolName:    env.ToolName,
		Args:        append(json.RawMessage(nil), input...),
		StartFrame:  append([]byte(nil), raw...),
		Description: description,
		Order:       a.nextToolOrder,
		rowKey:      initialRowKey,
	}
	a.nextToolOrder++
	a.Mu.Unlock()
	// The spawn prompt, kept whole for the child transcript's first message.
	// pi-subagents declares it `prompt` on the nested-agent tool
	// (src/nested-tools.ts); a non-subagent tool simply has none.
	prompt := piExtractPrompt(input)
	if prompt != "" {
		a.toolCallPrompts.Remember(env.ToolCallID, prompt)
	}

	// A subagent spawn owns no span, so it reserves no color either. The
	// subagent's output lands in its own child transcript, so a rail held open
	// for the whole run only pushes every concurrent tool one column right.
	//
	// Pi does not read the recorded span type. tool_execution_end carries its own toolName.
	// providerkit.OpenToolSpanWithParent records the type for both root and nested calls.
	// Closing messages from Claude and Agent Client Protocol (ACP) providers read that recorded type.
	spawns := env.ToolName == contracts.PiToolAgent || env.ToolName == contracts.PiToolSubagentWorkflow
	content := agent.MessageContent{Original: raw}
	var reply *piReplyRun
	var item agent.NativeReplyItem
	if env.ToolName == contracts.PiToolPlanComplete {
		reply, item = a.preparePiReplyRow(&content)
	}
	err := providerkit.OpenToolSpanWithParent(a.orderedSink(), content, env.ToolCallID, env.ToolName, parentSpanID, spawns)
	a.recordPiReplyRow(reply, item, err)
	if err != nil {
		slog.Error("pi persist tool_execution_start", "agent_id", a.AgentID(), "error", err)
	}
	if env.ToolName == contracts.PiToolAgent {
		if resumeID := strings.TrimSpace(providerkit.StringField(input, "resume")); resumeID != "" {
			childID, status, found, err := a.sink.LookupBackgroundTask(resumeID)
			if err != nil {
				slog.Warn("pi resumed subagent lookup failed", "agent_id", a.AgentID(), "resume_id", resumeID, "error", err)
			} else if found {
				if status.IsFinished() {
					providerkit.LogRegistryRefusal("pi", "revive", a.sink.ReviveBackgroundTask(resumeID))
				}
				if childID != "" && prompt != "" {
					if err := a.sink.PersistChildUserMessage(childID, prompt); err != nil {
						slog.Warn("pi persist resumed subagent prompt failed", "agent_id", a.AgentID(), "row_key", resumeID, "error", err)
					}
				}
				a.Mu.Lock()
				if tool := a.toolStates[env.ToolCallID]; tool != nil {
					tool.rowKey = resumeID
					tool.resumed = true
				}
				a.Mu.Unlock()
				return
			}
		}
		piEnsureSubagentChild(
			a.sink, env.ToolCallID, env.ToolCallID, env.ToolCallID,
			description, prompt,
		)
	}
}

// The longest live output tail Pi broadcasts, in bytes.
//
// Only the last bytes of a running tool reach a reader, and the service caps the
// broadcast again at a lower figure. This cap is what keeps the JOIN off the
// accumulated output, which Pi re-sends whole on every update. Goose caps its own
// live tail the same way.
const piLiveOutputLimit = 8192

// The longest TRUNCATED snapshot Pi's byte counter compares, in bytes.
//
// A truncated snapshot lost its head, so it is not append-only and the counter
// measures growth by the overlap of two snapshots. That scan allocates eight bytes
// for each byte of the newer snapshot, and without a cap the allocation grows with
// the output, on every update. The delta stays exact while one update adds less than
// the window; past that the counter reports a floor, which the truncation flag
// already declares the total to be.
const piCountedWindowBytes = 64 << 10

// piJoinOutputTail joins one partial result's text blocks and keeps at most the last limit bytes.
// A limit of zero or less keeps every byte.
//
// total is the summed length that the caller calculated from those blocks.
// The builder allocates exactly what it keeps.
// A capped join therefore allocates only the cap, rather than the full accumulated output.
//
// It reports whether the cap dropped earlier bytes.
func piJoinOutputTail(blocks []piContentBlock, total, limit int) (string, bool) {
	skip := 0
	if limit > 0 && total > limit {
		skip = total - limit
	}
	var joined strings.Builder
	joined.Grow(total - skip)
	for _, block := range blocks {
		if block.Type != ContentBlockText {
			continue
		}
		text := block.Text
		if skip > 0 {
			if len(text) <= skip {
				skip -= len(text)
				continue
			}
			// The cut can land inside a rune, and a replacement character would then
			// reach the browser. Move it to the next boundary. ClipTailBytes repairs
			// a text that is already whole; here the join itself is what cuts, so the
			// repair belongs at the cut and the text is never built whole.
			for skip < len(text) && !utf8.RuneStart(text[skip]) {
				skip++
			}
			text = text[skip:]
			skip = 0
		}
		joined.WriteString(text)
	}
	return joined.String(), limit > 0 && total > limit
}

// handlePiToolExecutionUpdate counts Pi's cumulative partial result.
func (a *Agent) handlePiToolExecutionUpdate(raw []byte) {
	var env piToolUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		return
	}
	var partial piPartialResult
	hasPartial := len(env.PartialResult) > 0 && json.Unmarshal(env.PartialResult, &partial) == nil
	a.Mu.Lock()
	if a.closedSpans.WasClosed(env.ToolCallID) {
		a.Mu.Unlock()
		return
	}
	if hasPartial {
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
	}
	a.Mu.Unlock()

	// Pi re-sends the WHOLE partial result on every update, so a join of every text
	// block costs the accumulated output each time and the cost grows with the call.
	// Sum the lengths and join only the text that a report reads.
	// The tail takes only the last bytes. The byte counter can need more text.
	// It uses one of the two paths below.
	textBytes := 0
	for _, block := range partial.Content {
		if block.Type == ContentBlockText {
			textBytes += len(block.Text)
		}
	}
	var details struct {
		Truncation *struct {
			TotalBytes int64 `json:"totalBytes"`
			Truncated  bool  `json:"truncated"`
		} `json:"truncation"`
	}
	decoded := json.Unmarshal(partial.Details, &details) == nil
	// Use one truncation flag for both reports.
	// The counter previously used false while the tail used the real value.
	// That difference reported an exact total for a truncated window that lacked its first bytes.
	truncated := decoded && details.Truncation != nil && details.Truncation.Truncated
	if decoded && details.Truncation != nil && details.Truncation.TotalBytes > 0 {
		// totalBytes counts the full output even when the retained content is truncated.
		a.sink.ReportProgress(agent.OutputExactTotalProgress(env.ToolCallID, details.Truncation.TotalBytes))
	} else if textBytes > 0 {
		// A snapshot that kept its head is append-only, so the counter takes it WHOLE
		// and its length is the exact total. A truncated one is not append-only: the
		// counter measures growth by the overlap of two snapshots, and the window caps
		// what that scan allocates. See piCountedWindowBytes.
		limit := 0
		if truncated {
			limit = piCountedWindowBytes
		}
		counted, _ := piJoinOutputTail(partial.Content, textBytes, limit)
		observed := a.ObserveCumulativeOutput(env.ToolCallID, counted, truncated)
		a.sink.ReportProgress(agent.OutputTotalProgress(env.ToolCallID, observed.Total, observed.Minimum))
	}
	// Pi sends the full partial result on each update. Its last bytes form the tail.
	// Pi's truncation flag states whether earlier bytes are absent.
	// This cap removes more earlier bytes when the retained output exceeds the live tail's limit.
	if textBytes > 0 {
		tail, clipped := piJoinOutputTail(partial.Content, textBytes, piLiveOutputLimit)
		a.sink.ReportProgress(agent.OutputTailProgress(env.ToolCallID, tail, truncated || clipped))
	}

	a.Mu.Lock()
	toolName := env.ToolName
	rowKey := env.ToolCallID
	resumed := false
	if tool := a.toolStates[env.ToolCallID]; tool != nil {
		if tool.ToolName != "" {
			toolName = tool.ToolName
		}
		if tool.rowKey != "" {
			rowKey = tool.rowKey
		}
		resumed = tool.resumed
	}
	a.Mu.Unlock()
	// Other extensions also use status fields. Only Agent describes a child here.
	if toolName == contracts.PiToolAgent {
		title := a.toolCallTitle(env.ToolCallID)
		if resumed {
			title = ""
		}
		if obs := piSubagentFromDetails(partial.Details, rowKey, title); obs != nil {
			if obs.RowKey != rowKey {
				if err := a.sink.RenameBackgroundTask(rowKey, obs.RowKey); err != nil {
					slog.Warn("pi rename active subagent task failed", "agent_id", a.AgentID(), "tool_call", env.ToolCallID, "child_agent_id", obs.RowKey, "error", err)
					obs.RowKey = rowKey
				} else {
					a.Mu.Lock()
					if tool := a.toolStates[env.ToolCallID]; tool != nil {
						tool.rowKey = obs.RowKey
					}
					a.Mu.Unlock()
					a.renameOpenBgTask(rowKey, obs.RowKey)
				}
			}
			if err := a.sink.UpsertBackgroundTask(*obs); err != nil {
				slog.Warn("pi subagent upsert failed", "agent_id", a.AgentID(), "tool_call", env.ToolCallID, "error", err)
			}
		}
	}
}

func (a *Agent) handlePiToolExecutionEnd(raw []byte) {
	var env piToolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("pi tool_execution_end unmarshal failed",
			"agent_id", a.AgentID(), "error", err)
		return
	}

	a.Mu.Lock()
	if a.closedSpans.WasClosed(env.ToolCallID) {
		a.Mu.Unlock()
		return
	}
	tool := a.toolStates[env.ToolCallID]
	if tool == nil || !tool.nested {
		a.TurnToolUses++
	}
	delete(a.toolStates, env.ToolCallID)
	a.clearPiQuestionToolLocked(env.ToolCallID)
	title := ""
	rowKey := env.ToolCallID
	resumed := false
	var startArgs json.RawMessage
	if tool != nil {
		title = tool.Description
		if tool.rowKey != "" {
			rowKey = tool.rowKey
		}
		resumed = tool.resumed
		startArgs = tool.Args
	}
	a.Mu.Unlock()
	a.ClearCumulativeOutput(env.ToolCallID)
	a.sink.ReportProgress(agent.CompleteOutputProgress(env.ToolCallID))

	span := agent.SpanInfo{
		SpanID:   env.ToolCallID,
		SpanType: env.ToolName,
		Closing:  true,
	}
	var endErr error
	if env.ToolName == contracts.PiToolPlanComplete {
		endErr = a.persistPiReplyRow(agent.MessageContent{Original: raw}, span)
	} else {
		endErr = a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, span)
	}
	if endErr != nil {
		slog.Error("pi persist tool_execution_end", "agent_id", a.AgentID(), "error", endErr)
	}
	a.sink.CloseSpan(env.ToolCallID)
	a.reportPiGoalResult(env.ToolName, env.Result)

	prompt := a.toolCallPrompts.Take(env.ToolCallID)
	switch env.ToolName {
	case contracts.PiToolAgent:
		finalKey, stillRunning := piApplySubagentEndState(a.sink, piSubagentEndInput{
			Result:     env.Result,
			ToolCallID: env.ToolCallID,
			RowKey:     rowKey,
			Title:      title,
			Prompt:     prompt,
			IsError:    env.IsError,
			Resumed:    resumed,
		})
		if stillRunning {
			a.trackOpenBgTask(finalKey)
		} else {
			a.untrackOpenBgTask(rowKey)
			a.untrackOpenBgTask(finalKey)
		}
	case contracts.PiToolSubagentWorkflow:
		if !env.IsError {
			var result struct {
				Details struct {
					TaskID string `json:"taskId"`
				} `json:"details"`
			}
			if json.Unmarshal(env.Result, &result) == nil && result.Details.TaskID != "" {
				logUpsertRefusal(a.sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: result.Details.TaskID, Kind: bgtask.KindSubagent, Title: title, Status: bgtask.StatusRunning,
				}))
				a.trackOpenBgTask(result.Details.TaskID)
			}
		}
	case piToolGetSubagentResult:
		args := env.Args
		if len(args) == 0 {
			args = startArgs
		}
		if closedKey := piApplyGetSubagentResult(a.sink, args, env.Result); closedKey != "" {
			a.untrackOpenBgTask(closedKey)
		}
	}
}

func (a *Agent) discardIncompletePiToolsWithStatus(status bgtask.Status) {
	a.Mu.Lock()
	var openAgentRows []string
	for toolCallID, tool := range a.toolStates {
		if tool != nil && tool.ToolName == contracts.PiToolAgent {
			rowKey := tool.rowKey
			if rowKey == "" {
				rowKey = toolCallID
			}
			openAgentRows = append(openAgentRows, rowKey)
		}
	}
	a.clearPiQuestionStateLocked()
	a.toolStates = nil
	a.nextToolOrder = 0
	a.Mu.Unlock()
	a.toolCallPrompts.Clear()
	if a.sink == nil {
		return
	}
	for _, rowKey := range openAgentRows {
		providerkit.LogRegistryRefusal("pi", "close", a.sink.CloseBackgroundTask(rowKey, status))
		a.untrackOpenBgTask(rowKey)
	}
}

// clearStartlessPiTools releases tool_execution_update state and cumulative
// output counters for calls that never observed a tool_execution_start, while
// preserving started open tools across normal and error turn endings.
func (a *Agent) clearStartlessPiTools() {
	a.Mu.Lock()
	var orphanIDs []string
	for toolCallID, tool := range a.toolStates {
		if tool == nil || len(tool.StartFrame) == 0 {
			delete(a.toolStates, toolCallID)
			orphanIDs = append(orphanIDs, toolCallID)
		}
	}
	a.Mu.Unlock()
	sort.Strings(orphanIDs)
	for _, toolCallID := range orphanIDs {
		a.ClearCumulativeOutput(toolCallID)
		a.sink.ReportProgress(agent.CompleteOutputProgress(toolCallID))
	}
}

func (a *Agent) persistIncompletePiTools(completion agent.MessageCompletion) {
	status := agent.IncompleteTaskStatus(completion)
	if a.IsDiscardingOutput() {
		a.discardIncompletePiToolsWithStatus(status)
		return
	}
	a.Mu.Lock()
	toolCallIDs := make([]string, 0, len(a.toolStates))
	tools := make(map[string]piToolState, len(a.toolStates))
	for toolCallID, tool := range a.toolStates {
		if tool == nil {
			continue
		}
		a.closedSpans.MarkClosed(toolCallID)
		toolCallIDs = append(toolCallIDs, toolCallID)
		tools[toolCallID] = *tool
	}
	a.toolStates = nil
	a.clearPiQuestionStateLocked()
	a.nextToolOrder = 0
	a.Mu.Unlock()
	for _, toolCallID := range toolCallIDs {
		a.toolCallPrompts.Take(toolCallID)
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
		// The cumulative text and live counter belong to the call.
		// tool_execution_update can arrive without a start frame, and CumulativeOutputCounter retains its full text.
		// Release that text before the row guard below.
		// Otherwise, paths without agent_end retain it for the full process lifetime.
		a.ClearCumulativeOutput(toolCallID)
		a.sink.ReportProgress(agent.CompleteOutputProgress(toolCallID))
		if tool.ToolName == contracts.PiToolAgent {
			rowKey := tool.rowKey
			if rowKey == "" {
				rowKey = toolCallID
			}
			providerkit.LogRegistryRefusal("pi", "close", a.sink.CloseBackgroundTask(rowKey, status))
			a.untrackOpenBgTask(rowKey)
		}
		// An agent that never announced the call opened no row and reserved no span.
		if len(tool.StartFrame) == 0 {
			continue
		}
		supplement, err := buildPiIncompleteToolSupplement(toolCallID, tool)
		if err != nil {
			slog.Warn("marshal incomplete pi tool", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "error", err)
			continue
		}
		// The row retains the agent's native start frame and the partial result that Pi reported.
		// LeapMux's completion column states that the call did not finish.
		// The previous isError:true field claimed a failure that no native event reported.
		if err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original: tool.StartFrame, Supplemental: supplement, Completion: completion,
		}, agent.SpanInfo{
			SpanID: toolCallID, SpanType: tool.ToolName, Closing: true,
		}); err != nil {
			slog.Error("persist incomplete pi tool", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "error", err)
		}
		a.sink.CloseSpan(toolCallID)
	}
}

// handlePiThinkingLevelChanged copies Pi's confirmed thinking level into the agent settings.
//
// Pi restricts the level to the model's supported levels.
// setModel calls setThinkingLevel, which lowers an unsupported level and announces the new value.
// A native model switch can therefore change the level without a LeapMux request.
// Keeping the old thinkingLevel would show a value that the active model no longer uses.
//
// The row itself stays out of the transcript. `PersistSettingsRefresh` announces the
// change through the same settings pipeline every other axis uses, and a raw event row
// beside that notification would state the same fact a second time.
func (a *Agent) handlePiThinkingLevelChanged(raw []byte) {
	var env struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Level == "" {
		slog.Warn("The Pi thinking_level_changed event is invalid.", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.publishPiSettings(func() map[string]string {
		if a.thinkingLevel == env.Level {
			return nil
		}
		a.thinkingLevel = env.Level
		return map[string]string{agent.OptionIDEffort: env.Level}
	})
}

// publishPiSettings applies one event and publishes only the axes that the event confirms.
// The settings pipeline preserves each absent axis.
// Pi can send a thinking event before set_model replies, while the local model still holds
// the previous model. A complete local snapshot would overwrite the requested model during startup.
// apply runs under Mu and returns nil when the event changes nothing.
func (a *Agent) publishPiSettings(apply func() map[string]string) {
	a.Mu.Lock()
	refresh := apply()
	a.Mu.Unlock()
	if len(refresh) == 0 {
		return
	}
	a.sink.PersistSettingsRefresh(refresh)
}

// These constants identify native session entries. Visible entry types gain shared readers in the plugin.
const (
	// Custom data stays internal. A custom message needs an explicit display flag.
	piEntryCustom = "custom"

	piEntryUsage       = "usage"
	piEntryContextEdit = "context_edit"
	// piEntryModelChange states the confirmed model, even when LeapMux did not request it.
	// A native build can append it inside setModel without an event.
	// applyModel therefore reads get_state. See handlePiModelChangeEntry.
	piEntryModelChange = "model_change"
	// Message entries repeat the message stream. Branch summaries, labels, and session info are internal state.
	// Thinking-level entries repeat the settings event.
	// An extension compaction has no lifecycle pair and needs its own boundary.
	piEntryMessage = "message"

	piEntryBranchSummary       = "branch_summary"
	piEntryLabel               = "label"
	piEntrySessionInfo         = "session_info"
	piEntryThinkingLevelChange = "thinking_level_change"
	// piCustomTypeGoalFocus marks the extension entry that moves a session goal.
	piCustomTypeGoalFocus = "pi-goal-focus"
)

// handlePiEntryAppended reads a native session entry.
// Usage entries record cache-warm costs that the stats refresh reads. Context edits describe retries.
// Custom entries are extension data. Only displayed custom messages become transcript rows.
// An extension compaction has no start or end event, so its entry supplies the boundary.
// Unknown entry types remain available for inspection.
func (a *Agent) handlePiEntryAppended(raw []byte) {
	var event struct {
		Entry struct {
			Type       string          `json:"type"`
			CustomType string          `json:"customType"`
			Provider   string          `json:"provider"`
			ModelID    string          `json:"modelId"`
			Display    json.RawMessage `json:"display"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		// Keep the raw frame in the transcript when a changed envelope cannot decode.
		// Returning without a row hid every entry and left only a worker log.
		// The reader cannot see that log but can inspect the raw frame's card.
		slog.Warn("pi entry_appended unmarshal failed", "agent_id", a.AgentID(), "error", err)
		if err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
			slog.Error("persist an unreadable Pi session entry", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	// ABOVE the switch, because `customType` is a field of EVERY entry. A goal marker
	// that a later Pi build writes on a new entry type still refreshes the panel, and
	// the panel otherwise keeps a goal the session already moved past.
	if event.Entry.CustomType == piCustomTypeGoalFocus {
		a.schedulePiGoalRefresh(false)
	}
	switch event.Entry.Type {
	case contracts.PiEntryTypeCustomMessage:
		var display bool
		if json.Unmarshal(event.Entry.Display, &display) != nil || !display {
			return
		}
		if err := a.persistPiReplyRow(agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
			slog.Error("persist Pi session entry", "agent_id", a.AgentID(), "error", err)
		}
	case contracts.PiEntryTypeCompaction:
		a.Mu.Lock()
		live := a.compactionLive
		a.Mu.Unlock()
		if live {
			return
		}
		if _, err := a.orderedSink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
			slog.Error("pi failed to persist a compaction entry", "agent_id", a.AgentID(), "error", err)
		}
	case piEntryModelChange:
		a.handlePiModelChangeEntry(event.Entry.Provider, event.Entry.ModelID)
	case piEntryMessage, piEntryCustom, piEntryUsage, piEntryContextEdit, piEntryBranchSummary, piEntryLabel,
		piEntrySessionInfo, piEntryThinkingLevelChange:
		// Dropped on purpose. The drop list above states the reason for each one.
	default:
		// The row reaches the transcript, for the reason the unmarshal path above
		// states: a drop leaves the reader nothing and the worker a log line the
		// reader never sees. The raw frame at least draws as an inspectable card,
		// and the outer event switch answers an unknown EVENT the same way.
		slog.Debug("pi session entry of an unknown type", "agent_id", a.AgentID(), "type", event.Entry.Type)
		if err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
			slog.Error("persist an unknown Pi session entry", "agent_id", a.AgentID(), "type", event.Entry.Type, "error", err)
		}
	}
}

// handlePiModelChangeEntry copies Pi's confirmed model into the settings.
//
// applyModel also reads get_state after set_model replies, because a build can omit the model-change event.
// handlePiThinkingLevelChanged reads the separate thinking-level event.
// Both handlers accept native changes that LeapMux did not request.
func (a *Agent) handlePiModelChangeEntry(provider, modelID string) {
	if modelID == "" {
		return
	}
	a.publishPiSettings(func() map[string]string {
		if a.model == modelID && (provider == "" || a.provider == provider) {
			return nil
		}
		a.model = modelID
		refresh := map[string]string{agent.OptionIDModel: modelID}
		if provider != "" {
			a.provider = provider
			refresh[OptionProvider] = provider
		}
		return refresh
	})
}

// withdrawTimedOutPiDialog withdraws the card of a dialog that Pi answered itself
// when its deadline passed. It forgets the dialog's question state, as a
// cancellation does, so a late answer with its id starts no exchange.
func (a *Agent) withdrawTimedOutPiDialog(id string) {
	a.Mu.Lock()
	if dialog := a.questionDialogs[id]; dialog != nil {
		delete(a.questionDialogs, id)
		delete(a.customQuestionAnswers, dialog.Key)
	}
	delete(a.openDialogs, id)
	a.Mu.Unlock()
	a.sink.CancelControlRequest(id)
}

// handlePiExtensionUIRequest routes a Pi extension_ui_request event to either
// a control request (dialog methods) or a session-info / notification
// broadcast (fire-and-forget methods).
func (a *Agent) handlePiExtensionUIRequest(raw []byte) {
	var head piExtensionUIRequestHeader
	if err := json.Unmarshal(raw, &head); err != nil {
		slog.Warn("pi extension_ui_request unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	if _, isDialog := piDialogMethods[head.Method]; isDialog {
		if head.ID == "" {
			slog.Warn("pi extension_ui_request dialog missing id",
				"agent_id", a.AgentID(), "method", head.Method)
			return
		}
		// The fresh-implementation settings dialog follows an approval this
		// worker already answered for, and no ask-user-question call backs it,
		// so it is consumed here rather than published.
		if a.answerPiFreshSettingsDialog(head.ID, raw) {
			return
		}
		question, answered := a.preparePiQuestionDialog(head.ID, raw)
		if answered {
			return
		}
		// Recorded BEFORE the publish, for the same reason as the deadline below: an
		// answer that the reader sends at once must find the dialog to forget.
		a.rememberOpenDialog(head.ID)
		// Pi answers a dialog whose deadline passes itself, with its default, and
		// tells the host nothing, so the worker withdraws the card when that
		// deadline passes. The deadline is armed BEFORE the publish, so an answer
		// that the reader sends at once finds it to disarm.
		a.dialogDeadlines.Arm(a.Clock(), head.ID, head.deadline(), func() { a.withdrawTimedOutPiDialog(head.ID) })
		receipt := a.piReplyControlReceipt()
		if err := a.sink.PublishControlRequest(agent.ControlRequest{
			AgentSessionID: receipt.SessionID, NativeTurnID: receipt.TurnID, RequestID: head.ID,
			Payload: raw, SourceSeq: a.piControlSourceSeq(question),
		}); err != nil {
			a.dialogDeadlines.Disarm(head.ID)
			a.forgetOpenDialog(head.ID)
			slog.Error("publish pi control request", "agent_id", a.AgentID(), "request_id", head.ID, "error", err)
			// Pi offers cancellation but no error response for extension dialogs.
			answer, marshalErr := piCancelResponse(head.ID).answer()
			if marshalErr != nil {
				slog.Error("encode pi control cancellation", "agent_id", a.AgentID(), "error", marshalErr)
				return
			}
			if err := a.SendRawInput(agent.RawInput{Data: answer.Content}); err != nil {
				slog.Warn("send pi control cancellation", "agent_id", a.AgentID(), "error", err)
			}
		}
		return
	}

	switch head.Method {
	case contracts.PiExtensionMethodNotify:
		// Store the native envelope with the AGENT source.
		// The Pi plugin reads the level and message from notifyType and message.
		if _, err := a.orderedSink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
			slog.Error("pi persist notify", "agent_id", a.AgentID(), "error", err)
		}
	case piExtensionMethodSetStatus:
		if head.StatusKey == piGoalDisplayKey {
			a.schedulePiGoalRefresh(false)
		}
		// Display-only: no browser reads pi_status. Consume without broadcast.
		// https://github.com/leapmux/leapmux/issues/433
	case piExtensionMethodSetWidget:
		if head.WidgetKey == piGoalDisplayKey {
			a.schedulePiGoalRefresh(false)
		}
		// Display-only: no browser reads pi_widget. Consume without broadcast.
	case piExtensionMethodSetTitle:
		// Display-only: no browser reads pi_terminal_title. Consume without broadcast.
	case piExtensionMethodSetEditorText:
		// Display-only: no browser reads pi_editor_text. Consume without broadcast.
	default:
		// Store an unknown extension method so the user can inspect it.
		// Pi sent the frame, so its source is AGENT.
		if _, err := a.orderedSink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
			slog.Error("pi persist unknown extension_ui_request",
				"agent_id", a.AgentID(), "method", head.Method, "error", err)
		}
	}
}
