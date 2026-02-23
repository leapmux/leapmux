package logging

import (
	"fmt"
	"net"
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
	`                  |_|                       `,
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

var standaloneArt = [6]string{
	`  ____  _                  _       _                  `,
	` / ___|| |_ __ _ _ __   __| | __ _| | ___  _ __   ___ `,
	` \___ \| __/ _` + "`" + ` | '_ \ / _` + "`" + ` |/ _` + "`" + ` | |/ _ \| '_ \ / _ \`,
	`  ___) | || (_| | | | | (_| | (_| | | (_) | | | |  __/`,
	` |____/ \__\__,_|_| |_|\__,_|\__,_|_|\___/|_| |_|\___|`,
	`                                                       `,
}

// PrintBanner prints the LeapMux ASCII art logo with mode-specific
// art appended to the right. Below the art it prints version and
// listen address. Colors are used only when stderr is a TTY.
func PrintBanner(mode, ver, addr string) {
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
	default: // standalone
		modeArt = &standaloneArt
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

	// Info line below the art.
	if color {
		fmt.Fprintf(os.Stderr, "\n  %sversion%s %s   %saddr%s %s\n\n",
			dim, reset, ver, dim, reset, addr)
	} else {
		fmt.Fprintf(os.Stderr, "\n  version %s   addr %s\n\n", ver, addr)
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

// PrintAccessURL prints the full access URL and a QR code to stderr.
// The QR code is only printed when stderr is a TTY.
func PrintAccessURL(addr string) {
	url := addrToURL(addr)
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

	if isTTY {
		fmt.Fprintf(os.Stderr, "  %s%s➜%s  %s%s%s\n\n", bold, green, reset, bold, url, reset)
	} else {
		fmt.Fprintf(os.Stderr, "  ➜  %s\n\n", url)
	}

	if isTTY {
		qrterminal.GenerateWithConfig(url, qrterminal.Config{
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
}

// PrintQRCode prints just a QR code for the given URL to stderr (TTY only).
func PrintQRCode(url string) {
	isTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
	if !isTTY {
		return
	}
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
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
