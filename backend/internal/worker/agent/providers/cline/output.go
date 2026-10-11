package cline

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
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
	// transcript that shows it. Cline states no agent on a tool event, and the
	// call's id is what routes its later events.
	toolOwner map[string]*transcript
	// dropped holds the tool calls whose start reached no transcript: the calls
	// of a subagent whose output the worker could not attribute (subagent.go).
	// The worker drops their later events too.
	dropped map[string]bool
	// spawns holds each spawn_agent call that runs, by tool call id.
	spawns map[string]*spawnCall
	// claimed holds the stored child sessions that a backfill used, so two
	// subagents with the same task never take one session.
	claimed map[string]bool
	// lastLeadText is the text of the lead's last message. It is the plan when
	// the model asks to switch to Act mode.
	lastLeadText string
}

func newOutputState() outputState {
	return outputState{
		lead:      newTranscript(""),
		toolOwner: make(map[string]*transcript),
		dropped:   make(map[string]bool),
		spawns:    make(map[string]*spawnCall),
		claimed:   make(map[string]bool),
	}
}

// transcript is the output state of one transcript: the lead's, or one child's.
// Guarded by Agent.Mu, and changed only under dispatchMu, except by a backfill,
// which writes a child transcript that no live event reaches any more.
type transcript struct {
	// childID is the child agent, or "" for the lead.
	childID string
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
	// nextOrder orders the open calls, so a turn end closes them in the order
	// they started.
	nextOrder uint64
	// written counts what reached this transcript live, so a later backfill
	// from Cline's stored messages writes only the rest (subagent.go).
	written writtenCounts
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

// handleReasoningDelta counts one reasoning chunk and keeps it for the row.
func (a *Agent) handleReasoningDelta(t *transcript, payload json.RawMessage) {
	var delta struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &delta) != nil || delta.Text == "" {
		return
	}
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

// handleAssistantDelta counts one text chunk.
func (a *Agent) handleAssistantDelta(t *transcript, payload json.RawMessage) {
	var delta struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &delta) != nil || delta.Text == "" {
		return
	}
	a.Mu.Lock()
	t.text.WriteString(delta.Text)
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.ModelTextProgress(t.progressScope("text"), delta.Text))
}

// handleAssistantFinished persists one message's text, after the reasoning the
// message streamed before it.
func (a *Agent) handleAssistantFinished(t *transcript, event hubEvent) {
	var finished struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(event.Payload, &finished)
	a.flushReasoning(t, event.SessionID)
	a.Mu.Lock()
	t.text.Reset()
	if t.childID == "" {
		a.out.lastLeadText = finished.Text
	}
	a.Mu.Unlock()
	sink := a.sinkFor(t)
	sink.ReportProgress(agent.CompleteModelProgress(t.progressScope("text")))
	if strings.TrimSpace(finished.Text) == "" {
		return
	}
	a.persistRow(t, event.Raw, noCompletion)
	a.Mu.Lock()
	t.written.texts++
	a.Mu.Unlock()
}

// handleReasoningFinished persists the reasoning of a message that had no text,
// and drops the copy of reasoning that its text already put in place.
func (a *Agent) handleReasoningFinished(t *transcript, event hubEvent) {
	a.Mu.Lock()
	flushed := t.reasoningFlushed
	t.reasoning.Reset()
	t.reasoningFlushed = false
	a.Mu.Unlock()
	a.sinkFor(t).ReportProgress(agent.CompleteModelProgress(t.progressScope("reasoning")))
	if flushed {
		return
	}
	var finished struct {
		Reasoning string `json:"reasoning"`
	}
	_ = json.Unmarshal(event.Payload, &finished)
	if strings.TrimSpace(finished.Reasoning) == "" {
		// A redacted reasoning holds nothing a reader can read.
		return
	}
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
}

// handleToolStarted opens one tool call in transcript t.
func (a *Agent) handleToolStarted(t *transcript, event hubEvent, call toolEvent) {
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
	a.persistRow(t, eventRow(sessionID, contracts.ClineEventAssistantFinished, map[string]any{"text": text}), completion)
	a.Mu.Lock()
	t.written.texts++
	a.Mu.Unlock()
}

// persistRow persists one row of Cline's in transcript t.
func (a *Agent) persistRow(t *transcript, row []byte, completion agent.MessageCompletion) {
	if a.IsDiscardingOutput() {
		return
	}
	if err := a.sinkFor(t).PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row, Completion: completion}, agent.SpanInfo{}); err != nil {
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
	case runReasonCompleted:
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
	Reason string `json:"reason"`
	Error  string `json:"error"`
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
	a.Mu.Lock()
	active, interrupted, settling := a.turn.active, a.turn.interruptRequested, a.turn.settling
	a.Mu.Unlock()
	if !active || settling {
		// A run ended that no turn tracked: the end of a turn that the worker
		// already closed, or the end that the detach of a mode change publishes.
		// It states nothing new.
		return
	}
	a.endTurn(completionOf(end.Reason, interrupted), trimRunEnd(event.Raw))
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

	a.withdrawAllControls()
	a.settleSpawns(completion)
	a.flushPending(lead, sessionID, completion)
	// A call that a completed turn still held open did not finish: the run
	// ended around it.
	toolCompletion := completion
	if toolCompletion == agent.MessageCompletionComplete {
		toolCompletion = agent.MessageCompletionError
	}
	a.closeOpenTools(lead, toolCompletion)
	if !a.IsDiscardingOutput() {
		content := agent.MessageContent{
			Original:   row,
			Completion: completion,
			Metadata:   rowMetadata(usage, turnDurationMs(turn.startedAt, endedAt)),
		}
		a.Mu.Lock()
		toolUses := a.turn.toolUses
		a.Mu.Unlock()
		if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
			slog.Error("cline persist turn end", "agent_id", a.AgentID(), "error", err)
		}
	}
	a.sink.ResetSpans()

	a.Mu.Lock()
	change := a.modeRebuild
	stopping := a.StoppedLocked() || a.ctx.Err() != nil
	if change != nil && !stopping {
		a.turn = turnState{active: true, settling: true, startedAt: endedAt}
	} else {
		a.turn = turnState{}
		a.modeRebuild = nil
		change = nil
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
	if change == nil {
		return
	}
	// The plan continues only after a turn in which the model called the plan
	// tool and the user approved it, and which completed.
	a.afterTurn(change.continuePlan && turn.actModeApproved && completion == agent.MessageCompletionComplete)
}

// handleUsageUpdated broadcasts the context usage of the lead's last request.
// The hub marks a teammate's usage as a teammate's; a subagent's usage it marks
// as the lead's, so a usage event that arrives while a subagent runs is the
// subagent's and changes nothing here.
func (a *Agent) handleUsageUpdated(payload json.RawMessage) {
	var usage struct {
		Delta struct {
			InputTokens      int64 `json:"inputTokens"`
			OutputTokens     int64 `json:"outputTokens"`
			CacheReadTokens  int64 `json:"cacheReadTokens"`
			CacheWriteTokens int64 `json:"cacheWriteTokens"`
		} `json:"delta"`
		Agent struct {
			Kind string `json:"kind"`
		} `json:"agent"`
	}
	if json.Unmarshal(payload, &usage) != nil || usage.Agent.Kind != "lead" || a.spawnRuns() {
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
	changed := !maps.Equal(reading, a.contextUsage)
	a.contextUsage = reading
	a.Mu.Unlock()
	if changed {
		a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: maps.Clone(reading)})
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
