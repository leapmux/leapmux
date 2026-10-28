package acp

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type acpIncompleteTool struct {
	target     *acpTranscriptTarget
	admission  *acpTranscriptAdmission
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
	target   *acpTranscriptTarget
	original json.RawMessage
	fields   map[string]json.RawMessage
	order    uint64
}

type acpTurnSnapshot struct {
	sessionID                                             string
	interrupted                                           bool
	cleanup                                               func()
	cleanupCaptured                                       bool
	hasPromptBoundary                                     bool
	thoughtAdmission, assistantAdmission, endingAdmission *acpTranscriptAdmission
	rootTargets                                           []*acpTranscriptTarget
	priorText                                             *agent.CapturedTranscriptBatch
	assistantMessageID                                    string
	thoughtMessageID                                      string
	assistantText                                         string
	thoughtText                                           string
	completedToolUses                                     int
	incompleteTools                                       []acpIncompleteTool
}

// acpPromptBoundary is the end of a prompt's output while an agent turn waits
// behind that prompt (Base.BeginAgentTurn). The agent started its turn, so the
// prompt's work is over, but the base processes the prompt's end later, on its
// own goroutine. Everything that the agent streams in between belongs to the
// agent turn. The boundary keeps what is still the prompt's.
type acpPromptBoundary struct {
	target *acpTranscriptTarget
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
	rootTarget, promptTarget       *acpTranscriptTarget
	foldedTargets                  []*acpTranscriptTarget
	assistantTarget, thoughtTarget *acpTranscriptTarget
	turnMu                         sync.Mutex

	turnAssistantText strings.Builder
	turnThoughtText   strings.Builder
	// Each ID belongs to its buffered text. A drain clears the text and its ID together.
	assistantMessageID string
	thoughtMessageID   string

	toolUpdateState     map[string]*acpToolUpdateState
	toolRequestContents map[string]*acpToolRequestContent
	toolSubagentRows    map[string]string
	nextToolUpdateOrder uint64
	spawnSpansReleased  map[string]struct{}
	turnToolUses        int
	// endedTools is the tool calls that a final update ended in this turn, so a
	// repeated final update counts the call no second time (see completeTool). A
	// prompt boundary keeps it, because a call of the prompt that ended before the
	// boundary must not count for the agent turn after it.
	endedTools map[string]struct{}
	// promptBoundary is set while an agent turn waits behind a prompt, and nil
	// otherwise.
	promptBoundary *acpPromptBoundary
}

func (o *acpTurnOutput) appendAssistant(text, messageID string) {
	o.turnMu.Lock()
	if o.turnAssistantText.Len() == 0 {
		o.assistantTarget = o.rootTarget
	}
	if messageID != "" && (o.turnAssistantText.Len() == 0 || o.assistantMessageID == "") {
		o.assistantMessageID = messageID
	}
	// ACP ContentChunk values are append-only fragments. ACP clients concatenate
	// them directly, and a provider can split one at any text position.
	providerkit.AppendText(&o.turnAssistantText, text, providerkit.JoinVerbatim)
	o.turnMu.Unlock()
}

func (o *acpTurnOutput) appendThought(text, messageID string) (fresh bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	fresh = o.turnThoughtText.Len() == 0
	if fresh {
		o.thoughtTarget = o.rootTarget
	}
	if messageID != "" && (fresh || o.thoughtMessageID == "") {
		o.thoughtMessageID = messageID
	}
	providerkit.AppendText(&o.turnThoughtText, text, providerkit.JoinVerbatim)
	return fresh
}

// switchMessage identifies a boundary before a chunk of another known message.
// The old ID stays with buffered text until the caller takes that text.
// An absent ID continues the segment. Its first explicit ID also identifies any preceding untagged chunks.
func (o *acpTurnOutput) switchMessage(kind agent.AssembledMessageKind, id string) (flushID string, flush bool) {
	if id == "" {
		return "", false
	}
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	current, buffered := &o.assistantMessageID, &o.turnAssistantText
	if kind == agent.AssembledMessageKindReasoning {
		current, buffered = &o.thoughtMessageID, &o.turnThoughtText
	}
	if buffered.Len() > 0 && *current != "" && *current != id {
		return *current, true
	}
	*current = id
	return "", false
}

