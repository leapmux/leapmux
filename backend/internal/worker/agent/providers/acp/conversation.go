package acp

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// conversation renders the session updates of ONE conversation into ONE
// transcript. The main conversation is the session that the agent serves, and
// it writes to the transcript of the agent. A child conversation is a subagent
// whose updates reach LeapMux on the same process, and it writes to the
// transcript of that child.
//
// One type renders both, so a subagent transcript assembles its text, pairs its
// tool calls and closes its spans with the same code as the transcript of its
// parent. A second, reduced renderer for children would drift from the main one
// with each fix that reached only one of them.
//
// A conversation holds no state of its own beyond its turn output. The main
// conversation is built on demand from the fields of Base, so an agent that a
// test builds as a bare Base still renders.
type conversation struct {
	b *Base
	// childSink is the services of the child transcript, and nil for the main
	// conversation. The main conversation reads b.sink on each call rather than
	// a copy, so it always writes through the sink that Base holds at that
	// moment: the handshake replaces that field with a decorator (see Sink in
	// extension.go).
	childSink agent.ProviderServices
	// childAgentID is the id of the child agent, and "" for the main conversation.
	childAgentID string
	out          *acpTurnOutput
}

// main returns the conversation of the session that the agent serves.
func (b *Base) main() *conversation {
	return &conversation{b: b, out: &b.acpTurnOutput}
}

// sink returns the services of the transcript that this conversation writes.
func (c *conversation) sink() agent.ProviderServices {
	if c.childSink != nil {
		return c.childSink
	}
	return c.b.sink
}

// handleUpdate renders one session update of this conversation. It returns
// false for an update type that only the main session carries, so the caller
// can apply it to the session state.
func (c *conversation) handleUpdate(header acpUpdateHeader, update json.RawMessage) bool {
	updateType, status, content := header.SessionUpdate, header.Status, header.Content
	// Flush model segments at each chronology boundary. Status and tool-progress
	// updates do not split a segment.
	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk:
		c.flushThoughtBuffer()
		c.flushOnNewMessage(agent.AssembledMessageKindText, header.Meta)
	case contracts.ACPUpdateAgentThoughtChunk:
		c.flushAssistantBuffer()
		c.flushOnNewMessage(agent.AssembledMessageKindReasoning, header.Meta)
	case contracts.ACPUpdateToolCall, acpUpdatePlan:
		c.flushThoughtBuffer()
		c.flushAssistantBuffer()
	case contracts.ACPUpdateToolCallUpdate:
		if StatusIsFinal(status) {
			c.flushThoughtBuffer()
			c.flushAssistantBuffer()
		}
	case contracts.ACPUpdateUsageUpdate,
		contracts.ACPUpdateCurrentMode,
		contracts.ACPUpdateUserMessageChunk,
		contracts.ACPUpdateAvailableCommandsUpdate,
		contracts.ACPUpdateConfigOptionUpdate,
		contracts.ACPUpdateSessionInfoUpdate:
		// no flush. session_info_update carries the runtime's own title and modified
		// time, and one arrives for every turn, so a flush here split each assembled
		// message in two. The switch below reads nothing from it.
	default:
		c.flushThoughtBuffer()
		c.flushAssistantBuffer()
	}

	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk:
		c.bufferChunk(content)
	case contracts.ACPUpdateAgentThoughtChunk:
		c.handleThoughtChunk(content)
	case contracts.ACPUpdateToolCall:
		c.handleToolCall(update)
	case contracts.ACPUpdateToolCallUpdate:
		c.handleToolCallUpdate(update)
	case acpUpdatePlan:
		c.handlePlan(update)
	case contracts.ACPUpdateUsageUpdate,
		contracts.ACPUpdateConfigOptionUpdate,
		contracts.ACPUpdateCurrentMode,
		contracts.ACPUpdateUserMessageChunk,
		contracts.ACPUpdateAvailableCommandsUpdate,
		contracts.ACPUpdateSessionInfoUpdate:
		return false
	default:
		if err := c.sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}, agent.SpanInfo{}); err != nil {
			slog.Error("persist unknown acp sessionUpdate", "agent_id", c.b.AgentID(), "child_agent_id", c.childAgentID, "type", updateType, "error", err)
		}
	}
	return true
}

