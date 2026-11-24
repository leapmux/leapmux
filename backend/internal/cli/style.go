package cli

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ColorMode controls whether ANSI color escapes are emitted in text output.
type ColorMode int

const (
	// ColorAuto emits ANSI color only on interactive TTYs (and honors NO_COLOR / CLICOLOR_FORCE).
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

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// ColorEnabled reports whether ANSI styling should be emitted to w under mode.
// NO_COLOR disables color under auto; CLICOLOR_FORCE enables it under auto.
func ColorEnabled(w io.Writer, mode ColorMode) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		if os.Getenv("NO_COLOR") != "" {
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
	enabled bool
}

func newStyler(w io.Writer, mode ColorMode) styler {
	return styler{enabled: ColorEnabled(w, mode)}
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

// stateBadge colors known agent, terminal, worker, and health state tokens.
func (s styler) stateBadge(text string) string {
	if !s.enabled {
		return text
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "idle", "ready", "active", "online", "ok", "pass", "running", "completed":
		return s.wrap(ansiGreen, text)
	case "working", "starting", "pending", "warn", "queued", "dispatching":
		return s.wrap(ansiYellow, text)
	case "waiting_for_user":
		return s.wrap(ansiBoldMagenta, text)
	case "error", "exited", "startup_failed", "failed", "offline", "fail", "closed", "canceled":
		return s.wrap(ansiRed, text)
	default:
		return text
	}
}
