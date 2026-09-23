package acp

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type acpIncompleteTool struct {
	toolCallID string
	// original is the last frame the agent sent for this tool call, byte for byte.
	// It is what the transcript row stores.
	original []byte
	// content is the fields merged across every frame of the call. The row carries
	// the fields the original lacks as supplemental content, never as provider JSON.
	content   []byte
	rowKey    string
	encodeErr error
}

// acpToolUpdateState is what LeapMux knows about one running tool call.
//
// The Agent Client Protocol sends a tool call as an opening frame and a run of
// updates, and each frame repeats only what changed. One struct holds the last
// frame and the merge, so the two can never describe different tool calls.
type acpToolUpdateState struct {
	original json.RawMessage
	fields   map[string]json.RawMessage
	order    uint64
}

type acpTurnSnapshot struct {
	assistantText     string
	thoughtText       string
	completedToolUses int
	incompleteTools   []acpIncompleteTool
}

// acpTurnOutput protects assembled text and incomplete tool state with turnMu.
// Callers can hold session, update, or terminal lifecycle locks.
// Session replacement acquires turnMu before the protocol-state lock.
type acpTurnOutput struct {
	turnMu sync.Mutex

	turnAssistantText strings.Builder
	turnThoughtText   strings.Builder

	toolUpdateState     map[string]*acpToolUpdateState
	toolRequestContents map[string]*acpToolRequestContent
	toolSubagentRows    map[string]string
	nextToolUpdateOrder uint64
	spawnSpansReleased  map[string]struct{}
	turnToolUses        int
}

func (o *acpTurnOutput) appendAssistant(text string) {
	o.turnMu.Lock()
	// ACP ContentChunk values are append-only fragments. ACP clients concatenate
	// them directly, and a provider can split one at any text position.
	providerkit.AppendText(&o.turnAssistantText, text, providerkit.JoinVerbatim)
	o.turnMu.Unlock()
}

func (o *acpTurnOutput) appendThought(text string) (fresh bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	fresh = o.turnThoughtText.Len() == 0
	providerkit.AppendText(&o.turnThoughtText, text, providerkit.JoinVerbatim)
	return fresh
}

func (o *acpTurnOutput) takeText(kind agent.AssembledMessageKind) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	builder := &o.turnAssistantText
	if kind == agent.AssembledMessageKindReasoning {
		builder = &o.turnThoughtText
	}
	text := builder.String()
	builder.Reset()
	return text
}

func (o *acpTurnOutput) drainTurn() acpTurnSnapshot {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	return o.drainTurnLocked()
}

// replaceSession drains turn output and invokes updateProtocol while turnMu stays held.
// updateProtocol may acquire the protocol-state lock. It must not perform I/O.
func (o *acpTurnOutput) replaceSession(updateProtocol func()) acpTurnSnapshot {
	o.turnMu.Lock()
	snapshot := o.drainTurnLocked()
	updateProtocol()
	o.turnMu.Unlock()
	return snapshot
}

func (o *acpTurnOutput) resetTurnLocked() {
	o.turnAssistantText.Reset()
	o.turnThoughtText.Reset()
	o.toolUpdateState = nil
	o.toolRequestContents = nil
	o.toolSubagentRows = nil
	o.nextToolUpdateOrder = 0
	o.spawnSpansReleased = nil
	o.turnToolUses = 0
}

