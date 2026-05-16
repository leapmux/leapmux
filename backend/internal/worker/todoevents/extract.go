package todoevents

import (
	"bytes"
	"encoding/json"
)

// Wire tool names from Claude Code's to-do list family. Centralized so
// the dispatcher, the output-handler integration, and tests can't drift.
const (
	ToolTodoWrite  = "TodoWrite"
	ToolTaskCreate = "TaskCreate"
	ToolTaskUpdate = "TaskUpdate"
	ToolTaskList   = "TaskList"
	ToolTaskGet    = "TaskGet"
)

// IsTodoToolSpanType reports whether a message's span_type is one of
// the to-do family tools this package consumes. Used by the worker
// output handler as a cheap early-exit gate so the hot path skips JSON
// parsing for every free-form assistant/user/bash message.
func IsTodoToolSpanType(spanType string) bool {
	switch spanType {
	case ToolTodoWrite, ToolTaskCreate, ToolTaskUpdate, ToolTaskList, ToolTaskGet:
		return true
	}
	return false
}

// claudeToolUseEnvelope is the assistant-shape JSON Claude emits for
// tool_use messages: `{ type: "assistant", message: { content: [{ type:
// "tool_use", name, input }, ...] } }`.
type claudeToolUseEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// claudeToolResultEnvelope is the minimal slice of a Claude
// tool_result message the to-do extractor needs: just the structured
// `tool_use_result` payload. The full envelope's `message.content`
// array is not read here (the result-side parsers branch on the
// `tool_use_result` shape directly).
type claudeToolResultEnvelope struct {
	ToolUseResult json.RawMessage `json:"tool_use_result"`
}

// todoWriteInput is the input shape for Claude TodoWrite.
type todoWriteInput struct {
	Todos []rawTodo `json:"todos"`
}

type rawTodo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// taskCreateInput captures the input fields the extractor needs from
// the paired Claude TaskCreate tool_use.
type taskCreateInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
}

// taskUpdateInput captures Claude TaskUpdate's tool_use input.
type taskUpdateInput struct {
	TaskID      string  `json:"taskId"`
	Subject     *string `json:"subject,omitempty"`
	Description *string `json:"description,omitempty"`
	ActiveForm  *string `json:"activeForm,omitempty"`
	Status      *string `json:"status,omitempty"`
}

type taskCreateResult struct {
	Task struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	} `json:"task"`
}

