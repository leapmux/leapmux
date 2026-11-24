package cline

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Transcript output.
//
// Each transcript row is Cline's own hub event envelope. The worker persists:
//
//   - `assistant.finished` and `assistant.media` as they arrive.
//   - `reasoning.finished`, which Cline sends AFTER the text of the same
//     message. The reasoning comes first in the model's answer, so the worker
//     persists the reasoning that its deltas carried as soon as the message's
//     text ends, in Cline's own `reasoning.finished` shape, and drops the late
//     copy. A message with reasoning alone keeps Cline's own row.
//   - `tool.started` as the row that opens a tool span, and `tool.finished` as
//     the row that closes it. A call that its turn outlived closes with its own
//     opening row again, with the completion that ended the turn.
//   - The end event of the run (`run.completed`, `run.failed`, `run.aborted`) as
//     the turn-end row, without the conversation and the session snapshot that
//     Cline puts in it: each turn end would otherwise store the whole history
//     again. The worker writes the same shape itself for a turn that ended with
//     no end event.
//
// Deltas feed the live counters and are never stored. A subagent's output goes
// to the subagent's own transcript (subagent.go).

// noCompletion marks a row that no stop and no failure cut short.
const noCompletion agent.MessageCompletion = ""

// liveOutputLimit caps the tail of a running command's output that the card
// shows.
const liveOutputLimit = 8 << 10

// outputState is the output state of every transcript of the agent. Guarded by
// Agent.Mu.
type outputState struct {
	// lead is the lead's own transcript.
	lead *transcript
	// toolOwner maps each tool call that started and did not finish to the
	// transcript that shows it, so later updates and finishes follow the opening
	// call even when payloads carry no agent metadata.
	toolOwner map[string]*transcript
	// dropped holds the tool calls whose start reached no transcript: the calls
	// of a subagent whose output the worker could not attribute (subagent.go).
	// The worker drops their later events too.
	dropped map[string]bool
	// spawns holds each spawn_agent call that runs, by tool call id.
	spawns map[string]*spawnCall
	// spawnsByAgent indexes active spawn_agent calls by their bound native agent.agentId.
	spawnsByAgent map[string]*spawnCall
	// leadAgentID records the native agent.agentId of the lead when tagged events state it.
	leadAgentID string
	// claimed holds the stored child sessions that a backfill used, so two
	// subagents with the same task never take one session.
	claimed map[string]bool
	// lastLeadText is the text of the lead's last message. It is the plan when
	// the model asks to switch to Act mode.
	lastLeadText string
}

func newOutputState() outputState {
	return outputState{
		lead:          newTranscript(""),
		toolOwner:     make(map[string]*transcript),
		dropped:       make(map[string]bool),
		spawns:        make(map[string]*spawnCall),
		spawnsByAgent: make(map[string]*spawnCall),
		claimed:       make(map[string]bool),
	}
}

// transcript is the output state of one transcript: the lead's, or one child's.
// Guarded by Agent.Mu, and changed only under dispatchMu, except by a backfill,
// which writes a child transcript that no live event reaches any more.
type transcript struct {
	// childID is the child agent, or "" for the lead.
	childID        string
	compactionLive bool
	// sink is the services of a child's transcript, which the sink that created
	// the child resolves. nil for the lead, whose services are the agent's own.
	sink agent.ProviderServices
	// spawns holds the spawn_agent calls of this transcript that run, in the
	// order they started (subagent.go).
	spawns []*spawnCall
	// reasoning holds the reasoning deltas of the current message, and
	// reasoningFlushed records that the worker persisted them already.
	reasoning        strings.Builder
	reasoningFlushed bool
	// text holds the text deltas of the current message, for the counters.
	text strings.Builder
	// open holds the tool calls that started and did not finish, by id.
	open map[string]*openTool
	// nextOrder orders the open calls, so an interrupted turn, an idle
	// interrupt, or teardown closes them in the order they started.
	nextOrder uint64
	// written counts what reached this transcript live, so a later backfill
	// from Cline's stored messages writes only the rest (subagent.go).
	written writtenCounts
	// contextUsage holds the last context-usage broadcast for a child transcript.
	contextUsage map[string]any
}

