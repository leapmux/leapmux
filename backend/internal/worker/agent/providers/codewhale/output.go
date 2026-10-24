package codewhale

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// This file handles the conversation output of one thread:
// - Turn events.
// - Text and reasoning.
// - Tool calls.
// - Native notices.
//
// The worker persists the runtime's OWN event envelopes, byte for byte, and the
// browser plugin reads the same envelopes. It persists the FINAL event of an
// item, never its start alone, for everything but a tool call: an item's final
// event carries its whole text in `detail`, so the streamed deltas only feed the
// live progress counter and the buffer that keeps a dying process's text.

// itemEventPayload is the payload of every `item.*` event.
type itemEventPayload struct {
	Item turnItem `json:"item"`
	// Tool is on item.started of a tool call alone. It carries the PARSED input,
	// where the item's metadata carries it as a JSON string.
	Tool *toolStart `json:"tool"`
}

// turnItem is the runtime's TurnItemRecord.
type turnItem struct {
	ID       string          `json:"id"`
	TurnID   string          `json:"turn_id"`
	Kind     string          `json:"kind"`
	Status   string          `json:"status"`
	Summary  string          `json:"summary"`
	Detail   string          `json:"detail"`
	Metadata json.RawMessage `json:"metadata"`
}

// toolStart identifies a tool call on its start event.
type toolStart struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// itemMetadata is the part of an item's `metadata` that identifies a tool
// call. contract_tags_test.go pins its tags to contracts/codewhale-protocol.json,
// which the browser plugin reads the same names from.
//
// The runtime classifies a tool item by a name heuristic -- `bash` arrives as
// `tool_call` and `todo_write` as `file_change` -- so the tool name, not the
// item kind, is what says which tool ran.
type itemMetadata struct {
	SpilloverPath string `json:"spillover_path"`
	// ToolMedia identifies immutable native image output files for this completed call.
	ToolMedia json.RawMessage `json:"tool_media"`
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	// ToolCallID replaces ToolUseID on the final event of a question, whose
	// answer the runtime redacts.
	ToolCallID string `json:"tool_call_id"`
	// ToolInput is the call's input as a JSON STRING.
	ToolInput string `json:"tool_input"`
	// DeferredToolLoaded marks the first call of a deferred tool. The runtime
	// loads the tool's schema and does NOT run the call; the model then calls
	// again.
	DeferredToolLoaded bool `json:"deferred_tool_loaded"`
	// Visibility is `internal` for a status item that a release after 0.10.0
	// marks as the runtime's own plumbing.
	Visibility string `json:"visibility"`
}

func (item turnItem) metadata() itemMetadata {
	var md itemMetadata
	if len(item.Metadata) > 0 {
		_ = json.Unmarshal(item.Metadata, &md)
	}
	return md
}

// parseItemPayload decodes an item event's payload.
func parseItemPayload(env codewhaleEnvelope) (itemEventPayload, bool) {
	var payload itemEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		slog.Warn("codewhale item event unmarshal failed", "event", env.Event, "error", err)
		return itemEventPayload{}, false
	}
	return payload, true
}

// toolIdentity reads which tool call an item is. ok is false for an item that
// is not a tool call.
func (p itemEventPayload) toolIdentity() (spanID, name string, input json.RawMessage, ok bool) {
	md := p.Item.metadata()
	name = md.ToolName
	spanID = md.ToolUseID
	if spanID == "" {
		spanID = md.ToolCallID
	}
	if md.ToolInput != "" && json.Valid([]byte(md.ToolInput)) {
		input = json.RawMessage(md.ToolInput)
	}
	if p.Tool != nil {
		if p.Tool.Name != "" {
			name = p.Tool.Name
		}
		if p.Tool.ID != "" {
			spanID = p.Tool.ID
		}
		if len(p.Tool.Input) > 0 {
			input = p.Tool.Input
		}
	}
	return spanID, name, input, name != ""
}

