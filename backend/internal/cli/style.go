package cli

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// ColorMode controls whether ANSI color escapes are emitted in text output.
type ColorMode int

const (
	// ColorAuto emits ANSI color only on interactive TTYs (and honors NO_COLOR / TERM=dumb / CLICOLOR_FORCE).
	ColorAuto ColorMode = iota
	// ColorAlways emits ANSI color regardless of TTY detection.
	ColorAlways
	// ColorNever suppresses all ANSI color escapes.
	ColorNever
)

// ParseColorMode parses --color auto|always|never.
func ParseColorMode(raw string) (ColorMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	default:
		return ColorAuto, Usagef("--color must be one of auto, always, never; got %q", raw)
	}
}

// IsTerminalFD reports whether fd refers to an interactive terminal,
// including Cygwin and MSYS2 pseudo-terminals on Windows.
func IsTerminalFD(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// IsTerminalFile reports whether f is a non-nil file attached to an
// interactive terminal.
func IsTerminalFile(f *os.File) bool {
	return f != nil && IsTerminalFD(f.Fd())
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && IsTerminalFile(f)
}

// ColorEnabled reports whether ANSI styling should be emitted to w under mode.
// NO_COLOR and TERM=dumb disable color under auto; CLICOLOR_FORCE enables it
// under auto otherwise.
func ColorEnabled(w io.Writer, mode ColorMode) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
			return false
		}
		if f := os.Getenv("CLICOLOR_FORCE"); f != "" && f != "0" {
			return true
		}
		return isTerminalWriter(w)
	}
}

// TerminalWidth returns the column width of w if w is a TTY, or 0 otherwise.
func TerminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

// StateTone selects the ANSI style applied to a cell in a `,state` table column.
type StateTone int

const (
	StateToneNone StateTone = iota
	StateToneGreen
	StateToneYellow
	StateToneRed
	StateToneMagenta
	StateToneCyan
	StateToneDim
)

// StateClassifier maps a `,state` table cell value to a StateTone.
type StateClassifier func(value string) StateTone

var (
	defaultStateClassifierMu sync.RWMutex
	defaultStateClassifier   StateClassifier
)

// SetDefaultStateClassifier registers the fallback StateClassifier used when a
// Printer leaves StateClassifier nil.
func SetDefaultStateClassifier(fn StateClassifier) {
	defaultStateClassifierMu.Lock()
	defaultStateClassifier = fn
	defaultStateClassifierMu.Unlock()
}

func getDefaultStateClassifier() StateClassifier {
	defaultStateClassifierMu.RLock()
	fn := defaultStateClassifier
	defaultStateClassifierMu.RUnlock()
	return fn
}

const (
	ansiReset       = "\x1b[0m"
	ansiDim         = "\x1b[2m"
	ansiRed         = "\x1b[31m"
	ansiBoldRed     = "\x1b[1;31m"
	ansiGreen       = "\x1b[32m"
	ansiYellow      = "\x1b[33m"
	ansiBoldMagenta = "\x1b[1;35m"
	ansiCyan        = "\x1b[36m"
)

type styler struct {
	enabled    bool
	classifier StateClassifier
}

func newStylerWithClassifier(w io.Writer, mode ColorMode, classifier StateClassifier) styler {
	if classifier == nil {
		classifier = getDefaultStateClassifier()
	}
	return styler{
		enabled:    ColorEnabled(w, mode),
		classifier: classifier,
	}
}

func (s styler) wrap(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func (s styler) dim(text string) string { return s.wrap(ansiDim, text) }

func (s styler) errorPrefix() string { return s.wrap(ansiBoldRed, "error:") }

func (s styler) hintPrefix() string { return s.wrap(ansiCyan, "hint:") }

// stateBadge styles a `,state` cell value using the configured StateClassifier.
func (s styler) stateBadge(text string) string {
	if !s.enabled || s.classifier == nil {
		return text
	}
	switch s.classifier(text) {
	case StateToneGreen:
		return s.wrap(ansiGreen, text)
	case StateToneYellow:
		return s.wrap(ansiYellow, text)
	case StateToneRed:
		return s.wrap(ansiRed, text)
	case StateToneMagenta:
		return s.wrap(ansiBoldMagenta, text)
	case StateToneCyan:
		return s.wrap(ansiCyan, text)
	case StateToneDim:
		return s.wrap(ansiDim, text)
	default:
		return text
	}
}
