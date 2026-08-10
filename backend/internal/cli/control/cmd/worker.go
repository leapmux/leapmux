package cmd

import (
	"context"
	"flag"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// RunWorkerGet prints metadata for a single worker. The resolver
// derives --worker-id from --tab-id when only the tab anchor is
// given (LocateTab returns the worker hosting the tab).
func RunWorkerGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkerID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		var resp leapmuxv1.GetWorkerResponse
		return hubCallUnaryEmitOn(ctx, c, "GetWorker",
			&leapmuxv1.GetWorkerRequest{WorkerId: got.WorkerID}, &resp,
			func() any { return resp.GetWorker() })
	})
}

func RunWorkerList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	var resp leapmuxv1.ListWorkersResponse
	return hubUnaryEmit(hub, "ListWorkers",
		&leapmuxv1.ListWorkersRequest{}, &resp,
		func() any { return resp.GetWorkers() })
}

// `worker pins` is a subgroup whose list/show/remove leaves operate on
// the worker-local TOFU pin store; the file lives under
// $LEAPMUX_CONTROL_CONFIG_DIR keyed by --hub. No hub RPC is involved,
// so the resolver isn't needed here -- each handler binds --hub
// (mandatory: pin file path) and, where applicable, --worker-id
// (defaulted from $LEAPMUX_CONTROL_WORKER_ID for scripts running inside
// a spawn).
//
// Splitting the verbs into distinct handlers means `leapmux control
// worker pins` with no subcommand dispatches through the standard
// "missing subcommand" path that prints the help block, instead of
// emitting a JSON `invalid_request` envelope from inside a single
// monolithic handler.

// openPinStoreFromHub validates --hub and opens the per-hub pin store.
// Centralising the gate keeps every leaf's error envelope identical.
func openPinStoreFromHub(hub string) (*control.PinStore, error) {
	if hub == "" {
		return nil, control.EmitError("invalid_request", "--hub is required (or set $LEAPMUX_HUB)")
	}
	pins, err := control.NewPinStore(hub)
	if err != nil {
		return nil, control.EmitErrorWith("pins_open_failed", err)
	}
	return pins, nil
}

// RunWorkerPinsList lists every pinned worker for the given hub.
func RunWorkerPinsList(rawCtx any, args []string) error {
	hub, err := parseHubOnly(rawCtx, args, nil)
	if err != nil {
		return err
	}
	pins, err := openPinStoreFromHub(hub)
	if err != nil {
		return err
	}
	return control.EmitData(pins.List())
}

// RunWorkerPinsShow surfaces a single recorded pin (--worker-id required).
func RunWorkerPinsShow(rawCtx any, args []string) error {
	var workerID string
	hub, err := parseHubOnly(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&workerID, "worker-id", os.Getenv("LEAPMUX_CONTROL_WORKER_ID"), "worker id (defaults to $LEAPMUX_CONTROL_WORKER_ID)")
	})
	if err != nil {
		return err
	}
	pins, err := openPinStoreFromHub(hub)
	if err != nil {
		return err
	}
	if workerID == "" {
		return control.EmitError("invalid_request", "--worker-id is required")
	}
	for _, p := range pins.List() {
		if p.WorkerID == workerID {
			return control.EmitData(p)
		}
	}
	return control.EmitError("not_found", "no pin recorded for worker_id="+workerID)
}

// RunWorkerPinsRemove drops a pin so the next connection to that
// worker triggers a fresh TOFU prompt.
func RunWorkerPinsRemove(rawCtx any, args []string) error {
	var workerID string
	hub, err := parseHubOnly(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&workerID, "worker-id", os.Getenv("LEAPMUX_CONTROL_WORKER_ID"), "worker id (defaults to $LEAPMUX_CONTROL_WORKER_ID)")
	})
	if err != nil {
		return err
	}
	pins, err := openPinStoreFromHub(hub)
	if err != nil {
		return err
	}
	if workerID == "" {
		return control.EmitError("invalid_request", "--worker-id is required")
	}
	if err := pins.Remove(workerID); err != nil {
		return control.EmitErrorWith("pins_remove_failed", err)
	}
	return control.EmitData(map[string]string{"removed_worker_id": workerID})
}
