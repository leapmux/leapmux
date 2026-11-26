package kimi

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var errKimiConversationNeedsSnapshot = errors.New("the Kimi conversation needs a complete snapshot")

// kimiConversationState follows one agent's native document. It performs no I/O.
// The dispatcher owns it; snapshots and patches validate before they replace state.
type kimiConversationState struct {
	sessionID     string
	agentID       string
	turns         map[string]*kimiConversationTurnState
	toolCallTurns map[string]map[string]struct{}
	removed       map[string]struct{}
}

func (state *kimiConversationState) ensureToolCallIndex() {
	if state.toolCallTurns != nil {
		return
	}
	state.toolCallTurns = make(map[string]map[string]struct{})
	for turnID, turn := range state.turns {
		for itemID, item := range turn.items {
			if item != nil && item.identity.kind == contracts.KimiConversationItemKindToolCall {
				state.indexToolCall(turnID, itemID)
			}
		}
	}
}

func (state *kimiConversationState) indexToolCall(turnID, itemID string) {
	if state.toolCallTurns == nil {
		state.toolCallTurns = make(map[string]map[string]struct{})
	}
	turns := state.toolCallTurns[itemID]
	if turns == nil {
		turns = make(map[string]struct{})
		state.toolCallTurns[itemID] = turns
	}
	turns[turnID] = struct{}{}
}

func (state *kimiConversationState) unindexToolCall(turnID, itemID string) {
	turns := state.toolCallTurns[itemID]
	if turns == nil {
		return
	}
	delete(turns, turnID)
	if len(turns) == 0 {
		delete(state.toolCallTurns, itemID)
	}
}

type kimiConversationTurnState struct {
	header            kimiConversationTurnHeader
	steps             map[int64]kimiConversationStep
	items             map[string]*kimiConversationItemState
	order             []string
	projectionPending bool
}

// Streamed strings have one accumulator each. Their fields are absent from data
// until snapshot builds a view, so a metadata patch never copies a large output.
type kimiConversationItemState struct {
	identity          kimiConversationItemIdentity
	data              *kimiConversationItem
	text              *strings.Builder
	inputText         *strings.Builder
	output            *kimiConversationToolOutput
	fullDigest        *[sha256.Size]byte
	projectionPending bool
}

type kimiConversationItemIdentity struct {
	itemID    string
	kind      string
	stepIndex *int64
}

// A running tool keeps its latest output and exact byte count. A completed
// native tool item can replace them with its full result.
type kimiConversationToolOutput struct {
	text  string
	total int64
}

func newKimiConversationToolOutput(status, text string) *kimiConversationToolOutput {
	output := &kimiConversationToolOutput{text: text, total: int64(len(text))}
	if status == contracts.KimiConversationStatusRunning {
		output.text, _ = agent.ClipTailBytes(text, kimiLiveOutputLimit)
	}
	return output
}

func (output *kimiConversationToolOutput) append(delta string) {
	output.total += int64(len(delta))
	if len(delta) >= kimiLiveOutputLimit {
		output.text, _ = agent.ClipTailBytes(delta, kimiLiveOutputLimit)
		return
	}
	output.text, _ = agent.ClipTailBytes(output.text+delta, kimiLiveOutputLimit)
}

func (output *kimiConversationToolOutput) truncated() bool {
	return output != nil && output.total > int64(len(output.text))
}

func newKimiConversationState(sessionID, agentID string) *kimiConversationState {
	return &kimiConversationState{sessionID: sessionID, agentID: agentID, turns: make(map[string]*kimiConversationTurnState), removed: make(map[string]struct{})}
}

func prepareKimiConversationItemState(previous *kimiConversationItemState, item kimiConversationItem) (*kimiConversationItemState, bool, error) {
	if err := item.validate(); err != nil {
		return nil, false, err
	}
	if previous != nil && !previous.compatible(item) {
		return nil, false, fmt.Errorf("the Kimi conversation event changes an item identity")
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, false, fmt.Errorf("encode the Kimi conversation item: %w", err)
	}
	digest := sha256.Sum256(raw)
	if previous != nil && previous.fullDigest != nil && *previous.fullDigest == digest {
		return previous, false, nil
	}
	state := &kimiConversationItemState{identity: kimiConversationItemIdentity{itemID: item.ItemID, kind: item.Type, stepIndex: item.StepIndex}, fullDigest: &digest}
	if previous != nil && state.identity.stepIndex == nil {
		state.identity.stepIndex = previous.identity.stepIndex
	}
	if item.Text != nil {
		state.text = &strings.Builder{}
		state.text.WriteString(*item.Text)
		item.Text = nil
	}
	if item.InputText != nil {
		state.inputText = &strings.Builder{}
		state.inputText.WriteString(*item.InputText)
		item.InputText = nil
	}
	if item.Type == "tool_call" && len(item.Output) != 0 {
		var output string
		if err := json.Unmarshal(item.Output, &output); err != nil {
			return nil, false, fmt.Errorf("decode the Kimi tool output: %w", err)
		}
		state.output = newKimiConversationToolOutput(item.Status, output)
		item.Output = nil
	}
	state.data = &item
	return state, true, nil
}

