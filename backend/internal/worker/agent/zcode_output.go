package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// handleZCodeOutput dispatches one line the response interceptor did not consume.
//
// Only two notification methods carry conversation: session/event and the top-level
// state.updated. Everything else the app-server emits is its own telemetry.
func handleZCodeOutput(a *zcodeAgent, line *parsedLine) {
	switch line.Method {
	case ZCodeNotifySessionEvent:
		event, ok := parseZCodeEvent(line.Params)
		if !ok {
			slog.Warn("zcode session/event carried no event type", "agent_id", a.agentID, "len", len(line.Raw))
			return
		}
		a.dispatchZCodeEvent(event)
	case ZCodeNotifyStateUpdated:
		a.handleZCodeStateUpdated(line.Params)
	case "":
		// An id with no method and no pending request: a reply nobody waited for. It
		// happens when a request timed out and its answer arrived afterwards.
		slog.Debug("zcode orphan response", "agent_id", a.agentID, "len", len(line.Raw))
	default:
		// The app-server also emits process/resourceSample, process/mcpTelemetry,
		// plugins/operationProgress and the computer-use stream. None of them is
		// conversation, and persisting them would fill the transcript with telemetry.
		slog.Debug("zcode unhandled notification", "agent_id", a.agentID, "method", line.Method)
	}
}

// dispatchZCodeEvent routes one session event.
//
// The switch lists EVERY member of the app-server's event enumeration, including the
// ones LeapMux ignores, each with the reason. A new event type therefore reaches the
// default branch and is logged, instead of being silently absorbed by a branch that
// looks deliberate.
//
// Two goroutines reach this function. The read loop delivers live notifications, and
// `subscribe` dispatches the events its own reply replays -- on the CALLER's goroutine,
// which is the one that ran ClearContext, not the read loop. dispatchMu serializes them
// for the WHOLE body, because `a.mu` guards each field read on its own and not the
// open-then-close SEQUENCE of one tool call: a replayed `scheduled` overtaken by its own
// live `result` closes a span before it opens, and the card then stays running for good.
//
// Take it here and never around `subscribe` itself. The read loop delivers the subscribe
// reply, so holding it across that RPC would deadlock the two against each other. No
// handler reachable from here issues a synchronous request; the one that issues any runs
// on its own goroutine.
func (a *zcodeAgent) dispatchZCodeEvent(event zcodeEventEnvelope) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	if event.Seq > 0 {
		a.mu.Lock()
		// An event at or below the watermark was already dispatched. A subscribe replays
		// every event after `afterSeq`, and a live notification for the same event can
		// arrive alongside it, so without this each row in the overlap is persisted twice.
		if event.Seq <= a.lastSeq {
			a.mu.Unlock()
			return
		}
		a.lastSeq = event.Seq
		a.mu.Unlock()
	}

	switch event.Type {
	case contracts.ZCodeEventTurnStarted:
		a.handleZCodeTurnStarted(event)
	case contracts.ZCodeEventTurnCompleted:
		a.handleZCodeTurnCompleted(event)
	case contracts.ZCodeEventTurnFailed:
		a.handleZCodeTurnFailed(event)
	case contracts.ZCodeEventModelStreaming:
		a.handleZCodeModelStreaming(event)
	case contracts.ZCodeEventToolUpdated:
		a.handleZCodeToolUpdated(event)
	case contracts.ZCodeEventSessionUpdated:
		a.handleZCodeSessionUpdated(event)
	case contracts.ZCodeEventPermissionResolved:
		a.handleZCodePermissionResolved(event)
	case contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained:
		a.persistZCodeNotification(event)
	case contracts.ZCodeEventSessionClosed:
		a.persistZCodeNotification(event)

	case contracts.ZCodeEventSessionCreated, contracts.ZCodeEventSessionResumed:
		// The create/resume RPC already returned the same state, and applyStateSnapshot
		// consumed it. The event is the second copy.

	case contracts.ZCodeEventSessionTitleUpdated:
		// LeapMux titles an agent from the user's first message and from its own
		// renaming flow. A model-written session title would fight that.

	case contracts.ZCodeEventMessageUpserted, contracts.ZCodeEventMessageRemoved,
		contracts.ZCodeEventPartStarted, contracts.ZCodeEventPartDelta,
		contracts.ZCodeEventPartUpserted, contracts.ZCodeEventPartRemoved:
		// The row/part projection is the desktop application's own rendering model, and
		// it repeats what model.streaming already streamed and what the model-response
		// session.updated already persisted. Consuming both would double every message.

	case contracts.ZCodeEventPermissionRequested:
		// The actionable copy is the interaction/requestPermission REQUEST, which
		// carries the reply id and the option list. This event is its announcement, and
		// persisting it would show the prompt twice.

	case contracts.ZCodeEventUserInputRequested:
		// Same reason: interaction/requestUserInput is the actionable copy.

	case contracts.ZCodeEventUserInputResolved:
		// Nothing to persist -- the answer row already records what the user chose.
		// The request id is dropped, which re-arms the re-announcement guard.
		a.handleZCodeUserInputResolved(event)

	case contracts.ZCodeEventCheckpointCreated, contracts.ZCodeEventRewindTriggered:
		// Checkpoint and rewind belong to the desktop application's undo model, which
		// LeapMux does not expose. Its own history is the transcript.

	case contracts.ZCodeEventStreamRecoveryUpdated:
		a.handleZCodeStreamRecovery(event)

	default:
		slog.Debug("zcode unknown event type", "agent_id", a.agentID, "type", event.Type)
	}
}

