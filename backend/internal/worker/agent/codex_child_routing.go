package agent

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

// This file routes Codex child events and replays events that arrived before a route.

type codexCollabAgentState struct {
	Status string `json:"status"`
}

type codexCollabAgentToolCall struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
	// Prompt is the instruction the spawned agent was given. Codex declares it
	// `prompt: string | null` on the collabAgentToolCall thread item, so it is
	// absent for the non-spawn collab tools (send/wait).
	Prompt string `json:"prompt"`
	// Both tags are contract constants the browser plugin reads off the same frame
	// (contracts/codex-protocol.json `collabItem`), pinned by
	// TestSupplementTagsMatchTheContract.
	ReceiverThreadIds []string                         `json:"receiverThreadIds"`
	AgentsStates      map[string]codexCollabAgentState `json:"agentsStates"`
}

func parseCollabToolCall(item json.RawMessage) *codexCollabAgentToolCall {
	var collab codexCollabAgentToolCall
	err := json.Unmarshal(item, &collab)
	if err == nil {
		return &collab
	}
	// A WRONGLY TYPED field is not a broken item. encoding/json fills every field it
	// could read and reports the one it could not, so the id and the receiver list are
	// already correct here. Discarding the value dropped the whole spawn -- no
	// background-task row and no child transcript route -- because one agent state
	// carried a number where a word belongs. The browser reads the same frame field by
	// field and still draws the run card, so the two sides disagreed about whether a
	// subagent exists. Keep what decoded; a SYNTAX error is different, because then no
	// field was read at all.
	// `Field` identifies the field that failed, and it is EMPTY when the whole value
	// is the wrong type -- an array where the object belongs. Nothing decoded in that
	// case, so the empty name separates "one field is missing" from "there is no item".
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		slog.Warn("codex collab tool call field skipped", "field", typeErr.Field, "error", err)
		return &collab
	}
	slog.Warn("codex collab tool call unmarshal failed", "error", err)
	return nil
}

// registerCollabReceiver records one legacy spawn route. Multi-Agent V2 later
// replaces these identity fields from its authoritative started activity.
func (a *CodexAgent) registerCollabReceiver(threadID, spawnCorrelationID, parentThreadID string) bool {
	if threadID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.codexChildStateLocked(threadID)
	changed := state.phase != codexChildRunning
	if state.spawnCorrelationID == "" && spawnCorrelationID != "" {
		state.spawnCorrelationID = spawnCorrelationID
		changed = true
	}
	if state.parentThreadID == "" {
		if parentThreadID == "" {
			parentThreadID = a.threadID
		}
		state.parentThreadID = parentThreadID
		changed = true
	}
	if changed {
		a.invalidateCodexChildRoutesLocked()
	}
	if state.phase != codexChildClosing {
		a.activateCodexChildStateLocked(state)
	}
	return changed
}

func (a *CodexAgent) registerCollabReceivers(
	collab *codexCollabAgentToolCall,
	spawnCorrelationID, parentThreadID string,
) {
	if collab == nil {
		return
	}
	for _, receiverID := range collab.ReceiverThreadIds {
		if collab.Tool == codexCollabToolSpawnAgent {
			a.recordCollabChildPromptTitle(receiverID, collab.Prompt)
			a.rememberCollabChildPrompt(receiverID, collab.Prompt)
			a.registerCollabReceiver(receiverID, spawnCorrelationID, parentThreadID)
			continue
		}
		// A wait, message, resume, or close call proves activity only. Its call
		// ID and sender do not identify the receiver's spawn or direct parent.
		a.activateCollabChild(receiverID)
	}
}