// Release the body after its records are retained. Keep identity and any
// authoritative fingerprint so a later native item can revise the same row.
func (state *kimiConversationItemState) releaseBody() {
	state.data = nil
	state.text, state.inputText, state.output = nil, nil, nil
}

func (turn *kimiConversationTurnState) replaceHeader(header kimiConversationTurnHeader) {
	turn.steps = make(map[int64]kimiConversationStep, len(header.Steps))
	for _, step := range header.Steps {
		turn.steps[step.Index] = step
	}
	header.Steps = nil
	turn.header = header
}

func (turn *kimiConversationTurnState) headerSnapshot() kimiConversationTurnHeader {
	header := turn.header
	header.Steps = make([]kimiConversationStep, 0, len(turn.steps))
	for _, step := range turn.steps {
		header.Steps = append(header.Steps, step)
	}
	slices.SortFunc(header.Steps, func(left, right kimiConversationStep) int { return cmp.Compare(left.Index, right.Index) })
	return header
}

// snapshot returns an independent view of the streamed strings. Later appends
// preserve the returned prefixes because strings.Builder never rewrites them.
func (state *kimiConversationItemState) snapshot() *kimiConversationItem {
	if state.data == nil {
		return nil
	}
	item := *state.data
	if state.text != nil {
		text := state.text.String()
		item.Text = &text
	}
	if state.inputText != nil {
		text := state.inputText.String()
		item.InputText = &text
	}
	if state.output != nil {
		item.Output, _ = json.Marshal(state.output.text)
	}
	return &item
}

func (state *kimiConversationItemState) compatible(item kimiConversationItem) bool {
	return state.identity.itemID == item.ItemID && state.identity.kind == item.Type &&
		(state.identity.stepIndex == nil || item.StepIndex == nil || *state.identity.stepIndex == *item.StepIndex)
}

func (state *kimiConversationItemState) append(field, delta string) error {
	if state.data == nil || state.data.Status != "running" {
		return fmt.Errorf("%w: the Kimi item delta has no running item", errKimiConversationNeedsSnapshot)
	}
	var target **strings.Builder
	switch field {
	case "text":
		if state.identity.kind != "assistant_message" && state.identity.kind != "reasoning" {
			return fmt.Errorf("the Kimi text delta targets another item kind")
		}
		target = &state.text
	case "input_text":
		if state.identity.kind != "tool_call" {
			return fmt.Errorf("the Kimi input delta targets another item kind")
		}
		target = &state.inputText
	case "output":
		if state.identity.kind != "tool_call" {
			return fmt.Errorf("the Kimi output delta targets another item kind")
		}
		if state.output == nil {
			state.output = &kimiConversationToolOutput{}
		}
		state.output.append(delta)
		state.fullDigest = nil
		return nil
	default:
		return fmt.Errorf("the Kimi item delta has an unknown field")
	}
	retainedBytes := 0
	if *target != nil {
		retainedBytes = (*target).Len()
	}
	// A text field assembled from deltas must fit the same limit as a native frame.
	// A rejected delta leaves the prior prefix intact until the next snapshot.
	if len(delta) > kimiStreamReadLimit-retainedBytes {
		return fmt.Errorf("%w: the Kimi item text exceeds the native frame limit", errKimiConversationNeedsSnapshot)
	}
	if *target == nil {
		*target = &strings.Builder{}
	}
	(*target).WriteString(delta)
	state.fullDigest = nil
	return nil
}

