package mimo

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// How MiMo's messages become transcript rows.
//
// A turn streams as message and part events. Each message names the actor that
// wrote it -- "main" or a subagent's actor id -- and each part names its
// message, so a part reaches the transcript of the actor behind its message:
// the main agent's own, or the child transcript of a subagent (subagent.go).
//
//   - A text or reasoning part streams as deltas and ends with one update that
//     holds the whole text. That final update becomes one assembled row. The
//     deltas only feed the live progress, and they are what a turn that ends
//     early persists instead.
//   - A tool part moves pending -> running -> completed or error. The first
//     running update opens the call's span with the event itself as the row, and
//     the final update closes the span with the event as the row. A call cut by
//     the turn end is closed with its last update.
//   - The user's own messages are the worker's rows already, and MiMo's copy of
//     them is not persisted. The same holds for the synthetic user messages MiMo
//     writes itself: a subagent's report, a goal reminder, a plan approval.

// mimoMessageRecord is what the worker knows about one message.
type mimoMessageRecord struct {
	role    string
	actorID string
	// summary marks the assistant message a compaction wrote.
	summary bool
	// completed marks a message that MiMo finished writing.
	completed bool
}

// Message roles.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// mimoTextPart is one streamed text or reasoning part.
type mimoTextPart struct {
	kind    agent.AssembledMessageKind
	actorID string
	// skip marks a part that no row carries: a synthetic or ignored text, the
	// text of a user message, or a compaction summary.
	skip bool
	// whole marks a part of a user message. MiMo writes a user message whole, so
	// its part is complete at its first update, and it states no time that could
	// say so (probe/mimo-code/actor.sse.jsonl).
	whole bool
}

// mimoToolCall is everything the worker knows about one tool call.
type mimoToolCall struct {
	name    string
	actorID string
	// opened marks a call whose opening row is persisted and whose span is open.
	opened bool
	// final marks a call that reached a final state. It outlives the close, so a
	// repeated final update does not close the call a second time.
	final bool
	// cut marks a call that a turn end closed before MiMo finished it. MiMo can
	// still send the call's own final update after the turn ends, so the record
	// outlives one more turn end.
	cut bool
	// lastFrame is the call's last update, byte for byte. A turn end that cuts
	// the call stores it, so the row is an event MiMo sent.
	lastFrame []byte
	order     uint64
}

// progressScope is the id of one part in the live progress counters.
func progressScope(partID string) string {
	return "mimo:" + partID
}

// --- routing ---

// ownsSession reports whether an event of sessionID belongs to this agent. An
// empty id states no session, and is taken as the agent's own.
//
// Every subagent runs inside the agent's own session, as an actor that the
// messages name, so one session is all the agent reads. A session the agent
// left, at a context clear, can still report the end of its last turn, and a
// peer actor of MiMo's experimental orchestrator runs in a child session; their
// events are not this agent's.
func (a *Agent) ownsSession(sessionID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.ownsSessionLocked(sessionID)
}

func (a *Agent) ownsSessionLocked(sessionID string) bool {
	if a.sessionID == "" {
		return false
	}
	return sessionID == "" || sessionID == a.sessionID
}

// messageRecord returns the record of a message, reading it from the server
// when the stream did not state it. A message whose record cannot be read is
// taken as the main agent's assistant message: the main transcript is where a
// row without a known owner does the least harm, and a lost row is worse.
func (a *Agent) messageRecord(sessionID, messageID string) mimoMessageRecord {
	a.Mu.Lock()
	if record := a.messages[messageID]; record != nil {
		defer a.Mu.Unlock()
		return *record
	}
	if sessionID == "" {
		sessionID = a.sessionID
	}
	a.Mu.Unlock()

	record := mimoMessageRecord{role: roleAssistant, actorID: mainActorID}
	if info, err := a.rpc.message(a.Context(), sessionID, messageID); err != nil {
		slog.Warn("mimo read an unannounced message", "agent_id", a.AgentID(), "message_id", messageID, "error", err)
	} else {
		record = recordFromInfo(info)
	}
	a.Mu.Lock()
	if existing := a.messages[messageID]; existing != nil {
		record = *existing
	} else {
		stored := record
		a.messages[messageID] = &stored
	}
	a.Mu.Unlock()
	return record
}