// persistBytes re-marshals the envelope for persistence.
//
// Both arrival paths (a session/event notification and the events array a
// session/subscribe returns) are normalized to this ONE shape, so the frontend has a
// single envelope to classify: `{type, payload, ...}`.
func (e zcodeEventEnvelope) persistBytes() []byte {
	encoded, err := json.Marshal(e)
	if err != nil {
		slog.Warn("zcode marshal event for persist failed", "type", e.Type, "error", err)
		return nil
	}
	return encoded
}

// withPayload returns a copy of the envelope carrying a different payload, so a
// handler can persist a payload it completed (a tool input recovered from the
// stream) without mutating the value it was given.
func (e zcodeEventEnvelope) withPayload(payload json.RawMessage) zcodeEventEnvelope {
	e.Payload = payload
	return e
}

// persistZCodeNotification records a lifecycle event as a notification row.
func (a *zcodeAgent) persistZCodeNotification(event zcodeEventEnvelope) {
	content := event.persistBytes()
	if content == nil {
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("zcode persist notification", "agent_id", a.agentID, "type", event.Type, "error", err)
	}
}

// --- turn lifecycle ---

// zcodeTurnStarted is the turn.started payload.
type zcodeTurnStarted struct {
	TurnNumber  int64  `json:"turnNumber"`
	Input       string `json:"input"`
	InputID     string `json:"inputId"`
	InputSource string `json:"inputSource"`
	MessageID   string `json:"messageId"`
}

// handleZCodeTurnStarted arms the turn.
//
// A turn whose inputSource is set was started by the RUNTIME, not by the user: a
// background task reporting back, a subagent's reply being folded in, a todo
// reminder. Such a turn is armed as a background turn, so its completion does not
// end the user's turn and its transcript rows still land.
func (a *zcodeAgent) handleZCodeTurnStarted(event zcodeEventEnvelope) {
	var payload zcodeTurnStarted
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.started unmarshal failed", "agent_id", a.agentID, "error", err)
		}
	}
	background := payload.InputSource != ""

	a.mu.Lock()
	a.turnActive = true
	a.backgroundTurn = background
	if !background {
		a.turnToolUses = 0
	}
	a.mu.Unlock()

	if !background {
		// A fresh user turn begins: restart the thinking-token estimate from zero.
		a.thinkingTokens.reset()
	}
	slog.Debug("zcode turn started", "agent_id", a.agentID, "turn", payload.TurnNumber, "input_source", payload.InputSource)
}

// zcodeTurnCompleted is the turn.completed payload.
type zcodeTurnCompleted struct {
	Response      string      `json:"response"`
	TokenCount    int64       `json:"tokenCount"`
	Usage         *zcodeUsage `json:"usage"`
	ToolCallCount int32       `json:"toolCallCount"`
	Duration      float64     `json:"duration"`
	ResultType    string      `json:"resultType"`
}

func (a *zcodeAgent) handleZCodeTurnCompleted(event zcodeEventEnvelope) {
	var payload zcodeTurnCompleted
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.completed unmarshal failed", "agent_id", a.agentID, "error", err)
		}
	}
	if payload.Usage != nil {
		a.recordZCodeUsage(*payload.Usage)
	}
	a.finishZCodeTurn(event, payload.ToolCallCount)
	// A completed turn is not an error, so any pending auto-continue for one is stale.
	scheduleOrCancelAPIErrorAutoContinue(a.sink, false, event.persistBytes())
}

// zcodeTurnFailed is the turn.failed payload. `error.retryable` is the app-server's
// own verdict on whether the failure is transient, which is exactly the question the
// auto-continue scheduler asks -- so no message-text matching is needed.
type zcodeTurnFailed struct {
	Error struct {
		Type      string `json:"type"`
		Message   string `json:"message"`
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		Retryable *bool  `json:"retryable"`
	} `json:"error"`
	TurnPhase string `json:"turnPhase"`
}

func (a *zcodeAgent) handleZCodeTurnFailed(event zcodeEventEnvelope) {
	var payload zcodeTurnFailed
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			slog.Warn("zcode turn.failed unmarshal failed", "agent_id", a.agentID, "error", err)
		}
	}
	content := event.persistBytes()
	a.finishZCodeTurn(event, 0)
	scheduleOrCancelAPIErrorAutoContinue(a.sink, zcodeFailureIsRetryable(payload), content)
}

