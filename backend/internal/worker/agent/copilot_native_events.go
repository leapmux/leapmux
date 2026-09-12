package agent

import (
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	// copilotMethodSessionEvent carries every frame the transcript reads.
	copilotMethodSessionEvent = "session.event"
	// copilotMethodSessionLifecycle carries the runtime's own session metadata: the
	// start time and the last modified time. One trivial turn sends ten of them.
	copilotMethodSessionLifecycle = "session.lifecycle"
	// copilotMethodHostEvent carries the runtime's feature-flag map, which is some
	// four kilobytes of switches that describe the runtime rather than the work.
	copilotMethodHostEvent = "host.event"
)

// copilotEventUsageCheckpoint reports the credits one turn spent and the model cache
// state. The runtime does not mark it ephemeral, so it would otherwise reach the
// transcript as a row that states no conversation.
const copilotEventUsageCheckpoint = "session.usage_checkpoint"

// copilotMethodIsTelemetry reports whether a method describes the RUNTIME rather
// than the work.
//
// Neither of these carries conversation, and neither feeds a counter LeapMux shows,
// so the transcript is better without them. Both remain in the runtime's own session
// store, which is where a reader who wants them looks.
func copilotMethodIsTelemetry(method string) bool {
	return method == copilotMethodSessionLifecycle || method == copilotMethodHostEvent
}

// copilotEventIsRuntimeTrace reports whether an event type names a family that
// describes the RUNTIME rather than the work.
//
// Two families qualify. The `model.` family is the runtime's own model-call trace:
// the request, the response, a snapshot of the whole message list, and the call's
// start and end. Every part of it repeats what the `assistant.` and `tool.` events
// already deliver, so it is a second copy of the conversation rather than the
// conversation. One trivial turn sent 71 kilobytes of it, and the reader saw ten
// raw-JSON rows. The `hook.` family states that the runtime ran one of its own hooks;
// a hook that changes the work changes a tool call, and that call has its own rows.
//
// `model.call_failure` is the one exception. LeapMux surfaces it as a notification,
// because a model call that failed is an answer the reader acts on.
func copilotEventIsRuntimeTrace(eventType string) bool {
	if eventType == contracts.CopilotEventModelCallFailure {
		return false
	}
	return strings.HasPrefix(eventType, contracts.CopilotEventPrefixModelTrace) ||
		strings.HasPrefix(eventType, contracts.CopilotEventPrefixHook)
}

// copilotNativeChild is one native subagent and the transcript that holds it.
//
// The native stream states the owner of every event, so a child needs no file
// read to find its parent: `subagent.started` carries the spawning tool call,
// and this agent already knows which transcript that call opened.
type copilotNativeChild struct {
	sink            ProviderServices
	owner           ProviderServices
	ownerAgentID    string
	workerAgentID   string
	nativeAgentID   string
	spawnToolCallID string
}

// copilotStreamedText is one accumulating assistant segment and the transcript it
// belongs to. The identity is the runtime's own message or reasoning id.
type copilotStreamedText struct {
	scope string
	sink  ProviderServices
}

// copilotOpenTool remembers where one running tool call belongs.
//
// The completion event repeats the tool-call ID and nothing else that identifies
// the transcript, so the start event is the only place that link can be made.
// That start event is also the frame a turn end stores when the runtime never
// finishes the call, so its bytes and its arrival order are kept here too.
type copilotOpenTool struct {
	child      *copilotNativeChild
	name       string
	arguments  json.RawMessage
	spawns     bool
	startFrame []byte
	order      uint64
}

// copilotToolStart is the part of `tool.execution_start` that LeapMux reads.
type copilotToolStart struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// copilotSubagentEvent is the part of every `subagent.*` event that LeapMux reads.
type copilotSubagentEvent struct {
	ToolCallID       string `json:"toolCallId"`
	AgentName        string `json:"agentName"`
	AgentDisplayName string `json:"agentDisplayName"`
	AgentDescription string `json:"agentDescription"`
	Cancelled        bool   `json:"cancelled"`
}

// copilotTaskInput is the `task` tool's argument object.
type copilotTaskInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Mode        string `json:"mode"`
}

