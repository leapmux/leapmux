package logging

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/leapmux/leapmux/util/version"
	"github.com/mattn/go-isatty"
)

// ANSI color codes.
const (
	reset = "\033[0m"
	bold  = "\033[1m"
	dim   = "\033[2m"
	green = "\033[32m"

	// Logo color: teal (#0D9488) — 256-color fallback: 36 (dark cyan).
	logoColor256 = "\033[38;2;13;148;136m" // 24-bit
	logoColor16  = "\033[36m"              // 16-color cyan

	// Mode color: amber (#F59E0B) — 256-color fallback: 33 (yellow).
	modeColor256 = "\033[38;2;245;158;11m" // 24-bit
	modeColor16  = "\033[33m"              // 16-color yellow
)

// Logo lines — compact LeapMux block art (2.5 visual lines).
// Spaces are replaced with figure space (U+2007) at init for consistent
// glyph width in proportional fonts.
var logoLines = [3]string{
	`  █   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █`,
	`  █   █▀  █▀█ █▀▀ █ ▀ █ █ █ ▄▀▄`,
	`  ▀▀▀ ▀▀▀ ▀ ▀ ▀   ▀   ▀ ▀▀▀ ▀ ▀`,
}

// Mode-specific block art (right-side, same height as logo).
var hubArt = [3]string{
	`   █ █ █ █ █▀▄`,
	`   █▀█ █ █ █▀▄`,
	`   ▀ ▀ ▀▀▀ ▀▀ `,
}

var workerArt = [3]string{
	`   █   █ █▀█ █▀█ █ █ █▀▀ █▀█`,
	`   █ █ █ █ █ █▀▄ █▀▄ █▀  █▀▄`,
	`    ▀ ▀  ▀▀▀ ▀ ▀ ▀ ▀ ▀▀▀ ▀ ▀`,
}

var soloArt = [3]string{
	`   █▀▀ █▀█ █   █▀█`,
	`   ▀▀█ █ █ █   █ █`,
	`   ▀▀▀ ▀▀▀ ▀▀▀ ▀▀▀`,
}

var devArt = [3]string{
	`   █▀▄ █▀▀ █ █`,
	`   █ █ █▀  █ █`,
	`   ▀▀  ▀▀▀  ▀ `,
}

func init() {
	// Replace ASCII spaces with figure space (U+2007) so the art
	// aligns correctly in proportional fonts.
	for i := range logoLines {
		logoLines[i] = strings.ReplaceAll(logoLines[i], " ", " ")
	}
	for _, art := range []*[3]string{&hubArt, &workerArt, &soloArt, &devArt} {
		for i := range art {
			art[i] = strings.ReplaceAll(art[i], " ", " ")
		}
	}
}

// PrintBanner prints the LeapMux ASCII art logo with mode-specific
// art appended to the right. Below the art it prints the canonical
// version identity (see version.Format) and copyright. Colors are
// used only when stderr is a TTY.
func PrintBanner(mode string) {
	color := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	var modeArt *[3]string
	switch mode {
	case "hub":
		modeArt = &hubArt
	case "worker":
		modeArt = &workerArt
	case "dev":
		modeArt = &devArt
	default: // solo
		modeArt = &soloArt
	}

	// Pick 24-bit or 16-color codes based on COLORTERM.
	lColor, mColor := logoColor16, modeColor16
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		lColor, mColor = logoColor256, modeColor256
	}

	fmt.Fprintln(os.Stderr)
	for i := 0; i < 3; i++ {
		if color {
			fmt.Fprintf(os.Stderr, "%s%s%s%s%s%s\n",
				bold+lColor, logoLines[i], reset,
				bold+mColor, modeArt[i], reset)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s\n", logoLines[i], modeArt[i])
		}
	}

	info := version.Format()

	// Copyright year prefers CommitTime; fall back to the current year.
	year := time.Now().Format("2006")
	if version.CommitTime != "" {
		if t, err := time.Parse(time.RFC3339, version.CommitTime); err == nil {
			year = t.Format("2006")
		}
	}
	copyright := fmt.Sprintf("Copyright © %s Event Loop, Inc.", year)
	if color {
		fmt.Fprintf(os.Stderr, "  %s%s%s\n", dim, info, reset)
		fmt.Fprintf(os.Stderr, "  %s%s%s\n\n", dim, copyright, reset)
	} else {
		fmt.Fprintf(os.Stderr, "  %s\n", info)
		fmt.Fprintf(os.Stderr, "  %s\n\n", copyright)
	}
}

// addrToURL converts a listen address (e.g. ":4327", "0.0.0.0:4327",
// "127.0.0.1:4327") into an http://localhost:<port> URL.
func addrToURL(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Fallback: try stripping leading ':'
		port = strings.TrimPrefix(addr, ":")
	}
	if port == "" || port == "80" {
		return "http://localhost"
	}
	return "http://localhost:" + port
}

// PrintRegistrationURL prints a registration approval message with the URL
// (or relative path for Unix sockets) to stderr, using colors when available.
func PrintRegistrationURL(url string, isRelativePath bool) {
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	label := "Approve this worker at:"
	if isRelativePath {
		label = "Approve this worker at the Hub's web UI:"
	}

	if isTTY {
		fmt.Fprintf(os.Stderr, "\n  %s%s%s\n\n  %s%s➜%s  %s%s%s\n\n", dim, label, reset, bold, green, reset, bold, url, reset)
	} else {
		fmt.Fprintf(os.Stderr, "\n  %s\n\n  ➜  %s\n\n", label, url)
	}
}

// PrintAccessURL prints the full access URL to stderr, derived from a listen
// address (e.g. ":4327" → "http://localhost:4327").
func PrintAccessURL(addr string) {
	PrintURL(addrToURL(addr))
}

// PrintBannerURL prints the public URL when set, otherwise derives one from
// the listen address. This is the URL the operator should see at startup.
func PrintBannerURL(publicURL, listen string) {
	if publicURL != "" {
		PrintURL(publicURL)
		return
	}
	PrintAccessURL(listen)
}

// PrintURL prints an already-normalized URL to stderr.
func PrintURL(url string) {
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	if isTTY {
		fmt.Fprintf(os.Stderr, "  %s%s➜%s  %s%s%s\n\n", bold, green, reset, bold, url, reset)
	} else {
		fmt.Fprintf(os.Stderr, "  ➜  %s\n\n", url)
	}
}
