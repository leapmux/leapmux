package copilot

import (
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	// copilotMethodSessionEvent carries every frame the transcript reads.
	copilotMethodSessionEvent = contracts.CopilotMethodSessionEvent
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

// copilotEventIsRuntimeTrace reports whether an event type identifies a family that
// describes the RUNTIME rather than the work.
//
// The families come from contracts.CopilotEventPrefixKeys, which both sides iterate.
// The membership of that table crosses the boundary as much as the spellings do: the
// browser hides every family the table lists, so a worker that tested a SUBSET stored
// a row for each of the rest -- each one spending a message seq and a slot in the
// chat-history page budget for a row nobody ever sees.
//
// The table holds six families today. `model.` is the runtime's own model-call trace:
// the request, the response, a snapshot of the whole message list, and the call's
// start and end. Every part of it repeats what the `assistant.` and `tool.` events
// already deliver, so it is a second copy of the conversation rather than the
// conversation. One trivial turn sent 71 kilobytes of it, and the reader saw ten
// raw-JSON rows. `hook.` states that the runtime ran one of its own hooks; a hook that
// changes the work changes a tool call, and that call has its own rows. The other four
// -- `session.canvas.`, `factory.`, `assistant.fusion_` and `session.fusion_` -- are
// the runtime's experiments, which no renderer draws.
//
// `model.call_failure` is the one exception. LeapMux surfaces it as a notification,
// because a model call that failed is an answer the reader acts on.
func copilotEventIsRuntimeTrace(eventType string) bool {
	if eventType == contracts.CopilotEventModelCallFailure {
		return false
	}
	for _, prefix := range contracts.CopilotEventPrefixKeys {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}
	return false
}

// copilotStreamedText is one accumulating assistant segment and the transcript it
// belongs to. The identity is the runtime's own message or reasoning id.
type copilotStreamedText struct {
	scope string
	sink  agent.ProviderServices
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
	// sawOutput records that this call already produced real output, so a later
	// progress SENTENCE does not replace it. See reportNativeToolOutput.
	sawOutput bool
}

// copilotToolStart is the part of `tool.execution_start` that LeapMux reads.
type copilotToolStart struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// handleNativeEvent routes one decoded session event. It returns with the event
// persisted exactly once, in the transcript that owns it.
//
// The caller holds outputMu, which is also what guards every map below. Each
// branch that needs a round trip to the runtime starts a goroutine, because the
// response for such a request arrives on THIS goroutine and a direct call would
// wait for itself.
func (a *Agent) handleNativeEvent(raw []byte, event copilotEvent) {
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
		a.reportNativeContextUsage(sink, event.Data)
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
	case contracts.CopilotEventToolProgress, contracts.CopilotEventToolPartialResult:
		// The only text a running call reports. It was dropped UNREAD before, so the
		// row stayed blank for the whole call.
		//
		// The drop is LeapMux's own decision rather than the runtime's `ephemeral`
		// mark: the frame carries a window on output the finished row states in
		// full, so a transcript row for one would repeat the result in pieces. The
		// mark is set today, and a build that stopped setting it would otherwise
		// write a row per progress tick.
		a.reportNativeToolOutput(sink, event.Data)
		return
	}
	// `Ephemeral` is the runtime's own "do not store" mark. The trace test is
	// LeapMux's: the runtime stores its trace itself and marks none of it ephemeral.
	if event.Ephemeral || copilotEventIsRuntimeTrace(event.Type) {
		return
	}
	a.persistNativeFrameTo(sink, raw, agent.SpanInfo{})
}

// reportNativeToolOutput carries the live text of a running call to its own row.
//
// The two frames carry DIFFERENT things, and neither is cumulative: `partialOutput`
// is the output the tool produced, and `progressMessage` is the runtime's own
// sentence about what the tool does. Output outranks the sentence, because the
// runtime interleaves them -- a `Running the tests...` between two output frames
// replaced the lines the reader watched with one status line, and the next
// output frame put them back. The sentence is what a call that produced NOTHING yet
// has to show, so it shows until output arrives and never after.
//
// The caller holds outputMu, which guards openTools.
func (a *Agent) reportNativeToolOutput(sink agent.ProviderServices, data json.RawMessage) {
	var frame struct {
		ToolCallID      string `json:"toolCallId"`
		ProgressMessage string `json:"progressMessage"`
		PartialOutput   string `json:"partialOutput"`
	}
	if err := json.Unmarshal(data, &frame); err != nil || frame.ToolCallID == "" {
		return
	}
	tool := a.openTools[frame.ToolCallID]
	if frame.PartialOutput != "" {
		if tool != nil {
			tool.sawOutput = true
		}
		// A partial output is a WINDOW on more by definition.
		sink.ReportProgress(agent.OutputTailProgress(frame.ToolCallID, frame.PartialOutput, true))
		return
	}
	// A call whose start this process never saw has no record to read, so its
	// sentence still shows: it is the only thing there is.
	if frame.ProgressMessage == "" || (tool != nil && tool.sawOutput) {
		return
	}
	// A whole sentence the runtime wrote, so it states no loss.
	sink.ReportProgress(agent.OutputTailProgress(frame.ToolCallID, frame.ProgressMessage, false))
}

