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

// acpPromptBoundary is the end of a prompt's output while an agent turn waits
// behind that prompt (Base.BeginAgentTurn). The agent started its turn, so the
// prompt's work is over, but the base processes the prompt's end later, on its
// own goroutine. Everything that the agent streams in between belongs to the
// agent turn. The boundary keeps what is still the prompt's.
type acpPromptBoundary struct {
	// completedToolUses counts the prompt's tool calls that completed, including
	// a call of the prompt that completes after the boundary.
	completedToolUses int
	// openTools is the tool calls that the prompt left open at the boundary.
	openTools map[string]struct{}
}

// acpTurnOutput protects assembled text and incomplete tool state with turnMu.
// Callers can hold session, update, or terminal lifecycle locks.
// Session replacement acquires turnMu before the protocol-state lock.
type acpTurnOutput struct {
	turnMu sync.Mutex

	turnAssistantText strings.Builder
	turnThoughtText   strings.Builder
	// assistantMessageID and thoughtMessageID identify the message of the last
	// chunk of each kind, for a provider that states it (Hooks.ChunkMessageID).
	// "" until such a chunk arrives in the turn.
	assistantMessageID string
	thoughtMessageID   string

	toolUpdateState     map[string]*acpToolUpdateState
	toolRequestContents map[string]*acpToolRequestContent
	toolSubagentRows    map[string]string
	nextToolUpdateOrder uint64
	spawnSpansReleased  map[string]struct{}
	turnToolUses        int
	// promptBoundary is set while an agent turn waits behind a prompt, and nil
	// otherwise.
	promptBoundary *acpPromptBoundary
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

// switchMessage records id as the message of the next chunk of kind. It reports
// whether the buffered text of that kind belongs to another message, which the
// caller then stores first. A chunk that states no id continues the buffered
// text, whatever message that text belongs to.
func (o *acpTurnOutput) switchMessage(kind agent.AssembledMessageKind, id string) bool {
	if id == "" {
		return false
	}
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	current, buffered := &o.assistantMessageID, &o.turnAssistantText
	if kind == agent.AssembledMessageKindReasoning {
		current, buffered = &o.thoughtMessageID, &o.turnThoughtText
	}
	previous := *current
	*current = id
	return previous != "" && previous != id && buffered.Len() > 0
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

// holdsOpenTool reports whether the tool call toolCallID opened and did not end.
func (o *acpTurnOutput) holdsOpenTool(toolCallID string) bool {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	_, open := o.toolUpdateState[toolCallID]
	return open
}

// latestOpenToolWithoutInput returns the id of the most recently opened tool
// call whose stored request states no raw input, or "" when every open call has
// one. A filesystem runtime opens the call and only then asks the host to read
// or write the file, so the host sees the arguments first -- as that request.
// The caller folds them into the row (see conversation.noteToolRequestFields).
func (o *acpTurnOutput) latestOpenToolWithoutInput() string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	bestID := ""
	var best uint64
	for id, state := range o.toolUpdateState {
		if toolRequestStatesInput(state) {
			continue
		}
		if bestID == "" || state.order > best {
			bestID, best = id, state.order
		}
	}
	return bestID
}

// toolRequestStatesInput reports whether a stored request carries a raw input
// with content. An empty object states no field a reader can use.
func toolRequestStatesInput(state *acpToolUpdateState) bool {
	raw := state.fields["rawInput"]
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return true // a scalar input is still an input
	}
	return len(object) > 0
}

// markPromptBoundaryLocked records the end of the prompt's output, because an
// agent turn now waits behind the prompt. It returns the prompt's text, which
// the caller persists at once, so the agent turn's text starts a segment of its
// own. The tool calls that are open now stay the prompt's, and drainPromptTurn
// closes them at the prompt's end. A second call before that end changes
// nothing: one agent turn at most waits behind a prompt. The caller holds
// turnMu.
func (o *acpTurnOutput) markPromptBoundaryLocked() (assistantText, thoughtText string) {
	if o.promptBoundary != nil {
		return "", ""
	}
	boundary := &acpPromptBoundary{
		completedToolUses: o.turnToolUses,
		openTools:         make(map[string]struct{}, len(o.toolUpdateState)),
	}
	for toolCallID := range o.toolUpdateState {
		boundary.openTools[toolCallID] = struct{}{}
	}
	o.promptBoundary = boundary
	o.turnToolUses = 0
	assistantText, thoughtText = o.turnAssistantText.String(), o.turnThoughtText.String()
	o.turnAssistantText.Reset()
	o.turnThoughtText.Reset()
	return assistantText, thoughtText
}