func (state *kimiConversationItemState) patch(set map[string]json.RawMessage) error {
	if state.data == nil || state.identity.kind != "tool_call" {
		return fmt.Errorf("%w: the Kimi item patch has no tool item", errKimiConversationNeedsSnapshot)
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	var patch kimiConversationItem
	if err := json.Unmarshal(raw, &patch); err != nil {
		return fmt.Errorf("decode the Kimi item patch: %w", err)
	}
	next := *state.data
	for field, value := range set {
		if string(value) == "null" && field != "input" && field != "display" {
			return fmt.Errorf("the Kimi item patch contains a null field")
		}
		switch field {
		case "step_index":
			next.StepIndex = patch.StepIndex
		case "debug":
			next.Debug = patch.Debug
		case "status":
			next.Status = patch.Status
		case "outcome":
			next.Outcome = patch.Outcome
		case "created_at":
			next.CreatedAt = patch.CreatedAt
		case "name":
			next.Name = patch.Name
		case "stage":
			next.Stage = patch.Stage
		case "input":
			next.Input = patch.Input
		case "input_text":
			next.InputText = patch.InputText
		case "display":
			next.Display = patch.Display
		case "output":
			next.Output = patch.Output
		case "media":
			next.Media = patch.Media
		case "error":
			next.Error = patch.Error
		case "note":
			next.Note = patch.Note
		case "fields":
			next.Fields = patch.Fields
		case "task_id":
			next.TaskID = patch.TaskID
		case "interaction_id":
			next.InteractionID = patch.InteractionID
		case "agent_refs":
			next.AgentRefs = patch.AgentRefs
		case "started_at":
			next.StartedAt = patch.StartedAt
		case "ended_at":
			next.EndedAt = patch.EndedAt
		default:
			return fmt.Errorf("the Kimi item patch contains an unknown or immutable field")
		}
	}
	if !state.compatible(next) {
		return fmt.Errorf("the Kimi item patch changes its native identity")
	}
	if err := next.validate(); err != nil {
		return err
	}
	if _, present := set["input_text"]; present {
		state.inputText = &strings.Builder{}
		state.inputText.WriteString(*next.InputText)
		next.InputText = nil
	}
	if _, present := set["output"]; present {
		var output string
		if err := json.Unmarshal(next.Output, &output); err != nil {
			return err
		}
		state.output = newKimiConversationToolOutput(next.Status, output)
		next.Output = nil
	}
	if next.StepIndex != nil {
		state.identity.stepIndex = next.StepIndex
	}
	state.data = &next
	state.fullDigest = nil
	return nil
}

func (state *kimiConversationState) replaceSnapshot(turns []kimiConversationTurn) (bool, error) {
	next := make(map[string]*kimiConversationTurnState, len(turns))
	nextToolCalls := make(map[string]map[string]struct{})
	changed := len(turns) != len(state.turns)
	for _, turn := range turns {
		if err := turn.validate(); err != nil {
			return false, err
		}
		if _, duplicate := next[turn.TurnID]; duplicate {
			return false, fmt.Errorf("the Kimi conversation snapshot repeats a turn")
		}
		previous := state.turns[turn.TurnID]
		if previous != nil && !sameKimiConversationTurnIdentity(previous.header, turn.kimiConversationTurnHeader) {
			return false, fmt.Errorf("the Kimi conversation snapshot changes a turn identity")
		}
		entry := &kimiConversationTurnState{items: make(map[string]*kimiConversationItemState, len(turn.Items))}
		entry.replaceHeader(turn.kimiConversationTurnHeader)
		if previous == nil || previous.projectionPending || len(previous.items) != len(turn.Items) ||
			!reflect.DeepEqual(previous.headerSnapshot(), entry.headerSnapshot()) {
			changed = true
		}
		for index, item := range turn.Items {
			var old *kimiConversationItemState
			if previous != nil {
				old = previous.items[item.ItemID]
			}
			value, itemChanged, err := prepareKimiConversationItemState(old, item)
			if err != nil {
				return false, err
			}
			if itemChanged || previous == nil || old == nil || old.projectionPending || old.data != nil ||
				index >= len(previous.order) || previous.order[index] != item.ItemID {
				changed = true
			}
			entry.items[item.ItemID] = value
			entry.order = append(entry.order, item.ItemID)
			if value.identity.kind == contracts.KimiConversationItemKindToolCall {
				owners := nextToolCalls[item.ItemID]
				if owners == nil {
					owners = make(map[string]struct{})
					nextToolCalls[item.ItemID] = owners
				}
				owners[turn.TurnID] = struct{}{}
			}
		}
		next[turn.TurnID] = entry
	}
	state.turns = next
	state.toolCallTurns = nextToolCalls
	clear(state.removed)
	return changed, nil
}

func sameKimiConversationTurnIdentity(left, right kimiConversationTurnHeader) bool {
	return left.TurnID == right.TurnID && left.Ordinal != nil && right.Ordinal != nil && *left.Ordinal == *right.Ordinal && left.Kind == right.Kind
}

// apply consumes a validated frame. False means the frame needs no projection.
func (state *kimiConversationState) apply(frame kimiConversationFrame) (bool, error) {
	if state.sessionID == "" || state.agentID == "" || frame.SessionID != state.sessionID || frame.AgentID != state.agentID {
		return false, fmt.Errorf("the Kimi conversation frame belongs to another owner")
	}
	switch frame.Type {
	case "turns":
		if frame.HasMore == nil || *frame.HasMore {
			return false, errKimiConversationNeedsSnapshot
		}
		_, err := state.replaceSnapshot(frame.Turns)
		return true, err
	case "turns.removed":
		state.ensureToolCallIndex()
		for _, id := range frame.TurnIDs {
			if removed := state.turns[id]; removed != nil {
				for itemID, item := range removed.items {
					if item != nil && item.identity.kind == contracts.KimiConversationItemKindToolCall {
						state.unindexToolCall(id, itemID)
					}
				}
			}
			delete(state.turns, id)
			state.removed[id] = struct{}{}
		}
		return len(frame.TurnIDs) > 0, nil
	case "turn.started", "turn.ended":
		state.ensureToolCallIndex()
		if frame.Turn == nil {
			return false, fmt.Errorf("the Kimi conversation event has no turn")
		}
		if _, removed := state.removed[frame.Turn.TurnID]; removed {
			return false, nil
		}
		entry := state.turns[frame.Turn.TurnID]
		if entry == nil {
			entry = &kimiConversationTurnState{items: make(map[string]*kimiConversationItemState)}
			state.turns[frame.Turn.TurnID] = entry
		} else if !sameKimiConversationTurnIdentity(entry.header, *frame.Turn) {
			return false, fmt.Errorf("the Kimi conversation event changes a turn identity")
		} else if reflect.DeepEqual(entry.headerSnapshot(), *frame.Turn) {
			return false, nil
		}
		entry.replaceHeader(*frame.Turn)
		return true, nil
	case "notice", "turns.failed":
		return true, nil
	}
	if _, removed := state.removed[frame.TurnID]; removed {
		return false, nil
	}
	turn := state.turns[frame.TurnID]
	if turn == nil {
		return false, fmt.Errorf("%w: the Kimi event has no matching turn", errKimiConversationNeedsSnapshot)
	}
	switch frame.Type {
	case "turn.updated":
		changed := !reflect.DeepEqual(turn.header.Retry, frame.Retry)
		if frame.Step != nil {
			previous, present := turn.steps[frame.Step.Index]
			changed = changed || !present || !reflect.DeepEqual(previous, *frame.Step)
			turn.steps[frame.Step.Index] = *frame.Step
		}
		turn.header.Retry = frame.Retry
		return changed, nil
	case "item.started", "item.ended":
		if frame.Item == nil {
			return false, fmt.Errorf("the Kimi conversation event has no item")
		}
		previous := turn.items[frame.Item.ItemID]
		next, changed, err := prepareKimiConversationItemState(previous, *frame.Item)
		if err != nil {
			return false, err
		}
		if !changed {
			return false, nil
		}
		if previous == nil {
			turn.order = append(turn.order, frame.Item.ItemID)
		}
		turn.items[frame.Item.ItemID] = next
		state.ensureToolCallIndex()
		if next.identity.kind == contracts.KimiConversationItemKindToolCall {
			state.indexToolCall(frame.TurnID, frame.Item.ItemID)
		}
		return true, nil
	case "item.removed":
		item, present := turn.items[frame.ItemID]
		if !present {
			return false, nil
		}
		state.ensureToolCallIndex()
		if item != nil && item.identity.kind == contracts.KimiConversationItemKindToolCall {
			state.unindexToolCall(frame.TurnID, frame.ItemID)
		}
		delete(turn.items, frame.ItemID)
		turn.order = slices.DeleteFunc(turn.order, func(id string) bool { return id == frame.ItemID })
		return true, nil
	case "item.updated", "item.delta":
		item := turn.items[frame.ItemID]
		if item == nil {
			return false, fmt.Errorf("%w: the Kimi event has no matching item", errKimiConversationNeedsSnapshot)
		}
		if frame.Type == "item.updated" {
			return len(frame.Set) > 0, item.patch(frame.Set)
		}
		if frame.Delta == nil || *frame.Delta == "" {
			return false, nil
		}
		return true, item.append(frame.Field, *frame.Delta)
	default:
		return false, fmt.Errorf("the Kimi conversation event has an unknown type")
	}
}
