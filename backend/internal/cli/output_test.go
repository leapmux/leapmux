package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testRow struct {
	ID       string `json:"id" table:"ID,short"`
	Title    string `json:"title" table:"TITLE"`
	State    string `json:"state,omitempty" table:"STATE"`
	WorkerID string `json:"worker_id"`
}

type testList struct {
	Rows []testRow `json:"rows"`
}

func newTestPrinter(format Format) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &Printer{Out: &out, Err: &errOut, Format: format}, &out, &errOut
}

func TestPrinterResultPrintsAListAsATableWithTaggedColumns(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(testList{Rows: []testRow{
		{ID: "87a19d5f-4838-4e34-9f0f-56130f0a62b1", Title: "CLI Design", State: "working", WorkerID: "w-1"},
		{ID: "4cbb3f34abcdef", Title: "Fix flaky spec", WorkerID: "w-2"},
	}}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"ID", "TITLE", "STATE"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"87a19d5f", "CLI", "Design", "working"}, strings.Fields(lines[1]), "a short column prints the first 8 characters")
	assert.Equal(t, []string{"4cbb3f34", "Fix", "flaky", "spec", "-"}, strings.Fields(lines[2]), "an empty cell prints a dash")
	assert.NotContains(t, out.String(), "w-1", "an untagged field stays out of the table")
}

func TestPrinterResultPrintsAnEmptyListAsTheHeaderOnly(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(testList{Rows: []testRow{}}))
	assert.Equal(t, []string{"ID", "TITLE", "STATE"}, strings.Fields(out.String()))
}

func TestPrinterResultPrintsAnObjectAsAlignedKeyValueLines(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(testRow{ID: "87a19d5f-4838-4e34-9f0f-56130f0a62b1", Title: "CLI Design", WorkerID: "w-1"}))
	assert.Equal(t, "id:        87a19d5f-4838-4e34-9f0f-56130f0a62b1\ntitle:     CLI Design\nworker_id: w-1\n", out.String(),
		"a key-value view prints the whole id, and omitempty drops the empty state")
}

func TestPrinterResultNestsObjectsAndListsUnderTheirKey(t *testing.T) {
	type inner struct {
		Branch string `json:"branch"`
	}
	type outer struct {
		Name    string    `json:"name"`
		Tags    []string  `json:"tags"`
		Empty   []string  `json:"empty"`
		Git     *inner    `json:"git"`
		Rows    []testRow `json:"rows"`
		Skipped string    `json:"-"`
	}
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(outer{
		Name:    "x",
		Tags:    []string{"a", "b"},
		Empty:   []string{},
		Git:     &inner{Branch: "main"},
		Rows:    []testRow{{ID: "1", Title: "t"}},
		Skipped: "secret",
	}))
	text := out.String()
	assert.Contains(t, text, "name:  x\n")
	assert.Contains(t, text, "tags:  a, b\n")
	assert.Contains(t, text, "empty: -\n")
	assert.Contains(t, text, "git:\n  branch: main\n")
	assert.Contains(t, text, "rows:\n  ID")
	assert.NotContains(t, text, "secret", `a "-" field never prints`)
}

func TestPrinterResultIndentsTheContinuationOfAMultiLineValue(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(map[string]string{"a": "one\ntwo"}))
	assert.Equal(t, "a: one\n   two\n", out.String())
}

func TestPrinterResultTruncatesALongCell(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	long := strings.Repeat("x", maxCellRunes+10)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "1", Title: long}}}))
	assert.Contains(t, out.String(), strings.Repeat("x", maxCellRunes-1)+"…")
	assert.NotContains(t, out.String(), long)
}

func TestPrinterResultReplacesTabsAndNewlinesInACell(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "1", Title: "a\tb\nc"}}}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2, "a newline inside a cell must not add a row")
	assert.Contains(t, lines[1], "a b c")
}

