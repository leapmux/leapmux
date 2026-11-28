package cmd

import (
	"bytes"
	"fmt"
	"strings"

	xterm "github.com/gitpod-io/xterm-go"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// screenEmulator replays the output of one terminal through the emulator that
// `terminal read` and `terminal wait` use, tracking geometry changes and
// stream offsets.
//
// Only one goroutine uses a screenEmulator at a time, so it holds no lock.
type screenEmulator struct {
	id         string
	cols, rows int
	position   terminalScreenPosition

	term     *xterm.Terminal
	boundary *screenBoundary
}

// newScreenEmulator creates an emulator initialized from the retained bytes
// and timeline in info.
func newScreenEmulator(info *leapmuxv1.TerminalInfo) (*screenEmulator, error) {
	e := &screenEmulator{id: info.GetTerminalId()}
	if err := e.init(info); err != nil {
		return nil, err
	}
	return e, nil
}

// init populates the emulator from info's screen timeline or raw screen bytes.
func (e *screenEmulator) init(info *leapmuxv1.TerminalInfo) error {
	if timeline := info.GetScreenTimeline(); timeline != nil {
		body, ok := bytes.CutPrefix(info.GetScreen(), timeline.GetModePrefix())
		if !ok {
			return cli.Newf(cli.CodeInternal, "the worker returned an invalid screen mode prefix for terminal %s", e.id)
		}
		start, end := timeline.GetStartOffset(), info.GetScreenEndOffset()
		if start < 0 || end < start || end-start > int64(len(body)) {
			return cli.Newf(cli.CodeInternal, "the worker returned an invalid screen byte range for terminal %s", e.id)
		}
		if end-start != int64(len(body)) {
			return e.screenHistoryUnavailable()
		}
		if err := e.replayScreenTimeline(&leapmuxv1.TerminalData{
			Data: body, EndOffset: info.GetScreenEndOffset(), IsSnapshot: true, ScreenTimeline: timeline,
		}, nil); err != nil {
			e.dispose()
			return err
		}
		return nil
	}
	cols, rows, err := renderDimensions(info.GetTerminalId(), info.GetCols(), info.GetRows())
	if err != nil {
		return err
	}
	e.cols, e.rows = cols, rows
	if err := e.reset(info.GetScreen()); err != nil {
		e.dispose()
		return err
	}
	e.position.offset = info.GetScreenEndOffset()
	return nil
}

// reset replaces the state of the emulator with screen: the bytes that the
// Worker retains. When a boundary is attached, the search starts at the
// visible screen or at the oldest retained row with scrollback enabled.
func (e *screenEmulator) reset(screen []byte) error {
	e.dispose()
	e.term = xterm.New(xterm.WithCols(e.cols), xterm.WithRows(e.rows), xterm.WithScrollback(contracts.TerminalScrollbackLines))
	if e.boundary != nil {
		e.boundary.observeScreenErases(e.term)
	}
	if err := e.emulate(screen); err != nil {
		return err
	}
	if e.boundary != nil {
		e.boundary.followInitialViewport(e.term)
	}
	return nil
}

// write feeds one chunk of output to the emulator.
func (e *screenEmulator) write(data []byte) error {
	if err := e.emulate(data); err != nil {
		return err
	}
	if e.boundary != nil {
		e.boundary.afterWrite(e.term)
	}
	return nil
}

// recoverTerminalEmulator converts a panic from xterm-go into an internal CLI
// error. Call it directly with defer so recover observes the active panic.
func recoverTerminalEmulator(op string, errp *error) {
	if r := recover(); r != nil {
		*errp = cli.Newf(cli.CodeInternal, "the terminal emulator failed to %s: %v", op, r)
	}
}

// emulate writes data to the emulator. The bytes come from whatever the
// program printed, and the emulator is a recent port of xterm.js, so a panic
// on a sequence that it mishandles fails the wait instead of the process.
func (e *screenEmulator) emulate(data []byte) (err error) {
	defer recoverTerminalEmulator(fmt.Sprintf("replay the output of terminal %s", e.id), &err)
	if _, writeErr := e.term.Write(data); writeErr != nil {
		return cli.Wrap(cli.CodeInternal, writeErr, fmt.Sprintf("replay the output of terminal %s", e.id))
	}
	return nil
}

// visibleLines returns the visible screen rows with wrapped rows joined into
// logical lines, trailing blanks stripped from each line, and trailing empty
// lines omitted.
func (e *screenEmulator) visibleLines() (lines []string, err error) {
	defer recoverTerminalEmulator(fmt.Sprintf("read the screen of terminal %s", e.id), &err)
	buf := e.term.Buffer()
	start := buf.YBase
	end := min(start+e.term.Rows(), buf.Lines.Length())
	var out []string
	for y := start; y < end; {
		line, next := logicalLine(buf, y, 0)
		out = append(out, line)
		y = next
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out, nil
}

// diffVisibleScreen compares the visible screen against initialLines row by
// row. When no line differs, it returns changed=false without allocating a
// joined screen string. When any line differs, it builds and returns the
// updated joined screen text.
func (e *screenEmulator) diffVisibleScreen(initialLines []string) (screen string, changed bool, err error) {
	defer recoverTerminalEmulator(fmt.Sprintf("read the screen of terminal %s", e.id), &err)
	buf := e.term.Buffer()
	start := buf.YBase
	end := min(start+e.term.Rows(), buf.Lines.Length())
	var lines []string
	differs := false
	i := 0
	for y := start; y < end; {
		line, next := logicalLine(buf, y, 0)
		if differs {
			lines = append(lines, line)
		} else if i < len(initialLines) {
			if line != initialLines[i] {
				differs = true
				lines = make([]string, 0, max(len(initialLines), i+1))
				lines = append(lines, initialLines[:i]...)
				lines = append(lines, line)
			}
		} else if line != "" {
			differs = true
			lines = make([]string, 0, i+1)
			lines = append(lines, initialLines...)
			for len(lines) < i {
				lines = append(lines, "")
			}
			lines = append(lines, line)
		}
		i++
		y = next
	}
	if !differs {
		if i >= len(initialLines) {
			return "", false, nil
		}
		// Fewer visible logical lines remain than initialLines, whose final
		// line is non-empty, so visible text disappeared.
		lines = append([]string(nil), initialLines[:i]...)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n"), true, nil
}

// dispose frees the emulator and any boundary listener. The emulator is
// unusable after it until reset is called.
func (e *screenEmulator) dispose() {
	if e.boundary != nil {
		e.boundary.release()
	}
	if e.term != nil {
		e.term.Dispose()
		e.term = nil
	}
}

// screenBoundary tracks which rows of the normal buffer belong to a text watch
// across scrollback trims, screen erases, full resets, and resizes. The
// emulator calls the trim listener on the caller's goroutine inside Write.
type screenBoundary struct {
	scrollback bool
	// normal is the normal buffer that row and settled count rows of. A
	// full reset of the terminal (RIS) replaces it.
	normal *xterm.Buffer
	trim   xterm.Disposable
	// row is the first row of the normal buffer that counts: the top of
	// the screen when the watch started. A row above it is scrollback from
	// before the watch.
	row int
	// column keeps the first eligible cell when reflow joins rows.
	column int
	// settled is the first row of the normal buffer that the next check reads.
	// A row above the screen no longer changes, so a check reads it once.
	settled int
}

// An erase that covers the excluded prefix removes the old cells. New text
// can then start at the first column without exposing old scrollback.
func (b *screenBoundary) observeScreenErases(term *xterm.Terminal) {
	for _, command := range []byte{'J', 'K'} {
		term.RegisterCsiHandler(xterm.FunctionIdentifier{Final: command}, func(params *xterm.Params) bool {
			if b.column == 0 || term.Buffer() != b.normal {
				return false
			}
			start := b.normal.YBase
			cursor := start + b.normal.Y
			if b.row < start || b.row >= start+term.Rows() || command == 'K' && cursor != b.row {
				return false
			}
			var mode int32
			if params.Length != 0 {
				mode = params.Params[0]
			}
			if mode == 2 || mode == 0 && (cursor < b.row || cursor == b.row && b.normal.X == 0) ||
				mode == 1 && (cursor > b.row || cursor == b.row && b.normal.X+1 >= b.column) {
				b.column = 0
				b.settled = min(b.settled, b.row)
			}
			return false
		})
	}
}

func (b *screenBoundary) followInitialViewport(term *xterm.Terminal) {
	buffer := term.NormalBuffer()
	first := buffer.YBase
	if b.scrollback {
		first = 0
	}
	b.follow(buffer, first)
}

// follow makes normal the buffer that row and settled count rows of,
// with both at first. A trim of the scrollback moves every row up, so it
// moves both marks up as far.
func (b *screenBoundary) follow(normal *xterm.Buffer, first int) {
	if b.trim != nil {
		b.trim.Dispose()
	}
	b.normal, b.row, b.settled = normal, first, first
	b.column = 0
	b.trim = normal.Lines.OnTrimEmitter.Event(func(amount int) {
		if amount > b.row {
			b.column = 0
		}
		b.row = max(b.row-amount, 0)
		b.settled = max(b.settled-amount, 0)
	})
}

// afterWrite re-anchors the boundary when a full reset (RIS) replaces the
// normal buffer.
func (b *screenBoundary) afterWrite(term *xterm.Terminal) {
	if normal := term.NormalBuffer(); normal != b.normal {
		// A full reset replaced the buffers, so every row of the new normal
		// buffer is output from after the watch started.
		b.follow(normal, 0)
	}
}

// release detaches the trim listener and drops the buffer reference.
func (b *screenBoundary) release() {
	if b.trim != nil {
		b.trim.Dispose()
		b.trim = nil
	}
	b.normal = nil
}

// screenWatch replays the output of one terminal through screenEmulator and
// finds the lines that match a wait for text.
//
// The lines that count are the lines of the screen when the watch starts,
// and every line that appears after them. A line that scrolls off the screen
// within one chunk of output still counts, because a check reads every row
// that scrolled into the scrollback since the last check. With --scrollback,
// the retained history in the initial or reconnected snapshot counts too.
//
// The Worker states the size that applies at each offset. Text that a program
// overwrites within one chunk is gone when the next check runs.
type screenWatch struct {
	screenEmulator
	match func(line string) bool
}

// newScreenWatch starts a watch over the screen that info reports, at the
// size that info reports. match reports whether one line meets the wait.
func newScreenWatch(info *leapmuxv1.TerminalInfo, match func(line string) bool, scrollback bool) (*screenWatch, error) {
	if match == nil {
		return nil, cli.Newf(cli.CodeInternal, "screen watch requires a line matcher for terminal %s", info.GetTerminalId())
	}
	w := &screenWatch{
		screenEmulator: screenEmulator{
			id:       info.GetTerminalId(),
			boundary: &screenBoundary{scrollback: scrollback},
		},
		match: match,
	}
	if err := w.init(info); err != nil {
		return nil, err
	}
	return w, nil
}

// check reads the lines that changed or appeared since the last check from
// bottom to top, and returns the lowest one that matches. On the alternate
// screen, which has no scrollback, it reads the screen.
func (w *screenWatch) check() (line string, ok bool, err error) {
	defer recoverTerminalEmulator(fmt.Sprintf("read the screen of terminal %s", w.id), &err)
	buf := w.term.Buffer()
	end := min(buf.YBase+w.term.Rows(), buf.Lines.Length())
	from := buf.YBase
	alt := w.term.IsAltBufferActive()
	if !alt {
		from = max(w.boundary.row, w.boundary.settled)
		// A line that the terminal wrapped is read from its first row, but
		// never from a row above the boundary.
		for from > w.boundary.row && from < buf.Lines.Length() {
			row := buf.Lines.Get(from)
			if row == nil || !row.IsWrapped {
				break
			}
			from--
		}
	}
	var matched string
	var found bool
	for y := end - 1; y >= from; {
		start := y
		for start > from {
			row := buf.Lines.Get(start)
			if row == nil || !row.IsWrapped {
				break
			}
			start--
		}
		column := 0
		if !alt && start == w.boundary.row {
			column = w.boundary.column
		}
		text, _ := logicalLine(buf, start, column)
		if w.match(text) {
			matched, found = text, true
			break
		}
		y = start - 1
	}
	if !alt {
		w.boundary.settled = buf.YBase
	}
	return matched, found, nil
}

// logicalLine reads the line that starts at row y of buf: row y, joined with
// each row after it that continues it because the terminal wrapped the line
// at its width. It returns the text without its trailing blanks, as
// `terminal read` prints a row, and the row after the line.
func logicalLine(buf *xterm.Buffer, y, firstColumn int) (string, int) {
	var sb strings.Builder
	for {
		row := buf.Lines.Get(y)
		next := y + 1
		if row == nil {
			return strings.TrimRight(sb.String(), " "), next
		}
		if next < buf.Lines.Length() {
			if continued := buf.Lines.Get(next); continued != nil && continued.IsWrapped {
				sb.WriteString(row.TranslateToString(false, firstColumn, wrappedRowLength(row, continued)))
				firstColumn = 0
				y = next
				continue
			}
		}
		sb.WriteString(row.TranslateToString(true, firstColumn, -1))
		return strings.TrimRight(sb.String(), " "), next
	}
}

// wrappedRowLength is the number of cells of row that its line holds, when
// next continues the line. A wide character that does not fit in the last
// cell moves to the next row and leaves that cell empty; the empty cell is not
// part of the text. xterm.js reads a wrapped line the same way.
func wrappedRowLength(row, next *xterm.BufferLine) int {
	last := row.Len - 1
	if last >= 0 && row.HasContent(last) == 0 && row.GetWidth(last) == 1 && next.GetWidth(0) == 2 {
		return last
	}
	return row.Len
}