// openTool is one tool call that started and did not finish.
type openTool struct {
	id    string
	name  string
	row   []byte
	order uint64
	// tail and total are the output a running command printed so far.
	tail     string
	tailLost bool
	total    int64
}

// writtenCounts counts the rows of each kind that a transcript received.
type writtenCounts struct {
	texts     int
	reasoning int
	tools     map[string]bool
}

func newTranscript(childID string) *transcript {
	return &transcript{childID: childID, open: make(map[string]*openTool), written: writtenCounts{tools: make(map[string]bool)}}
}

// newChildTranscript is the transcript of the child childID, which the sink
// parent created.
func newChildTranscript(parent agent.ProviderServices, childID string) *transcript {
	t := newTranscript(childID)
	t.sink = parent.ChildSink(childID)
	return t
}

// progressScope gives the live counter scope of one transcript's text and
// reasoning.
func (t *transcript) progressScope(kind string) string {
	if t.childID == "" {
		return "cline:lead:" + kind
	}
	return "cline:" + t.childID + ":" + kind
}

// sinkFor returns the services of one transcript.
func (a *Agent) sinkFor(t *transcript) agent.ProviderServices {
	if t == nil || t.sink == nil {
		return a.sink
	}
	return t.sink
}

// finishTranscriptCompaction records an unfinished compaction when its owner ends.
func (a *Agent) finishTranscriptCompaction(t *transcript, completion agent.MessageCompletion) {
	if t == nil {
		return
	}
	a.Mu.Lock()
	live := t.compactionLive
	t.compactionLive = false
	a.Mu.Unlock()
	if !live {
		return
	}
	sink := a.sinkFor(t)
	providerkit.EndCompaction(sink)
	if a.IsDiscardingOutput() {
		return
	}
	outcome := providerkit.CompactionEndFailed
	if completion == agent.MessageCompletionInterrupted {
		outcome = providerkit.CompactionEndAborted
	}
	providerkit.PersistCompactionEnded(sink, outcome, "")
}

// resolveLeadRetry records success before the first new output of the lead.
func (a *Agent) resolveLeadRetry(t *transcript) {
	if a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	lead := t == a.out.lead
	a.Mu.Unlock()
	if lead {
		a.leadRetry.Resolve(a.sink)
	}
}

type textDeltaPayload struct {
	Text  string     `json:"text"`
	Agent eventAgent `json:"agent"`
}

type assistantFinishedPayload struct {
	Text  string     `json:"text"`
	Agent eventAgent `json:"agent"`
}

type reasoningFinishedPayload struct {
	Reasoning string     `json:"reasoning"`
	Agent     eventAgent `json:"agent"`
}

// handleReasoningDelta counts one reasoning chunk and keeps it for the row.
func (a *Agent) handleReasoningDelta(payload json.RawMessage) {
	var delta textDeltaPayload
	if json.Unmarshal(payload, &delta) != nil || delta.Text == "" {
		return
	}
	t := a.routeContentMeta(delta.Agent)
	if t == nil {
		return
	}
	a.resolveLeadRetry(t)
	a.Mu.Lock()
	if t.reasoningFlushed {
		// The reasoning of the previous message reached the transcript, and this
		// chunk starts the next message's.
		t.reasoning.Reset()
		t.reasoningFlushed = false
	}
	t.reasoning.WriteString(delta.Text)
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.ModelTextProgress(t.progressScope("reasoning"), delta.Text))
}

// discardUnflushedReasoning removes reasoning that the native model did not retain.
func (a *Agent) discardUnflushedReasoning(t *transcript) {
	a.Mu.Lock()
	t.reasoning.Reset()
	t.reasoningFlushed = false
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.ResetModelScopeProgress(t.progressScope("reasoning")))
}