// copilotTurnWasAborted reports whether the reader stopped the turn that is ending.
// The runtime states it on the idle event, and the browser reads the same field.
func copilotTurnWasAborted(data json.RawMessage) bool {
	var idle struct {
		Aborted bool `json:"aborted"`
	}
	return json.Unmarshal(data, &idle) == nil && idle.Aborted
}

func (a *Agent) endNativeTurn(raw []byte, aborted bool) {
	if !a.setNativeTurnActive(false) {
		a.persistNativeFrame(raw, agent.SpanInfo{})
		return
	}
	// A segment the runtime never finished ends with the turn. It reads as interrupted
	// only when the turn was: a turn that ended by itself and left a segment open
	// produced all the text it was going to.
	completion := agent.MessageCompletionComplete
	if aborted {
		completion = agent.MessageCompletionInterrupted
	}
	a.closeStreamedNativeText(completion)
	a.closeOpenNativeTools()
	a.sink.ReportProgress(agent.ResetProgress())
	if err := a.sink.PersistTurnEnd(agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}, agent.SpanInfo{}); err != nil {
		slog.Error("Persist Copilot turn end", "agent_id", a.AgentID(), "error", err)
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
func (a *Agent) closeOpenNativeTools() {
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
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original:       tool.startFrame,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     agent.MessageCompletionInterrupted,
		}, agent.SpanInfo{SpanID: id, SpanType: tool.name, Closing: true}); err != nil {
			slog.Error("Persist Copilot interrupted tool", "agent_id", a.AgentID(), "tool_call_id", id, "error", err)
		}
		if !tool.spawns {
			sink.CloseSpan(id)
		}
	}
}