func (o *acpTurnOutput) takeText(kind agent.AssembledMessageKind) (text, messageID string) {
	text, messageID, admission := o.takeOwnedText(kind)
	admission.release()
	return text, messageID
}

func (o *acpTurnOutput) takeOwnedText(kind agent.AssembledMessageKind) (text, messageID string, admission *acpTranscriptAdmission) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	builder, id, target := &o.turnAssistantText, &o.assistantMessageID, &o.assistantTarget
	if kind == agent.AssembledMessageKindReasoning {
		builder, id, target = &o.turnThoughtText, &o.thoughtMessageID, &o.thoughtTarget
	}
	text, messageID = builder.String(), *id
	if text != "" {
		admission = (*target).reserve()
	}
	builder.Reset()
	*id = ""
	*target = nil
	return text, messageID, admission
}

// takeBufferedTextLocked takes both text segments and their identities. The caller holds turnMu.
func (o *acpTurnOutput) takeBufferedTextLocked() acpTurnSnapshot {
	snapshot := acpTurnSnapshot{assistantText: o.turnAssistantText.String(), thoughtText: o.turnThoughtText.String(), assistantMessageID: o.assistantMessageID, thoughtMessageID: o.thoughtMessageID}
	// Reserve in render order before detaching the selected bytes.
	if snapshot.thoughtText != "" {
		snapshot.thoughtAdmission = o.thoughtTarget.reserve()
	}
	if snapshot.assistantText != "" {
		snapshot.assistantAdmission = o.assistantTarget.reserve()
	}
	o.turnAssistantText.Reset()
	o.turnThoughtText.Reset()
	o.assistantMessageID, o.thoughtMessageID = "", ""
	o.assistantTarget, o.thoughtTarget = nil, nil
	return snapshot
}

func (turn acpTurnSnapshot) releaseTranscriptAdmissions() {
	turn.thoughtAdmission.release()
	turn.assistantAdmission.release()
	for _, tool := range turn.incompleteTools {
		tool.admission.release()
	}
	turn.endingAdmission.release()
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
// that states something (see requestFieldStates).
func toolRequestStatesInput(state *acpToolUpdateState) bool {
	return requestFieldStates(state.fields[contracts.ACPSupplementRequestRawInput])
}

// acpLateInputKeys are the request fields that a frame other than the call's
// own can state late: the input of the call. The title and the kind identify
// the call, and the frames of the call state them. The schema requires a title
// in a tool_call (ToolCall.title). A permission request words the title for its
// dialog, and it can state another kind: Goose 1.53 opens a write with no kind
// and asks for it with `kind: "other"`. So a permission request never fills
// the title or the kind.
var acpLateInputKeys = []string{contracts.ACPSupplementRequestRawInput, contracts.ACPSupplementRequestLocations}

// unstatedInputFields returns the input fields of offered (see acpLateInputKeys)
// that the open call toolCallID never stated in a frame of its own. It returns
// only a field that states something (see requestFieldStates). It returns nil
// when the call is not open, and when the call stated each field that offered
// states.
//
// The function reads the fields that the frames of the call merged
// (acpToolUpdateState), never the supplement of the request row. The caller
// folds the returned fields into that supplement only. So the call still
// states no input to latestOpenToolWithoutInput, and a later fs/* request of
// the host still folds the complete arguments into the row.
func (o *acpTurnOutput) unstatedInputFields(toolCallID string, offered map[string]json.RawMessage) map[string]json.RawMessage {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	state := o.toolUpdateState[toolCallID]
	if state == nil {
		return nil
	}
	var unstated map[string]json.RawMessage
	for _, key := range acpLateInputKeys {
		value := offered[key]
		if !requestFieldStates(value) || requestFieldStates(state.fields[key]) {
			continue
		}
		if unstated == nil {
			unstated = make(map[string]json.RawMessage, len(acpLateInputKeys))
		}
		unstated[key] = value
	}
	return unstated
}

// requestFieldStates reports whether one request field of a frame states
// something that a reader can use. These values state nothing:
//
//   - An absent field.
//   - null.
//   - An empty string.
//   - An empty object. OpenCode sends it as the rawInput of a call that it
//     fills later.
//   - An empty array.
//
// Every other value states something, a scalar input included. raw holds one
// JSON value, as json.Unmarshal leaves each field of a map of json.RawMessage.
// The function reads the bytes at the two ends of the value and decodes
// nothing, because a rawInput can hold the whole text of a file.
func requestFieldStates(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		return false
	}
	switch value[0] {
	case 'n':
		return false
	case '"':
		return len(value) > len(`""`)
	case '{', '[':
		return len(value) > 2 && len(bytes.TrimSpace(value[1:len(value)-1])) > 0
	default:
		return true
	}
}