// zcodeFailureIsRetryable reports whether a failed turn should be retried.
//
// The app-server's own `retryable` flag decides when it states one. A missing flag
// falls back to "not retryable", with one named exception: a provider that carries no
// usable credential is NEVER retryable, because a retry re-reads the same empty
// configuration and produces the same failure forever.
func zcodeFailureIsRetryable(payload zcodeTurnFailed) bool {
	if payload.Error.Code == ZCodeCauseProviderNotConfigured ||
		payload.Error.Type == ZCodeCauseProviderNotConfigured {
		return false
	}
	return payload.Error.Retryable != nil && *payload.Error.Retryable
}

// finishZCodeTurn closes out a turn: persist the divider, reset the spans, and
// refresh the authoritative usage.
//
// A BACKGROUND turn takes none of it. Its completion says nothing about the user's
// turn, and closing the spans there would tear down the cards of tool calls the
// user's own turn is still running.
func (a *zcodeAgent) finishZCodeTurn(event zcodeEventEnvelope, toolCallCount int32) {
	a.mu.Lock()
	background := a.backgroundTurn
	// A turn ends whichever kind it was, so the flag clears either way. Leaving it set
	// for a background turn made Interrupt and Stop fire a session/stop RPC at an idle
	// session for the rest of the agent's life. What a background turn does NOT do is
	// close the user's spans or persist a divider, which is what the guard below states.
	a.turnActive = false
	if !background && toolCallCount > 0 {
		a.turnToolUses = int(toolCallCount)
	}
	a.backgroundTurn = false
	a.mu.Unlock()

	content := event.persistBytes()
	if content == nil {
		return
	}
	if background {
		// The background turn's own outcome is still worth recording, as a notification
		// rather than as the user's turn end.
		a.persistZCodeNotification(event)
		return
	}

	// Recover any tool call that never reached a final update (an aborted turn), so
	// the next turn's progress deltas are not measured against a dead stream.
	a.resetCumulativeDeltas()

	if err := a.sink.PersistTurnEnd(zcodeAugmentWithUsage(content, a.usageSnapshot()), SpanInfo{}); err != nil {
		slog.Error("zcode persist turn end", "agent_id", a.agentID, "type", event.Type, "error", err)
	}
	a.sink.ResetSpans()
	a.mu.Lock()
	// Any batch summary that could still reference these arrived within the turn, so the
	// marks are spent. A record that holds nothing else goes with them, which is what
	// keeps the map bounded by one turn's calls rather than by the session's.
	for id, tc := range a.toolCalls {
		tc.final = false
		if tc.name == "" && tc.input == nil {
			delete(a.toolCalls, id)
		}
	}
	a.mu.Unlock()
	notifyInputReady(a.sink)

	// The app-server's own reading is authoritative, and reading it takes an RPC --
	// which the read loop must stay free to deliver, so it cannot run inline here.
	go a.refreshZCodeUsageFromSession()
}

// --- streaming ---

// zcodeModelStreaming is the model.streaming payload.
type zcodeModelStreaming struct {
	AssistantMessageID string          `json:"assistantMessageId"`
	Delta              string          `json:"delta"`
	Done               bool            `json:"done"`
	Input              json.RawMessage `json:"input"`
	Kind               string          `json:"kind"`
	ToolCallID         string          `json:"toolCallId"`
	ToolName           string          `json:"toolName"`
}

// handleZCodeModelStreaming streams assistant text and reasoning, and caches what
// the stream says about a tool call.
//
// The app-server filters this event before it leaves: only text_delta and
// reasoning_delta (with a non-empty delta) and the four tool_input kinds are ever
// sent. The start/finish/error and the *_start / *_end phase markers exist in its
// enumeration and never arrive, which is why there is no phase handling here.
func (a *zcodeAgent) handleZCodeModelStreaming(event zcodeEventEnvelope) {
	var payload zcodeModelStreaming
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode model.streaming unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	switch payload.Kind {
	case ZCodeStreamTextDelta:
		if payload.Delta == "" {
			return
		}
		a.sink.BroadcastStreamChunk([]byte(payload.Delta), "", payload.Kind)
	case ZCodeStreamReasoningDelta:
		if payload.Delta == "" {
			return
		}
		a.sink.BroadcastStreamChunk([]byte(payload.Delta), "", payload.Kind)
		a.thinkingTokens.observe(a.sink, payload.Delta)

	case ZCodeStreamToolInputStart:
		if payload.ToolCallID == "" {
			return
		}
		a.mu.Lock()
		tc := a.zcodeToolCallLocked(payload.ToolCallID)
		if payload.ToolName != "" {
			tc.name = payload.ToolName
		}
		// A restart of the same id drops a partial input, so a retried tool call does
		// not inherit the abandoned attempt's fragment.
		tc.input = nil
		a.mu.Unlock()

	case ZCodeStreamToolInputDelta:
		// The input arrives as JSON TEXT split across deltas, so the fragments are
		// concatenated and parsed only once the call is complete.
		if payload.ToolCallID == "" || payload.Delta == "" {
			return
		}
		a.mu.Lock()
		tc := a.zcodeToolCallLocked(payload.ToolCallID)
		tc.input = append(tc.input, payload.Delta...)
		a.mu.Unlock()

	case ZCodeStreamToolInputEnd:
		// Nothing to do: the accumulated fragments stay cached until the tool.updated
		// that opens the call consumes them.

	case ZCodeStreamToolCall:
		// The complete, PARSED input. It supersedes the concatenated fragments, which
		// can be truncated when the model's stream was cut.
		if payload.ToolCallID == "" {
			return
		}
		a.mu.Lock()
		tc := a.zcodeToolCallLocked(payload.ToolCallID)
		if payload.ToolName != "" {
			tc.name = payload.ToolName
		}
		if len(payload.Input) > 0 {
			tc.input = append(json.RawMessage(nil), payload.Input...)
		}
		a.mu.Unlock()

	default:
		slog.Debug("zcode unknown model.streaming kind", "agent_id", a.agentID, "kind", payload.Kind)
	}
}

