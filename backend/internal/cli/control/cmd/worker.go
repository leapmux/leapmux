package cmd

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your workers with their names",
			Examples: []cli.Example{
				{Command: "leapmux worker list"},
			},
			Run: runWorkerList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [worker]",
			Short: "Show what the hub knows about a worker (default: the worker of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux worker get"},
				{Command: "leapmux worker get name:build-box"},
			},
			Complete: completeTargets,
			Run:      runWorkerGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "info [worker]",
			Short: "Show what an online worker reports about itself: name, OS, home and build",
			Examples: []cli.Example{
				{Command: "leapmux worker info"},
			},
			Complete: completeTargets,
			Run:      runWorkerInfo,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "ping [worker]",
			Short: "Measure end-to-end encrypted round-trip latency to an online worker",
			Examples: []cli.Example{
				{Command: "leapmux worker ping"},
				{Command: "leapmux worker ping name:build-box"},
			},
			Complete: completeTargets,
			Run:      runWorkerPing,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "deregister <worker>",
			Aliases:     []string{"rm"},
			Short:       "Deregister one of your workers from the hub",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux worker deregister name:old-box"},
			},
			Complete: completeTargets,
			Run:      runWorkerDeregister,
		}),
		cli.Group("key", "Create, extend, revoke, and email registration keys for new workers",
			cli.NewLeaf(cli.Leaf{
				Use:     "create",
				Mutates: true,
				Short:   "Create a registration key that a new worker uses once, within five minutes",
				Examples: []cli.Example{
					{Comment: "Register a worker on another machine with the key.", Command: `K=$(leapmux worker key create --json | jq -r .data.key)`},
				},
				Run: runWorkerKeyCreate,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:     "extend <key>",
				Mutates: true,
				Short:   "Extend a registration key's expiry by five minutes",
				Examples: []cli.Example{
					{Command: "leapmux worker key extend lmx_reg_..."},
				},
				Run: runWorkerKeyExtend,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:              "revoke <key>",
				Aliases:          []string{"rm"},
				Mutates:          true,
				Destructive:      true,
				QuietUnsupported: true,
				Short:            "Revoke an unused registration key immediately",
				Examples: []cli.Example{
					{Command: "leapmux worker key revoke lmx_reg_..."},
				},
				Run: runWorkerKeyRevoke,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:              "email <key>",
				Mutates:          true,
				QuietUnsupported: true,
				Short:            "Email worker registration instructions to your verified email address",
				Examples: []cli.Example{
					{Command: "leapmux worker key email lmx_reg_..."},
				},
				Flags: func(fs *pflag.FlagSet) {
					fs.String("command", "", "custom registration command line to embed in the email")
				},
				Run: runWorkerKeyEmail,
			}),
		),
		cli.Group("pins", "Inspect and reset the worker keys that this machine trusts",
			cli.NewLeaf(cli.Leaf{
				Use:     "list",
				Aliases: []string{"ls"},
				Short:   "List the worker keys that this machine pinned for the hub",
				Examples: []cli.Example{
					{Command: "leapmux worker pins list"},
				},
				Run: runWorkerPinsList,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:   "get <worker>",
				Short: "Show the pinned key of one worker",
				Args:  map[string]string{"worker": pinnedWorkerArgHelp},
				Examples: []cli.Example{
					{Command: "leapmux worker pins get 5f2c9a1d"},
				},
				Complete: completePinnedWorkers,
				Run:      runWorkerPinsGet,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:         "remove <worker>",
				Aliases:     []string{"rm"},
				Short:       "Forget a worker's pinned key, so the next connection pins it again",
				Args:        map[string]string{"worker": pinnedWorkerArgHelp},
				Mutates:     true,
				Destructive: true,
				Examples: []cli.Example{
					{Comment: "Forget the key of a worker that was reinstalled.", Command: "leapmux worker pins remove 5f2c9a1d"},
				},
				Complete: completePinnedWorkers,
				Run:      runWorkerPinsRemove,
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
	Online       bool   `json:"online" table:"ONLINE,state"`
	LastSeenAt   string `json:"last_seen_at,omitempty" table:"LAST SEEN,dim"`
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
func listAllWorkers(ctx context.Context, c control.Peer) ([]*leapmuxv1.Worker, error) {
	return readAllPages(func(page *leapmuxv1.PageRequest) ([]*leapmuxv1.Worker, *leapmuxv1.PageResponse, error) {
		var resp leapmuxv1.ListWorkersResponse
		err := hubCallUnary(ctx, c, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, &leapmuxv1.ListWorkersRequest{Page: page}, &resp)
		return resp.GetWorkers(), resp.GetPage(), err
	}, (*leapmuxv1.Worker).GetId)
}

// resolveOptionalWorker resolves the optional [worker] positional argument,
// falling back to s.workerScope() (self -> context default -> sole online
// worker) when omitted.
func (s *session) resolveOptionalWorker(arg string) (string, error) {
	if strings.TrimSpace(arg) != "" {
		target, err := s.resolveOne(resolve.KindWorker, arg)
		if err != nil {
			return "", err
		}
		return target.ID, nil
	}
	return s.workerScope()
}

// resolveWorkerForGet resolves the optional [worker] argument and returns any
// hub worker record and display name that resolution already fetched, so
// runWorkerGet avoids redundant GetWorker and GetWorkerSystemInfo RPCs.
func (s *session) resolveWorkerForGet(arg string) (workerID string, worker *leapmuxv1.Worker, name string, nameQueried bool, err error) {
	if strings.TrimSpace(arg) == "" {
		workerID, err = s.workerScope()
		return workerID, nil, "", false, err
	}
	var (
		listedWorkers []*leapmuxv1.Worker
		fetchedNames  map[string]string
		queriedNames  map[string]struct{}
	)
	listing := s.listing()
	rawWorkers := listing.Workers
	listing.Workers = func(ctx context.Context) ([]*leapmuxv1.Worker, error) {
		workers, listErr := rawWorkers(ctx)
		if listErr == nil {
			listedWorkers = workers
		}
		return workers, listErr
	}
	rawNames := listing.WorkerNames
	listing.WorkerNames = func(ctx context.Context, workers []*leapmuxv1.Worker) map[string]string {
		names := rawNames(ctx, workers)
		if fetchedNames == nil {
			fetchedNames = map[string]string{}
			queriedNames = map[string]struct{}{}
		}
		for _, w := range workers {
			queriedNames[w.GetId()] = struct{}{}
		}
		for id, n := range names {
			fetchedNames[id] = n
		}
		return names
	}
	target, err := resolve.Canonical(s.ctx, listing, resolve.KindWorker, arg)
	if err != nil {
		return "", nil, "", false, err
	}
	for _, w := range listedWorkers {
		if w.GetId() == target.ID {
			worker = w
			break
		}
	}
	_, nameQueried = queriedNames[target.ID]
	return target.ID, worker, fetchedNames[target.ID], nameQueried, nil
}

// runWorkerGet prints the hub's row of one worker, which the hub answers
// for an offline worker too.
func runWorkerGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, worker, name, nameQueried, err := s.resolveWorkerForGet(inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if worker == nil {
		var resp leapmuxv1.GetWorkerResponse
		if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, &leapmuxv1.GetWorkerRequest{WorkerId: workerID}, &resp); err != nil {
			return nil, err
		}
		worker = resp.GetWorker()
	}
	if worker.GetOnline() && !nameQueried {
		name = fetchWorkerNames(s.ctx, s.c, []string{workerID})[workerID]
	}
	return workerRowOf(worker, name), nil
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
	workerID, err := s.resolveOptionalWorker(inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetWorkerSystemInfoResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp); err != nil {
		return nil, err
	}
	return workerInfo{
		ID:      workerID,
		Name:    resp.GetName(),
		OS:      resp.GetOs(),
		Arch:    resp.GetArch(),
		HomeDir: resp.GetHomeDir(),
		Build:   buildInfoOf(resp.GetBuild()),
	}, nil
}

// registrationKey is the result of `worker key create` and `worker key extend`.
type registrationKey struct {
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

func (r registrationKey) QuietLines() []string { return []string{r.Key} }

// runWorkerKeyCreate mints a registration key. The RPC needs the worker:admin
// scope, which the bridge of a tab's socket does not carry, so the command
// always talks to the hub directly.
func runWorkerKeyCreate(inv *cli.Invocation) (any, error) {
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	resp, err := c.WorkerManagementService().CreateRegistrationKey(s.ctx, &leapmuxv1.CreateRegistrationKeyRequest{})
	if err != nil {
		return nil, err
	}
	return registrationKey{Key: resp.GetRegistrationKey(), ExpiresAt: formatTime(resp.GetExpiresAt())}, nil
}

type workerPingResult struct {
	ID        string `json:"id"`
	LatencyMS int64  `json:"latency_ms"`
}

func runWorkerPing(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.resolveOptionalWorker(inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	var latencyMS int64
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		start := time.Now()
		var resp leapmuxv1.PingResponse
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerSystemServicePingProcedure, &leapmuxv1.PingRequest{}, &resp); err != nil {
			return err
		}
		latencyMS = time.Since(start).Milliseconds()
		return nil
	}); err != nil {
		return nil, err
	}
	return workerPingResult{
		ID:        workerID,
		LatencyMS: latencyMS,
	}, nil
}

func runWorkerDeregister(inv *cli.Invocation) (any, error) {
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorker, inv.Arg("worker"))
	if err != nil {
		return nil, err
	}
	if _, err := c.WorkerManagementService().DeregisterWorker(s.ctx, &leapmuxv1.DeregisterWorkerRequest{WorkerId: target.ID}); err != nil {
		return nil, err
	}
	return struct {
		ID           string `json:"id"`
		Deregistered bool   `json:"deregistered"`
	}{ID: target.ID, Deregistered: true}, nil
}