// handleAssistantDelta counts one text chunk.
func (a *Agent) handleAssistantDelta(payload json.RawMessage) {
	var delta textDeltaPayload
	if json.Unmarshal(payload, &delta) != nil || delta.Text == "" {
		return
	}
	t := a.routeContentMeta(delta.Agent)
	if t == nil {
		return
	}
	a.resolveLeadRetry(t)
	a.Mu.Lock()
	t.text.WriteString(delta.Text)
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.ModelTextProgress(t.progressScope("text"), delta.Text))
}

// handleAssistantFinished persists one message's text, after the reasoning the
// message streamed before it.
func (a *Agent) handleAssistantFinished(event hubEvent) {
	var finished *assistantFinishedPayload
	if err := json.Unmarshal(event.Payload, &finished); err != nil || finished == nil {
		slog.Debug("cline ignored a malformed final text frame", "agent_id", a.AgentID(), "error", err)
		return
	}
	t := a.routeContentMeta(finished.Agent)
	if t == nil {
		return
	}
	if strings.TrimSpace(finished.Text) != "" {
		a.resolveLeadRetry(t)
	}
	a.flushReasoning(t, event.SessionID)
	a.Mu.Lock()
	t.text.Reset()
	completion := noCompletion
	if a.turn.steersPending > 0 {
		completion = agent.MessageCompletionInterrupted
	}
	if t.childID == "" {
		a.out.lastLeadText = finished.Text
	}
	a.Mu.Unlock()
	sink := a.sinkFor(t)
	sink.ReportProgress(agent.CompleteModelProgress(t.progressScope("text")))
	if strings.TrimSpace(finished.Text) == "" {
		return
	}
	a.persistReplyRow(t, event.Raw, completion)
	a.Mu.Lock()
	t.written.texts++
	a.Mu.Unlock()
}

// handleReasoningFinished persists the reasoning of a message that had no text,
// and drops the copy of reasoning that its text already put in place.
func (a *Agent) handleReasoningFinished(event hubEvent) {
	var finished *reasoningFinishedPayload
	if err := json.Unmarshal(event.Payload, &finished); err != nil || finished == nil {
		slog.Debug("cline ignored a malformed final reasoning frame", "agent_id", a.AgentID(), "error", err)
		return
	}
	t := a.routeContentMeta(finished.Agent)
	if t == nil {
		return
	}
	a.Mu.Lock()
	flushed := t.reasoningFlushed
	t.reasoning.Reset()
	t.reasoningFlushed = false
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.CompleteModelProgress(t.progressScope("reasoning")))
	if flushed || strings.TrimSpace(finished.Reasoning) == "" {
		return
	}
	a.resolveLeadRetry(t)
	a.persistRow(t, event.Raw, noCompletion)
	a.Mu.Lock()
	t.written.reasoning++
	a.Mu.Unlock()
}

// flushReasoning persists the reasoning that the current message streamed, in
// Cline's own `reasoning.finished` shape, before the message's text or tool
// call reaches the transcript. The worker then drops the late
// `reasoning.finished`.
func (a *Agent) flushReasoning(t *transcript, sessionID string) {
	a.flushReasoningWith(t, sessionID, noCompletion)
}