// dropPromptBoundaryLocked removes the boundary that a queued agent turn set,
// because that turn will not run. The output after the boundary goes back to
// the prompt, and the prompt's count of the tools that finished before the
// boundary counts again. It reports whether a boundary was there: false states
// that the prompt's end already took its part. The caller holds turnMu.
func (o *acpTurnOutput) dropPromptBoundaryLocked() bool {
	boundary := o.promptBoundary
	if boundary == nil {
		return false
	}
	o.turnToolUses += boundary.completedToolUses
	o.promptBoundary = nil
	return true
}

// drainPromptTurn drains the output of the prompt that ends now. With no
// boundary, that is the whole turn output. With a boundary, it is only the tool
// calls that the prompt left open, and bounded is true: the prompt's text was
// persisted at the boundary, and everything else belongs to the agent turn that
// waits, which keeps it.
func (o *acpTurnOutput) drainPromptTurn() (snapshot acpTurnSnapshot, bounded bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	boundary := o.promptBoundary
	if boundary == nil {
		return o.drainTurnLocked(), false
	}
	o.promptBoundary = nil
	snapshot.completedToolUses = boundary.completedToolUses
	for toolCallID := range boundary.openTools {
		if _, open := o.toolUpdateState[toolCallID]; open {
			snapshot.incompleteTools = append(snapshot.incompleteTools, o.incompleteToolLocked(toolCallID))
		}
	}
	o.sortIncompleteToolsLocked(snapshot.incompleteTools)
	for _, tool := range snapshot.incompleteTools {
		o.forgetToolLocked(tool.toolCallID)
	}
	return snapshot, true
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
	o.promptBoundary = nil
	o.turnAssistantText.Reset()
	o.turnThoughtText.Reset()
	o.assistantMessageID = ""
	o.thoughtMessageID = ""
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
	for toolCallID := range o.toolUpdateState {
		snapshot.incompleteTools = append(snapshot.incompleteTools, o.incompleteToolLocked(toolCallID))
	}
	o.sortIncompleteToolsLocked(snapshot.incompleteTools)
	o.resetTurnLocked()
	return snapshot
}

// incompleteToolLocked returns the row data of one open tool call.
func (o *acpTurnOutput) incompleteToolLocked(toolCallID string) acpIncompleteTool {
	state := o.toolUpdateState[toolCallID]
	encoded, err := json.Marshal(state.fields)
	return acpIncompleteTool{
		toolCallID: toolCallID,
		original:   state.original,
		content:    encoded,
		rowKey:     o.toolSubagentRows[toolCallID],
		encodeErr:  err,
	}
}

// sortIncompleteToolsLocked orders tools as the agent opened them. Every tool
// must still be in toolUpdateState.
func (o *acpTurnOutput) sortIncompleteToolsLocked(tools []acpIncompleteTool) {
	sort.Slice(tools, func(left, right int) bool {
		leftID := tools[left].toolCallID
		rightID := tools[right].toolCallID
		leftOrder := o.toolUpdateState[leftID].order
		rightOrder := o.toolUpdateState[rightID].order
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return leftID < rightID
	})
}

// forgetToolLocked removes every record of one tool call, and returns the row
// key of the subagent that the call spawned, or "".
func (o *acpTurnOutput) forgetToolLocked(toolCallID string) string {
	delete(o.toolUpdateState, toolCallID)
	delete(o.toolRequestContents, toolCallID)
	rowKey := o.toolSubagentRows[toolCallID]
	delete(o.toolSubagentRows, toolCallID)
	delete(o.spawnSpansReleased, toolCallID)
	return rowKey
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

// completeTool records that one tool call ended, and counts it for the turn
// that it belongs to: a call that the prompt left open at a boundary counts for
// the prompt, and any other call for the running turn.
func (o *acpTurnOutput) completeTool(toolCallID string) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	if boundary := o.promptBoundary; boundary != nil {
		if _, ofPrompt := boundary.openTools[toolCallID]; ofPrompt {
			delete(boundary.openTools, toolCallID)
			boundary.completedToolUses++
			return o.forgetToolLocked(toolCallID)
		}
	}
	o.turnToolUses++
	return o.forgetToolLocked(toolCallID)
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
