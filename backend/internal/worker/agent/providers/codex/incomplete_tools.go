package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// This file owns partial tool state and the retained supplement for an incomplete call.

func (a *Agent) rememberCodexIncompleteTool(itemID, itemType, childThreadID string, params json.RawMessage) {
	if itemID == "" {
		return
	}
	a.Mu.Lock()
	if a.incompleteTools == nil {
		a.incompleteTools = make(map[string]*codexIncompleteTool)
	}
	if a.incompleteTools[itemID] != nil {
		a.Mu.Unlock()
		return
	}
	a.incompleteTools[itemID] = &codexIncompleteTool{
		params:        append(json.RawMessage(nil), params...),
		itemType:      itemType,
		childThreadID: childThreadID,
		order:         a.incompleteToolOrder,
	}
	a.incompleteToolOrder++
	a.Mu.Unlock()
}

// codexToolOutputSoFar is everything one running call has printed, and whether
// the incomplete-output cap already dropped some of it.
func (a *Agent) codexToolOutputSoFar(itemID string) (string, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	tool := a.incompleteTools[itemID]
	if tool == nil {
		return "", false
	}
	// The `truncated` flag travels beside the text rather than inside it: the
	// browser draws its own notice, and the prefix `renderCodexToolOutput` writes
	// belongs to the PERSISTED row, whose reader parses it back out.
	//
	// The retained TAIL, not a fresh join of every event: this runs once per output
	// delta, and the sink keeps only the last bytes of what it returns anyway.
	// `persistIncompleteCodexTools` still renders the full list, once, at the end.
	return tool.outputTail, tool.outputTruncated || len(tool.outputTail) < tool.outputBytes
}

func (a *Agent) appendCodexToolOutput(itemID, output string) {
	a.Mu.Lock()
	a.appendCodexToolEventLocked(itemID, output, false)
	a.Mu.Unlock()
}

func (a *Agent) appendCodexToolEventLocked(itemID, text string, stdin bool) {
	tool := a.incompleteTools[itemID]
	if tool == nil || text == "" {
		return
	}
	if stdin {
		text = "> " + text
	}
	// The live tail advances BEFORE the retention cap below, and whatever that cap
	// decides. The cap restricts what the finished row keeps; the tail is what a
	// reader watches while the call still runs. Updating the tail after the cap's
	// early return froze it at the megabyte where the cap hit, while the byte
	// counter beside it kept climbing on every later delta.
	tool.outputTail, _ = agent.ClipTailBytes(tool.outputTail+text, codexLiveTailLimit)
	remaining := codexIncompleteOutputLimit - tool.outputBytes
	if remaining <= 0 {
		tool.outputTruncated = true
		return
	}
	if len(text) > remaining {
		// remaining is a BYTE index into a UTF-8 stream, so step back to the start
		// of the rune it lands inside. A cut in the middle of a rune stores a
		// partial one in the finished row, which renders as a replacement
		// character.
		for remaining > 0 && !utf8.RuneStart(text[remaining]) {
			remaining--
		}
		text = text[:remaining]
		tool.outputTruncated = true
		if text == "" {
			return
		}
	}
	tool.outputEvents = append(tool.outputEvents, codexToolOutputEvent{text: text})
	tool.outputBytes += len(text)
}

// codexLiveTailLimit is the longest live tail one Codex call keeps between deltas.
//
// The sink caps the tail again before it broadcasts. This cap is here so the agent
// never holds more than a window per running call, whatever the command prints.
const codexLiveTailLimit = 8192

