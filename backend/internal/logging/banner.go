package logging

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/mdp/qrterminal/v3"
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
		logoLines[i] = strings.ReplaceAll(logoLines[i], " ", "\u2007")
	}
	for _, art := range []*[3]string{&hubArt, &workerArt, &soloArt, &devArt} {
		for i := range art {
			art[i] = strings.ReplaceAll(art[i], " ", "\u2007")
		}
	}
}

// VersionInfo holds the fields displayed below the banner art.
type VersionInfo struct {
	Version    string
	CommitHash string
	BuildTime  string
}

// PrintBanner prints the LeapMux ASCII art logo with mode-specific
// art appended to the right. Below the art it prints version info
// and copyright. Colors are used only when stderr is a TTY.
func PrintBanner(mode string, vi VersionInfo) {
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

	for i := 0; i < 3; i++ {
		if color {
			fmt.Fprintf(os.Stderr, "%s%s%s%s%s%s\n",
				bold+lColor, logoLines[i], reset,
				bold+mColor, modeArt[i], reset)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s\n", logoLines[i], modeArt[i])
		}
	}

	// Build the version info line: "0.0.1-dev (deadbeef) · Fri, 4/3/2026, 2:00:00 AM"
	info := vi.Version
	if vi.CommitHash != "" {
		info += " (" + vi.CommitHash + ")"
	}
	if vi.BuildTime != "" {
		display := vi.BuildTime
		if t, err := time.Parse(time.RFC3339, vi.BuildTime); err == nil {
			display = t.Local().Format("Mon, 1/2/2006, 3:04:05 PM")
		}
		info += " \u00b7 " + display
	}

	// Info lines below the art.
	copyright := "Copyright \u00a9 Event Loop, Inc."
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

// PrintAccessURL prints the full access URL and optionally a QR code to
// stderr. The QR code is skipped when the mode is "solo" or the listen
// address resolves to a loopback interface, since QR codes are only
// useful for accessing the server from another device.
func PrintAccessURL(mode, addr string) {
	u := addrToURL(addr)
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	if isTTY {
		fmt.Fprintf(os.Stderr, "  %s%s➜%s  %s%s%s\n\n", bold, green, reset, bold, u, reset)
	} else {
		fmt.Fprintf(os.Stderr, "  ➜  %s\n\n", u)
	}

	if isTTY && mode != "solo" && !isLoopbackAddr(addr) {
		printQRCode(u)
	}
}

// PrintQRCode prints a QR code for the given URL to stderr (TTY only).
// It is a no-op when the URL's host resolves to a loopback address.
func PrintQRCode(rawURL string) {
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
	if !isTTY {
		return
	}
	if isLoopbackURL(rawURL) {
		return
	}
	printQRCode(rawURL)
}

// printQRCode renders a QR code to stderr unconditionally.
func printQRCode(u string) {
	qrterminal.GenerateWithConfig(u, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stderr,
		QuietZone:      1,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
	})
	fmt.Fprintln(os.Stderr)
}

// isLoopbackAddr reports whether the host portion of a listen address
// (e.g. "127.0.0.1:4327", "localhost:4327", "[::1]:4327") refers to
// the loopback interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return isLoopbackHost(host)
}

// isLoopbackURL reports whether the host in a URL refers to loopback.
func isLoopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// isLoopbackHost reports whether a hostname or IP refers to the
// loopback interface.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	// Resolve hostnames like "localhost".
	addrs, err := net.LookupHost(host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if resolved := net.ParseIP(a); resolved != nil && !resolved.IsLoopback() {
			return false
		}
	}
	return len(addrs) > 0
}
