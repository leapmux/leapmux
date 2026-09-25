package kimi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's conversation output.
//
// Every event carries the id of the agent it belongs to: `main`, or a subagent's
// `agent-N`. The main agent's rows go to this agent's own transcript and each
// subagent's go to its child transcript (subagent.go). Both run through the
// same handlers, keyed by the event's agent, so a subagent's tool card and turn
// read exactly like the main agent's.
//
// The persisted rows are the server's own event payloads, verbatim:
// `tool.call.started` opens a tool span and `tool.result` closes it,
// `turn.ended` is the turn-end divider, and the notices (compaction, a retry, a
// warning, a turn the agent started by itself) are notification rows. The only
// rows the worker builds are the assembled text and thinking: the server
// streams those as `assistant.delta` and `thinking.delta` and never repeats
// them in a durable event.

// kimiLiveOutputLimit caps the running command output the worker holds for the
// live tail. The sink caps the tail again before it broadcasts; this cap keeps
// the joined string from growing without limit for a command that prints for
// minutes.
const kimiLiveOutputLimit = 8192

// kimiRun is the output state of one agent of the session.
type kimiRun struct {
	// agentID is the server's id for the agent: `main`, or `agent-N`.
	agentID string
	// generation holds the text and thinking the agent streamed since the last
	// boundary.
	generation providerkit.GenerationBuffer
	// tools holds every tool call the agent opened and nothing closed yet,
	// keyed by the server's tool call id.
	tools map[string]*kimiTool
	// spawns maps an Agent or AgentSwarm call id to the span it opened, for the
	// subagents it starts. It outlives the call's own row: a background spawn
	// reports its subagent after the call returned.
	spawns map[string]kimiSpawn
	// nextOrder orders the tool calls for a turn-end sweep.
	nextOrder uint64
	// turnID is the id of the agent's running or last turn. It keys the span of
	// each tool call, because the server's tool call ids are the model's and a
	// later turn can repeat one.
	turnID int64
	// turnActive is true while the agent runs a turn.
	turnActive bool
	// userTurn is true while a subagent runs a turn that the user started from
	// the subagent's tab. Such a turn runs as no task, so its own end is the end
	// of the run. A subagent.* event or a task.terminated ends every other run
	// of a subagent: the run can span more than one turn, as the retry turn of a
	// swarm member that a rate limit suspended does.
	userTurn bool
	// started is true once the subagent ran a turn, so a later turn's prompt is a
	// message delivered in the middle of the transcript.
	started bool
	// pending holds a subagent's events that arrived before its spawn linked it
	// to a child transcript. They replay in order once it does.
	pending []kimiEvent
}

// kimiTool is one open tool call.
type kimiTool struct {
	spanID string
	name   string
	order  uint64
	// lastFrame is the call's own opening payload. A turn that ends while the
	// call runs stores it as the call's last row, with the completion column
	// stating how the turn ended.
	lastFrame []byte
	// outputBytes and tail are the running output, for the live counter.
	outputBytes int64
	tail        string
	tailLost    bool
}

// kimiSpawn is the span of one Agent or AgentSwarm call, and what its input
// states about the subagents it starts.
type kimiSpawn struct {
	spanID string
	name   string
	// prompt is an Agent call's prompt, which opens its child transcript.
	prompt string
	// label is an AgentSwarm call's description, which heads its workflow group.
	label string
}

// kimiMaxPendingEvents caps the events held for a subagent that no spawn
// linked yet. The server states the spawn after the subagent's first few events
// (its creation, its first turn.started, its context setup), so a run that fills
// the cap belongs to an agent no spawn will ever claim -- a side agent -- and its
// events are dropped rather than held for the life of the session.
const kimiMaxPendingEvents = 256

// runLocked returns the run of agentID, creating it on first use. The caller
// holds a.Mu.
func (a *Agent) runLocked(agentID string) *kimiRun {
	if a.runs == nil {
		a.runs = make(map[string]*kimiRun)
	}
	run := a.runs[agentID]
	if run == nil {
		run = &kimiRun{agentID: agentID, tools: make(map[string]*kimiTool), spawns: make(map[string]kimiSpawn)}
		a.runs[agentID] = run
	}
	return run
}

