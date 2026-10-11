package acp

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// This file routes each subagent's session updates to its own transcript.
// An agent can report a subagent's work through either route:
//
//   - The main session tags each update.
//     Qwen Code supplies the spawning tool-call ID, and Hooks.ChildUpdateRoute identifies that tag.
//   - A separate child session supplies its own updates.
//     Grok Build streams under the child session ID, and AttachChildSession links that session to a registry row.
//
// Both routes reach a child conversation that uses the same rendering code as the main conversation.
// A provider that reads a subagent from storage instead of the stream constructs updates and passes them through FeedChildUpdate.
// Each registry row identifies at most one child conversation.
// Its row key identifies the corresponding entries in every map below.

// childAgentRef holds the child that EnsureChildAgent creates for one row and the sink that creates it.
// A nested subagent uses its parent subagent's sink, so that same sink opens its transcript.
type childAgentRef struct {
	id     string
	parent agent.ProviderServices
}

// acpChildren holds the child conversations of one agent.
type acpChildren struct {
	mu sync.Mutex
	// agents maps a registry row key to the child agent that this process
	// created for it.
	agents map[string]childAgentRef
	// active maps a registry row key to the child conversation that renders
	// its updates.
	active map[string]*childConversation
	// sessions maps a child session id to the registry row key of its subagent.
	sessions map[string]string
	// closed holds row keys that this process closes or that the registry reports as finished.
	// Those rows route no update, and later chunks do not read the registry again.
	// Without an identity refusal, a late tagged update stays in the parent transcript.
	// rememberChildAgent removes a row from this set when this process opens it again.
	//
	// Do not retain a row that the registry does not know.
	// A provider can create it later through a path outside the base, such as Qwen Code's background-subagent store reader.
	// Retaining that earlier miss would discard the child's later transcript.
	closed map[string]struct{}
	// refused holds rows whose explicit child validation rejected the stored identity.
	// Only a later successful validation removes a row.
	refused map[string]struct{}
}

type childRouteResult uint8

const (
	childRouteUnknown childRouteResult = iota
	childRouteBlocked
	childRouteAccepted
)

// childConversation holds one child conversation and the lock that serializes its updates.
// The reader handles stream updates, and a provider can feed a child from a separate goroutine.
// Every child write therefore holds feedMu.
type childConversation struct {
	feedMu sync.Mutex
	conversation
	// parent is the sink that created the child agent, which owns the child's
	// transcript primitives.
	parent agent.ProviderServices
	// prompted records that an agent message opens the transcript.
	// feedMu protects the field.
	prompted bool
}

// rememberChildAgent records the child agent that EnsureChildAgent created for
// a row, and the sink that created it.
func (b *Base) rememberChildAgent(rowKey, childAgentID string, parent agent.ProviderServices) {
	b.children.mu.Lock()
	previous := b.children.active[rowKey]
	if previous != nil && previous.childAgentID != childAgentID {
		delete(b.children.active, rowKey)
	} else {
		previous = nil
	}
	if b.children.agents == nil {
		b.children.agents = make(map[string]childAgentRef)
	}
	b.children.agents[rowKey] = childAgentRef{id: childAgentID, parent: parent}
	delete(b.children.closed, rowKey)
	delete(b.children.refused, rowKey)
	b.children.mu.Unlock()
	if previous != nil {
		previous.finish(bgtask.StatusStopped)
	}
}

// refuseChild records a failed identity check without discarding accepted output.
func (b *Base) refuseChild(rowKey string) {
	if rowKey == "" {
		return
	}
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	if b.children.refused == nil {
		b.children.refused = make(map[string]struct{})
	}
	b.children.refused[rowKey] = struct{}{}
}

func (b *Base) childIdentityRefused(rowKey string) bool {
	if rowKey == "" {
		return false
	}
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	_, refused := b.children.refused[rowKey]
	return refused
}

// childRouteUnavailableLocked checks stored route state. The caller holds children.mu.
func (b *Base) childRouteUnavailableLocked(rowKey string) bool {
	_, refused := b.children.refused[rowKey]
	_, closed := b.children.closed[rowKey]
	return refused || closed
}

// childRouteCurrent checks ownership after the caller takes the child's feed mutex.
// A frame that passes this check precedes a later refusal and keeps its original delegate.
func (b *Base) childRouteCurrent(rowKey string, child *childConversation) bool {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	return !b.childRouteUnavailableLocked(rowKey) && b.children.active[rowKey] == child
}

