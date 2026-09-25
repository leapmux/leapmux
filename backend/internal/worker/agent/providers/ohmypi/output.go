package ohmypi

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
)

// turnState is the turn that the agent owes the user a reply for. All fields are
// guarded by Process.Mu.
//
// omp's turn and LeapMux's turn are not one thing, and the difference is why this
// is more than one flag:
//
//   - A run starts at agent_start and ends at agent_end. A retry and a to-do
//     reminder START A SECOND RUN with no agent_end between, and the turn
//     continues through both.
//   - omp starts a run by itself when a background job finishes. That run is a
//     turn too, although no prompt caused it.
//   - omp acknowledges a prompt BEFORE the run starts. The worker arms the turn
//     at the prompt, so the input queue does not dispatch the next message into
//     that window, and it releases the arm when the prompt starts no run.
type turnState struct {
	// active is the flag PublishTurnActive publishes.
	active bool
	// armedBy is the id of the prompt (or of the compaction) that armed the turn
	// before its run started. Empty when no arm is pending.
	armedBy string
	// runStarted is true once an agent_start arrived for the current turn. An arm
	// is released only while it is false: once a run started, agent_end owns the
	// turn.
	runStarted bool
	// startedAt marks when the turn began, so agent_end can report its wall time.
	// omp's agent_end carries no duration of its own.
	startedAt time.Time
	// interruptRequested records that the user stopped the running turn, so the
	// agent_end that ends it reads as an interruption. omp reports an aborted run
	// with `stopReason: "aborted"`, but a run whose tool was still running can end
	// on an error instead, and only the worker knows it asked for the stop.
	interruptRequested bool
	// pendingSteers counts the steers that omp acknowledged and has not yet taken
	// into a run. omp takes a steer as a user message marked `steering`. A steer
	// that arrives at the tail of a run is still pending at that run's agent_end,
	// which omp then marks `isTerminal: false`: the steer continues the work at
	// once, as part of the same LeapMux turn.
	pendingSteers int
	// steers holds each steer that the worker wrote during the turn, by its
	// command id. The value is true once the steer counts in pendingSteers.
	//
	// A steer that never enters a run must not count, or a later end with
	// `isTerminal: false` keeps the turn open for work that can start much later.
	// omp answers a slash command itself, and it can resolve a steer local-only or
	// fail it after the acknowledgement. The read loop can report either before
	// the worker reads the acknowledgement, so the steer is recorded before its
	// write: a report that comes first then removes it, and the acknowledgement
	// finds nothing to count.
	steers map[string]bool
}

// clear ends the turn and drops every mark it held.
func (t *turnState) clear() {
	*t = turnState{}
}

