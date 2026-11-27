package cmd

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// screenOf builds the record of a terminal of cols by rows whose retained
// bytes are screen.
func screenOf(cols, rows uint32, screen string) *leapmuxv1.TerminalInfo {
	return &leapmuxv1.TerminalInfo{TerminalId: "term-1", Cols: cols, Rows: rows, Screen: []byte(screen), ScreenEndOffset: int64(len(screen))}
}

// render renders info with opts and fails the test on an error.
func render(t *testing.T, info *leapmuxv1.TerminalInfo, opts screenReadOptions) renderedScreen {
	t.Helper()
	got, err := renderScreen(info, opts)
	require.NoError(t, err)
	return got
}

// numberedLines returns "line 1\r\n" to "line n\r\n".
func numberedLines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d\r\n", i)
	}
	return sb.String()
}

// TestRenderScreen_Fixtures replays fixtures through the emulator and pins the
// text that the browser shows for the same bytes.
func TestRenderScreen_Fixtures(t *testing.T) {
	for name, tc := range map[string]struct {
		info       *leapmuxv1.TerminalInfo
		wantLines  []string
		wantCursor screenCursor
	}{
		"colors and attributes print as plain text": {
			info:       screenOf(40, 5, "\x1b[31mred\x1b[0m plain\r\n\x1b[1;4;38;5;208mbold orange\x1b[m \x1b[48;2;10;20;30mbg\x1b[0m"),
			wantLines:  []string{"red plain", "bold orange bg"},
			wantCursor: screenCursor{Row: 1, Col: 14},
		},
		"cursor moves place the text": {
			info:       screenOf(20, 5, "\x1b[2J\x1b[3;5Hthird\x1b[1;1Hfirst\x1b[2;10H\x1b[Cx"),
			wantLines:  []string{"first", "          x", "    third"},
			wantCursor: screenCursor{Row: 1, Col: 11},
		},
		"a long line wraps at the width": {
			info:       screenOf(10, 4, "abcdefghijklmnopqrstuvwxy"),
			wantLines:  []string{"abcdefghij", "klmnopqrst", "uvwxy"},
			wantCursor: screenCursor{Row: 2, Col: 5},
		},
		"a carriage return overwrites the row": {
			info:       screenOf(30, 3, "progress  10%\rprogress 100%\r\nhello world\rHELLO"),
			wantLines:  []string{"progress 100%", "HELLO world"},
			wantCursor: screenCursor{Row: 1, Col: 5},
		},
		"wide characters take two columns": {
			info:       screenOf(10, 4, "日本語abc\r\n😀 ok\r\n日本語日本語"),
			wantLines:  []string{"日本語abc", "😀 ok", "日本語日本", "語"},
			wantCursor: screenCursor{Row: 3, Col: 2},
		},
		"a wide character that does not fit wraps whole": {
			info:       screenOf(9, 3, "日本語日本"),
			wantLines:  []string{"日本語日", "本"},
			wantCursor: screenCursor{Row: 1, Col: 2},
		},
		"the cursor after the last column stays on it": {
			info:       screenOf(10, 3, "abcdefghij"),
			wantLines:  []string{"abcdefghij"},
			wantCursor: screenCursor{Row: 0, Col: 9},
		},
		"blank cells and empty rows at the end do not print": {
			info:       screenOf(20, 6, "a   \r\n\r\nb  \x1b[5;1H     \r\n"),
			wantLines:  []string{"a", "", "b"},
			wantCursor: screenCursor{Row: 5, Col: 0},
		},
		"an empty screen prints no line": {
			info:       screenOf(80, 24, ""),
			wantLines:  []string{},
			wantCursor: screenCursor{},
		},
		"a clear screen prints no line": {
			info:       screenOf(20, 4, "old text\r\n\x1b[H\x1b[2J"),
			wantLines:  []string{},
			wantCursor: screenCursor{},
		},
		"a sequence cut by the ring is no failure": {
			// xterm.js skips a byte that starts no UTF-8 sequence, and the
			// rest of a cut sequence then reads as text.
			info:       screenOf(20, 3, "\xa9[31mtail of a cut\r\n\x1b[3"),
			wantLines:  []string{"[31mtail of a cut"},
			wantCursor: screenCursor{Row: 1, Col: 0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, tc.info, screenReadOptions{})
			assert.Equal(t, tc.wantLines, got.Lines)
			assert.Equal(t, tc.wantCursor, got.Cursor)
			assert.False(t, got.AlternateScreen)
			assert.Equal(t, tc.info.GetTerminalId(), got.ID)
			assert.Equal(t, tc.info.GetCols(), got.Cols)
			assert.Equal(t, tc.info.GetRows(), got.Rows)
			assert.Equal(t, tc.info.GetScreenEndOffset(), got.ScreenEndOffset)
		})
	}
}