// flushReasoningWith persists the streamed reasoning with completion.
func (a *Agent) flushReasoningWith(t *transcript, sessionID string, completion agent.MessageCompletion) {
	a.Mu.Lock()
	text := t.reasoning.String()
	if t.reasoningFlushed || strings.TrimSpace(text) == "" {
		a.Mu.Unlock()
		return
	}
	t.reasoningFlushed = true
	a.Mu.Unlock()
	a.persistRow(t, eventRow(sessionID, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": text}), completion)
	a.Mu.Lock()
	t.written.reasoning++
	a.Mu.Unlock()
}

// toolEvent is the payload of tool.started, tool.updated and tool.finished
// that the worker reads.
type toolEvent struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	Update     json.RawMessage `json:"update"`
	Output     json.RawMessage `json:"output"`
	Error      string          `json:"error"`
	Agent      eventAgent      `json:"agent"`
}

// handleToolStarted opens one tool call in transcript t.
func (a *Agent) handleToolStarted(t *transcript, event hubEvent, call toolEvent) {
	a.resolveLeadRetry(t)
	a.flushReasoning(t, event.SessionID)
	a.openToolRow(t, call.ToolCallID, call.ToolName, event.Raw)
}

// openToolRow persists a call's opening row and opens its span.
func (a *Agent) openToolRow(t *transcript, id, name string, row []byte) {
	a.Mu.Lock()
	t.nextOrder++
	t.open[id] = &openTool{id: id, name: name, row: row, order: t.nextOrder}
	t.written.tools[id] = true
	a.out.toolOwner[id] = t
	a.Mu.Unlock()
	if err := providerkit.OpenToolSpan(a.sinkFor(t), agent.MessageContent{Original: row}, id, name, startsSubagent(name)); err != nil {
		slog.Error("cline persist tool call", "agent_id", a.AgentID(), "tool", name, "error", err)
	}
}

// handleToolUpdated streams a running command's output to its card.
func (a *Agent) handleToolUpdated(t *transcript, call toolEvent) {
	var update struct {
		Chunk string `json:"chunk"`
	}
	if json.Unmarshal(call.Update, &update) != nil || update.Chunk == "" {
		return
	}
	a.Mu.Lock()
	tool := t.open[call.ToolCallID]
	if tool == nil {
		a.Mu.Unlock()
		return
	}
	tool.total = agent.SaturatingAdd(tool.total, int64(len(update.Chunk)))
	var clipped bool
	tool.tail, clipped = agent.ClipTailBytes(tool.tail+update.Chunk, liveOutputLimit)
	tool.tailLost = tool.tailLost || clipped
	total, tail, lost := tool.total, tool.tail, tool.tailLost
	a.Mu.Unlock()
	sink := a.sinkFor(t)
	sink.ReportProgress(agent.OutputExactTotalProgress(call.ToolCallID, total))
	sink.ReportProgress(agent.OutputTailProgress(call.ToolCallID, tail, lost))
}

// handleToolFinished closes one tool call in transcript t, and reports whether
// the call was open there.
func (a *Agent) handleToolFinished(t *transcript, event hubEvent, call toolEvent) bool {
	a.Mu.Lock()
	tool := t.open[call.ToolCallID]
	delete(t.open, call.ToolCallID)
	delete(a.out.toolOwner, call.ToolCallID)
	if t.childID == "" && a.turn.active {
		a.turn.toolUses++
	}
	a.Mu.Unlock()
	name := call.ToolName
	if tool != nil && name == "" {
		name = tool.name
	}
	a.closeToolRow(t, call.ToolCallID, name, event.Raw, noCompletion)
	return tool != nil
}

// closeToolRow persists a call's closing row and closes its span.
func (a *Agent) closeToolRow(t *transcript, id, name string, row []byte, completion agent.MessageCompletion) {
	sink := a.sinkFor(t)
	sink.ReportProgress(agent.CompleteOutputProgress(id))
	if !a.IsDiscardingOutput() {
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row, Completion: completion}, agent.SpanInfo{
			SpanID:   id,
			SpanType: name,
			Closing:  true,
		}); err != nil {
			slog.Error("cline persist tool result", "agent_id", a.AgentID(), "tool", name, "error", err)
		}
	}
	sink.CloseSpan(id)
}

// closeOpenTools closes every call that transcript t still holds open, in the
// order they started, with the call's own opening row and the completion that
// ended them.
func (a *Agent) closeOpenTools(t *transcript, completion agent.MessageCompletion) {
	a.Mu.Lock()
	open := make([]*openTool, 0, len(t.open))
	for _, tool := range t.open {
		open = append(open, tool)
		delete(a.out.toolOwner, tool.id)
	}
	clear(t.open)
	a.Mu.Unlock()
	sort.Slice(open, func(i, j int) bool { return open[i].order < open[j].order })
	for _, tool := range open {
		a.closeToolRow(t, tool.id, tool.name, tool.row, completion)
	}
}

