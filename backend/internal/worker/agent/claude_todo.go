package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// Claude Code's to-do list, in its own wire shapes.
//
// Two families feed one list. `TodoWrite` re-sends the WHOLE list on every call,
// so it is a snapshot. `TaskCreate` / `TaskUpdate` / `TaskGet` / `TaskList` are
// incremental and address a row by a stable id, which is why the neutral model
// carries an id at all.
//
// The two halves of a tool call are split across two messages, and which half
// carries what decides where each parser reads. `TodoWrite` states the list in the
// tool_use INPUT. The Task* family states the outcome in the tool_result and the
// text in the input, so those parsers read the result and reach back to the paired
// tool_use for the fields it does not repeat.

// Wire tool names of Claude Code's to-do list family.
const (
	claudeToolTodoWrite  = "TodoWrite"
	claudeToolTaskCreate = "TaskCreate"
	claudeToolTaskUpdate = "TaskUpdate"
	claudeToolTaskList   = "TaskList"
	claudeToolTaskGet    = "TaskGet"
)

// claudeToolUseEnvelope is the assistant-shape JSON Claude emits for tool_use
// messages: `{type:"assistant", message:{content:[{type:"tool_use", name, input}]}}`.
type claudeToolUseEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

// claudeToolUseBlock is one entry of that content array. Only the fields the
// extractor reads are declared; the block carries more.
type claudeToolUseBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// claudeToolResultEnvelope is the slice of a Claude tool_result message the to-do
// extractor needs: the structured `tool_use_result` payload. The envelope's
// `message.content` array is not read here, because each result-side parser
// branches on the `tool_use_result` shape directly.
type claudeToolResultEnvelope struct {
	ToolUseResult json.RawMessage `json:"tool_use_result"`
}

type claudeTodoWriteInput struct {
	Todos []struct {
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm"`
	} `json:"todos"`
}

// claudeTaskCreateInput is what the paired TaskCreate tool_use adds to the result:
// the description and the active form, which the result does not repeat.
type claudeTaskCreateInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
}

// claudeTaskUpdateInput carries the TEXT fields of a TaskUpdate. Each is a
// POINTER, because the wire tells "no change" and "set to empty" apart and the
// neutral Patch does too. The status is not read here: the RESULT states the
// transition that actually happened, which is not always the one asked for.
type claudeTaskUpdateInput struct {
	Subject     *string `json:"subject,omitempty"`
	Description *string `json:"description,omitempty"`
	ActiveForm  *string `json:"activeForm,omitempty"`
}

type claudeTaskCreateResult struct {
	Task struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	} `json:"task"`
}

type claudeTaskUpdateResult struct {
	Success      bool   `json:"success"`
	TaskID       string `json:"taskId"`
	StatusChange *struct {
		To string `json:"to"`
	} `json:"statusChange"`
}

type claudeTaskGetResult struct {
	Task *struct {
		ID          string `json:"id"`
		Subject     string `json:"subject"`
		Description string `json:"description"`
		Status      string `json:"status"`
	} `json:"task"`
}

type claudeTaskListResult struct {
	Tasks []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Status  string `json:"status"`
	} `json:"tasks"`
}

// ExtractTodoEvent reads Claude Code's to-do list off one persisted message.
//
// Every message of a Claude tool call carries its span type, so the tool name
// decides which parser runs and a message outside the family exits on the switch
// without a parse. A row with no span type is not a to-do row at all: Claude states
// each one through a tool.
func (claudeProvider) ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	if len(content) == 0 {
		return todoevents.Event{}, false
	}
	switch spanType {
	case claudeToolTodoWrite:
		return claudeTodoWriteEvent(content)
	case claudeToolTaskCreate:
		return claudeTaskCreateEvent(content, pairedToolUse)
	case claudeToolTaskUpdate:
		return claudeTaskUpdateEvent(content, pairedToolUse)
	case claudeToolTaskList:
		return claudeTaskListEvent(content)
	case claudeToolTaskGet:
		return claudeTaskGetEvent(content)
	}
	return todoevents.Event{}, false
}