func runWorkerKeyExtend(inv *cli.Invocation) (any, error) {
	key, err := requireNonEmptyArg(inv, "key")
	if err != nil {
		return nil, err
	}
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	resp, err := c.WorkerManagementService().ExtendRegistrationKey(s.ctx, &leapmuxv1.ExtendRegistrationKeyRequest{RegistrationKey: key})
	if err != nil {
		return nil, err
	}
	return registrationKey{Key: key, ExpiresAt: formatTime(resp.GetExpiresAt())}, nil
}

func runWorkerKeyRevoke(inv *cli.Invocation) (any, error) {
	key, err := requireNonEmptyArg(inv, "key")
	if err != nil {
		return nil, err
	}
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if _, err := c.WorkerManagementService().RevokeRegistrationKey(s.ctx, &leapmuxv1.RevokeRegistrationKeyRequest{RegistrationKey: key}); err != nil {
		return nil, err
	}
	return struct {
		Key     string `json:"key"`
		Revoked bool   `json:"revoked"`
	}{Key: key, Revoked: true}, nil
}

func runWorkerKeyEmail(inv *cli.Invocation) (any, error) {
	key, err := requireNonEmptyArg(inv, "key")
	if err != nil {
		return nil, err
	}
	s, c, err := openDirectHubSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cmdLine := strings.TrimSpace(stringFlag(inv, "command"))
	if cmdLine == "" {
		cmdLine = "leapmux daemon worker register --hub " + c.HubURL + " --registration-key " + key
	}
	if _, err := c.WorkerManagementService().EmailRegistrationInstructions(s.ctx, &leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: key,
		Command:         cmdLine,
	}); err != nil {
		return nil, err
	}
	return struct {
		Key     string `json:"key"`
		Command string `json:"command"`
		Sent    bool   `json:"sent"`
	}{Key: key, Command: cmdLine, Sent: true}, nil
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
	FirstSeenAt     string `json:"first_seen_at,omitempty" table:"FIRST SEEN,dim"`
	LastUsedAt      string `json:"last_used_at,omitempty" table:"LAST USED,dim"`
}