// flushPending persists what transcript t streamed and no finished event
// stored: the reasoning and the text of a message that a stop or a failure cut
// short. Each row carries completion, so the transcript shows where it ended.
// The rows keep Cline's own shapes, `reasoning.finished` and
// `assistant.finished`.
func (a *Agent) flushPending(t *transcript, sessionID string, completion agent.MessageCompletion) {
	a.flushReasoningWith(t, sessionID, completion)
	a.Mu.Lock()
	text := t.text.String()
	t.text.Reset()
	t.reasoning.Reset()
	t.reasoningFlushed = false
	a.Mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return
	}
	a.persistReplyRow(t, eventRow(sessionID, contracts.ClineEventAssistantFinished, map[string]any{"text": text}), completion)
	a.Mu.Lock()
	t.written.texts++
	a.Mu.Unlock()
}

// persistRow persists one row of Cline's in transcript t.
func (a *Agent) persistRow(t *transcript, row []byte, completion agent.MessageCompletion) {
	a.persistTranscriptRow(t, agent.MessageContent{Original: row, Completion: completion}, false)
}

func (a *Agent) persistReplyRow(t *transcript, row []byte, completion agent.MessageCompletion) {
	content := agent.MessageContent{Original: row, Completion: completion}
	_, hasReplyText := (clineProvider{}).ReplyText(content)
	a.persistTranscriptRow(t, content, hasReplyText)
}

func (a *Agent) persistTranscriptRow(t *transcript, content agent.MessageContent, hasReplyText bool) {
	if a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	reply := a.turn.inputReply
	if t == a.out.lead && reply.ownsOutput(a.sessionID) {
		content.AgentSessionID, content.NativeTurnID = reply.receipt.SessionID, reply.receipt.TurnID
		if hasReplyText && !reply.autonomous {
			ordinal := int64(len(reply.items)) + 2
			content.IdempotencyKey = "cline-reply:" + reply.receipt.TurnID + ":" + strconv.FormatInt(ordinal, 10)
			reply.items = append(reply.items, agent.NativeReplyItem{Ordinal: ordinal, MessageKey: content.IdempotencyKey})
		}
	}
	a.Mu.Unlock()
	if err := a.sinkFor(t).PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		if content.NativeTurnID != "" && !agent.OutputRetained(err) {
			a.Mu.Lock()
			reply.ambiguous = true
			a.Mu.Unlock()
		}
		slog.Error("cline persist row", "agent_id", a.AgentID(), "error", err)
	}
}

// eventRow encodes a row the worker writes itself, in Cline's event envelope.
func eventRow(sessionID, event string, payload any) []byte {
	row, err := json.Marshal(map[string]any{
		"version":                        hubProtocolVersion,
		contracts.ClineEventFieldEvent:   event,
		"sessionId":                      sessionID,
		contracts.ClineEventFieldPayload: payload,
	})
	if err != nil {
		// The worker builds every payload here from strings, numbers and raw
		// JSON that already decoded, which always encodes.
		panic(err)
	}
	return row
}

// The run reasons a worker-written turn end states.
const (
	runReasonCompleted = contracts.ClineRunReasonCompleted
	runReasonError     = contracts.ClineRunReasonError
	runReasonAborted   = contracts.ClineRunReasonAborted
)

// runEndRow is the turn-end row the worker writes for a turn that ended with no
// end event of Cline's: run.completed for a turn that had no run, run.aborted,
// or run.failed with the error.
func runEndRow(sessionID, reason, message string) []byte {
	event := contracts.ClineEventRunFailed
	switch reason {
	case runReasonCompleted, contracts.ClineRunReasonMaxIterations, contracts.ClineRunReasonMistakeLimit:
		event = contracts.ClineEventRunCompleted
	case runReasonAborted:
		event = contracts.ClineEventRunAborted
	}
	payload := map[string]any{"reason": reason}
	if message != "" {
		payload["error"] = message
	}
	return eventRow(sessionID, event, payload)
}