// The alternate screen shows the full-screen program alone, and it has no
// scrollback, as in xterm.js. Leaving it shows the normal screen again.
func TestRenderScreen_AlternateScreen(t *testing.T) {
	normal := numberedLines(6) + "$ vim"
	inVim := normal + "\x1b[?1049h\x1b[H\x1b[2J~ file.txt\r\n~\x1b[4;1H-- INSERT --"
	for _, opts := range []screenReadOptions{{}, {scrollback: true}} {
		got := render(t, screenOf(20, 4, inVim), opts)
		assert.True(t, got.AlternateScreen)
		assert.Equal(t, []string{"~ file.txt", "~", "", "-- INSERT --"}, got.Lines, "scrollback %v", opts.scrollback)
		assert.Equal(t, screenCursor{Row: 3, Col: 12}, got.Cursor)
	}

	left := render(t, screenOf(20, 4, inVim+"\x1b[?1049l"), screenReadOptions{scrollback: true})
	assert.False(t, left.AlternateScreen)
	assert.Equal(t, []string{"line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "$ vim"}, left.Lines,
		"the normal screen and its scrollback come back")

	// The Worker prefixes the bytes with the modes that it tracked, so a
	// program that entered the alternate screen before the retained window
	// still renders on it.
	prefixed := render(t, screenOf(20, 4, "\x1b[?1049h\x1b[Hstatus line"), screenReadOptions{})
	assert.True(t, prefixed.AlternateScreen)
	assert.Equal(t, []string{"status line"}, prefixed.Lines)
}

func TestRenderScreenUsesRetainedGeometryBeforeResize(t *testing.T) {
	const output = "\x1b[1;8HREADY\x1b[2;1HNOPE"
	info := screenOf(12, 2, output)
	info.ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{
		Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2},
		Changes: []*leapmuxv1.TerminalScreenGeometry{
			{Epoch: "screen", Offset: int64(len(output)), Revision: 2, Cols: 12, Rows: 2},
		},
	}
	got := render(t, info, screenReadOptions{})
	assert.NotContains(t, strings.Join(got.Lines, "\n"), "READY")
	assert.Contains(t, strings.Join(got.Lines, "\n"), "NOPE")
}

