package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/semanticevents"
)

// The kinds of a control request, as `agent requests` prints them.
const (
	controlKindPermission  = "permission"
	controlKindPlan        = "plan"
	controlKindQuestion    = "question"
	controlKindElicitation = "elicitation"
	controlKindDialog      = "dialog"
	// controlKindUnreadable is a request whose payload the Worker cannot
	// read. It takes no answer.
	controlKindUnreadable = "unreadable"
	// controlKindUnknown is a prompt of a kind that this CLI does not know,
	// from a Worker of another version.
	controlKindUnknown = "unknown"
)

// controlPromptKind returns the kind token of prompt.
func controlPromptKind(prompt *leapmuxv1.ControlPrompt) string {
	return promptKindToken(semanticevents.PromptKind(prompt))
}

func promptKindToken(kind leapmuxv1.SemanticControlPromptKind) string {
	if token := enumToken(kind); token != "" {
		return token
	}
	return controlKindUnknown
}

// controlRequestRow is one pending control request of an agent: a
// permission prompt, a plan to approve, questions, an MCP elicitation form,
// or an extension's dialog. Exactly one of the prompt fields is set, as Kind
// states.
type controlRequestRow struct {
	// ID is the request's handle: the claim token of this request instance.
	// `agent approve`, `agent deny` and `agent answer` take it, or a unique
	// prefix of it. A request that the agent replaced has a new handle, so an
	// answer cannot reach a request that the caller did not read.
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// RequestID is the provider's own id of the request.
	RequestID string `json:"request_id"`
	// Kind is "permission", "plan", "question", "elicitation", "dialog" or
	// "unreadable".
	Kind string `json:"kind"`
	// Blocking reports whether the agent waits for the answer. An agent goes
	// on working beside a request that does not block it.
	Blocking bool `json:"blocking"`
	// State is "ready" for a request that has no answer yet. Any other state
	// is the delivery of an answer that the Worker already holds: "pending",
	// "uncertain", "delivered", "completed" or "canceled".
	State       string           `json:"state"`
	Permission  *permissionView  `json:"permission,omitempty"`
	Plan        *planView        `json:"plan,omitempty"`
	Questions   []questionView   `json:"questions,omitempty"`
	Elicitation *elicitationView `json:"elicitation,omitempty"`
	Dialog      *dialogView      `json:"dialog,omitempty"`
	// Fault says why LeapMux cannot read an unreadable request: "malformed"
	// (not JSON), "not_an_object", or "unrecognized" (a JSON object that the
	// agent's provider does not recognize as a request).
	Fault string `json:"fault,omitempty"`
}