// recordFromInfo builds the record of a message. A message that names no actor
// is the main agent's: the first update of a user message omits the field.
func recordFromInfo(info mimoMessageInfo) mimoMessageRecord {
	actorID := info.AgentID
	if actorID == "" {
		actorID = mainActorID
	}
	return mimoMessageRecord{
		role:      info.Role,
		actorID:   actorID,
		summary:   info.isCompactionSummary(),
		completed: info.Time.Completed != 0,
	}
}

// sinkForActor returns the transcript an actor's rows go to.
func (a *Agent) sinkForActor(actorID string) agent.ProviderServices {
	if actorID == "" || actorID == mainActorID {
		return a.sink
	}
	if childID, ok := a.ensureActorTranscript(actorID); ok {
		return a.sink.ChildSink(childID)
	}
	// A subagent the worker cannot give a transcript still ran; its rows go to the
	// main transcript rather than nowhere.
	return a.sink
}

// bufferFor returns the streamed-text buffer of one actor.
func (a *Agent) bufferFor(actorID string) *providerkit.GenerationBuffer {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	buffer := a.buffers[actorID]
	if buffer == nil {
		buffer = &providerkit.GenerationBuffer{}
		a.buffers[actorID] = buffer
	}
	return buffer
}

// --- messages ---

func (a *Agent) handleMessageUpdated(event mimoEvent) {
	var payload mimoMessageEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo message.updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	info := payload.Info
	if info.ID == "" {
		return
	}
	sessionID := info.SessionID
	if sessionID == "" {
		sessionID = payload.SessionID
	}
	if !a.ownsSession(sessionID) {
		return
	}
	record := recordFromInfo(info)
	a.Mu.Lock()
	if existing := a.messages[info.ID]; existing != nil && info.AgentID == "" {
		// A later update states the agent id that the first update of a user
		// message omitted. An update without it keeps the one already known.
		record.actorID = existing.actorID
	}
	a.messages[info.ID] = &record
	a.Mu.Unlock()
	if info.Role == roleAssistant {
		a.recordMessageUsage(info, record.actorID)
		if info.Error != nil {
			a.attributeFailure(*info.Error, record.actorID)
		}
	}
}

// --- parts ---

func (a *Agent) handlePartUpdated(event mimoEvent) {
	var payload mimoPartEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo message.part.updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	part := payload.Part
	sessionID := part.SessionID
	if sessionID == "" {
		sessionID = payload.SessionID
	}
	if !a.ownsSession(sessionID) || part.ID == "" {
		return
	}
	switch part.Type {
	case partTypeText, partTypeReasoning:
		a.handleTextPart(sessionID, part)
	case contracts.MiMoPartTypeTool:
		a.handleToolPart(event, sessionID, part)
	case contracts.MiMoPartTypeCompaction:
		a.handleCompactionPart(event, sessionID, part)
	default:
		// step-start, step-finish, snapshot, patch, file, agent, subtask, retry and
		// checkpoint parts carry no conversation. A step's tokens reach the usage
		// readout through its message's own update.
	}
}

func (a *Agent) handleTextPart(sessionID string, part mimoPart) {
	a.Mu.Lock()
	state := a.parts[part.ID]
	a.Mu.Unlock()
	if state == nil {
		record := a.messageRecord(sessionID, part.MessageID)
		kind := agent.AssembledMessageKindText
		if part.Type == partTypeReasoning {
			kind = agent.AssembledMessageKindReasoning
		}
		state = &mimoTextPart{
			kind:    kind,
			actorID: record.actorID,
			skip:    part.Synthetic || part.Ignored || record.role != roleAssistant || record.summary,
			whole:   record.role == roleUser,
		}
		a.Mu.Lock()
		if existing := a.parts[part.ID]; existing != nil {
			state = existing
		} else {
			a.parts[part.ID] = state
		}
		a.Mu.Unlock()
	}
	// A streamed part is complete at the update that states its end. A part of a
	// user message is complete at once.
	if !part.ended() && !state.whole {
		return
	}
	a.Mu.Lock()
	delete(a.parts, part.ID)
	a.Mu.Unlock()
	if state.skip {
		a.openChildTranscript(sessionID, part, state)
		return
	}
	a.persistTextPart(part.ID, state, part.Text, agent.MessageCompletionComplete)
}