func TestPrinterResultDerivesColumnsForAnUntaggedList(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(map[string]any{"items": []map[string]any{{"b": 2, "a": "x"}, {"c": true}}}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	assert.Equal(t, []string{"A", "B", "C"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"x", "2", "-"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"-", "-", "true"}, strings.Fields(lines[2]))
}

func TestPrinterResultPrintsTimesInUTC(t *testing.T) {
	type when struct {
		At time.Time `json:"at"`
	}
	p, out, _ := newTestPrinter(FormatText)
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.FixedZone("KST", 9*3600))
	require.NoError(t, p.Result(when{At: at}))
	assert.Equal(t, "at: 2026-11-19T01:00:00Z\n", out.String())
}

func TestPrinterResultReadsATypeThatEncodesItself(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(selfEncoding{}))
	assert.Equal(t, "kind: custom\n", out.String())
}

type selfEncoding struct{}

func (selfEncoding) MarshalJSON() ([]byte, error) { return []byte(`{"kind":"custom"}`), nil }

// valuedCell is an object whose text form is one line.
type valuedCell struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (c valuedCell) TextValue() string { return c.Name + " (" + c.Kind + ")" }

type valuedRow struct {
	ID   string      `json:"id" table:"ID"`
	From *valuedCell `json:"from,omitempty" table:"FROM"`
}

func TestPrinterResultPrintsATextValuerInOneCell(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(map[string][]valuedRow{"rows": {
		{ID: "1", From: &valuedCell{Name: "agent 87a19d5f", Kind: "Claude Code"}},
		{ID: "2"},
	}}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"1", "agent", "87a19d5f", "(Claude", "Code)"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"2", "-"}, strings.Fields(lines[2]), "an absent value prints a dash")
}

func TestPrinterResultPrintsATextValuerOnItsKeyLine(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(valuedRow{ID: "1", From: &valuedCell{Name: "a", Kind: "b"}}))
	assert.Equal(t, "id:   1\nfrom: a (b)\n", out.String())
}

func TestPrinterResultEncodesATextValuerAsItsFieldsInJSON(t *testing.T) {
	p, out, _ := newTestPrinter(FormatJSON)
	require.NoError(t, p.Result(valuedRow{ID: "1", From: &valuedCell{Name: "a", Kind: "b"}}))
	assert.JSONEq(t, `{"data":{"id":"1","from":{"name":"a","kind":"b"}}}`, out.String())
}

func TestPrinterResultUsesATextRenderer(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(rendered("raw\x1b[0m bytes")))
	assert.Equal(t, "raw\x1b[0m bytes", out.String())
}

type rendered string

func (r rendered) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, string(r))
	return err
}

func TestPrinterResultPrintsNothingForNil(t *testing.T) {
	p, out, _ := newTestPrinter(FormatJSON)
	require.NoError(t, p.Result(nil))
	var nilRow *testRow
	require.NoError(t, p.Result(nilRow))
	assert.Empty(t, out.String())
}