func TestRenderScreen_ScrollbackAndLines(t *testing.T) {
	info := screenOf(20, 5, numberedLines(8)+"$ ")
	for name, tc := range map[string]struct {
		opts screenReadOptions
		want []string
	}{
		"the screen":                   {screenReadOptions{}, []string{"line 5", "line 6", "line 7", "line 8", "$"}},
		"the scrollback above it":      {screenReadOptions{scrollback: true}, []string{"line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "$"}},
		"the last lines of the screen": {screenReadOptions{lines: 2}, []string{"line 8", "$"}},
		"the last lines across both":   {screenReadOptions{scrollback: true, lines: 7}, []string{"line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "$"}},
		"more lines than there are":    {screenReadOptions{lines: 100}, []string{"line 5", "line 6", "line 7", "line 8", "$"}},
		"one line":                     {screenReadOptions{lines: 1}, []string{"$"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, info, tc.opts)
			assert.Equal(t, tc.want, got.Lines)
			assert.Equal(t, screenCursor{Row: 4, Col: 2}, got.Cursor, "the cursor counts from the top of the screen")
		})
	}

	// --lines counts the lines that print, after the empty rows at the end
	// are gone.
	short := render(t, screenOf(20, 10, "one\r\ntwo\r\n"), screenReadOptions{lines: 1})
	assert.Equal(t, []string{"two"}, short.Lines)

	// --lines on an empty screen returns a non-nil empty slice.
	emptyLines := render(t, screenOf(20, 5, ""), screenReadOptions{lines: 5})
	assert.Equal(t, []string{}, emptyLines.Lines)

	// --lines preserves internal blank lines while skipping trailing blanks.
	withInternalBlank := render(t, screenOf(20, 6, "a\r\n\r\nb\r\n\r\n"), screenReadOptions{lines: 2})
	assert.Equal(t, []string{"", "b"}, withInternalBlank.Lines)

	// --lines with large scrollback returns only the last N non-empty lines.
	big := render(t, screenOf(20, 5, numberedLines(contracts.TerminalScrollbackLines+20)), screenReadOptions{scrollback: true, lines: 3})
	assert.Equal(t, []string{"line 1018", "line 1019", "line 1020"}, big.Lines)
}

// The scrollback keeps at most the 1000 lines that the browser keeps.
func TestRenderScreen_ScrollbackLimit(t *testing.T) {
	got := render(t, screenOf(20, 5, numberedLines(contracts.TerminalScrollbackLines+50)), screenReadOptions{scrollback: true})
	require.Len(t, got.Lines, contracts.TerminalScrollbackLines+4, "the whole scrollback and the 4 rows above the empty last one")
	assert.Equal(t, "line 47", got.Lines[0])
	assert.Equal(t, "line 1050", got.Lines[len(got.Lines)-1])
}

func TestRenderScreen_RefusesASizeItCannotRender(t *testing.T) {
	for name, info := range map[string]*leapmuxv1.TerminalInfo{
		"no columns":                  screenOf(0, 24, "x"),
		"no rows":                     screenOf(80, 0, "x"),
		"no size":                     screenOf(0, 0, ""),
		"too many columns":            screenOf(maxTerminalDimension+1, 24, "x"),
		"too many rows":               screenOf(80, maxTerminalDimension+1, "x"),
		"too many cells":              screenOf(1000, 5000, "x"),
		"the largest size on the API": screenOf(65535, 65535, "x"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderScreen(info, screenReadOptions{})
			e := cli.FromError(err)
			require.NotNil(t, e)
			assert.Equal(t, cli.CodeInternal, e.Code)
			assert.Contains(t, e.Message, "term-1")
			assert.Equal(t, []string{"pass --raw to print the bytes that it retains"}, e.Hints)
		})
	}

	// The largest screen under the cap renders.
	got := render(t, screenOf(2000, 1000, "corner"), screenReadOptions{})
	assert.Equal(t, []string{"corner"}, got.Lines)

	// The cap counts the rows of the scrollback too. A screen whose cells, the
	// scrollback included, fill the cap exactly renders, and one more column
	// does not. The rows make the rows and the scrollback a power of two, which
	// divides the cap, and keep the columns under the limit of one dimension.
	rowsWithScrollback := 1
	for rowsWithScrollback <= contracts.TerminalScrollbackLines || maxRenderedCells/rowsWithScrollback > maxTerminalDimension {
		rowsWithScrollback <<= 1
	}
	require.Zero(t, maxRenderedCells%rowsWithScrollback, "the cap is a power of two")
	capRows := uint32(rowsWithScrollback - contracts.TerminalScrollbackLines)
	capCols := uint32(maxRenderedCells / rowsWithScrollback)
	atCap := render(t, screenOf(capCols, capRows, "at the cap"), screenReadOptions{})
	assert.Equal(t, []string{"at the cap"}, atCap.Lines)
	_, err := renderScreen(screenOf(capCols+1, capRows, "x"), screenReadOptions{})
	require.Error(t, err, "one column more passes the cap")
	assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code)

	// A terminal narrower than two columns renders at two, as xterm.js
	// does, and reports the size that the Worker gave.
	narrow := render(t, screenOf(1, 3, "abc"), screenReadOptions{})
	assert.Equal(t, []string{"ab", "c"}, narrow.Lines)
	assert.EqualValues(t, 1, narrow.Cols)
}

// renderDimensions is the one size check of `terminal read` and `terminal wait`.
// Both commands refuse the same sizes with the same message, and only the
// read adds its hint, because only the read has --raw.
func TestRenderDimensions(t *testing.T) {
	for name, tc := range map[string]struct {
		info       *leapmuxv1.TerminalInfo
		cols, rows int
		refusal    string
	}{
		"a common size":    {info: screenOf(80, 24, ""), cols: 80, rows: 24},
		"one cell":         {info: screenOf(1, 1, ""), cols: 1, rows: 1},
		"no columns":       {info: screenOf(0, 24, ""), refusal: "the worker reports no size for terminal term-1"},
		"no rows":          {info: screenOf(80, 0, ""), refusal: "the worker reports no size for terminal term-1"},
		"too many columns": {info: screenOf(maxTerminalDimension+1, 1, ""), refusal: "terminal term-1 is 10001 columns by 1 rows, too large to render"},
		"too many rows":    {info: screenOf(1, maxTerminalDimension+1, ""), refusal: "terminal term-1 is 1 columns by 10001 rows, too large to render"},
		"too many cells":   {info: screenOf(1000, 5000, ""), refusal: "too large to render"},
		// One column keeps the rows and the scrollback under the cap of the
		// cells, so only the limit of one side applies.
		"the most rows": {info: screenOf(1, maxTerminalDimension, ""), cols: 1, rows: maxTerminalDimension},
		// The product of the two sides would overflow, and the check of each
		// side refuses the size before the product is computed.
		"the largest values of the fields": {info: screenOf(math.MaxUint32, math.MaxUint32, ""), refusal: "too large to render"},
	} {
		t.Run(name, func(t *testing.T) {
			cols, rows, err := renderDimensions(tc.info.GetTerminalId(), tc.info.GetCols(), tc.info.GetRows())
			if tc.refusal == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.cols, cols)
				assert.Equal(t, tc.rows, rows)
				return
			}
			e := cli.FromError(err)
			require.NotNil(t, e)
			assert.Equal(t, cli.CodeInternal, e.Code)
			assert.Contains(t, e.Message, tc.refusal)
			assert.Empty(t, e.Hints, "a refusal without hints carries none")
			assert.Zero(t, cols)
			assert.Zero(t, rows)

			_, readErr := renderScreen(tc.info, screenReadOptions{})
			_, waitErr := newScreenWatch(tc.info, func(string) bool { return false }, false)
			assert.Equal(t, e.Message, cli.FromError(readErr).Message, "terminal read refuses the size alike")
			assert.Equal(t, e.Message, cli.FromError(waitErr).Message, "terminal wait refuses the size alike")
			assert.Equal(t, []string{rawScreenHint}, cli.FromError(readErr).Hints)
		})
	}
}