func (p pinRow) QuietLines() []string { return []string{p.WorkerID} }

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
func openPinStore(cmd *cobra.Command) (*control.PinStore, error) {
	hub, err := hubURLFor(cmd)
	if err != nil {
		return nil, err
	}
	return control.NewPinStore(hub)
}

// pinList is the result of `worker pins list`.
type pinList struct {
	Pins []pinRow `json:"pins"`
}

func (l pinList) QuietLines() []string {
	out := make([]string, 0, len(l.Pins))
	for _, p := range l.Pins {
		out = append(out, p.WorkerID)
	}
	return out
}

// withPinStore opens the invocation's pin store and runs fn under a context
// limited by pinOpTimeout.
func withPinStore(inv *cli.Invocation, fn func(context.Context, *control.PinStore) (any, error)) (any, error) {
	pins, err := openPinStore(inv.Command)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(inv.Context, pinOpTimeout)
	defer cancel()
	return fn(ctx, pins)
}

func runWorkerPinsList(inv *cli.Invocation) (any, error) {
	return withPinStore(inv, func(ctx context.Context, pins *control.PinStore) (any, error) {
		entries, err := pins.List(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]pinRow, 0, len(entries))
		for _, p := range entries {
			rows = append(rows, pinRowOf(p))
		}
		return pinList{Pins: rows}, nil
	})
}

