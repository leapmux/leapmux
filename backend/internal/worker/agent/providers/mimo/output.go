package mimo

import (
	"encoding/json"
	"fmt"
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

// How MiMo messages become transcript rows.
//
// A turn streams as message and part events. Each message identifies its actor:
// "main" or a subagent's actor ID. Each part identifies its message. Thus each
// part reaches the main transcript or its subagent's child transcript.
//
//   - A text or reasoning part streams as deltas. Its final update holds the
//     whole text and becomes one assembled row. Deltas supply live progress.
//     An early turn end persists the retained deltas instead.
//   - A tool part moves pending -> running -> completed or error. The first
//     running update opens the call's span. The final update closes its span.
//     Both rows retain the original native event. An early turn end closes the
//     call with its last native update.
//   - The user's own messages are the worker's rows already, and MiMo's copy of
//     them is not persisted. The same holds for the synthetic user messages MiMo
//     writes itself: a subagent's report, a goal reminder, a plan approval.

// mimoMessageRecord keeps one native message's identity through its session.
// Persistence releases its observations. Session closure removes finished identity records.
type mimoMessageRecord struct {
	role    string
	actorID string
	// identityKnown separates native identity from the failed-read fallback.
	identityKnown bool
	// actorKnown distinguishes an omitted user actor from an explicit main actor.
	actorKnown bool
	sessionID  string
	// actor keeps the original child identity after its session ends.
	actor *mimoActor
	// turnEpoch keeps an old message's update outside a replacement turn.
	turnEpoch uint64
	// summary marks the assistant message a compaction wrote.
	summary bool
	// completed marks a finished or failed message. MiMo can omit the completed
	// time when an error ends the message.
	completed bool
	// errorName is the native outcome of this message.
	errorName string
	// attributionClosed marks a record that can no longer receive later actor
	// or identity metadata: flushUnfinishedOutput closed it when native events
	// could no longer finish its output, so a pending opening or unresolved
	// stream must not keep waiting for an attribution that cannot arrive.
	attributionClosed bool
	// streamFailure records the first accepted stream failure, independent of GET metadata.
	streamFailure *mimoError
	// pendingParts keeps native observations once, in their original order.
	pendingParts []*mimoPendingPart
}

type mimoPendingPartKind uint8

const (
	mimoPendingText mimoPendingPartKind = iota
	mimoPendingStreamedText
	mimoPendingToolOpening
	mimoPendingToolClosing
	mimoPendingUserPrompt
	mimoPendingActorFailure
	mimoPendingSpawnPrompt
)

type mimoPendingPart struct {
	order  uint64
	kind   mimoPendingPartKind
	partID string
	// nativeFrame points at the original immutable tool event. Text stays in parts.
	nativeFrame []byte
	// actorFailure keeps the original native status until its error row persists.
	actorFailure *mimoActorStatus
	// completion keeps a cut tool's resolved outcome across retries.
	completion agent.MessageCompletion
	first      bool
}

// Message roles.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// mimoTextPart is one streamed text or reasoning part.
type mimoTextPart struct {
	kind      agent.AssembledMessageKind
	actorID   string
	messageID string
	// final replaces the streamed copy when native completion needs attribution.
	final *mimoPart
	// unresolved keeps an unended native update until its message role is known.
	unresolved      *mimoPart
	unresolvedOrder uint64
	// completion freezes the resolved outcome across persistence failures.
	completion agent.MessageCompletion
	// buffer keeps the original streamed scope after its session ends.
	buffer         *providerkit.GenerationBuffer
	streamOrder    *uint64
	progressTarget *mimoTranscriptTarget
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
	name      string
	actorID   string
	callID    string
	messageID string
	sessionID string
	// opened marks a call whose opening update the worker observed.
	// Its opening row can still wait for attribution or a persistence retry.
	opened bool
	// final marks a call that reached a final state. It outlives the close, so a
	// repeated final update does not close the call a second time.
	final bool
	// countEligible marks a native final or an interrupted running call.
	countEligible bool
	// counted records the one root count for this native part.
	counted bool
	// lastFrame is the call's last update, byte for byte. A turn end that cuts
	// the call stores it, so the row is an event MiMo sent.
	lastFrame []byte
	order     uint64
	// openingTarget owns a successfully persisted opening and its closing rail.
	openingTarget  *mimoTranscriptTarget
	progressTarget *mimoTranscriptTarget
}

// mimoTranscriptTarget captures the transcript that received one native observation.
type mimoTranscriptTarget struct {
	sink    agent.ProviderServices
	childID string
}

// mimoRootProgressSink keeps live child fallback scopes through root transcript resets.
// PersistScope holds its buffer mutex across the sink write. Reading buffer text
// in this handler would deadlock that write. Existing part ownership supplies the IDs.
type mimoRootProgressSink struct {
	agent.ProviderServices
	owner *Agent
}

func (s mimoRootProgressSink) ReportProgress(update agent.ProgressUpdate) {
	if update.Operation == agent.ProgressModelReset && update.ScopeID == "" {
		preserved := slices.Clone(update.PreserveModelScopes)
		s.owner.Mu.Lock()
		for partID, state := range s.owner.parts {
			if state.actorID != "" && state.actorID != mainActorID && !state.skip && state.progressTarget != nil && state.progressTarget.childID == "" {
				preserved = append(preserved, progressScope(partID))
			}
		}
		s.owner.Mu.Unlock()
		update.PreserveModelScopes = preserved
	}
	s.ProviderServices.ReportProgress(update)
}

// progressScope is the id of one part in the live progress counters.
func progressScope(partID string) string {
	return "mimo:" + partID
}

// --- routing ---

// ownsSession reports whether the event belongs to this agent.
// An empty session ID means the current session.
//
// Every subagent runs inside the agent's own session. Each message identifies
// its actor, so the agent reads one session. A session that the agent
// left can still report its final events after context clear.
// A peer actor in MiMo's experimental orchestrator uses a separate child session.
// This agent does not read either session's events.
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

// messageRecord returns the message record. It reads the native server when the
// stream did not supply the record. A failed read creates an unconfirmed
// assistant/main record. This fallback keeps output in the main transcript
// until native metadata identifies its owner.
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
		record.turnEpoch = a.turnEpoch
		if record.sessionID == "" {
			record.sessionID = sessionID
		}
		if record.actorID != mainActorID {
			record.actor = a.actorLocked(record.actorID)
		}
		stored := record
		a.messages[messageID] = &stored
	}
	a.Mu.Unlock()
	return record
}

// recordFromInfo builds the native message record.
// A missing actor initially selects main. User messages can supply their actor later.
func recordFromInfo(info mimoMessageInfo) mimoMessageRecord {
	actorID := info.AgentID
	if actorID == "" {
		actorID = mainActorID
	}
	record := mimoMessageRecord{
		role:          info.Role,
		actorID:       actorID,
		identityKnown: info.Role == roleAssistant || info.Role == roleUser,
		actorKnown:    info.AgentID != "" || info.Role == roleAssistant,
		sessionID:     info.SessionID,
		summary:       info.isCompactionSummary(),
		completed:     info.Time.Completed != 0 || info.Error != nil && info.Error.Name != "",
	}
	if info.Error != nil {
		record.errorName = info.Error.Name
	}
	return record
}