// flushOnNewMessage stores the buffered text of one kind as a message of its
// own when the next chunk of that kind belongs to another message. Only a
// provider that states the message of a chunk (Hooks.ChunkMessageID) splits
// this way. For every other provider, a run of chunks stays one message until
// another update ends it, because the protocol states no message boundary.
func (c *conversation) flushOnNewMessage(kind agent.AssembledMessageKind, metadata map[string]json.RawMessage) {
	if c.b.hooks.ChunkMessageID == nil {
		return
	}
	if c.out.switchMessage(kind, c.b.hooks.ChunkMessageID(metadata)) {
		c.flushTextBuffer(kind)
	}
}

// bufferChunk extracts text from a pre-parsed content envelope and counts it.
func (c *conversation) bufferChunk(content json.RawMessage) {
	text := c.b.extractACPChunkText(content, contracts.ACPUpdateAgentMessageChunk)
	if text == "" {
		return
	}
	c.out.appendAssistant(text)
	c.sink().ReportProgress(agent.ModelTextProgress("acp:"+contracts.ACPUpdateAgentMessageChunk, text))
}

// handleThoughtChunk buffers an agent_thought_chunk notification's text for
// later flushing. ACP providers vary in chunk size. Persisting each
// notification would produce a separate Thinking row for each token. The
// buffer flushes when another event interrupts it or when the turn ends.
func (c *conversation) handleThoughtChunk(content json.RawMessage) {
	text := c.b.extractACPChunkText(content, contracts.ACPUpdateAgentThoughtChunk)
	if text == "" {
		return
	}
	// A chunk opens a segment when the thought buffer was empty before it.
	freshSegment := c.out.appendThought(text)
	// A new reasoning segment completes the preceding assistant counter scope.
	if freshSegment {
		c.sink().ReportProgress(agent.CompleteModelProgress("acp:" + contracts.ACPUpdateAgentMessageChunk))
	}
	c.sink().ReportProgress(agent.ModelTextProgress("acp:"+contracts.ACPUpdateAgentThoughtChunk, text))
}

// flushThoughtBuffer persists the buffered thought text (if any) as one assembled
// reasoning message and resets the buffer.
func (c *conversation) flushThoughtBuffer() {
	c.flushTextBuffer(agent.AssembledMessageKindReasoning)
}

// flushAssistantBuffer persists one completed assistant-text segment.
func (c *conversation) flushAssistantBuffer() {
	c.flushTextBuffer(agent.AssembledMessageKindText)
}

func (c *conversation) flushTextBuffer(kind agent.AssembledMessageKind) {
	text := c.out.takeText(kind)
	if text == "" {
		return
	}
	c.persistCompletedText(kind, text)
}

// persistCompletedText ends one text kind's live counter and stores the segment.
//
// The counter closes even when the text is empty, because the caller reaches this
// point only when the segment ended. A scope that stays open keeps counting its
// characters into the next segment.
func (c *conversation) persistCompletedText(kind agent.AssembledMessageKind, text string) {
	c.sink().ReportProgress(agent.CompleteModelProgress("acp:" + acpTextProgressScope(kind)))
	c.persistAssembledText(kind, text, agent.MessageCompletionComplete)
}

// persistAssembledText stores one assembled text segment.
//
// The Agent Client Protocol streams text as a run of chunks, and one transcript row
// holds the whole segment. That row is therefore LeapMux's ASSEMBLY, not a frame the
// agent sent, so it carries LeapMux's own assembled-message envelope. Writing a
// chunk-shaped object instead would put a message the agent never sent into the
// column that holds the agent's own bytes -- and the interrupted path already used
// this envelope, so the completed path had a second shape for the same content.
func (c *conversation) persistAssembledText(kind agent.AssembledMessageKind, text string, completion agent.MessageCompletion) {
	if text == "" {
		return
	}
	raw, err := agent.MarshalAssembledMessage(kind, text, completion)
	if err != nil {
		slog.Warn("marshal assembled acp text", "agent_id", c.b.AgentID(), "child_agent_id", c.childAgentID, "error", err)
		return
	}
	if err := c.sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("persist assembled acp text", "agent_id", c.b.AgentID(), "child_agent_id", c.childAgentID, "error", err)
	}
}

