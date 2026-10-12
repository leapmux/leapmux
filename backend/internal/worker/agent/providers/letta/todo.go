package letta

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// lettaTaskRecord is the full record returned by a native Task tool.
type lettaTaskRecord struct {
	TaskID      string `json:"taskId"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
	Status      string `json:"status"`
}

// ExtractTodoEvent reads a native Task result and its request from one span.
// The closing result row has no span type, so the paired request identifies
// the tool and must carry the same call ID as the result.
func (lettaProvider) ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	if pairedToolUse == nil {
		return todoevents.Event{}, false
	}
	var result struct {
		MessageType string `json:"message_type"`
		ToolCallID  string `json:"tool_call_id"`
		Status      string `json:"status"`
		ToolReturn  string `json:"tool_return"`
	}
	if json.Unmarshal(content, &result) != nil || result.MessageType != "tool_return_message" ||
		result.ToolCallID == "" || result.Status != "success" {
		return todoevents.Event{}, false
	}

	name := lettaPairedTaskName(pairedToolUse(), result.ToolCallID)
	if name == "" || (spanType != "" && spanType != name) {
		return todoevents.Event{}, false
	}

	switch name {
	case "TaskCreate", "TaskUpdate":
		var row lettaTaskRecord
		if json.Unmarshal([]byte(result.ToolReturn), &row) != nil {
			return todoevents.Event{}, false
		}
		item, ok := lettaTodoItem(row)
		if !ok {
			return todoevents.Event{}, false
		}
		return todoevents.Event{Kind: todoevents.KindCreate, Item: item}, true
	case "TaskList":
		var list struct {
			Tasks *[]lettaTaskRecord `json:"tasks"`
		}
		if json.Unmarshal([]byte(result.ToolReturn), &list) != nil || list.Tasks == nil {
			return todoevents.Event{}, false
		}
		items := make([]todoevents.Item, 0, len(*list.Tasks))
		seen := make(map[string]bool, len(*list.Tasks))
		for _, row := range *list.Tasks {
			item, ok := lettaTodoItem(row)
			if !ok || seen[item.ID] {
				return todoevents.Event{}, false
			}
			seen[item.ID] = true
			items = append(items, item)
		}
		return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
	default:
		return todoevents.Event{}, false
	}
}

// lettaPairedTaskName accepts client tool starts and both tool-call frames.
func lettaPairedTaskName(content []byte, callID string) string {
	var request struct {
		MessageType string                `json:"message_type"`
		ToolCallID  string                `json:"tool_call_id"`
		ToolName    string                `json:"tool_name"`
		ToolCall    *lettaNativeToolCall  `json:"tool_call"`
		ToolCalls   []lettaNativeToolCall `json:"tool_calls"`
	}
	if json.Unmarshal(content, &request) != nil {
		return ""
	}
	if request.MessageType == "client_tool_start" && request.ToolCallID == callID {
		return request.ToolName
	}
	if request.MessageType != "tool_call_message" {
		return ""
	}
	if request.ToolCall != nil {
		request.ToolCalls = append(request.ToolCalls, *request.ToolCall)
	}
	for _, call := range request.ToolCalls {
		if call.CallID == callID {
			return call.Name
		}
	}
	return ""
}

func lettaTodoItem(row lettaTaskRecord) (todoevents.Item, bool) {
	if row.TaskID == "" || row.Subject == "" {
		return todoevents.Item{}, false
	}
	switch row.Status {
	case "pending", "in_progress", "completed", "deleted":
	default:
		return todoevents.Item{}, false
	}
	return todoevents.Item{
		ID:          row.TaskID,
		Content:     row.Subject,
		Description: row.Description,
		ActiveForm:  row.ActiveForm,
		Status:      todoevents.StatusFromProviderWord(row.Status),
	}, true
}