// sinkForActor returns the transcript an actor's rows go to.
func (a *Agent) sinkForActor(actorID string) agent.ProviderServices {
	if actorID == "" || actorID == mainActorID {
		return a.sink
	}
	if childID, ok := a.ensureActorTranscript(actorID); ok {
		return a.sink.ChildSink(childID)
	}
	// The main transcript keeps output when the worker cannot create the child transcript.
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
	if info.ID == "" || (info.Role != roleUser && info.Role != roleAssistant) {
		return
	}
	if info.SessionID != "" && payload.SessionID != "" && info.SessionID != payload.SessionID {
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
	existing := a.messages[info.ID]
	resolvedSession := sessionID
	if resolvedSession == "" {
		resolvedSession = a.sessionID
	}
	if existing != nil && existing.sessionID != resolvedSession {
		a.Mu.Unlock()
		return
	}
	if existing != nil && existing.identityKnown && (existing.role != info.Role || existing.actorKnown && info.AgentID != "" && info.AgentID != existing.actorID) {
		a.Mu.Unlock()
		slog.Warn("mimo ignored a conflicting message identity", "agent_id", a.AgentID(), "message_id", info.ID)
		return
	}
	if existing != nil {
		record.turnEpoch = existing.turnEpoch
		record.pendingParts = existing.pendingParts
		record.streamFailure = existing.streamFailure
		record.attributionClosed = existing.attributionClosed
		record.actor = existing.actor
		record.sessionID = existing.sessionID
		if existing.completed && existing.identityKnown {
			record.completed = true
			record.errorName = existing.errorName
		}
		if info.AgentID == "" && existing.identityKnown {
			// An update without an actor keeps the actor that the first update states.
			record.actorID = existing.actorID
			record.actorKnown = existing.actorKnown
		}
		if existing.identityKnown && (existing.role != info.Role || existing.role == roleAssistant) {
			record.role = existing.role
			record.actorID = existing.actorID
		}
	} else {
		record.turnEpoch = a.turnEpoch
		if record.sessionID == "" {
			record.sessionID = a.sessionID
		}
		if record.actorID != mainActorID {
			record.actor = a.actorLocked(record.actorID)
		}
	}
	if record.actor == nil && record.actorID != mainActorID {
		record.actor = a.actorLocked(record.actorID)
	}
	identityResolved := existing != nil &&
		(!existing.identityKnown || existing.role == roleUser && !existing.actorKnown && record.actorKnown)
	if identityResolved {
		a.resolveMessageOwnerLocked(info.ID, &record)
	}
	if a.turnActive && existing != nil && record.identityKnown && !existing.completed && existing.turnEpoch == a.turnEpoch &&
		existing.role == roleAssistant &&
		info.Role == roleAssistant && record.actorID == mainActorID && info.Error != nil && info.Error.Name == contracts.MiMoErrorNameAborted {
		a.noteNativeAbortLocked()
	}
	a.messages[info.ID] = &record
	retireMain := record.actorID == mainActorID && record.actorKnown && (record.turnEpoch != a.turnEpoch || !a.turnActive)
	a.Mu.Unlock()

	if identityResolved {
		a.reconcileMessageProgress(info.ID)
	}
	if retireMain {
		a.retireControls(func(control *mimoControl) bool {
			return control.messageID == info.ID && control.sessionID == record.sessionID && control.actorKnown && control.actorID == mainActorID
		})
	}
	a.associateParentActor(info.ParentID, &record)
	if info.Role == roleAssistant {
		a.recordMessageUsage(info, record)
		if info.Error != nil {
			a.attributeFailure(*info.Error, &record)
		}
	}
	a.flushPendingMessage(info.ID, "")
}

// resolveMessageOwnerLocked updates derived owners and retains native observation order.
// The caller holds a.Mu. Native text stays in its existing part record.
func (a *Agent) resolveMessageOwnerLocked(messageID string, record *mimoMessageRecord) {
	type observedInstruction struct {
		partID string
		order  uint64
	}
	var instructions []observedInstruction
	for _, call := range a.tools {
		if call.messageID == messageID && call.sessionID == record.sessionID {
			call.actorID = record.actorID
			a.countMainToolLocked(call, record)
		}
	}
	for partID, part := range a.parts {
		if part.messageID != messageID {
			continue
		}
		part.actorID = record.actorID
		part.skip = part.skip || record.role != roleAssistant || record.summary
		part.whole = record.role == roleUser
		if part.unresolved != nil {
			if record.role == roleUser {
				part.final = part.unresolved
				instructions = append(instructions, observedInstruction{partID: partID, order: part.unresolvedOrder})
			}
			part.unresolved = nil
		}
	}
	sort.Slice(instructions, func(i, j int) bool { return instructions[i].order < instructions[j].order })
	for _, instruction := range instructions {
		record.pendingParts = append(record.pendingParts, &mimoPendingPart{kind: mimoPendingUserPrompt, partID: instruction.partID, order: instruction.order})
	}
	for _, control := range a.controls {
		if control.messageID == messageID && control.sessionID == record.sessionID {
			control.actorID = record.actorID
			control.actorKnown = record.actorKnown
		}
	}
	sort.SliceStable(record.pendingParts, func(i, j int) bool { return record.pendingParts[i].order < record.pendingParts[j].order })
}

// associateParentActor uses the native reply link to resolve a missing user actor.
func (a *Agent) associateParentActor(parentID string, reply *mimoMessageRecord) {
	if parentID == "" || reply.role != roleAssistant || !reply.identityKnown {
		return
	}
	a.Mu.Lock()
	parent := a.messages[parentID]
	if parent == nil || parent.identityKnown && parent.role != roleUser || parent.actorKnown || parent.sessionID != reply.sessionID {
		a.Mu.Unlock()
		return
	}
	parent.role, parent.identityKnown = roleUser, true
	parent.actorID, parent.actor, parent.actorKnown = reply.actorID, reply.actor, true
	a.resolveMessageOwnerLocked(parentID, parent)
	a.Mu.Unlock()
	a.reconcileMessageProgress(parentID)
	a.flushPendingMessage(parentID, "")
}

// appendPendingPartLocked retains one observation in its existing message record.
// The caller holds a.Mu. Native frame bytes remain immutable.
func (a *Agent) appendPendingPartLocked(record *mimoMessageRecord, part *mimoPendingPart) {
	part.order = a.pendingPartOrder
	a.pendingPartOrder++
	record.pendingParts = append(record.pendingParts, part)
}

func (a *Agent) messageTranscriptTarget(record *mimoMessageRecord) *mimoTranscriptTarget {
	if record.actor == nil {
		return &mimoTranscriptTarget{sink: a.sink}
	}
	childID, ok := a.ensureActorRecordTranscript(record.actor, record.sessionID)
	if !ok {
		// Resolve the original actor again on retry. A successful root fallback
		// preserves the output when the child transcript cannot be created.
		return &mimoTranscriptTarget{sink: a.sink}
	}
	return &mimoTranscriptTarget{sink: a.sink.ChildSink(childID), childID: childID}
}

// reconcileMessageProgress moves only the scopes whose native identity changed.
func (a *Agent) reconcileMessageProgress(messageID string) {
	a.Mu.Lock()
	record := a.messages[messageID]
	parts := make(map[string]*mimoTextPart)
	calls := make(map[string]*mimoToolCall)
	for id, part := range a.parts {
		if part.messageID == messageID {
			parts[id] = part
		}
	}
	for id, call := range a.tools {
		if call.messageID == messageID && call.sessionID == record.sessionID {
			calls[id] = call
		}
	}
	a.Mu.Unlock()
	var target *mimoTranscriptTarget
	resolveTarget := func() *mimoTranscriptTarget {
		if target == nil {
			target = a.messageTranscriptTarget(record)
		}
		return target
	}
	for id, part := range parts {
		if part.progressTarget != nil {
			owner := part.progressTarget
			if !part.skip {
				owner = resolveTarget()
			}
			a.reconcileTextProgress(id, part, owner)
		}
		if part.skip && part.buffer != nil {
			part.buffer.Discard(progressScope(id))
		}
	}
	for id, call := range calls {
		if call.progressTarget == nil {
			continue
		}
		owner := call.openingTarget
		if owner == nil {
			owner = resolveTarget()
		}
		a.reconcileToolProgress(id, owner)
	}
}

// reconcileTextProgress retires the prior scope before replaying its canonical text.
func (a *Agent) reconcileTextProgress(partID string, state *mimoTextPart, target *mimoTranscriptTarget) {
	if state == nil || state.progressTarget == nil {
		return
	}
	previous := state.progressTarget
	if !state.skip && previous.childID == target.childID {
		return
	}
	scope := progressScope(partID)
	previous.sink.ReportProgress(agent.ResetModelScopeProgress(scope))
	a.Mu.Lock()
	state.progressTarget = nil
	if !state.skip {
		state.progressTarget = target
	}
	a.Mu.Unlock()
	if state.skip {
		return
	}
	var text string
	if state.final != nil {
		text = state.final.Text
	} else if state.buffer != nil {
		_, text, _ = state.buffer.ScopeSnapshot(scope)
	}
	if text != "" {
		target.sink.ReportProgress(agent.ModelTextProgress(scope, text))
	}
}

func (a *Agent) completeTextProgress(partID string, state *mimoTextPart, fallback agent.ProviderServices) {
	if state.progressTarget != nil {
		fallback = state.progressTarget.sink
	}
	fallback.ReportProgress(agent.CompleteModelProgress(progressScope(partID)))
}

// reconcileToolProgress preserves a successful opening target or transfers an unbound scope.
func (a *Agent) reconcileToolProgress(partID string, target *mimoTranscriptTarget) {
	a.Mu.Lock()
	call := a.tools[partID]
	if call == nil {
		a.Mu.Unlock()
		return
	}
	previous := call.progressTarget
	call.progressTarget = target
	raw := call.lastFrame
	a.Mu.Unlock()
	if previous == nil || previous.childID == target.childID {
		return
	}
	previous.sink.ReportProgress(agent.ResetOutputProgress(partID))
	part, err := retainedToolPart(raw)
	if err != nil {
		slog.Error("mimo read retained tool progress", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !part.State.final() {
		a.observeToolOutput(target.sink, part, toolMetadata(part.State))
	}
}

// resetUnownedRootSpans keeps successful fallback openings while their native child runs.
func (a *Agent) resetUnownedRootSpans() {
	a.Mu.Lock()
	for _, call := range a.tools {
		if call.actorID != mainActorID && call.openingTarget != nil && call.openingTarget.childID == "" && call.lastFrame != nil {
			a.Mu.Unlock()
			return
		}
	}
	a.Mu.Unlock()
	a.sink.ResetSpans()
}

func retainedToolPart(raw []byte) (mimoPart, error) {
	var event struct {
		Type       string        `json:"type"`
		Properties mimoPartEvent `json:"properties"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return mimoPart{}, err
	}
	part := event.Properties.Part
	if event.Type != contracts.MiMoEventMessagePartUpdated || part.Type != contracts.MiMoPartTypeTool || part.ID == "" || part.CallID == "" || part.Tool == "" || part.State == nil {
		return mimoPart{}, fmt.Errorf("the retained MiMo tool event is invalid")
	}
	return part, nil
}

// pendingTranscriptTarget preserves the successful opening owner for its closing row.
func (a *Agent) pendingTranscriptTarget(record *mimoMessageRecord, pending *mimoPendingPart, state *mimoTextPart) *mimoTranscriptTarget {
	if pending.kind == mimoPendingToolClosing {
		a.Mu.Lock()
		call := a.tools[pending.partID]
		var target *mimoTranscriptTarget
		if call != nil {
			target = call.openingTarget
		}
		a.Mu.Unlock()
		if target != nil {
			return target
		}
	}
	if pending.kind == mimoPendingUserPrompt || state != nil && state.skip {
		return &mimoTranscriptTarget{sink: a.sink}
	}
	return a.messageTranscriptTarget(record)
}

// flushPendingMessage writes a ready message group in native observation order.
// A failed write keeps its part and every later observation in the same group.
func (a *Agent) flushPendingMessage(messageID string, fallback agent.MessageCompletion) bool {
	for {
		a.Mu.Lock()
		record := a.messages[messageID]
		if record == nil || len(record.pendingParts) == 0 {
			a.Mu.Unlock()
			return true
		}
		pending := record.pendingParts[0]
		state := a.parts[pending.partID]
		if (pending.kind == mimoPendingUserPrompt || state != nil && state.skip && record.role == roleUser) && !record.actorKnown && !record.attributionClosed {
			a.Mu.Unlock()
			return true
		}
		if (pending.kind == mimoPendingText || pending.kind == mimoPendingStreamedText) && state != nil && state.completion == "" {
			switch {
			case record.completed:
				state.completion = agent.MessageCompletionComplete
				if record.errorName == contracts.MiMoErrorNameAborted {
					state.completion = agent.MessageCompletionInterrupted
				}
			case fallback != "":
				state.completion = fallback
			default:
				a.Mu.Unlock()
				return true
			}
		}
		a.Mu.Unlock()
		target := a.pendingTranscriptTarget(record, pending, state)
		if target.childID != "" && record.role != roleUser && !a.flushChildInstructions(record, pending.order) {
			return false
		}
		if target.childID != "" && a.actorSpawnPromptPending(record.actor) {
			return false
		}
		sink := target.sink
		var err error
		var closedPartID string
		switch pending.kind {
		case mimoPendingText:
			a.reconcileTextProgress(pending.partID, state, target)
			if state != nil && state.skip {
				if record.role == roleUser && state.final != nil {
					err = a.persistUserInstruction(record, *state.final)
				}
			} else if state != nil && state.final != nil {
				err = a.persistFinalText(sink, record, state)
			} else {
				err = fmt.Errorf("the retained MiMo text part is absent")
			}
		case mimoPendingStreamedText:
			a.reconcileTextProgress(pending.partID, state, target)
			if state == nil || state.buffer == nil {
				err = fmt.Errorf("the retained MiMo streamed scope is absent")
			} else {
				_, err = state.buffer.PersistScope(progressScope(pending.partID), state.completion, func(raw []byte) error {
					return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
						Original: raw, AgentSessionID: record.sessionID, IdempotencyKey: "mimo-part:" + pending.partID + ":text",
					}, agent.SpanInfo{})
				})
				if err == nil {
					a.completeTextProgress(pending.partID, state, sink)
				}
			}
		case mimoPendingToolOpening, mimoPendingToolClosing:
			var part mimoPart
			part, err = retainedToolPart(pending.nativeFrame)
			if err == nil {
				if pending.kind == mimoPendingToolOpening {
					err = a.openToolCall(sink, part, pending.nativeFrame, record.sessionID)
					if err == nil {
						a.Mu.Lock()
						if call := a.tools[part.ID]; call != nil {
							call.openingTarget = target
						}
						a.Mu.Unlock()
						a.reconcileToolProgress(part.ID, target)
					}
				} else {
					err = a.closeToolCall(sink, part, pending.nativeFrame, pending.first, record.sessionID, pending.completion)
					closedPartID = part.ID
				}
			}
		case mimoPendingActorFailure:
			if pending.actorFailure == nil {
				err = fmt.Errorf("the retained MiMo actor failure is absent")
			} else {
				var raw []byte
				raw, err = agent.MarshalAssembledMessage(agent.AssembledMessageKindText, pending.actorFailure.Error, agent.MessageCompletionError)
				if err == nil {
					err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
						Original: raw, AgentSessionID: record.sessionID, IdempotencyKey: pending.partID,
					}, agent.SpanInfo{})
				}
			}
		case mimoPendingSpawnPrompt:
			if target.childID == "" || a.actorSpawnPromptPending(record.actor) {
				err = fmt.Errorf("the native MiMo spawn instruction still waits for its child")
			}
		case mimoPendingUserPrompt:
			if state != nil && state.final != nil {
				err = a.persistUserInstruction(record, *state.final)
			}
		default:
			err = fmt.Errorf("the retained MiMo part kind is invalid")
		}
		if err != nil {
			slog.Error("mimo persist retained part", "agent_id", a.AgentID(), "message_id", messageID, "error", err)
			return false
		}
		a.Mu.Lock()
		if pending.kind == mimoPendingActorFailure {
			// The successful actor row reports only its exact captured failure.
			for _, failure := range a.pendingFailures {
				if failure.actor == record.actor && failure.sessionID == record.sessionID && failure.err.Data.Message == pending.actorFailure.Error {
					a.removeFailureLocked(failure)
					break
				}
			}
		}
		record.pendingParts[0] = nil
		record.pendingParts = record.pendingParts[1:]
		if pending.kind == mimoPendingText || pending.kind == mimoPendingStreamedText || pending.kind == mimoPendingUserPrompt {
			delete(a.parts, pending.partID)
		} else if closedPartID != "" {
			if call := a.tools[closedPartID]; call != nil {
				call.lastFrame = nil
				call.progressTarget = nil
				call.openingTarget = nil
			}
		}
		a.Mu.Unlock()
	}
}

// flushChildInstructions gives a retained first instruction priority over later child output.
// The captured actor and session keep a retry separate from a replacement child.
func (a *Agent) flushChildInstructions(record *mimoMessageRecord, beforeOrder uint64) bool {
	type instruction struct {
		messageID string
		order     uint64
	}
	a.Mu.Lock()
	var pending []instruction
	for id, other := range a.messages {
		if other == record || other.role != roleUser || other.actor != record.actor || other.sessionID != record.sessionID || len(other.pendingParts) == 0 {
			continue
		}
		if order := other.pendingParts[0].order; order < beforeOrder {
			pending = append(pending, instruction{messageID: id, order: order})
		}
	}
	a.Mu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	for _, entry := range pending {
		if !a.flushPendingMessage(entry.messageID, "") {
			return false
		}
	}
	return true
}

func (a *Agent) flushPendingActor(actorID string, completion agent.MessageCompletion) {
	var messages []struct {
		id    string
		order uint64
	}
	a.Mu.Lock()
	for id, record := range a.messages {
		if record.actorID == actorID && len(record.pendingParts) > 0 {
			messages = append(messages, struct {
				id    string
				order uint64
			}{id: id, order: record.pendingParts[0].order})
		}
	}
	a.Mu.Unlock()
	sort.Slice(messages, func(i, j int) bool { return messages[i].order < messages[j].order })
	for _, message := range messages {
		// A failed message keeps its group. Other messages remain independent.
		a.flushPendingMessage(message.id, completion)
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
	if part.SessionID != "" && payload.SessionID != "" && part.SessionID != payload.SessionID {
		return
	}
	sessionID := part.SessionID
	if sessionID == "" {
		sessionID = payload.SessionID
	}
	if !a.ownsSession(sessionID) || part.ID == "" || part.MessageID == "" {
		return
	}
	a.Mu.Lock()
	resolvedSession := sessionID
	if resolvedSession == "" {
		resolvedSession = a.sessionID
	}
	valid := a.ownsPartIdentityLocked(part, resolvedSession)
	a.Mu.Unlock()
	if !valid {
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

// ownsPartIdentityLocked validates an update against its captured native part.
// The caller holds a.Mu. Omitted session fields resolve to the current session.
func (a *Agent) ownsPartIdentityLocked(part mimoPart, sessionID string) bool {
	if previous := a.parts[part.ID]; previous != nil {
		kind := partTypeText
		if previous.kind == agent.AssembledMessageKindReasoning {
			kind = partTypeReasoning
		}
		if part.MessageID != previous.messageID || part.Type != kind {
			return false
		}
	} else if previous := a.tools[part.ID]; previous != nil {
		if part.Type != contracts.MiMoPartTypeTool || part.MessageID != previous.messageID ||
			part.CallID != previous.callID || part.Tool != previous.name || sessionID != previous.sessionID {
			return false
		}
	}
	record := a.messages[part.MessageID]
	return record == nil || record.sessionID == sessionID
}

func (a *Agent) handleTextPart(sessionID string, part mimoPart) {
	a.Mu.Lock()
	state := a.parts[part.ID]
	a.Mu.Unlock()
	if state != nil && state.final != nil {
		return
	}
	recordInfo := a.messageRecord(sessionID, part.MessageID)
	if state == nil {
		kind := agent.AssembledMessageKindText
		if part.Type == partTypeReasoning {
			kind = agent.AssembledMessageKindReasoning
		}
		state = &mimoTextPart{
			kind:      kind,
			actorID:   recordInfo.actorID,
			messageID: part.MessageID,
			skip:      part.Synthetic || part.Ignored || recordInfo.role != roleAssistant || recordInfo.summary,
			whole:     recordInfo.role == roleUser,
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
		if !recordInfo.identityKnown && state.buffer == nil && part.Type == partTypeText && part.Text != "" && !part.Synthetic && !part.Ignored {
			a.Mu.Lock()
			if state.unresolved == nil {
				state.unresolvedOrder = a.pendingPartOrder
				a.pendingPartOrder++
			}
			state.unresolved = &part
			a.Mu.Unlock()
		}
		return
	}
	a.Mu.Lock()
	if state.skip {
		record := a.messages[part.MessageID]
		if record.role == roleUser && part.Type == partTypeText && !part.Synthetic && !part.Ignored {
			state.final = &part
			a.appendPendingPartLocked(record, &mimoPendingPart{kind: mimoPendingUserPrompt, partID: part.ID})
			a.Mu.Unlock()
			a.flushPendingMessage(part.MessageID, "")
			return
		}
		delete(a.parts, part.ID)
		a.Mu.Unlock()
		return
	}
	record := a.messages[part.MessageID]
	state.final = &part
	state.unresolved = nil
	waits := state.actorID == mainActorID && len(a.interruptRequests) > 0 ||
		a.awaitingAbortOutcome || len(record.pendingParts) > 0
	if !waits && !record.completed && state.completion == "" {
		state.completion = agent.MessageCompletionComplete
	}
	replaced := false
	for _, pending := range record.pendingParts {
		if pending.kind == mimoPendingStreamedText && pending.partID == part.ID {
			pending.kind = mimoPendingText
			replaced = true
			break
		}
	}
	if !replaced {
		a.appendPendingPartLocked(record, &mimoPendingPart{kind: mimoPendingText, partID: part.ID})
	}
	a.Mu.Unlock()
	// The final native text replaces its streamed copy. Its live scope stays
	// visible until the final row persists.
	if state.buffer != nil {
		state.buffer.Discard(progressScope(part.ID))
	}
	a.flushPendingMessage(part.MessageID, "")
}

// persistFinalText writes the retained native final part with its resolved outcome.
func (a *Agent) persistFinalText(sink agent.ProviderServices, record *mimoMessageRecord, state *mimoTextPart) error {
	part := state.final
	text := part.Text
	if strings.TrimSpace(text) == "" {
		a.completeTextProgress(part.ID, state, sink)
		return nil
	}
	raw, err := agent.MarshalAssembledMessage(state.kind, text, state.completion)
	if err != nil {
		return err
	}
	content := agent.MessageContent{Original: raw, AgentSessionID: record.sessionID, IdempotencyKey: "mimo-part:" + part.ID + ":text"}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		return err
	}
	if state.kind == agent.AssembledMessageKindText && record.actor != nil {
		a.Mu.Lock()
		record.actor.lastText = text
		a.Mu.Unlock()
	}
	a.completeTextProgress(part.ID, state, sink)
	return nil
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
	resolvedSession := delta.SessionID
	if resolvedSession == "" {
		resolvedSession = a.sessionID
	}
	wrongSession := false
	if state != nil {
		if record := a.messages[state.messageID]; record != nil {
			wrongSession = record.sessionID != resolvedSession
		}
	}
	a.Mu.Unlock()
	// A delta whose part never announced itself cannot be typed, and its part's
	// final update carries the whole text anyway.
	if state == nil || wrongSession || state.skip || state.final != nil || delta.MessageID != "" && delta.MessageID != state.messageID {
		return
	}
	scope := progressScope(delta.PartID)
	buffer := state.buffer
	if buffer == nil {
		buffer = a.bufferFor(state.actorID)
	}
	a.Mu.Lock()
	state.buffer = buffer
	// Whole user parts emit no deltas. The model delta makes this buffer authoritative.
	state.unresolved = nil
	if state.streamOrder == nil {
		order := a.pendingPartOrder
		a.pendingPartOrder++
		state.streamOrder = &order
	}
	a.Mu.Unlock()
	a.Mu.Lock()
	record := a.messages[state.messageID]
	a.Mu.Unlock()
	target := a.messageTranscriptTarget(record)
	a.reconcileTextProgress(delta.PartID, state, target)
	a.Mu.Lock()
	state.progressTarget = target
	a.Mu.Unlock()
	buffer.Append(scope, state.kind, delta.Delta, providerkit.JoinVerbatim)
	target.sink.ReportProgress(agent.ModelTextProgress(scope, delta.Delta))
}

// flushActorText persists what an actor streamed and never finished, as rows
// that state how the turn ended.
func (a *Agent) flushActorText(actorID string, completion agent.MessageCompletion) {
	type streamed struct {
		id    string
		state *mimoTextPart
	}
	a.Mu.Lock()
	var streams []streamed
	for id, state := range a.parts {
		if state.actorID != actorID || state.final != nil || state.completion != "" {
			continue
		}
		record := a.messages[state.messageID]
		// An unresolved whole text keeps its native record through Stop and
		// ClearContext: a failed native GET cannot identify a role or a
		// transcript destination, so closure retains the observation instead
		// of flushing it somewhere it does not belong. attributionClosed does
		// not lift this guard -- closure makes MORE attribution impossible,
		// not less.
		if state.unresolved != nil && !record.identityKnown {
			continue
		}
		if state.buffer == nil || state.streamOrder == nil {
			delete(a.parts, id)
			continue
		}
		streams = append(streams, streamed{id: id, state: state})
	}
	sort.Slice(streams, func(i, j int) bool { return *streams[i].state.streamOrder < *streams[j].state.streamOrder })
	for _, stream := range streams {
		record := a.messages[stream.state.messageID]
		stream.state.completion = completion
		if record.completed {
			stream.state.completion = agent.MessageCompletionComplete
			if record.errorName == contracts.MiMoErrorNameAborted {
				stream.state.completion = agent.MessageCompletionInterrupted
			}
		}
		a.appendPendingPartLocked(record, &mimoPendingPart{kind: mimoPendingStreamedText, partID: stream.id})
	}
	a.Mu.Unlock()
	a.flushPendingActor(actorID, completion)
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
	if part.ID == "" || part.CallID == "" || part.Tool == "" || part.State == nil {
		return
	}
	status := part.State.Status
	if status == contracts.MiMoToolStatusPending {
		// A pending call states no input yet, so it has nothing to show.
		return
	}
	record := a.messageRecord(sessionID, part.MessageID)
	a.Mu.Lock()
	call := a.tools[part.ID]
	if call == nil {
		call = &mimoToolCall{
			name: part.Tool, actorID: record.actorID, callID: part.CallID,
			messageID: part.MessageID, sessionID: record.sessionID, order: a.nextToolOrder,
		}
		a.nextToolOrder++
		a.tools[part.ID] = call
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
		call.countEligible = true
	}
	recordState := a.messages[part.MessageID]
	a.countMainToolLocked(call, recordState)
	if final || first {
		kind := mimoPendingToolOpening
		if final {
			kind = mimoPendingToolClosing
		}
		a.appendPendingPartLocked(recordState, &mimoPendingPart{
			kind: kind, partID: part.ID, nativeFrame: event.raw, first: first,
		})
	}
	a.Mu.Unlock()

	target := call.openingTarget
	if target == nil {
		target = a.messageTranscriptTarget(&record)
	}
	sink := target.sink
	a.reconcileToolProgress(part.ID, target)
	if first && part.Tool == contracts.MiMoToolBash {
		a.ClearCumulativeOutput(part.ID)
		sink.ReportProgress(agent.ResetOutputProgress(part.ID))
	}
	if final {
		a.observeCompletedTool(part)
	} else {
		a.observeRunningTool(sink, part)
	}
	a.flushPendingMessage(part.MessageID, "")
}

// countMainToolLocked counts one native main call in its captured active turn.
// The caller holds a.Mu. A transcript fallback does not establish native ownership.
func (a *Agent) countMainToolLocked(call *mimoToolCall, record *mimoMessageRecord) {
	if call.counted || !call.countEligible || record == nil || !a.turnActive ||
		!record.identityKnown || !record.actorKnown || record.role != roleAssistant || record.actorID != mainActorID ||
		record.turnEpoch != a.turnEpoch || record.sessionID != a.sessionID ||
		call.sessionID != record.sessionID || call.actorID != record.actorID {
		return
	}
	call.counted = true
	a.TurnToolUses++
}

// openToolCall persists a call's opening row and opens its span.
func (a *Agent) openToolCall(sink agent.ProviderServices, part mimoPart, raw []byte, sessionID string) error {
	spawns := isSpawnCall(part)
	content := agent.MessageContent{Original: raw, AgentSessionID: sessionID, IdempotencyKey: "mimo-part:" + part.ID + ":opening"}
	err := providerkit.OpenToolSpan(sink, content, part.ID, part.Tool, spawns)
	if err != nil && !spawns {
		// The shared helper opens the span even when persistence fails.
		// Close it on this sink before a retry can select another transcript.
		sink.CloseSpan(part.ID)
	}
	return err
}

// observeRunningTool reads what a running update adds: a shell command's
// output so far, and the subagent an actor call started.
func (a *Agent) observeRunningTool(sink agent.ProviderServices, part mimoPart) {
	metadata := toolMetadata(part.State)
	if part.Tool == contracts.MiMoToolActor && metadata.ActorID != "" && isSpawnCall(part) {
		a.linkSpawn(part.ID, metadata.ActorID, spawnTitle(part), spawnPrompt(part.State.Input))
	}
	a.observeToolOutput(sink, part, metadata)
}

// observeToolOutput reads the native cumulative output without repeating tool effects.
func (a *Agent) observeToolOutput(sink agent.ProviderServices, part mimoPart, metadata mimoToolMetadata) {
	if part.Tool == contracts.MiMoToolBash && metadata.Output != nil && *metadata.Output != "" {
		// The native update holds the whole output so far.
		// The counter measures its cumulative size without counting repeated bytes twice.
		// The last bytes supply the live tail.
		output := *metadata.Output
		observed := a.ObserveCumulativeOutput(part.ID, output, false)
		sink.ReportProgress(agent.OutputTotalProgress(part.ID, observed.Total, observed.Minimum))
		tail, clipped := agent.ClipTailBytes(output, mimoLiveOutputLimit)
		sink.ReportProgress(agent.OutputTailProgress(part.ID, tail, clipped))
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
func (a *Agent) closeToolCall(sink agent.ProviderServices, part mimoPart, raw []byte, first bool, sessionID string, completion agent.MessageCompletion) error {
	spawns := isSpawnCall(part)
	content := agent.MessageContent{Original: raw, AgentSessionID: sessionID, IdempotencyKey: "mimo-part:" + part.ID + ":closing", Completion: completion}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: part.ID, SpanType: part.Tool, Closing: true, NoSpan: first && spawns,
	}); err != nil {
		return err
	}
	a.ClearCumulativeOutput(part.ID)
	sink.ReportProgress(agent.CompleteOutputProgress(part.ID))
	sink.CloseSpan(part.ID)
	return nil
}

// observeCompletedTool applies native effects while its transcript row can still wait.
func (a *Agent) observeCompletedTool(part mimoPart) {
	spawns := isSpawnCall(part)
	metadata := toolMetadata(part.State)
	switch part.Tool {
	case contracts.MiMoToolActor:
		if spawns && metadata.ActorID != "" {
			a.linkSpawn(part.ID, metadata.ActorID, spawnTitle(part), spawnPrompt(part.State.Input))
		}
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
		partID string
		call   *mimoToolCall
	}
	a.Mu.Lock()
	var open []pending
	for partID, call := range a.tools {
		if call.actorID == actorID && call.opened && !call.final && len(call.lastFrame) > 0 {
			open = append(open, pending{partID: partID, call: call})
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].call.order < open[j].call.order })
	var messages []string
	for _, entry := range open {
		call := entry.call
		record := a.messages[call.messageID]
		if record == nil {
			continue
		}
		call.final = true
		call.countEligible = completion == agent.MessageCompletionInterrupted
		a.countMainToolLocked(call, record)
		a.appendPendingPartLocked(record, &mimoPendingPart{
			kind: mimoPendingToolClosing, partID: entry.partID,
			nativeFrame: call.lastFrame, completion: completion,
		})
		messages = append(messages, call.messageID)
	}
	a.Mu.Unlock()
	for _, messageID := range messages {
		a.flushPendingMessage(messageID, completion)
	}
}

// flushUnfinishedOutput persists unfinished text and closes unfinished tools
// with completion. It runs when native events can no longer finish that output:
//
//   - Process stop.
//   - Process exit.
//   - Session replacement.
//
// The main agent goes first, then each subagent in a stable order. Only an
// actor with retained output needs a flush. An actor with none produces no row
// and opens no transcript.
func (a *Agent) flushUnfinishedOutput(completion agent.MessageCompletion) {
	a.Mu.Lock()
	for _, record := range a.messages {
		record.attributionClosed = true
	}
	holders := map[string]struct{}{mainActorID: {}}
	for actorID := range a.buffers {
		holders[actorID] = struct{}{}
	}
	for _, call := range a.tools {
		holders[call.actorID] = struct{}{}
	}
	for _, part := range a.parts {
		if part.buffer != nil || part.final != nil {
			holders[part.actorID] = struct{}{}
		}
	}
	for _, record := range a.messages {
		if len(record.pendingParts) > 0 {
			holders[record.actorID] = struct{}{}
		}
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

// handleCompactionPart records each compaction phase.
// The first part starts the compaction and has no projection.
// The final part supplies the summary in its projection.
// Each phase becomes a notification in the affected actor's transcript.
// The browser combines the start notification with the final notification.
// See mimoProvider.Classify.
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
	var releaseManual bool
	if actorID == mainActorID {
		if phase == compactionStarted {
			// CompactContext compacts the main agent, so only its compaction
			// confirms the request.
			ack = a.compactionAck
			a.compactionAck = nil
			if ack != nil {
				a.manualCompactionID = part.ID
			}
		} else if part.ID != "" && part.ID == a.manualCompactionID {
			a.manualCompactionID = ""
			a.manualCompactionReady = true
			releaseManual = true
		}
	}
	a.Mu.Unlock()
	if ack != nil {
		close(ack)
	}
	content := agent.MessageContent{Original: event.raw}
	// A main-transcript compaction holds while a native failure can still
	// claim the main transcript ahead of it; a child transcript's compaction
	// is exactly addressed and writes now.
	rootHeld := actorID == mainActorID && a.rootHoldsNotifications()
	a.persistCapturedNotification(a.sinkForActor(actorID), content, rootHeld)
	if releaseManual {
		// The native summarize route keeps its reply until the next prompt
		// finishes. Release the Worker queue after the summary exists.
		a.Mu.Lock()
		a.turnActive = false
		a.Mu.Unlock()
		a.sink.ReportProgress(agent.ResetModelProgress())
		a.PublishTurnActive()
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
		// Every retry observation is distinct, including two with equal
		// bodies: nothing here identifies a native retry, so the queue keeps
		// each arrival as its own observation.
		a.beginTurn()
		a.persistCapturedNotification(a.sink, agent.MessageContent{Original: event.raw}, a.rootHoldsNotifications())
	case contracts.MiMoStatusTypeIdle:
		a.endTurn(event.raw)
	default:
		slog.Debug("mimo unknown session status", "agent_id", a.AgentID(), "status", payload.Status.Type)
	}
}

// beginTurn starts a turn. MiMo reports busy many times within one turn, so a
// repeat changes nothing.
func (a *Agent) beginTurn() {
	a.Mu.Lock()
	if a.turnActive {
		a.Mu.Unlock()
		return
	}
	if a.manualCompactionReady {
		if a.manualFollowupSending {
			a.manualFollowupBusy = true
		}
		a.Mu.Unlock()
		return
	}
	// A live child can still claim an earlier failure after the parent starts again.
	// Retire only observations that no live child can claim.
	if a.runningActorsLocked() == 0 {
		a.readyUnreportedFailuresLocked()
	}
	if a.runningActorsLocked() == 0 {
		a.awaitingAbortOutcome = false
	}
	a.turnEpoch++
	a.turnActive = true
	a.interruptRequests = nil
	a.turnFailure = nil
	a.TurnToolUses = 0
	a.Mu.Unlock()
	a.flushFailureNotifications()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetModelProgress())
}

// endTurn persists unfinished root output, then the divider, then clears the active flag.
//
// The divider is the event that ended the turn: the failure when one failed it,
// else the idle status. PersistTurnEnd reads the turn's tool count before the
// active flag clears. That clear releases the Worker input queue.
//
// An idle with no active turn persists nothing and republishes the idle turn.
// The Worker can hold an active turn before this agent starts one.
// The input queue marks a turn active when the native server accepts its prompt.
func (a *Agent) endTurn(idle []byte) {
	// A divider whose write failed retries first: the later idle can only
	// carry that retry, never a fresh finalization over the failed turn's
	// real outcome.
	a.retryRetainedTurnEnd()
	a.flushFailureNotifications()
	a.Mu.Lock()
	if a.manualCompactionID != "" {
		// The native summary can report idle before its completed part arrives.
		// The completed part releases the queued follow-up.
		a.Mu.Unlock()
		return
	}
	if !a.turnActive {
		// An idle from the summarize route can precede its follow-up prompt.
		// Keep that prompt eligible until MiMo accepts it.
		a.manualFollowupBusy = false
		a.Mu.Unlock()
		a.PublishTurnActive()
		return
	}
	a.manualCompactionID = ""
	a.manualCompactionReady = false
	if a.turnFailure == nil && a.runningActorsLocked() == 0 {
		// With no live child, an unclaimed current failure belongs to the main prompt.
		for _, failure := range a.pendingFailures {
			if failure.sessionID == a.sessionID && failure.epoch == a.turnEpoch && failure.actor == nil && !failure.notificationReady {
				failure.main = true
				a.turnFailure = failure
				break
			}
		}
	}
	selected := a.turnFailure
	completion := agent.MessageCompletionComplete
	divider := idle
	switch {
	case a.hasConfirmedInterruptLocked():
		completion = agent.MessageCompletionInterrupted
	case a.turnFailure != nil:
		completion = agent.MessageCompletionError
		divider = a.turnFailure.raw
	}
	reportsFailure := selected != nil && completion == agent.MessageCompletionError
	if selected != nil && !reportsFailure {
		selected.notificationReady = true
	}
	a.Mu.Unlock()

	// Each call that this closes clears its own output count. A subagent's call
	// can still run, so the counts are not reset as a whole.
	a.flushActorText(mainActorID, completion)
	a.closeUnfinishedTools(mainActorID, completion)
	a.sink.ReportProgress(agent.ResetModelProgress())
	content := a.turnEndContent(divider, completion)
	// The native session fact travels IN the content: the captured write
	// freezes it, so a retry after a session change still lands the divider
	// in the transcript of the session that actually ended the turn.
	a.Mu.Lock()
	content.AgentSessionID = a.sessionID
	a.Mu.Unlock()
	captured := agent.CaptureTranscript(a.sink, content, agent.SpanInfo{})
	if err := captured.PersistTurnEnd(); err != nil {
		slog.Error("mimo persist turn end", "agent_id", a.AgentID(), "error", err)
		// Retain the exact finalization -- divider bytes, completion, tool
		// count -- for the retry the next idle or closure carries. A later
		// idle would otherwise write an empty divider over the failed turn's
		// real outcome.
		a.Mu.Lock()
		a.retainedTurnEnd = &captured
		if reportsFailure {
			a.retainedTurnEndFailure = selected
		}
		a.Mu.Unlock()
	} else if reportsFailure {
		a.Mu.Lock()
		a.removeFailureLocked(selected)
		a.Mu.Unlock()
	}
	a.resetUnownedRootSpans()

	a.Mu.Lock()
	a.turnActive = false
	a.interruptRequests = nil
	if a.runningActorsLocked() == 0 {
		a.awaitingAbortOutcome = false
	}
	a.turnFailure = nil
	for _, failure := range a.pendingFailures {
		if failure != selected && (failure.main || a.runningActorsLocked() == 0) {
			failure.notificationReady = true
		}
	}
	clear(a.compactions)
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.retireMainControls()
	a.flushFailureNotifications()
	a.flushCapturedNotifications()
}

// retryRetainedTurnEnd retries a turn-end divider whose write failed, with
// the finalization captured when the turn actually ended.
func (a *Agent) retryRetainedTurnEnd() {
	a.Mu.Lock()
	captured, failure := a.retainedTurnEnd, a.retainedTurnEndFailure
	a.Mu.Unlock()
	if captured == nil {
		return
	}
	if err := captured.PersistTurnEnd(); err != nil {
		slog.Error("mimo persist turn end", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	a.retainedTurnEnd, a.retainedTurnEndFailure = nil, nil
	a.Mu.Unlock()
	if failure != nil {
		a.Mu.Lock()
		a.removeFailureLocked(failure)
		a.Mu.Unlock()
	}
}

// keepPendingPartsLocked preserves failed native writes when the session changes.
// The caller holds a.Mu. Each retained message keeps its original owner.
func (a *Agent) keepPendingPartsLocked() {
	textParts := make(map[string]struct{})
	toolCalls := make(map[string]struct{})
	unresolvedMessages := make(map[string]struct{})
	for partID, part := range a.parts {
		if part.unresolved != nil {
			textParts[partID] = struct{}{}
			unresolvedMessages[part.messageID] = struct{}{}
		}
	}
	for messageID, record := range a.messages {
		_, hasUnresolvedText := unresolvedMessages[messageID]
		if len(record.pendingParts) == 0 && !hasUnresolvedText {
			delete(a.messages, messageID)
			continue
		}
		for _, pending := range record.pendingParts {
			switch pending.kind {
			case mimoPendingText, mimoPendingStreamedText, mimoPendingUserPrompt:
				textParts[pending.partID] = struct{}{}
			case mimoPendingToolOpening, mimoPendingToolClosing:
				if part, err := retainedToolPart(pending.nativeFrame); err == nil {
					toolCalls[part.ID] = struct{}{}
				}
			case mimoPendingActorFailure, mimoPendingSpawnPrompt:
				// The message record owns this native status directly.
			}
		}
	}
	for partID := range a.parts {
		if _, keeps := textParts[partID]; !keeps {
			delete(a.parts, partID)
		}
	}
	for partID := range a.tools {
		if _, keeps := toolCalls[partID]; !keeps {
			delete(a.tools, partID)
		}
	}
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

// mimoFailure owns one native session.error until attribution or persistence succeeds.
type mimoFailure struct {
	raw               []byte
	err               mimoError
	sessionID         string
	epoch             uint64
	main              bool
	actor             *mimoActor
	notificationReady bool
}

// claimedBy reports whether a failed message states this failure.
func (f *mimoFailure) claimedBy(err mimoError) bool {
	return f != nil && f.err.Name == err.Name && f.err.Data.Message == err.Data.Message
}

// handleSessionError records a failure.
//
// Subagent failures use the parent's session ID. The later message supplies the actor.
// The handler retains the failure until that message identifies its owner:
//
//   - A main failure becomes the root divider.
//   - A child failure stays in its child transcript. endActorTurn records that failure.
//   - An unclaimed failure becomes the root failure at settlement.
//
// Outside a turn, a refused prompt can report only a session failure.
// The handler persists that failure as a notification.
// It retains the failure while a subagent can still claim it.
// MiMo does not identify a session.error. Equal error text cannot prove a repeat.
// The handler preserves each observation until ownership or persistence resolves it.
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
	case contracts.MiMoErrorNameAborted:
		a.Mu.Lock()
		if a.turnActive || a.runningActorsLocked() > 0 {
			a.awaitingAbortOutcome = true
		}
		a.Mu.Unlock()
		return
	case errorNameContextOverflow:
		slog.Debug("mimo context overflow; MiMo compacts and continues", "agent_id", a.AgentID())
		return
	}
	a.Mu.Lock()
	failure := &mimoFailure{
		raw: event.raw, err: payload.Error, sessionID: a.sessionID, epoch: a.turnEpoch,
		notificationReady: !a.turnActive && a.runningActorsLocked() == 0,
	}
	a.pendingFailures = append(a.pendingFailures, failure)
	ready := failure.notificationReady
	a.Mu.Unlock()
	if ready {
		a.flushFailureNotifications()
		a.PublishTurnActive()
	}
}

// attributeFailure gives a held failure to its first native message observation.
// A repeated message cannot claim a later failure with the same native text.
// Main claims require their captured root epoch. Live child messages can cross root epochs.
func (a *Agent) attributeFailure(err mimoError, record *mimoMessageRecord) {
	if err.Name == "" {
		return
	}
	a.Mu.Lock()
	if record.streamFailure != nil {
		a.Mu.Unlock()
		return
	}
	record.streamFailure = &err
	if record.actorID == mainActorID && record.turnEpoch != a.turnEpoch {
		a.Mu.Unlock()
		return
	}
	var failure *mimoFailure
	for _, pending := range a.pendingFailures {
		if !pending.main && pending.actor == nil && pending.sessionID == record.sessionID && pending.claimedBy(err) &&
			(record.actorID != mainActorID || pending.epoch == record.turnEpoch) {
			failure = pending
			break
		}
	}
	if failure == nil {
		a.Mu.Unlock()
		return
	}
	if record.actorID != mainActorID {
		// Native actor settlement or closure must still persist this failure.
		failure.actor = record.actor
		a.Mu.Unlock()
		// The failure claimed a child transcript, so the main transcript's
		// held notifications now have a settled destination.
		a.flushCapturedNotifications()
		return
	}
	if a.turnActive {
		failure.main = true
		if a.turnFailure == nil {
			a.turnFailure = failure
		}
		a.Mu.Unlock()
		return
	}
	failure.main = true
	failure.notificationReady = true
	a.Mu.Unlock()
	a.flushFailureNotifications()
	a.flushCapturedNotifications()
	a.PublishTurnActive()
}

// flushHeldFailure persists a failure held outside a turn once no subagent
// runs that could still claim it.
func (a *Agent) flushHeldFailure() {
	a.Mu.Lock()
	active := a.turnActive || a.runningActorsLocked() > 0
	held := false
	if !active {
		a.awaitingAbortOutcome = false
		held = a.readyUnreportedFailuresLocked()
	}
	if active || !held {
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	a.flushFailureNotifications()
	a.flushCapturedNotifications()
	a.PublishTurnActive()
}

// readyUnreportedFailuresLocked keeps every unreported failure eligible for persistence.
// The caller holds a.Mu. Closure removes no native frame before its write succeeds.
func (a *Agent) readyUnreportedFailuresLocked() bool {
	for _, failure := range a.pendingFailures {
		failure.notificationReady = true
	}
	a.turnFailure = nil
	return len(a.pendingFailures) > 0
}

// removeFailureLocked removes only the captured observation. The caller holds a.Mu.
func (a *Agent) removeFailureLocked(failure *mimoFailure) {
	a.pendingFailures = slices.DeleteFunc(a.pendingFailures, func(pending *mimoFailure) bool { return pending == failure })
}

// flushFailureNotifications retries eligible observations in their native order.
// A child failure stays held while its child row can still succeed: the retained
// native actor row owns that report until its write succeeds, so closure cannot
// persist a second copy over it. A write that fails keeps its failure queued;
// a later Stop, Wait, or ClearContext retries it without repeating the entries
// that already succeeded.
func (a *Agent) flushFailureNotifications() {
	for {
		a.Mu.Lock()
		var failure *mimoFailure
		for _, pending := range a.pendingFailures {
			if pending.notificationReady {
				failure = pending
				break
			}
		}
		a.Mu.Unlock()
		if failure == nil {
			return
		}
		a.Mu.Lock()
		pendingActorRow := a.hasPendingActorFailureLocked(failure)
		a.Mu.Unlock()
		if pendingActorRow {
			// The retained native actor row owns this report until its write succeeds.
			return
		}
		sink := a.sink
		if failure.actor != nil {
			if childID, ok := a.ensureActorRecordTranscript(failure.actor, failure.sessionID); ok {
				sink = a.sink.ChildSink(childID)
			}
		}
		if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: failure.raw}); err != nil {
			slog.Error("mimo persist error", "agent_id", a.AgentID(), "error", err)
			return
		}
		a.Mu.Lock()
		a.removeFailureLocked(failure)
		a.Mu.Unlock()
	}
}

// hasPendingActorFailureLocked finds the retained native row for one captured child failure.
// The caller holds a.Mu. The existing pending parts remain the sole row authority.
func (a *Agent) hasPendingActorFailureLocked(failure *mimoFailure) bool {
	if failure.actor == nil {
		return false
	}
	for _, record := range a.messages {
		if record.actor != failure.actor || record.sessionID != failure.sessionID {
			continue
		}
		for _, pending := range record.pendingParts {
			if pending.kind == mimoPendingActorFailure && pending.actorFailure != nil && pending.actorFailure.Error == failure.err.Data.Message {
				return true
			}
		}
	}
	return false
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

// recordMessageUsage adds one assistant message's usage to the readout.
// Context usage follows only the current main message's native session and turn epoch.
// A child has its own context. Cost includes every actor in the session.
func (a *Agent) recordMessageUsage(info mimoMessageInfo, record mimoMessageRecord) {
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
	if record.actorID == mainActorID && record.sessionID == a.sessionID && record.turnEpoch == a.turnEpoch && info.Tokens != nil {
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