// --- tool lifecycle ---

// zcodeToolUpdated is the tool.updated payload, across every kind.
//
// The subagent fields (Source / AgentID / AgentType / ChildSessionID) mark an update
// that belongs to a SUBAGENT's own conversation rather than to this one.
type zcodeToolUpdated struct {
	Kind           string `json:"kind"`
	ToolCallID     string `json:"toolCallId"`
	ToolName       string `json:"toolName"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	AgentID        string `json:"agentId"`
	AgentType      string `json:"agentType"`
	ChildSessionID string `json:"childSessionId"`
	// ParentToolCallID is the tool-call id of the `Agent` call that started the
	// subagent this update belongs to. It is the ONLY field that links a subagent's
	// work back to the spawn card, and it is what the child transcript is keyed on.
	ParentToolCallID string `json:"parentToolCallId"`

	// scheduled
	Input        json.RawMessage `json:"input"`
	InputOmitted bool            `json:"inputOmitted"`
	InputRef     string          `json:"inputRef"`

	// progress. OutputBytes is the COMBINED total; the two per-stream counters are what
	// the tails are measured against, because each tail holds one stream only.
	OutputBytes int64  `json:"outputBytes"`
	StdoutBytes int64  `json:"stdoutBytes"`
	StderrBytes int64  `json:"stderrBytes"`
	StdoutTail  string `json:"stdoutTail"`
	StderrTail  string `json:"stderrTail"`

	// result
	Result json.RawMessage `json:"result"`

	// error
	Error json.RawMessage `json:"error"`

	// batch
	ToolCallIDs  []string `json:"toolCallIds"`
	SuccessCount int      `json:"successCount"`
	ErrorCount   int      `json:"errorCount"`
}

func (a *zcodeAgent) handleZCodeToolUpdated(event zcodeEventEnvelope) {
	var payload zcodeToolUpdated
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode tool.updated unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	switch payload.Kind {
	case contracts.ZCodeToolKindScheduled:
		// A subagent's own tool calls get a transcript of their own -- see
		// zcode_subagent.go. One whose parent tool call the update does not name falls
		// through to the parent transcript rather than being dropped.
		if zcodeToolFromSubagent(payload) && a.openZCodeSubagentToolCall(event, payload) {
			return
		}
		a.openZCodeToolCall(event, payload)
	case contracts.ZCodeToolKindStarted:
		a.recordZCodeToolStarted(payload)
	case contracts.ZCodeToolKindProgress:
		a.streamZCodeToolProgress(payload)
	case contracts.ZCodeToolKindResult, contracts.ZCodeToolKindError:
		a.closeZCodeToolCall(event, payload)
	case contracts.ZCodeToolKindBatch:
		a.applyZCodeToolBatch(event, payload)
	default:
		slog.Debug("zcode unknown tool.updated kind", "agent_id", a.agentID, "kind", payload.Kind)
	}
}

// openZCodeToolCall persists the tool call's opening row into this agent's own
// transcript. A subagent's call goes to its child transcript instead -- see
// zcode_subagent.go.
func (a *zcodeAgent) openZCodeToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated) {
	a.openZCodeToolCallInto(a.sink, event, payload)
}

// zcodeInputIsAbsent reports whether a tool call's `input` states nothing.
//
// A field the app-server omits, one it sends as JSON `null`, and one it sends as an
// empty object all mean the same thing: the input was not delivered here, and the model
// stream is the only copy. Reading only `len(raw) == 0` misses the other two, because a
// `null` decodes into a json.RawMessage as the four bytes `null` -- non-empty, so the
// recovery is skipped and the persisted row carries no command at all.
//
// One predicate, so the caller that decides to substitute and the helper that performs
// the substitution can never disagree about what "absent" means.
func zcodeInputIsAbsent(raw json.RawMessage) bool {
	trimmed := string(bytes.TrimSpace(raw))
	return trimmed == "" || trimmed == "null" || trimmed == "{}"
}

// zcodeCompleteToolInput returns the scheduled payload with its `input` filled in
// from the stream cache, and the omission markers removed so the persisted row does
// not claim an input it now carries.
//
// Returns the payload unchanged when there is nothing to fill in, which keeps the
// common path free of a decode/encode round trip.
func zcodeCompleteToolInput(payload, input json.RawMessage) json.RawMessage {
	if zcodeInputIsAbsent(input) || !json.Valid(input) {
		return payload
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil || obj == nil {
		return payload
	}
	if existing, ok := obj["input"]; ok && !zcodeInputIsAbsent(existing) {
		return payload
	}
	obj["input"] = input
	delete(obj, "inputOmitted")
	delete(obj, "inputRef")
	encoded, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return encoded
}

// recordZCodeToolStarted notes that a scheduled call began running.
func (a *zcodeAgent) recordZCodeToolStarted(payload zcodeToolUpdated) {
	if payload.ToolCallID == "" {
		return
	}
	// The progress counters are measured from here, so a re-started call does not read
	// its predecessor's byte totals as already-broadcast. Both streams are keyed
	// separately, so both are dropped.
	a.clearCumulativeDelta(zcodeProgressKey(payload.ToolCallID, zcodeStreamStdout))
	a.clearCumulativeDelta(zcodeProgressKey(payload.ToolCallID, zcodeStreamStderr))
	// This used to broadcast a `zcode_running_tool` session-info key that nothing
	// read. The shared channel for "which tool is running" is now
	// contracts.SessionInfoKeyRunningTool, which the tool card renders as a live
	// badge. ZCode does not feed it yet: the badge shows an elapsed time or a
	// retry state, and the app-server reports neither for a tool call, so an entry
	// from here would render nothing. Broadcast one from this site (span_id =
	// payload.ToolCallID) once ZCode reports either. The clear needs no code --
	// the frontend drops a span's entry when its result row lands, and
	// closeZCodeToolCall already persists that row with Closing: true.
	//
	// The other route to a ZCode badge needs nothing from the app-server: the
	// browser can count from the tool_use row's own timestamp, which would give
	// every provider a badge at once.
	// https://github.com/leapmux/leapmux/issues/439
}

// streamZCodeToolProgress ships the new output of a running tool.
//
// A tool writes to two streams and the app-server reports both, so each one is shipped
// on its own. Measuring one stream's tail against the OTHER's counter -- or against the
// combined `outputBytes` -- makes the growth of either look like the growth of both: the
// quiet stream's tail is re-broadcast while the busy stream's new bytes never appear.
// Every compiler, `git` and `npm` invocation writes on both.
func (a *zcodeAgent) streamZCodeToolProgress(payload zcodeToolUpdated) {
	if payload.ToolCallID == "" {
		return
	}
	// One sink for every chunk of this call. A subagent's tool call opened its span in a
	// CHILD transcript, so a chunk broadcast on the parent lands under a span the parent
	// never opened -- invisible in the child tab and orphaned in the parent's.
	sink := a.zcodeSinkForToolCall(payload.ToolCallID)
	stdoutTotal, stderrTotal := payload.StdoutBytes, payload.StderrBytes
	if stdoutTotal == 0 && stderrTotal == 0 && payload.OutputBytes > 0 {
		// A build that sends only the COMBINED counter. It is attributable when exactly
		// one tail is present, and then it is exact. When both are, it belongs to neither
		// stream on its own, so both fall back to their tail's own growth -- which is
		// exact anyway while the output is shorter than the tail's size limit.
		switch {
		case payload.StdoutTail != "" && payload.StderrTail == "":
			stdoutTotal = payload.OutputBytes
		case payload.StderrTail != "" && payload.StdoutTail == "":
			stderrTotal = payload.OutputBytes
		}
	}
	a.streamZCodeToolTail(sink, payload.ToolCallID, zcodeStreamStdout, payload.StdoutTail, stdoutTotal)
	a.streamZCodeToolTail(sink, payload.ToolCallID, zcodeStreamStderr, payload.StderrTail, stderrTotal)
}

// zcodeStreamStdout and zcodeStreamStderr key the per-stream progress bookkeeping. They
// are internal to this file and never reach the wire.
const (
	zcodeStreamStdout = "stdout"
	zcodeStreamStderr = "stderr"
)

// streamZCodeToolTail ships one stream's new output for one tool call.
//
// The app-server sends a byte TOTAL plus a size-limited TAIL, not a delta. So the growth
// is computed from the total and cut from the end of the tail. When the output grew by
// more than the tail holds, the tail is shipped whole and the middle is lost -- it is
// lost at the source too, because the app-server keeps only a tail.
func (a *zcodeAgent) streamZCodeToolTail(sink OutputSink, toolCallID, stream, tail string, total int64) {
	if tail == "" {
		return
	}
	key := zcodeProgressKey(toolCallID, stream)
	if total <= 0 {
		// No counter to measure against: fall back to the tail's own growth.
		if delta := a.recordCumulativeDelta(key, tail); delta != "" {
			sink.BroadcastStreamChunk([]byte(delta), toolCallID, contracts.ZCodeToolKindProgress)
		}
		return
	}
	prev, reset := a.recordCumulativeLength(key, int(total))
	if !reset && int(total) <= prev {
		return
	}
	grown := int(total) - prev
	if reset {
		grown = len(tail)
	}
	chunk := tail
	if grown < len(tail) {
		// The counter is a BYTE count and `tail` is UTF-8, so the cut can land inside a
		// multi-byte rune. Move it forward to the next rune start: the browser decodes
		// each chunk on its own, and an orphan continuation byte renders as U+FFFD.
		// Moving forward drops at most three bytes of a rune whose remainder is already
		// unusable; moving back would repeat bytes the previous chunk already shipped.
		cut := len(tail) - grown
		for cut < len(tail) && !utf8.RuneStart(tail[cut]) {
			cut++
		}
		chunk = tail[cut:]
	}
	if chunk == "" {
		return
	}
	sink.BroadcastStreamChunk([]byte(chunk), toolCallID, contracts.ZCodeToolKindProgress)
}

// zcodeProgressKey is the key of one tool call's progress bookkeeping for one stream. The two
// streams must never share a slot: their totals count different bytes, so a shared slot
// reads one stream's growth as the other's.
func zcodeProgressKey(toolCallID, stream string) string {
	return toolCallID + "\x00" + stream
}

// closeZCodeToolCall persists the tool call's final row into the transcript that
// holds its opening row -- this agent's own, or a subagent's child transcript.
func (a *zcodeAgent) closeZCodeToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated) {
	a.closeZCodeToolCallInto(a.zcodeSinkForToolCall(payload.ToolCallID), event, payload)
}

// applyZCodeToolBatch backfills the calls a batch summary lists.
//
// A batch arrives AFTER the per-call results and only summarizes them, so it must
// close only an id that was opened and never reached a final state -- a call whose
// own result was lost. Closing one that already finished would double its row.
func (a *zcodeAgent) applyZCodeToolBatch(event zcodeEventEnvelope, payload zcodeToolUpdated) {
	for _, id := range payload.ToolCallIDs {
		if id == "" {
			continue
		}
		a.mu.Lock()
		tc := a.toolCalls[id]
		// `seen` is "a name is known", which is what marks a call that was OPENED as a
		// span. A call known only from a stream fragment has none, and closing it would
		// close a span nothing opened.
		final, toolName := false, ""
		if tc != nil {
			final, toolName = tc.final, tc.name
		}
		seen := toolName != ""
		a.mu.Unlock()
		if final || !seen {
			continue
		}
		recovered := zcodeToolUpdated{
			Kind:       contracts.ZCodeToolKindResult,
			ToolCallID: id,
			ToolName:   toolName,
		}
		a.closeZCodeToolCall(event.withPayload(zcodeBatchResultPayload(id, toolName, payload)), recovered)
	}
}

// zcodeBatchResultPayload synthesizes the result payload for a call the batch summary
// recovered.
//
// The BATCH payload cannot be persisted as the call's own result: it is addressed to a
// list (`toolCallIds`) and carries no `toolCallId`, so every frontend extractor refuses
// it and the row renders as an empty bubble that closes the span and shows nothing. This
// builds the one-call shape those extractors read instead.
//
// The batch states aggregate counts only, so it cannot say WHICH call failed. A batch
// with no error at all makes every recovered call a success; otherwise the outcome is
// unknown, and saying so is better than claiming either one.
func zcodeBatchResultPayload(toolCallID, toolName string, batch zcodeToolUpdated) json.RawMessage {
	content := "This tool's own result was lost. The batch summary reports it finished."
	if batch.ErrorCount > 0 {
		content = "This tool's own result was lost. The batch summary reports that some call in the batch failed."
	}
	encoded, err := json.Marshal(map[string]any{
		"kind":       contracts.ZCodeToolKindResult,
		"toolCallId": toolCallID,
		"toolName":   toolName,
		"result": map[string]any{
			"success": batch.ErrorCount == 0,
			"content": content,
		},
	})
	if err != nil {
		// Every field is a plain string or bool, so this cannot fail; returning the batch
		// payload keeps the span closing rather than leaving the card open for good.
		slog.Error("zcode marshal batch result", "tool_call_id", toolCallID, "error", err)
		return batch.Result
	}
	return encoded
}

// --- session.updated ---

// handleZCodeSessionUpdated splits the one overloaded event.
//
// `session.updated` is the app-server's catch-all: every internal event it does not
// map explicitly becomes one, with a free-form payload. So the shapes are told apart
// by which fields they carry, in order of specificity.
func (a *zcodeAgent) handleZCodeSessionUpdated(event zcodeEventEnvelope) {
	if len(event.Payload) == 0 {
		return
	}
	var payload struct {
		// A background task's lifecycle.
		TaskID string `json:"taskId"`
		Status string `json:"status"`

		// A finished model request: the assistant's text plus what it cost.
		Content    *string     `json:"content"`
		StopReason string      `json:"stopReason"`
		Usage      *zcodeUsage `json:"usage"`

		ContextWindow int64 `json:"contextWindow"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode session.updated unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	switch {
	case payload.TaskID != "":
		a.handleZCodeBackgroundTask(event)
	case payload.Content != nil && payload.StopReason != "":
		if payload.ContextWindow > 0 {
			a.mu.Lock()
			a.contextWindow = payload.ContextWindow
			a.mu.Unlock()
		}
		if payload.Usage != nil {
			a.recordZCodeUsage(*payload.Usage)
		}
		a.persistZCodeAssistantMessage(event, *payload.Content)
	default:
		// The remaining shapes are telemetry: the per-request model/iteration counters
		// and the provider request record (baseURL, requestId, maxAttempts). They carry
		// no conversation, and persisting them would fill the transcript.
	}
}