// handleNativeEvent routes one decoded session event. It returns with the event
// persisted exactly once, in the transcript that owns it.
//
// The caller holds outputMu, which is also what guards every map below. Each
// branch that needs a round trip to the runtime starts a goroutine, because the
// response for such a request arrives on THIS goroutine and a direct call would
// wait for itself.
func (a *copilotAgent) handleNativeEvent(raw []byte, event copilotEvent) {
	if a.handleNativeControlEvent(raw, event) {
		return
	}
	switch event.Type {
	case contracts.CopilotEventSubagentStarted:
		a.startNativeSubagent(raw, event)
		return
	case contracts.CopilotEventSubagentCompleted, contracts.CopilotEventSubagentFailed:
		a.finishNativeSubagent(raw, event)
		return
	case contracts.CopilotEventToolStarted:
		a.startNativeTool(raw, event)
		return
	case contracts.CopilotEventToolCompleted:
		a.completeNativeTool(raw, event)
		return
	}
	child := a.childForEvent(event)
	sink := a.sinkFor(child)
	switch event.Type {
	case contracts.CopilotEventAssistantTurnStart:
		if child == nil && !a.closing {
			a.setNativeTurnActive(true)
		}
	case contracts.CopilotEventSessionIdle:
		if child == nil {
			a.endNativeTurn(raw, copilotTurnWasAborted(event.Data))
			return
		}
	case contracts.CopilotEventSessionUsageInfo:
		a.reportNativeContextUsage(event.Data)
	case copilotEventUsageCheckpoint:
		// Credit totals and model cache state. LeapMux surfaces neither, and the
		// runtime does not mark the event ephemeral, so the transcript would hold one
		// row of counters for every turn.
		return
	case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta,
		contracts.CopilotEventAssistantReasoningDelta,
		contracts.CopilotEventAssistantMessage, contracts.CopilotEventAssistantReasoning:
		a.reportNativeTextProgress(sink, event)
	case contracts.CopilotEventSessionModeChanged, contracts.CopilotEventSessionPermissionsChanged,
		contracts.CopilotEventSessionModelChange:
		a.refreshNativeSettingsInBackground()
	case contracts.CopilotEventSessionAutopilotObjectiveChanged:
		a.refreshNativeGoalInBackground()
	}
	// `Ephemeral` is the runtime's own "do not store" mark. The trace test is
	// LeapMux's: the runtime stores its trace itself and marks none of it ephemeral.
	if event.Ephemeral || copilotEventIsRuntimeTrace(event.Type) {
		return
	}
	a.persistNativeFrameTo(sink, raw, SpanInfo{})
}

// copilotTurnWasAborted reports whether the reader stopped the turn that is ending.
// The runtime states it on the idle event, and the browser reads the same field.
func copilotTurnWasAborted(data json.RawMessage) bool {
	var idle struct {
		Aborted bool `json:"aborted"`
	}
	return json.Unmarshal(data, &idle) == nil && idle.Aborted
}

// sinkFor resolves the transcript that owns an event. A nil child is the root.
func (a *copilotAgent) sinkFor(child *copilotNativeChild) ProviderServices {
	if child == nil {
		return a.sink
	}
	return child.sink
}

// childForEvent resolves the subagent that emitted an event.
//
// An unknown agent ID answers nil, so the event reaches the root transcript
// rather than disappearing. That happens when a resumed session replays a
// subagent whose `subagent.started` this process never saw, and a visible row in
// the wrong transcript is recoverable where a dropped one is not.
func (a *copilotAgent) childForEvent(event copilotEvent) *copilotNativeChild {
	if event.AgentID == "" {
		return nil
	}
	child := a.children[event.AgentID]
	if child == nil {
		slog.Debug("Copilot event names an unknown subagent", "agent_id", a.agentID, "native_agent_id", event.AgentID, "event", event.Type)
	}
	return child
}