type taskUpdateResult struct {
	Success       bool     `json:"success"`
	TaskID        string   `json:"taskId"`
	UpdatedFields []string `json:"updatedFields"`
	StatusChange  *struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"statusChange"`
}

type taskGetResult struct {
	Task *struct {
		ID          string `json:"id"`
		Subject     string `json:"subject"`
		Description string `json:"description"`
		Status      string `json:"status"`
	} `json:"task"`
}

type taskListResult struct {
	Tasks []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Status  string `json:"status"`
	} `json:"tasks"`
}

// codexPlanNotification is the Codex turn/plan/updated JSON-RPC shape:
// `{ method: "turn/plan/updated", params: { plan: [{step, status}] } }`.
type codexPlanNotification struct {
	Method string `json:"method"`
	Params struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	} `json:"params"`
}

// acpPlanNotification is the ACP `sessionUpdate=plan` shape:
// `{ sessionUpdate: "plan", entries: [{content, status}] }`.
type acpPlanNotification struct {
	SessionUpdate string `json:"sessionUpdate"`
	Entries       []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
}

// Extract derives a TodoEvent from a single message. contentJSON is
// the decompressed message body. pairedToolUseJSON is the decompressed
// body of the paired tool_use message when contentJSON is a Claude
// tool_result (nil otherwise — caller resolves the span_id lookup).
// Returns false when the message does not affect the to-do list (any
// non-Task* tool, unrelated notifications, etc.).
func Extract(spanType string, contentJSON, pairedToolUseJSON []byte) (Event, bool) {
	if len(contentJSON) == 0 {
		return Event{}, false
	}

	// Claude tool spans carry span_type on every message — dispatch
	// directly and skip the notification-shape probes below.
	switch spanType {
	case ToolTodoWrite:
		return tryTodoWrite(contentJSON)
	case ToolTaskCreate:
		return tryTaskCreate(contentJSON, pairedToolUseJSON)
	case ToolTaskUpdate:
		return tryTaskUpdate(contentJSON, pairedToolUseJSON)
	case ToolTaskList:
		return tryTaskList(contentJSON)
	case ToolTaskGet:
		return tryTaskGet(contentJSON)
	}

	// Codex turn/plan/updated and ACP sessionUpdate=plan are top-level
	// JSON-RPC notifications with no span_type. Byte-pattern pre-filters
	// inside the try* helpers gate the full unmarshal.
	if ev, ok := tryCodexPlan(contentJSON); ok {
		return ev, true
	}
	if ev, ok := tryAcpPlan(contentJSON); ok {
		return ev, true
	}
	return Event{}, false
}

func tryTodoWrite(content []byte) (Event, bool) {
	var env claudeToolUseEnvelope
	if err := json.Unmarshal(content, &env); err != nil || env.Type != "assistant" {
		return Event{}, false
	}
	for _, raw := range env.Message.Content {
		var tu claudeToolUse
		if err := json.Unmarshal(raw, &tu); err != nil || tu.Type != "tool_use" {
			continue
		}
		if tu.Name != ToolTodoWrite {
			continue
		}
		var input todoWriteInput
		if err := json.Unmarshal(tu.Input, &input); err != nil {
			return Event{}, false
		}
		items := make([]Item, 0, len(input.Todos))
		for _, t := range input.Todos {
			items = append(items, Item{
				Content:    t.Content,
				Status:     StatusFromWire(t.Status),
				ActiveForm: t.ActiveForm,
			})
		}
		return Event{Kind: KindSnapshot, Snapshot: items}, true
	}
	return Event{}, false
}

func tryTaskCreate(content, pairedToolUse []byte) (Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return Event{}, false
	}
	var result taskCreateResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil || result.Task.ID == "" {
		return Event{}, false
	}
	// The result only carries id + subject; the paired tool_use input
	// has description/activeForm. The lookup is best-effort — a race
	// where the tool_use isn't yet visible just yields a less detailed
	// row (subject only).
	var input taskCreateInput
	parsePairedToolUseInput(pairedToolUse, ToolTaskCreate, &input)
	subject := input.Subject
	if subject == "" {
		subject = result.Task.Subject
	}
	return Event{
		Kind: KindCreate,
		Item: Item{
			ID:          result.Task.ID,
			Content:     subject,
			Status:      StatusPending,
			ActiveForm:  input.ActiveForm,
			Description: input.Description,
		},
	}, true
}

func tryTaskUpdate(content, pairedToolUse []byte) (Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return Event{}, false
	}
	var result taskUpdateResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil {
		return Event{}, false
	}
	if !result.Success || result.TaskID == "" {
		return Event{}, false
	}
	if result.StatusChange != nil && result.StatusChange.To == "deleted" {
		return Event{Kind: KindDelete, ID: result.TaskID}, true
	}
	patch := Patch{}
	if result.StatusChange != nil {
		s := StatusFromWire(result.StatusChange.To)
		patch.Status = &s
	}
	// Merge in the input-side patch fields.
	var input taskUpdateInput
	parsePairedToolUseInput(pairedToolUse, ToolTaskUpdate, &input)
	if input.Subject != nil {
		patch.Content = input.Subject
	}
	if input.ActiveForm != nil {
		patch.ActiveForm = input.ActiveForm
	}
	if input.Description != nil {
		patch.Description = input.Description
	}
	return Event{Kind: KindUpdate, ID: result.TaskID, Patch: patch}, true
}

// parsePairedToolUseInput unmarshals the named tool_use block's input
// from a paired tool_use envelope into out. No-op when bytes are empty
// or the envelope doesn't contain the named tool. out is expected to
// be a pointer to a struct shaped like the tool's input.
func parsePairedToolUseInput(pairedToolUse []byte, name string, out any) {
	if len(pairedToolUse) == 0 {
		return
	}
	var env claudeToolUseEnvelope
	if err := json.Unmarshal(pairedToolUse, &env); err != nil {
		return
	}
	for _, raw := range env.Message.Content {
		var tu claudeToolUse
		if err := json.Unmarshal(raw, &tu); err != nil || tu.Type != "tool_use" || tu.Name != name {
			continue
		}
		_ = json.Unmarshal(tu.Input, out)
		return
	}
}

func tryTaskList(content []byte) (Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return Event{}, false
	}
	var result taskListResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil {
		return Event{}, false
	}
	items := make([]Item, 0, len(result.Tasks))
	for _, t := range result.Tasks {
		items = append(items, Item{
			ID:      t.ID,
			Content: t.Subject,
			Status:  StatusFromWire(t.Status),
		})
	}
	return Event{Kind: KindSnapshot, Snapshot: items}, true
}

func tryTaskGet(content []byte) (Event, bool) {
	var env claudeToolResultEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return Event{}, false
	}
	var result taskGetResult
	if err := json.Unmarshal(env.ToolUseResult, &result); err != nil || result.Task == nil {
		return Event{}, false
	}
	return Event{
		Kind: KindDetail,
		Item: Item{
			ID:          result.Task.ID,
			Content:     result.Task.Subject,
			Status:      StatusFromWire(result.Task.Status),
			Description: result.Task.Description,
		},
	}, true
}

func tryCodexPlan(content []byte) (Event, bool) {
	// Cheap discriminator before the full parse.
	if !looksLikeCodexPlan(content) {
		return Event{}, false
	}
	var n codexPlanNotification
	if err := json.Unmarshal(content, &n); err != nil || n.Method != "turn/plan/updated" {
		return Event{}, false
	}
	items := make([]Item, 0, len(n.Params.Plan))
	for _, p := range n.Params.Plan {
		if p.Step == "" {
			continue
		}
		items = append(items, Item{
			Content:    p.Step,
			Status:     StatusFromWire(p.Status),
			ActiveForm: p.Step,
		})
	}
	return Event{Kind: KindSnapshot, Snapshot: items}, true
}

func tryAcpPlan(content []byte) (Event, bool) {
	if !looksLikeAcpPlan(content) {
		return Event{}, false
	}
	var n acpPlanNotification
	if err := json.Unmarshal(content, &n); err != nil || n.SessionUpdate != "plan" {
		return Event{}, false
	}
	items := make([]Item, 0, len(n.Entries))
	for _, e := range n.Entries {
		items = append(items, Item{
			Content: e.Content,
			Status:  StatusFromWire(e.Status),
		})
	}
	return Event{Kind: KindSnapshot, Snapshot: items}, true
}

// LooksLikeProviderPlan is a cheap byte-pattern check that returns
// true when the content body has a plausible chance of being a
// Codex turn/plan/updated or ACP sessionUpdate=plan notification.
// Worker callers use this as a hot-path gate so non-todo messages
// skip the full JSON parse pipeline.
//
// Each marker is a distinctive whole-string token, so a single
// bytes.Contains is sufficient — searching for a key/value adjacency
// would silently miss occurrences where the same key appears earlier
// with a different value.
func LooksLikeProviderPlan(b []byte) bool {
	return looksLikeCodexPlan(b) || looksLikeAcpPlan(b)
}

func looksLikeCodexPlan(b []byte) bool {
	return bytes.Contains(b, []byte(`"turn/plan/updated"`))
}

func looksLikeAcpPlan(b []byte) bool {
	// Two independent substring probes so the gate accepts both the
	// compact ({"sessionUpdate":"plan",...}) and pretty-printed
	// ({"sessionUpdate": "plan", ...}) forms. False positives are fine
	// because callers follow up with a real Unmarshal.
	return bytes.Contains(b, []byte(`"sessionUpdate"`)) && bytes.Contains(b, []byte(`"plan"`))
}