// persistZCodeAssistantMessage records one finished model response.
//
// This is the assistant text's ONLY persisted copy on this stream: the app-server's
// message/part projection is not what desktop-continuous delivers, so the
// model-response `session.updated` is where the completed text arrives. A turn that
// only called tools carries an empty string, and nothing is persisted for it.
func (a *zcodeAgent) persistZCodeAssistantMessage(event zcodeEventEnvelope, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	raw := event.persistBytes()
	if raw == nil {
		return
	}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, SpanInfo{}); err != nil {
		slog.Error("zcode persist assistant message", "agent_id", a.agentID, "error", err)
	}
}

// zcodeBackgroundTask is the background-task shape session.updated carries.
type zcodeBackgroundTask struct {
	TaskID         string `json:"taskId"`
	ToolCallID     string `json:"toolCallId"`
	ToolName       string `json:"toolName"`
	TaskKind       string `json:"taskKind"`
	ChildSessionID string `json:"childSessionId"`
	Command        string `json:"command"`
	Status         string `json:"status"`
	Blocked        bool   `json:"blocked"`
	BlockedReason  string `json:"blockedReason"`
	StdoutTail     string `json:"stdoutTail"`
}

// zcodeBackgroundStatus maps a task status onto the registry's. ok is false for a
// status that is not final, so the row stays open rather than being closed by a
// value the registry would treat as absorbing.
func zcodeBackgroundStatus(status string) (bgtask.Status, bool) {
	switch status {
	case "completed":
		return bgtask.StatusCompleted, true
	case "failed", "spawn_error", "timed_out":
		return bgtask.StatusFailed, true
	case "cancelled":
		return bgtask.StatusStopped, true
	case "lost":
		// The app-server lost track of the task. It will never report again, so leaving
		// the row Running would pin the thinking indicator for good.
		return bgtask.StatusFailed, true
	case "running", "":
		return bgtask.StatusRunning, false
	default:
		// A token outside the app-server's enumeration. Running is the only safe answer:
		// a final status is absorbing, and it tears down the child transcript, so guessing
		// "finished" for a task that is still working destroys live output. Every sibling
		// dispatch in this file logs its unhandled input, and so does this one -- an
		// unknown status pins the row, and a silent default leaves no trace of why.
		slog.Debug("zcode unknown background task status", "status", status)
		return bgtask.StatusRunning, false
	}
}