func (a *copilotAgent) endNativeTurn(raw []byte, aborted bool) {
	if !a.setNativeTurnActive(false) {
		a.persistNativeFrame(raw, SpanInfo{})
		return
	}
	// A segment the runtime never finished ends with the turn. It reads as interrupted
	// only when the turn was: a turn that ended by itself and left a segment open
	// produced all the text it was going to.
	completion := MessageCompletionComplete
	if aborted {
		completion = MessageCompletionInterrupted
	}
	a.closeStreamedNativeText(completion)
	a.closeOpenNativeTools()
	a.sink.ReportProgress(ResetProgress())
	if err := a.sink.PersistTurnEnd(MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}, SpanInfo{}); err != nil {
		slog.Error("Persist Copilot turn end", "agent_id", a.agentID, "error", err)
	}
}

// closeOpenNativeTools ends every call the runtime started and never finished.
//
// The runtime sends no completion for a call its turn cut short, so without this
// sweep the card keeps a running badge for the rest of the session. The closing row
// is the agent's OWN start frame, and LeapMux's completion column is what states
// that the call did not finish -- nothing here invents a result.
//
// The order is the order the calls opened, so the rows read as the agent ran them.
func (a *copilotAgent) closeOpenNativeTools() {
	open := a.openTools
	a.openTools = nil
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		if open[ids[left]].order != open[ids[right]].order {
			return open[ids[left]].order < open[ids[right]].order
		}
		return ids[left] < ids[right]
	})
	for _, id := range ids {
		tool := open[id]
		sink := a.sinkFor(tool.child)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{
			Original:       tool.startFrame,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     MessageCompletionInterrupted,
		}, SpanInfo{SpanID: id, SpanType: tool.name, Closing: true}); err != nil {
			slog.Error("Persist Copilot interrupted tool", "agent_id", a.agentID, "tool_call_id", id, "error", err)
		}
		if !tool.spawns {
			sink.CloseSpan(id)
		}
	}
}