// completionFor maps an item's final event onto LeapMux's completion.
func completionFor(event string) agent.MessageCompletion {
	switch event {
	case contracts.CodewhaleEventItemInterrupted, contracts.CodewhaleEventItemCanceled:
		return agent.MessageCompletionInterrupted
	case contracts.CodewhaleEventItemFailed:
		return agent.MessageCompletionError
	default:
		return agent.MessageCompletionComplete
	}
}

// --- turns ---

// turnEventPayload is the payload of turn.started and turn.completed.
type turnEventPayload struct {
	Turn turnRecord `json:"turn"`
}

// handleTurnStarted sets the turn flag. A turn the runtime starts on its own --
// a goal continuation, a compaction -- is a turn like any other: the agent is
// busy, and the input queue waits for it.
func (a *Agent) handleTurnStarted(env codewhaleEnvelope) {
	turnID := env.TurnID
	var payload turnEventPayload
	if json.Unmarshal(env.Payload, &payload) == nil && payload.Turn.ID != "" {
		turnID = payload.Turn.ID
	}
	if a.markTurnStarted(turnID) {
		a.discardGeneration()
		a.finishSteerRequestBoundary()
		a.modelRequestObserved = false
		a.sink.ReportProgress(agent.ResetModelProgress())
		a.PublishTurnActive()
	}
}

// codewhaleTurnEnd retains the native end while a steer request remains unresolved.
type codewhaleTurnEnd struct {
	env     codewhaleEnvelope
	payload turnEventPayload
	turnID  string
}