// handleZCodeBackgroundTask maintains the background-task registry row for one task.
//
// A `bash` task reuses the launch card it already has: its row is keyed by the
// tool-call id, which is the span the transcript already shows. A `subagent` task
// gets its own child transcript, because its output is a conversation of its own.
func (a *zcodeAgent) handleZCodeBackgroundTask(event zcodeEventEnvelope) {
	var task zcodeBackgroundTask
	if err := json.Unmarshal(event.Payload, &task); err != nil {
		slog.Warn("zcode background task unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if task.TaskID == "" {
		return
	}
	status, final := zcodeBackgroundStatus(task.Status)

	kind := bgtask.KindShell
	if task.TaskKind == ZCodeTaskKindSubagent {
		kind = bgtask.KindSubagent
	}
	title, titleIsCommand := zcodeBackgroundTitle(task)

	// A shell task reuses the launch card the transcript already shows, which is the
	// tool call's own span. A subagent shares its key with the tool.updated path --
	// see zcodeSubagentRowKey.
	rowKey := zcodeSubagentRowKey(task.ToolCallID, task.ChildSessionID, task.TaskID)
	upsert := bgtask.Upsert{
		RowKey:         rowKey,
		Kind:           kind,
		Title:          title,
		TitleIsCommand: titleIsCommand,
		Status:         status,
	}
	if task.Blocked && !final {
		upsert.ActiveForm = task.BlockedReason
	}

	// A subagent task owns a conversation, so it gets a child transcript -- through the
	// SAME creator the tool.updated path uses, so one subagent can never end up with two
	// transcripts and two rows.
	//
	// A task with NO tool-call id is the one case that gets none. Its row key fell back
	// to the child session or the task id, which the tool.updated path never sees, so a
	// transcript created here would be a second one that `zcodeSinkForToolCall`,
	// `takeChild` and the spawn's own teardown all miss -- a permanently Running row
	// beside an empty tab. The tool.updated path owns the transcript instead.
	if kind == bgtask.KindSubagent && task.ToolCallID != "" {
		childID, childTitle, ok := a.ensureZCodeSubagentTranscript(rowKey, task.ToolCallID, title)
		if ok {
			upsert.ChildAgentID = childID
			// The creator may have used the spawn's own label instead of this event's, so
			// the row states what the tab states.
			upsert.Title = childTitle
			if final {
				a.sink.CleanupChildAgent(childID)
				// Drop the index entry too, or the spawn's own result would clean up a
				// transcript this path already tore down.
				a.children.takeChild(rowKey)
			}
		}
	}

	logRegistryRefusal("zcode", "upsert", a.sink.UpsertBackgroundTask(upsert))
}

// zcodeBackgroundTitle labels a background-task row with the command it runs, then
// its tool name. titleIsCommand is true only for the command, which the app-server
// hands over verbatim -- so the row can set it as code, and prose never is.
func zcodeBackgroundTitle(task zcodeBackgroundTask) (title string, titleIsCommand bool) {
	if cmd := strings.TrimSpace(task.Command); cmd != "" {
		return bgtask.CleanTitleRunes(bgtask.FirstLine(cmd), 120), true
	}
	return strings.TrimSpace(task.ToolName), false
}

// --- permissions the app-server decided by itself ---

// zcodePermissionResolved is the permission.resolved payload.
type zcodePermissionResolved struct {
	RequestID  string `json:"requestId"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
}

// handleZCodePermissionResolved records a decision LeapMux did not make.
//
// The app-server resolves some permissions itself -- a rule the user saved in ZCode,
// a mode that allows the tool outright, or a mode that denies it. Only the ones with
// no matching control request are recorded, so an answer the user gave through
// LeapMux is not reported back to them as an automatic decision.
func (a *zcodeAgent) handleZCodePermissionResolved(event zcodeEventEnvelope) {
	var payload zcodePermissionResolved
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode permission.resolved unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if a.forgetZCodeControlRequest(payload.RequestID) {
		// The user answered this one through LeapMux; the structured answer row already
		// records it.
		return
	}
	// An automatic ALLOW is the normal case in every mode but plan, and reporting each
	// one would bury the transcript in rows saying "as configured". A DENIAL is the
	// one that needs saying, because the model's next step reacts to it.
	if payload.Decision == contracts.ZCodeDecisionAllow {
		return
	}
	a.persistZCodeNotification(event)
}

// handleZCodeUserInputResolved drops the resolved request from the forwarded set.
//
// It renders nothing: a plan approval and a question are both answered through
// LeapMux's own control surface, which persists the answer row. What the event is
// needed for is the bookkeeping -- while the id stays in the set, a later request
// that REUSES it would be mistaken for a re-announcement and never reach the user.
func (a *zcodeAgent) handleZCodeUserInputResolved(event zcodeEventEnvelope) {
	if len(event.Payload) == 0 {
		return
	}
	var payload struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("zcode userInput.resolved unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	a.forgetZCodeControlRequest(payload.RequestID)
}

// --- stream recovery ---

// handleZCodeStreamRecovery logs the app-server's stream-recovery report.
//
// It takes NO action, and that is the whole point of the function. The report describes
// a retry of the MODEL PROVIDER's own SSE stream, not a lapse in LeapMux's event
// subscription: its payloads carry `streamMode:"sse"`, a failure kind, a retry number
// and the count of assistant bytes discarded before the retry. The event subscription is
// a separate, sticky registration, and the app-server appends every event to its store
// before it delivers one -- so there is nothing here for a re-subscribe to recover, and
// firing one on every model-provider retry would spend an RPC to replay nothing.
//
// The outcome of the retry still reaches the transcript, on `turn.failed` or
// `turn.completed`.
func (a *zcodeAgent) handleZCodeStreamRecovery(event zcodeEventEnvelope) {
	slog.Debug("zcode stream recovery", "agent_id", a.agentID, "payload_len", len(event.Payload))
}