// trimRunEnd drops the conversation, the tool call records and the session
// snapshot from the end event of a run, and keeps what the turn-end row reads:
// the reason, the error, and the result's text, usage, iterations, finish
// reason, duration and model.
func trimRunEnd(raw []byte) []byte {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return raw
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(envelope[contracts.ClineEventFieldPayload], &payload) != nil {
		return raw
	}
	delete(payload, "snapshot")
	if result, ok := payload["result"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(result, &fields) == nil {
			delete(fields, "messages")
			delete(fields, "toolCalls")
			if model, ok := fields["model"]; ok {
				var info map[string]json.RawMessage
				if json.Unmarshal(model, &info) == nil {
					delete(info, "info")
					fields["model"], _ = json.Marshal(info)
				}
			}
			payload["result"], _ = json.Marshal(fields)
		}
	}
	envelope[contracts.ClineEventFieldPayload], _ = json.Marshal(payload)
	trimmed, err := json.Marshal(envelope)
	if err != nil {
		return raw
	}
	return trimmed
}

// runEnd is the payload of the end event of a run that the worker reads.
type runEnd struct {
	Reason string          `json:"reason"`
	Result json.RawMessage `json:"result"`
}

// completionOf maps a run's reason onto the turn's completion. An interrupt the
// user asked for ends as interrupted whatever the reason states.
func completionOf(reason string, interrupted bool) agent.MessageCompletion {
	switch {
	case reason == contracts.ClineRunReasonAborted || interrupted:
		return agent.MessageCompletionInterrupted
	case reason == contracts.ClineRunReasonCompleted, reason == contracts.ClineRunReasonMaxIterations:
		return agent.MessageCompletionComplete
	default:
		return agent.MessageCompletionError
	}
}

