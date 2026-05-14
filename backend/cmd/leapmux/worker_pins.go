package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// runWorkerCrossWorkerPins implements `leapmux worker cross-worker-pins
// list|remove [--target-worker-id <id>] [--data-dir <dir>]`.
//
// The TOFU pin store lives next to the worker's data directory so this
// command runs entirely against local files; no worker process needs to
// be running. Used to clear a stale pin after a sibling worker re-keys.
func runWorkerCrossWorkerPins(args []string) error {
	if len(args) == 0 {
		return crossWorkerPinsUsage(fmt.Errorf("missing subcommand"))
	}
	sub := args[0]
	rest := args[1:]
	fs := flag.NewFlagSet("leapmux worker cross-worker-pins "+sub, flag.ContinueOnError)
	var targetWorkerID, dataDir string
	fs.StringVar(&targetWorkerID, "target-worker-id", "", "sibling worker id (required for show/remove)")
	fs.StringVar(&dataDir, "data-dir", "", "worker data directory (defaults to LEAPMUX_DATA_DIR or platform default)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if dataDir == "" {
		// Reuse the standard config loader so the resolved data dir
		// matches what `leapmux worker` would use.
		cfg, _, err := config.Load([]string{})
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		dataDir = cfg.DataDir
	}
	store, err := crossworker.NewPinStore(dataDir)
	if err != nil {
		return fmt.Errorf("open pin store: %w", err)
	}
	switch sub {
	case "list":
		return printJSON(store.List())
	case "show":
		if targetWorkerID == "" {
			return fmt.Errorf("--target-worker-id is required for show")
		}
		pins := store.List()
		entry, ok := pins[targetWorkerID]
		if !ok {
			return fmt.Errorf("no pin recorded for target_worker_id=%s", targetWorkerID)
		}
		return printJSON(entry)
	case "remove":
		if targetWorkerID == "" {
			return fmt.Errorf("--target-worker-id is required for remove")
		}
		if err := store.Remove(targetWorkerID); err != nil {
			return fmt.Errorf("remove: %w", err)
		}
		return printJSON(map[string]string{"removed_target_worker_id": targetWorkerID})
	default:
		return crossWorkerPinsUsage(fmt.Errorf("unknown subcommand: %s", sub))
	}
}

func crossWorkerPinsUsage(err error) error {
	fmt.Fprintln(os.Stderr, "usage: leapmux worker cross-worker-pins list|show|remove [--target-worker-id=<id>] [--data-dir=<dir>]")
	return err
}
