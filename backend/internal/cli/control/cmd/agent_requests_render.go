package cmd

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

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
	_, err := io.WriteString(w, cli.SanitizeTerminalText(buf.String()))
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
		fmt.Fprintf(w, "  Stop the agent's turn with \"leapmux agent interrupt %s\".\n", r.AgentID)
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
		for last > 0 && oneLine(row[last-1]) == "" {
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
