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

// screenWatch replays the output of one terminal through the emulator that
// `terminal read` uses, and finds the lines that match a wait for text.
//
// The lines that count are the lines of the screen when the watch starts,
// and every line that appears after them. A line that scrolls off the screen
// within one chunk of output still counts, because a check reads every row
// that scrolled into the scrollback since the last check. With --scrollback,
// the retained history in the initial or reconnected snapshot counts too.
//
// The Worker states the size that applies at each offset. Text that a program
// overwrites within one chunk is gone when the next check runs.
//
// Only the goroutine of the wait uses a screenWatch, so it holds no lock. The
// emulator calls the trim listener on that goroutine, inside Write.
type screenWatch struct {
	id         string
	cols, rows int
	match      func(line string) bool
	scrollback bool
	position   terminalScreenPosition

	term *xterm.Terminal
	// normal is the normal buffer that boundary and settled count rows of. A
	// full reset of the terminal (RIS) replaces it.
	normal *xterm.Buffer
	trim   xterm.Disposable
	// boundary is the first row of the normal buffer that counts: the top of
	// the screen when the watch started. A row above it is scrollback from
	// before the watch.
	boundary int
	// boundaryColumn keeps the first eligible cell when reflow joins rows.
	boundaryColumn int
	// settled is the first row of the normal buffer that the next check reads.
	// A row above the screen no longer changes, so a check reads it once.
	settled int
}

// newScreenWatch starts a watch over the screen that info reports, at the
// size that info reports. match reports whether one line meets the wait.
func newScreenWatch(info *leapmuxv1.TerminalInfo, match func(line string) bool, scrollback bool) (*screenWatch, error) {
	w := &screenWatch{id: info.GetTerminalId(), match: match, scrollback: scrollback}
	if timeline := info.GetScreenTimeline(); timeline != nil {
		body, ok := bytes.CutPrefix(info.GetScreen(), timeline.GetModePrefix())
		if !ok {
			return nil, cli.Newf(cli.CodeInternal, "the worker returned an invalid screen mode prefix for terminal %s", w.id)
		}
		start, end := timeline.GetStartOffset(), info.GetScreenEndOffset()
		if start < 0 || end < start || end-start > int64(len(body)) {
			return nil, cli.Newf(cli.CodeInternal, "the worker returned an invalid screen byte range for terminal %s", w.id)
		}
		if end-start != int64(len(body)) {
			return nil, w.screenHistoryUnavailable()
		}
		if err := w.replayScreenTimeline(&leapmuxv1.TerminalData{
			Data: body, EndOffset: info.GetScreenEndOffset(), IsSnapshot: true, ScreenTimeline: timeline,
		}, nil); err != nil {
			w.dispose()
			return nil, err
		}
		return w, nil
	}
	cols, rows, err := renderSize(info)
	if err != nil {
		return nil, err
	}
	w.cols, w.rows = cols, rows
	if err := w.reset(info.GetScreen()); err != nil {
		w.dispose()
		return nil, err
	}
	w.position.offset = info.GetScreenEndOffset()
	return w, nil
}

// reset replaces the state of the emulator with screen: the bytes that the
// Worker retains. The search starts at the visible screen or at the oldest
// retained row when the caller requests scrollback.
func (w *screenWatch) reset(screen []byte) error {
	w.release()
	w.term = xterm.New(xterm.WithCols(w.cols), xterm.WithRows(w.rows), xterm.WithScrollback(contracts.TerminalScrollbackLines))
	w.observeScreenErases()
	if err := w.emulate(screen); err != nil {
		return err
	}
	w.followInitialViewport()
	return nil
}

// An erase that covers the excluded prefix removes the old cells. New text
// can then start at the first column without exposing old scrollback.
func (w *screenWatch) observeScreenErases() {
	for _, command := range []byte{'J', 'K'} {
		w.term.RegisterCsiHandler(xterm.FunctionIdentifier{Final: command}, func(params *xterm.Params) bool {
			if w.boundaryColumn == 0 || w.term.Buffer() != w.normal {
				return false
			}
			start := w.normal.YBase
			cursor := start + w.normal.Y
			if w.boundary < start || w.boundary >= start+w.rows || command == 'K' && cursor != w.boundary {
				return false
			}
			var mode int32
			if params.Length != 0 {
				mode = params.Params[0]
			}
			if mode == 2 || mode == 0 && (cursor < w.boundary || cursor == w.boundary && w.normal.X == 0) ||
				mode == 1 && (cursor > w.boundary || cursor == w.boundary && w.normal.X+1 >= w.boundaryColumn) {
				w.boundaryColumn = 0
				w.settled = min(w.settled, w.boundary)
			}
			return false
		})
	}
}