// openChildTranscript writes a subagent's first instruction as the opening row
// of its child transcript, for an actor that no spawn call started: a workflow's
// actor states its instruction only as its own first user message. The row holds
// the task alone, without the return-format instruction that MiMo appended to it
// (subagentInstruction), as a spawned actor's row does. The write is a no-op for
// a transcript that already holds a row, which covers every spawned actor, whose
// spawn prompt opened the transcript, and every later message of an actor.
func (a *Agent) openChildTranscript(sessionID string, part mimoPart, state *mimoTextPart) {
	if state.actorID == mainActorID || part.Type != partTypeText {
		return
	}
	instruction := subagentInstruction(part.Text)
	if instruction == "" {
		return
	}
	if a.messageRecord(sessionID, part.MessageID).role != roleUser {
		return
	}
	childID, ok := a.ensureActorTranscript(state.actorID)
	if !ok {
		return
	}
	if err := a.sink.PersistChildPrompt(childID, instruction); err != nil {
		slog.Warn("mimo persist subagent instruction", "agent_id", a.AgentID(), "actor_id", state.actorID, "error", err)
	}
}

// persistTextPart writes one finished text or reasoning part. The final update
// holds the whole text, so the streamed copy is dropped rather than persisted.
func (a *Agent) persistTextPart(partID string, state *mimoTextPart, text string, completion agent.MessageCompletion) {
	scope := progressScope(partID)
	a.bufferFor(state.actorID).Discard(scope)
	sink := a.sinkForActor(state.actorID)
	defer sink.ReportProgress(agent.CompleteModelProgress(scope))
	if strings.TrimSpace(text) == "" {
		return
	}
	raw, err := agent.MarshalAssembledMessage(state.kind, text, completion)
	if err != nil {
		slog.Error("mimo marshal assembled message", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("mimo persist text", "agent_id", a.AgentID(), "error", err)
		return
	}
	if state.kind == agent.AssembledMessageKindText && state.actorID != mainActorID {
		a.rememberActorText(state.actorID, text)
	}
}

func (a *Agent) handlePartDelta(event mimoEvent) {
	var delta mimoPartDelta
	if err := json.Unmarshal(event.Properties, &delta); err != nil {
		slog.Warn("mimo message.part.delta unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if delta.Field != partTypeText || delta.Delta == "" {
		return
	}
	if !a.ownsSession(delta.SessionID) {
		return
	}
	a.Mu.Lock()
	state := a.parts[delta.PartID]
	a.Mu.Unlock()
	// A delta whose part never announced itself cannot be typed, and its part's
	// final update carries the whole text anyway.
	if state == nil || state.skip {
		return
	}
	scope := progressScope(delta.PartID)
	a.bufferFor(state.actorID).Append(scope, state.kind, delta.Delta, providerkit.JoinVerbatim)
	a.sinkForActor(state.actorID).ReportProgress(agent.ModelTextProgress(scope, delta.Delta))
}

// flushActorText persists what an actor streamed and never finished, as rows
// that state how the turn ended.
func (a *Agent) flushActorText(actorID string, completion agent.MessageCompletion) {
	a.Mu.Lock()
	buffer := a.buffers[actorID]
	for id, state := range a.parts {
		if state.actorID == actorID {
			delete(a.parts, id)
		}
	}
	a.Mu.Unlock()
	if buffer == nil {
		return
	}
	// The sink is resolved at the first row, so a buffer that holds no text opens
	// no transcript and brings back no caches of a subagent that already ended.
	var sink agent.ProviderServices
	err := buffer.PersistAll(completion, func(raw []byte) error {
		if sink == nil {
			sink = a.sinkForActor(actorID)
		}
		return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	})
	if err != nil {
		slog.Error("mimo persist unfinished text", "agent_id", a.AgentID(), "actor_id", actorID, "error", err)
	}
}

// --- tools ---

// mimoToolMetadata is the part of a tool's `state.metadata` the worker reads.
type mimoToolMetadata struct {
	// Output is a running shell command's output so far.
	Output *string `json:"output"`
	// ActorID is the subagent that an actor call started or addressed.
	ActorID string `json:"actorId"`
	// Switched is true when a plan approval moved the session to build.
	Switched *bool `json:"switched"`
}

func toolMetadata(state *mimoToolState) mimoToolMetadata {
	var metadata mimoToolMetadata
	if state == nil || len(state.Metadata) == 0 {
		return metadata
	}
	if err := json.Unmarshal(state.Metadata, &metadata); err != nil {
		slog.Debug("mimo tool metadata unmarshal failed", "error", err)
	}
	return metadata
}

func (a *Agent) handleToolPart(event mimoEvent, sessionID string, part mimoPart) {
	if part.CallID == "" || part.State == nil {
		return
	}
	status := part.State.Status
	if status == contracts.MiMoToolStatusPending {
		// A pending call states no input yet, so it has nothing to show.
		return
	}
	record := a.messageRecord(sessionID, part.MessageID)
	a.Mu.Lock()
	call := a.tools[part.CallID]
	if call == nil {
		call = &mimoToolCall{name: part.Tool, actorID: record.actorID, order: a.nextToolOrder}
		a.nextToolOrder++
		a.tools[part.CallID] = call
	}
	if call.final {
		a.Mu.Unlock()
		return
	}
	call.lastFrame = event.raw
	first := !call.opened
	call.opened = true
	final := part.State.final()
	if final {
		call.final = true
		call.lastFrame = nil
		if a.turnActive {
			a.TurnToolUses++
		}
	}
	actorID := call.actorID
	a.Mu.Unlock()

	sink := a.sinkForActor(actorID)
	switch {
	case final:
		a.closeToolCall(sink, part, event.raw, first)
	case first:
		a.openToolCall(sink, part, event.raw)
	default:
		a.observeRunningTool(sink, part)
	}
}

// openToolCall persists a call's opening row and opens its span.
func (a *Agent) openToolCall(sink agent.ProviderServices, part mimoPart, raw []byte) {
	spawns := isSpawnCall(part)
	if spawns {
		if prompt := spawnPrompt(part.State.Input); prompt != "" {
			a.spawnPrompts.Remember(part.CallID, prompt)
		}
	}
	if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: raw}, part.CallID, part.Tool, spawns); err != nil {
		slog.Error("mimo persist tool call", "agent_id", a.AgentID(), "tool", part.Tool, "error", err)
	}
	if part.Tool == contracts.MiMoToolBash {
		a.ClearCumulativeOutput(part.CallID)
		sink.ReportProgress(agent.ResetOutputProgress(part.CallID))
	}
	a.observeRunningTool(sink, part)
}