// startNativeTool opens the tool call's span in the transcript that owns it.
func (a *copilotAgent) startNativeTool(raw []byte, event copilotEvent) {
	var start copilotToolStart
	if err := json.Unmarshal(event.Data, &start); err != nil || start.ToolCallID == "" || start.ToolName == "" {
		slog.Warn("Read Copilot tool start", "agent_id", a.agentID, "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, SpanInfo{})
		return
	}
	child := a.childForEvent(event)
	tool := &copilotOpenTool{
		child: child, name: start.ToolName,
		arguments:  append(json.RawMessage(nil), start.Arguments...),
		spawns:     start.ToolName == contracts.CopilotToolTask,
		startFrame: append([]byte(nil), raw...),
		order:      a.nextNativeToolOrder,
	}
	a.nextNativeToolOrder++
	if a.openTools == nil {
		a.openTools = make(map[string]*copilotOpenTool)
	}
	a.openTools[start.ToolCallID] = tool
	content := MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	if err := openToolSpan(a.sinkFor(child), content, start.ToolCallID, start.ToolName, tool.spawns); err != nil {
		slog.Error("Persist Copilot tool call", "agent_id", a.agentID, "tool_call_id", start.ToolCallID, "error", err)
	}
}

// completeNativeTool closes the span that startNativeTool opened.
//
// A completion whose start this process never saw still reaches its transcript:
// the span type is absent, so the row carries the tool-call ID alone and the
// renderer shows the result without a request.
func (a *copilotAgent) completeNativeTool(raw []byte, event copilotEvent) {
	var complete struct {
		ToolCallID string `json:"toolCallId"`
		Success    *bool  `json:"success"`
	}
	if err := json.Unmarshal(event.Data, &complete); err != nil || complete.ToolCallID == "" {
		slog.Warn("Read Copilot tool completion", "agent_id", a.agentID, "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, SpanInfo{})
		return
	}
	tool := a.openTools[complete.ToolCallID]
	delete(a.openTools, complete.ToolCallID)
	child := a.childForEvent(event)
	spanType := ""
	spawns := false
	if tool != nil {
		child, spanType, spawns = tool.child, tool.name, tool.spawns
	}
	sink := a.sinkFor(child)
	content := MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	if complete.Success != nil && !*complete.Success {
		content.Completion = MessageCompletionError
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{
		SpanID: complete.ToolCallID, SpanType: spanType, Closing: true,
	}); err != nil {
		slog.Error("Persist Copilot tool result", "agent_id", a.agentID, "tool_call_id", complete.ToolCallID, "error", err)
	}
	if !spawns {
		sink.CloseSpan(complete.ToolCallID)
	}
}

// startNativeSubagent opens the child transcript for one spawned subagent.
//
// The spawning tool call states the owner, so a nested subagent reaches the
// transcript of the subagent that spawned it rather than the root.
func (a *copilotAgent) startNativeSubagent(raw []byte, event copilotEvent) {
	var started copilotSubagentEvent
	if err := json.Unmarshal(event.Data, &started); err != nil || started.ToolCallID == "" || event.AgentID == "" {
		slog.Warn("Read Copilot subagent start", "agent_id", a.agentID, "error", err)
		a.persistNativeFrame(raw, SpanInfo{})
		return
	}
	if existing := a.children[event.AgentID]; existing != nil {
		// A repeated start for a subagent that already runs. Opening a second
		// transcript would strand the first one's registry row as Running forever.
		a.persistNativeFrameTo(existing.owner, raw, SpanInfo{})
		return
	}
	tool := a.openTools[started.ToolCallID]
	owner := a.sink
	ownerAgentID := a.agentID
	if tool != nil && tool.child != nil {
		owner, ownerAgentID = tool.child.sink, tool.child.workerAgentID
	}
	var input copilotTaskInput
	if tool != nil {
		if err := json.Unmarshal(tool.arguments, &input); err != nil {
			slog.Debug("Decode Copilot subagent arguments", "agent_id", a.agentID, "tool_call_id", started.ToolCallID, "error", err)
		}
	}
	title := firstNonEmpty(input.Name, started.AgentDisplayName, started.AgentName, input.Description, started.AgentDescription)
	workerAgentID, err := owner.EnsureChildAgent(started.ToolCallID, event.AgentID, title)
	if err != nil {
		slog.Error("Open Copilot subagent transcript", "agent_id", a.agentID, "tool_call_id", started.ToolCallID, "error", err)
		a.persistNativeFrameTo(owner, raw, SpanInfo{})
		return
	}
	child := &copilotNativeChild{
		sink: owner.ChildSink(workerAgentID), owner: owner, ownerAgentID: ownerAgentID,
		workerAgentID: workerAgentID, nativeAgentID: event.AgentID, spawnToolCallID: started.ToolCallID,
	}
	if a.children == nil {
		a.children = make(map[string]*copilotNativeChild)
	}
	a.children[event.AgentID] = child
	if err := owner.PersistChildPrompt(workerAgentID, input.Prompt); err != nil {
		slog.Warn("Persist Copilot subagent prompt", "agent_id", a.agentID, "child_agent_id", workerAgentID, "error", err)
	}
	logRegistryRefusal("copilot", "upsert subagent", owner.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: event.AgentID, Kind: bgtask.KindSubagent, ChildAgentID: workerAgentID,
		ParentAgentID: ownerAgentID, Title: title, Status: bgtask.StatusRunning,
	}))
	a.persistNativeFrameTo(owner, raw, SpanInfo{})
}

// finishNativeSubagent closes one subagent's registry row and its transcript state.
func (a *copilotAgent) finishNativeSubagent(raw []byte, event copilotEvent) {
	var finished copilotSubagentEvent
	if err := json.Unmarshal(event.Data, &finished); err != nil {
		slog.Warn("Read Copilot subagent completion", "agent_id", a.agentID, "error", err)
	}
	child := a.children[event.AgentID]
	if child == nil {
		child = a.childForSpawnToolCall(finished.ToolCallID)
	}
	status := bgtask.StatusCompleted
	switch {
	case finished.Cancelled:
		status = bgtask.StatusStopped
	case event.Type == contracts.CopilotEventSubagentFailed:
		status = bgtask.StatusFailed
	}
	if child == nil {
		a.persistNativeFrame(raw, SpanInfo{})
		return
	}
	delete(a.children, child.nativeAgentID)
	a.persistNativeFrameTo(child.owner, raw, SpanInfo{})
	logRegistryRefusal("copilot", "close subagent", child.owner.CloseBackgroundTask(child.nativeAgentID, status))
	child.sink.ReportProgress(ResetProgress())
	child.sink.ResetSpans()
	child.owner.CleanupChildAgent(child.workerAgentID)
	for id, tool := range a.openTools {
		if tool.child == child {
			delete(a.openTools, id)
		}
	}
}