// handleTurnCompleted records the native end and waits for outstanding steer requests.
func (a *Agent) handleTurnCompleted(env codewhaleEnvelope) {
	var payload turnEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		slog.Warn("codewhale turn.completed unmarshal failed", "agent_id", a.AgentID(), "error", err)
	}
	turnID := env.TurnID
	if payload.Turn.ID != "" {
		turnID = payload.Turn.ID
	}
	a.Mu.Lock()
	stale := a.finishedTurns.contains(turnID) || a.turnID != "" && turnID != "" && a.turnID != turnID
	a.finishedTurns.add(turnID)
	pending := a.steerCall != nil
	a.Mu.Unlock()
	if stale {
		slog.Debug("codewhale ignored a stale turn end", "agent_id", a.AgentID(), "turn_id", turnID)
		if err := agent.FlushPendingOutput(a.sink); err != nil {
			slog.Error("codewhale retry pending output", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	end := codewhaleTurnEnd{env: env, payload: payload, turnID: turnID}
	if pending {
		a.pendingTurnEnd = &end
		a.PublishTurnActive()
		return
	}
	a.completeTurn(end)
}

// completeTurn runs once after every native steer call of this turn returns.
func (a *Agent) completeTurn(end codewhaleTurnEnd) {
	env, payload, turnID := end.env, end.payload, end.turnID
	a.Mu.Lock()
	compacting := a.compactionLive
	a.Mu.Unlock()
	completion := turnCompletion(payload.Turn.Status)
	if compacting && a.endCompaction(turnID) {
		outcome := providerkit.CompactionEndFailed
		if completion == agent.MessageCompletionInterrupted {
			outcome = providerkit.CompactionEndAborted
		}
		providerkit.PersistCompactionEnded(a.sink, outcome, "")
	}
	a.flushGeneration(completion)
	a.persistIncompleteTools(completion)
	a.finishSteerRequestBoundary()
	// A question or an approval cannot outlive its turn. The runtime settles each
	// one with its own event as well; this retires any card that event missed.
	a.withdrawControlsOfTurn(turnID)
	a.ResetCumulativeOutput()
	a.sink.ReportProgress(agent.ResetModelProgress())

	// A proved native cancellation returns the retained input to the queue.
	// An unresolved delivery becomes uncertain before the queue releases the turn.
	a.settleSteersOfTurn(turnID)
	failure, _ := providerkit.RawString(payload.Turn.Error)
	a.persistHeldErrors(turnID, failure, false)

	if err := a.sink.PersistTurnEnd(a.turnEndContent(env.raw), agent.SpanInfo{}); err != nil {
		slog.Error("codewhale persist turn end", "agent_id", a.AgentID(), "error", err)
	}
	a.sink.ResetSpans()
	if a.markTurnFinished(turnID) {
		a.PublishTurnActive()
	}
	a.sink.CancelAutoRetry(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
	a.refreshContextUsage()
}

// turnCompletion maps a turn's final status onto LeapMux's completion.
func turnCompletion(status string) agent.MessageCompletion {
	switch status {
	case contracts.CodewhaleTurnStatusCompleted:
		return agent.MessageCompletionComplete
	case contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled:
		return agent.MessageCompletionInterrupted
	default:
		return agent.MessageCompletionError
	}
}

// --- items ---

// handleItemStarted opens a tool span or records a generated item.
// A new item of the same generated kind replaces an unfinished stream attempt.
func (a *Agent) handleItemStarted(env codewhaleEnvelope) {
	payload, ok := parseItemPayload(env)
	if !ok {
		return
	}
	item := payload.Item
	switch item.Kind {
	case contracts.CodewhaleItemKindContextCompaction:
		a.publishCompaction()
		return
	case contracts.CodewhaleItemKindAgentMessage, contracts.CodewhaleItemKindAgentReasoning:
		if item.ID == "" {
			return
		}
		for kind, id := range a.openGeneratedItems {
			if id == item.ID {
				if kind != item.Kind {
					slog.Debug("codewhale ignored a conflicting generated item kind", "agent_id", a.AgentID(), "item_id", item.ID)
				}
				return
			}
		}
		if previous := a.openGeneratedItems[item.Kind]; previous != "" {
			// A replacement of the same generated kind is a stream resume even without a request snapshot.
			a.discardGeneration()
			a.finishSteerRequestBoundary()
		}
		if a.openGeneratedItems == nil {
			a.openGeneratedItems = make(map[string]string)
		}
		a.openGeneratedItems[item.Kind] = item.ID
		return
	}
	if codewhaleNotificationItemKinds[item.Kind] || item.Kind == itemKindUserMessage {
		return
	}
	spanID, name, input, isTool := payload.toolIdentity()
	if !isTool {
		if !codewhaleToolItemKind(item.Kind) {
			a.persistUnknownFrame(env)
		}
		return
	}
	if spanID == "" {
		return
	}
	a.openToolCall(env, payload.Item.ID, spanID, name, input)
}

// handleItemDelta feeds the streamed text of a message or a reasoning item to
// the progress counter, and keeps it until the item's final event.
func (a *Agent) handleItemDelta(env codewhaleEnvelope) {
	var payload struct {
		Delta string `json:"delta"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Delta == "" || env.ItemID == "" {
		return
	}
	var kind agent.AssembledMessageKind
	switch payload.Kind {
	case contracts.CodewhaleItemKindAgentMessage:
		kind = agent.AssembledMessageKindText
	case contracts.CodewhaleItemKindAgentReasoning:
		kind = agent.AssembledMessageKindReasoning
	default:
		if !codewhaleToolItemKind(payload.Kind) && !codewhaleNotificationItemKinds[payload.Kind] && payload.Kind != itemKindUserMessage {
			a.persistUnknownFrame(env)
		}
		return
	}
	a.generationBuffer.Append(env.ItemID, kind, payload.Delta, providerkit.JoinVerbatim)
	a.sink.ReportProgress(agent.ModelTextProgress(env.ItemID, payload.Delta))
}

// handleItemFinished persists an item's final event.
func (a *Agent) handleItemFinished(env codewhaleEnvelope) {
	payload, ok := parseItemPayload(env)
	if !ok {
		return
	}
	item := payload.Item
	if codewhaleNotificationItemKinds[item.Kind] {
		a.handleNoticeItem(env, item)
		return
	}
	switch item.Kind {
	case contracts.CodewhaleItemKindAgentMessage, contracts.CodewhaleItemKindAgentReasoning:
		a.finishGeneratedItem(env, item)
		return
	case itemKindUserMessage:
		// LeapMux already persisted what the reader sent, and a goal kickoff or a
		// continuation pass is the runtime's own prompt.
		return

	}
	spanID, name, _, isTool := payload.toolIdentity()
	if !isTool {
		spanID = a.spanForItem(item.ID)
		if spanID == "" {
			if !codewhaleToolItemKind(item.Kind) {
				a.persistUnknownFrame(env)
			}
			return
		}
	}
	if spanID == "" {
		spanID = a.spanForItem(item.ID)
	}
	if spanID == "" {
		return
	}
	a.closeToolCall(env, payload, spanID, name)
}

func codewhaleToolItemKind(kind string) bool {
	switch kind {
	case contracts.CodewhaleItemKindToolCall, contracts.CodewhaleItemKindFileChange, contracts.CodewhaleItemKindCommandExecution:
		return true
	default:
		return false
	}
}

// publishCompaction reports the start once for the active native compaction.
func (a *Agent) publishCompaction() {
	a.Mu.Lock()
	live := a.compactionLive
	a.compactionLive = true
	a.Mu.Unlock()
	if !live {
		providerkit.PublishCompaction(a.sink, "")
	}
}

// endCompaction records the native end and clears its live indicator.
// It returns whether this agent observed a live start.
func (a *Agent) endCompaction(turnID string) bool {
	a.Mu.Lock()
	live := a.compactionLive
	a.compactionLive = false
	if turnID != "" {
		a.compactionEndedTurn = turnID
	}
	a.Mu.Unlock()
	if live || turnID != "" {
		providerkit.EndCompaction(a.sink)
	}
	return live
}

// handleNoticeItem keeps native notice membership separate from tool handling.
func (a *Agent) handleNoticeItem(env codewhaleEnvelope, item turnItem) {
	switch item.Kind {
	case contracts.CodewhaleItemKindStatus:
		if !statusItemIsPlumbing(item.Summary, item.metadata().Visibility) {
			a.persistNotification(env)
		}
	case contracts.CodewhaleItemKindContextCompaction:
		turnID := env.TurnID
		if turnID == "" {
			turnID = item.TurnID
		}
		a.endCompaction(turnID)
		a.persistNotification(env)
	case contracts.CodewhaleItemKindError:
		if env.TurnID != "" && item.TurnID != "" && env.TurnID != item.TurnID {
			// Conflicting native identities cannot prove that a divider repeats this error.
			a.persistNotification(env)
			return
		}
		if env.TurnID == "" {
			env.TurnID = item.TurnID
		}
		if env.TurnID == "" {
			// No turn can supply a matching divider for this error.
			a.persistNotification(env)
			return
		}
		a.heldErrors = append(a.heldErrors, env)
	}
}

// persistHeldErrors stores errors that the matching turn divider does not repeat.
// Process exit stores every remaining error because no later divider can replace it.
func (a *Agent) persistHeldErrors(turnID, repeated string, all bool) {
	pending := a.heldErrors
	a.heldErrors = nil
	for _, env := range pending {
		if !all && env.TurnID != turnID {
			a.heldErrors = append(a.heldErrors, env)
			continue
		}
		payload, ok := parseItemPayload(env)
		if !all && ok {
			text := strings.TrimSpace(payload.Item.Detail)
			if text == "" {
				text = strings.TrimSpace(payload.Item.Summary)
			}
			if text == strings.TrimSpace(repeated) {
				continue
			}
		}
		a.persistNotification(env)
	}
}

// finishGeneratedItem persists a message or a reasoning item from its final
// event. The event's `detail` holds the whole text, so the buffer that kept the
// deltas is dropped; it stands in only when the event states no text.
func (a *Agent) finishGeneratedItem(env codewhaleEnvelope, item turnItem) {
	defer a.releaseSteerOutputs([]steerOutputKey{{kind: steerGeneratedOutput, id: item.ID}})
	completion := completionFor(env.Event)
	if a.openGeneratedItems[item.Kind] == item.ID {
		delete(a.openGeneratedItems, item.Kind)
	}
	if strings.TrimSpace(item.Detail) == "" {
		// The event carries no text of its own, so the deltas are the only copy.
		if err := providerkit.PersistGenerationScopes(&a.generationBuffer, a.sink, []string{item.ID}, completion, a.IsDiscardingOutput()); err != nil {
			slog.Error("codewhale persist streamed text", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	defer a.sink.ReportProgress(agent.CompleteModelProgress(item.ID))
	a.generationBuffer.Discard(item.ID)
	if a.IsDiscardingOutput() {
		return
	}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: env.raw, Completion: completion}, agent.SpanInfo{}); err != nil {
		slog.Error("codewhale persist message", "agent_id", a.AgentID(), "kind", item.Kind, "error", err)
	}
}

// discardGeneration drops open attempt scopes without changing accepted pending writes.
func (a *Agent) discardGeneration() {
	ids := a.openGenerationIDs()
	providerkit.DiscardGenerationScopes(&a.generationBuffer, a.sink, ids)
	clear(a.openGeneratedItems)
	a.releaseSteerOutputs(generatedSteerOutputs(ids))
}

// handleModelRequestSnapshot reads preparation and final diagnostics from the native request stream.
// Preparation starts a new attempt. The snapshot does not prove delivery to the model provider.
func (a *Agent) handleModelRequestSnapshot(env codewhaleEnvelope) {
	var payload struct {
		Snapshot *struct {
			TurnID struct {
				Value     string `json:"value"`
				Truncated *bool  `json:"truncated"`
			} `json:"turn_id"`
			Step     *int64          `json:"step"`
			Finished json.RawMessage `json:"terminal"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(env.Payload, &payload) != nil || payload.Snapshot == nil {
		return
	}
	snapshot := payload.Snapshot
	if snapshot.Step == nil || *snapshot.Step < 0 || snapshot.TurnID.Value == "" || snapshot.TurnID.Truncated == nil || *snapshot.TurnID.Truncated {
		return
	}
	a.Mu.Lock()
	own := env.TurnID != "" && (a.turnID == "" || a.turnID == env.TurnID)
	a.Mu.Unlock()
	if !own {
		return
	}
	a.modelRequestObserved = true
	if providerkit.RawPresent(snapshot.Finished) {
		if providerkit.StringField(snapshot.Finished, "status") != "" {
			a.finishSteerRequestBoundary()
		}
		return
	}
	a.discardGeneration()
	a.finishSteerRequestBoundary()
}

// flushGeneration stores unfinished streamed text in its original order.
func (a *Agent) flushGeneration(completion agent.MessageCompletion) {
	ids := a.openGenerationIDs()
	if err := providerkit.FlushGeneration(&a.generationBuffer, a.sink, completion, a.IsDiscardingOutput()); err != nil {
		slog.Error("codewhale persist streamed text", "agent_id", a.AgentID(), "error", err)
	}
	clear(a.openGeneratedItems)
	a.releaseSteerOutputs(generatedSteerOutputs(ids))
}

// persistNotification records one runtime event as a notification row.
func (a *Agent) persistNotification(env codewhaleEnvelope) {
	if a.IsDiscardingOutput() || len(env.raw) == 0 {
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: env.raw}); err != nil {
		slog.Error("codewhale persist notification", "agent_id", a.AgentID(), "event", env.Event, "error", err)
	}
}

// --- tool calls ---

// codewhaleToolCall is everything the agent knows about one open tool call.
type codewhaleToolCall struct {
	spanID string
	itemID string
	name   string
	input  json.RawMessage
	// spawns marks an `agent` call that starts a subagent. A spawn owns no span:
	// its work lands in a child transcript.
	spawns bool
	// lastFrame is the call's start event, byte for byte. A turn that ends before
	// the call does stores that frame as the closing row, so the transcript never
	// holds an event the runtime did not send.
	lastFrame []byte
	// held marks a call whose start row is not persisted yet. See
	// toolCallIsHeld.
	held  bool
	order uint64
}

// toolCallIsHeld reports whether a tool call's start row waits until the call
// shows that it ran.
//
// `request_user_input` is a DEFERRED tool: the runtime answers the first call
// by loading the tool's schema, and it runs nothing. The runtime also redacts
// every result of this tool on its event stream -- the summary reads "User
// input submitted" and the metadata loses `deferred_tool_loaded` -- so the
// final event cannot tell a schema load from an answered question. The
// `user_input.required` event can: a call that asks the reader raises it, and
// a schema load never does. So the call's rows wait for that event, and a call
// that completes without it leaves no row.
func toolCallIsHeld(name string) bool {
	return name == contracts.CodewhaleToolRequestUserInput
}

// codewhaleToolCalls indexes the open tool calls. The caller holds Mu.
type codewhaleToolCalls struct {
	bySpan    map[string]*codewhaleToolCall
	byItem    map[string]string
	nextOrder uint64
}

func (t *codewhaleToolCalls) add(call *codewhaleToolCall) {
	if t.bySpan == nil {
		t.bySpan = make(map[string]*codewhaleToolCall)
		t.byItem = make(map[string]string)
	}
	call.order = t.nextOrder
	t.nextOrder++
	t.bySpan[call.spanID] = call
	if call.itemID != "" {
		t.byItem[call.itemID] = call.spanID
	}
}

func (t *codewhaleToolCalls) remove(spanID string) *codewhaleToolCall {
	call := t.bySpan[spanID]
	if call == nil {
		return nil
	}
	delete(t.bySpan, spanID)
	if call.itemID != "" {
		delete(t.byItem, call.itemID)
	}
	return call
}

// spanForItem returns the span of an open tool call from its item id.
func (a *Agent) spanForItem(itemID string) string {
	if itemID == "" {
		return ""
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.tools.byItem[itemID]
}

// toolCallInput returns the input of an open tool call, for the approval that
// states none of its own.
func (a *Agent) toolCallInput(spanID string) (name string, input json.RawMessage) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if call := a.tools.bySpan[spanID]; call != nil {
		return call.name, call.input
	}
	return "", nil
}

// openToolCall persists a tool call's opening row and opens its span.
func (a *Agent) openToolCall(env codewhaleEnvelope, itemID, spanID, name string, input json.RawMessage) {
	spawns := toolSpawnsSubagent(name, input)
	a.Mu.Lock()
	if a.tools.bySpan[spanID] != nil {
		// A replayed start of a call that is already open.
		a.Mu.Unlock()
		return
	}
	held := toolCallIsHeld(name)
	a.tools.add(&codewhaleToolCall{
		spanID:    spanID,
		itemID:    itemID,
		name:      name,
		input:     input,
		spawns:    spawns,
		lastFrame: env.raw,
		held:      held,
	})
	a.Mu.Unlock()
	if held {
		return
	}
	a.persistToolStart(env.raw, spanID, name, spawns)
}

// persistToolStart persists a tool call's opening row and opens its span.
func (a *Agent) persistToolStart(raw []byte, spanID, name string, spawns bool) {
	if a.IsDiscardingOutput() {
		return
	}
	if err := providerkit.OpenToolSpan(a.sink, agent.MessageContent{Original: raw}, spanID, name, spawns); err != nil {
		slog.Error("codewhale persist tool start", "agent_id", a.AgentID(), "tool", name, "error", err)
	}
}

// releaseHeldCall persists the start row of a held call, once the call shows
// that it runs. It does nothing for a call that is not held.
func (a *Agent) releaseHeldCall(spanID string) {
	a.Mu.Lock()
	call := a.tools.bySpan[spanID]
	if call == nil || !call.held {
		a.Mu.Unlock()
		return
	}
	call.held = false
	raw, name, spawns := call.lastFrame, call.name, call.spawns
	a.Mu.Unlock()
	a.persistToolStart(raw, spanID, name, spawns)
}

// closeToolCall persists a tool call's final row, closes its span, and hands
// the result to the tool's own bookkeeping.
//
// The first call of a deferred tool is closed like any other: the runtime only
// loaded the tool's schema, and the model calls again. The browser plugin hides
// its result row and states the call on its request row, and it does not count
// toward the turn's tools. A held call that asked nothing leaves no row at all
// (see toolCallIsHeld).
func (a *Agent) closeToolCall(env codewhaleEnvelope, payload itemEventPayload, spanID, name string) {
	defer a.releaseSteerOutputs([]steerOutputKey{{kind: steerToolOutput, id: spanID}})
	md := payload.Item.metadata()
	a.Mu.Lock()
	call := a.tools.remove(spanID)
	if call != nil && name == "" {
		name = call.name
	}
	// A held call that completed never asked the reader anything: it was the
	// schema load. It leaves no row and counts for nothing.
	schemaLoad := call != nil && call.held && env.Event == contracts.CodewhaleEventItemCompleted
	if !md.DeferredToolLoaded && !schemaLoad {
		a.TurnToolUses++
	}
	a.Mu.Unlock()
	if name == contracts.CodewhaleToolRequestUserInput {
		a.withdrawQuestionOfCall(spanID)
	}
	if schemaLoad {
		return
	}
	if call != nil && call.held {
		// A held call that failed or stopped before it asked: its start row goes
		// in first, so the failure has a call to belong to.
		a.persistToolStart(call.lastFrame, spanID, call.name, call.spawns)
	}

	if !a.IsDiscardingOutput() {
		content := agent.MessageContent{Original: env.raw, Completion: completionFor(env.Event)}
		if env.Event == contracts.CodewhaleEventItemCompleted {
			content.Supplemental = a.recoverToolMedia(md, spanID, name, len(env.raw))

		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			content,
			agent.SpanInfo{SpanID: spanID, SpanType: name, Closing: true}); err != nil {
			slog.Error("codewhale persist tool result", "agent_id", a.AgentID(), "tool", name, "error", err)
		}
	}
	a.sink.CloseSpan(spanID)
	a.sink.ReportProgress(agent.CompleteOutputProgress(spanID))

	if md.DeferredToolLoaded {
		return
	}
	var input json.RawMessage
	if call != nil {
		input = call.input
	}
	if len(input) == 0 {
		_, _, input, _ = payload.toolIdentity()
	}
	a.observeToolResult(env, payload, spanID, name, input)
}

// observeToolResult gives a finished tool call to the features that read one:
// the subagent registry, the workflow registry and the shell job registry.
//
// The shell registry reads every tool, because more than one tool starts or
// waits for a shell job, and each of them states the job in its metadata.
func (a *Agent) observeToolResult(env codewhaleEnvelope, payload itemEventPayload, spanID, name string, input json.RawMessage) {
	switch name {
	case contracts.CodewhaleToolAgent:
		a.observeAgentToolResult(env, payload, spanID, input)
	case contracts.CodewhaleToolWorkflow:
		a.observeWorkflowToolResult(env, payload, spanID, input)
	}
	if env.Event == contracts.CodewhaleEventItemCompleted {
		a.observeShellResult(payload, spanID, input)
	}
}

// persistIncompleteTools closes every tool call that its turn, or the process,
// ended before the call did. The row is the call's own start event, and the
// completion column states that the call did not finish.
func (a *Agent) persistIncompleteTools(completion agent.MessageCompletion) {
	a.Mu.Lock()
	calls := make([]*codewhaleToolCall, 0, len(a.tools.bySpan))
	for spanID := range a.tools.bySpan {
		calls = append(calls, a.tools.remove(spanID))
	}
	a.Mu.Unlock()
	sort.Slice(calls, func(i, j int) bool { return calls[i].order < calls[j].order })
	keys := make([]steerOutputKey, 0, len(calls))
	for _, call := range calls {
		if call.held {
			// The turn ended before the call asked anything. It still ran, so both of
			// its rows go in, and the closing row states that it did not finish.
			a.persistToolStart(call.lastFrame, call.spanID, call.name, call.spawns)
		}
		if !a.IsDiscardingOutput() && len(call.lastFrame) > 0 {
			if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: call.lastFrame, Completion: completion},
				agent.SpanInfo{SpanID: call.spanID, SpanType: call.name, Closing: true}); err != nil {
				slog.Error("codewhale persist incomplete tool", "agent_id", a.AgentID(), "tool", call.name, "error", err)
			}
		}
		a.sink.CloseSpan(call.spanID)
		a.sink.ReportProgress(agent.CompleteOutputProgress(call.spanID))
		keys = append(keys, steerOutputKey{kind: steerToolOutput, id: call.spanID})
	}
	a.releaseSteerOutputs(keys)
}