func (o *acpTurnOutput) drainTurnLocked() acpTurnSnapshot {
	snapshot := acpTurnSnapshot{
		assistantText:     o.turnAssistantText.String(),
		thoughtText:       o.turnThoughtText.String(),
		completedToolUses: o.turnToolUses,
		incompleteTools:   make([]acpIncompleteTool, 0, len(o.toolUpdateState)),
	}
	for toolCallID, state := range o.toolUpdateState {
		encoded, err := json.Marshal(state.fields)
		snapshot.incompleteTools = append(snapshot.incompleteTools, acpIncompleteTool{
			toolCallID: toolCallID,
			original:   state.original,
			content:    encoded,
			rowKey:     o.toolSubagentRows[toolCallID],
			encodeErr:  err,
		})
	}
	sort.Slice(snapshot.incompleteTools, func(left, right int) bool {
		leftID := snapshot.incompleteTools[left].toolCallID
		rightID := snapshot.incompleteTools[right].toolCallID
		leftOrder := o.toolUpdateState[leftID].order
		rightOrder := o.toolUpdateState[rightID].order
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return leftID < rightID
	})
	o.resetTurnLocked()
	return snapshot
}

// rememberIncompleteTool records the opening frame of a tool call. An earlier frame
// wins each field, because this runs for the frame that OPENS the call.
func (o *acpTurnOutput) rememberIncompleteTool(toolCallID string, incoming map[string]json.RawMessage, original json.RawMessage) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	state := o.toolUpdateStateLocked(toolCallID, len(incoming))
	state.original = append(json.RawMessage(nil), original...)
	for key, value := range incoming {
		if _, exists := state.fields[key]; !exists {
			state.fields[key] = append(json.RawMessage(nil), value...)
		}
	}
}

// toolUpdateStateLocked returns the state for one tool call, creating it on the
// first frame. The creation order is the order the rows persist in, so a turn that
// ends with several open calls lists them as the agent opened them.
func (o *acpTurnOutput) toolUpdateStateLocked(toolCallID string, size int) *acpToolUpdateState {
	if o.toolUpdateState == nil {
		o.toolUpdateState = make(map[string]*acpToolUpdateState)
	}
	state := o.toolUpdateState[toolCallID]
	if state == nil {
		state = &acpToolUpdateState{fields: make(map[string]json.RawMessage, size), order: o.nextToolUpdateOrder}
		o.toolUpdateState[toolCallID] = state
		o.nextToolUpdateOrder++
	}
	return state
}

func (o *acpTurnOutput) completeTool(toolCallID string) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	o.turnToolUses++
	delete(o.toolUpdateState, toolCallID)
	delete(o.toolRequestContents, toolCallID)
	rowKey := o.toolSubagentRows[toolCallID]
	delete(o.toolSubagentRows, toolCallID)
	delete(o.spawnSpansReleased, toolCallID)
	return rowKey
}

// mergeToolUpdate folds one update into the call's merged fields and records the
// frame that carried it. A later frame wins each field. A final update ends the
// call, so its state is removed rather than kept for the turn-end sweep.
func (o *acpTurnOutput) mergeToolUpdate(
	toolCallID string,
	incoming map[string]json.RawMessage,
	final bool,
	original json.RawMessage,
) (json.RawMessage, ToolCallUpdateEnvelope, bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	state := o.toolUpdateStateLocked(toolCallID, len(incoming))
	state.original = append(json.RawMessage(nil), original...)
	for key, value := range incoming {
		state.fields[key] = value
	}
	update, ok := decodeACPToolCallUpdate(state.fields)
	encoded, err := json.Marshal(state.fields)
	if final {
		delete(o.toolUpdateState, toolCallID)
	}
	if err != nil || !ok {
		return nil, ToolCallUpdateEnvelope{}, false
	}
	return encoded, update, true
}

func (o *acpTurnOutput) rememberSubagentRow(toolCallID, rowKey string) {
	o.turnMu.Lock()
	if o.toolSubagentRows == nil {
		o.toolSubagentRows = make(map[string]string)
	}
	o.toolSubagentRows[toolCallID] = rowKey
	o.turnMu.Unlock()
}

func (o *acpTurnOutput) markSpanReleased(toolCallID string) bool {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	if o.spawnSpansReleased == nil {
		o.spawnSpansReleased = make(map[string]struct{})
	}
	if _, exists := o.spawnSpansReleased[toolCallID]; exists {
		return false
	}
	o.spawnSpansReleased[toolCallID] = struct{}{}
	return true
}