func (a *Agent) persistIncompleteCodexTools(childThreadID string, all bool, completion agent.MessageCompletion) int {
	a.Mu.Lock()
	itemIDs := make([]string, 0, len(a.incompleteTools))
	tools := make(map[string]codexIncompleteToolSnapshot)
	for itemID, tool := range a.incompleteTools {
		if tool == nil || (!all && tool.childThreadID != childThreadID) {
			continue
		}
		itemIDs = append(itemIDs, itemID)
		tools[itemID] = codexIncompleteToolSnapshot{
			params:        append(json.RawMessage(nil), tool.params...),
			itemType:      tool.itemType,
			childThreadID: tool.childThreadID,
			output:        renderCodexToolOutput(tool.outputEvents, tool.outputTruncated),
			order:         tool.order,
		}
		delete(a.incompleteTools, itemID)
		delete(a.collabChildItems, itemID)
	}
	a.Mu.Unlock()
	if a.IsDiscardingOutput() {
		return 0
	}
	retainedCompletion := completion
	if retainedCompletion == agent.MessageCompletionComplete {
		retainedCompletion = agent.MessageCompletionInterrupted
	}
	sort.Slice(itemIDs, func(left, right int) bool {
		leftTool, rightTool := tools[itemIDs[left]], tools[itemIDs[right]]
		if leftTool.order != rightTool.order {
			return leftTool.order < rightTool.order
		}
		return itemIDs[left] < itemIDs[right]
	})
	for _, itemID := range itemIDs {
		tool := tools[itemID]
		supplement, err := buildIncompleteCodexToolSupplement(itemID, tool)
		if err != nil {
			slog.Warn("marshal incomplete codex tool", "agent_id", a.AgentID(), "item_id", itemID, "error", err)
			continue
		}
		sink := a.sink
		if tool.childThreadID != "" {
			route, ok := a.ensureCodexChildRoute(tool.childThreadID)
			if !ok {
				slog.Warn("persist incomplete codex child tool: route missing", "agent_id", a.AgentID(), "thread", tool.childThreadID)
				continue
			}
			sink = route.childSink
		}
		// The row is the agent's own item/started frame. The output arrived as a run of
		// delta events, so the joined text is recovered provider data and rides beside
		// the frame rather than inside it.
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original: tool.params, Supplemental: supplement, Completion: retainedCompletion,
		}, agent.SpanInfo{
			SpanID: itemID, SpanType: tool.itemType, Closing: true,
		}); err != nil {
			slog.Error("persist incomplete codex tool", "agent_id", a.AgentID(), "item_id", itemID, "error", err)
		}
		sink.CloseSpan(itemID)
		sink.ReportProgress(agent.CompleteOutputProgress(itemID))
	}
	return len(itemIDs)
}

// buildIncompleteCodexToolSupplement encodes the joined output, or nothing when the
// call produced none.
//
// The truncation is already inside the text: renderCodexToolOutput leads a limited
// join with the shared prefix. An earlier build also wrote an `outputTruncated` field
// into the item, which no reader ever read.
func buildIncompleteCodexToolSupplement(itemID string, tool codexIncompleteToolSnapshot) ([]byte, error) {
	if !json.Valid(tool.params) {
		return nil, fmt.Errorf("incomplete Codex tool %q has no valid frame", itemID)
	}
	if tool.output == "" {
		return nil, nil
	}
	return json.Marshal(contracts.CodexToolSupplement{
		ItemID:           itemID,
		ItemType:         tool.itemType,
		AggregatedOutput: tool.output,
	})
}

// ResolveProviderData puts the joined output back on the item it belongs to.
//
// The identity keys are checked first: a supplement that identifies another item, or
// another item type, cannot reach this row. The original bytes stay unchanged; this
// returns a resolved COPY for the extractors.
func (codexProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	var extra contracts.CodexToolSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil || extra.ItemID == "" || extra.AggregatedOutput == "" {
		return content.Original
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(content.Original, &params) != nil || params == nil {
		return content.Original
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(params[contracts.CodexItemEnvelope], &item) != nil || item == nil {
		return content.Original
	}
	var itemID, itemType string
	if json.Unmarshal(item[contracts.CodexItemID], &itemID) != nil || itemID != extra.ItemID {
		return content.Original
	}
	if json.Unmarshal(item[contracts.CodexItemType], &itemType) != nil || itemType != extra.ItemType {
		return content.Original
	}
	encodedOutput, err := json.Marshal(extra.AggregatedOutput)
	if err != nil {
		return content.Original
	}
	item[contracts.CodexItemAggregatedOutput] = encodedOutput
	encodedItem, err := json.Marshal(item)
	if err != nil {
		return content.Original
	}
	params[contracts.CodexItemEnvelope] = encodedItem
	resolved, err := json.Marshal(params)
	if err != nil {
		return content.Original
	}
	return resolved
}

func renderCodexToolOutput(events []codexToolOutputEvent, truncated bool) string {
	var output strings.Builder
	if truncated {
		output.WriteString(providerkit.LimitedOutputPrefix)
	}
	for _, event := range events {
		output.WriteString(event.text)
	}
	return output.String()
}

func codexItemIsTool(itemType string) bool {
	switch itemType {
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange, contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall, contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView:
		return true
	default:
		return false
	}
}