// childFor returns the row's child conversation and opens it on the first update.
// Return nil when the row identifies no child transcript.
//
// A row from a process before a worker restart resolves through the registry, which retains its child link across restarts.
// A row whose subagent already finished routes no updates. See acpChildren.closed.
func (b *Base) childFor(rowKey string) *childConversation {
	if rowKey == "" {
		return nil
	}
	b.children.mu.Lock()
	if b.childRouteUnavailableLocked(rowKey) {
		b.children.mu.Unlock()
		return nil
	}
	if child := b.children.active[rowKey]; child != nil {
		b.children.mu.Unlock()
		return child
	}
	ref, known := b.children.agents[rowKey]
	b.children.mu.Unlock()
	if !known {
		// Read the registry outside the lock.
		// The database read must not make the reader goroutine hold every child's shared lock across I/O.
		childAgentID, status, found, err := b.sink.LookupBackgroundTask(rowKey)
		if err != nil {
			slog.Warn("acp child route lookup failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "row_key", rowKey, "error", err)
			return nil
		}
		if !found || childAgentID == "" {
			return nil
		}
		if status.IsFinished() {
			b.markChildClosed(rowKey)
			return nil
		}
		ref = childAgentRef{id: childAgentID, parent: b.sink}
	}
	b.children.mu.Lock()
	if b.childRouteUnavailableLocked(rowKey) {
		b.children.mu.Unlock()
		return nil
	}
	// Another goroutine can open the same child while the registry read runs.
	// The first one wins, so one row never renders into two conversations.
	if child := b.children.active[rowKey]; child != nil {
		b.children.mu.Unlock()
		return child
	}
	if current, known := b.children.agents[rowKey]; known {
		ref = current
	}
	b.children.mu.Unlock()
	childSink := ref.parent.ChildSink(ref.id)
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	if b.childRouteUnavailableLocked(rowKey) {
		return nil
	}
	if child := b.children.active[rowKey]; child != nil {
		return child
	}
	if current, known := b.children.agents[rowKey]; known && current.id != ref.id {
		return nil
	}
	if childSink == nil {
		return nil
	}
	if b.children.active == nil {
		b.children.active = make(map[string]*childConversation)
	}
	child := &childConversation{
		conversation: conversation{
			b:            b,
			childSink:    childSink,
			childAgentID: ref.id,
			out:          &acpTurnOutput{},
		},
		parent: ref.parent,
	}
	b.children.active[rowKey] = child
	return child
}

// routeToChild distinguishes an unknown row from a known blocked child route.
// A blocked tagged update must not enter the root transcript.
func (b *Base) routeToChild(rowKey string, header acpUpdateHeader, update json.RawMessage) childRouteResult {
	child := b.childFor(rowKey)
	if child == nil {
		if b.childIdentityRefused(rowKey) {
			return childRouteBlocked
		}
		b.children.mu.Lock()
		_, known := b.children.agents[rowKey]
		_, closed := b.children.closed[rowKey]
		b.children.mu.Unlock()
		if known && !closed {
			return childRouteBlocked
		}
		return childRouteUnknown
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if !b.childRouteCurrent(rowKey, child) {
		return childRouteBlocked
	}
	if header.SessionUpdate == contracts.ACPUpdateUserMessageChunk && b.hooks.ChildUserMessages {
		child.persistAgentMessage(header.Content)
		return childRouteAccepted
	}
	// A child renders conversation updates only.
	// The session's mode and model belong to the main conversation, as do its command set and usage.
	// Discard an update that reports those fields instead of applying it to the parent.
	child.handleUpdate(header, update)
	return childRouteAccepted
}

// persistAgentMessage writes the agent's message to its subagent's transcript through Hooks.ChildUserMessages.
// The caller holds feedMu.
//
// The first message opens the transcript as its prompt.
// A transcript already opened by a spawn prompt keeps that existing prompt instead.
// Each later message becomes an ordinary conversation message.
// Persist the child's previously assembled text first to preserve transcript order.
func (c *childConversation) persistAgentMessage(content json.RawMessage) {
	text := c.b.extractACPChunkText(content, contracts.ACPUpdateUserMessageChunk)
	if strings.TrimSpace(text) == "" {
		return
	}
	c.flushThoughtBuffer()
	c.flushAssistantBuffer()
	var err error
	if c.prompted {
		err = c.parent.PersistChildUserMessage(c.childAgentID, text)
	} else {
		c.prompted = true
		err = c.parent.PersistChildPrompt(c.childAgentID, text)
	}
	if err != nil {
		slog.Warn("acp child message persist failed", "provider", c.b.ProviderName(), "agent_id", c.b.AgentID(), "child_agent_id", c.childAgentID, "error", err)
	}
}

// FeedChildUpdate renders one session update in the child transcript that owns rowKey.
// A provider with a stored transcript constructs the update that its agent would send for each record and calls this method.
// Return false for an unknown or refused route.
func (b *Base) FeedChildUpdate(rowKey string, update json.RawMessage) bool {
	var header acpUpdateHeader
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp child update unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "row_key", rowKey, "error", err)
		return false
	}
	return b.routeToChild(rowKey, header, update) == childRouteAccepted
}