// observeRunningTool reads what a running update adds: a shell command's
// output so far, and the subagent an actor call started.
func (a *Agent) observeRunningTool(sink agent.ProviderServices, part mimoPart) {
	metadata := toolMetadata(part.State)
	if part.Tool == contracts.MiMoToolActor && metadata.ActorID != "" && isSpawnCall(part) {
		a.linkSpawn(part.CallID, metadata.ActorID, spawnTitle(part))
	}
	if part.Tool == contracts.MiMoToolBash && metadata.Output != nil && *metadata.Output != "" {
		// The output is the WHOLE output so far, so the counter measures what it
		// adds rather than adding it twice, and its last bytes are the live tail.
		output := *metadata.Output
		observed := a.ObserveCumulativeOutput(part.CallID, output, false)
		sink.ReportProgress(agent.OutputTotalProgress(part.CallID, observed.Total, observed.Minimum))
		tail, clipped := agent.ClipTailBytes(output, mimoLiveOutputLimit)
		sink.ReportProgress(agent.OutputTailProgress(part.CallID, tail, clipped))
	}
}

// mimoLiveOutputLimit is the longest live output tail the worker broadcasts, in
// bytes. MiMo re-sends a running command's whole output on every update, and
// only its end reaches a reader; the service caps the broadcast again. Pi and
// Goose cap their own live tails the same way.
const mimoLiveOutputLimit = 8192

