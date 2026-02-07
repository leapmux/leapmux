package logging

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
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
