package cmd

import (
	"fmt"
	"io"
	"strings"

	xterm "github.com/gitpod-io/xterm-go"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// terminalReadLong is the help of `terminal read`. The scrollback limit
// comes from the contract that the browser's terminal reads too.
var terminalReadLong = fmt.Sprintf(`Print what a terminal shows, as plain text. The CLI replays the bytes that
the Worker retains through a terminal emulator at the terminal's size. The
emulator is a Go port of xterm.js, which the browser uses, so the text is the
text that the browser shows. Colors and other attributes do not print.

Each row loses its trailing blanks, and the empty rows at the end of the
screen do not print, so a short output does not end in empty lines.

--scrollback also prints the lines above the screen: at most %d, as in the
browser, and only those that the bytes the Worker retains still hold. A
full-screen program on the alternate screen has no scrollback, as in the
browser, so --scrollback adds nothing there. --lines N keeps the last N lines
of what the command prints otherwise.

--json adds the cursor, whose row and column count from 0 at the top left of
the screen, and alternate_screen, which is true while a full-screen program
uses the alternate screen. --raw prints the retained bytes as they are, with
their escape sequences, so that "less -R" shows them.`, contracts.TerminalScrollbackLines)

// maxRenderedCells caps the cells that one render allocates, the scrollback
// included. A screen of 2000 columns by 1000 rows fits. The Worker accepts a
// size up to 65535 by 65535, and a render of that would take gigabytes, so a
// screen past the cap fails and points at --raw instead.
const maxRenderedCells = 4 << 20

// screenReadOptions are the flags of `terminal read`.
type screenReadOptions struct {
	// raw prints the retained bytes as they are.
	raw bool
	// scrollback adds the lines above the screen.
	scrollback bool
	// lines keeps the last lines of the result. Zero keeps every line.
	lines int
}

// screenReadOptionsOf reads the flags of `terminal read`, and refuses a
// combination before the command sends anything.
func screenReadOptionsOf(inv *cli.Invocation) (screenReadOptions, error) {
	lines, err := inv.Command.Flags().GetInt("lines")
	if err != nil {
		return screenReadOptions{}, err
	}
	opts := screenReadOptions{raw: boolFlag(inv, "raw"), scrollback: boolFlag(inv, "scrollback")}
	if inv.Changed("lines") {
		if lines < 1 {
			return screenReadOptions{}, cli.Usagef("--lines must be 1 or more; got %d", lines)
		}
		opts.lines = lines
	}
	if opts.raw && (opts.scrollback || opts.lines > 0) {
		return screenReadOptions{}, cli.Usagef("--raw prints the retained bytes as they are, so it takes no --lines and no --scrollback")
	}
	return opts, nil
}

func runTerminalRead(inv *cli.Invocation) (any, error) {
	opts, err := screenReadOptionsOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	info, err := s.terminalInfo(got)
	if err != nil {
		return nil, err
	}
	if opts.raw {
		return rawScreenOf(info), nil
	}
	return renderScreen(info, opts)
}

// rawScreen is a terminal's retained bytes. Its text form writes the bytes
// as they are, so the escape sequences render in a terminal. JSON carries
// them as a string.
type rawScreen struct {
	ID              string `json:"id"`
	Cols            uint32 `json:"cols"`
	Rows            uint32 `json:"rows"`
	ScreenEndOffset int64  `json:"screen_end_offset"`
	Screen          string `json:"screen"`
}

func rawScreenOf(info *leapmuxv1.TerminalInfo) rawScreen {
	return rawScreen{
		ID:              info.GetTerminalId(),
		Cols:            info.GetCols(),
		Rows:            info.GetRows(),
		ScreenEndOffset: info.GetScreenEndOffset(),
		Screen:          string(info.GetScreen()),
	}
}

func (r rawScreen) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, r.Screen)
	return err
}