func TestRenderedScreen_Forms(t *testing.T) {
	got := render(t, screenOf(20, 4, "one\r\n\r\nthree"), screenReadOptions{})

	var text strings.Builder
	require.NoError(t, got.RenderText(&text))
	assert.Equal(t, "one\n\nthree\n", text.String(), "the text form prints the lines alone")

	assert.Equal(t, map[string]any{
		"id": "term-1", "cols": 20.0, "rows": 4.0, "screen_end_offset": 12.0,
		"alternate_screen": false,
		"cursor":           map[string]any{"row": 2.0, "col": 5.0},
		"lines":            []any{"one", "", "three"},
	}, jsonMap(t, got))

	empty := render(t, screenOf(20, 4, ""), screenReadOptions{})
	text.Reset()
	require.NoError(t, empty.RenderText(&text))
	assert.Empty(t, text.String(), "an empty screen prints nothing")
	assert.Equal(t, []any{}, jsonMap(t, empty)["lines"], "JSON states an empty list, not null")
}

func TestRawScreenOf(t *testing.T) {
	assert.Equal(t, rawScreen{ID: "term-1", Cols: 80, Rows: 24, ScreenEndOffset: 6, Screen: "a\x1b[1mb"},
		rawScreenOf(screenOf(80, 24, "a\x1b[1mb")))
}

