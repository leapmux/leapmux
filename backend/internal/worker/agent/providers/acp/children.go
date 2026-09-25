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
	// no update, so a late update stays in the parent, and no chunk reads the
	// registry again. A row that this process opens again through
	// rememberChildAgent leaves it.
	//
	// It holds no row that the registry does not know. A provider can create a
	// row later through a route that the base does not see (Qwen Code reads a
	// background subagent from its store), and a kept miss would lose that
	// child's transcript.
	closed map[string]struct{}
}

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
	defer b.children.mu.Unlock()
	if b.children.agents == nil {
		b.children.agents = make(map[string]childAgentRef)
	}
	b.children.agents[rowKey] = childAgentRef{id: childAgentID, parent: parent}
	delete(b.children.closed, rowKey)
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
	if child := b.children.active[rowKey]; child != nil {
		b.children.mu.Unlock()
		return child
	}
	if _, closed := b.children.closed[rowKey]; closed {
		b.children.mu.Unlock()
		return nil
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
	defer b.children.mu.Unlock()
	// Another goroutine can open the same child while the registry read runs.
	// The first one wins, so one row never renders into two conversations.
	if child := b.children.active[rowKey]; child != nil {
		return child
	}
	if b.children.active == nil {
		b.children.active = make(map[string]*childConversation)
	}
	child := &childConversation{
		conversation: conversation{
			b:            b,
			childSink:    ref.parent.ChildSink(ref.id),
			childAgentID: ref.id,
			out:          &acpTurnOutput{},
		},
		parent: ref.parent,
	}
	b.children.active[rowKey] = child
	return child
}

// routeToChild renders one update into the transcript of the child that owns
// rowKey. It reports false when the row identifies no child transcript, and
// the caller then decides where the update belongs.
func (b *Base) routeToChild(rowKey string, header acpUpdateHeader, update json.RawMessage) bool {
	child := b.childFor(rowKey)
	if child == nil {
		return false
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	if header.SessionUpdate == contracts.ACPUpdateUserMessageChunk && b.hooks.ChildUserMessages {
		child.persistAgentMessage(header.Content)
		return true
	}
	// A child renders its conversation and nothing else: the mode, the model,
	// the command set and the usage of the session belong to the main one, so
	// an update that states them is dropped rather than applied to the parent.
	child.handleUpdate(header, update)
	return true
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
// itself would send for that record. It reports false when the row identifies
// no child transcript.
func (b *Base) FeedChildUpdate(rowKey string, update json.RawMessage) bool {
	var header acpUpdateHeader
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp child update unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "row_key", rowKey, "error", err)
		return false
	}
	return b.routeToChild(rowKey, header, update)
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
	b.children.sessions[sessionID] = rowKey
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

// childSessionRow returns the registry row key of the subagent that runs in
// sessionID, or "" for a session that no subagent owns.
func (b *Base) childSessionRow(sessionID string) string {
	b.children.mu.Lock()
	defer b.children.mu.Unlock()
	return b.children.sessions[sessionID]
}

// finishChildConversation ends the child conversation of a row that closes
// with status. The text that it assembled is stored complete, and each tool
// call that it left open is stored with the completion that status states.
func (b *Base) finishChildConversation(rowKey string, status bgtask.Status) {
	b.children.mu.Lock()
	child := b.children.active[rowKey]
	delete(b.children.active, rowKey)
	b.children.mu.Unlock()
	if child == nil {
		return
	}
	child.feedMu.Lock()
	defer child.feedMu.Unlock()
	turn := child.out.drainTurn()
	textCompletion, toolCompletion := childCloseCompletions(status)
	child.persistAssembledText(agent.AssembledMessageKindReasoning, turn.thoughtText, textCompletion)
	child.persistAssembledText(agent.AssembledMessageKindText, turn.assistantText, textCompletion)
	child.persistIncompleteTools(turn.incompleteTools, toolCompletion)
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
	b.children.mu.Lock()
	rowKeys := make([]string, 0, len(b.children.active))
	for rowKey := range b.children.active {
		rowKeys = append(rowKeys, rowKey)
	}
	b.children.mu.Unlock()
	for _, rowKey := range rowKeys {
		b.finishChildConversation(rowKey, bgtask.StatusStopped)
	}
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
	for sessionID, rowKey := range b.children.sessions {
		if rowKey == from {
			b.children.sessions[sessionID] = to
		}
	}
}

// forgetChild drops every route to the subagent of a closed row. A late
// update of its session then reaches no transcript, which is the answer the
// base gives for any session it does not serve, and a late update with its tag
// stays in the parent.
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