// finishTurn ends the turn of this conversation that did not reach a turn-end
// frame: the text that it assembled and each tool call that it left open are
// stored with completion.
func (c *conversation) finishTurn(turn acpTurnSnapshot, completion agent.MessageCompletion) {
	c.persistAssembledText(agent.AssembledMessageKindReasoning, turn.thoughtText, completion)
	c.persistAssembledText(agent.AssembledMessageKindText, turn.assistantText, completion)
	c.persistIncompleteTools(turn.incompleteTools, completion)
}

func (c *conversation) persistIncompleteTools(tools []acpIncompleteTool, completion agent.MessageCompletion) {
	for _, tool := range tools {
		if tool.encodeErr != nil {
			slog.Warn("marshal incomplete acp tool", "agent_id", c.b.AgentID(), "tool_call_id", tool.toolCallID, "error", tool.encodeErr)
		} else {
			content := c.b.acpMessageContent(tool.original, tool.content)
			content.Completion = completion
			if err := c.persistClosingTool(tool.toolCallID, content); err != nil {
				slog.Error("persist incomplete acp tool", "agent_id", c.b.AgentID(), "tool_call_id", tool.toolCallID, "error", err)
			}
		}
		c.sink().CloseSpan(tool.toolCallID)
		c.completeToolOutput(tool.toolCallID)
		if tool.rowKey != "" {
			c.b.finishChildConversation(tool.rowKey, agent.IncompleteTaskStatus(completion))
			if err := c.sink().CloseBackgroundTask(tool.rowKey, agent.IncompleteTaskStatus(completion)); err != nil {
				slog.Warn("close incomplete acp subagent", "agent_id", c.b.AgentID(), "row_key", tool.rowKey, "error", err)
			} else {
				c.b.openRows.closed(tool.rowKey)
			}
			// The row is over, so the child agent releases its service state, as
			// it does when the agent closes the row. This runs before forgetChild,
			// which drops the only record of the child agent.
			c.b.cleanupChildAgent(tool.rowKey)
			c.b.forgetChild(tool.rowKey)
		}
	}
}

// persistClosingTool writes the row that closes one ACP tool call.
//
// The span type falls back to contracts.ACPUpdateToolCall when no span reports one: a call that
// ended without a span still needs a type on its row. Both closing sites share that
// default and the Closing span shape, so a change to either now lands in one place.
// Each caller keeps its own error message, and its own order for completeTool, the span
// close and the background-task close, because the two sites do not agree on that order.
func (c *conversation) persistClosingTool(toolCallID string, content agent.MessageContent) error {
	sink := c.sink()
	spanType := sink.GetSpanType(toolCallID)
	if spanType == "" {
		spanType = contracts.ACPUpdateToolCall
	}
	return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: toolCallID, SpanType: spanType, Closing: true,
	})
}

func (c *conversation) completeToolOutput(toolCallID string) {
	c.b.ClearCumulativeOutput(toolCallID)
	c.sink().ReportProgress(agent.CompleteOutputProgress(toolCallID))
	if c.b.hooks.ToolOutputComplete != nil {
		c.b.hooks.ToolOutputComplete(toolCallID)
	}
}

func (c *conversation) rememberToolSubagentRow(toolCallID string, obs *SubagentObservation) {
	if toolCallID == "" || obs == nil || obs.RowKey == "" || obs.CloseRow {
		return
	}
	c.out.rememberSubagentRow(toolCallID, obs.RowKey)
}

