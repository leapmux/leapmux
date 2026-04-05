package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/util/version"
)

func main() {
	logging.Setup()

	if len(os.Args) < 2 {
		// No subcommand: run solo mode (default).
		if os.Getenv("LEAPMUX_DOCKER") == "1" {
			fmt.Fprintf(os.Stderr, "error: LEAPMUX_MODE env var is required in Docker\n")
			fmt.Fprintf(os.Stderr, "usage: leapmux [hub|worker|dev|version] [flags]\n")
			os.Exit(1)
		}
		if err := runSolo(os.Args[1:], true); err != nil {
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
	case "dev":
		if err := runSolo(os.Args[2:], false); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
	case "admin":
		if err := runAdmin(os.Args[2:]); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version.Value)
	default:
		// If the first arg starts with '-', treat as solo flags.
		if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
			if os.Getenv("LEAPMUX_DOCKER") == "1" {
				fmt.Fprintf(os.Stderr, "error: LEAPMUX_MODE env var is required in Docker\n")
				fmt.Fprintf(os.Stderr, "usage: leapmux [hub|worker|dev|version] [flags]\n")
				os.Exit(1)
			}
			if err := runSolo(os.Args[1:], true); err != nil {
				slog.Error("fatal", "error", err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		fmt.Fprintf(os.Stderr, "usage: leapmux [hub|worker|dev|version] [flags]\n")
		os.Exit(1)
	}
}
