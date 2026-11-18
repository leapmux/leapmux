package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// pinOpTimeout limits a command's transactions with the local pin store.
const pinOpTimeout = 10 * time.Second

func newWorkerCommand() *cobra.Command {
	return cli.Group("worker", "List workers, read what they report, and register new ones",
		cli.NewLeaf(cli.Leaf{
			Use:   "list",
			Short: "List your workers with their names",
			Examples: []cli.Example{
				{Command: "leapmux control worker list"},
			},
			Run: runWorkerList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [worker]",
			Short: "Show what the hub knows about a worker (default: the worker of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux control worker get"},
				{Command: "leapmux control worker get name:build-box"},
			},
			Complete: completeTargets,
			Run:      runWorkerGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "info [worker]",
			Short: "Show what an online worker reports about itself: name, OS, home and build",
			Examples: []cli.Example{
				{Command: "leapmux control worker info"},
			},
			Complete: completeTargets,
			Run:      runWorkerInfo,
		}),
		cli.Group("key", "Create keys that register new workers",
			cli.NewLeaf(cli.Leaf{
				Use:     "create",
				Mutates: true,
				Short:   "Create a registration key that a new worker uses once, within five minutes",
				Examples: []cli.Example{
					{Comment: "Register a worker on another machine with the key.", Command: `K=$(leapmux control worker key create --json | jq -r .data.key)`},
				},
				Run: runWorkerKeyCreate,
			}),
		),
		cli.Group("pins", "Inspect and reset the worker keys that this machine trusts",
			cli.NewLeaf(cli.Leaf{
				Use:   "list",
				Short: "List the worker keys that this machine pinned for the hub",
				Examples: []cli.Example{
					{Command: "leapmux control worker pins list"},
				},
				Run: runWorkerPinsList,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:   "get <worker>",
				Short: "Show the pinned key of one worker",
				Args:  map[string]string{"worker": pinnedWorkerArgHelp},
				Examples: []cli.Example{
					{Command: "leapmux control worker pins get 5f2c9a1d"},
				},
				Run: runWorkerPinsGet,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:     "remove <worker>",
				Short:   "Forget a worker's pinned key, so the next connection pins it again",
				Args:    map[string]string{"worker": pinnedWorkerArgHelp},
				Mutates: true,
				Examples: []cli.Example{
					{Comment: "Forget the key of a worker that was reinstalled.", Command: "leapmux control worker pins remove 5f2c9a1d"},
				},
				Run: runWorkerPinsRemove,
			}),
		),
	)
}

// workerRow is one worker in a result.
type workerRow struct {
	ID string `json:"id" table:"ID,short"`
	// Name is what the worker reports about itself. An offline worker
	// reports nothing, so its name is empty.
	Name         string `json:"name,omitempty" table:"NAME"`
	Online       bool   `json:"online" table:"ONLINE"`
	LastSeenAt   string `json:"last_seen_at,omitempty" table:"LAST SEEN"`
	CreatedAt    string `json:"created_at,omitempty"`
	RegisteredBy string `json:"registered_by,omitempty"`
	// AutoRegistered prints when false too: absent and false are different
	// answers to "did the worker register itself".
	AutoRegistered bool `json:"auto_registered"`
}

func workerRowOf(w *leapmuxv1.Worker, name string) workerRow {
	return workerRow{
		ID:             w.GetId(),
		Name:           name,
		Online:         w.GetOnline(),
		LastSeenAt:     formatTime(w.GetLastSeenAt()),
		CreatedAt:      formatTime(w.GetCreatedAt()),
		RegisteredBy:   w.GetRegisteredBy(),
		AutoRegistered: w.GetAutoRegistered(),
	}
}

// workerList is the result of `worker list`.
type workerList struct {
	Workers []workerRow `json:"workers"`
}

// runWorkerList prints EVERY worker the caller owns, with the name that
// each online worker reports. The hub answers one page per call, so the
// command reads each page in turn.
func runWorkerList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workers, err := listAllWorkers(s.ctx, s.c)
	if err != nil {
		return nil, err
	}
	names := s.listing().WorkerNames(s.ctx, workers)
	rows := make([]workerRow, 0, len(workers))
	for _, w := range workers {
		rows = append(rows, workerRowOf(w, names[w.GetId()]))
	}
	return workerList{Workers: rows}, nil
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

// runWorkerGet prints the hub's row of one worker, which the hub answers
// for an offline worker too.
func runWorkerGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorker, inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetWorkerResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, &leapmuxv1.GetWorkerRequest{WorkerId: target.ID}, &resp); err != nil {
		return nil, err
	}
	name := ""
	if resp.GetWorker().GetOnline() {
		name = fetchWorkerNames(s.ctx, s.c, []string{target.ID})[target.ID]
	}
	return workerRowOf(resp.GetWorker(), name), nil
}

// workerInfo is what a worker reports about itself.
type workerInfo struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	OS      string    `json:"os"`
	Arch    string    `json:"arch"`
	HomeDir string    `json:"home_dir"`
	Build   buildInfo `json:"build"`
}

// runWorkerInfo prints what a worker reports about itself. Only the worker
// can answer, so it needs a channel to an online worker.
func runWorkerInfo(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorker, inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, target.ID); err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetWorkerSystemInfoResponse
	if err := callInnerRPC(s.ctx, s.c, target.ID, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp); err != nil {
		return nil, err
	}
	return workerInfo{
		ID:      target.ID,
		Name:    resp.GetName(),
		OS:      resp.GetOs(),
		Arch:    resp.GetArch(),
		HomeDir: resp.GetHomeDir(),
		Build:   buildInfoOf(resp.GetBuild()),
	}, nil
}