// lookupCodexChildRoute reads an existing route without creating transcript
// state. Call ensureCodexChildRoute when the current lifecycle event permits
// child creation.
func (a *CodexAgent) lookupCodexChildRoute(threadID string) (codexChildRoute, bool) {
	if threadID == "" || a.isMainThreadID(threadID) {
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	state, ok := a.collabChildren[threadID]
	if !ok || state == nil || state.childAgentID == "" {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	if state.resolvedRoute != nil {
		route := *state.resolvedRoute
		a.mu.Unlock()
		return route, true
	}
	childAgentID := state.childAgentID
	parentThreadID := state.parentThreadID
	a.mu.Unlock()
	parentSink, parentAgentID, ok := a.codexServicesForThread(parentThreadID)
	if !ok {
		return codexChildRoute{}, false
	}
	route := codexChildRoute{
		agentID:       childAgentID,
		parentAgentID: parentAgentID,
		parentSink:    parentSink,
		childSink:     parentSink.ChildSink(childAgentID),
	}
	a.mu.Lock()
	if a.collabChildren[threadID] != state || state.childAgentID != childAgentID {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	if state.resolvedRoute != nil {
		route = *state.resolvedRoute
	} else {
		state.resolvedRoute = &route
	}
	a.mu.Unlock()
	return route, true
}

func (a *CodexAgent) ensureCodexChildRoute(threadID string) (codexChildRoute, bool) {
	if route, ok := a.lookupCodexChildRoute(threadID); ok {
		return route, true
	}
	if threadID == "" || a.isMainThreadID(threadID) {
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state == nil || state.spawnCorrelationID == "" {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	spawnCorrelationID := state.spawnCorrelationID
	parentThreadID := state.parentThreadID
	title := state.displayTitle()
	rootThreadID := a.threadID
	a.mu.Unlock()
	parentSink, parentAgentID, ok := a.codexServicesForThread(parentThreadID)
	if !ok {
		return codexChildRoute{}, false
	}
	childID, err := parentSink.EnsureChildAgent(spawnCorrelationID, threadID, title)
	if err != nil {
		slog.Warn("codex route child ensure failed", "thread", threadID, "error", err)
		return codexChildRoute{}, false
	}
	a.mu.Lock()
	if a.threadID != rootThreadID || a.collabChildren[threadID] != state {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	if state.resolvedRoute != nil {
		route := *state.resolvedRoute
		a.mu.Unlock()
		return route, true
	}
	if state.childAgentID == "" {
		state.childAgentID = childID
	} else {
		childID = state.childAgentID
	}
	a.mu.Unlock()
	route := codexChildRoute{
		agentID:       childID,
		parentAgentID: parentAgentID,
		parentSink:    parentSink,
		childSink:     parentSink.ChildSink(childID),
	}
	a.mu.Lock()
	if a.threadID != rootThreadID || a.collabChildren[threadID] != state || state.childAgentID != childID {
		a.mu.Unlock()
		return codexChildRoute{}, false
	}
	if state.resolvedRoute != nil {
		route = *state.resolvedRoute
		a.mu.Unlock()
		return route, true
	}
	state.resolvedRoute = &route
	a.mu.Unlock()
	return route, true
}

// codexServicesForThread resolves a thread's transcript sink. It walks the
// recorded parent chain before it touches sink caches, so malformed cycles or
// incomplete nested routes fail without creating a sink under the wrong parent.
func (a *CodexAgent) codexServicesForThread(threadID string) (ProviderServices, string, bool) {
	a.mu.Lock()
	mainThreadID := a.threadID
	if threadID == "" {
		threadID = mainThreadID
	}
	if threadID == mainThreadID {
		a.mu.Unlock()
		return a.sink, a.agentID, true
	}
	var agentIDs []string
	seen := make(map[string]struct{})
	for threadID != "" && threadID != mainThreadID {
		if _, duplicate := seen[threadID]; duplicate {
			a.mu.Unlock()
			return nil, "", false
		}
		seen[threadID] = struct{}{}
		state, ok := a.collabChildren[threadID]
		if !ok || state == nil || state.childAgentID == "" {
			a.mu.Unlock()
			return nil, "", false
		}
		agentIDs = append(agentIDs, state.childAgentID)
		threadID = state.parentThreadID
		if threadID == "" {
			threadID = mainThreadID
		}
	}
	a.mu.Unlock()

	sink := a.sink
	for i := len(agentIDs) - 1; i >= 0; i-- {
		sink = sink.ChildSink(agentIDs[i])
	}
	return sink, agentIDs[0], true
}

func (a *CodexAgent) rememberCodexChildItemThread(itemID, threadID string) {
	if itemID == "" || threadID == "" {
		return
	}
	a.mu.Lock()
	if a.collabChildItems == nil {
		a.collabChildItems = make(map[string]string)
	}
	a.collabChildItems[itemID] = threadID
	a.mu.Unlock()
}

func (a *CodexAgent) enqueuePendingCodexChildEvent(threadID string, event codexPendingChildEvent) {
	if threadID == "" {
		return
	}
	size := len(event.raw) + len(event.params)
	a.mu.Lock()
	state := a.codexChildStateLocked(threadID)
	if len(state.pendingEvents) >= codexPendingChildEventLimit ||
		state.pendingEventBytes+size > codexPendingChildEventBytesLimit {
		firstDrop := !state.pendingOutputDropped
		state.pendingOutputDropped = true
		a.mu.Unlock()
		if firstDrop {
			slog.Warn("codex pending child event limit reached", "thread", threadID)
		}
		return
	}
	state.pendingEvents = append(state.pendingEvents, event)
	state.pendingEventBytes += size
	a.mu.Unlock()
}

func (a *CodexAgent) replayPendingCodexChildEvents(threadID string, route codexChildRoute) {
	a.mu.Lock()
	state := a.collabChildren[threadID]
	if state == nil || len(state.pendingEvents) == 0 && !state.pendingOutputDropped {
		a.mu.Unlock()
		return
	}
	events := state.pendingEvents
	dropped := state.pendingOutputDropped
	state.pendingEvents = nil
	state.pendingEventBytes = 0
	state.pendingOutputDropped = false
	a.mu.Unlock()

	if dropped {
		route.childSink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: "Some Codex subagent events exceeded the pending-route limit.",
		})
	}
	for _, event := range events {
		switch event.kind {
		case codexPendingItemStarted, codexPendingItemCompleted:
			itemEvent, ok := newCodexItemEvent(event.raw, event.params)
			if !ok {
				continue
			}
			if event.kind == codexPendingItemStarted {
				a.handleCodexItemStartedForSink(route.childSink, route.agentID, false, itemEvent)
			} else {
				a.handleCodexItemCompletedForSink(route.childSink, route.agentID, false, itemEvent)
				a.persistCodexChildReport(route, itemEvent)
			}
		case codexPendingTurnStarted:
			var value struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(event.params, &value) == nil && value.Turn.ID != "" {
				a.handleChildTurnStarted(threadID, value.Turn.ID, route)
			}
		case codexPendingTurnCompleted:
			a.handleChildTurnCompleted(threadID, event.params, route)
		case codexPendingHookCompleted, codexPendingMcpOauthCompleted, codexPendingMcpStartupUpdated:
			a.persistCodexFailureForSink(route.childSink, route.agentID, event.kind, event.raw)
		}
	}
}
