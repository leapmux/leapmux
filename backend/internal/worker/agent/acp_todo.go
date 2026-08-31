package agent

import (
	"bytes"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// acpPlanSessionUpdate is the `sessionUpdate` discriminator that marks a plan.
const acpPlanSessionUpdate = "plan"

// acpPlanNotification is the ACP plan shape:
// `{sessionUpdate:"plan", entries:[{content,status}]}`.
type acpPlanNotification struct {
	SessionUpdate string `json:"sessionUpdate"`
	Entries       []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
}

// ExtractTodoEvent reads an ACP plan off one persisted message.
//
// Every ACP provider shares this shape, which is why it sits on acpProvider rather
// than on each of the six that embed it.
//
// The discriminator is the only signal. Two byte searches are the cheap exit for the
// great majority of messages, and they are deliberately independent: one key/value
// search would miss the pretty-printed form (`"sessionUpdate": "plan"`). A false
// positive costs one unmarshal that the exact check below then rejects.
//
// What decides CORRECTNESS is that exact check: a plan states `sessionUpdate` at the
// TOP level, so a tool result that merely contains both markers is rejected. The span
// type is deliberately not read. Every ACP handler persists its plan with no span
// today, but keying on that would silently return an empty sidebar the day one
// persists a plan inside a tool call -- and the exact check already makes the guard
// unnecessary.
//
// The whole plan arrives on every update, so this is a snapshot.
func (acpProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if !bytes.Contains(content, []byte(`"sessionUpdate"`)) ||
		!bytes.Contains(content, []byte(`"`+acpPlanSessionUpdate+`"`)) {
		return todoevents.Event{}, false
	}
	var notification acpPlanNotification
	if err := json.Unmarshal(content, &notification); err != nil || notification.SessionUpdate != acpPlanSessionUpdate {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(notification.Entries))
	for _, entry := range notification.Entries {
		items = append(items, todoevents.Item{
			Content: entry.Content,
			Status:  todoevents.StatusFromWire(entry.Status),
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