func (a *copilotAgent) childForSpawnToolCall(toolCallID string) *copilotNativeChild {
	if toolCallID == "" {
		return nil
	}
	for _, child := range a.children {
		if child.spawnToolCallID == toolCallID {
			return child
		}
	}
	return nil
}

// copilotResponseSizeScope names the counter for a stream that reports a size and no
// identity. The size is a RUNNING TOTAL for the whole response, so every report has
// to land on one scope: a scope for each event would add the totals instead of
// replacing one, and an eight-byte answer would count as thirty-six.
const copilotResponseSizeScope = "copilot:response"

// copilotAssistantText is every field an assistant text event has been seen to carry.
//
// The runtime does not spell them alike: Copilot 1.0.24 streams both its answer and
// its reasoning as `deltaContent`, and sends the finished message as `content`.
// Reading each known spelling means a build that fills a different one needs no
// change here.
type copilotAssistantText struct {
	Content      string `json:"content"`
	Text         string `json:"text"`
	Delta        string `json:"delta"`
	DeltaContent string `json:"deltaContent"`
	MessageID    string `json:"messageId"`
	ReasoningID  string `json:"reasoningId"`
	// TotalResponseSizeBytes is the running size of the answer, which is all the
	// answer's own stream carries today.
	TotalResponseSizeBytes int64 `json:"totalResponseSizeBytes"`
}

// body reports the text one assistant event carries, whichever field holds it.
func (t copilotAssistantText) body() string {
	return firstNonEmpty(t.DeltaContent, t.Delta, t.Content, t.Text)
}

// copilotAssistantTextKind reports which assembled segment one assistant event
// contributes to, and whether it is a streaming DELTA rather than the finished
// message.
//
// Copilot 1.0.24 streams the answer's text on `assistant.message_delta` and, in
// parallel, the answer's running SIZE on `assistant.streaming_delta`. Both belong to
// the answer, so both map to the same segment: whichever one a later build fills is
// the one that counts, and a third name costs one contract entry and one case.
func copilotAssistantTextKind(eventType string) (AssembledMessageKind, bool, bool) {
	switch eventType {
	case contracts.CopilotEventAssistantReasoningDelta:
		return AssembledMessageKindReasoning, true, true
	case contracts.CopilotEventAssistantReasoning:
		return AssembledMessageKindReasoning, false, true
	case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta:
		return AssembledMessageKindText, true, true
	case contracts.CopilotEventAssistantMessage:
		return AssembledMessageKindText, false, true
	}
	return "", false, false
}

// reportNativeTextProgress publishes the live counters for one assistant event, and
// accumulates whatever text that event streams.
//
// The scope is the message or reasoning identity, so a delta and the finished
// message that replaces it share one counter instead of adding twice. That identity
// is also what pairs the accumulated deltas with the finished message: the runtime
// sends the message only once it is COMPLETE, and the frame it sends then is the
// authoritative row, so the deltas it replaces are discarded rather than stored twice.
//
// A stream that reports a SIZE and no text moves the output counter instead, on one
// scope of its own -- it names no message, and its total is for the whole response.
func (a *copilotAgent) reportNativeTextProgress(sink ProviderServices, event copilotEvent) {
	kind, isDelta, ok := copilotAssistantTextKind(event.Type)
	if !ok {
		return
	}
	var text copilotAssistantText
	if json.Unmarshal(event.Data, &text) != nil {
		return
	}
	body := text.body()
	if body == "" && text.TotalResponseSizeBytes > 0 {
		sink.ReportProgress(OutputExactTotalProgress(copilotResponseSizeScope, text.TotalResponseSizeBytes))
		return
	}
	scope := firstNonEmpty(text.MessageID, text.ReasoningID, event.ID)
	if scope == "" {
		return
	}
	if body != "" {
		sink.ReportProgress(ModelTextProgress(scope, body))
	}
	if !isDelta {
		// The finished message is the row. Its own deltas are not a second one.
		a.dropStreamedNativeText(scope)
		return
	}
	a.appendStreamedNativeText(sink, scope, kind, body)
}

