package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/leapmux/leapmux/internal/logging"
)

var version = "dev"

func main() {
	logging.Setup()

	if len(os.Args) < 2 {
		// No subcommand: run standalone mode (default).
		if err := runStandalone(os.Args[1:]); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
		return
	}

	switch os.Args[1] {
	case "hub":
		if err := runHub(os.Args[2:]); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
	case "worker":
		if err := runWorker(os.Args[2:]); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		// If the first arg starts with '-', treat as standalone flags.
		if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
			if err := runStandalone(os.Args[1:]); err != nil {
				slog.Error("fatal", "error", err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		fmt.Fprintf(os.Stderr, "usage: leapmux [hub|worker|version] [flags]\n")
		os.Exit(1)
	}
}
