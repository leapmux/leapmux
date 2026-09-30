package goose

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

var gooseChecklistItem = regexp.MustCompile(`^[-*+] \[([ xX])\] (.+)$`)

type gooseTodoResultFrame struct {
	SessionUpdate string `json:"sessionUpdate"`
	ToolCallID    string `json:"toolCallId"`
	Status        string `json:"status"`
}

type gooseTodoRequestFrame struct {
	SessionUpdate string `json:"sessionUpdate"`
	ToolCallID    string `json:"toolCallId"`
	RawInput      struct {
		Content *string `json:"content"`
	} `json:"rawInput"`
	Meta struct {
		Goose struct {
			ToolCall struct {
				ToolName      string `json:"toolName"`
				ExtensionName string `json:"extensionName"`
			} `json:"toolCall"`
		} `json:"goose"`
	} `json:"_meta"`
}

// ExtractTodoEvent reads Goose's completed to-do write as a snapshot. The
// result confirms that the write succeeded; its paired request holds the list.
// Goose also accepts ACP plans through the embedded provider.
func (p gooseProvider) ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	if event, ok := p.Provider.ExtractTodoEvent(spanType, content, pairedToolUse); ok {
		return event, true
	}
	if pairedToolUse == nil || !bytes.Contains(content, []byte(`"`+contracts.ACPUpdateToolCallUpdate+`"`)) {
		return todoevents.Event{}, false
	}
	var result gooseTodoResultFrame
	if json.Unmarshal(content, &result) != nil ||
		result.SessionUpdate != contracts.ACPUpdateToolCallUpdate ||
		result.Status != "completed" || result.ToolCallID == "" {
		return todoevents.Event{}, false
	}

	var request gooseTodoRequestFrame
	if json.Unmarshal(pairedToolUse(), &request) != nil ||
		request.SessionUpdate != contracts.ACPUpdateToolCall ||
		request.ToolCallID != result.ToolCallID ||
		request.Meta.Goose.ToolCall.ExtensionName != contracts.GooseTodoExtension ||
		request.Meta.Goose.ToolCall.ToolName != contracts.GooseTodoExtension+"__"+contracts.GooseTodoTool ||
		request.RawInput.Content == nil {
		return todoevents.Event{}, false
	}
	items, ok := gooseTodoItems(*request.RawInput.Content)
	if !ok {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

func gooseTodoItems(content string) ([]todoevents.Item, bool) {
	var items []todoevents.Item
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := gooseChecklistItem.FindStringSubmatch(line)
		if len(match) != 3 {
			return nil, false
		}
		text := strings.TrimSpace(match[2])
		if text == "" {
			return nil, false
		}
		status := todoevents.StatusPending
		if match[1] != " " {
			status = todoevents.StatusCompleted
		}
		items = append(items, todoevents.Item{Content: text, Status: status})
	}
	return items, true
}
