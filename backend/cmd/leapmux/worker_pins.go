package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/tofupins"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// crossWorkerPinsOpTimeout limits the time a command spends in the pin store.
const crossWorkerPinsOpTimeout = 10 * time.Second

// newCrossWorkerPinsCommand builds `leapmux daemon worker cross-worker-pins`.
//
// The trust-on-first-use (TOFU) pin store lives in the Worker's data
// directory, so these commands work on local files alone; no Worker process
// needs to run. Remove a pin after a mismatch, once the presented fingerprint
// matches the one that the sibling Worker logs when it starts.
func newCrossWorkerPinsCommand() *cobra.Command {
	group := cli.Group("cross-worker-pins", "List and remove the keys this Worker pinned for its sibling Workers")
	group.Long = `List and remove the keys this Worker pinned for its sibling Workers.

The Worker pins the keys of a sibling Worker the first time it connects to it,
and refuses a later connection whose keys differ. After you verify a sibling
Worker's new keys, remove its pin. The next verified connection pins the new keys.`
	group.AddCommand(
		newPinStoreLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List the pinned keys of every sibling Worker",
			Examples: []cli.Example{
				{Command: "leapmux daemon worker cross-worker-pins list"},
			},
		}, func(ctx context.Context, _ *cli.Invocation, store *tofupins.Store) (any, error) {
			pins, err := store.Snapshot(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"pins": crossWorkerPinRows(pins)}, nil
		}),
		newPinStoreLeaf(cli.Leaf{
			Use:   "get <worker-id>",
			Short: "Print the pinned keys of one sibling Worker",
			Args:  map[string]string{"worker-id": pinStoreWorkerIDHelp},
			Examples: []cli.Example{
				{Command: "leapmux daemon worker cross-worker-pins get 0193c0de-7a1b-7c2d-8e3f-4a5b6c7d8e9f"},
			},
		}, func(ctx context.Context, inv *cli.Invocation, store *tofupins.Store) (any, error) {
			pin, err := lookupCrossWorkerPin(ctx, store, inv.Arg("worker-id"))
			if err != nil {
				return nil, err
			}
			return pin, nil
		}),
		newPinStoreLeaf(cli.Leaf{
			Use:     "remove <worker-id>",
			Aliases: []string{"rm"},
			Short:   "Remove the pinned keys of one sibling Worker",
			Mutates: true,
			Args:    map[string]string{"worker-id": pinStoreWorkerIDHelp},
			Examples: []cli.Example{
				{Command: "leapmux daemon worker cross-worker-pins remove 0193c0de-7a1b-7c2d-8e3f-4a5b6c7d8e9f"},
			},
		}, func(ctx context.Context, inv *cli.Invocation, store *tofupins.Store) (any, error) {
			id := inv.Arg("worker-id")
			if _, err := lookupCrossWorkerPin(ctx, store, id); err != nil {
				return nil, err
			}
			if err := store.Remove(ctx, id); err != nil {
				return nil, fmt.Errorf("remove the pin of %s: %w", id, err)
			}
			return map[string]any{"removed": id}, nil
		}),
	)
	return group
}

// pinStoreWorkerIDHelp describes the <worker-id> argument of the pin store
// commands.
const pinStoreWorkerIDHelp = "The full id of the sibling Worker, as `leapmux daemon worker cross-worker-pins list` prints it."

// newPinStoreLeaf builds one command of the pin store: the instance
// selection flags of the Worker, and run on the opened store.
func newPinStoreLeaf(leaf cli.Leaf, run func(context.Context, *cli.Invocation, *tofupins.Store) (any, error)) *cobra.Command {
	var flags *config.Flags
	leaf.Flags = func(fs *pflag.FlagSet) {
		// The data directory holds the store, so the command takes the flags
		// that select it, as every other command of an instance does.
		flags = config.NewFlags(config.LoadOptions{CLIFlags: instanceSelectionFlags})
		flags.Register(fs)
	}
	leaf.Run = func(inv *cli.Invocation) (any, error) {
		cfg, err := flags.Load(inv.Command.Flags())
		if err != nil {
			return nil, err
		}
		store, err := crossworker.NewPinStore(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("open the pin store: %w", err)
		}
		ctx, cancel := context.WithTimeout(inv.Context, crossWorkerPinsOpTimeout)
		defer cancel()
		return run(ctx, inv, store)
	}
	return cli.NewLeaf(leaf)
}

// crossWorkerPin is one pinned sibling Worker, as the pin store commands
// print it. The table of `list` leaves out the keys, which are too long for
// a column; `get` and --json print them.
type crossWorkerPin struct {
	WorkerID        string    `json:"worker_id" table:"WORKER ID"`
	FirstSeenAt     time.Time `json:"first_seen_at" table:"FIRST SEEN"`
	LastUsedAt      time.Time `json:"last_used_at" table:"LAST USED"`
	X25519PublicKey string    `json:"x25519_public_key"`
	MlkemPublicKey  string    `json:"mlkem_public_key"`
	SlhdsaPublicKey string    `json:"slhdsa_public_key"`
}

func newCrossWorkerPin(id string, entry tofupins.Entry) crossWorkerPin {
	return crossWorkerPin{
		WorkerID:        id,
		FirstSeenAt:     entry.FirstSeenAt,
		LastUsedAt:      entry.LastUsedAt,
		X25519PublicKey: entry.X25519PublicKey,
		MlkemPublicKey:  entry.MlkemPublicKey,
		SlhdsaPublicKey: entry.SlhdsaPublicKey,
	}
}

// crossWorkerPinRows lists the pins in the order of their Worker ids, so the
// output does not depend on the order of a map.
func crossWorkerPinRows(pins map[string]tofupins.Entry) []crossWorkerPin {
	rows := make([]crossWorkerPin, 0, len(pins))
	for id, entry := range pins {
		rows = append(rows, newCrossWorkerPin(id, entry))
	}
	slices.SortFunc(rows, func(a, b crossWorkerPin) int { return cmp.Compare(a.WorkerID, b.WorkerID) })
	return rows
}

// lookupCrossWorkerPin returns the pin of the sibling Worker id. A Worker
// without a pin is not found, so a mistyped id never reads as a removed pin.
func lookupCrossWorkerPin(ctx context.Context, store *tofupins.Store, id string) (crossWorkerPin, error) {
	pins, err := store.Snapshot(ctx)
	if err != nil {
		return crossWorkerPin{}, err
	}
	entry, ok := pins[id]
	if !ok {
		return crossWorkerPin{}, cli.Newf(cli.CodeNotFound, "no pin for the sibling Worker %q", id).
			WithHint("run \"leapmux daemon worker cross-worker-pins list\" for the pinned Workers")
	}
	return newCrossWorkerPin(id, entry), nil
}
