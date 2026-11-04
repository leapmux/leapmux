package cmd

import (
	"context"
	"flag"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
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
		return hubCallUnaryEmitOn(ctx, c, leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
			&leapmuxv1.GetWorkerRequest{WorkerId: got.WorkerID}, &resp,
			func() any { return workerToMap(resp.GetWorker()) })
	})
}

// RunWorkerInfo prints what a worker reports about itself. It takes the flags
// of `worker get` and derives --worker-id from --tab-id the same way. The two
// verbs ask different owners: the hub answers `worker get` for an offline
// worker too, and only the worker can answer this verb, so it needs a channel
// to an online worker.
func RunWorkerInfo(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	c, workerID, err := resolveWorker(hub, in)
	if err != nil {
		return err
	}
	var resp leapmuxv1.GetWorkerSystemInfoResponse
	return workerUnaryEmitOn(c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure,
		&leapmuxv1.GetWorkerSystemInfoRequest{}, &resp,
		func() any { return workerSystemInfoToMap(&resp) })
}

// RunWorkerList prints EVERY worker the caller owns. The hub answers one page
// per call, so the command reads each page in turn.
func RunWorkerList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	workers, err := listAllWorkers(context.Background(), c)
	if err != nil {
		return control.EmitErrorWith(classifyHubError(err), err)
	}
	rows := make([]map[string]any, 0, len(workers))
	for _, w := range workers {
		rows = append(rows, workerToMap(w))
	}
	return control.EmitData(rows)
}

// listAllWorkers reads every page of the caller's workers, over whichever
// transport the client uses.
func listAllWorkers(ctx context.Context, c *control.Client) ([]*leapmuxv1.Worker, error) {
	return readAllPages(func(page *leapmuxv1.PageRequest) ([]*leapmuxv1.Worker, *leapmuxv1.PageResponse, error) {
		var resp leapmuxv1.ListWorkersResponse
		err := hubCallUnary(ctx, c, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, &leapmuxv1.ListWorkersRequest{Page: page}, &resp)
		return resp.GetWorkers(), resp.GetPage(), err
	}, (*leapmuxv1.Worker).GetId)
}

// workerToMap renders one worker for the CLI's JSON. putTime prints each
// instant as a string in the CLI's one layout and omits an unset one, such as
// the last-seen time of a worker that never connected.
func workerToMap(w *leapmuxv1.Worker) map[string]any {
	row := map[string]any{
		"id":              w.GetId(),
		"online":          w.GetOnline(),
		"registered_by":   w.GetRegisteredBy(),
		"auto_registered": w.GetAutoRegistered(),
	}
	putTime(row, "created_at", w.GetCreatedAt())
	putTime(row, "last_seen_at", w.GetLastSeenAt())
	return row
}

// workerSystemInfoToMap renders a worker's report for the CLI's JSON through an
// explicit snake_case map, so the keys do not follow the proto's Go field names.
func workerSystemInfoToMap(info *leapmuxv1.GetWorkerSystemInfoResponse) map[string]any {
	return map[string]any{
		"name":     info.GetName(),
		"os":       info.GetOs(),
		"arch":     info.GetArch(),
		"home_dir": info.GetHomeDir(),
		"build":    buildInfoToMap(info.GetBuild()),
	}
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