// handleRunEnded ends the turn with Cline's own end event of the run.
func (a *Agent) handleRunEnded(event hubEvent) {
	var end runEnd
	_ = json.Unmarshal(event.Payload, &end)
	nativeEnd := nativeRunEnd{event: event, reason: end.Reason, result: readInputResult(end.Result)}
	a.Mu.Lock()
	if previous := a.detachedReply; previous != nil && previous.receipt.SessionID == event.SessionID && nativeEnd.result.reason != "" &&
		(previous.end == nil || previous.end.event.EventID == event.EventID) {
		if previous.end == nil {
			previous.end = &nativeEnd
		}
		a.Mu.Unlock()
		a.publishDetachedReply()
		return
	}
	if !a.turn.active || a.turn.settling {
		a.Mu.Unlock()
		// A run ended that no turn tracked: the end of a turn that the worker
		// already closed, or the end that the detach of a mode change publishes.
		// It states nothing new.
		return
	}
	if reply := a.turn.inputReply; reply != nil && reply.autonomous {
		// The queue drain never returns a command result. Result-bearing
		// ends belong to the input command that preceded the drain.
		failed := reply.started && nativeEnd.result.reason == "" && event.Event == contracts.ClineEventRunFailed && end.Reason == contracts.ClineRunReasonError
		if !failed {
			a.Mu.Unlock()
			return
		}
		outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
		if reply.ownsOutput(event.SessionID) && a.out.lead != nil && len(a.out.lead.spawns) == 0 && !a.team.busy() {
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		interrupted := a.turn.interruptRequested
		a.Mu.Unlock()
		a.endTurnWithOutcome(completionOf(end.Reason, interrupted), trimRunEnd(event.Raw), outcome)
		return
	}
	if reply := a.turn.inputReply; reply.ownsOutput(event.SessionID) && nativeEnd.result.reason != "" && !reply.resultSeen {
		if reply.end == nil {
			reply.end = &nativeEnd
		} else if reply.end.event.EventID != event.EventID {
			reply.ambiguous = true
		}
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	a.finishNativeRun(nativeEnd)
}

// finishNativeRun persists a native end once its command result is available
// or the turn no longer has enough evidence for an exact outcome.
func (a *Agent) finishNativeRun(end nativeRunEnd) {
	a.Mu.Lock()
	outcome := end.outcome(a.turn.inputReply)
	interrupted := a.turn.interruptRequested
	a.Mu.Unlock()
	a.endTurnWithOutcome(completionOf(end.reason, interrupted), trimRunEnd(end.event.Raw), outcome)
}

// endTurn ends the running turn: it withdraws the turn's requests, persists
// what the turn streamed and left open, persists the turn-end row, and then
// clears the turn. The caller holds dispatchMu.
//
// The turn stays set while the worker writes the rows, so a message that
// arrives meanwhile waits in the input queue instead of starting a turn before
// this one's end row. The worker persists the row BEFORE the flag clears,
// because the clear is the settle edge that spends the turn's tool count (see
// agent.TranscriptServices.PersistTurnEnd).
//
// A mode change that waited for the turn keeps the flag set with a settling
// turn, and a goroutine applies the change and then clears it (afterTurn). The
// input queue holds the next message until the session runs in its new mode.
func (a *Agent) endTurn(completion agent.MessageCompletion, row []byte) {
	a.endTurnWithOutcome(completion, row, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
}

func (a *Agent) endTurnWithTeardown(completion agent.MessageCompletion, row []byte, teardown bool) {
	a.endTurnWithState(completion, row, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED, nil, teardown)
}

func (a *Agent) endTurnWithOutcome(completion agent.MessageCompletion, row []byte, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.endTurnWithSuccessor(completion, row, outcome, nil)
}

// endTurnWithSuccessor keeps the queue active when Cline has already submitted
// another input. The previous command can return its result during that run.
func (a *Agent) endTurnWithSuccessor(completion agent.MessageCompletion, row []byte, outcome leapmuxv1.AgentInputTurnOutcome, successor *inputReply) {
	a.endTurnWithState(completion, row, outcome, successor, false)
}

func (a *Agent) endTurnWithState(completion agent.MessageCompletion, row []byte, outcome leapmuxv1.AgentInputTurnOutcome, successor *inputReply, teardown bool) {
	a.leadRetry.Reset()
	endedAt := a.clock.Now()
	a.Mu.Lock()
	if !a.turn.active {
		a.Mu.Unlock()
		return
	}
	turn := a.turn
	sessionID := a.sessionID
	lead := a.out.lead
	usage := maps.Clone(a.contextUsage)
	a.Mu.Unlock()

	closeOpen := teardown || completion == agent.MessageCompletionInterrupted
	a.withdrawAllControls()
	a.finishTranscriptCompaction(lead, completion)
	if closeOpen {
		a.settleSpawns(completion)
	}
	a.flushPending(lead, sessionID, completion)
	if closeOpen {
		toolCompletion := completion
		if toolCompletion == agent.MessageCompletionComplete {
			toolCompletion = agent.MessageCompletionError
		}
		a.closeOpenTools(lead, toolCompletion)
	}
	var previous *inputReply
	if !a.IsDiscardingOutput() {
		content := agent.MessageContent{
			Original:   row,
			Completion: completion,
			Metadata:   rowMetadata(usage, turnDurationMs(turn.startedAt, endedAt)),
		}
		a.Mu.Lock()
		toolUses := a.turn.toolUses
		if reply := turn.inputReply; reply != nil {
			content.AgentSessionID, content.NativeTurnID = reply.receipt.SessionID, reply.receipt.TurnID
			if successor != nil && !reply.ambiguous {
				previous = reply
			} else if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED || reply.ambiguous {
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
			}
			content.InputTurnOutcome = outcome
		}
		a.Mu.Unlock()
		if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
			slog.Error("cline persist turn end", "agent_id", a.AgentID(), "error", err)
			if !agent.OutputRetained(err) {
				previous = nil
				a.retireInputReply(turn.inputReply)
			}
		}
	} else {
		a.retireInputReply(turn.inputReply)
	}
	if closeOpen {
		providerkit.ResetSpansAndProgress(a.sink)
	}

	a.Mu.Lock()
	change := a.modeRebuild
	stopping := a.StoppedLocked() || a.ctx.Err() != nil
	if successor != nil && !stopping {
		a.detachedReply = previous
		a.turn = a.drainedTurn(successor, endedAt)
		a.turn.actModeApproved = change != nil && turn.actModeApproved && completion == agent.MessageCompletionComplete
		change = nil
	} else if change != nil && !stopping {
		a.turn = turnState{active: true, settling: true, startedAt: endedAt}
	} else {
		a.turn = turnState{}
		a.modeRebuild = nil
		change = nil
	}
	a.Mu.Unlock()
	if successor != nil && stopping {
		a.retireInputReply(previous)
		a.retireInputReply(successor)
	}
	a.PublishTurnActive()
	if change == nil {
		return
	}
	// The plan continues only after a turn in which the model called the plan
	// tool and the user approved it, and which completed.
	a.afterTurn(change.continuePlan && turn.actModeApproved && completion == agent.MessageCompletionComplete)
}

// handleUsageUpdated broadcasts context usage for the lead or a tagged child transcript.
// Untagged payloads keep the lead-only guard while any subagent runs.
func (a *Agent) handleUsageUpdated(payload json.RawMessage) {
	var usage struct {
		Delta struct {
			InputTokens      int64 `json:"inputTokens"`
			OutputTokens     int64 `json:"outputTokens"`
			CacheReadTokens  int64 `json:"cacheReadTokens"`
			CacheWriteTokens int64 `json:"cacheWriteTokens"`
		} `json:"delta"`
		Agent eventAgent `json:"agent"`
	}
	if json.Unmarshal(payload, &usage) != nil {
		return
	}
	var target *transcript
	a.Mu.Lock()
	if usage.Agent.hasTag() {
		target, _ = a.routeTaggedLocked(usage.Agent)
	} else if usage.Agent.Kind == agentKindLead && len(a.out.spawns) == 0 {
		target = a.out.lead
	}
	a.Mu.Unlock()
	if target == nil {
		return
	}
	// Cline's input count includes the cached tokens, and it is the size of the
	// context that the request sent.
	uncached := usage.Delta.InputTokens - usage.Delta.CacheReadTokens - usage.Delta.CacheWriteTokens
	if uncached < 0 {
		uncached = 0
	}
	reading := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:      uncached,
		CacheWrite: usage.Delta.CacheWriteTokens,
		CacheRead:  usage.Delta.CacheReadTokens,
		Output:     usage.Delta.OutputTokens,
	})
	reading[contracts.ContextUsageFieldContextTokens] = usage.Delta.InputTokens
	if window := a.contextWindow(); window > 0 {
		reading[contracts.ContextUsageFieldContextWindow] = window
	}
	a.Mu.Lock()
	var changed bool
	if target == a.out.lead {
		changed = !maps.Equal(reading, a.contextUsage)
		a.contextUsage = reading
	} else {
		changed = !maps.Equal(reading, target.contextUsage)
		target.contextUsage = reading
	}
	a.Mu.Unlock()
	if changed {
		a.sinkFor(target).BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: maps.Clone(reading)})
	}
}