func (a *Agent) run(agentID string) *kimiRun {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.runLocked(agentID)
}

// runSink returns the transcript a run's rows go to: this agent's own for the
// main agent, a child transcript for a linked subagent, or nil for a subagent no
// spawn linked yet.
func (a *Agent) runSink(run *kimiRun) agent.ProviderServices {
	if run.agentID == kimiMainAgentID {
		return a.sink
	}
	if childID, ok := a.children.childOf(run.agentID); ok {
		return a.sink.ChildSink(childID)
	}
	return nil
}

// holdForLink queues an event of a subagent that no spawn linked yet, and
// reports whether it did.
//
// The server states a subagent's first turn BEFORE the spawn that links the
// subagent to the call that started it, so the subagent's transcript does not
// exist yet when its first events arrive. Every event of such a subagent waits,
// in order, and replays once the spawn links it (replayPending). The events of
// the main agent and of a linked subagent need no wait, and neither does a
// control request: the user answers it on the main agent's tab, and a tool call
// parked on it must never wait for a link.
func (a *Agent) holdForLink(event kimiEvent) bool {
	if event.AgentID == kimiMainAgentID || !kimiEventWaitsForLink(event.Type) {
		return false
	}
	if _, linked := a.children.childOf(event.AgentID); linked {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	run := a.runLocked(event.AgentID)
	if len(run.pending) >= kimiMaxPendingEvents {
		slog.Debug("kimi drops an event of an unlinked agent", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "type", event.Type)
		return true
	}
	run.pending = append(run.pending, event)
	return true
}

// kimiEventWaitsForLink reports whether an event of an unlinked subagent waits
// for the link. A control request and its resolution do not.
func kimiEventWaitsForLink(eventType string) bool {
	switch eventType {
	case contracts.KimiEventApprovalRequested, contracts.KimiEventApprovalResolved,
		contracts.KimiEventQuestionRequested, contracts.KimiEventQuestionAnswered,
		contracts.KimiEventQuestionDismissed:
		return false
	default:
		return true
	}
}

// replayPending dispatches the events a subagent sent before its spawn linked
// it. It runs on the dispatcher, which already holds dispatchMu.
func (a *Agent) replayPending(run *kimiRun) {
	a.Mu.Lock()
	pending := run.pending
	run.pending = nil
	a.Mu.Unlock()
	for _, event := range pending {
		a.dispatchEvent(event)
	}
}

// kimiSpanID keys one tool call's span. The server's tool call id is the
// model's, and a later turn -- or the next session after a context clear --
// can repeat it, so the session, the agent and the turn qualify it.
func kimiSpanID(sessionID, agentID string, turnID int64, toolCallID string) string {
	return fmt.Sprintf("%s/%s/%d/%s", sessionID, agentID, turnID, toolCallID)
}

// --- turns ---

// kimiTurnStarted is the turn.started payload.
type kimiTurnStarted struct {
	TurnID int64      `json:"turnId"`
	Origin kimiOrigin `json:"origin"`
	Prompt string     `json:"prompt"`
}

// kimiOrigin states who started a turn.
type kimiOrigin struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (a *Agent) handleTurnStarted(event kimiEvent) {
	var payload kimiTurnStarted
	if !event.decode(&payload) {
		return
	}
	run := a.run(event.AgentID)
	if event.AgentID != kimiMainAgentID {
		a.handleChildTurnStarted(run, payload)
		return
	}
	a.Mu.Lock()
	a.turnActive = true
	a.turnSteerable = kimiOriginTakesSteer(payload.Origin.Kind)
	a.TurnToolUses = 0
	run.turnID = payload.TurnID
	run.turnActive = true
	a.Mu.Unlock()
	a.PublishTurnActive()
	run.generation.Reset()
	a.sink.ReportProgress(agent.ResetModelProgress())
	if kimiOriginIsNotable(payload.Origin.Kind) {
		// The agent started this turn by itself, and nothing in the transcript
		// says why the agent speaks. The row states it.
		a.persistNotification(a.sink, event)
	}
}

// kimiOriginIsNotable reports whether a turn of this origin needs a row that
// states why it started.
//
// A user turn follows the user's own message. A background task's turn follows
// its `task.notified`, which the transcript records and which states the task
// in words. Every other origin -- a goal continuation, a cron fire, a retry, a
// hook -- has nothing else in the transcript to explain it.
func kimiOriginIsNotable(kind string) bool {
	switch kind {
	case contracts.KimiOriginUser, contracts.KimiOriginTask, contracts.KimiOriginBackgroundTask, "":
		return false
	default:
		return true
	}
}

// kimiOriginTakesSteer reports whether a main turn of this origin takes a
// steer.
//
// The server steers into a turn only when a tracked prompt started it
// (loopService.steer). Only the user's own prompt, a skill activation and a
// plugin command submit a tracked prompt to the main agent. A goal
// continuation, a task notification, a cron fire, a retry and a hook submit an
// untracked one. A steer into such a turn fails with PROMPT_NOT_FOUND, and the
// steered text waits in the server's queue behind the turn.
func kimiOriginTakesSteer(kind string) bool {
	switch kind {
	case contracts.KimiOriginUser, contracts.KimiOriginSkillActivation, contracts.KimiOriginPluginCommand:
		return true
	default:
		return false
	}
}

// kimiTurnEnded is the turn.ended payload.
type kimiTurnEnded struct {
	TurnID int64         `json:"turnId"`
	Reason string        `json:"reason"`
	Error  *kimiEventErr `json:"error"`
}

// kimiEventErr is the error object of a failed turn and of an `error` event.
type kimiEventErr struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// key identifies the failure, so the `error` event that repeats it can be told
// from a new one.
func (e *kimiEventErr) key() string {
	if e == nil {
		return ""
	}
	return e.Code + "\x00" + e.Message
}

// kimiTurnCompletion maps a turn's end reason onto LeapMux's completion.
func kimiTurnCompletion(reason string) agent.MessageCompletion {
	switch reason {
	case contracts.KimiTurnEndCompleted:
		return agent.MessageCompletionComplete
	case contracts.KimiTurnEndCancelled:
		return agent.MessageCompletionInterrupted
	case contracts.KimiTurnEndFailed, contracts.KimiTurnEndBlocked:
		return agent.MessageCompletionError
	default:
		// A reason this build does not know. The turn did not state that it
		// completed.
		return agent.MessageCompletionError
	}
}

func (a *Agent) handleTurnEnded(event kimiEvent) {
	var payload kimiTurnEnded
	if !event.decode(&payload) {
		return
	}
	run := a.run(event.AgentID)
	if event.AgentID != kimiMainAgentID {
		a.handleChildTurnEnded(run, payload)
		return
	}
	completion := kimiTurnCompletion(payload.Reason)
	a.flushRun(run, a.sink, completion)
	a.closeOpenTools(run, a.sink, completion)
	a.ResetCumulativeOutput()
	a.sink.ReportProgress(agent.ResetModelProgress())

	a.Mu.Lock()
	a.lastTurnError = payload.Error.key()
	usage := maps.Clone(a.contextUsage)
	a.Mu.Unlock()
	// PersistTurnEnd BEFORE the flag clears: the turn end hands this turn's tool
	// count to the activity latch, and the clear is the edge that spends it.
	content := a.MessageWithToolUses(event.Raw)
	content = kimiWithContextUsage(content, usage)
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Error("kimi persist turn end", "agent_id", a.AgentID(), "error", err)
	}
	a.sink.ResetSpans()

	a.Mu.Lock()
	a.turnActive = false
	a.turnSteerable = false
	run.turnActive = false
	a.Mu.Unlock()
	a.PublishTurnActive()

	retry := payload.Reason == contracts.KimiTurnEndFailed && payload.Error != nil && payload.Error.Retryable
	providerkit.ScheduleOrCancelAPIErrorAutoContinue(a.sink, retry, event.Raw)
}

