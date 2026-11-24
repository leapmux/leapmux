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
	// FormatNDJSON prints one compact JSON object per line (each item of a list
	// result on its own line without an outer wrapper).
	FormatNDJSON
	// FormatTSV prints tab-separated columns without padding or ANSI escapes.
	FormatTSV
)

// ParseFormat parses --format text|json|ndjson|tsv.
func ParseFormat(raw string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	case "ndjson":
		return FormatNDJSON, nil
	case "tsv":
		return FormatTSV, nil
	default:
		return FormatText, Usagef("--format must be one of text, json, ndjson, tsv; got %q", raw)
	}
}

// Printer writes the results, the stream events and the failure of one
// command in the format that the command line selected.
type Printer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
	// Quiet prints only the ids of a result, one per line.
	Quiet bool
	// Wide disables table cell truncation.
	Wide bool
	// NoHeader omits the header row from table and TSV output.
	NoHeader bool
	// Fields restricts rendered columns/fields when non-empty.
	Fields []string
	// Color controls ANSI styling on interactive TTYs.
	Color ColorMode
	// oneLineResult is the default text form for an ordinary mutation.
	oneLineResult bool
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
	switch p.Format {
	case FormatJSON:
		if len(p.Fields) > 0 {
			return writeEnvelope(p.Out, "data", projectValueFields(v, p.Fields))
		}
		return writeEnvelope(p.Out, "data", v)
	case FormatNDJSON:
		return writeNDJSON(p.Out, v, p.Fields)
	case FormatTSV:
		return renderTSV(p.Out, v, p.tableOpts())
	default:
		if p.oneLineResult && len(p.Fields) == 0 {
			return renderSingleLine(p.Out, v)
		}
		return renderTextOpts(p.Out, v, p.tableOpts())
	}
}

func (p *Printer) tableOpts() tableOptions {
	return tableOptions{
		wide:      p.Wide,
		noHeader:  p.NoHeader,
		fields:    p.Fields,
		termWidth: TerminalWidth(p.Out),
		style:     newStyler(p.Out, p.Color),
	}
}

// Event prints one event of a stream. In JSON it is one {"data":…} line, the
// same envelope as a result, so a consumer reads every line alike. In NDJSON
// it is one unwrapped JSON object per line. In text it is the event's TextLine
// when the event has one, and compact JSON otherwise.
func (p *Printer) Event(v any) error {
	if p.Format == FormatJSON {
		if len(p.Fields) > 0 {
			return writeEnvelope(p.Out, "data", projectValueFields(v, p.Fields))
		}
		return writeEnvelope(p.Out, "data", v)
	}
	if p.Format == FormatNDJSON {
		if len(p.Fields) > 0 {
			v = projectValueFields(v, p.Fields)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		_, err = p.Out.Write(raw)
		return err
	}
	if p.Quiet {
		return writeIDs(p.Out, v)
	}
	if p.Format == FormatTSV {
		return renderTSVEvent(p.Out, v, p.tableOpts())
	}
	if line, ok := v.(TextLiner); ok {
		text := line.TextLine()
		if strings.Contains(text, "\n") {
			_, err := fmt.Fprintln(p.Out, SanitizeTerminalText(text))
			return err
		}
		_, err := fmt.Fprintln(p.Out, SanitizeSingleLine(text))
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
	msg := SanitizeTerminalText(fmt.Sprintf(format, args...))
	_, _ = fmt.Fprintln(p.Err, msg)
}

// Failure prints e. In text it is "error: <code>: <message>" on stderr with
// one "hint:" line per hint. In JSON it is the {"error":…} envelope on
// stdout, beside the results, and the same "error:" line on stderr, so a
// caller that pipes stdout through a filter still sees the failure.
func (p *Printer) Failure(e *Error) {
	if e == nil {
		return
	}
	st := newStyler(p.Err, p.Color)
	if p.Format != FormatText || p.Quiet {
		st.enabled = false
	}
	_, _ = fmt.Fprintf(p.Err, "%s %s: %s\n", st.errorPrefix(), SanitizeSingleLine(string(e.Code)), SanitizeTerminalText(e.Message))
	if p.Format == FormatJSON || p.Format == FormatNDJSON {
		_ = writeEnvelope(p.Out, "error", errorEnvelope{
			Code:       e.Code,
			Message:    e.Message,
			Hints:      e.Hints,
			Retryable:  e.Retryable(),
			Candidates: e.Candidates,
			Receipt:    e.Receipt,
		})
		return
	}
	if len(e.Candidates) > 0 {
		_, _ = fmt.Fprintln(p.Err, "candidates:")
		_ = writeTableOpts(indentWriter(p.Err, "  "), sliceOf(e.Candidates), tableOptions{style: st})
	}
	for _, hint := range e.Hints {
		_, _ = fmt.Fprintf(p.Err, "%s %s\n", st.hintPrefix(), SanitizeTerminalText(hint))
	}
}

// errorEnvelope is the body of {"error":…}.
type errorEnvelope struct {
	Code       Code          `json:"code"`
	Message    string        `json:"message"`
	Hints      []string      `json:"hints,omitempty"`
	Retryable  bool          `json:"retryable"`
	Candidates []Candidate   `json:"candidates,omitempty"`
	Receipt    *RetryReceipt `json:"receipt,omitempty"`
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

// TextValuer is a field value with a one-line text form: the cell that a
// table prints for it, and the value of its "key: value" line. Implement it
// for a small object that a table must show in one cell, such as the sender
// of a message. Its JSON form stays what its fields encode.
type TextValuer interface {
	TextValue() string
}

// QuietRenderer is a result that specifies the exact lines printed under
// -q / --quiet. Implement it when a result holds multiple fields or uses a
// primary identifier other than a top-level "id" field.
type QuietRenderer interface {
	QuietLines() []string
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