// `terminal read` refuses a flag combination before it sends anything: the
// hub of the command line does not exist, so a request would fail otherwise.
func TestTerminalRead_RefusesBadFlags(t *testing.T) {
	clearRemoteEnv(t)
	id := fullTestID("term-1")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"zero lines":            {[]string{"--lines", "0"}, "--lines must be 1 or more; got 0"},
		"negative lines":        {[]string{"--lines", "-3"}, "--lines must be 1 or more; got -3"},
		"lines that is no int":  {[]string{"--lines", "many"}, "many"},
		"raw with lines":        {[]string{"--raw", "--lines", "5"}, "--raw prints the retained bytes as they are"},
		"raw with a scrollback": {[]string{"--raw", "--scrollback"}, "--raw prints the retained bytes as they are"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"terminal", "read", id, "--hub", "https://stub.invalid"}, tc.args...)
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.want)
		})
	}
}

// `terminal read` reads the record of the terminal from its Worker and prints
// the rendered screen, in text and in JSON.
func TestTerminalReadOverIPC_PrintsTheRenderedScreen(t *testing.T) {
	id := fullTestID("term-1")
	info := screenOf(20, 4, numberedLines(5)+"\x1b[32m$\x1b[0m ")
	info.TerminalId = id
	startTerminalWorker(t, &terminalWorker{info: info}, id)

	o := runControlText(t, "terminal", "read", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "line 3\nline 4\nline 5\n$\n", o.Stdout)

	data := runControl(t, "terminal", "read", id, "--scrollback", "--lines", "4").Data(t)
	assert.Equal(t, []any{"line 3", "line 4", "line 5", "$"}, data["lines"])
	assert.Equal(t, map[string]any{"row": 3.0, "col": 2.0}, data["cursor"])
	assert.Equal(t, false, data["alternate_screen"])
	assert.Equal(t, id, data["id"])
	assert.EqualValues(t, 20, data["cols"])
	assert.EqualValues(t, 4, data["rows"])

	o = runControlText(t, "terminal", "read", id, "--raw")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, string(info.GetScreen()), o.Stdout, "--raw prints the retained bytes as they are")
	raw := runControl(t, "terminal", "read", id, "--raw").Data(t)
	assert.Equal(t, string(info.GetScreen()), raw["screen"])

	o = runControlText(t, "terminal", "read", id, "-q")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, id+"\n", o.Stdout)
}

// A Worker that reports no size gets a failure that points at --raw, and
// --raw still prints the bytes.
func TestTerminalReadOverIPC_NoSize(t *testing.T) {
	id := fullTestID("term-1")
	info := screenOf(0, 0, "bytes")
	info.TerminalId = id
	startTerminalWorker(t, &terminalWorker{info: info}, id)

	f := runControl(t, "terminal", "read", id).requireFailure(t, cli.CodeInternal)
	assert.Contains(t, f.Message, "reports no size")
	assert.Equal(t, []string{"pass --raw to print the bytes that it retains"}, f.Hints)

	o := runControlText(t, "terminal", "read", id, "--raw")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "bytes", o.Stdout)
}