// kimiWithContextUsage adds the last context readout to a turn end's metadata,
// so a reconnecting client reads it off the persisted row.
func kimiWithContextUsage(content agent.MessageContent, usage map[string]any) agent.MessageContent {
	if len(usage) == 0 {
		return content
	}
	var fields map[string]json.RawMessage
	if len(content.Metadata) > 0 && json.Unmarshal(content.Metadata, &fields) != nil {
		return content
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		slog.Warn("kimi encode context usage metadata", "error", err)
		return content
	}
	fields[contracts.SessionInfoKeyContextUsage] = encoded
	metadata, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("kimi encode turn end metadata", "error", err)
		return content
	}
	content.Metadata = metadata
	return content
}

// --- text and thinking ---

// kimiTextDelta is the assistant.delta and thinking.delta payload.
type kimiTextDelta struct {
	TurnID int64  `json:"turnId"`
	Delta  string `json:"delta"`
}

// kimiTextScope and kimiThinkingScope key a run's text and thinking segments.
// Each run keeps at most one of each, flushed at every boundary.
func kimiTextScope(agentID string) string     { return "kimi:text:" + agentID }
func kimiThinkingScope(agentID string) string { return "kimi:thinking:" + agentID }

func (a *Agent) handleTextDelta(event kimiEvent, thinking bool) {
	var payload kimiTextDelta
	if !event.decode(&payload) || payload.Delta == "" {
		return
	}
	run := a.run(event.AgentID)
	sink := a.runSink(run)
	scope, kind := kimiTextScope(run.agentID), agent.AssembledMessageKindText
	if thinking {
		scope, kind = kimiThinkingScope(run.agentID), agent.AssembledMessageKindReasoning
	} else if sink != nil {
		// The answer follows the thinking, so the thinking is complete.
		a.flushScope(run, sink, kimiThinkingScope(run.agentID), agent.MessageCompletionComplete)
	}
	run.generation.Append(scope, kind, payload.Delta, providerkit.JoinVerbatim)
	if sink != nil {
		sink.ReportProgress(agent.ModelTextProgress(scope, payload.Delta))
	}
}