func (c *conversation) handleToolCall(update json.RawMessage) {
	var tc ToolCallEnvelope
	if err := json.Unmarshal(update, &tc); err != nil {
		slog.Warn("acp tool_call unmarshal failed", "provider", c.b.ProviderName(), "agent_id", c.b.AgentID(), "error", err)
		return
	}
	if tc.ToolCallID == "" {
		return
	}

	spanType := tc.Kind
	if spanType == "" {
		spanType = contracts.ACPUpdateToolCall
	}

	// Ask the provider's detector BEFORE persisting, so a spawn it recognizes
	// here never reserves a color and never opens a span. The observation is
	// applied further down, at the point it was applied before.
	var obs *SubagentObservation
	if c.b.hooks.SubagentFromToolCall != nil {
		obs = c.b.hooks.SubagentFromToolCall(tc)
	}
	c.rememberToolSubagentRow(tc.ToolCallID, obs)

	sink := c.sink()
	// Persist a final tool call as a closing row. It closes an earlier pending
	// call when one exists. A call that first arrives final opens no span.
	if StatusIsFinal(tc.Status) {
		opened := sink.GetSpanType(tc.ToolCallID) != ""
		c.out.completeTool(tc.ToolCallID)
		c.completeToolOutput(tc.ToolCallID)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}, agent.SpanInfo{
			SpanID: tc.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist final acp tool_call", "agent_id", c.b.AgentID(), "kind", tc.Kind, "status", tc.Status, "error", err)
		}
		if opened {
			sink.CloseSpan(tc.ToolCallID)
		}
		c.applySubagentObservation(obs)
		return
	}
	c.rememberIncompleteTool(tc.ToolCallID, update)

	// A subagent spawn owns no span: its output lands in its own child
	// transcript, so a rail held open for the whole subagent run only pushes
	// every concurrent tool one column right.
	spawns := ObservationIsSpawn(obs)
	if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: update}, tc.ToolCallID, spanType, spawns); err != nil {
		slog.Error("persist acp tool_call", "agent_id", c.b.AgentID(), "kind", tc.Kind, "error", err)
	} else {
		c.rememberToolRequest(tc.ToolCallID, update)
	}
	c.applySubagentObservation(obs)
}

// rememberIncompleteTool retains a tool call that opened but has not ended.
//
// The frame is kept UNCHANGED. An earlier build rewrote `sessionUpdate` to
// `tool_call_update` and replaced the agent's status with `in_progress`, so an
// interrupted turn stored a frame the agent never sent. The turn-end row now stores
// the agent's own last frame, and LeapMux's own completion column states that the
// call did not finish.
func (c *conversation) rememberIncompleteTool(toolCallID string, update json.RawMessage) {
	var incoming map[string]json.RawMessage
	if json.Unmarshal(update, &incoming) != nil {
		return
	}
	c.out.rememberIncompleteTool(toolCallID, incoming, update)
}