// pinnedWorkerArgHelp describes the <worker> argument of the pin commands.
// A pin belongs to the local credential store, not to the hub, so these
// commands match only the ids that the store holds.
const pinnedWorkerArgHelp = `A worker id, or a unique prefix of 4 or more characters of one, as "leapmux worker pins list" prints it.`

// findPin returns the pin matching input by exact worker ID, or by unique
// prefix when input does not use "id:".
func findPin(ctx context.Context, pins *control.PinStore, input string) (control.PinListEntry, error) {
	input = strings.TrimSpace(input)
	minLen := resolve.MinPrefixLength
	if rest, ok := strings.CutPrefix(input, "id:"); ok {
		input = strings.TrimSpace(rest)
		if input == "" {
			return control.PinListEntry{}, cli.Usagef(`"id:" needs a worker id after the colon`)
		}
		minLen = math.MaxInt
	}
	entries, err := pins.List(ctx)
	if err != nil {
		return control.PinListEntry{}, err
	}
	found, ambiguous, ok := lookupByIDPrefixMin(entries, input, minLen, func(p control.PinListEntry) string {
		return p.WorkerID
	})
	if ok {
		return found, nil
	}
	if len(ambiguous) == 0 {
		return control.PinListEntry{}, cli.Newf(cli.CodeNotFound, "no worker key is pinned for %q", input).
			WithHint(`list pinned workers with "leapmux worker pins list"`)
	}
	candidates := make([]cli.Candidate, 0, len(ambiguous))
	for _, p := range ambiguous {
		candidates = append(candidates, cli.Candidate{ID: p.WorkerID, Kind: "worker"})
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%d pinned workers match %q", len(candidates), input)
	e.Candidates = candidates
	return control.PinListEntry{}, e
}

func runWorkerPinsGet(inv *cli.Invocation) (any, error) {
	return withPinStore(inv, func(ctx context.Context, pins *control.PinStore) (any, error) {
		p, err := findPin(ctx, pins, inv.Arg("worker"))
		if err != nil {
			return nil, err
		}
		return pinRowOf(p), nil
	})
}

// pinRemoveResult is the result of `worker pins remove`.
type pinRemoveResult struct {
	WorkerID string `json:"worker_id"`
	Removed  bool   `json:"removed"`
}

func (r pinRemoveResult) QuietLines() []string { return []string{r.WorkerID} }

// runWorkerPinsRemove drops a pin so the next verified channel can pin its key.
func runWorkerPinsRemove(inv *cli.Invocation) (any, error) {
	return withPinStore(inv, func(ctx context.Context, pins *control.PinStore) (any, error) {
		p, err := findPin(ctx, pins, inv.Arg("worker"))
		if err != nil {
			return nil, err
		}
		if err := pins.Remove(ctx, p.WorkerID); err != nil {
			return nil, err
		}
		return pinRemoveResult{WorkerID: p.WorkerID, Removed: true}, nil
	})
}
