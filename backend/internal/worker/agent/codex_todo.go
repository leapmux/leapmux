package agent

import (
	"bytes"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// codexPlanMethod is the notification that carries Codex's turn plan.
const codexPlanMethod = "turn/plan/updated"

// codexPlanNotification is the shape of that notification:
// `{method:"turn/plan/updated", params:{plan:[{step,status}]}}`.
type codexPlanNotification struct {
	Method string `json:"method"`
	Params struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	} `json:"params"`
}

// ExtractTodoEvent reads Codex's turn plan off one persisted message.
//
// The method name is the discriminator, and a byte search for it is the cheap exit
// for the great majority of messages. The name is a distinctive whole-string token,
// so one search is enough.
//
// What decides CORRECTNESS is the exact check below: a plan states `method` at the
// TOP level, so a tool result that merely quotes the name carries its own `method`
// and the unmarshal rejects it. The span type is deliberately not read. Codex
// persists its plan with no span today, but keying on that would silently return an
// empty sidebar the day a plan arrives inside a tool call -- and the exact check
// already makes the guard unnecessary.
//
// Codex re-sends the WHOLE plan on every update, so this is a snapshot.
func (codexProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if !bytes.Contains(content, []byte(`"`+codexPlanMethod+`"`)) {
		return todoevents.Event{}, false
	}
	var notification codexPlanNotification
	if err := json.Unmarshal(content, &notification); err != nil || notification.Method != codexPlanMethod {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(notification.Params.Plan))
	for _, step := range notification.Params.Plan {
		// A step with no text would render as an empty row, and Codex emits one while
		// it is still writing the plan.
		if step.Step == "" {
			continue
		}
		items = append(items, todoevents.Item{
			Content: step.Step,
			Status:  todoevents.StatusFromWire(step.Status),
			// Codex states no separate active form, and the step reads as one.
			ActiveForm: step.Step,
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