func (c *conversation) handleToolCallUpdate(update json.RawMessage) {
	incoming, tcu, ok := parseACPToolCallUpdate(update)
	if !ok {
		slog.Warn("acp tool_call_update decode failed", "provider", c.b.ProviderName(), "agent_id", c.b.AgentID())
		return
	}
	if tcu.ToolCallID == "" {
		return
	}
	sink := c.sink()
	c.enrichToolRequest(tcu.ToolCallID, incoming)
	// BEFORE the output hooks, because a claimed notification is not output: a
	// progress sentence and a platform event carry no chunk to count, and letting
	// them reach the merge below would fold an empty update into the stored row.
	//
	// A FINAL status still falls through. The hook claims an update by its
	// notification type alone and never reads the status, so a frame that carried
	// both would have returned before `completeTool`, `persistClosingTool` and
	// `CloseSpan` -- leaving the row a spinning card for the rest of the session,
	// with its result never stored and nothing logged. Goose's own protocol is not
	// supposed to send that pair, but nothing here enforces it.
	if c.b.hooks.ToolNotification != nil && c.b.hooks.ToolNotification(tcu) && !StatusIsFinal(tcu.Status) {
		return
	}
	if c.b.hooks.ToolOutput != nil {
		if out, ok := c.b.hooks.ToolOutput(tcu); ok {
			sink.ReportProgress(agent.OutputTotalProgress(tcu.ToolCallID, out.Total, out.TotalIsMinimum))
			// The content-bearing path below reports the tail of a provider whose
			// output rides in the update's own content, so an empty one here is a
			// provider with nothing extra to say rather than a call with no output.
			if out.Tail != "" {
				sink.ReportProgress(agent.OutputTailProgress(tcu.ToolCallID, out.Tail, out.TailLost))
			}
		}
	}
	originalUpdate := update
	update, tcu, ok = c.out.mergeToolUpdate(tcu.ToolCallID, incoming, StatusIsFinal(tcu.Status), originalUpdate)
	if !ok {
		return
	}

	// Goose's tool-request meta rides content-less in_progress updates, so the
	// subagent hook runs BEFORE the content-less early-return below. OpenCode/
	// Kilo final updates close on the final-status branch below.
	if c.b.hooks.SubagentFromToolCallUpdate != nil {
		if obs := c.b.hooks.SubagentFromToolCallUpdate(tcu); obs != nil {
			c.rememberToolSubagentRow(tcu.ToolCallID, obs)
			c.applySubagentObservation(obs)
			// A spawn recognized only HERE already opened a span at its
			// tool_call, so give that span back now. Kilo is why: it opens the
			// spawn with `rawInput: {}` and fills the spawn shape only on the
			// first in-progress update. CloseSpan frees the column although the
			// subagent keeps running. The recorded span type survives it, so the
			// closing branch below still persists the real kind.
			//
			// Only before the final status. Freeing the column removes it from
			// the active set, so the closing branch would find nothing to mark
			// connector_end: the rail drawn for the whole call would stop
			// mid-transcript instead of ending. A spawn learned that late keeps
			// its span and closes it once, below.
			//
			// Only ONCE per tool call. The detector re-runs on every update, and
			// a provider that echoes its rawInput re-reports the spawn on each
			// one. Without the note, every later update would take the tracker
			// mutex and re-scan the active set to remove a span that is already
			// gone, for the whole subagent run.
			if !StatusIsFinal(tcu.Status) && ObservationIsSpawn(obs) &&
				c.out.markSpanReleased(tcu.ToolCallID) {
				sink.CloseSpan(tcu.ToolCallID)
			}
		}
	}

	switch tcu.Status {
	case "", "in_progress":
		// ACP tool output is cumulative. Count growth from the latest snapshot.
		full := ToolCallText(tcu.Content)
		if full == "" {
			return
		}
		limited := strings.HasPrefix(full, providerkit.LimitedOutputPrefix)
		if limited {
			full = strings.TrimPrefix(full, providerkit.LimitedOutputPrefix)
		}
		observed := c.b.ObserveCumulativeOutput(tcu.ToolCallID, full, limited)
		sink.ReportProgress(agent.OutputTotalProgress(tcu.ToolCallID, observed.Total, observed.Minimum))
		// The Agent Client Protocol sends the whole output on every update, so the
		// text above IS the tail. `limited` is the provider's own statement that it
		// dropped earlier bytes.
		sink.ReportProgress(agent.OutputTailProgress(tcu.ToolCallID, full, limited))
	case "completed", "failed", "cancelled":
		c.out.completeTool(tcu.ToolCallID)
		c.completeToolOutput(tcu.ToolCallID)

		content := c.b.acpMessageContent(originalUpdate, update)
		// The reader stopped this turn, so the row reports the stop rather than the
		// status a cancelled call happens to carry. Cursor and Reasonix send
		// `failed` for a command the reader stopped, and `Error` states the wrong
		// cause; OpenCode and Kilo send an empty `completed`, which states none.
		if c.b.acpInterruptRequested() {
			content.Completion = agent.MessageCompletionInterrupted
		}
		if err := c.persistClosingTool(tcu.ToolCallID, content); err != nil {
			slog.Error("persist acp tool_call_update", "agent_id", c.b.AgentID(), "status", tcu.Status, "error", err)
		}
		sink.CloseSpan(tcu.ToolCallID)
	}
}

func (c *conversation) handlePlan(update json.RawMessage) {
	if err := c.sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}, agent.SpanInfo{}); err != nil {
		slog.Error("persist acp plan", "agent_id", c.b.AgentID(), "child_agent_id", c.childAgentID, "error", err)
	}
}