func (w *screenWatch) followInitialViewport() {
	buffer := w.term.NormalBuffer()
	first := buffer.YBase
	if w.scrollback {
		first = 0
	}
	w.follow(buffer, first)
}

// write feeds one chunk of output to the emulator.
func (w *screenWatch) write(data []byte) error {
	if err := w.emulate(data); err != nil {
		return err
	}
	if normal := w.term.NormalBuffer(); normal != w.normal {
		// A full reset replaced the buffers, so every row of the new normal
		// buffer is output from after the watch started.
		w.follow(normal, 0)
	}
	return nil
}

// emulate writes data to the emulator. The bytes come from whatever the
// program printed, and the emulator is a recent port of xterm.js, so a panic
// on a sequence that it mishandles fails the wait instead of the process.
func (w *screenWatch) emulate(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = cli.Newf(cli.CodeInternal, "the terminal emulator failed on the output of terminal %s: %v", w.id, r)
		}
	}()
	if _, writeErr := w.term.Write(data); writeErr != nil {
		return cli.Wrap(cli.CodeInternal, writeErr, fmt.Sprintf("replay the output of terminal %s", w.id))
	}
	return nil
}

// follow makes normal the buffer that boundary and settled count rows of,
// with both at first. A trim of the scrollback moves every row up, so it
// moves both marks up as far.
func (w *screenWatch) follow(normal *xterm.Buffer, first int) {
	if w.trim != nil {
		w.trim.Dispose()
	}
	w.normal, w.boundary, w.settled = normal, first, first
	w.boundaryColumn = 0
	w.trim = normal.Lines.OnTrimEmitter.Event(func(amount int) {
		if amount > w.boundary {
			w.boundaryColumn = 0
		}
		w.boundary = max(w.boundary-amount, 0)
		w.settled = max(w.settled-amount, 0)
	})
}

// check reads the lines that changed or appeared since the last check, and
// returns the last one that matches. On the alternate screen, which has no
// scrollback, it reads the screen.
func (w *screenWatch) check() (line string, ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			line, ok = "", false
			err = cli.Newf(cli.CodeInternal, "the terminal emulator failed on the screen of terminal %s: %v", w.id, r)
		}
	}()
	buf := w.term.Buffer()
	end := min(buf.YBase+w.term.Rows(), buf.Lines.Length())
	from := buf.YBase
	alt := w.term.IsAltBufferActive()
	if !alt {
		from = max(w.boundary, w.settled)
		// A line that the terminal wrapped is read from its first row, but
		// never from a row above the boundary.
		for from > w.boundary && from < buf.Lines.Length() && buf.Lines.Get(from).IsWrapped {
			from--
		}
	}
	for y := from; y < end; {
		column := 0
		if !alt && y == w.boundary {
			column = w.boundaryColumn
		}
		text, next := logicalLine(buf, y, column)
		if w.match(text) {
			line, ok = text, true
		}
		y = next
	}
	if !alt {
		w.settled = buf.YBase
	}
	return line, ok, nil
}

// snapshotVisibleText returns the normalized visible screen text (matching
// `terminal read`'s line trimming without scrollback). Used by
// `terminal wait --change` to detect real visible screen updates while ignoring
// no-op control sequences.
func (w *screenWatch) snapshotVisibleText() (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = cli.Newf(cli.CodeInternal, "the terminal emulator failed on the screen of terminal %s: %v", w.id, r)
		}
	}()
	buf := w.term.Buffer()
	start := buf.YBase
	end := min(start+w.term.Rows(), buf.Lines.Length())
	var lines []string
	for y := start; y < end; {
		line, next := logicalLine(buf, y, 0)
		lines = append(lines, line)
		y = next
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n"), nil
}

// release frees the emulator.
func (w *screenWatch) release() {
	if w.trim != nil {
		w.trim.Dispose()
		w.trim = nil
	}
	if w.term != nil {
		w.term.Dispose()
		w.term = nil
	}
	w.normal = nil
}

// dispose frees the watch. The watch is unusable after it.
func (w *screenWatch) dispose() { w.release() }

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
