package logging

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/mdp/qrterminal/v3"
)

// ANSI color codes.
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	cyan    = "\033[36m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	magenta = "\033[35m"
	dim     = "\033[2m"
)

// Logo lines — base LeapMux ASCII art.
var logoLines = [6]string{
	`  _                      __  __            `,
	` | |    ___  __ _ _ __  |  \/  |_   ___  __`,
	` | |   / _ \/ _` + "`" + ` | '_ \ | |\/| | | | \ \/ /`,
	` | |__|  __/ (_| | |_) || |  | | |_| |>  < `,
	` |_____\___|\__,_| .__/ |_|  |_|\__,_/_/\_\`,
	`                 |_|                        `,
}

// Mode-specific ASCII art (right-side, same height as logo).
var hubArt = [6]string{
	`  _   _       _     `,
	` | | | |_   _| |__  `,
	` | |_| | | | | '_ \ `,
	` |  _  | |_| | |_) |`,
	` |_| |_|\__,_|_.__/ `,
	`                     `,
}

var workerArt = [6]string{
	` __        __         _             `,
	` \ \      / /__  _ __| | _____ _ __ `,
	`  \ \ /\ / / _ \| '__| |/ / _ \ '__|`,
	`   \ V  V / (_) | |  |   <  __/ |   `,
	`    \_/\_/ \___/|_|  |_|\_\___|_|   `,
	`                                     `,
}

var soloArt = [6]string{
	`  ____        _       `,
	` / ___|  ___ | | ___  `,
	` \___ \ / _ \| |/ _ \ `,
	`  ___) | (_) | | (_) |`,
	` |____/ \___/|_|\___/ `,
	`                      `,
}

var devArt = [6]string{
	`  ____             `,
	` |  _ \  _____   __`,
	` | | | |/ _ \ \ / /`,
	` | |_| |  __/\ V / `,
	` |____/ \___| \_/  `,
	`                   `,
}

// PrintBanner prints the LeapMux ASCII art logo with mode-specific
// art appended to the right. Below the art it prints version info
// and copyright. Colors are used only when stderr is a TTY.
func PrintBanner(mode, ver, commitHash, buildTime string) {
	color := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	var modeArt *[6]string
	var modeColor string
	switch mode {
	case "hub":
		modeArt = &hubArt
		modeColor = green
	case "worker":
		modeArt = &workerArt
		modeColor = yellow
	case "dev":
		modeArt = &devArt
		modeColor = yellow
	default: // solo
		modeArt = &soloArt
		modeColor = magenta
	}

	for i := 0; i < 6; i++ {
		if color {
			fmt.Fprintf(os.Stderr, "%s%s%s%s%s%s\n",
				bold+cyan, logoLines[i], reset,
				bold+modeColor, modeArt[i], reset)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s\n", logoLines[i], modeArt[i])
		}
	}

	// Build the version info line: "0.0.1-dev (deadbeef) · Fri, 4/3/2026, 2:00:00 AM"
	info := ver
	if commitHash != "" {
		info += " (" + commitHash + ")"
	}
	if buildTime != "" {
		info += " \u00b7 " + buildTime
	}

	// Info lines below the art.
	copyright := "Copyright \u00a9 Event Loop, Inc."
	if color {
		fmt.Fprintf(os.Stderr, "\n  %s%s%s\n", dim, info, reset)
		fmt.Fprintf(os.Stderr, "  %s%s%s\n\n", dim, copyright, reset)
	} else {
		fmt.Fprintf(os.Stderr, "\n  %s\n", info)
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