// applySubagentObservation translates a provider hook's neutral observation
// into registry and child-transcript calls. A nil observation is a no-op. The shared ACP final-status map
// lives here so every provider agrees: completed->Completed, failed->Failed,
// cancelled->Stopped.
//
// A close-only observation (Mode == ModeCloseOnly) skips the upsert: it
// closes an existing row without first creating one. This matters for
// Goose/Cursor, whose closing-update hooks fire for EVERY tool_call, not just
// spawns -- the detector sets the Mode explicitly instead of relying on which
// fields happen to be empty.
//
// The child agent is created through the sink of THIS conversation, so a
// subagent that a subagent spawns becomes a child of that subagent. Every
// registry write reaches the root owner whichever sink makes it.
func (c *conversation) applySubagentObservation(obs *SubagentObservation) {
	sink := c.sink()
	if obs == nil || obs.RowKey == "" || sink == nil {
		return
	}
	b := c.b
	// Resolve the child agent id once so the upsert, report, and close use one transcript.
	if obs.Prompt != "" {
		b.subagentPrompts.Remember(obs.RowKey, obs.Prompt)
	}
	rowKey := obs.RowKey
	promptKey := obs.RowKey
	// Rename the spawn row before child resolution. OpenCode and Kilo learn the
	// child session id only on the final update. EnsureChildAgent must attach to
	// that renamed row rather than create a second row beside it.
	if obs.RenameFrom != "" && obs.RenameFrom != obs.RowKey {
		promptKey = obs.RenameFrom
		if err := sink.RenameBackgroundTask(obs.RenameFrom, obs.RowKey); err != nil {
			slog.Warn("acp subagent rename failed", "provider", b.ProviderName(), "from", obs.RenameFrom, "to", obs.RowKey, "error", err)
			// Keep all later work on the row that still exists. Creating a child
			// under the new key after a failed rename would split one task in two.
			rowKey = obs.RenameFrom
		} else {
			b.renameChildConversation(obs.RenameFrom, obs.RowKey)
			b.openRows.renamed(obs.RenameFrom, obs.RowKey)
		}
	}
	childAgentID := ""
	if obs.ChildAgentKey != "" && rowKey == obs.RowKey {
		var err error
		spawnSpanID := obs.RowKey
		if obs.RenameFrom != "" {
			spawnSpanID = obs.RenameFrom
		}
		childAgentID, err = sink.EnsureChildAgent(spawnSpanID, obs.ChildAgentKey, obs.Title)
		if err != nil {
			slog.Warn("acp subagent ensure child failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
		}
		// The child exists now, so spend the prompt the spawn remembered.
		// PersistChildPrompt is a no-op once the transcript has a message, so a
		// repeated observation cannot duplicate it.
		if childAgentID != "" {
			b.rememberChildAgent(rowKey, childAgentID, sink)
			if prompt := b.subagentPrompts.Take(promptKey); prompt != "" {
				if err := sink.PersistChildPrompt(childAgentID, prompt); err != nil {
					slog.Warn("acp subagent prompt persist failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
				}
			}
		}
	}
	// A rename+close operates on the existing spawn row. It skips the upsert
	// because the rename already preserved the row's fields.
	if obs.Mode != ModeCloseOnly && obs.RenameFrom == "" {
		kind := obs.Kind
		if kind == bgtask.KindUnspecified {
			kind = bgtask.KindSubagent
		}
		if err := sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:         rowKey,
			Kind:           kind,
			ChildAgentID:   childAgentID,
			ParentAgentID:  b.AgentID(),
			GroupKey:       obs.GroupKey,
			GroupLabel:     obs.GroupLabel,
			Title:          obs.Title,
			TitleIsCommand: obs.TitleIsCommand,
			ActiveForm:     obs.Activity,
			Status:         obs.Status,
		}); err != nil {
			slog.Warn("acp subagent upsert failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
		} else if obs.Status.IsFinished() {
			// A final status ends the row in the registry, closed or not.
			b.openRows.closed(rowKey)
		} else if !obs.CloseRow {
			b.openRows.opened(rowKey)
		}
		if obs.ChildTranscriptPayload != nil && childAgentID != "" {
			if err := sink.PersistChildMessage(childAgentID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, obs.ChildTranscriptPayload, agent.SpanInfo{}); err != nil {
				slog.Warn("acp subagent child persist failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
			}
		}
	}
	lookupOK := true
	if obs.Report.Text != "" || (obs.CloseRow && childAgentID == "") {
		var resolvedChildID string
		var err error
		resolvedChildID, _, _, err = sink.LookupBackgroundTask(rowKey)
		if err != nil {
			slog.Warn("acp subagent child lookup failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
			lookupOK = false
		} else if childAgentID == "" {
			childAgentID = resolvedChildID
		}
	}
	if obs.CloseRow {
		// The transcript of the child ends with the row, so the text and the tool
		// calls that it still holds reach that transcript BEFORE the report and
		// before the close draws its divider.
		b.finishChildConversation(rowKey, obs.Status)
	}
	if obs.Report.Text != "" {
		if lookupOK && childAgentID != "" {
			providerkit.PersistChildSubagentReport(sink, agent.ChildSubagentReportWrite{
				RowKey: rowKey,
				Write: agent.SubagentReportWrite{
					ReportID: obs.ReportID,
					Report:   obs.Report,
				},
			})
		}
	}
	if obs.CloseRow {
		// The row is over, so an unspent prompt has no transcript left to open.
		//
		// Drop BOTH keys. The prompt was remembered under the key the SPAWN
		// carried, and a provider that learns the child's stable id only on the
		// closing update (OpenCode, Kilo) re-keys the row, so obs.RowKey here is
		// the new key and RenameFrom is the one the prompt sits under. Forgetting
		// only obs.RowKey deletes a key that was never inserted and leaves the
		// spawn's entry to accumulate for the life of the process.
		b.subagentPrompts.Forget(obs.RowKey)
		if obs.RenameFrom != "" {
			b.subagentPrompts.Forget(obs.RenameFrom)
		}
		// A row whose close failed stays open, so a context clear tries it again.
		if err := sink.CloseBackgroundTask(rowKey, obs.Status); err != nil {
			slog.Warn("acp subagent close failed", "provider", b.ProviderName(), "row_key", rowKey, "error", err)
		} else {
			b.openRows.closed(rowKey)
		}
		// Release the child's per-agent service state so a long-running root
		// that cycles many subagents does not retain a stale SpanTracker + sink
		// ref per closed child until the root itself closes. The transcript row survives.
		if childAgentID != "" {
			sink.CleanupChildAgent(childAgentID)
		}
		b.forgetChild(rowKey)
	}
}

// rememberToolRequest records the request row of one tool call, so a later
// update that revises its input can enrich that row.
func (c *conversation) rememberToolRequest(toolID string, content []byte) {
	c.out.turnMu.Lock()
	defer c.out.turnMu.Unlock()
	if c.out.toolRequestContents == nil {
		c.out.toolRequestContents = make(map[string]*acpToolRequestContent)
	}
	c.out.toolRequestContents[toolID] = &acpToolRequestContent{original: append([]byte(nil), content...)}
}

// enrichToolRequest publishes late input fields while the tool still runs.
// Output and completion remain on the result row.
func (c *conversation) enrichToolRequest(toolID string, fields map[string]json.RawMessage) {
	c.out.turnMu.Lock()
	previous := c.out.toolRequestContents[toolID]
	c.out.turnMu.Unlock()
	if previous == nil {
		return
	}
	next, changed := revisedToolRequestSupplement(previous, fields)
	if !changed {
		return
	}
	updated, err := c.sink().EnrichMessage(agent.MessageEnrichment{
		SpanID: toolID, OriginalContent: previous.original,
		PreviousRevision: previous.revision, SupplementalContent: next,
	})
	if err != nil {
		slog.Warn("Publish updated ACP tool input", "error", err)
		return
	}
	if !updated {
		return
	}
	c.out.turnMu.Lock()
	if c.out.toolRequestContents[toolID] == previous {
		c.out.toolRequestContents[toolID] = &acpToolRequestContent{original: previous.original, supplemental: next, revision: previous.revision + 1}
	}
	c.out.turnMu.Unlock()
}