// armTurn marks the turn active before the prompt that starts it is written.
// A turn that is already active keeps its own state: the arm is then a no-op.
func (a *Agent) armTurn(id string) {
	startedAt := a.Clock().Now()
	a.Mu.Lock()
	if a.turn.active {
		a.Mu.Unlock()
		return
	}
	a.turn = turnState{active: true, armedBy: id, startedAt: startedAt}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// disarmTurn releases an arm whose prompt started no run.
//
// It acts only while the SAME arm is pending and no run started. A run that did
// start owns the turn until its agent_end, and an arm that a later prompt replaced
// is not this caller's to release.
//
// It publishes either way. A publish that repeats the current state is safe, and
// the input queue needs one to settle a dispatch that started no turn.
func (a *Agent) disarmTurn(id string) {
	a.Mu.Lock()
	if a.turn.armedBy == id && !a.turn.runStarted {
		a.turn.clear()
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// conversation is the state that omp's session events drive for one transcript:
// the session's own, or one subagent's. Every field is guarded by Process.Mu,
// except generation, which guards itself.
type conversation struct {
	// sink is the transcript the conversation writes to: the agent's own sink, or
	// the child sink of a subagent.
	sink agent.ProviderServices
	// childID is the child agent of a subagent's conversation, and empty for the
	// session's own.
	childID string
	// tools holds the tool calls that started and did not end, by call id.
	tools map[string]*toolState
	// nextOrder numbers the tool calls in start order, so an incomplete call is
	// persisted in the order it started.
	nextOrder uint64
	// generation joins the streamed text of the session's own assistant messages.
	// A subagent's conversation does not stream: its message_end supplies its rows.
	generation providerkit.GenerationBuffer
	// runStartedAt marks when a subagent's run began, for the duration of its
	// turn-end row. The session's own turn keeps its mark in turnState.
	runStartedAt time.Time
	// toolUses counts the tool calls of a subagent's run. The session's own count
	// is Process.TurnToolUses.
	toolUses int
}

func newConversation(sink agent.ProviderServices, childID string) *conversation {
	return &conversation{sink: sink, childID: childID, tools: make(map[string]*toolState)}
}

// isRoot reports whether the conversation is the session's own.
func (c *conversation) isRoot() bool { return c.childID == "" }

// toolState is one tool call that started and did not end.
type toolState struct {
	ToolName string
	Args     json.RawMessage
	// StartFrame is the tool_execution_start frame omp sent, byte for byte. A turn
	// that ends while the call runs stores THAT frame, so the transcript never
	// holds a frame omp did not send.
	StartFrame []byte
	// PartialResult is the last cumulative result a tool_execution_update stated.
	PartialResult json.RawMessage
	Order         uint64
}

// messageUpdateEnvelope takes the fields that model progress needs from a
// message_update frame.
//
// The frame repeats the WHOLE cumulative assistant message twice, under
// `assistantMessageEvent.partial` and under `message`, so it grows with the
// message. This shape declares neither field, and the decoder skips both.
type messageUpdateEnvelope struct {
	AssistantMessageEvent struct {
		Type         string `json:"type"`
		Delta        string `json:"delta"`
		ContentIndex int    `json:"contentIndex"`
	} `json:"assistantMessageEvent"`
}

// messageEndEnvelope takes the routing fields of a message_end frame.
type messageEndEnvelope struct {
	Message struct {
		Role string `json:"role"`
		// Steering marks the user message of a steer that omp took into a run.
		Steering    bool            `json:"steering"`
		CustomType  string          `json:"customType"`
		Display     *bool           `json:"display"`
		Content     json.RawMessage `json:"content"`
		Attribution string          `json:"attribution"`
		Details     json.RawMessage `json:"details"`
	} `json:"message"`
}

// toolExecutionEnvelope takes the fields of a tool_execution_* frame.
type toolExecutionEnvelope struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	PartialResult json.RawMessage `json:"partialResult"`
	Result        json.RawMessage `json:"result"`
	IsError       bool            `json:"isError"`
}

// toolResultEnvelope takes the text blocks and the details of a tool result.
type toolResultEnvelope struct {
	Content []contentBlock  `json:"content"`
	Details json.RawMessage `json:"details"`
}

// contentBlock is one block of a message or a tool result. A block whose Type is
// contentBlockText carries Text, and a block whose Type is contentBlockThinking
// carries Thinking.
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// agentEndEnvelope takes the outcome fields of an agent_end frame.
//
// `isTerminal` is false when omp has already scheduled more work: a steer or a
// follow-up that arrived at the tail of the run, an IRC wake, or a maintenance
// step that continues the run (session/agent-session.ts, #flushPendingAgentEnd).
// A steer of LeapMux's own continues the running turn, so the turn stays open
// across that end (see turnState.pendingSteers). Any other scheduled work can
// start much later -- a background job's result, for example -- so the worker
// ends the turn, and the run that the work starts is a turn of its own.
type agentEndEnvelope struct {
	Messages []struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"messages"`
	IsTerminal *bool `json:"isTerminal"`
}

// completion reads how the run ended from its last assistant message: an error
// stop reads as an error, and every other ending that left output unfinished
// reads as an interruption.
func (env agentEndEnvelope) completion() agent.MessageCompletion {
	for i := len(env.Messages) - 1; i >= 0; i-- {
		if env.Messages[i].Role != contracts.OhMyPiRoleAssistant {
			continue
		}
		if env.Messages[i].StopReason == contracts.OhMyPiStopReasonError {
			return agent.MessageCompletionError
		}
		break
	}
	return agent.MessageCompletionInterrupted
}

// handleAgentStart opens (or continues) the session's turn.
func (a *Agent) handleAgentStart() {
	// Read the clock before the lock, so an injected clock never runs under Mu.
	now := a.Clock().Now()
	a.Mu.Lock()
	firstRun := !a.turn.runStarted
	a.turn.active = true
	a.turn.runStarted = true
	if a.turn.startedAt.IsZero() {
		a.turn.startedAt = now
	}
	if firstRun && a.turn.armedBy == "" {
		// A run that no prompt of this worker armed -- omp started it for a
		// background job's result. A stop note left from before is stale.
		a.turn.interruptRequested = false
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
	// A fresh run begins with empty model counters. A retry continues the same
	// turn, and its output starts over too.
	a.sink.ReportProgress(agent.ResetModelProgress())
	if firstRun {
		a.refreshSessionStatsAsync()
	}
}

// beginSteer records a steer before its write, so a report about it that
// arrives before its acknowledgement can remove it. See turnState.steers.
func (a *Agent) beginSteer(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if !a.turn.active {
		return
	}
	if a.turn.steers == nil {
		a.turn.steers = make(map[string]bool)
	}
	a.turn.steers[id] = false
}

// acceptSteer counts a steer that omp acknowledged for a run. A steer that omp
// dropped meanwhile counts nothing. Neither does a steer of a turn that ended
// meanwhile, because the end forgot every steer of that turn.
func (a *Agent) acceptSteer(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	counted, recorded := a.turn.steers[id]
	if !recorded || counted || !a.turn.active {
		return
	}
	a.turn.steers[id] = true
	a.turn.pendingSteers++
}

// dropSteer forgets a steer that will not enter a run: omp refused it,
// answered it itself, or never acknowledged it. It is a no-op for an id that is
// not a steer of the current turn, such as a prompt.
func (a *Agent) dropSteer(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	counted, recorded := a.turn.steers[id]
	if !recorded {
		return
	}
	delete(a.turn.steers, id)
	if counted {
		a.turn.pendingSteers--
	}
}

// takePendingSteer records that omp took one steer into a run.
//
// The count can drop below zero: omp can report the steer's user message before
// the worker has processed the acknowledgement of the same steer, and that
// acknowledgement then brings the count back to zero. An IRC message that omp
// injects as a steer lowers the count as well, and the turn then ends at an end
// with `isTerminal: false`, as it does for any other scheduled work.
func (a *Agent) takePendingSteer() {
	a.Mu.Lock()
	a.turn.pendingSteers--
	a.Mu.Unlock()
}

// handleAgentEnd ends the session's turn and persists its divider. An end with
// `isTerminal: false` that a pending steer continues only ends the run: the turn
// stays open for the run that the steer starts. See agentEndEnvelope.
func (a *Agent) handleAgentEnd(raw []byte) {
	var env agentEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("omp agent_end decode failed", "agent_id", a.AgentID(), "error", err)
	}
	endedAt := a.Clock().Now()
	a.Mu.Lock()
	if env.IsTerminal != nil && !*env.IsTerminal && a.turn.pendingSteers > 0 {
		root := a.rootConversationLocked()
		a.Mu.Unlock()
		// The run's own text is complete: its last message ended it.
		a.flushGeneration(root, agent.MessageCompletionComplete)
		return
	}
	turn := a.turn
	a.turn.clear()
	toolUses := a.TurnToolUses
	a.TurnToolUses = 0
	root := a.rootConversationLocked()
	a.asks.clearLocked()
	a.Mu.Unlock()
	// Publish the inactive state after the sink receives the turn end, so the
	// completion sound knows the tool count. See TranscriptServices.PersistTurnEnd.
	defer a.PublishTurnActive()

	completion := env.completion()
	if turn.interruptRequested {
		completion = agent.MessageCompletionInterrupted
	}
	a.flushGeneration(root, completion)
	a.persistIncompleteTools(root, completion)
	a.ResetCumulativeOutput()

	content := agentEndContent(raw, a.currentUsageSnapshot(), turnDurationMs(turn.startedAt, endedAt))
	if turn.interruptRequested {
		content.Completion = agent.MessageCompletionInterrupted
	}
	if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
		slog.Error("omp persist agent_end", "agent_id", a.AgentID(), "error", err)
	}
	// omp retries a failed request itself (auto_retry_*), so LeapMux schedules no
	// continuation of its own, and a pending one is cancelled.
	providerkit.ScheduleOrCancelAPIErrorAutoContinue(a.sink, false, raw)
	a.sink.ResetSpans()
	a.refreshSessionStatsAsync()
}

// turnDurationMs measures one turn in milliseconds, or nil for a turn whose start
// this worker never saw and for a clock that moved backwards. The browser draws
// no time for an absent field, and "(0ms)" for a real zero.
func turnDurationMs(startedAt, endedAt time.Time) *int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return nil
	}
	ms := endedAt.Sub(startedAt).Milliseconds()
	return &ms
}

// handleMessageUpdate adds one streamed delta of the session's assistant message
// to the generation buffer and to the live progress.
func (a *Agent) handleMessageUpdate(c *conversation, raw []byte) {
	if !c.isRoot() {
		// A subagent's streamed text reaches its transcript with its message_end.
		return
	}
	var env messageUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("omp message_update decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	event := env.AssistantMessageEvent
	kind := agent.AssembledMessageKindText
	switch event.Type {
	case contracts.OhMyPiAssistantEventTextDelta:
	case contracts.OhMyPiAssistantEventThinkingDelta:
		kind = agent.AssembledMessageKindReasoning
	default:
		// The start, end, tool-call and done events carry no text. The
		// message_end and tool_execution_* frames state what they bracket.
		return
	}
	if event.Delta == "" {
		return
	}
	a.sink.ReportProgress(agent.ModelTextProgress("omp:model", event.Delta))
	c.generation.Append(fmt.Sprintf("omp:content:%d", event.ContentIndex), kind, event.Delta, providerkit.JoinVerbatim)
}

// flushGeneration persists the streamed text that no message_end completed.
func (a *Agent) flushGeneration(c *conversation, completion agent.MessageCompletion) {
	if a.IsDiscardingOutput() {
		c.generation.Reset()
		return
	}
	if err := c.generation.PersistAll(completion, func(raw []byte) error {
		return c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	}); err != nil {
		slog.Error("omp persist partial generation", "agent_id", a.AgentID(), "error", err)
	}
}

// handleMessageEnd persists one finished message.
//
// omp emits message_end for EVERY message the conversation gains, and each role
// reaches the transcript at most once:
//
//   - assistant: the reply itself. The session's own carries usage metadata. Its
//     thinking becomes a row of its own before it (see persistThinking).
//   - user: LeapMux already persisted the message it sent. A subagent's first
//     user message is the prompt its parent gave it, and it opens the child
//     transcript.
//   - toolResult: the tool_execution_end frame states the same result.
//   - custom: a message omp injects, such as a background job's result. It
//     reaches the transcript when omp displays it.
//   - bashExecution: the output of a `bash` COMMAND, which LeapMux never sends.
func (a *Agent) handleMessageEnd(c *conversation, raw []byte) {
	var env messageEndEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("omp message_end decode failed", "agent_id", a.AgentID(), "error", err)
		a.persistRaw(c, raw)
		return
	}
	message := env.Message
	switch message.Role {
	case contracts.OhMyPiRoleAssistant:
		content := agent.MessageContent{Original: raw}
		if c.isRoot() {
			content = a.assistantMessageContent(raw)
			a.rememberAskCalls(message.Content)
		}
		a.persistThinking(c, message.Content)
		if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
			slog.Error("omp persist message_end", "agent_id", a.AgentID(), "error", err)
		}
		c.generation.Reset()
	case contracts.OhMyPiRoleUser:
		if !c.isRoot() {
			a.persistChildPrompt(c, message.Content)
			return
		}
		if message.Steering {
			a.takePendingSteer()
		}
	case contracts.OhMyPiRoleCustom:
		if !c.isRoot() {
			return
		}
		if message.CustomType == contracts.OhMyPiCustomTypeAsyncResult {
			a.closeDeliveredShells(message.Details)
		}
		if message.Display != nil && !*message.Display {
			return
		}
		if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
			slog.Error("omp persist custom message", "agent_id", a.AgentID(), "error", err)
		}
	case contracts.OhMyPiRoleToolResult, contracts.OhMyPiRoleBashExecution:
		// The dropped roles; the list above states why.
	default:
		// A role this build does not know. The row reaches the transcript as an
		// inspectable card rather than disappearing.
		a.persistRaw(c, raw)
	}
}

// persistRaw persists a frame this build cannot read, so the reader can still
// inspect it.
func (a *Agent) persistRaw(c *conversation, raw []byte) {
	if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("omp persist an unknown frame", "agent_id", a.AgentID(), "error", err)
	}
}

// persistThinking persists the visible thinking of one assistant message as a
// reasoning row of its own, before the message's own row.
//
// omp states a reasoning model's reply as ONE message whose content holds thinking
// blocks beside the text blocks and the tool calls. A transcript row draws one kind
// of text, so a row that drew the message would lose one of the two. The thinking
// takes LeapMux's assembled-message envelope, which the transcript draws as
// thinking for every provider, and which the worker already writes for thinking
// that no message_end completed (flushGeneration). The message's own row keeps
// omp's frame and the usage, and the browser draws its text alone.
func (a *Agent) persistThinking(c *conversation, content json.RawMessage) {
	thinking := messageThinking(content)
	if thinking == "" {
		return
	}
	raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindReasoning, thinking, agent.MessageCompletionComplete)
	if err != nil {
		slog.Error("omp encode thinking", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("omp persist thinking", "agent_id", a.AgentID(), "error", err)
	}
}

// messageThinking joins the visible thinking blocks of an assistant message's
// content into paragraphs, or returns "" when it holds none.
//
// A thinking block whose text is blank carries only a signature for the provider,
// and a `redactedThinking` block carries only opaque data. Neither shows anything.
func messageThinking(content json.RawMessage) string {
	var blocks []contentBlock
	if len(content) == 0 || json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == contentBlockThinking && strings.TrimSpace(block.Thinking) != "" {
			parts = append(parts, block.Thinking)
		}
	}
	return strings.Join(parts, "\n\n")
}

// messageText joins the text of a message's content, which omp states either as a
// string or as a list of typed blocks.
func messageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []contentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == contentBlockText && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// handleToolStart opens one tool call's span and persists its start frame.
func (a *Agent) handleToolStart(c *conversation, raw []byte) {
	var env toolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("omp tool_execution_start decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	c.tools[env.ToolCallID] = &toolState{
		ToolName:   env.ToolName,
		Args:       append(json.RawMessage(nil), env.Args...),
		StartFrame: append([]byte(nil), raw...),
		Order:      c.nextOrder,
	}
	c.nextOrder++
	a.Mu.Unlock()
	// A `task` call spawns subagents. It owns no span, so it reserves no color:
	// the subagents' work lands in their own transcripts, and a rail held open for
	// the whole run would only push every concurrent tool one column right.
	spawns := env.ToolName == contracts.OhMyPiToolTask
	if spawns {
		a.rememberSpawns(env.ToolCallID, env.Args)
	}
	if err := providerkit.OpenToolSpan(c.sink, agent.MessageContent{Original: raw}, env.ToolCallID, env.ToolName, spawns); err != nil {
		slog.Error("omp persist tool_execution_start", "agent_id", a.AgentID(), "error", err)
	}
}

// liveOutputLimit is the longest live output tail the worker broadcasts, in bytes.
// omp re-sends the whole partial result on every update, and the cap keeps the
// join off the accumulated output.
const liveOutputLimit = 8192

// handleToolUpdate records a call's cumulative partial result, and reports the
// session's live output.
//
// An update for a call with no open state is dropped. omp sends updates for a
// `task` call AFTER its tool_execution_end, while the subagents it started run
// on, and a state created from those would outlive the call and persist it again
// as an incomplete call at the end of the turn.
func (a *Agent) handleToolUpdate(c *conversation, raw []byte) {
	var env toolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	tool := c.tools[env.ToolCallID]
	if tool != nil && len(env.PartialResult) > 0 {
		tool.PartialResult = env.PartialResult
	}
	a.Mu.Unlock()
	if tool == nil || !c.isRoot() || len(env.PartialResult) == 0 {
		return
	}
	var partial toolResultEnvelope
	if json.Unmarshal(env.PartialResult, &partial) != nil {
		return
	}
	a.reportToolOutput(env.ToolCallID, partial)
}

// reportToolOutput reports one partial result's output to the live counter and
// the live tail.
func (a *Agent) reportToolOutput(toolCallID string, partial toolResultEnvelope) {
	textBytes := 0
	for _, block := range partial.Content {
		if block.Type == contentBlockText {
			textBytes += len(block.Text)
		}
	}
	if textBytes == 0 {
		return
	}
	var details struct {
		Meta *struct {
			Truncation *struct {
				TotalBytes int64 `json:"totalBytes"`
			} `json:"truncation"`
		} `json:"meta"`
	}
	truncated := false
	if json.Unmarshal(partial.Details, &details) == nil && details.Meta != nil && details.Meta.Truncation != nil {
		truncated = true
		if details.Meta.Truncation.TotalBytes > 0 {
			a.sink.ReportProgress(agent.OutputExactTotalProgress(toolCallID, details.Meta.Truncation.TotalBytes))
		}
	}
	if !truncated {
		// An output that kept its head is append-only, so its length is the exact
		// total.
		whole, _ := joinOutputTail(partial.Content, textBytes, 0)
		observed := a.ObserveCumulativeOutput(toolCallID, whole, false)
		a.sink.ReportProgress(agent.OutputTotalProgress(toolCallID, observed.Total, observed.Minimum))
	}
	tail, clipped := joinOutputTail(partial.Content, textBytes, liveOutputLimit)
	a.sink.ReportProgress(agent.OutputTailProgress(toolCallID, tail, truncated || clipped))
}

// joinOutputTail joins the text blocks of one partial result, keeping at most the
// LAST limit bytes. A limit of zero or less keeps every byte. total is the summed
// length of those blocks. It reports whether the limit dropped earlier bytes.
func joinOutputTail(blocks []contentBlock, total, limit int) (string, bool) {
	skip := 0
	if limit > 0 && total > limit {
		skip = total - limit
	}
	var joined strings.Builder
	joined.Grow(total - skip)
	for _, block := range blocks {
		if block.Type != contentBlockText {
			continue
		}
		text := block.Text
		if skip > 0 {
			if len(text) <= skip {
				skip -= len(text)
				continue
			}
			// Move the cut to the next rune boundary, so no replacement character
			// reaches the browser.
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

// handleToolEnd persists one call's end frame and closes its span.
func (a *Agent) handleToolEnd(c *conversation, raw []byte) {
	var env toolExecutionEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ToolCallID == "" {
		slog.Warn("omp tool_execution_end decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	// omp's end frame carries no arguments; the call's start frame had them.
	args := env.Args
	if started := c.tools[env.ToolCallID]; len(args) == 0 && started != nil {
		args = started.Args
	}
	delete(c.tools, env.ToolCallID)
	if c.isRoot() {
		a.TurnToolUses++
	} else {
		c.toolUses++
	}
	a.Mu.Unlock()
	if c.isRoot() {
		a.ClearCumulativeOutput(env.ToolCallID)
		a.sink.ReportProgress(agent.CompleteOutputProgress(env.ToolCallID))
	}

	if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{
		SpanID:   env.ToolCallID,
		SpanType: env.ToolName,
		Closing:  true,
	}); err != nil {
		slog.Error("omp persist tool_execution_end", "agent_id", a.AgentID(), "error", err)
	}
	c.sink.CloseSpan(env.ToolCallID)

	switch env.ToolName {
	case contracts.OhMyPiToolAsk:
		if c.isRoot() {
			a.finishAsk(env.ToolCallID)
		}
	case contracts.OhMyPiToolBash:
		if c.isRoot() && !env.IsError {
			a.openBackgroundShell(env.ToolCallID, args, env.Result)
		}
	case contracts.OhMyPiToolYield:
		if !c.isRoot() {
			a.rememberYield(c, env.Result)
		}
	case contracts.OhMyPiToolTask:
		a.forgetSpawns(env.ToolCallID, env.IsError)
	}
}

// persistIncompleteTools persists every call of a conversation that started and
// did not end, and closes its span.
//
// The row is omp's own start frame. The partial result omp did report rides
// beside it as recovered provider data, and the completion column states that the
// call did not finish.
func (a *Agent) persistIncompleteTools(c *conversation, completion agent.MessageCompletion) {
	a.Mu.Lock()
	ids := make([]string, 0, len(c.tools))
	tools := make(map[string]toolState, len(c.tools))
	for id, tool := range c.tools {
		if tool == nil {
			continue
		}
		ids = append(ids, id)
		tools[id] = *tool
	}
	clear(c.tools)
	c.nextOrder = 0
	a.Mu.Unlock()
	sort.Slice(ids, func(i, j int) bool {
		left, right := tools[ids[i]], tools[ids[j]]
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		tool := tools[id]
		if c.isRoot() {
			a.ClearCumulativeOutput(id)
			a.sink.ReportProgress(agent.CompleteOutputProgress(id))
		}
		if a.IsDiscardingOutput() {
			continue
		}
		supplement, err := incompleteToolSupplement(id, tool)
		if err != nil {
			slog.Warn("omp encode incomplete tool", "agent_id", a.AgentID(), "tool_call_id", id, "error", err)
			continue
		}
		if err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original: tool.StartFrame, Supplemental: supplement, Completion: completion,
		}, agent.SpanInfo{SpanID: id, SpanType: tool.ToolName, Closing: true}); err != nil {
			slog.Error("omp persist incomplete tool", "agent_id", a.AgentID(), "tool_call_id", id, "error", err)
		}
		c.sink.CloseSpan(id)
	}
}

// incompleteToolSupplement encodes a call's partial result, or nothing when the
// call reported none.
func incompleteToolSupplement(toolCallID string, tool toolState) ([]byte, error) {
	if len(tool.PartialResult) == 0 {
		return nil, nil
	}
	return json.Marshal(contracts.OhMyPiIncompleteToolSupplement{
		ToolCallID:    toolCallID,
		ToolName:      tool.ToolName,
		PartialResult: tool.PartialResult,
	})
}