// closeToolCall persists a call's final row and closes its span.
//
// A call first seen in a final state opened no span, and its one row closes a
// span that nothing opened. That is the same row a call that opened normally
// ends with, so the browser draws both the same way.
func (a *Agent) closeToolCall(sink agent.ProviderServices, part mimoPart, raw []byte, first bool) {
	a.ClearCumulativeOutput(part.CallID)
	sink.ReportProgress(agent.CompleteOutputProgress(part.CallID))
	spawns := isSpawnCall(part)
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{
		SpanID: part.CallID, SpanType: part.Tool, Closing: true, NoSpan: first && spawns,
	}); err != nil {
		slog.Error("mimo persist tool result", "agent_id", a.AgentID(), "tool", part.Tool, "error", err)
	}
	sink.CloseSpan(part.CallID)

	metadata := toolMetadata(part.State)
	switch part.Tool {
	case contracts.MiMoToolActor:
		if spawns && metadata.ActorID != "" {
			a.linkSpawn(part.CallID, metadata.ActorID, spawnTitle(part))
		}
		a.spawnPrompts.Forget(part.CallID)
		if !spawns {
			a.recordParentMessage(part)
		}
	case contracts.MiMoToolPlanExit:
		if part.State.Status == contracts.MiMoToolStatusCompleted && metadata.Switched != nil && *metadata.Switched {
			a.adoptMode(contracts.MiMoModeBuild)
		}
	}
}

// closeUnfinishedTools closes every call of one actor that is still open, with
// its last update as the row and completion stating how the turn ended.
func (a *Agent) closeUnfinishedTools(actorID string, completion agent.MessageCompletion) {
	type pending struct {
		callID string
		call   mimoToolCall
	}
	a.Mu.Lock()
	var open []pending
	for callID, call := range a.tools {
		if call.actorID != actorID || !call.opened || call.final || len(call.lastFrame) == 0 {
			continue
		}
		open = append(open, pending{callID: callID, call: *call})
		call.final = true
		call.cut = true
		call.lastFrame = nil
	}
	a.Mu.Unlock()
	sort.Slice(open, func(i, j int) bool { return open[i].call.order < open[j].call.order })
	if len(open) == 0 {
		return
	}
	sink := a.sinkForActor(actorID)
	for _, entry := range open {
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: entry.call.lastFrame, Completion: completion},
			agent.SpanInfo{SpanID: entry.callID, SpanType: entry.call.name, Closing: true}); err != nil {
			slog.Error("mimo persist unfinished tool", "agent_id", a.AgentID(), "call_id", entry.callID, "error", err)
		}
		sink.CloseSpan(entry.callID)
		a.ClearCumulativeOutput(entry.callID)
		sink.ReportProgress(agent.CompleteOutputProgress(entry.callID))
		a.spawnPrompts.Forget(entry.callID)
	}
}

// flushUnfinishedOutput persists what every actor streamed and never finished,
// and closes every tool call that is still open, with completion. It runs when
// no event can end the actors' turns any more: at a stop, at a process exit, and
// for a session that the agent leaves.
//
// The main agent goes first, then each subagent in a stable order. Only an
// actor with a buffer or a call can hold unfinished output, and the flush of an
// actor that holds none writes nothing and opens no transcript.
func (a *Agent) flushUnfinishedOutput(completion agent.MessageCompletion) {
	a.Mu.Lock()
	holders := map[string]struct{}{mainActorID: {}}
	for actorID := range a.buffers {
		holders[actorID] = struct{}{}
	}
	for _, call := range a.tools {
		holders[call.actorID] = struct{}{}
	}
	a.Mu.Unlock()
	actorIDs := slices.Collect(maps.Keys(holders))
	slices.SortFunc(actorIDs, func(x, y string) int {
		switch {
		case x == y:
			return 0
		case x == mainActorID:
			return -1
		case y == mainActorID:
			return 1
		default:
			return strings.Compare(x, y)
		}
	})
	for _, actorID := range actorIDs {
		a.flushActorText(actorID, completion)
		a.closeUnfinishedTools(actorID, completion)
	}
}

// --- compaction ---

// Compaction phases the worker records for each compaction part.
const (
	compactionStarted = "start"
	compactionEnded   = "end"
)

// handleCompactionPart records a compaction. The part arrives twice: once when
// the compaction starts, with no projection, and once when it ends, with the
// summary in its projection. Each becomes a notification in the transcript of
// the actor whose context it compacts, and the thread folds the start into the
// end (see mimoProvider.Classify).
func (a *Agent) handleCompactionPart(event mimoEvent, sessionID string, part mimoPart) {
	phase := compactionStarted
	if compactionEndedIn(part) {
		phase = compactionEnded
	}
	actorID := a.messageRecord(sessionID, part.MessageID).actorID
	a.Mu.Lock()
	previous := a.compactions[part.ID]
	if previous == phase || previous == compactionEnded {
		a.Mu.Unlock()
		return
	}
	a.compactions[part.ID] = phase
	var ack chan struct{}
	if actorID == mainActorID {
		// CompactContext compacts the main agent, so only its compaction confirms
		// the request.
		ack = a.compactionAck
		a.compactionAck = nil
	}
	a.Mu.Unlock()
	if ack != nil {
		close(ack)
	}
	if _, err := a.sinkForActor(actorID).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
		slog.Error("mimo persist compaction", "agent_id", a.AgentID(), "error", err)
	}
}

