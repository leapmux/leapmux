package cmd

import (
	"context"
	"flag"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// RunWorkerGet prints metadata for a single worker. The resolver
// derives --worker-id from --tab-id when only the tab anchor is
// given (LocateTab returns the worker hosting the tab).
func RunWorkerGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		var resp leapmuxv1.GetWorkerResponse
		return hubCallUnaryEmitOn(ctx, c, "GetWorker", "",
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
	return hubUnaryEmit(hub, "ListWorkers", "",
		&leapmuxv1.ListWorkersRequest{}, &resp,
		func() any { return resp.GetWorkers() })
}

// `worker pins` is a subgroup whose list/show/remove leaves operate on
// the worker-local TOFU pin store; the file lives under
// $LEAPMUX_REMOTE_CONFIG_DIR keyed by --hub. No hub RPC is involved,
// so the resolver isn't needed here -- each handler binds --hub
// (mandatory: pin file path) and, where applicable, --worker-id
// (defaulted from $LEAPMUX_REMOTE_WORKER_ID for scripts running inside
// a spawn).
//
// Splitting the verbs into distinct handlers means `leapmux remote
// worker pins` with no subcommand dispatches through the standard
// "missing subcommand" path that prints the help block, instead of
// emitting a JSON `invalid_request` envelope from inside a single
// monolithic handler.

// openPinStoreFromHub validates --hub and opens the per-hub pin store.
// Centralising the gate keeps every leaf's error envelope identical.
func openPinStoreFromHub(hub string) (*remote.PinStore, error) {
	if hub == "" {
		return nil, remote.EmitError("invalid_request", "--hub is required (or set $LEAPMUX_HUB)")
	}
	pins, err := remote.NewPinStore(hub)
	if err != nil {
		return nil, remote.EmitErrorWith("pins_open_failed", err)
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
	return remote.EmitData(pins.List())
}

// RunWorkerPinsShow surfaces a single recorded pin (--worker-id required).
func RunWorkerPinsShow(rawCtx any, args []string) error {
	var workerID string
	hub, err := parseHubOnly(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&workerID, "worker-id", os.Getenv("LEAPMUX_REMOTE_WORKER_ID"), "worker id (defaults to $LEAPMUX_REMOTE_WORKER_ID)")
	})
	if err != nil {
		return err
	}
	pins, err := openPinStoreFromHub(hub)
	if err != nil {
		return err
	}
	if workerID == "" {
		return remote.EmitError("invalid_request", "--worker-id is required")
	}
	for _, p := range pins.List() {
		if p.WorkerID == workerID {
			return remote.EmitData(p)
		}
	}
	return remote.EmitError("not_found", "no pin recorded for worker_id="+workerID)
}

// RunWorkerPinsRemove drops a pin so the next connection to that
// worker triggers a fresh TOFU prompt.
func RunWorkerPinsRemove(rawCtx any, args []string) error {
	var workerID string
	hub, err := parseHubOnly(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&workerID, "worker-id", os.Getenv("LEAPMUX_REMOTE_WORKER_ID"), "worker id (defaults to $LEAPMUX_REMOTE_WORKER_ID)")
	})
	if err != nil {
		return err
	}
	pins, err := openPinStoreFromHub(hub)
	if err != nil {
		return err
	}
	if workerID == "" {
		return remote.EmitError("invalid_request", "--worker-id is required")
	}
	if err := pins.Remove(workerID); err != nil {
		return remote.EmitErrorWith("pins_remove_failed", err)
	}
	return remote.EmitData(map[string]string{"removed_worker_id": workerID})
}