func TestPrinterResultPrintsOneJSONEnvelopeLine(t *testing.T) {
	p, out, _ := newTestPrinter(FormatJSON)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "87a19d5f-4838", Title: "t"}}}))
	assert.Equal(t, 1, strings.Count(out.String(), "\n"), "an envelope is one line")
	var env struct {
		Data testList `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	assert.Equal(t, "87a19d5f-4838", env.Data.Rows[0].ID, "JSON keeps the whole id")
}

type quietLinesStub []string

func (q quietLinesStub) QuietLines() []string { return q }

func TestPrinterQuietPrintsTheIDs(t *testing.T) {
	t.Run("of an object", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Result(testRow{ID: "abc", Title: "t"}))
		assert.Equal(t, "abc\n", out.String())
	})
	t.Run("of the one list an object holds", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "a"}, {ID: "b"}}}))
		assert.Equal(t, "a\nb\n", out.String())
	})
	t.Run("of a top-level slice and a paginated wrapper with sibling metadata", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Result([]testRow{{ID: "s1"}, {ID: "s2"}}))
		assert.Equal(t, "s1\ns2\n", out.String())

		out.Reset()
		wrapper := struct {
			Cursor string    `json:"cursor"`
			Rows   []testRow `json:"rows"`
		}{Cursor: "next", Rows: []testRow{{ID: "p1"}, {ID: "p2"}}}
		require.NoError(t, p.Result(wrapper))
		assert.Equal(t, "p1\np2\n", out.String())
	})
	t.Run("sanitizes ANSI and line breaks while preserving tabs in QuietRenderer", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Result(quietLinesStub{"id-1\t\x1b[31mhandle\x1b[0m\nnext\r\nend"}))
		assert.Equal(t, "id-1\thandle next end\n", out.String())
	})
	t.Run("of nothing when the result has no id", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Result(map[string]string{"name": "x"}))
		assert.Empty(t, out.String())
	})
}

func TestPrinterEvent(t *testing.T) {
	t.Run("prints a JSON envelope per event", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatJSON)
		require.NoError(t, p.Event(testRow{ID: "a"}))
		require.NoError(t, p.Event(testRow{ID: "b"}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.JSONEq(t, `{"data":{"id":"a","title":"","worker_id":""}}`, lines[0])
	})
	t.Run("prints unwrapped NDJSON with optional field projection", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatNDJSON)
		p.Fields = []string{"id"}
		require.NoError(t, p.Event(testRow{ID: "e1", Title: "secret"}))
		assert.JSONEq(t, `{"id":"e1"}`, strings.TrimSpace(out.String()))
	})
	t.Run("prints IDs in quiet mode", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatQuiet)
		require.NoError(t, p.Event(testRow{ID: "e1", Title: "ignored"}))
		require.NoError(t, p.Event(quietLinesStub{"q1\th1"}))
		assert.Equal(t, "e1\nq1\th1\n", out.String())
	})
	t.Run("prints TSV for object with fields and for scalar events", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatTSV)
		p.Fields = []string{"id", "title"}
		require.NoError(t, p.Event(testRow{ID: "e1", Title: "hello\tworld", WorkerID: "w1"}))
		assert.Equal(t, "e1\thello world\n", out.String())

		out.Reset()
		require.NoError(t, p.Event("plain-scalar"))
		assert.Equal(t, "plain-scalar\n", out.String())
	})
	t.Run("prints the text line of an event and sanitizes single-line vs multi-line", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Event(liner("seq\t1\x1b[31mred\x1b[0m")))
		assert.Equal(t, "seq 1red\n", out.String())

		out.Reset()
		require.NoError(t, p.Event(liner("line1\n\t\x1b[31mline2\x1b[0m")))
		assert.Equal(t, "line1\n\tline2\n", out.String())
	})
	t.Run("prints compact JSON for an event without a text line", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Event(map[string]int{"n": 1}))
		assert.Equal(t, "{\"n\":1}\n", out.String())
	})
}

type liner string

func (l liner) TextLine() string { return string(l) }

func TestPrinterFailure(t *testing.T) {
	e := Newf(CodeTabBusyRefused, "tab 87a19d5f has an agent turn in progress").WithHint("pass --allow-busy to close it anyway")
	t.Run("prints the code, the message and the hints on stderr in text", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatText)
		p.Failure(e)
		assert.Empty(t, out.String())
		assert.Equal(t, "error: tab_busy_refused: tab 87a19d5f has an agent turn in progress\nhint: pass --allow-busy to close it anyway\n", errOut.String())
	})
	t.Run("applies ANSI styling to text failures when ColorAlways is set", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatText)
		p.Color = ColorAlways
		p.Failure(e)
		assert.Empty(t, out.String())
		assert.Contains(t, errOut.String(), ansiBoldRed+"error:"+ansiReset)
		assert.Contains(t, errOut.String(), ansiCyan+"hint:"+ansiReset)
	})
	t.Run("prints quiet failures on stderr without ANSI or stdout envelope", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatQuiet)
		p.Color = ColorAlways
		p.Failure(e)
		assert.Empty(t, out.String())
		assert.Equal(t, "error: tab_busy_refused: tab 87a19d5f has an agent turn in progress\nhint: pass --allow-busy to close it anyway\n", errOut.String())
	})
	t.Run("prints the envelope on stdout and the error line on stderr in JSON and NDJSON", func(t *testing.T) {
		withReceipt := Newf(CodeTimeout, "timed out").WithHint("retry safely").WithReceipt(&RetryReceipt{
			RequestID:   "req-42",
			Disposition: "uncertain",
		})
		for _, format := range []Format{FormatJSON, FormatNDJSON} {
			p, out, errOut := newTestPrinter(format)
			p.Failure(withReceipt)
			assert.JSONEq(t, `{"error":{"code":"timeout","message":"timed out","hints":["retry safely"],"retryable":false,"receipt":{"request_id":"req-42","disposition":"uncertain"}}}`, out.String())
			assert.Equal(t, "error: timeout: timed out\n", errOut.String())
		}
	})
	t.Run("lists the candidates of an ambiguous target", func(t *testing.T) {
		amb := Newf(CodeAmbiguousTarget, "2 tabs match")
		amb.Candidates = []Candidate{{ID: "87a19d5f-1", Kind: "agent", Title: "One"}, {ID: "87a19d5f-2", Kind: "terminal", Title: "Two"}}
		p, _, errOut := newTestPrinter(FormatText)
		p.Failure(amb)
		assert.Contains(t, errOut.String(), "candidates:\n  ID")
		assert.Contains(t, errOut.String(), "87a19d5f-1")
		jp, out, _ := newTestPrinter(FormatJSON)
		jp.Failure(amb)
		assert.Contains(t, out.String(), `"candidates":[{"id":"87a19d5f-1","kind":"agent","title":"One"}`)
	})
	t.Run("prints nothing for nil", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatJSON)
		p.Failure(nil)
		assert.Empty(t, out.String()+errOut.String())
	})
}

func TestParseFormatParseColorModeAndTerminalWidth(t *testing.T) {
	for raw, want := range map[string]Format{
		"":       FormatText,
		" text ": FormatText,
		"JSON":   FormatJSON,
		"ndjson": FormatNDJSON,
		"TSV":    FormatTSV,
	} {
		got, err := ParseFormat(raw)
		require.NoError(t, err, "raw=%q", raw)
		assert.Equal(t, want, got, "raw=%q", raw)
	}
	_, err := ParseFormat("yaml")
	require.Error(t, err)
	assert.Equal(t, CodeUsage, FromError(err).Code)

	for raw, want := range map[string]ColorMode{
		"":       ColorAuto,
		" auto ": ColorAuto,
		"ALWAYS": ColorAlways,
		"never":  ColorNever,
	} {
		got, err := ParseColorMode(raw)
		require.NoError(t, err, "raw=%q", raw)
		assert.Equal(t, want, got, "raw=%q", raw)
	}
	_, err = ParseColorMode("sometimes")
	require.Error(t, err)
	assert.Equal(t, CodeUsage, FromError(err).Code)

	assert.Zero(t, TerminalWidth(&bytes.Buffer{}))
	assert.Zero(t, TerminalWidth((*os.File)(nil)))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	assert.Zero(t, TerminalWidth(r))
}

func TestPrinterNoticeGoesToStderr(t *testing.T) {
	p, out, errOut := newTestPrinter(FormatJSON)
	p.Notice("open %s", "https://example.invalid")
	assert.Empty(t, out.String())
	assert.Equal(t, "open https://example.invalid\n", errOut.String())
}

func TestShortID(t *testing.T) {
	assert.Equal(t, "", ShortID(""))
	assert.Equal(t, "abc", ShortID("abc"))
	assert.Equal(t, "12345678", ShortID("12345678"))
	assert.Equal(t, "12345678", ShortID("123456789"))
	assert.Equal(t, "日本語のタブです", ShortID("日本語のタブですよね"), "the cut counts characters, not bytes")
}

type ptrSelfEncoding struct {
	Label string
}

func (p *ptrSelfEncoding) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"encoded": p.Label})
}

type ptrValuedCell struct {
	Name string
}

func (p *ptrValuedCell) TextValue() string { return "ptr:" + p.Name }

func TestToNode_PointerReceiverMarshalerAndTextValuer(t *testing.T) {
	t.Run("pointer-receiver MarshalJSON", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(&ptrSelfEncoding{Label: "ok"}))
		assert.Equal(t, "encoded: ok\n", out.String())
	})
	t.Run("pointer-receiver TextValuer", func(t *testing.T) {
		type row struct {
			ID   string         `json:"id" table:"ID"`
			From *ptrValuedCell `json:"from" table:"FROM"`
		}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(row{ID: "1", From: &ptrValuedCell{Name: "alice"}}))
		assert.Equal(t, "id:   1\nfrom: ptr:alice\n", out.String())
	})
	t.Run("pointer to time.Time still formats UTC and skips zero", func(t *testing.T) {
		type when struct {
			At   *time.Time `json:"at"`
			Zero *time.Time `json:"zero"`
		}
		at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.FixedZone("KST", 9*3600))
		zero := time.Time{}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(when{At: &at, Zero: &zero}))
		assert.Equal(t, "at: 2026-11-19T01:00:00Z\n", out.String())
	})
}

func TestToNode_Float32Precision(t *testing.T) {
	type metric struct {
		Ratio float32 `json:"ratio"`
	}
	p, out, _ := newTestPrinter(FormatText)
	require.NoError(t, p.Result(metric{Ratio: float32(0.1)}))
	assert.Equal(t, "ratio: 0.1\n", out.String())
}

type customZeroFlag struct {
	Sentinel int
}

func (c customZeroFlag) IsZero() bool { return c.Sentinel < 0 }

type unexportedEmbeddedBase struct {
	ID    string `json:"id" table:"ID"`
	Title string `json:"title" table:"TITLE"`
}

func TestToNode_OmitZeroMethodAndUnexportedEmbeddedStruct(t *testing.T) {
	t.Run("omitzero honors IsZero method", func(t *testing.T) {
		type sample struct {
			Hidden customZeroFlag `json:"hidden,omitzero"`
			Shown  customZeroFlag `json:"shown,omitzero"`
		}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(sample{
			Hidden: customZeroFlag{Sentinel: -1},
			Shown:  customZeroFlag{Sentinel: 2},
		}))
		assert.NotContains(t, out.String(), "hidden")
		assert.Contains(t, out.String(), "shown:")
	})

	t.Run("unexported embedded struct promotes exported fields and table columns", func(t *testing.T) {
		type derived struct {
			unexportedEmbeddedBase
			State string `json:"state" table:"STATE"`
		}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(derived{
			unexportedEmbeddedBase: unexportedEmbeddedBase{ID: "a1", Title: "Hello"},
			State:                  "ready",
		}))
		assert.Equal(t, "id:    a1\ntitle: Hello\nstate: ready\n", out.String())

		out.Reset()
		require.NoError(t, p.Result(struct {
			Rows []derived `json:"rows"`
		}{Rows: []derived{{
			unexportedEmbeddedBase: unexportedEmbeddedBase{ID: "a1", Title: "Hello"},
			State:                  "ready",
		}}}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, []string{"ID", "TITLE", "STATE"}, strings.Fields(lines[0]))
		assert.Equal(t, []string{"a1", "Hello", "ready"}, strings.Fields(lines[1]))
	})

	t.Run("nil embedded struct pointer does not panic", func(t *testing.T) {
		type derivedPtr struct {
			*unexportedEmbeddedBase
			State string `json:"state"`
		}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(derivedPtr{State: "idle"}))
		assert.Equal(t, "state: idle\n", out.String())
	})

	t.Run("byte slice encodes base64 and skips nil", func(t *testing.T) {
		type payload struct {
			Data  []byte `json:"data"`
			Empty []byte `json:"empty"`
			Nil   []byte `json:"nil"`
		}
		p, out, _ := newTestPrinter(FormatText)
		require.NoError(t, p.Result(payload{Data: []byte("hi"), Empty: []byte{}, Nil: nil}))
		assert.Equal(t, "data:  aGk=\nempty: \n", out.String())
	})
}

type optionsRenderedStub struct {
	seenOpts TableOptions
	called   bool
}

func (o *optionsRenderedStub) RenderTextWithOptions(w io.Writer, opts TableOptions) error {
	o.called = true
	o.seenOpts = opts
	_, err := io.WriteString(w, "custom-options-output\n")
	return err
}

func TestRenderSingleLine_HonorsOptionsTextRenderer(t *testing.T) {
	stub := &optionsRenderedStub{}
	var out bytes.Buffer
	p := &Printer{
		Out:           &out,
		Err:           &bytes.Buffer{},
		Format:        FormatText,
		Wide:          true,
		Color:         ColorAlways,
		oneLineResult: true,
	}
	require.NoError(t, p.Result(stub))
	assert.True(t, stub.called, "oneLineResult must dispatch OptionsTextRenderer")
	assert.Equal(t, "custom-options-output\n", out.String())
	assert.True(t, stub.seenOpts.opts.wide)
	assert.True(t, stub.seenOpts.opts.style.enabled)
}

type quotaWriter struct {
	buf       bytes.Buffer
	remaining int
	failErr   error
}

func (q *quotaWriter) Write(p []byte) (int, error) {
	if q.remaining <= 0 {
		return 0, q.failErr
	}
	if len(p) <= q.remaining {
		q.buf.Write(p)
		q.remaining -= len(p)
		return len(p), nil
	}
	n := q.remaining
	q.buf.Write(p[:n])
	q.remaining = 0
	return n, q.failErr
}

func TestPrefixWriter_FailingAndShortWriter(t *testing.T) {
	t.Run("partial write mid-line returns accurate source count and preserves atLineStart", func(t *testing.T) {
		errFull := errors.New("disk full")
		// Allow "  line1\n" (8) + "  " (2) + "lin" (3) = 13 bytes.
		qw := &quotaWriter{remaining: 13, failErr: errFull}
		pw := indentWriter(qw, "  ")

		input := []byte("line1\nline2\n")
		n, err := pw.Write(input)
		require.ErrorIs(t, err, errFull)
		assert.Equal(t, 9, n, "must report exact source bytes written (line1\\n + lin)")

		// Restore quota and finish writing the remaining source bytes.
		qw.remaining = 100
		n2, err := pw.Write(input[n:])
		require.NoError(t, err)
		assert.Equal(t, len(input)-n, n2)
		assert.Equal(t, "  line1\n  line2\n", qw.buf.String(), "continuation must not emit a duplicate prefix")
	})

	t.Run("failure before first byte keeps atLineStart true", func(t *testing.T) {
		errFail := errors.New("broken pipe")
		qw := &quotaWriter{remaining: 0, failErr: errFail}
		pw := indentWriter(qw, "  ")

		n, err := pw.Write([]byte("hello\n"))
		require.ErrorIs(t, err, errFail)
		assert.Equal(t, 0, n)

		qw.remaining = 100
		n2, err := pw.Write([]byte("hello\n"))
		require.NoError(t, err)
		assert.Equal(t, 6, n2)
		assert.Equal(t, "  hello\n", qw.buf.String())
	})

	t.Run("short write without error returns io.ErrShortWrite", func(t *testing.T) {
		// Allow prefix "  " (2) + 2 bytes of "hello\n", with nil error.
		qw := &quotaWriter{remaining: 4, failErr: nil}
		pw := indentWriter(qw, "  ")

		n, err := pw.Write([]byte("hello\n"))
		require.ErrorIs(t, err, io.ErrShortWrite)
		assert.Equal(t, 2, n)
	})
}
