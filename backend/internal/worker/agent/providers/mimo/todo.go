package mimo

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// MiMo's to-do list is its `task` tool: each call carries ONE operation on one
// work item (create, start, block, unblock, done, abandon, rename), or reads the
// items (list, get). The call's result states the item's id and its status after
// the operation, and a create's input states the item's text. So the to-do list
// is read incrementally from the finished calls, as Claude Code's Task tools are.
//
// Only a COMPLETED call changes the list. A failed call changed no item, and a
// running call has no result yet.
//
// A change to an existing item is a KindDetail, never a KindUpdate. MiMo keeps
// the items per SESSION, and a subagent's calls reach the subagent's own
// transcript, so the item a call changes can be one that this transcript never
// saw created, and so can an item of a session that started before LeapMux
// opened it. The worker refuses a KindUpdate for an unknown item and drops the
// tool's row with it. A KindDetail adds the item instead, and the next list or
// get call states its text.

// mimoTaskInput is the part of a task call's input the reader needs.
type mimoTaskInput struct {
	Operation struct {
		Action  string `json:"action"`
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"operation"`
}

// mimoTaskResult is the task call's `state.metadata`: the item's id and its
// status after the operation.
type mimoTaskResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// mimoTaskRecord is one item as a get call prints it.
type mimoTaskRecord struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// mimoTaskListLine matches one line of a list call's output:
// `<id> <status> — <summary>`.
var mimoTaskListLine = regexp.MustCompile(`^(T\d+(?:\.\d+)*) ([a-z_]+) — (.*)$`)

// mimoTaskStatus maps a work item's status onto the neutral one. MiMo has no
// neutral word for `blocked`, and a blocked item is still work that remains, so
// it reads as pending. An abandoned item stops being work, which is what the
// neutral deleted status states.
func mimoTaskStatus(status string) todoevents.Status {
	switch status {
	case contracts.MiMoTaskStatusInProgress:
		return todoevents.StatusInProgress
	case contracts.MiMoTaskStatusDone:
		return todoevents.StatusCompleted
	case contracts.MiMoTaskStatusAbandoned:
		return todoevents.StatusDeleted
	case contracts.MiMoTaskStatusOpen, contracts.MiMoTaskStatusBlocked:
		return todoevents.StatusPending
	case "":
		return todoevents.StatusUnspecified
	default:
		return todoevents.StatusPending
	}
}

// extractTaskTodo reads a to-do change off a finished task call.
func extractTaskTodo(spanType string, content []byte) (todoevents.Event, bool) {
	if spanType != contracts.MiMoToolTask {
		return todoevents.Event{}, false
	}
	event, ok := parseEvent(content)
	if !ok || event.Type != contracts.MiMoEventMessagePartUpdated {
		return todoevents.Event{}, false
	}
	var payload mimoPartEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		return todoevents.Event{}, false
	}
	part := payload.Part
	if part.Tool != contracts.MiMoToolTask || part.State == nil || part.State.Status != contracts.MiMoToolStatusCompleted {
		return todoevents.Event{}, false
	}
	var input mimoTaskInput
	if err := json.Unmarshal(part.State.Input, &input); err != nil {
		return todoevents.Event{}, false
	}
	switch input.Operation.Action {
	case contracts.MiMoTaskActionList:
		return taskListTodo(part.State.Output)
	case contracts.MiMoTaskActionGet:
		return taskGetTodo(part.State.Output)
	}
	var result mimoTaskResult
	if len(part.State.Metadata) > 0 {
		if err := json.Unmarshal(part.State.Metadata, &result); err != nil {
			return todoevents.Event{}, false
		}
	}
	id := strings.TrimSpace(result.ID)
	if id == "" {
		id = strings.TrimSpace(input.Operation.ID)
	}
	if id == "" {
		return todoevents.Event{}, false
	}
	status := mimoTaskStatus(result.Status)
	switch input.Operation.Action {
	case contracts.MiMoTaskActionCreate:
		return todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
			ID:      id,
			Content: strings.TrimSpace(input.Operation.Summary),
			Status:  status,
		}}, true
	case contracts.MiMoTaskActionRename:
		return todoevents.Event{Kind: todoevents.KindDetail, Item: todoevents.Item{
			ID:      id,
			Content: strings.TrimSpace(input.Operation.Summary),
			Status:  status,
		}}, true
	case contracts.MiMoTaskActionStart, contracts.MiMoTaskActionBlock, contracts.MiMoTaskActionUnblock,
		contracts.MiMoTaskActionDone, contracts.MiMoTaskActionAbandon:
		if status == todoevents.StatusUnspecified {
			return todoevents.Event{}, false
		}
		return todoevents.Event{Kind: todoevents.KindDetail, Item: todoevents.Item{ID: id, Status: status}}, true
	default:
		return todoevents.Event{}, false
	}
}

// taskListTodo reads a list call's output. The list can leave items out -- by
// default it omits the finished ones -- so each listed item replaces its row
// and every other row stays.
func taskListTodo(output string) (todoevents.Event, bool) {
	var items []todoevents.Item
	for _, line := range strings.Split(output, "\n") {
		match := mimoTaskListLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		items = append(items, todoevents.Item{ID: match[1], Status: mimoTaskStatus(match[2]), Content: strings.TrimSpace(match[3])})
	}
	if len(items) == 0 {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindMerge, Items: items}, true
}

// taskGetTodo reads a get call's output, which is the item as JSON. An item
// that does not exist prints a sentence instead, and changes nothing.
func taskGetTodo(output string) (todoevents.Event, bool) {
	var record mimoTaskRecord
	if err := json.Unmarshal([]byte(output), &record); err != nil || strings.TrimSpace(record.ID) == "" {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindDetail, Item: todoevents.Item{
		ID:      strings.TrimSpace(record.ID),
		Content: strings.TrimSpace(record.Summary),
		Status:  mimoTaskStatus(record.Status),
	}}, true
}