// FinishChildTurn persists the text that rowKey's child assembled before its turn ended.
// The child transcript stays open, so a later resumed turn renders in that same transcript.
func (b *Base) FinishChildTurn(rowKey string) {
	b.children.mu.Lock()
	child := b.children.active[rowKey]
	b.children.mu.Unlock()
	if child == nil {
		return
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if !b.childRouteCurrent(rowKey, child) {
		return
	}
	child.flushThoughtBuffer()
	child.flushAssistantBuffer()
}

// AttachChildSession routes sessionID updates to the subagent transcript that owns rowKey.
// An agent with a separate child session calls it after linking that session to its row.
func (b *Base) AttachChildSession(sessionID, rowKey string) {
	if sessionID == "" || rowKey == "" {
		return
	}
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	if b.children.sessions == nil {
		b.children.sessions = make(map[string]string)
	}
	// One row has one active native session. A later run retires the previous route.
	for previous, row := range b.children.sessions {
		if row == rowKey && previous != sessionID {
			delete(b.children.sessions, previous)
		}
	}
	b.children.sessions[sessionID] = rowKey
}

// sessionUpdateOwner resolves a known session without holding a routing lock during provider work.
func (b *Base) sessionUpdateOwner(sessionID string) (agent.ProviderServices, bool) {
	if sessionID == "" {
		return nil, false
	}
	if b.IsCurrentSession(sessionID) {
		return b.sink, b.sink != nil
	}
	rowKey := b.childSessionRow(sessionID)
	if rowKey == "" {
		return nil, false
	}
	child := b.childFor(rowKey)
	if child == nil {
		return nil, false
	}
	return child.sink(), true
}

// ApplySubagentObservationForSession writes through the conversation that owns a known native session.
func (b *Base) ApplySubagentObservationForSession(sessionID string, observation *SubagentObservation) bool {
	if sessionID == "" {
		return false
	}
	if b.IsCurrentSession(sessionID) {
		b.main().applySubagentObservation(observation)
		return true
	}
	rowKey := b.childSessionRow(sessionID)
	if rowKey == "" {
		return false
	}
	child := b.childFor(rowKey)
	if child == nil {
		return false
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if !b.childRouteCurrent(rowKey, child) {
		return false
	}
	child.applySubagentObservation(observation)
	return true
}

// OpenToolSink returns the transcript services that hold toolCallID open in sessionID.
// The current session uses the agent's own services.
// A subagent session uses the child services linked by its registry row.
// Return ok=false when neither transcript holds that call open.
// A provider can report live output outside tool-call updates, as Kiro does through content chunks.
// These services route that output to the correct tool row and tab.
func (b *Base) OpenToolSink(sessionID, toolCallID string) (agent.ProviderServices, bool) {
	if toolCallID == "" {
		return nil, false
	}
	if b.IsCurrentSession(sessionID) {
		return b.sink, b.holdsOpenTool(toolCallID)
	}
	child := b.childFor(b.childSessionRow(sessionID))
	if child == nil {
		return nil, false
	}
	return child.childSink, child.out.holdsOpenTool(toolCallID)
}

// withOpenToolConversation runs fn in the conversation that holds toolCallID open in sessionID.
// It selects one of these conversations:
//
//   - The main conversation for the current session.
//     A request with no session also uses it, as ServesSession permits.
//   - The child conversation linked by a registry row for a subagent session.
//     Hold that child's feedMu while fn runs, as every child write does.
//
// Do not call fn when neither conversation holds that tool call open.
func (b *Base) withOpenToolConversation(sessionID, toolCallID string, fn func(*conversation)) {
	if toolCallID == "" {
		return
	}
	if sessionID == "" || b.IsCurrentSession(sessionID) {
		if b.holdsOpenTool(toolCallID) {
			fn(b.main())
		}
		return
	}
	rowKey := b.childSessionRow(sessionID)
	child := b.childFor(rowKey)
	if child == nil {
		return
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if !b.childRouteCurrent(rowKey, child) || !child.out.holdsOpenTool(toolCallID) {
		return
	}
	fn(&child.conversation)
}

// childSessionRow returns the registry row key of the subagent that runs in
// sessionID, or "" for a session that no subagent owns.
func (b *Base) childSessionRow(sessionID string) string {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	return b.children.sessions[sessionID]
}

// finishChildConversation ends a child with status unless identity validation refused it.
// It stores accepted text and closes each open tool with the completion that status supplies.
func (b *Base) finishChildConversation(rowKey string, status bgtask.Status) {
	b.children.mu.Lock()
	if _, refused := b.children.refused[rowKey]; refused {
		b.children.mu.Unlock()
		return
	}
	child := b.children.active[rowKey]
	delete(b.children.active, rowKey)
	b.children.mu.Unlock()
	if child == nil {
		return
	}
	child.finish(status)
}

// finish stores output that this conversation accepted before its route ended.
func (c *childConversation) finish(status bgtask.Status) {
	c.feedMu.Lock()
	defer c.feedMu.Unlock()
	turn := c.out.drainTurn()
	textCompletion, toolCompletion := childCloseCompletions(status)
	c.persistAssembledText(agent.AssembledMessageKindReasoning, turn.thoughtText, textCompletion)
	c.persistAssembledText(agent.AssembledMessageKindText, turn.assistantText, textCompletion)
	c.persistIncompleteTools(turn.incompleteTools, toolCompletion)
}

// childCloseCompletions selects completions for a child's text and open tools when its row closes with status.
// A successful subagent completes its text, but any tool it leaves open fails, as it does at a main turn's end.
// A failed subagent ends both text and tools with an error.
// Every other final status interrupts both.
func childCloseCompletions(status bgtask.Status) (text, tools agent.MessageCompletion) {
	switch status {
	case bgtask.StatusSucceeded:
		return agent.MessageCompletionComplete, agent.MessageCompletionError
	case bgtask.StatusFailed:
		return agent.MessageCompletionError, agent.MessageCompletionError
	default:
		return agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted
	}
}

// finishAllChildConversations ends every child conversation because its supplying process stopped.
func (b *Base) finishAllChildConversations() {
	for _, rowKey := range b.childRows() {
		b.children.mu.Lock()
		child := b.children.active[rowKey]
		delete(b.children.active, rowKey)
		b.children.mu.Unlock()
		if child != nil {
			child.finish(bgtask.StatusStopped)
		}
	}
}

// finishUnrefusedChildConversations keeps refused conversations until the process ends.
func (b *Base) finishUnrefusedChildConversations() {
	for _, rowKey := range b.childRows() {
		b.finishChildConversation(rowKey, bgtask.StatusStopped)
	}
}

// childRows lists rows with a retained child conversation.
func (b *Base) childRows() []string {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	rowKeys := make([]string, 0, len(b.children.active))
	for rowKey := range b.children.active {
		rowKeys = append(rowKeys, rowKey)
	}
	return rowKeys
}

// renameChildConversation moves child state when a provider changes the row key.
// The new key retains the transcript that the preceding key opened.
func (b *Base) renameChildConversation(from, to string) {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	if ref, ok := b.children.agents[from]; ok {
		delete(b.children.agents, from)
		b.children.agents[to] = ref
	}
	if child, ok := b.children.active[from]; ok {
		delete(b.children.active, from)
		b.children.active[to] = child
	}
	delete(b.children.closed, to)
	if _, refused := b.children.refused[from]; refused {
		b.children.refused[to] = struct{}{}
	}
	for sessionID, rowKey := range b.children.sessions {
		if rowKey == from {
			b.children.sessions[sessionID] = to
		}
	}
}

// forgetChild removes every route to a closed row's subagent.
// A later session update reaches no transcript, as for any session that the base does not serve.
// A later tagged update stays in the parent unless identity validation refuses that row.
func (b *Base) forgetChild(rowKey string) {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	delete(b.children.agents, rowKey)
	for sessionID, key := range b.children.sessions {
		if key == rowKey {
			delete(b.children.sessions, sessionID)
		}
	}
	b.markChildClosedLocked(rowKey)
}

// forgetChildSessions removes every subagent-session route because the session that ran those subagents ended.
// A later child update reaches no transcript, and ServesSession refuses a later child control request.
func (b *Base) forgetChildSessions() {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	b.children.sessions = nil
}

// cleanupChildAgent releases the child service state that this process created for rowKey.
// It uses the sink that created the child, and the child's transcript survives.
func (b *Base) cleanupChildAgent(rowKey string) {
	b.children.mu.Lock()
	if _, refused := b.children.refused[rowKey]; refused {
		b.children.mu.Unlock()
		return
	}
	ref, known := b.children.agents[rowKey]
	b.children.mu.Unlock()
	if known {
		ref.parent.CleanupChildAgent(ref.id)
	}
}

// markChildClosed records that the subagent of rowKey finished.
func (b *Base) markChildClosed(rowKey string) {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	b.markChildClosedLocked(rowKey)
}

func (b *Base) markChildClosedLocked(rowKey string) {
	if b.children.closed == nil {
		b.children.closed = make(map[string]struct{})
	}
	b.children.closed[rowKey] = struct{}{}
}