// markPromptBoundaryLocked records the end of the prompt's output, because an
// agent turn now waits behind the prompt. It returns the prompt's text, which
// the caller persists at once, so the agent turn's text starts a segment of its
// own. The tool calls that are open now stay the prompt's, and drainPromptTurn
// closes them at the prompt's end. A second call before that end changes
// nothing: one agent turn at most waits behind a prompt. The caller holds
// turnMu.
func (o *acpTurnOutput) markPromptBoundaryLocked() acpTurnSnapshot {
	if o.promptBoundary != nil {
		return acpTurnSnapshot{}
	}
	boundary := &acpPromptBoundary{
		target:            o.promptTarget,
		completedToolUses: o.turnToolUses,
		openTools:         make(map[string]struct{}, len(o.toolUpdateState)),
	}
	for toolCallID := range o.toolUpdateState {
		boundary.openTools[toolCallID] = struct{}{}
	}
	o.promptBoundary = boundary
	o.turnToolUses = 0
	return o.takeBufferedTextLocked()
}

// takeUnownedOutputLocked ends the output that no turn owns, because a turn
// starts now. That output is what the base recorded after the last turn
// drained and before this turn started. Several sources reach it:
//
//   - The replay of a session/load. The agent sends it before its reply, and
//     the base dispatches it after the startup, while no prompt runs.
//   - A chunk that trails the end of a turn.
//   - Content that an agent sends while it is idle.
//
// It returns the buffered text, which the caller persists as segments of their
// own before the turn writes anything. Without this, the first segment of the
// turn started with that text, and one row held both. It also drops the count
// of the tool calls that completed meanwhile: each of them already has its row,
// and the turn-end row counts the tools of its own turn only. A tool call that
// is still open stays open, because only its own update can end it. The caller
// holds turnMu.
func (o *acpTurnOutput) takeUnownedOutputLocked() acpTurnSnapshot {
	snapshot := o.takeBufferedTextLocked()
	o.turnToolUses = 0
	o.endedTools = nil
	return snapshot
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
	if o.rootTarget != nil && o.rootTarget != boundary.target {
		o.foldedTargets = appendRootTarget(o.foldedTargets, o.rootTarget)
	}
	o.rootTarget = boundary.target
	o.promptBoundary = nil
	return true
}

// drainPromptTurn drains the output of the prompt that ends now. With no
// boundary, that is the whole turn output. With a boundary, it is only the tool
// calls that the prompt left open, and hasPromptBoundary is true: the prompt's text was
// persisted at the boundary, and everything else belongs to the agent turn that
// waits, which keeps it.
func (o *acpTurnOutput) drainPromptTurn() (snapshot acpTurnSnapshot, hasPromptBoundary bool) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	return o.drainPromptTurnLocked()
}