// claudeTodoWriteEvent reads the whole list off the tool_use input.
//
// The USER-side envelope of the same span carries the tool_result and no input, so
// the `assistant` check is what keeps the result half from reading as an empty list
// that would wipe the row the use half just wrote.
func claudeTodoWriteEvent(content []byte) (todoevents.Event, bool) {
	var env claudeToolUseEnvelope
	if err := json.Unmarshal(content, &env); err != nil || env.Type != "assistant" {
		return todoevents.Event{}, false
	}
	for _, raw := range env.Message.Content {
		var block claudeToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil || block.Type != "tool_use" {
			continue
		}
		if block.Name != claudeToolTodoWrite {
			continue
		}
		var input claudeTodoWriteInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			return todoevents.Event{}, false
		}
		items := make([]todoevents.Item, 0, len(input.Todos))
		for _, t := range input.Todos {
			items = append(items, todoevents.Item{
				Content:    t.Content,
				Status:     todoevents.StatusFromWire(t.Status),
				ActiveForm: t.ActiveForm,
			})
		}
		return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
	}
	return todoevents.Event{}, false
}

func claudeTaskCreateEvent(content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return todoevents.Event{}, false
	}
	var result claudeTaskCreateResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil || result.Task.ID == "" {
		return todoevents.Event{}, false
	}
	// The result carries the id and the subject; the paired tool_use input carries
	// the description and the active form. That lookup is best-effort -- a race in
	// which the tool_use is not visible yet yields a less detailed row rather than
	// none -- so it runs only after the result proved there is a row to build.
	var input claudeTaskCreateInput
	claudeParsePairedToolUseInput(pairedToolUse, claudeToolTaskCreate, &input)
	subject := input.Subject
	if subject == "" {
		subject = result.Task.Subject
	}
	return todoevents.Event{
		Kind: todoevents.KindCreate,
		Item: todoevents.Item{
			ID:          result.Task.ID,
			Content:     subject,
			Status:      todoevents.StatusPending,
			ActiveForm:  input.ActiveForm,
			Description: input.Description,
		},
	}, true
}

func claudeTaskUpdateEvent(content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return todoevents.Event{}, false
	}
	var result claudeTaskUpdateResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil {
		return todoevents.Event{}, false
	}
	if !result.Success || result.TaskID == "" {
		return todoevents.Event{}, false
	}
	if result.StatusChange != nil && result.StatusChange.To == "deleted" {
		return todoevents.Event{Kind: todoevents.KindDelete, ID: result.TaskID}, true
	}
	patch := todoevents.Patch{}
	if result.StatusChange != nil {
		status := todoevents.StatusFromWire(result.StatusChange.To)
		patch.Status = &status
	}
	// The result states the status change; the input states the text fields.
	var input claudeTaskUpdateInput
	claudeParsePairedToolUseInput(pairedToolUse, claudeToolTaskUpdate, &input)
	if input.Subject != nil {
		patch.Content = input.Subject
	}
	if input.ActiveForm != nil {
		patch.ActiveForm = input.ActiveForm
	}
	if input.Description != nil {
		patch.Description = input.Description
	}
	return todoevents.Event{Kind: todoevents.KindUpdate, ID: result.TaskID, Patch: patch}, true
}

func claudeTaskListEvent(content []byte) (todoevents.Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return todoevents.Event{}, false
	}
	var result claudeTaskListResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(result.Tasks))
	for _, t := range result.Tasks {
		items = append(items, todoevents.Item{
			ID:      t.ID,
			Content: t.Subject,
			Status:  todoevents.StatusFromWire(t.Status),
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

func claudeTaskGetEvent(content []byte) (todoevents.Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return todoevents.Event{}, false
	}
	var result claudeTaskGetResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil || result.Task == nil {
		return todoevents.Event{}, false
	}
	return todoevents.Event{
		Kind: todoevents.KindDetail,
		Item: todoevents.Item{
			ID:          result.Task.ID,
			Content:     result.Task.Subject,
			Status:      todoevents.StatusFromWire(result.Task.Status),
			Description: result.Task.Description,
		},
	}, true
}

// claudeParsePairedToolUseInput unmarshals the named tool_use block's input into
// out. It is a no-op when the paired message is absent or holds no such block, so
// the caller reads the zero value and reports the row it could build.
//
// `pairedToolUse` is a function rather than bytes because resolving it costs a
// database read. Calling it here means only the two parsers that need the input
// pay for one.
func claudeParsePairedToolUseInput(pairedToolUse func() []byte, name string, out any) {
	if pairedToolUse == nil {
		return
	}
	content := pairedToolUse()
	if len(content) == 0 {
		return
	}
	var env claudeToolUseEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return
	}
	for _, raw := range env.Message.Content {
		var block claudeToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil || block.Type != "tool_use" || block.Name != name {
			continue
		}
		_ = json.Unmarshal(block.Input, out)
		return
	}
}