// appendStreamedNativeText accumulates one delta under its message identity.
//
// The caller holds outputMu, which guards nativeTextSinks as it guards every other
// map the dispatch touches.
func (a *copilotAgent) appendStreamedNativeText(sink ProviderServices, scope string, kind AssembledMessageKind, delta string) {
	if delta == "" {
		return
	}
	a.nativeText.Append(scope, kind, delta, joinVerbatim)
	for _, segment := range a.nativeTextSegments {
		if segment.scope == scope {
			return
		}
	}
	a.nativeTextSegments = append(a.nativeTextSegments, copilotStreamedText{scope: scope, sink: sink})
}

// dropStreamedNativeText forgets the deltas a finished message replaces.
func (a *copilotAgent) dropStreamedNativeText(scope string) {
	a.nativeText.Discard(scope)
	a.nativeTextSegments = slices.DeleteFunc(a.nativeTextSegments, func(segment copilotStreamedText) bool {
		return segment.scope == scope
	})
}

// closeStreamedNativeText stores every segment the runtime streamed and never
// finished, as LeapMux's own assembled message.
//
// The runtime sends no frame for such a segment -- it marks each delta ephemeral and
// emits the message only when the message is complete -- so this row is LeapMux's
// calculation over what the agent actually produced, which is what the assembled
// envelope is for. Nothing is invented: the text is the agent's own, and the
// completion column states that the segment did not finish.
func (a *copilotAgent) closeStreamedNativeText(completion MessageCompletion) {
	segments := a.nativeTextSegments
	a.nativeTextSegments = nil
	for _, segment := range segments {
		raw, ok, err := a.nativeText.Finish(segment.scope, completion)
		if err != nil {
			slog.Error("Assemble Copilot streamed text", "agent_id", a.agentID, "scope", segment.scope, "error", err)
			continue
		}
		if !ok {
			continue
		}
		sink := segment.sink
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{
			Original:       raw,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     completion,
		}, SpanInfo{}); err != nil {
			slog.Error("Persist Copilot streamed text", "agent_id", a.agentID, "error", err)
		}
	}
}

// reportNativeContextUsage publishes the live context counter.
func (a *copilotAgent) reportNativeContextUsage(data json.RawMessage) {
	var usage struct {
		CurrentTokens *int64 `json:"currentTokens"`
		TokenLimit    *int64 `json:"tokenLimit"`
	}
	if json.Unmarshal(data, &usage) != nil || usage.CurrentTokens == nil {
		return
	}
	fields := map[string]interface{}{contracts.ContextUsageFieldContextTokens: *usage.CurrentTokens}
	if usage.TokenLimit != nil && *usage.TokenLimit > 0 {
		fields[contracts.ContextUsageFieldContextWindow] = *usage.TokenLimit
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyContextUsage: fields})
}

// persistNativeFrameTo stores one native frame in the transcript that owns it.
func (a *copilotAgent) persistNativeFrameTo(sink ProviderServices, raw []byte, span SpanInfo) {
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}, span); err != nil {
		slog.Error("Persist Copilot event", "agent_id", a.agentID, "error", err)
	}
}

// clearNativeChildren ends every open subagent transcript.
//
// A session replacement and a process stop both reach it, so each child leaves
// its registry row stopped rather than running forever.
func (a *copilotAgent) clearNativeChildren() {
	// A session that goes away takes every unfinished segment with it, so the text
	// each transcript had already streamed is stored before its sink is dropped.
	a.closeStreamedNativeText(MessageCompletionInterrupted)
	a.closeOpenNativeTools()
	children := a.children
	a.children = nil
	for _, child := range children {
		logRegistryRefusal("copilot", "close subagent", child.owner.CloseBackgroundTask(child.nativeAgentID, bgtask.StatusStopped))
		child.sink.ReportProgress(ResetProgress())
		child.sink.ResetSpans()
		child.owner.CleanupChildAgent(child.workerAgentID)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return value
		}
	}
	return ""
}