// renderedScreen is what a terminal shows, as text. Its text form prints the
// lines alone.
type renderedScreen struct {
	ID              string `json:"id"`
	Cols            uint32 `json:"cols"`
	Rows            uint32 `json:"rows"`
	ScreenEndOffset int64  `json:"screen_end_offset"`
	// AlternateScreen is true while a full-screen program uses the
	// alternate screen, which has no scrollback.
	AlternateScreen bool         `json:"alternate_screen"`
	Cursor          screenCursor `json:"cursor"`
	// Lines are the rows that the command prints, oldest first.
	Lines []string `json:"lines"`
}

// screenCursor is the position of the cursor on the screen. Row and Col count
// from 0 at the top left of the screen; a scrollback line does not count.
type screenCursor struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

func (r renderedScreen) RenderText(w io.Writer) error {
	for _, line := range r.Lines {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// renderScreen replays the retained bytes of info through the emulator at the
// size that info reports, and reads the text of its rows.
func renderScreen(info *leapmuxv1.TerminalInfo, opts screenReadOptions) (screen renderedScreen, err error) {
	id := info.GetTerminalId()
	cols, rows := info.GetCols(), info.GetRows()
	if cols == 0 || rows == 0 {
		return renderedScreen{}, cli.Newf(cli.CodeInternal, "the worker reports no size for terminal %s, so its screen has no layout", id).
			WithHint("pass --raw to print the bytes that it retains")
	}
	if cols > maxTerminalDimension || rows > maxTerminalDimension ||
		(int(rows)+contracts.TerminalScrollbackLines)*int(cols) > maxRenderedCells {
		return renderedScreen{}, cli.Newf(cli.CodeInternal, "terminal %s is %d columns by %d rows, too large to render", id, cols, rows).
			WithHint("pass --raw to print the bytes that it retains")
	}
	// The bytes come from whatever the program printed, and the emulator is a
	// young port. A panic on a sequence that it mishandles fails this one
	// command with the way out, rather than crash with a stack trace.
	defer func() {
		if r := recover(); r != nil {
			screen = renderedScreen{}
			err = cli.Newf(cli.CodeInternal, "the terminal emulator failed on the screen of terminal %s: %v", id, r).
				WithHint("pass --raw to print the bytes that it retains")
		}
	}()
	term := xterm.New(xterm.WithCols(int(cols)), xterm.WithRows(int(rows)), xterm.WithScrollback(contracts.TerminalScrollbackLines))
	defer term.Dispose()
	if _, writeErr := term.Write(info.GetScreen()); writeErr != nil {
		return renderedScreen{}, cli.Wrap(cli.CodeInternal, writeErr, fmt.Sprintf("replay the screen of terminal %s", id))
	}
	return renderedScreen{
		ID:              id,
		Cols:            cols,
		Rows:            rows,
		ScreenEndOffset: info.GetScreenEndOffset(),
		AlternateScreen: term.IsAltBufferActive(),
		// The cursor stands past the last column after the program wrote
		// the last cell of a row, until the next character wraps. The
		// browser draws it on the last column.
		Cursor: screenCursor{Row: term.CursorY(), Col: min(term.CursorX(), term.Cols()-1)},
		Lines:  screenLines(term, opts),
	}, nil
}

// screenLines reads the rows of the active buffer of term: the screen, after
// the scrollback with opts.scrollback. It trims the trailing blanks of each
// row, drops the empty rows at the end, and then keeps the last opts.lines.
//
// The alternate buffer holds no line above the screen, so the scrollback of
// a full-screen program is empty, as in xterm.js.
func screenLines(term *xterm.Terminal, opts screenReadOptions) []string {
	buf := term.Buffer()
	first := buf.YBase
	if opts.scrollback {
		first = 0
	}
	end := buf.YBase + term.Rows()
	lines := make([]string, 0, end-first)
	for y := first; y < end; y++ {
		// TranslateBufferLineToString trims the cells that hold nothing, and
		// TrimRight the spaces that the program wrote, so both kinds of
		// blank cell end a row alike.
		lines = append(lines, strings.TrimRight(buf.TranslateBufferLineToString(y, true, 0, -1), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if opts.lines > 0 && len(lines) > opts.lines {
		lines = lines[len(lines)-opts.lines:]
	}
	return lines
}