// compactionEndedIn reports whether a compaction part states the end of its
// compaction: the part that ends it carries the summary in its projection.
func compactionEndedIn(part mimoPart) bool {
	return len(part.Projection) > 0 && string(part.Projection) != "null"
}

// --- turn lifecycle ---

func (a *Agent) handleSessionStatus(event mimoEvent) {
	var payload mimoStatusEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo session.status unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	own := payload.SessionID != "" && payload.SessionID == a.sessionID
	a.Mu.Unlock()
	// A session the agent left can still report the end of its last turn, and
	// only the agent's own session moves the turn.
	if !own {
		return
	}
	switch payload.Status.Type {
	case contracts.MiMoStatusTypeBusy:
		a.beginTurn()
	case contracts.MiMoStatusTypeRetry:
		// A retry is still the same turn: the agent waits and tries again.
		a.beginTurn()
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.raw); err != nil {
			slog.Error("mimo persist retry", "agent_id", a.AgentID(), "error", err)
		}
	case contracts.MiMoStatusTypeIdle:
		a.endTurn(event.raw)
	default:
		slog.Debug("mimo unknown session status", "agent_id", a.AgentID(), "status", payload.Status.Type)
	}
}

// beginTurn arms a turn. MiMo reports busy many times within one turn, so a
// repeat changes nothing.
func (a *Agent) beginTurn() {
	a.Mu.Lock()
	if a.turnActive {
		a.Mu.Unlock()
		return
	}
	// A failure held while no turn ran belongs to no subagent: a subagent's
	// failed message would have claimed it by now. It is reported before the new
	// turn, where it happened.
	held := a.unattributed
	a.unattributed = nil
	a.turnActive = true
	a.interruptRequested = false
	a.turnFailure = nil
	a.lastTurnFailed = false
	a.TurnToolUses = 0
	a.Mu.Unlock()
	if held != nil {
		a.persistFailure(held.raw)
	}
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetModelProgress())
}

// endTurn closes the running turn: it persists what the turn left unfinished,
// then the turn-end divider, then clears the flag.
//
// The divider is the event that ended the turn: the failure when one failed it,
// else the idle status. The divider is persisted BEFORE the clear, because the
// clear is the settle edge that spends the turn's tool count
// (see agent.TranscriptServices.PersistTurnEnd).
//
// An idle with no turn armed persists nothing and republishes the idle turn.
// The server's word is the state, and the worker can hold a turn that this
// agent never armed: the queue counts a prompt as a started turn when the
// server accepts it.
func (a *Agent) endTurn(idle []byte) {
	a.Mu.Lock()
	if !a.turnActive {
		a.Mu.Unlock()
		a.PublishTurnActive()
		return
	}
	if a.turnFailure == nil && a.unattributed != nil {
		// No message claimed the failure. A failure of the prompt itself, such as
		// an unknown model, fails no message, and it is the main agent's.
		a.turnFailure = a.unattributed.raw
	}
	a.unattributed = nil
	completion := agent.MessageCompletionComplete
	divider := idle
	switch {
	case a.interruptRequested:
		completion = agent.MessageCompletionInterrupted
	case a.turnFailure != nil:
		completion = agent.MessageCompletionError
		divider = a.turnFailure
	}
	failed := a.turnFailure != nil && !a.interruptRequested
	a.Mu.Unlock()

	// Each call that this closes clears its own output count. A subagent's call
	// can still run, so the counts are not reset as a whole.
	a.flushActorText(mainActorID, completion)
	a.closeUnfinishedTools(mainActorID, completion)
	a.sink.ReportProgress(agent.ResetModelProgress())
	content := a.turnEndContent(divider, completion)
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Error("mimo persist turn end", "agent_id", a.AgentID(), "error", err)
	}
	a.sink.ResetSpans()

	a.Mu.Lock()
	a.turnActive = false
	a.interruptRequested = false
	a.turnFailure = nil
	a.lastTurnFailed = failed
	a.pruneFinishedLocked()
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.retireMainControls()
}

