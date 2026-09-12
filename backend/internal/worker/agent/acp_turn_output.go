package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

type acpIncompleteTool struct {
	toolCallID string
	content    []byte
	rowKey     string
	encodeErr  error
}

type acpTurnSnapshot struct {
	assistantText     string
	thoughtText       string
	completedToolUses int
	incompleteTools   []acpIncompleteTool
}

// acpTurnOutput owns text assembly and incomplete tool state for one ACP turn.
// Its lock is independent from protocol and session state. Callers never hold
// another ACP lock while they call these methods.
type acpTurnOutput struct {
	turnMu sync.Mutex

	turnAssistantText strings.Builder
	turnThoughtText   strings.Builder

	toolUpdateState     map[string]map[string]json.RawMessage
	toolRequestContents map[string]*acpToolRequestContent
	toolUpdateOrder     map[string]uint64
	toolSubagentRows    map[string]string
	nextToolUpdateOrder uint64
	spawnSpansReleased  map[string]struct{}
	turnToolUses        int
}

func (o *acpTurnOutput) appendAssistant(text string) {
	o.turnMu.Lock()
	// ACP ContentChunk values are append-only fragments. ACP clients concatenate
	// them directly, and a provider can split one at any text position.
	appendText(&o.turnAssistantText, text, joinVerbatim)
	o.turnMu.Unlock()
}

func (o *acpTurnOutput) appendThought(text string) (fresh bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	fresh = o.turnThoughtText.Len() == 0
	appendText(&o.turnThoughtText, text, joinVerbatim)
	return fresh
}

func (o *acpTurnOutput) takeText(kind AssembledMessageKind) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	builder := &o.turnAssistantText
	if kind == AssembledMessageKindReasoning {
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

// replaceSession drains the old turn before it changes protocol state.
// updateProtocol must only mutate in-memory state. It must not do I/O.
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
	o.toolUpdateOrder = nil
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
	for toolCallID, fields := range o.toolUpdateState {
		if _, present := fields["status"]; !present {
			fields["status"] = json.RawMessage(`"in_progress"`)
		}
		encoded, err := json.Marshal(fields)
		snapshot.incompleteTools = append(snapshot.incompleteTools, acpIncompleteTool{
			toolCallID: toolCallID,
			content:    encoded,
			rowKey:     o.toolSubagentRows[toolCallID],
			encodeErr:  err,
		})
	}
	sort.Slice(snapshot.incompleteTools, func(left, right int) bool {
		leftID := snapshot.incompleteTools[left].toolCallID
		rightID := snapshot.incompleteTools[right].toolCallID
		if o.toolUpdateOrder[leftID] != o.toolUpdateOrder[rightID] {
			return o.toolUpdateOrder[leftID] < o.toolUpdateOrder[rightID]
		}
		return leftID < rightID
	})
	o.resetTurnLocked()
	return snapshot
}

func (o *acpTurnOutput) rememberIncompleteTool(toolCallID string, incoming map[string]json.RawMessage) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	if o.toolUpdateState == nil {
		o.toolUpdateState = make(map[string]map[string]json.RawMessage)
	}
	state := o.toolUpdateState[toolCallID]
	if state == nil {
		state = make(map[string]json.RawMessage, len(incoming))
		o.toolUpdateState[toolCallID] = state
		if o.toolUpdateOrder == nil {
			o.toolUpdateOrder = make(map[string]uint64)
		}
		o.toolUpdateOrder[toolCallID] = o.nextToolUpdateOrder
		o.nextToolUpdateOrder++
	}
	for key, value := range incoming {
		if _, exists := state[key]; !exists {
			state[key] = append(json.RawMessage(nil), value...)
		}
	}
}

func (o *acpTurnOutput) completeTool(toolCallID string) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	o.turnToolUses++
	delete(o.toolUpdateState, toolCallID)
	delete(o.toolRequestContents, toolCallID)
	delete(o.toolUpdateOrder, toolCallID)
	rowKey := o.toolSubagentRows[toolCallID]
	delete(o.toolSubagentRows, toolCallID)
	delete(o.spawnSpansReleased, toolCallID)
	return rowKey
}

func (o *acpTurnOutput) mergeToolUpdate(
	toolCallID string,
	incoming map[string]json.RawMessage,
	final bool,
) (json.RawMessage, acpToolCallUpdateEnvelope, bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	if o.toolUpdateState == nil {
		o.toolUpdateState = make(map[string]map[string]json.RawMessage)
	}
	merged := o.toolUpdateState[toolCallID]
	existed := merged != nil
	if !existed {
		merged = make(map[string]json.RawMessage, len(incoming))
	}
	for key, value := range incoming {
		merged[key] = value
	}
	update, ok := decodeACPToolCallUpdate(merged)
	encoded, err := json.Marshal(merged)
	if final {
		delete(o.toolUpdateState, toolCallID)
		delete(o.toolUpdateOrder, toolCallID)
	} else {
		if !existed {
			if o.toolUpdateOrder == nil {
				o.toolUpdateOrder = make(map[string]uint64)
			}
			o.toolUpdateOrder[toolCallID] = o.nextToolUpdateOrder
			o.nextToolUpdateOrder++
		}
		o.toolUpdateState[toolCallID] = merged
	}
	if err != nil || !ok {
		return nil, acpToolCallUpdateEnvelope{}, false
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
