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

// This file routes the session updates of a subagent into the transcript of
// that subagent. An agent reports the work of a subagent in one of two ways:
//
//   - In the SAME session, with a tag on each update. Qwen Code tags the update
//     of a subagent with the id of the tool call that spawned it. The provider
//     states the tag through Hooks.ChildUpdateRoute.
//   - In a session of its OWN, which the agent opens beside the main one. Grok
//     Build streams each subagent under the id of its child session. The
//     provider links that session to a registry row through AttachChildSession.
//
// Either way the update reaches a child conversation, which renders it with the
// same code as the main conversation. A provider that reads a subagent from a
// store instead of the stream feeds the updates that it builds through
// FeedChildUpdate.
//
// Each registry row that carries a child transcript identifies at most one child
// conversation, and the row key is the key of every map below.

// childAgentRef is the child agent that EnsureChildAgent created for one row,
// and the sink that created it. A subagent of a subagent is created through
// the sink of its parent subagent, so its transcript opens through that sink.
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
	// closed holds the row keys whose subagent finished: a row that this process
	// closed, and a finished row that the registry reported. Such a row routes
	// no update, and no chunk reads the registry again. Without an identity refusal,
	// a late tagged update stays in the parent. A row that this process opens through
	// rememberChildAgent leaves it.
	//
	// It holds no row that the registry does not know. A provider can create a
	// row later through a route that the base does not see (Qwen Code reads a
	// background subagent from its store), and a kept miss would lose that
	// child's transcript.
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

// childConversation is one child conversation and the lock that serializes its
// updates. The reader goroutine renders the stream, and a provider can feed a
// child from a goroutine of its own, so every write to one child takes feedMu.
type childConversation struct {
	feedMu sync.Mutex
	conversation
	// parent is the sink that created the child agent, which owns the child's
	// transcript primitives.
	parent agent.ProviderServices
	// prompted records that a message of the agent opened the transcript.
	// Guarded by feedMu.
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

// childFor returns the child conversation of a row, and opens it on the first
// update. It returns nil for a row that identifies no child transcript.
//
// A row that this process did not create -- the subagent of a process that
// ran before a worker restart -- resolves through the registry, which keeps
// the child link across restarts. A row whose subagent finished routes
// nothing (see acpChildren.closed).
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
		// The registry read runs outside the lock: it is a database read, and
		// the reader goroutine must not hold the lock of every child across it.
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
	// A child renders its conversation and nothing else: the mode, the model,
	// the command set and the usage of the session belong to the main one, so
	// an update that states them is dropped rather than applied to the parent.
	child.handleUpdate(header, update)
	return childRouteAccepted
}

// persistAgentMessage writes a message that the agent gave its subagent into
// the child transcript (Hooks.ChildUserMessages). The caller holds feedMu.
//
// The first one opens the transcript as its prompt, which is a no-op for a
// transcript that already holds a message -- a spawn whose prompt opened it.
// Each later one is a message in the middle of the conversation. The text the
// child assembled before it is stored first, so the transcript keeps its order.
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

// FeedChildUpdate renders one session update into the transcript of the child
// that owns rowKey. A provider that reads the work of a subagent from its own
// store rather than from the stream calls it with the update that the agent
// itself would send for that record. It reports false for an unknown or refused route.
func (b *Base) FeedChildUpdate(rowKey string, update json.RawMessage) bool {
	var header acpUpdateHeader
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp child update unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "row_key", rowKey, "error", err)
		return false
	}
	return b.routeToChild(rowKey, header, update) == childRouteAccepted
}

// FinishChildTurn stores the text that the child of rowKey assembled so far,
// because its turn ended. The child stays open: a subagent that the agent
// continues later renders its next turn into the same transcript.
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

// AttachChildSession routes each update of the session sessionID into the
// transcript of the subagent that owns rowKey. An agent that runs a subagent
// in a session of its own calls it once it links that session to its row.
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

// OpenToolSink returns the services of the transcript that holds the tool call
// toolCallID open in the session sessionID: the agent's own for the current
// session, and the child's for a subagent session that a registry row routes.
// ok is false for a call that no such transcript holds open. A provider whose
// agent reports the live output of a call outside the updates of that call --
// Kiro's content chunks -- reports it through these services, so the output
// reaches the row that the call draws, in whichever tab that row is.
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

// withOpenToolConversation runs fn on the conversation that holds the tool call
// toolCallID open in the session sessionID. The conversation is one of two:
//
//   - The main conversation, for the current session. A request that states no
//     session also uses it, as ServesSession admits such a request.
//   - The conversation of a child, for a subagent session that a registry row
//     routes. Then fn runs under the feedMu of the child, as every write to a
//     child does.
//
// fn does not run when no such conversation holds the call open.
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

// childCloseCompletions states how the text and the open tool calls of a child
// end when its row closes with status. A subagent that completed wrote its
// text in full, and a tool call it still held open did not finish -- the same
// verdict a main turn gives such a call. A subagent that failed ends both with
// the error, and every other close is a stop.
func childCloseCompletions(status bgtask.Status) (text, tools agent.MessageCompletion) {
	switch status {
	case bgtask.StatusCompleted:
		return agent.MessageCompletionComplete, agent.MessageCompletionError
	case bgtask.StatusFailed:
		return agent.MessageCompletionError, agent.MessageCompletionError
	default:
		return agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted
	}
}

// finishAllChildConversations ends every child conversation, because the
// process that fed them stopped.
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

// renameChildConversation moves the child state of a row that a provider
// re-keyed, so the renamed row keeps the transcript that the old key opened.
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

// forgetChild drops every route to the subagent of a closed row. A late
// update of its session then reaches no transcript, which is the answer the
// base gives for any session it does not serve, and a late update with its tag
// stays in the parent unless identity validation refused that row.
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

// forgetChildSessions drops the route of every subagent session, because the
// session that ran those subagents is gone. A late update of one then reaches
// no transcript, and ServesSession refuses a late control request of one.
func (b *Base) forgetChildSessions() {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	b.children.sessions = nil
}

// cleanupChildAgent releases the per-agent service state of the child agent
// that this process created for rowKey, through the sink that created it. The
// transcript of the child survives.
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