// pruneFinishedLocked drops the bookkeeping that a finished turn no longer
// needs: finished calls and completed messages. A subagent that still runs keeps
// its open calls and its messages, and a call that this turn end cut keeps its
// record until the next one, so its late final update closes nothing twice. The
// caller holds a.Mu.
func (a *Agent) pruneFinishedLocked() {
	for callID, call := range a.tools {
		if !call.final {
			continue
		}
		if call.cut {
			call.cut = false
			continue
		}
		delete(a.tools, callID)
	}
	for messageID, record := range a.messages {
		if record.completed {
			delete(a.messages, messageID)
		}
	}
	clear(a.compactions)
}

// turnEndContent is the divider row: the event that ended the turn, with the
// worker's own tool count and usage readout as metadata.
func (a *Agent) turnEndContent(raw []byte, completion agent.MessageCompletion) agent.MessageContent {
	content := a.MessageWithToolUses(raw)
	content.Completion = completion
	usage := a.usageSnapshot()
	if len(usage.contextUsage) == 0 && !usage.hasCost {
		return content
	}
	fields := map[string]json.RawMessage{}
	if len(content.Metadata) > 0 {
		if err := json.Unmarshal(content.Metadata, &fields); err != nil {
			slog.Warn("mimo decode turn metadata", "error", err)
			return content
		}
	}
	if len(usage.contextUsage) > 0 {
		if encoded, err := json.Marshal(usage.contextUsage); err == nil {
			fields[contracts.SessionInfoKeyContextUsage] = encoded
		}
	}
	if usage.hasCost {
		if encoded, err := json.Marshal(usage.costUSD); err == nil {
			fields[contracts.SessionInfoKeyTotalCostUsd] = encoded
		}
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("mimo encode turn metadata", "error", err)
		return content
	}
	content.Metadata = metadata
	return content
}

// --- errors ---

// mimoFailure is one session.error that the worker holds until a message
// claims it.
type mimoFailure struct {
	raw []byte
	err mimoError
}

// claimedBy reports whether a failed message states this failure.
func (f *mimoFailure) claimedBy(err mimoError) bool {
	return f != nil && f.err.Name == err.Name && f.err.Data.Message == err.Data.Message
}

