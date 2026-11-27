package cmd

import (
	"fmt"
	"io"
	"slices"
	"strings"

	xterm "github.com/gitpod-io/xterm-go"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// terminalReadLong is the help of `terminal read`. The scrollback limit
// comes from the contract that the browser's terminal reads too.
var terminalReadLong = fmt.Sprintf(`Print what a terminal shows, as plain text. The CLI replays the bytes that
the Worker retains through a terminal emulator and applies recorded size
changes in order. The emulator is a Go port of xterm.js, which the browser
uses. Colors and other attributes do not print.

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

// addTerminalReadFlags registers the flags shared by `terminal read` and
// `terminal quake read`.
func addTerminalReadFlags(fs *pflag.FlagSet) {
	fs.Int("lines", 0, "print only the last N lines")
	fs.Bool("scrollback", false, "also print the lines above the screen")
	fs.Bool("raw", false, "print the retained bytes as they are, with their escape sequences")
	fs.BoolP("follow", "f", false, "stream live output until Ctrl-C or shell exit")
}

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

// executeTerminalRead runs `terminal read` or `terminal quake read` using
// resolveTarget to locate the terminal and fetch its initial record.
func executeTerminalRead(inv *cli.Invocation, resolveTarget func(*session) (tabTarget, *leapmuxv1.TerminalInfo, error)) (any, error) {
	opts, err := screenReadOptionsOf(inv)
	if err != nil {
		return nil, err
	}
	if boolFlag(inv, "follow") {
		s, err := openStreamSession(inv)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		callSession := s.bounded()
		got, info, err := resolveTarget(callSession)
		callSession.Close()
		if err != nil {
			return nil, err
		}
		return nil, followTerminalOutput(inv, s, got, info, opts)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_, info, err := resolveTarget(s)
	if err != nil {
		return nil, err
	}
	if opts.raw {
		return rawScreenOf(info), nil
	}
	return renderScreen(info, opts)
}

func runTerminalRead(inv *cli.Invocation) (any, error) {
	return executeTerminalRead(inv, func(s *session) (tabTarget, *leapmuxv1.TerminalInfo, error) {
		got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
		if err != nil {
			return tabTarget{}, nil, err
		}
		info, err := s.terminalInfo(got)
		if err != nil {
			return tabTarget{}, nil, err
		}
		return got, info, nil
	})
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
		if _, err := io.WriteString(w, cli.SanitizeTerminalText(line)+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// rawScreenHint points a read that cannot render the screen at the bytes that
// the Worker retains.
const rawScreenHint = "pass --raw to print the bytes that it retains"

// renderDimensions validates the emulator dimensions of terminal id. It
// refuses a terminal that reports no size, and a terminal too large to render:
// wider or taller than maxTerminalDimension, or with more than
// maxRenderedCells cells, the scrollback included.
//
// `terminal read` and `terminal wait` both replay a screen, so both check its
// size here.
func renderDimensions(id string, c, r uint32) (cols, rows int, err error) {
	switch {
	case c == 0 || r == 0:
		return 0, 0, cli.Newf(cli.CodeInternal, "the worker reports no size for terminal %s, so its screen has no layout", id)
	// The product is computed only when both sides are within
	// maxTerminalDimension, so it cannot overflow.
	case c > maxTerminalDimension || r > maxTerminalDimension ||
		(int(r)+contracts.TerminalScrollbackLines)*int(c) > maxRenderedCells:
		return 0, 0, cli.Newf(cli.CodeInternal, "terminal %s is %d columns by %d rows, too large to render", id, c, r)
	default:
		return int(c), int(r), nil
	}
}

// renderScreen replays the retained bytes of info through the emulator at the
// size that info reports, and reads the text of its rows.
func renderScreen(info *leapmuxv1.TerminalInfo, opts screenReadOptions) (screen renderedScreen, err error) {
	id := info.GetTerminalId()
	// Registered before recoverTerminalEmulator so LIFO runs this second and
	// attaches rawScreenHint to both recovered panics and normal errors.
	defer func() {
		if err != nil {
			screen = renderedScreen{}
			if ce := cli.FromError(err); !slices.Contains(ce.Hints, rawScreenHint) {
				err = ce.WithHint("%s", rawScreenHint)
			}
		}
	}()
	// The bytes come from whatever the program printed, and the emulator is a
	// young port. A panic on a sequence that it mishandles fails this one
	// command with the way out, rather than crash with a stack trace.
	defer recoverTerminalEmulator("render the terminal screen", &err)
	em, err := newScreenEmulator(info)
	if err != nil {
		return renderedScreen{}, err
	}
	defer em.dispose()
	term := em.term
	return renderedScreen{
		ID:              id,
		Cols:            uint32(em.cols),
		Rows:            uint32(em.rows),
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
	if end <= first {
		return []string{}
	}
	if opts.lines > 0 {
		// Scan backward so a small --lines request over a large scrollback
		// translates only the trailing rows it prints.
		lines := make([]string, 0, min(opts.lines, end-first))
		for y := end - 1; y >= first; y-- {
			line := strings.TrimRight(buf.TranslateBufferLineToString(y, true, 0, -1), " ")
			if len(lines) == 0 && line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) == opts.lines {
				break
			}
		}
		slices.Reverse(lines)
		return lines
	}
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
	return lines
}