// startNativeTool opens the tool call's span in the transcript that owns it.
func (a *Agent) startNativeTool(raw []byte, event copilotEvent) {
	var start copilotToolStart
	if err := json.Unmarshal(event.Data, &start); err != nil || start.ToolCallID == "" || start.ToolName == "" {
		slog.Warn("Read Copilot tool start", "agent_id", a.AgentID(), "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, agent.SpanInfo{})
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
	content := agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	if err := providerkit.OpenToolSpan(a.sinkFor(child), content, start.ToolCallID, start.ToolName, tool.spawns); err != nil {
		slog.Error("Persist Copilot tool call", "agent_id", a.AgentID(), "tool_call_id", start.ToolCallID, "error", err)
	}
}

// completeNativeTool closes the span that startNativeTool opened.
//
// A completion whose start this process never saw still reaches its transcript:
// the span type is absent, so the row carries the tool-call ID alone and the
// renderer shows the result without a request.
func (a *Agent) completeNativeTool(raw []byte, event copilotEvent) {
	var complete struct {
		ToolCallID string `json:"toolCallId"`
		Success    *bool  `json:"success"`
	}
	if err := json.Unmarshal(event.Data, &complete); err != nil || complete.ToolCallID == "" {
		slog.Warn("Read Copilot tool completion", "agent_id", a.AgentID(), "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, agent.SpanInfo{})
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
	content := agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	if complete.Success != nil && !*complete.Success {
		content.Completion = agent.MessageCompletionError
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: complete.ToolCallID, SpanType: spanType, Closing: true,
	}); err != nil {
		slog.Error("Persist Copilot tool result", "agent_id", a.AgentID(), "tool_call_id", complete.ToolCallID, "error", err)
	}
	if !spawns {
		sink.CloseSpan(complete.ToolCallID)
	}
}

// copilotResponseSizeScope identifies the counter for a stream that reports a size and no
// identity. The size is a RUNNING TOTAL for the whole response, so every report has
// to land on one scope: a scope for each event would add the totals instead of
// replacing one, and an eight-byte answer would count as thirty-six.
const copilotResponseSizeScope = "copilot:response"

// copilotAssistantText holds every field that an assistant text event carries.
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
	return sessionstore.FirstNonBlank(t.DeltaContent, t.Delta, t.Content, t.Text)
}

// copilotAssistantTextKind reports which assembled segment one assistant event
// contributes to, and whether it is a streaming DELTA rather than the finished
// message.
//
// Copilot 1.0.24 streams the answer's text on `assistant.message_delta` and, in
// parallel, the answer's running SIZE on `assistant.streaming_delta`. Both belong to
// the answer, so both map to the same segment: whichever one a later build fills is
// the one that counts, and a third name costs one contract entry and one case.
func copilotAssistantTextKind(eventType string) (agent.AssembledMessageKind, bool, bool) {
	switch eventType {
	case contracts.CopilotEventAssistantReasoningDelta:
		return agent.AssembledMessageKindReasoning, true, true
	case contracts.CopilotEventAssistantReasoning:
		return agent.AssembledMessageKindReasoning, false, true
	case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta:
		return agent.AssembledMessageKindText, true, true
	case contracts.CopilotEventAssistantMessage:
		return agent.AssembledMessageKindText, false, true
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
// scope of its own -- it identifies no message, and its total is for the whole response.
func (a *Agent) reportNativeTextProgress(sink agent.ProviderServices, event copilotEvent) {
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
		sink.ReportProgress(agent.OutputExactTotalProgress(copilotResponseSizeScope, text.TotalResponseSizeBytes))
		return
	}
	// The scope is the runtime's own message or reasoning identity. The FRAME id is
	// not a substitute: it changes for every delta, so it would open one segment for
	// each delta, and the finished message would then discard none of them.
	scope := sessionstore.FirstNonBlank(text.MessageID, text.ReasoningID)
	if scope == "" {
		slog.Debug("Skip a Copilot assistant event that states no message identity",
			"agent_id", a.AgentID(), "event", event.Type, "frame_id", event.ID)
		return
	}
	if body != "" {
		sink.ReportProgress(agent.ModelTextProgress(scope, body))
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
// The caller holds outputMu, which guards nativeTextSegments as it guards every
// other field the dispatch touches.
func (a *Agent) appendStreamedNativeText(sink agent.ProviderServices, scope string, kind agent.AssembledMessageKind, delta string) {
	if delta == "" {
		return
	}
	a.nativeText.Append(scope, kind, delta, providerkit.JoinVerbatim)
	for _, segment := range a.nativeTextSegments {
		if segment.scope == scope {
			return
		}
	}
	a.nativeTextSegments = append(a.nativeTextSegments, copilotStreamedText{scope: scope, sink: sink})
}

// dropStreamedNativeText forgets the deltas a finished message replaces.
func (a *Agent) dropStreamedNativeText(scope string) {
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
func (a *Agent) closeStreamedNativeText(completion agent.MessageCompletion) {
	segments := a.nativeTextSegments
	a.nativeTextSegments = nil
	for _, segment := range segments {
		raw, ok, err := a.nativeText.Finish(segment.scope, completion)
		if err != nil {
			slog.Error("Assemble Copilot streamed text", "agent_id", a.AgentID(), "scope", segment.scope, "error", err)
			continue
		}
		if !ok {
			continue
		}
		sink := segment.sink
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original:       raw,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     completion,
		}, agent.SpanInfo{}); err != nil {
			slog.Error("Persist Copilot streamed text", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// reportNativeContextUsage publishes the live context counter in the transcript that
// owns the event. A subagent reports its own context, so the root sink is not the
// answer for every caller.
func (a *Agent) reportNativeContextUsage(sink agent.ProviderServices, data json.RawMessage) {
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
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyContextUsage: fields})
}

// persistNativeFrameTo stores one native frame in the transcript that owns it.
func (a *Agent) persistNativeFrameTo(sink agent.ProviderServices, raw []byte, span agent.SpanInfo) {
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}, span); err != nil {
		slog.Error("Persist Copilot event", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) HandleOutput(content []byte) {
	line := &providerkit.ParsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		a.persistNativeFrame(content, agent.SpanInfo{})
		return
	}
	a.handleNativeOutput(line)
}

func (a *Agent) handleNativeOutput(line *providerkit.ParsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if line.Method != copilotMethodSessionEvent {
		a.RefuseUnsupportedRequest(line)
		// An unrecognized method still reaches the transcript: a frame that carries
		// conversation is worse lost than shown as raw JSON.
		if !copilotMethodIsTelemetry(line.Method) {
			a.persistNativeFrame(line.Raw, agent.SpanInfo{})
		}
		return
	}
	event, err := decodeCopilotSessionEvent(line.Params, a.currentNativeSessionID())
	if err != nil {
		slog.Debug("Skip Copilot event with an invalid session", "error", err)
		return
	}
	a.handleNativeEvent(line.Raw, event)
}

func (a *Agent) persistNativeFrame(raw []byte, span agent.SpanInfo) {
	a.persistNativeFrameTo(a.sink, raw, span)
}
