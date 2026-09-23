package cursor

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// cursorTodoItem is one row of a cursor/update_todos frame.
//
// The three fields are the SHARED to-do vocabulary rather than Cursor's own, so no
// contract holds them and contracts.CursorExtensionParams keeps `Todos` raw.
//
// Status is Cursor's own lowercase word -- `pending`, `in_progress`, `completed` or
// `cancelled` -- which is the normalized form. The tool call's rawInput.todos spells
// the same state as the protobuf enum name (`TODO_STATUS_IN_PROGRESS`), so this frame
// is also the half that needs no second parser.
type cursorTodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// cursorTodoFrame is the payload of cursor/update_todos.
//
// The shared half is the GENERATED contracts.CursorExtensionParams, whose json tags
// the browser reads back out of the stored frame. `Merge` stays here, because the
// worker applies the flag and the browser deliberately draws none from it, so the
// contracts rule exempts it. The embedded `Todos` stays raw, and cursorStoredTodoFrame
// decodes it into `todos` in one more step.
type cursorTodoFrame struct {
	contracts.CursorExtensionParams
	// Merge false REPLACES the list. Cursor sends it on the first update of a turn and
	// sends true afterwards, with the rows that changed. Reading a true frame as a
	// replacement would delete every row it stayed silent about.
	Merge bool `json:"merge"`
	todos []cursorTodoItem
}

// handleCursorExtension stores one `cursor/*` extension frame on the row of its tool
// call, and reports whether the frame was one this worker knows.
//
// Cursor sends one after the tool-call update that COMPLETES the same toolCallId, on
// the same stream, so the row the worker persisted for that call is the row the frame
// describes. Each frame carries fields the tool call itself does not:
//
//   - cursor/update_todos adds `merge`, which states whether the listed rows replace
//     the whole list or join it. The tool call's own rawInput.todos carries the rows
//     with no word for that, so the flag is the one thing that makes the list usable.
//   - cursor/task adds the model, the agent id and the measured duration of a subagent
//     run. rawInput carries the prompt, the description and the requested type alone.
//   - cursor/generate_image adds the path the image was WRITTEN to. rawInput carries
//     the requested filename, which the runtime does not have to honor.
//
// It NEVER refuses the frame because the write failed. Cursor keeps no state for an
// extension request and an error reply would reach a reader as a failed turn, so the
// ack is unconditional and a failed write is a log line.
func (a *Agent) handleCursorExtension(method string, params json.RawMessage) bool {
	switch method {
	case contracts.CursorMethodUpdateTodos, contracts.CursorMethodTask, contracts.CursorMethodGenerateImage:
	default:
		return false
	}
	var frame contracts.CursorExtensionParams
	if err := json.Unmarshal(params, &frame); err != nil || frame.ToolCallID == "" {
		// A frame with no tool call identifies no row. It is not an error the reader can
		// act on, so it is a trace and not a warning.
		slog.Debug("Cursor extension frame identifies no tool call", "method", method, "error", err)
		return true
	}
	if method == contracts.CursorMethodTask {
		a.noteCursorTaskExtension(frame.ToolCallID)
	}
	if a.transcript == nil {
		// Only a test builds a Cursor agent with no transcript, and it does so to
		// exercise the ack. Persisting from here would need the transcript's own
		// single-writer discipline -- see EnrichToolSpan.
		return true
	}
	build := func(original []byte) ([]byte, error) { return cursorExtensionSupplement(original, method, params) }
	written, err := a.transcript.EnrichToolSpan(frame.ToolCallID, build)
	switch {
	case err != nil:
		slog.Warn("Store the Cursor extension frame on its tool row",
			"method", method, "tool_call_id", frame.ToolCallID, "error", err)
	case !written:
		// A false with no error is a REFUSAL, not a failure: no row carries the tool
		// call, or EnrichMessage declined a write whose PreviousRevision no longer
		// matches the row. The frame is lost either way -- a `cursor/update_todos`
		// row then draws as a plain tool call with no checklist -- and this is the
		// only line that states it, because the error branch above sees nothing.
		slog.Debug("No row took the Cursor extension frame",
			"method", method, "tool_call_id", frame.ToolCallID)
	}
	return true
}