// permissionView asks whether one call may run.
type permissionView struct {
	Title            string `json:"title,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Text             string `json:"text,omitempty"`
	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	// Input is the call's arguments.
	Input   json.RawMessage        `json:"input,omitempty"`
	Options []permissionOptionView `json:"options"`
}

// permissionOptionView is one answer to a permission prompt. `agent answer
// --option <n>` chooses it.
type permissionOptionView struct {
	N  int    `json:"n"`
	ID string `json:"id"`
	// Kind is "allow_once", "allow_always", "reject_once", "reject_always"
	// or "other".
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Scope is how far a remembered answer ("allow_always" or
	// "reject_always") reaches: "session", "workspace", "project" or "user",
	// when the provider states it.
	Scope string `json:"scope,omitempty"`
	// ScopeLabel is the provider's own word for the scope, such as Codex's
	// "Command rule" or "Host rule". It tells apart two remembered answers of
	// one scope that keep different rules.
	ScopeLabel string `json:"scope_label,omitempty"`
	// EndsTurn reports a refusal that also ends the agent's turn, rather than
	// refusing only this call.
	EndsTurn bool `json:"ends_turn,omitempty"`
}

// planView asks to approve a plan.
type planView struct {
	Text        string               `json:"text,omitempty"`
	Permissions []planPermissionView `json:"permissions,omitempty"`
	Details     []string             `json:"details,omitempty"`
	// Choices are the answers that the runtime offers beside a plain
	// approval and refusal. `agent answer --option <n>` chooses one.
	Choices []planChoiceView `json:"choices,omitempty"`
}

type planPermissionView struct {
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

type planChoiceView struct {
	N           int    `json:"n"`
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Effect is what the choice does with the plan: "approve", "refuse", or
	// "other" for a choice such as saving the plan for later.
	Effect string `json:"effect"`
}

// questionView is one question. `agent answer` answers every question of a
// request at once.
type questionView struct {
	N      int    `json:"n"`
	ID     string `json:"id,omitempty"`
	Header string `json:"header,omitempty"`
	// Context is text that the question shows above itself.
	Context  string               `json:"context,omitempty"`
	Question string               `json:"question"`
	Options  []questionOptionView `json:"options"`
	// InitialSelections are the values of the options that the agent selected
	// for the question. An empty list is an answer of no option. `agent answer`
	// sends them for a question that its command line does not answer.
	InitialSelections *[]string `json:"initial_selections,omitempty"`
	// MultiSelect allows any number of options.
	MultiSelect bool `json:"multi_select"`
	// TypedAnswer allows a typed answer.
	TypedAnswer bool `json:"typed_answer"`
	// TypedWithOptions allows a typed answer beside chosen options. A
	// multiple-choice question always allows it, and the answer then holds
	// the options and the text.
	TypedWithOptions bool `json:"typed_with_options"`
	// AllowEmpty allows no answer at all.
	AllowEmpty        bool   `json:"allow_empty"`
	MinimumSelections *int32 `json:"minimum_selections,omitempty"`
	MaximumSelections *int32 `json:"maximum_selections,omitempty"`
}

type questionOptionView struct {
	N int `json:"n"`
	// Value is what the answer sends for the option.
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// elicitationView is a form that an MCP server asks the user to fill in.
type elicitationView struct {
	// Purpose is "form", or "permission" for a form that asks to approve a
	// call.
	Purpose     string `json:"purpose"`
	Mode        string `json:"mode,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Message     string `json:"message,omitempty"`
	Server      string `json:"server,omitempty"`
	URL         string `json:"url,omitempty"`
	// Schema is the JSON Schema of the form. `agent answer --field` fills
	// its properties.
	Schema         json.RawMessage `json:"schema,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	ArgumentNotice string          `json:"argument_notice,omitempty"`
	// AcceptChoices are the accept answers that the server offers beside a
	// plain accept. `agent answer --option <n>` chooses one.
	AcceptChoices []acceptChoiceView `json:"accept_choices,omitempty"`
	// FieldRules state the fields of the schema that the form keeps hidden,
	// and the fields that it asks only under conditions.
	FieldRules []fieldRuleView `json:"field_rules,omitempty"`
}

// fieldRuleView states how a form treats one field of its schema.
type fieldRuleView struct {
	Key string `json:"key"`
	// Hidden reports a field that the form never shows. Its default is sent
	// while the field is active.
	Hidden bool `json:"hidden,omitempty"`
	// Conditions must all hold, for the answers to earlier fields, for the
	// form to ask the field. An inactive field is neither sent nor required.
	Conditions []fieldConditionView `json:"conditions,omitempty"`
}

type fieldConditionView struct {
	Field string `json:"field"`
	// Comparison is "equal" or "not_equal". A list answer is equal when it
	// holds the value.
	Comparison string `json:"comparison"`
	// Value is a string, a number or a boolean.
	Value json.RawMessage `json:"value"`
}

// fieldRuleViewsOf projects the field rules of a form. A condition of no
// known comparison, or whose value is not the JSON of a string, a number or
// a boolean, states nothing, so it is dropped, as the browser drops it.
func fieldRuleViewsOf(rules []*leapmuxv1.ElicitationFieldRule) []fieldRuleView {
	views := make([]fieldRuleView, 0, len(rules))
	for _, rule := range rules {
		view := fieldRuleView{Key: rule.GetKey(), Hidden: rule.GetHidden()}
		for _, condition := range rule.GetConditions() {
			comparison := enumToken(condition.GetComparison())
			var value any
			if comparison == "" || json.Unmarshal([]byte(condition.GetValueJson()), &value) != nil {
				continue
			}
			switch value.(type) {
			case string, float64, bool:
			default:
				continue
			}
			compact, _ := json.Marshal(value)
			view.Conditions = append(view.Conditions, fieldConditionView{Field: condition.GetField(), Comparison: comparison, Value: compact})
		}
		views = append(views, view)
	}
	return views
}

type acceptChoiceView struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Label string `json:"label"`
}

// dialogView is a dialog that an extension opened through the runtime.
type dialogView struct {
	// Variant is "confirm", "input" or "editor".
	Variant     string `json:"variant"`
	Title       string `json:"title,omitempty"`
	Message     string `json:"message,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Prefill     string `json:"prefill,omitempty"`
	// TimeoutMS is how long the runtime waits before it answers itself.
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

// controlRequestRowOf projects one request of the agent agentID.
func controlRequestRowOf(agentID string, r *leapmuxv1.AgentControlRequest) controlRequestRow {
	prompt := r.GetPrompt()
	row := controlRequestRow{
		ID:        r.GetClaimToken(),
		AgentID:   agentID,
		RequestID: r.GetRequestId(),
		Kind:      controlPromptKind(prompt),
		Blocking:  !r.GetNonBlocking(),
		State:     controlResponseStateToken(r.GetResponseState()),
	}
	switch kind := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		row.Permission = permissionViewOf(kind.Permission)
	case *leapmuxv1.ControlPrompt_Plan:
		row.Plan = planViewOf(kind.Plan)
	case *leapmuxv1.ControlPrompt_Question:
		row.Questions = questionViewsOf(kind.Question)
	case *leapmuxv1.ControlPrompt_Elicitation:
		row.Elicitation = elicitationViewOf(kind.Elicitation)
	case *leapmuxv1.ControlPrompt_Dialog:
		row.Dialog = dialogViewOf(kind.Dialog)
	case *leapmuxv1.ControlPrompt_Unreadable:
		row.Fault = enumToken(kind.Unreadable)
	}
	return row
}

// controlResponseStateToken is the token of a response state. The Worker
// states one for every listed request, so an unstated one prints
// "unknown", never "".
func controlResponseStateToken(state leapmuxv1.ControlResponseState) string {
	if token := enumToken(state); token != "" {
		return token
	}
	return agentStateUnknown
}

// jsonText returns text as raw JSON for a result, or nil for empty text.
// Text that is not JSON becomes a JSON string, so a result never carries
// invalid JSON and still shows what arrived.
func jsonText(text string) json.RawMessage {
	if text == "" {
		return nil
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	quoted, _ := json.Marshal(text)
	return quoted
}

func permissionViewOf(p *leapmuxv1.PermissionPrompt) *permissionView {
	view := &permissionView{
		Title:            p.GetTitle(),
		Reason:           p.GetReason(),
		Text:             p.GetText(),
		Command:          p.GetCommand(),
		WorkingDirectory: p.GetWorkingDirectory(),
		Input:            jsonText(p.GetInputJson()),
		Options:          make([]permissionOptionView, 0, len(p.GetOptions())),
	}
	for i, option := range p.GetOptions() {
		view.Options = append(view.Options, permissionOptionView{
			N:          i + 1,
			ID:         option.GetOptionId(),
			Kind:       permissionOptionKindToken(option.GetKind()),
			Name:       option.GetName(),
			Scope:      enumToken(option.GetScope()),
			ScopeLabel: option.GetScopeLabel(),
			EndsTurn:   option.GetEndsTurn(),
		})
	}
	return view
}

// permissionOptionKindToken is the token of an option kind. An option of no
// stated kind is "other", as the Worker states a kind outside the four.
func permissionOptionKindToken(kind leapmuxv1.PermissionOptionKind) string {
	if token := enumToken(kind); token != "" {
		return token
	}
	return enumToken(leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER)
}

// planChoiceEffectToken is the token of a choice's effect. A choice of no
// stated effect is "other": nothing says what it does with the plan.
func planChoiceEffectToken(effect leapmuxv1.PlanChoiceEffect) string {
	if token := enumToken(effect); token != "" {
		return token
	}
	return enumToken(leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER)
}

func planViewOf(p *leapmuxv1.PlanPrompt) *planView {
	view := &planView{Text: p.GetText(), Details: p.GetDetails()}
	for _, permission := range p.GetPermissions() {
		view.Permissions = append(view.Permissions, planPermissionView{Tool: permission.GetTool(), Prompt: permission.GetPrompt()})
	}
	for i, choice := range p.GetChoices() {
		view.Choices = append(view.Choices, planChoiceView{
			N: i + 1, ID: choice.GetId(), Label: choice.GetLabel(), Description: choice.GetDescription(), Effect: planChoiceEffectToken(choice.GetEffect()),
		})
	}
	return view
}

func questionViewsOf(p *leapmuxv1.QuestionPrompt) []questionView {
	views := make([]questionView, 0, len(p.GetQuestions()))
	for i, q := range p.GetQuestions() {
		typed := q.AllowCustomText == nil || q.GetAllowCustomText()
		view := questionView{
			N:                 i + 1,
			ID:                q.GetId(),
			Header:            q.GetHeader(),
			Context:           q.GetContext(),
			Question:          q.GetQuestion(),
			Options:           make([]questionOptionView, 0, len(q.GetOptions())),
			MultiSelect:       q.GetMultiSelect(),
			TypedAnswer:       typed,
			TypedWithOptions:  typed && (q.GetMultiSelect() || p.GetPreservesSelectionNotes()),
			AllowEmpty:        q.GetAllowEmpty(),
			MinimumSelections: q.MinimumSelections,
			MaximumSelections: q.MaximumSelections,
		}
		if initial := q.GetInitialSelections(); initial != nil {
			values := append([]string{}, initial.GetValues()...)
			view.InitialSelections = &values
		}
		for j, option := range q.GetOptions() {
			value := option.GetLabel()
			if option.Value != nil {
				value = option.GetValue()
			}
			view.Options = append(view.Options, questionOptionView{
				N: j + 1, Value: value, Label: option.GetLabel(), Description: option.GetDescription(), Preview: option.GetPreview(),
			})
		}
		views = append(views, view)
	}
	return views
}

func elicitationViewOf(e *leapmuxv1.ElicitationPrompt) *elicitationView {
	purpose := "form"
	if e.GetPurpose() == leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION {
		purpose = enumToken(e.GetPurpose())
	}
	view := &elicitationView{
		Purpose:        purpose,
		Mode:           e.GetMode(),
		Title:          e.GetTitle(),
		Description:    e.GetDescription(),
		Message:        e.GetMessage(),
		Server:         e.GetServer(),
		URL:            e.GetUrl(),
		Schema:         jsonText(e.GetSchemaJson()),
		Arguments:      jsonText(e.GetArgumentsJson()),
		ArgumentNotice: e.GetArgumentNotice(),
	}
	for i, choice := range e.GetAcceptChoices() {
		view.AcceptChoices = append(view.AcceptChoices, acceptChoiceView{N: i + 1, Key: choice.GetKey(), Label: choice.GetLabel()})
	}
	if len(e.GetFieldRules()) > 0 {
		view.FieldRules = fieldRuleViewsOf(e.GetFieldRules())
	}
	return view
}

func dialogViewOf(d *leapmuxv1.DialogPrompt) *dialogView {
	variant := enumToken(d.GetVariant())
	if variant == "" {
		// The browser draws a dialog of no stated variant as a confirm.
		variant = enumToken(leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM)
	}
	return &dialogView{
		Variant:     variant,
		Title:       d.GetTitle(),
		Message:     d.GetMessage(),
		Placeholder: d.GetPlaceholder(),
		Prefill:     d.GetPrefill(),
		TimeoutMS:   d.TimeoutMs,
	}
}

// controlRequestList is the result of `agent requests`.
type controlRequestList struct {
	Requests []controlRequestRow `json:"requests"`
}

// RenderText prints each request as a block. A table would cut a command
// at the width of its cell, and the reader must see the whole command
// before approving it.
func (l controlRequestList) RenderText(w io.Writer) error {
	var buf bytes.Buffer
	for i, row := range l.Requests {
		if i > 0 {
			buf.WriteByte('\n')
		}
		row.writeText(&buf)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// writeText writes the block of one request: a heading, then what the
// request asks.
func (r controlRequestRow) writeText(w *bytes.Buffer) {
	fmt.Fprintf(w, "request %s of agent %s: %s", r.ID, r.AgentID, r.Kind)
	var notes []string
	if !r.Blocking {
		notes = append(notes, "the agent does not wait for it")
	}
	if r.State != enumToken(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY) {
		notes = append(notes, "answered, "+r.State)
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(notes, "; "))
	}
	w.WriteByte('\n')
	switch {
	case r.Permission != nil:
		r.Permission.writeText(w)
	case r.Plan != nil:
		r.Plan.writeText(w)
	case len(r.Questions) > 0:
		for _, q := range r.Questions {
			q.writeText(w)
		}
	case r.Elicitation != nil:
		r.Elicitation.writeText(w)
	case r.Dialog != nil:
		r.Dialog.writeText(w)
	case r.Kind == controlKindUnreadable:
		fmt.Fprintf(w, "  LeapMux cannot read this request (%s), so it takes no answer.\n", r.Fault)
		fmt.Fprintf(w, "  Stop the agent's turn with \"leapmux control agent interrupt %s\".\n", r.AgentID)
	}
}

func (p *permissionView) writeText(w *bytes.Buffer) {
	writeFields(w, "  ", []textField{
		{"title", p.Title},
		{"reason", p.Reason},
		{"text", p.Text},
		{"command", p.Command},
		{"working directory", p.WorkingDirectory},
		{"input", string(p.Input)},
	})
	w.WriteString("  options:\n")
	rows := make([][]string, 0, len(p.Options))
	for _, option := range p.Options {
		kind := option.Kind
		if scope := option.scopeText(); scope != "" {
			kind += " (" + scope + ")"
		}
		if option.EndsTurn {
			kind += " (ends the turn)"
		}
		rows = append(rows, []string{fmt.Sprint(option.N), kind, option.Name})
	}
	writeRows(w, "    ", rows)
}

// scopeText states the scope of an option for the text form: the scope, then
// the provider's label for it after a colon, either one alone, or "" when the
// option states neither.
func (o permissionOptionView) scopeText() string {
	switch {
	case o.Scope != "" && o.ScopeLabel != "":
		return o.Scope + ": " + o.ScopeLabel
	case o.Scope != "":
		return o.Scope
	default:
		return o.ScopeLabel
	}
}

func (p *planView) writeText(w *bytes.Buffer) {
	if p.Text != "" {
		w.WriteString("  plan:\n")
		writeIndented(w, "    ", p.Text)
	}
	if len(p.Permissions) > 0 {
		w.WriteString("  permissions:\n")
		rows := make([][]string, 0, len(p.Permissions))
		for _, permission := range p.Permissions {
			rows = append(rows, []string{permission.Tool, permission.Prompt})
		}
		writeRows(w, "    ", rows)
	}
	for _, detail := range p.Details {
		writeIndented(w, "  ", detail)
	}
	if len(p.Choices) > 0 {
		w.WriteString("  choices:\n")
		rows := make([][]string, 0, len(p.Choices))
		for _, choice := range p.Choices {
			rows = append(rows, []string{fmt.Sprint(choice.N), choice.Effect, choice.Label, choice.Description})
		}
		writeRows(w, "    ", rows)
	}
}

func (q questionView) writeText(w *bytes.Buffer) {
	heading := q.Question
	if q.Header != "" {
		heading = q.Header + ": " + heading
	}
	fmt.Fprintf(w, "  question %d: %s\n", q.N, oneLine(heading))
	writeFields(w, "    ", []textField{
		{"context", q.Context},
		{"answer", q.answerRule()},
		{"default", q.defaultText()},
	})
	rows := make([][]string, 0, len(q.Options))
	for _, option := range q.Options {
		rows = append(rows, []string{fmt.Sprint(option.N), option.Label, option.Description})
	}
	writeRows(w, "    ", rows)
}

// defaultText states the options that the agent selected, by number, or ""
// when it selected none of its own.
func (q questionView) defaultText() string {
	if q.InitialSelections == nil {
		return ""
	}
	var numbers []string
	for _, value := range *q.InitialSelections {
		for _, option := range q.Options {
			if option.Value == value {
				numbers = append(numbers, fmt.Sprint(option.N))
				break
			}
		}
	}
	if len(numbers) == 0 {
		return "no option"
	}
	noun := "option"
	if len(numbers) > 1 {
		noun = "options"
	}
	return noun + " " + strings.Join(numbers, ", ")
}

// answerRule states in words what the question accepts.
func (q questionView) answerRule() string {
	var choose string
	switch {
	case len(q.Options) == 0:
	case !q.MultiSelect:
		choose = "one option"
	case q.MinimumSelections != nil && q.MaximumSelections != nil && *q.MinimumSelections == *q.MaximumSelections:
		choose = fmt.Sprintf("%d options", *q.MinimumSelections)
	case q.MinimumSelections != nil && q.MaximumSelections != nil:
		choose = fmt.Sprintf("%d to %d options", *q.MinimumSelections, *q.MaximumSelections)
	case q.MinimumSelections != nil:
		choose = fmt.Sprintf("at least %d options", *q.MinimumSelections)
	case q.MaximumSelections != nil:
		choose = fmt.Sprintf("up to %d options", *q.MaximumSelections)
	default:
		choose = "any options"
	}
	var parts []string
	switch {
	case choose == "" && q.TypedAnswer:
		parts = append(parts, "a typed answer")
	case choose == "":
	case q.TypedWithOptions:
		parts = append(parts, choose+", a typed answer, or both")
	case q.TypedAnswer:
		parts = append(parts, choose+" or a typed answer")
	default:
		parts = append(parts, choose)
	}
	if q.AllowEmpty {
		parts = append(parts, "or nothing")
	}
	if len(parts) == 0 {
		return "nothing that this command can send"
	}
	return strings.Join(parts, ", ")
}

func (e *elicitationView) writeText(w *bytes.Buffer) {
	writeFields(w, "  ", []textField{
		{"purpose", e.Purpose},
		{"server", e.Server},
		{"title", e.Title},
		{"description", e.Description},
		{"message", e.Message},
		{"url", e.URL},
		{"arguments", string(e.Arguments)},
		{"argument notice", e.ArgumentNotice},
	})
	var rows [][]string
	for _, field := range elicitationFields(e.Schema, e.FieldRules) {
		if field.Hidden {
			continue
		}
		required := ""
		if field.Required {
			required = "required"
		}
		kind := field.Type
		if len(field.Enum) > 0 {
			kind += " (" + strings.Join(field.Enum, ", ") + ")"
		}
		when := ""
		if len(field.Conditions) > 0 {
			when = conditionText(field.Conditions)
		}
		rows = append(rows, []string{field.Name, kind, required, when, field.Description})
	}
	if len(rows) > 0 {
		w.WriteString("  fields:\n")
		writeRows(w, "    ", rows)
	}
	if len(e.AcceptChoices) > 0 {
		w.WriteString("  accept choices:\n")
		rows := make([][]string, 0, len(e.AcceptChoices))
		for _, choice := range e.AcceptChoices {
			rows = append(rows, []string{fmt.Sprint(choice.N), choice.Label})
		}
		writeRows(w, "    ", rows)
	}
}

func (d *dialogView) writeText(w *bytes.Buffer) {
	var timeout string
	if d.TimeoutMS != nil {
		timeout = (time.Duration(*d.TimeoutMS) * time.Millisecond).String()
	}
	writeFields(w, "  ", []textField{
		{"variant", d.Variant},
		{"title", d.Title},
		{"message", d.Message},
		{"placeholder", d.Placeholder},
		{"prefill", d.Prefill},
		{"answers itself after", timeout},
	})
}

// textField is one "label: value" line of a request block.
type textField struct {
	label, value string
}

// writeFields writes each field that has a value, with the values aligned.
// A value of several lines continues under its first line.
func writeFields(w *bytes.Buffer, indent string, fields []textField) {
	width := 0
	for _, f := range fields {
		if f.value != "" {
			width = max(width, len(f.label))
		}
	}
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(f.value, "\n"), "\n")
		fmt.Fprintf(w, "%s%s:%s %s\n", indent, f.label, strings.Repeat(" ", width-len(f.label)), lines[0])
		continuation := indent + strings.Repeat(" ", width+2)
		for _, line := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", continuation, line)
		}
	}
}

// writeRows writes rows with their columns aligned. It drops the empty cells
// at the end of a row, and a column that is empty in every row takes no
// space.
func writeRows(w *bytes.Buffer, indent string, rows [][]string) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len([]rune(oneLine(cell))))
		}
	}
	for _, row := range rows {
		last := len(row)
		for last > 0 && row[last-1] == "" {
			last--
		}
		var line strings.Builder
		line.WriteString(indent)
		// padding is what the previous cell owes before the next one starts.
		padding := 0
		for i, cell := range row[:last] {
			if widths[i] == 0 {
				continue
			}
			cell = oneLine(cell)
			line.WriteString(strings.Repeat(" ", padding))
			line.WriteString(cell)
			padding = widths[i] - len([]rune(cell)) + 2
		}
		w.WriteString(line.String())
		w.WriteByte('\n')
	}
}

// writeIndented writes text with indent before each line.
func writeIndented(w *bytes.Buffer, indent, text string) {
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		w.WriteString(indent)
		w.WriteString(line)
		w.WriteByte('\n')
	}
}

// oneLine joins the lines of text with spaces, for a cell of a row.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// runAgentRequests lists the pending requests of the given agents, or of
// every agent when none is given.
func runAgentRequests(inv *cli.Invocation) (any, error) {
	inputs := inv.Variadic("agent")
	if len(inputs) > 0 && inv.Changed("workspace") {
		return nil, cli.Usagef("give agents or --workspace, not both")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	targets, err := requestTargets(s, inputs)
	if err != nil {
		return nil, err
	}
	groups, agentIDs := groupAgentsByWorker(targets)
	listed := listControlRequests(s.ctx, func(workerID string, body func(call workerCaller) error) error {
		return withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
			return body(func(method string, in, out proto.Message) error { return w.Call(s.ctx, method, in, out) })
		})
	}, groups)
	result := controlRequestList{Requests: []controlRequestRow{}}
	var failures []error
	for _, id := range agentIDs {
		got := listed[id]
		if got.err != nil {
			failures = append(failures, got.err)
			continue
		}
		result.Requests = append(result.Requests, got.rows...)
	}
	return reportControlRequests(inv.Printer, result, failures, len(agentIDs))
}

// requestTargets resolves the agents of `agent requests`. With no input it
// lists every agent tab, of one workspace when --workspace gives one.
func requestTargets(s *session, inputs []string) ([]tabTarget, error) {
	if len(inputs) > 0 {
		targets := make([]tabTarget, 0, len(inputs))
		for _, input := range inputs {
			got, err := s.resolveTab(resolve.KindAgent, input, resolve.Need{WorkerID: true})
			if err != nil {
				return nil, err
			}
			targets = append(targets, got)
		}
		return targets, nil
	}
	req := &leapmuxv1.ListTabsRequest{}
	if s.inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return nil, err
		}
		req.WorkspaceIds = []string{ws.ID}
	}
	var resp leapmuxv1.ListTabsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, req, &resp); err != nil {
		return nil, err
	}
	var targets []tabTarget
	for _, t := range filterTabsByType(resp.GetTabs(), leapmuxv1.TabType_TAB_TYPE_AGENT) {
		// A tab that no Worker hosts has no agent to ask.
		if t.GetWorkerId() != "" {
			targets = append(targets, tabTarget{ID: t.GetTab().GetTabId(), Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: t.GetWorkerId()})
		}
	}
	return targets, nil
}

// reportControlRequests returns the result of `agent requests`. When every
// agent failed, the first failure is the result. Otherwise the result
// prints, each failure is a notice, and the exit status is the first
// failure's, so a script never reads a partial list as a complete one.
func reportControlRequests(p *cli.Printer, result controlRequestList, failures []error, agents int) (any, error) {
	if len(failures) == 0 {
		if len(result.Requests) == 0 {
			p.Notice("no agent waits for an answer")
		}
		return result, nil
	}
	if len(failures) == agents {
		return nil, failures[0]
	}
	if err := p.Result(result); err != nil {
		return nil, err
	}
	for _, failure := range failures {
		p.Notice("%s", cli.FromError(failure).Message)
	}
	return nil, &cli.ExitStatus{Code: cli.FromError(failures[0]).ExitCode()}
}

// listedRequests is what one agent's listing gave: its requests, or why it
// failed.
type listedRequests struct {
	rows []controlRequestRow
	err  error
}

// workerChannelFunc runs body with a caller of one Worker. It is the seam of
// listControlRequests: production opens the Worker's channel, and a test
// answers the calls itself.
type workerChannelFunc func(workerID string, body func(call workerCaller) error) error

// listControlRequests asks each Worker for the pending requests of its
// agents. Every Worker runs at once, and so does every agent of one Worker:
// the Worker's channel carries concurrent calls. A failure belongs to its
// agent, so the other agents still list.
func listControlRequests(ctx context.Context, open workerChannelFunc, groups []workerAgents) map[string]listedRequests {
	var mu sync.Mutex
	out := map[string]listedRequests{}
	record := func(agentID string, got listedRequests) {
		mu.Lock()
		defer mu.Unlock()
		out[agentID] = got
	}
	var workers errgroup.Group
	workers.SetLimit(detailFanoutLimit)
	for _, group := range groups {
		workers.Go(func() error {
			err := open(group.workerID, func(call workerCaller) error {
				var agents errgroup.Group
				agents.SetLimit(detailFanoutLimit)
				for _, agentID := range group.agentIDs {
					agents.Go(func() error {
						var resp leapmuxv1.ListControlRequestsResponse
						if err := call(leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: agentID}, &resp); err != nil {
							record(agentID, listedRequests{err: fmt.Errorf("list the requests of agent %s: %w", agentID, err)})
							return nil
						}
						rows := make([]controlRequestRow, 0, len(resp.GetRequests()))
						for _, r := range resp.GetRequests() {
							rows = append(rows, controlRequestRowOf(agentID, r))
						}
						record(agentID, listedRequests{rows: rows})
						return nil
					})
				}
				return agents.Wait()
			})
			if err != nil {
				// The channel did not open: every agent of the Worker fails
				// with it.
				for _, agentID := range group.agentIDs {
					record(agentID, listedRequests{err: fmt.Errorf("list the requests of agent %s: %w", agentID, err)})
				}
			}
			return nil
		})
	}
	_ = workers.Wait()
	if ctx.Err() != nil {
		for _, group := range groups {
			for _, agentID := range group.agentIDs {
				if _, ok := out[agentID]; !ok {
					out[agentID] = listedRequests{err: errors.Join(fmt.Errorf("list the requests of agent %s", agentID), context.Cause(ctx))}
				}
			}
		}
	}
	return out
}
