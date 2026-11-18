package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format is the form of a command's output.
type Format int

const (
	// FormatText is the default: a table for a list, "key: value" lines
	// for one object, and one line for a stream event.
	FormatText Format = iota
	// FormatJSON prints one envelope per line: {"data":…} for a result or
	// a stream event, and {"error":…} for a failure.
	FormatJSON
)

// Printer writes the results, the stream events and the failure of one
// command in the format that the command line selected.
type Printer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
	// Quiet prints only the ids of a result, one per line.
	Quiet bool
}

// Result prints the result of a command. A nil result prints nothing.
//
// Every result is an object: a struct, or a map with string keys. A list
// lives under its plural noun ({"tabs": […]}), so a result can gain a field
// without a change of shape.
func (p *Printer) Result(v any) error {
	if isNil(v) {
		return nil
	}
	if p.Quiet {
		return writeIDs(p.Out, v)
	}
	if p.Format == FormatJSON {
		return writeEnvelope(p.Out, "data", v)
	}
	return renderText(p.Out, v)
}

// Event prints one event of a stream. In JSON it is one {"data":…} line, the
// same envelope as a result, so a consumer reads every line alike. In text it
// is the event's TextLine when the event has one, and compact JSON
// otherwise.
func (p *Printer) Event(v any) error {
	if p.Format == FormatJSON {
		return writeEnvelope(p.Out, "data", v)
	}
	if p.Quiet {
		return writeIDs(p.Out, v)
	}
	if line, ok := v.(TextLiner); ok {
		_, err := fmt.Fprintln(p.Out, line.TextLine())
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(p.Out, string(raw))
	return err
}

// Notice prints one line of prose for a person on stderr: progress, or a
// warning. It never reaches stdout, so it cannot corrupt a result that a
// script parses.
func (p *Printer) Notice(format string, args ...any) {
	_, _ = fmt.Fprintf(p.Err, format+"\n", args...)
}

// Failure prints e. In text it is "error: <code>: <message>" on stderr with
// one "hint:" line per hint. In JSON it is the {"error":…} envelope on
// stdout, beside the results, and the same "error:" line on stderr, so a
// caller that pipes stdout through a filter still sees the failure.
func (p *Printer) Failure(e *Error) {
	if e == nil {
		return
	}
	_, _ = fmt.Fprintf(p.Err, "error: %s: %s\n", e.Code, e.Message)
	if p.Format == FormatJSON {
		_ = writeEnvelope(p.Out, "error", errorEnvelope{
			Code:       e.Code,
			Message:    e.Message,
			Hints:      e.Hints,
			Retryable:  e.Retryable(),
			Candidates: e.Candidates,
		})
		return
	}
	if len(e.Candidates) > 0 {
		_, _ = fmt.Fprintln(p.Err, "candidates:")
		_ = writeTable(indentWriter(p.Err, "  "), sliceOf(e.Candidates))
	}
	for _, hint := range e.Hints {
		_, _ = fmt.Fprintf(p.Err, "hint: %s\n", hint)
	}
}

// errorEnvelope is the body of {"error":…}.
type errorEnvelope struct {
	Code       Code        `json:"code"`
	Message    string      `json:"message"`
	Hints      []string    `json:"hints,omitempty"`
	Retryable  bool        `json:"retryable"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// TextRenderer is a result that renders its own text form. Implement it only
// when the generic table and "key: value" forms cannot show the result, such
// as a file's content.
type TextRenderer interface {
	RenderText(w io.Writer) error
}

// TextLiner is a stream event with a one-line text form.
type TextLiner interface {
	TextLine() string
}

// writeEnvelope writes {"<key>": v} as one line of compact JSON.
func writeEnvelope(w io.Writer, key string, v any) error {
	raw, err := json.Marshal(map[string]any{key: v})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

// indentWriter prefixes each line that w receives with prefix.
func indentWriter(w io.Writer, prefix string) io.Writer {
	return &prefixWriter{w: w, prefix: prefix, atLineStart: true}
}

type prefixWriter struct {
	w           io.Writer
	prefix      string
	atLineStart bool
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	var sb strings.Builder
	for _, c := range b {
		if p.atLineStart && c != '\n' {
			sb.WriteString(p.prefix)
		}
		sb.WriteByte(c)
		p.atLineStart = c == '\n'
	}
	if _, err := io.WriteString(p.w, sb.String()); err != nil {
		return 0, err
	}
	return len(b), nil
}
