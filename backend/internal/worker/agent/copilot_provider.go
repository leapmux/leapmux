package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// copilotProvider is the stateless wire-format plugin for GitHub Copilot. It answers
// the questions the service layer asks without a running agent.
//
// Every method below states a decision about Copilot's NATIVE protocol. A default
// that Copilot simply takes needs no method here.
type copilotProvider struct {
	noopProvider
}

// IsInterrupt recognizes Copilot's own abort frame.
//
// The frontend uses the InterruptAgent remote procedure call. This parser supports
// raw provider input from another caller.
func (copilotProvider) IsInterrupt(content string) bool {
	var frame struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &frame); err != nil {
		return false
	}
	return frame.Method == "session.abort"
}

// ListStoredSessions reads Copilot's own per-session sidecars. See copilot_sessions.go.
func (copilotProvider) ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return copilotStoredSessions(ctx, q)
}

// copilotSessionEventFrame is the part of a stored native frame that identifies its event.
type copilotSessionEventFrame struct {
	Method string `json:"method"`
	Params struct {
		Event struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"event"`
	} `json:"params"`
}

// copilotEventOfType decodes one stored native frame and reports its event data when
// the frame carries the event that `eventType` identifies.
//
// The byte search is the cheap exit for the great majority of rows. What decides
// CORRECTNESS is the exact check that follows: a tool result that merely quotes the
// event name carries its own `method`, and the decode rejects it.
func copilotEventOfType(content []byte, eventType string) (json.RawMessage, bool) {
	if !bytes.Contains(content, []byte(`"`+eventType+`"`)) {
		return nil, false
	}
	var frame copilotSessionEventFrame
	if json.Unmarshal(content, &frame) != nil || frame.Method != copilotMethodSessionEvent {
		return nil, false
	}
	if frame.Params.Event.Type != eventType {
		return nil, false
	}
	return frame.Params.Event.Data, true
}

// ExtractTodoEvent reads Copilot's to-do list off one persisted message.
//
// The list rides the `update_todo` tool call, and Copilot's own schema describes that
// tool's single argument as "a markdown checklist of TODO items showing completed and
// pending tasks". The checklist is therefore the provider's own statement of the
// list, and the whole list arrives on every call, so this is a snapshot.
func (copilotProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	data, ok := copilotEventOfType(content, contracts.CopilotEventToolStarted)
	if !ok {
		return todoevents.Event{}, false
	}
	var start struct {
		ToolName  string `json:"toolName"`
		Arguments struct {
			Todos string `json:"todos"`
		} `json:"arguments"`
	}
	if json.Unmarshal(data, &start) != nil || start.ToolName != contracts.CopilotToolUpdateTodo {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: parseCopilotChecklist(start.Arguments.Todos)}, true
}

// copilotChecklistItem matches one markdown task-list row.
//
// The pattern is the same one the browser plugin applies, and
// testdata/copilot_checklist_conformance.json is the corpus both suites replay.
var copilotChecklistItem = regexp.MustCompile(`^[ \t]*[-*+][ \t]+\[(.)\][ \t]+(\S.*)?$`)

// parseCopilotChecklist reads a markdown task list.
//
// Only the two markers that the markdown task-list syntax defines are read as
// finished and unfinished. Every other marker reads as pending, which is the answer
// todoevents.StatusFromProviderWord already gives an unrecognized status word: a state that
// this build cannot understand must not be shown as done.
func parseCopilotChecklist(checklist string) []todoevents.Item {
	items := make([]todoevents.Item, 0)
	for _, line := range strings.Split(checklist, "\n") {
		match := copilotChecklistItem.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if match == nil {
			continue
		}
		text := strings.TrimSpace(match[2])
		if text == "" {
			continue
		}
		status := todoevents.StatusPending
		if match[1] == "x" || match[1] == "X" {
			status = todoevents.StatusCompleted
		}
		items = append(items, todoevents.Item{Content: text, ActiveForm: text, Status: status})
	}
	return items
}

// ResolveControlResponse turns the browser's decision into Copilot's native answer.
//
// The translation lives in copilot_control_response.go, which also classifies the
// plan decision. It is Exit rather than Prompt because the runtime ASKED and stays
// blocked until it is answered. The request name is inside the stored native frame:
// Copilot's control requests carry no `tool_name` envelope, so the shared metadata
// reader leaves ToolName empty and the payload is the only source.
func (copilotProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	if result, ok := copilotResolveControlAnswer(ctx); ok {
		return result
	}
	return defaultControlResponseResolution(ctx)
}

// PlanModePermissionMode is empty for every kind.
//
// Copilot's plan axis is its SESSION mode, not its permission mode, and the runtime
// leaves plan mode itself when it accepts the exit response -- it then reports
// `session.mode_changed`, which the running agent folds back into its settings. A
// mode pushed from here would travel down the permission-mode axis, whose values are
// manual, assisted and allow-all, and the runtime rejects every other word.
func (copilotProvider) PlanModePermissionMode(PlanModeControlKind) string { return "" }