// rowMetadata encodes the fields LeapMux calculates for a turn-end row: the
// context usage, and the turn's duration.
func rowMetadata(usage map[string]any, durationMs *int64) []byte {
	fields := map[string]any{}
	if len(usage) > 0 {
		fields[contracts.SessionInfoKeyContextUsage] = usage
	}
	if durationMs != nil {
		fields[contracts.MessageMetadataFieldDurationMs] = *durationMs
	}
	if len(fields) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("cline encode row metadata", "error", err)
		return nil
	}
	return encoded
}

// turnDurationMs measures one turn, or nil for a turn with no start or a clock
// that moved backwards.
func turnDurationMs(startedAt, endedAt time.Time) *int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return nil
	}
	ms := endedAt.Sub(startedAt).Milliseconds()
	return &ms
}

// lastPlanText returns the lead's last answer, which is the plan when the
// model asks to switch to Act mode.
func (a *Agent) lastPlanText() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return strings.TrimSpace(a.out.lastLeadText)
}

// toolCallSink returns the services of the transcript that shows the tool call
// id, or the lead's when no open call has that id.
func (a *Agent) toolCallSink(id string) agent.ProviderServices {
	a.Mu.Lock()
	t := a.out.toolOwner[id]
	a.Mu.Unlock()
	return a.sinkFor(t)
}