// handleSessionError records a failure.
//
// A subagent runs in the agent's own session, so its failure arrives as the
// session's own, and the message that failed follows it and states the actor.
// A failure inside a turn is therefore held until a message claims it: the
// main agent's failure becomes the turn's divider, which states the reason,
// and a subagent's is dropped, because its own transcript states it
// (endActorTurn). A failure that no message claims by the turn end is the main
// agent's.
//
// Outside a turn, a failure is persisted on its own: a prompt the server
// refused before it started a turn reports itself only this way. It is held
// while a subagent runs, for the same reason as above. MiMo also repeats the
// failure of a turn after the turn ends, and a repeat that follows a failed
// turn with no input between them is dropped.
func (a *Agent) handleSessionError(event mimoEvent) {
	var payload mimoErrorEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo session.error unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !a.ownsSession(payload.SessionID) {
		return
	}
	switch payload.Error.Name {
	case errorNameAborted:
		a.Mu.Lock()
		if a.turnActive {
			a.interruptRequested = true
		}
		a.Mu.Unlock()
		return
	case errorNameContextOverflow:
		slog.Debug("mimo context overflow; MiMo compacts and continues", "agent_id", a.AgentID())
		return
	}
	failure := &mimoFailure{raw: event.raw, err: payload.Error}
	a.Mu.Lock()
	switch {
	case a.turnActive:
		if a.turnFailure == nil {
			a.unattributed = failure
		}
		a.Mu.Unlock()
		return
	case a.lastTurnFailed:
		a.Mu.Unlock()
		slog.Debug("mimo repeated turn failure", "agent_id", a.AgentID(), "error", payload.Error.Name)
		return
	case a.runningActorsLocked() > 0:
		a.unattributed = failure
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	a.persistFailure(event.raw)
}

// attributeFailure ties a held failure to the actor whose message failed with
// it.
func (a *Agent) attributeFailure(err mimoError, actorID string) {
	a.Mu.Lock()
	failure := a.unattributed
	if !failure.claimedBy(err) {
		a.Mu.Unlock()
		return
	}
	a.unattributed = nil
	if actorID != mainActorID {
		// A subagent's failure: its transcript states it when its turn ends.
		a.Mu.Unlock()
		return
	}
	if a.turnActive {
		a.turnFailure = failure.raw
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	a.persistFailure(failure.raw)
}

// flushHeldFailure persists a failure held outside a turn once no subagent
// runs that could still claim it.
func (a *Agent) flushHeldFailure() {
	a.Mu.Lock()
	held := a.unattributed
	if held == nil || a.turnActive || a.runningActorsLocked() > 0 {
		a.Mu.Unlock()
		return
	}
	a.unattributed = nil
	a.Mu.Unlock()
	a.persistFailure(held.raw)
}

// persistFailure writes a failure outside a turn as a notification. It then
// republishes the idle turn, because the queue counted a refused prompt as a
// started turn.
func (a *Agent) persistFailure(raw []byte) {
	a.persistFailureRow(raw)
	a.PublishTurnActive()
}

// persistFailureRow writes a failure as a notification row.
func (a *Agent) persistFailureRow(raw []byte) {
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
		slog.Error("mimo persist error", "agent_id", a.AgentID(), "error", err)
	}
}

// takeUnreportedFailureLocked removes and returns the failure that no turn end
// persisted: the running turn's own failure, else a failure that no message
// claimed. It returns nil when the agent holds neither. A caller that ends what
// no event can end any more -- a stop, a process exit, a context clear --
// persists the result, because nothing else will state that failure. The caller
// holds a.Mu.
func (a *Agent) takeUnreportedFailureLocked() []byte {
	held := a.turnFailure
	if held == nil && a.unattributed != nil {
		held = a.unattributed.raw
	}
	a.turnFailure = nil
	a.unattributed = nil
	return held
}

// --- usage ---

// mimoUsage is the usage readout the worker keeps and broadcasts.
type mimoUsage struct {
	// costs holds the latest cost of each assistant message, since an update
	// restates it. Their sum is the session's cost.
	costs        map[string]float64
	contextUsage map[string]any
}

type mimoUsageSnapshot struct {
	contextUsage map[string]any
	costUSD      float64
	hasCost      bool
}

func (a *Agent) usageSnapshot() mimoUsageSnapshot {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return mimoUsageSnapshot{
		contextUsage: maps.Clone(a.usage.contextUsage),
		costUSD:      sumCosts(a.usage.costs),
		hasCost:      len(a.usage.costs) > 0,
	}
}

func sumCosts(costs map[string]float64) float64 {
	total := 0.0
	for _, cost := range costs {
		total += cost
	}
	return total
}

// recordMessageUsage folds one assistant message into the readout. The
// context readout follows the main agent alone, because a subagent's context is
// its own; the cost counts every actor, because every actor's calls are billed
// to the session.
func (a *Agent) recordMessageUsage(info mimoMessageInfo, actorID string) {
	broadcast := map[string]any{}
	a.Mu.Lock()
	if info.Cost > 0 {
		if a.usage.costs == nil {
			a.usage.costs = map[string]float64{}
		}
		if a.usage.costs[info.ID] != info.Cost {
			a.usage.costs[info.ID] = info.Cost
			broadcast[contracts.SessionInfoKeyTotalCostUsd] = sumCosts(a.usage.costs)
		}
	}
	if actorID == mainActorID && info.Tokens != nil {
		tokens := *info.Tokens
		used := tokens.Input + tokens.Cache.Read + tokens.Cache.Write
		if used > 0 {
			usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
				Input:      tokens.Input,
				CacheWrite: tokens.Cache.Write,
				CacheRead:  tokens.Cache.Read,
				Output:     tokens.Output,
			})
			usage[contracts.ContextUsageFieldContextTokens] = used
			if window := a.catalog.contextWindow(joinModelID(mimoModelRef{ProviderID: info.ProviderID, ModelID: info.ModelID})); window > 0 {
				usage[contracts.ContextUsageFieldContextWindow] = window
			}
			if !maps.Equal(usage, a.usage.contextUsage) {
				a.usage.contextUsage = usage
				broadcast[contracts.SessionInfoKeyContextUsage] = maps.Clone(usage)
			}
		}
	}
	a.Mu.Unlock()
	if len(broadcast) > 0 {
		a.sink.BroadcastSessionInfo(broadcast)
	}
}
