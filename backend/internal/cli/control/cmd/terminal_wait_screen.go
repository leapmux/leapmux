package cmd

import (
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
// that scrolled into the scrollback since the last check. The scrollback from
// before the watch does not count.
//
// The emulator keeps the size that the terminal had when the watch started.
// The Worker reports no resize on the watch, so output that a program draws for
// a new size wraps at the old width here. A text that a program overwrites in
// place within one chunk of output is never on the screen when a check runs.
//
// Only the goroutine of the wait uses a screenWatch, so it holds no lock. The
// emulator calls the trim listener on that goroutine, inside Write.
type screenWatch struct {
	id         string
	cols, rows int
	match      func(line string) bool

	term *xterm.Terminal
	// normal is the normal buffer that boundary and settled count rows of. A
	// full reset of the terminal (RIS) replaces it.
	normal *xterm.Buffer
	trim   xterm.Disposable
	// boundary is the first row of the normal buffer that counts: the top of
	// the screen when the watch started. A row above it is scrollback from
	// before the watch.
	boundary int
	// settled is the first row of the normal buffer that the next check reads.
	// A row above the screen no longer changes, so a check reads it once.
	settled int
}

// newScreenWatch starts a watch over the screen that info reports, at the
// size that info reports. match reports whether one line meets the wait.
func newScreenWatch(info *leapmuxv1.TerminalInfo, match func(line string) bool) (*screenWatch, error) {
	cols, rows, err := renderSize(info)
	if err != nil {
		return nil, err
	}
	w := &screenWatch{id: info.GetTerminalId(), cols: cols, rows: rows, match: match}
	if err := w.reset(info.GetScreen()); err != nil {
		w.dispose()
		return nil, err
	}
	return w, nil
}

// reset replaces the state of the emulator with screen: the bytes that the
// Worker retains, which replay the whole state. The screen that they show
// counts, and the scrollback above it does not.
func (w *screenWatch) reset(screen []byte) error {
	w.release()
	w.term = xterm.New(xterm.WithCols(w.cols), xterm.WithRows(w.rows), xterm.WithScrollback(contracts.TerminalScrollbackLines))
	if err := w.emulate(screen); err != nil {
		return err
	}
	w.follow(w.term.NormalBuffer(), w.term.NormalBuffer().YBase)
	return nil
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
	w.trim = normal.Lines.OnTrimEmitter.Event(func(amount int) {
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
		text, next := logicalLine(buf, y)
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
func logicalLine(buf *xterm.Buffer, y int) (string, int) {
	var sb strings.Builder
	for {
		row := buf.Lines.Get(y)
		next := y + 1
		if row == nil {
			return strings.TrimRight(sb.String(), " "), next
		}
		if next < buf.Lines.Length() {
			if continued := buf.Lines.Get(next); continued != nil && continued.IsWrapped {
				sb.WriteString(row.TranslateToString(false, 0, wrappedRowLength(row, continued)))
				y = next
				continue
			}
		}
		sb.WriteString(row.TranslateToString(true, 0, -1))
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