func (o *acpTurnOutput) drainPromptTurnLocked() (snapshot acpTurnSnapshot, hasPromptBoundary bool) {
	boundary := o.promptBoundary
	if boundary == nil {
		return o.drainTurnLocked(), false
	}
	o.promptBoundary = nil
	snapshot.hasPromptBoundary = true
	snapshot.completedToolUses = boundary.completedToolUses
	for toolCallID := range boundary.openTools {
		if _, open := o.toolUpdateState[toolCallID]; open {
			snapshot.incompleteTools = append(snapshot.incompleteTools, o.incompleteToolLocked(toolCallID))
		}
	}
	o.sortIncompleteToolsLocked(snapshot.incompleteTools)
	for i := range snapshot.incompleteTools {
		snapshot.incompleteTools[i].admission = snapshot.incompleteTools[i].target.reserve()
	}
	snapshot.endingAdmission = boundary.target.reserve()
	snapshot.rootTargets = appendRootTarget(nil, boundary.target)
	for _, target := range o.foldedTargets {
		snapshot.rootTargets = appendRootTarget(snapshot.rootTargets, target)
	}
	o.foldedTargets = nil
	o.promptTarget = nil
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

func (o *acpTurnOutput) drainTurnLocked() acpTurnSnapshot {
	snapshot := o.takeBufferedTextLocked()
	snapshot.completedToolUses = o.turnToolUses
	snapshot.incompleteTools = make([]acpIncompleteTool, 0, len(o.toolUpdateState))
	for toolCallID := range o.toolUpdateState {
		snapshot.incompleteTools = append(snapshot.incompleteTools, o.incompleteToolLocked(toolCallID))
	}
	o.sortIncompleteToolsLocked(snapshot.incompleteTools)
	for i := range snapshot.incompleteTools {
		snapshot.incompleteTools[i].admission = snapshot.incompleteTools[i].target.reserve()
	}
	snapshot.rootTargets = appendRootTarget(nil, o.promptTarget)
	snapshot.rootTargets = appendRootTarget(snapshot.rootTargets, o.rootTarget)
	if o.promptBoundary != nil {
		snapshot.rootTargets = appendRootTarget(snapshot.rootTargets, o.promptBoundary.target)
	}
	for _, target := range o.foldedTargets {
		snapshot.rootTargets = appendRootTarget(snapshot.rootTargets, target)
	}
	endingTarget := o.promptTarget
	if endingTarget == nil {
		endingTarget = o.rootTarget
	}
	snapshot.endingAdmission = endingTarget.reserve()
	o.rootTarget, o.promptTarget = nil, nil
	o.foldedTargets = nil
	o.promptBoundary = nil
	o.toolUpdateState = nil
	o.toolRequestContents = nil
	o.toolSubagentRows = nil
	o.nextToolUpdateOrder = 0
	o.spawnSpansReleased = nil
	o.turnToolUses = 0
	o.endedTools = nil
	return snapshot
}

// incompleteToolLocked returns the row data of one open tool call.
func (o *acpTurnOutput) incompleteToolLocked(toolCallID string) acpIncompleteTool {
	state := o.toolUpdateState[toolCallID]
	encoded, err := json.Marshal(state.fields)
	return acpIncompleteTool{
		toolCallID: toolCallID,
		target:     state.target,
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
func (o *acpTurnOutput) rememberIncompleteTool(toolCallID string, incoming map[string]json.RawMessage, original json.RawMessage, target *acpTranscriptTarget) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	state := o.toolUpdateStateLocked(toolCallID, len(incoming))
	state.target = target
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
//
// A call counts once. An agent can repeat the final update of one call: Junie
// sends its own `failed` update for a command that a stop ended, then one more
// for each exit report of the client terminal.
func (o *acpTurnOutput) completeTool(toolCallID string) string {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	if _, ended := o.endedTools[toolCallID]; ended {
		return o.forgetToolLocked(toolCallID)
	}
	if o.endedTools == nil {
		o.endedTools = make(map[string]struct{})
	}
	o.endedTools[toolCallID] = struct{}{}
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

func (o *acpTurnOutput) reserveCurrentTranscript() *acpTranscriptAdmission {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	return o.rootTarget.reserve()
}

func (o *acpTurnOutput) reserveToolTranscript(toolCallID string, opening bool) (*acpTranscriptTarget, *acpTranscriptAdmission) {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	target := o.rootTarget
	if state := o.toolUpdateState[toolCallID]; state != nil {
		target = state.target
	} else if !opening {
		target = nil
	}
	return target, target.reserve()
}
