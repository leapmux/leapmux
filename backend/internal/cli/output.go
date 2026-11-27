package cli

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
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
	// FormatQuiet prints only the ids of a result, one per line.
	FormatQuiet
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
	// Wide disables table cell truncation.
	Wide bool
	// NoHeader omits the header row from table and TSV output.
	NoHeader bool
	// Fields restricts rendered columns/fields when non-empty.
	Fields []string
	// Color controls ANSI styling on interactive TTYs.
	Color ColorMode
	// StateClassifier maps values in `,state` table columns to a StateTone.
	// When nil, Printer falls back to SetDefaultStateClassifier.
	StateClassifier StateClassifier
	// oneLineResult is the default text form for an ordinary mutation.
	oneLineResult bool
}

// completeWriter turns a short write with a nil error into io.ErrShortWrite.
type completeWriter struct {
	w io.Writer
}

func (cw completeWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if err == nil && n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, err
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
	out := completeWriter{w: p.Out}
	switch p.Format {
	case FormatQuiet:
		return writeIDs(out, v)
	case FormatJSON:
		return p.writeJSONData(out, v)
	case FormatNDJSON:
		return writeNDJSON(out, v, p.Fields)
	case FormatTSV:
		return renderTSV(out, v, p.tableOpts())
	default:
		opts := p.tableOpts()
		if p.oneLineResult && len(p.Fields) == 0 {
			return renderSingleLine(out, v, opts)
		}
		return renderTextOpts(out, v, opts)
	}
}

func (p *Printer) writeJSONData(w io.Writer, v any) error {
	if len(p.Fields) > 0 {
		v = projectValueFields(v, p.Fields)
	}
	return writeEnvelope(w, "data", v)
}

func (p *Printer) tableOpts() tableOptions {
	return tableOptions{
		wide:      p.Wide,
		noHeader:  p.NoHeader,
		fields:    p.Fields,
		termWidth: TerminalWidth(p.Out),
		style:     newStylerWithClassifier(p.Out, p.Color, p.StateClassifier),
	}
}

// Event prints one event of a stream. In JSON it is one {"data":…} line, the
// same envelope as a result, so a consumer reads every line alike. In NDJSON
// it is one unwrapped JSON object per line. In text it is the event's TextLine
// when the event has one, and compact JSON otherwise.
func (p *Printer) Event(v any) error {
	out := completeWriter{w: p.Out}
	switch p.Format {
	case FormatJSON:
		return p.writeJSONData(out, v)
	case FormatNDJSON:
		if len(p.Fields) > 0 {
			v = projectValueFields(v, p.Fields)
		}
		return writeJSONLine(out, v)
	case FormatQuiet:
		return writeIDs(out, v)
	case FormatTSV:
		return renderTSVEvent(out, v, tableOptions{fields: p.Fields})
	default:
		if line, ok := v.(TextLiner); ok {
			text := line.TextLine()
			if strings.Contains(text, "\n") {
				_, err := fmt.Fprintln(out, SanitizeTerminalText(text))
				return err
			}
			_, err := fmt.Fprintln(out, SanitizeSingleLine(text))
			return err
		}
		return writeJSONLine(out, v)
	}
}

// Notice prints one line of prose for a person on stderr: progress, or a
// warning. It never reaches stdout, so it cannot corrupt a result that a
// script parses.
func (p *Printer) Notice(format string, args ...any) error {
	if p == nil || p.Err == nil {
		return nil
	}
	msg := SanitizeTerminalText(fmt.Sprintf(format, args...))
	_, err := fmt.Fprintln(completeWriter{w: p.Err}, msg)
	return err
}

// Failure prints e. In text it is "error: <code>: <message>" on stderr with
// one "hint:" line per hint. In JSON it is the {"error":…} envelope on
// stdout, beside the results, and the same "error:" line on stderr, so a
// caller that pipes stdout through a filter still sees the failure.
func (p *Printer) Failure(e *Error) {
	if e == nil {
		return
	}
	st := newStylerWithClassifier(p.Err, p.Color, p.StateClassifier)
	if p.Format != FormatText {
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
		_ = writeTableOpts(indentWriter(p.Err, "  "), toNode(reflect.ValueOf(e.Candidates)), tableOptions{style: st})
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

// TableOptions holds rendering options passed to an OptionsTextRenderer so
// custom table views honor --wide, --no-header, --color, and terminal width.
type TableOptions struct {
	opts tableOptions
}

// OptionsTextRenderer is an optional alternative to TextRenderer for values
// that embed aligned tables and want to honor --wide, --no-header, --color,
// and TTY column width.
type OptionsTextRenderer interface {
	RenderTextWithOptions(w io.Writer, opts TableOptions) error
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

// DefaultColumnHider is an optional interface for list results that hide one
// or more default table columns in plain text output when the caller did not
// pass --fields. It has no effect on --format tsv, --format json,
// --format ndjson, --quiet, or explicit --fields selection, so TSV column
// indices stay stable and explicit --fields keep struct-tag styling like ,state.
type DefaultColumnHider interface {
	DefaultHiddenColumns() []string
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
	written := 0
	for len(b) > 0 {
		if p.atLineStart {
			if b[0] == '\n' {
				n, err := p.w.Write(b[:1])
				written += n
				if err != nil {
					return written, err
				}
				if n < 1 {
					return written, io.ErrShortWrite
				}
				b = b[1:]
				continue
			}
			if p.prefix != "" {
				n, err := io.WriteString(p.w, p.prefix)
				if err != nil {
					return written, err
				}
				if n < len(p.prefix) {
					return written, io.ErrShortWrite
				}
			}
			p.atLineStart = false
		}
		idx := bytes.IndexByte(b, '\n')
		chunk := b
		if idx >= 0 {
			chunk = b[:idx+1]
		}
		n, err := p.w.Write(chunk)
		written += n
		if idx >= 0 && n == len(chunk) {
			p.atLineStart = true
		}
		if err != nil {
			return written, err
		}
		if n < len(chunk) {
			return written, io.ErrShortWrite
		}
		b = b[len(chunk):]
	}
	return written, nil
}