// flushRun persists a run's buffered thinking and text, in the order they
// streamed. A subagent no spawn linked yet keeps its buffer.
func (a *Agent) flushRun(run *kimiRun, sink agent.ProviderServices, completion agent.MessageCompletion) {
	if sink == nil {
		return
	}
	if a.IsDiscardingOutput() {
		run.generation.Reset()
		return
	}
	persist := func(raw []byte) error {
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	}
	if err := run.generation.PersistAll(completion, persist); err != nil {
		slog.Error("kimi persist generation", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "error", err)
	}
	sink.ReportProgress(agent.CompleteModelProgress(kimiTextScope(run.agentID)))
	sink.ReportProgress(agent.CompleteModelProgress(kimiThinkingScope(run.agentID)))
}

// flushScope persists one segment of a run.
func (a *Agent) flushScope(run *kimiRun, sink agent.ProviderServices, scope string, completion agent.MessageCompletion) {
	if a.IsDiscardingOutput() {
		run.generation.Discard(scope)
		return
	}
	ok, err := run.generation.PersistScope(scope, completion, func(raw []byte) error {
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	})
	if err != nil {
		slog.Error("kimi persist generation", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "error", err)
		return
	}
	if ok {
		sink.ReportProgress(agent.CompleteModelProgress(scope))
	}
}

// handleStepCompleted closes a model step: its text is complete, and its usage
// updates the readout.
func (a *Agent) handleStepCompleted(event kimiEvent) {
	run := a.run(event.AgentID)
	if sink := a.runSink(run); sink != nil {
		a.flushRun(run, sink, agent.MessageCompletionComplete)
	}
	if event.AgentID == kimiMainAgentID {
		a.recordStepUsage(event)
	}
}

// --- tool calls ---