// registrationKey is the result of `worker key create`.
type registrationKey struct {
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

// runWorkerKeyCreate mints a registration key. The RPC needs the worker:admin
// scope, which the bridge of a tab's socket does not carry, so the command
// always talks to the hub directly.
func runWorkerKeyCreate(inv *cli.Invocation) (any, error) {
	hub, err := hubURLFor(inv)
	if err != nil {
		return nil, err
	}
	c, err := control.NewClient(hub)
	if err != nil {
		return nil, err
	}
	ctx, cancel := rpcDeadline(inv.Context)
	defer cancel()
	resp, err := c.WorkerManagementService().CreateRegistrationKey(ctx, &leapmuxv1.CreateRegistrationKeyRequest{})
	if err != nil {
		return nil, err
	}
	return registrationKey{Key: resp.GetRegistrationKey(), ExpiresAt: formatTime(resp.GetExpiresAt())}, nil
}

// The `worker pins` verbs read and change this machine's TOFU pin store
// for one hub. The file lives under $LEAPMUX_CONTROL_CONFIG_DIR, keyed by the
// hub, and no hub RPC is involved: a <worker> here is an id, or a unique
// prefix of an id, among the pins.

// pinRow is one pinned worker key.
type pinRow struct {
	WorkerID        string `json:"worker_id" table:"WORKER,short"`
	X25519PublicKey string `json:"x25519_public_key"`
	MlkemPublicKey  string `json:"mlkem_public_key"`
	SlhdsaPublicKey string `json:"slhdsa_public_key"`
	FirstSeenAt     string `json:"first_seen_at,omitempty" table:"FIRST SEEN"`
	LastUsedAt      string `json:"last_used_at,omitempty" table:"LAST USED"`
}

func pinRowOf(p control.PinListEntry) pinRow {
	return pinRow{
		WorkerID:        p.WorkerID,
		X25519PublicKey: p.Entry.X25519PublicKey,
		MlkemPublicKey:  p.Entry.MlkemPublicKey,
		SlhdsaPublicKey: p.Entry.SlhdsaPublicKey,
		FirstSeenAt:     formatWallTime(p.Entry.FirstSeenAt),
		LastUsedAt:      formatWallTime(p.Entry.LastUsedAt),
	}
}

// openPinStore opens the pin store of the invocation's hub.
func openPinStore(inv *cli.Invocation) (*control.PinStore, error) {
	hub, err := hubURLFor(inv)
	if err != nil {
		return nil, err
	}
	return control.NewPinStore(hub)
}

// pinList is the result of `worker pins list`.
type pinList struct {
	Pins []pinRow `json:"pins"`
}

func runWorkerPinsList(inv *cli.Invocation) (any, error) {
	pins, err := openPinStore(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(inv.Context, pinOpTimeout)
	defer cancel()
	entries, err := pins.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]pinRow, 0, len(entries))
	for _, p := range entries {
		rows = append(rows, pinRowOf(p))
	}
	return pinList{Pins: rows}, nil
}

// findPin returns the one pin whose worker id is input or starts with it.
// pinnedWorkerArgHelp describes the <worker> argument of the pin commands.
// A pin belongs to the local credential store, not to the hub, so these
// commands match only the ids that the store holds.
const pinnedWorkerArgHelp = `A worker id, or a unique prefix of 4 or more characters of one, as "leapmux control worker pins list" prints it.`

func findPin(ctx context.Context, pins *control.PinStore, input string) (control.PinListEntry, error) {
	input = strings.TrimSpace(input)
	entries, err := pins.List(ctx)
	if err != nil {
		return control.PinListEntry{}, err
	}
	var matches []cli.Candidate
	var found control.PinListEntry
	for _, p := range entries {
		if p.WorkerID == input {
			return p, nil
		}
		if len(input) >= resolve.MinPrefixLength && strings.HasPrefix(p.WorkerID, input) {
			matches = append(matches, cli.Candidate{ID: p.WorkerID, Kind: "worker"})
			found = p
		}
	}
	switch len(matches) {
	case 1:
		return found, nil
	case 0:
		return control.PinListEntry{}, cli.Newf(cli.CodeNotFound, "no worker key is pinned for %q", input)
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%d pinned workers match %q", len(matches), input)
	e.Candidates = matches
	return control.PinListEntry{}, e
}

func runWorkerPinsGet(inv *cli.Invocation) (any, error) {
	pins, err := openPinStore(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(inv.Context, pinOpTimeout)
	defer cancel()
	p, err := findPin(ctx, pins, inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	return pinRowOf(p), nil
}

// pinRemoveResult is the result of `worker pins remove`.
type pinRemoveResult struct {
	WorkerID string `json:"worker_id"`
	Removed  bool   `json:"removed"`
}

// runWorkerPinsRemove drops a pin so the next verified channel can pin its key.
func runWorkerPinsRemove(inv *cli.Invocation) (any, error) {
	pins, err := openPinStore(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(inv.Context, pinOpTimeout)
	defer cancel()
	p, err := findPin(ctx, pins, inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if err := pins.Remove(ctx, p.WorkerID); err != nil {
		return nil, err
	}
	return pinRemoveResult{WorkerID: p.WorkerID, Removed: true}, nil
}