// cursorExtensionSupplement wraps one frame for the row's supplemental content.
//
// It IDENTIFIES the frame it belongs to, like every other supplement this provider family
// stores. Without the identity keys the browser's shared gate refused the envelope --
// while the worker's own resolve took it -- so a `cursor/update_todos` row drew as a
// plain tool call with no checklist, and neither side reported anything.
func cursorExtensionSupplement(original []byte, method string, params json.RawMessage) ([]byte, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(original, &frame); err != nil {
		return nil, fmt.Errorf("decode the row's own frame: %w", err)
	}
	stored, err := json.Marshal(contracts.CursorStoredExtension{Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode the stored frame: %w", err)
	}
	supplement := acp.NewToolSupplement(frame)
	supplement[contracts.CursorSupplementExtension] = stored
	encoded, err := json.Marshal(supplement)
	if err != nil {
		return nil, fmt.Errorf("encode the supplement: %w", err)
	}
	return encoded, nil
}

// ExtractTodoEvent reads Cursor's to-do list off one persisted message.
//
// The list reaches the row as a SUPPLEMENT, not in the provider's own message: the
// tool call carries the rows and the cursor/update_todos frame that follows it carries
// the `merge` flag, and a list without that flag cannot be applied. ResolveProviderData
// puts the stored frame back into the content this reads.
//
// It DELEGATES to the embedded Provider for every other message. Cursor speaks ACP,
// so an ACP plan notification still reads as one.
func (p cursorProvider) ExtractTodoEvent(spanType string, content []byte, paired func() []byte) (todoevents.Event, bool) {
	frame, ok := cursorStoredTodoFrame(content)
	if !ok {
		return p.Provider.ExtractTodoEvent(spanType, content, paired)
	}
	items := make([]todoevents.Item, 0, len(frame.todos))
	for _, todo := range frame.todos {
		if todo.ID == "" {
			continue
		}
		items = append(items, todoevents.Item{
			ID:      todo.ID,
			Content: todo.Content,
			Status:  todoevents.StatusFromProviderWord(todo.Status),
		})
	}
	// A frame that listed rows and yielded none states nothing about the list: every
	// row it carried had no id, which the loop above skips. A REPLACEMENT built from
	// it deletes every row and broadcasts an empty checklist, so a single malformed
	// frame wipes the list. The OpenCode sibling guards the same case.
	if len(frame.todos) > 0 && len(items) == 0 {
		return todoevents.Event{}, false
	}
	if frame.Merge {
		// An empty merge frame states that nothing changed, so it is not an event.
		// An empty REPLACEMENT frame is the real answer "the list is now empty".
		if len(items) == 0 {
			return todoevents.Event{}, false
		}
		return todoevents.Event{Kind: todoevents.KindMerge, Items: items}, true
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

// cursorStoredTodoFrame reads a stored cursor/update_todos frame out of one message.
func cursorStoredTodoFrame(content []byte) (cursorTodoFrame, bool) {
	// Indexed with the generated constant rather than a struct tag, which cannot
	// hold one. Every other reader and the writer take this key from the contract,
	// so a hand-written tag here would survive a rename and silently stop matching:
	// ExtractTodoEvent would fall through to the ACP base and Cursor's to-do list
	// would stop updating, with no build error and no log line.
	var message map[string]json.RawMessage
	if json.Unmarshal(content, &message) != nil {
		return cursorTodoFrame{}, false
	}
	stored, ok := message[contracts.CursorSupplementExtension]
	if !ok {
		return cursorTodoFrame{}, false
	}
	var extension contracts.CursorStoredExtension
	if json.Unmarshal(stored, &extension) != nil || extension.Method != contracts.CursorMethodUpdateTodos {
		return cursorTodoFrame{}, false
	}
	var frame cursorTodoFrame
	if json.Unmarshal(extension.Params, &frame) != nil {
		return cursorTodoFrame{}, false
	}
	// The generated struct keeps `Todos` raw, so the rows take one more step. A frame
	// whose rows do not decode is refused here, exactly as a frame whose outer shape
	// is wrong is refused above -- ExtractTodoEvent then falls through to the ACP
	// base for both, rather than reading a malformed list as an empty one.
	if len(frame.Todos) > 0 && json.Unmarshal(frame.Todos, &frame.todos) != nil {
		return cursorTodoFrame{}, false
	}
	return frame, true
}

// ResolveProviderData gives semantic extractors the stored extension frame.
//
// It DELEGATES to the embedded Provider and then adds Cursor's own key, so an ACP
// resolution rule added later reaches Cursor too. The shared resolver copies a fixed
// set of ACP field names and cannot carry a name only Cursor writes.
func (p cursorProvider) ResolveProviderData(content agent.MessageContent) []byte {
	resolved := p.Provider.ResolveProviderData(content)
	if len(content.Supplemental) == 0 {
		return resolved
	}
	var supplement acp.ToolSupplement
	if json.Unmarshal(content.Supplemental, &supplement) != nil {
		return resolved
	}
	extension, present := supplement[contracts.CursorSupplementExtension]
	if !present {
		return resolved
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(resolved, &fields) != nil || fields == nil {
		return resolved
	}
	// The SAME gate the shared ACP resolve applies, and the same gate the browser
	// plugin applies. A supplement stored beside one frame must not reach another.
	if !supplement.IdentityMatches(fields) {
		return resolved
	}
	fields[contracts.CursorSupplementExtension] = extension
	encoded, err := json.Marshal(fields)
	if err != nil {
		return resolved
	}
	return encoded
}