// kimiToolCall is the part of tool.call.started, tool.call.delta, tool.progress
// and tool.result that the worker reads.
type kimiToolCall struct {
	TurnID     int64           `json:"turnId"`
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
	IsError    bool            `json:"isError"`
	Update     struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"update"`
}

// handleToolCallDelta flushes the text the model streamed before its first
// tool call, so the text precedes the call -- and precedes an approval the call
// needs, which arrives before the call starts.
func (a *Agent) handleToolCallDelta(event kimiEvent) {
	run := a.run(event.AgentID)
	if sink := a.runSink(run); sink != nil {
		a.flushRun(run, sink, agent.MessageCompletionComplete)
	}
}

func (a *Agent) handleToolCallStarted(event kimiEvent) {
	var payload kimiToolCall
	if !event.decode(&payload) || payload.ToolCallID == "" {
		return
	}
	run := a.run(event.AgentID)
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	a.flushRun(run, sink, agent.MessageCompletionComplete)

	a.Mu.Lock()
	sessionID := a.sessionID
	if run.tools[payload.ToolCallID] != nil {
		a.Mu.Unlock()
		slog.Debug("kimi tool call started twice", "agent_id", a.AgentID(), "tool_call_id", payload.ToolCallID)
		return
	}
	tool := &kimiTool{
		spanID:    kimiSpanID(sessionID, run.agentID, payload.TurnID, payload.ToolCallID),
		name:      payload.Name,
		order:     run.nextOrder,
		lastFrame: event.Raw,
	}
	run.nextOrder++
	run.tools[payload.ToolCallID] = tool
	spawns := kimiToolSpawns(payload.Name)
	if spawns {
		run.spawns[payload.ToolCallID] = kimiSpawnFromArgs(tool.spanID, payload.Name, payload.Args)
	}
	a.Mu.Unlock()

	if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: event.Raw}, tool.spanID, payload.Name, spawns); err != nil {
		slog.Error("kimi persist tool call", "agent_id", a.AgentID(), "tool_call_id", payload.ToolCallID, "error", err)
	}
	a.ClearCumulativeOutput(tool.spanID)
	sink.ReportProgress(agent.ResetOutputProgress(tool.spanID))
}

// kimiToolSpawns reports whether a tool call starts subagents. A spawn owns no
// span: its subagents' work lands in child transcripts, and a rail held open for
// the whole run would push every concurrent call one column right.
func kimiToolSpawns(name string) bool {
	return name == contracts.KimiToolAgent || name == contracts.KimiToolAgentSwarm
}

// kimiSpawnFromArgs reads what a spawn's input states about its subagents.
func kimiSpawnFromArgs(spanID, name string, args json.RawMessage) kimiSpawn {
	var input struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			slog.Debug("kimi spawn input does not decode", "tool", name, "error", err)
		}
	}
	spawn := kimiSpawn{spanID: spanID, name: name, label: strings.TrimSpace(input.Description)}
	if name == contracts.KimiToolAgent {
		spawn.prompt = strings.TrimSpace(input.Prompt)
	}
	return spawn
}

func (a *Agent) handleToolProgress(event kimiEvent) {
	var payload kimiToolCall
	if !event.decode(&payload) || payload.ToolCallID == "" {
		return
	}
	if payload.Update.Kind != kimiProgressStdout && payload.Update.Kind != kimiProgressStderr {
		return
	}
	run := a.run(event.AgentID)
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	a.Mu.Lock()
	tool := run.tools[payload.ToolCallID]
	if tool == nil {
		a.Mu.Unlock()
		return
	}
	text := payload.Update.Text
	tool.outputBytes = agent.SaturatingAdd(tool.outputBytes, int64(len(text)))
	var clipped bool
	tool.tail, clipped = agent.ClipTailBytes(tool.tail+text, kimiLiveOutputLimit)
	if clipped {
		tool.tailLost = true
	}
	spanID, total, tail, lost := tool.spanID, tool.outputBytes, tool.tail, tool.tailLost
	a.Mu.Unlock()
	sink.ReportProgress(agent.OutputExactTotalProgress(spanID, total))
	sink.ReportProgress(agent.OutputTailProgress(spanID, tail, lost))
}

func (a *Agent) handleToolResult(event kimiEvent) {
	var payload kimiToolCall
	if !event.decode(&payload) || payload.ToolCallID == "" {
		return
	}
	run := a.run(event.AgentID)
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	a.Mu.Lock()
	tool := run.tools[payload.ToolCallID]
	delete(run.tools, payload.ToolCallID)
	if tool == nil {
		// A result for a call this process never saw open: the session was
		// resumed while the call ran. The row still belongs to its span.
		tool = &kimiTool{spanID: kimiSpanID(a.sessionID, run.agentID, payload.TurnID, payload.ToolCallID)}
	}
	if run.agentID == kimiMainAgentID {
		a.TurnToolUses++
	}
	a.Mu.Unlock()

	a.ClearCumulativeOutput(tool.spanID)
	sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.Raw}, agent.SpanInfo{
		SpanID: tool.spanID, SpanType: tool.name, Closing: true,
	}); err != nil {
		slog.Error("kimi persist tool result", "agent_id", a.AgentID(), "tool_call_id", payload.ToolCallID, "error", err)
	}
	sink.CloseSpan(tool.spanID)
}

// closeOpenTools ends every call a turn left open. The server reports a result
// for a call the user aborted, so one left here means the turn ended in a way
// the server did not report per call. Each call's own opening payload becomes
// its last row, and the completion column states how the turn ended. The output
// the call printed before it stopped rides that row (kimiWithRetainedOutput).
func (a *Agent) closeOpenTools(run *kimiRun, sink agent.ProviderServices, completion agent.MessageCompletion) {
	if sink == nil {
		return
	}
	a.Mu.Lock()
	open := make([]*kimiTool, 0, len(run.tools))
	for id, tool := range run.tools {
		open = append(open, tool)
		delete(run.tools, id)
	}
	a.Mu.Unlock()
	sort.Slice(open, func(i, j int) bool { return open[i].order < open[j].order })
	for _, tool := range open {
		a.ClearCumulativeOutput(tool.spanID)
		sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		if !a.IsDiscardingOutput() {
			a.Mu.Lock()
			frame := kimiWithRetainedOutput(tool.lastFrame, tool.tail, tool.tailLost)
			a.Mu.Unlock()
			if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: frame, Completion: completion}, agent.SpanInfo{
				SpanID: tool.spanID, SpanType: tool.name, Closing: true,
			}); err != nil {
				slog.Error("kimi persist incomplete tool call", "agent_id", a.AgentID(), "span_id", tool.spanID, "error", err)
			}
		}
		sink.CloseSpan(tool.spanID)
	}
}

// kimiWithRetainedOutput adds the output a call printed before its turn ended
// to the call's opening payload, which becomes the call's last row.
//
// The server sends no result for such a call, and the output streamed only as
// volatile progress, which no durable event repeats. `output` and `truncated` are
// the fields a `tool.result` states them in, so the browser reads the retained
// row with the reader it reads a result with. A call that printed nothing keeps
// its payload unchanged.
func kimiWithRetainedOutput(frame []byte, output string, truncated bool) []byte {
	if output == "" {
		return frame
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(frame, &fields); err != nil || fields == nil {
		return frame
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return frame
	}
	fields["output"] = encoded
	if truncated {
		fields["truncated"] = json.RawMessage("true")
	}
	retained, err := json.Marshal(fields)
	if err != nil {
		return frame
	}
	return retained
}

// --- notices ---

// persistEventNotification records a notice event -- a compaction, a retry, a
// warning, a task notification -- in the transcript of the agent it belongs to.
func (a *Agent) persistEventNotification(event kimiEvent) {
	a.persistNotification(a.runSink(a.run(event.AgentID)), event)
}

func (a *Agent) persistNotification(sink agent.ProviderServices, event kimiEvent) {
	if sink == nil || a.IsDiscardingOutput() {
		return
	}
	if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.Raw); err != nil {
		slog.Error("kimi persist notification", "agent_id", a.AgentID(), "type", event.Type, "error", err)
	}
}

// handleError records an error event, unless it repeats the failure the last
// main turn ended with: the server states a failed turn twice, as the turn's
// own error and as an `error` event right after it, and the turn-end divider
// already shows the first.
func (a *Agent) handleError(event kimiEvent) {
	var payload kimiEventErr
	if !event.decode(&payload) {
		return
	}
	if event.AgentID == kimiMainAgentID {
		a.Mu.Lock()
		repeat := a.lastTurnError != "" && a.lastTurnError == payload.key()
		if repeat {
			a.lastTurnError = ""
		}
		a.Mu.Unlock()
		if repeat {
			return
		}
	}
	a.persistEventNotification(event)
}
