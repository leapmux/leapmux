package cli

import (
	"bytes"
	"encoding/json"
	"io"
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

func newTestPrinter(format Format, quiet bool) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &Printer{Out: &out, Err: &errOut, Format: format, Quiet: quiet}, &out, &errOut
}

func TestPrinterResultPrintsAListAsATableWithTaggedColumns(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
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
	p, out, _ := newTestPrinter(FormatText, false)
	require.NoError(t, p.Result(testList{Rows: []testRow{}}))
	assert.Equal(t, []string{"ID", "TITLE", "STATE"}, strings.Fields(out.String()))
}

func TestPrinterResultPrintsAnObjectAsAlignedKeyValueLines(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
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
	p, out, _ := newTestPrinter(FormatText, false)
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
	p, out, _ := newTestPrinter(FormatText, false)
	require.NoError(t, p.Result(map[string]string{"a": "one\ntwo"}))
	assert.Equal(t, "a: one\n   two\n", out.String())
}

func TestPrinterResultTruncatesALongCell(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
	long := strings.Repeat("x", maxCellRunes+10)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "1", Title: long}}}))
	assert.Contains(t, out.String(), strings.Repeat("x", maxCellRunes-1)+"…")
	assert.NotContains(t, out.String(), long)
}

func TestPrinterResultReplacesTabsAndNewlinesInACell(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "1", Title: "a\tb\nc"}}}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2, "a newline inside a cell must not add a row")
	assert.Contains(t, lines[1], "a b c")
}

func TestPrinterResultDerivesColumnsForAnUntaggedList(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
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
	p, out, _ := newTestPrinter(FormatText, false)
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.FixedZone("KST", 9*3600))
	require.NoError(t, p.Result(when{At: at}))
	assert.Equal(t, "at: 2026-11-19T01:00:00Z\n", out.String())
}

func TestPrinterResultReadsATypeThatEncodesItself(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
	require.NoError(t, p.Result(selfEncoding{}))
	assert.Equal(t, "kind: custom\n", out.String())
}

type selfEncoding struct{}

func (selfEncoding) MarshalJSON() ([]byte, error) { return []byte(`{"kind":"custom"}`), nil }

func TestPrinterResultUsesATextRenderer(t *testing.T) {
	p, out, _ := newTestPrinter(FormatText, false)
	require.NoError(t, p.Result(rendered("raw\x1b[0m bytes")))
	assert.Equal(t, "raw\x1b[0m bytes", out.String())
}

type rendered string

func (r rendered) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, string(r))
	return err
}

func TestPrinterResultPrintsNothingForNil(t *testing.T) {
	p, out, _ := newTestPrinter(FormatJSON, false)
	require.NoError(t, p.Result(nil))
	var nilRow *testRow
	require.NoError(t, p.Result(nilRow))
	assert.Empty(t, out.String())
}

func TestPrinterResultPrintsOneJSONEnvelopeLine(t *testing.T) {
	p, out, _ := newTestPrinter(FormatJSON, false)
	require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "87a19d5f-4838", Title: "t"}}}))
	assert.Equal(t, 1, strings.Count(out.String(), "\n"), "an envelope is one line")
	var env struct {
		Data testList `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	assert.Equal(t, "87a19d5f-4838", env.Data.Rows[0].ID, "JSON keeps the whole id")
}

func TestPrinterQuietPrintsTheIDs(t *testing.T) {
	t.Run("of an object", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText, true)
		require.NoError(t, p.Result(testRow{ID: "abc", Title: "t"}))
		assert.Equal(t, "abc\n", out.String())
	})
	t.Run("of the one list an object holds", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText, true)
		require.NoError(t, p.Result(testList{Rows: []testRow{{ID: "a"}, {ID: "b"}}}))
		assert.Equal(t, "a\nb\n", out.String())
	})
	t.Run("of nothing when the result has no id", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText, true)
		require.NoError(t, p.Result(map[string]string{"name": "x"}))
		assert.Empty(t, out.String())
	})
	t.Run("even with JSON", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatJSON, true)
		require.NoError(t, p.Result(testRow{ID: "abc"}))
		assert.Equal(t, "abc\n", out.String())
	})
}

func TestPrinterEvent(t *testing.T) {
	t.Run("prints a JSON envelope per event", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatJSON, false)
		require.NoError(t, p.Event(testRow{ID: "a"}))
		require.NoError(t, p.Event(testRow{ID: "b"}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.JSONEq(t, `{"data":{"id":"a","title":"","worker_id":""}}`, lines[0])
	})
	t.Run("prints the text line of an event that has one", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText, false)
		require.NoError(t, p.Event(liner("seq 1")))
		assert.Equal(t, "seq 1\n", out.String())
	})
	t.Run("prints compact JSON for an event without a text line", func(t *testing.T) {
		p, out, _ := newTestPrinter(FormatText, false)
		require.NoError(t, p.Event(map[string]int{"n": 1}))
		assert.Equal(t, "{\"n\":1}\n", out.String())
	})
}

type liner string

func (l liner) TextLine() string { return string(l) }

func TestPrinterFailure(t *testing.T) {
	e := Newf(CodeTabBusyRefused, "tab 87a19d5f has an agent turn in progress").WithHint("pass --allow-busy to close it anyway")
	t.Run("prints the code, the message and the hints on stderr in text", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatText, false)
		p.Failure(e)
		assert.Empty(t, out.String())
		assert.Equal(t, "error: tab_busy_refused: tab 87a19d5f has an agent turn in progress\nhint: pass --allow-busy to close it anyway\n", errOut.String())
	})
	t.Run("prints the envelope on stdout and the error line on stderr in JSON", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatJSON, false)
		p.Failure(e)
		assert.JSONEq(t, `{"error":{"code":"tab_busy_refused","message":"tab 87a19d5f has an agent turn in progress","hints":["pass --allow-busy to close it anyway"],"retryable":false}}`, out.String())
		assert.Equal(t, "error: tab_busy_refused: tab 87a19d5f has an agent turn in progress\n", errOut.String())
	})
	t.Run("lists the candidates of an ambiguous target", func(t *testing.T) {
		amb := Newf(CodeAmbiguousTarget, "2 tabs match")
		amb.Candidates = []Candidate{{ID: "87a19d5f-1", Kind: "agent", Title: "One"}, {ID: "87a19d5f-2", Kind: "terminal", Title: "Two"}}
		p, _, errOut := newTestPrinter(FormatText, false)
		p.Failure(amb)
		assert.Contains(t, errOut.String(), "candidates:\n  ID")
		assert.Contains(t, errOut.String(), "87a19d5f-1")
		jp, out, _ := newTestPrinter(FormatJSON, false)
		jp.Failure(amb)
		assert.Contains(t, out.String(), `"candidates":[{"id":"87a19d5f-1","kind":"agent","title":"One"}`)
	})
	t.Run("prints nothing for nil", func(t *testing.T) {
		p, out, errOut := newTestPrinter(FormatJSON, false)
		p.Failure(nil)
		assert.Empty(t, out.String()+errOut.String())
	})
}

func TestPrinterNoticeGoesToStderr(t *testing.T) {
	p, out, errOut := newTestPrinter(FormatJSON, false)
	p.Notice("open %s", "https://example.invalid")
	assert.Empty(t, out.String())
	assert.Equal(t, "open https://example.invalid\n", errOut.String())
}
