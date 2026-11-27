package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/util/version"
)

func TestRunWorkerPinsRemoveUpdatesAnExistingHandle(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	const hub = "http://127.0.0.1:4327"
	pins, err := control.NewPinStore(hub)
	require.NoError(t, err)
	for _, workerID := range []string{"worker-to-remove", "worker-to-keep"} {
		commit, err := pins.Resolve(t.Context(), workerID, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))
	}
	before, err := pins.List(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 2)
	require.Equal(t, "worker-to-keep", before[0].WorkerID)
	waitingCommit, err := pins.Resolve(t.Context(), "worker-to-remove", []byte{1}, []byte{2}, []byte{3})
	require.NoError(t, err)

	reply := runControl(t, "worker", "pins", "remove", "worker-to-remove", "--hub", hub).Data(t)
	assert.Equal(t, map[string]any{"worker_id": "worker-to-remove", "removed": true}, reply)
	require.ErrorContains(t, waitingCommit(t.Context()), "removal revision changed")
	after, err := pins.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before[:1], after)
}

func TestRunWorkerPinsListAndGetDistinguishReadErrorsFromMissingPins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	const hub = "http://127.0.0.1:4327"
	_, err := control.NewPinStore(hub)
	require.NoError(t, err)
	host, err := control.HubHost(hub)
	require.NoError(t, err)
	path := filepath.Join(dir, host, "pins.json")
	require.NoError(t, os.WriteFile(path, []byte("{invalid"), 0o644))

	list := func() cliOutcome {
		return runControl(t, "worker", "pins", "list", "--hub", hub)
	}
	get := func() cliOutcome {
		return runControl(t, "worker", "pins", "get", "missing", "--hub", hub)
	}
	assert.Contains(t, list().requireFailure(t, cli.CodeInternal).Message, "parse pins")
	assert.Contains(t, get().requireFailure(t, cli.CodeInternal).Message, "parse pins")

	require.NoError(t, os.WriteFile(path, []byte(`{"pins":{}}`), 0o644))
	assert.Equal(t, []any{}, list().Data(t)["pins"])
	assert.Contains(t, get().requireFailure(t, cli.CodeNotFound).Message, "no worker key is pinned")
}

// TestRunWorkerListReadsEveryPage pins that `worker list` prints the workers
// of every page. The hub answers one page per call, so a single call printed
// only the first page and said nothing about the rest.
func TestRunWorkerListReadsEveryPage(t *testing.T) {
	hub := &recordingHub{listWorkerPages: [][]string{{"worker-A"}, {"worker-B", "worker-C"}}}
	startSpawnIPC(t, hub, nil)

	out := runControl(t, "worker", "list").ok(t)

	var env struct {
		Data struct {
			Workers []map[string]any `json:"workers"`
		} `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	ids := make([]any, 0, len(env.Data.Workers))
	for _, row := range env.Data.Workers {
		ids = append(ids, row["id"])
	}
	assert.Equal(t, []any{"worker-A", "worker-B", "worker-C"}, ids)
	assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called())
	// An omitted limit resolves to the hub's small default page, which costs
	// ten times the requests for the same listing.
	assert.Equal(t, []int32{listPageSize, listPageSize}, hub.requestedWorkerPageLimits(),
		"every page asks for the hub's maximum page")
}

// An account with no worker prints an empty list, not null, so a script that
// iterates the list needs no special case.
func TestRunWorkerListPrintsAnEmptyListForNoWorkers(t *testing.T) {
	hub := &recordingHub{listWorkerPages: [][]string{{}}}
	startSpawnIPC(t, hub, nil)

	out := runControl(t, "worker", "list").ok(t)

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	assert.JSONEq(t, `{"workers":[]}`, string(env.Data))
	assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called(), "an empty page with no cursor ends the listing")
}

// A hub that refuses the listing makes the verb print one error envelope and
// no data, with the code of the hub's refusal.
func TestRunWorkerListReportsARefusedListing(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)

	out := runControl(t, "worker", "list").failed(t)

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
	assert.Equal(t, "permission_denied", env.Error["code"])
	assert.Contains(t, env.Error["message"], leapmuxv1connect.WorkerManagementServiceListWorkersProcedure)
	assert.Empty(t, env.Data)
}

// A listing that the hub never ends makes the verb print one error envelope and
// no data. Printing the workers read so far would present part of the list as
// all of it.
func TestRunWorkerListReportsAListingTheHubDidNotEnd(t *testing.T) {
	hub := &recordingHub{listWorkerPages: [][]string{{"worker-A"}, {"worker-B"}}, stallLastWorkerPage: true}
	startSpawnIPC(t, hub, nil)

	out := runControl(t, "worker", "list").failed(t)

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
	assert.Equal(t, "internal", env.Error["code"])
	assert.Contains(t, env.Error["message"], "the hub did not end the listing")
	assert.Empty(t, env.Data)
	assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called(),
		"the loop stops at the first page whose cursor did not advance")
}

// `worker get` prints the worker through the same row as `worker list`. Each
// instant prints as a string in the CLI's one layout, where the raw proto
// printed a protojson object. A full id costs no list call.
func TestRunWorkerGetPrintsTheWorkerRow(t *testing.T) {
	created := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	hub := &recordingHub{workers: []*leapmuxv1.Worker{{
		Id:           fullTestID("worker-A"),
		Online:       false,
		CreatedAt:    timestamppb.New(created),
		RegisteredBy: "u-1",
	}}}
	startSpawnIPC(t, hub, nil)

	out := runControl(t, "worker", "get", fullTestID("worker-A")).ok(t)

	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	assert.Equal(t, map[string]any{
		"id":              fullTestID("worker-A"),
		"online":          false,
		"registered_by":   "u-1",
		"auto_registered": false,
		"created_at":      timefmt.Format(created),
	}, env.Data)
	assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure}, hub.called(),
		"an offline worker is not asked for its name")
}

// TestWorkerRowOfPrintsInstantsAsStrings pins the worker JSON shape. Each
// instant prints in the CLI's one time layout, and an unset instant (a worker
// that never connected has no last-seen time) is omitted rather than printed
// as an empty object or a zero time.
func TestWorkerRowOfPrintsInstantsAsStrings(t *testing.T) {
	created := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	row := jsonMap(t, workerRowOf(&leapmuxv1.Worker{
		Id:             "worker-A",
		Online:         true,
		CreatedAt:      timestamppb.New(created),
		RegisteredBy:   "u-1",
		AutoRegistered: true,
	}, "build-box"))
	assert.Equal(t, map[string]any{
		"id":              "worker-A",
		"name":            "build-box",
		"online":          true,
		"registered_by":   "u-1",
		"auto_registered": true,
		"created_at":      timefmt.Format(created),
	}, row)

	seen := created.Add(time.Minute)
	row = jsonMap(t, workerRowOf(&leapmuxv1.Worker{Id: "worker-B", CreatedAt: timestamppb.New(created), LastSeenAt: timestamppb.New(seen)}, ""))
	assert.Equal(t, timefmt.Format(seen), row["last_seen_at"])
	assert.Equal(t, false, row["online"], "an offline worker still prints its online flag")
	assert.NotContains(t, row, "name", "a worker that reported no name prints none")
}

// TestWorkerRowOf_CarriesEveryFieldOfWorker is the tripwire for the explicit
// row. A field that the Worker message gains prints nowhere until workerRowOf
// prints it, and an operator's script then reads an absent key, not an error.
// The two booleans also run one at a time, so a swap between them fails.
func TestWorkerRowOf_CarriesEveryFieldOfWorker(t *testing.T) {
	worker := &leapmuxv1.Worker{}
	assertRowMapperCarriesEveryField(t, rowMapper{
		name:   "workerRowOf",
		msg:    worker,
		render: func() any { return workerRowOf(worker, "") },
		keys: map[string]string{
			"id":              "id",
			"online":          "online",
			"created_at":      "created_at",
			"last_seen_at":    "last_seen_at",
			"registered_by":   "registered_by",
			"auto_registered": "auto_registered",
		},
	})
}

// systemInfoDispatcher is the worker side of `worker info`. It answers
// GetWorkerSystemInfo with info, or with the in-band refusal when refusal is
// set, and records every method that it was asked for, so a test can prove
// that the command reached the worker.
type systemInfoDispatcher struct {
	info    *leapmuxv1.GetWorkerSystemInfoResponse
	refusal string

	mu      sync.Mutex
	methods []string
}

func (d *systemInfoDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	if req.GetMethod() != leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	if d.refusal != "" {
		_ = w.SendError(int32(codes.PermissionDenied), d.refusal)
		return
	}
	writeProtoResponse(w, d.info)
}

func (d *systemInfoDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

// sampleWorkerSystemInfo gives each field a value of its own, so a verb that
// prints one field under the key of another fails.
func sampleWorkerSystemInfo() *leapmuxv1.GetWorkerSystemInfoResponse {
	return &leapmuxv1.GetWorkerSystemInfoResponse{
		Name:    "build-box",
		Os:      "linux",
		Arch:    "arm64",
		HomeDir: "/home/amy",
		Build: &leapmuxv1.BuildInfo{
			Version:    "1.2.3",
			CommitHash: "abc1234",
			CommitTime: "2026-10-01T00:00:00Z",
			BuildTime:  "2026-10-02T00:00:00Z",
			Branch:     "feature/x",
		},
	}
}

// `worker info` prints what the worker reports through an explicit snake_case
// map. The build uses the key names of `leapmux control version`, so a script
// reads one spelling for every build that the CLI prints.
func TestRunWorkerInfoPrintsTheWorkerReport(t *testing.T) {
	disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, disp)

	out := runControl(t, "worker", "info", "worker-A").ok(t)

	assert.Equal(t, map[string]any{
		"id":       "worker-A",
		"name":     "build-box",
		"os":       "linux",
		"arch":     "arm64",
		"home_dir": "/home/amy",
		"build": map[string]any{
			"version":     "1.2.3",
			"commit":      "abc1234",
			"commit_time": "2026-10-01T00:00:00Z",
			"build_time":  "2026-10-02T00:00:00Z",
			"branch":      "feature/x",
			"formatted":   "1.2.3 · abc1234 · feature/x · " + version.FormatLocalTimestamp("2026-10-02T00:00:00Z"),
		},
	}, envelopeData(t, out))
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure}, disp.called())
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerManagementServiceListWorkersProcedure,
		leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
	}, hub.called(), "the hub resolves the short id and answers the preflight: the report comes from the worker")
}

// A spawned process inherits the worker id, so `worker info` with no flag
// describes the worker that runs it.
func TestRunWorkerInfoDefaultsToTheSpawningWorker(t *testing.T) {
	disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
	startSpawnIPC(t, &recordingHub{}, disp)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	out := runControl(t, "worker", "info").ok(t)

	assert.Equal(t, "build-box", envelopeData(t, out)["name"],
		"the report must arrive although this bearer may not read GetWorker")
}

// A worker that reports no build still prints a build object with empty
// values, so a script that reads `.data.build.version` needs no null check.
func TestRunWorkerInfoPrintsAnEmptyBuildForAnAbsentOne(t *testing.T) {
	disp := &systemInfoDispatcher{info: &leapmuxv1.GetWorkerSystemInfoResponse{Name: "build-box"}}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)

	out := runControl(t, "worker", "info", "worker-A").ok(t)

	assert.Equal(t, map[string]any{
		"id":       "worker-A",
		"name":     "build-box",
		"os":       "",
		"arch":     "",
		"home_dir": "",
		"build":    map[string]any{"version": "", "formatted": ""},
	}, envelopeData(t, out))
}

// With no worker to ask (and no sole online worker on the hub), the verb
// refuses before it contacts any worker.
func TestRunWorkerInfoRequiresAWorker(t *testing.T) {
	disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
	hub := &recordingHub{}
	startSpawnIPC(t, hub, disp)

	out := runControl(t, "worker", "info").failed(t)

	assert.Equal(t, "usage", envelopeError(t, out)["code"])
	assert.Empty(t, disp.called())
}

// An id that the hub does not know answers not_found from the preflight, so
// the request never reaches the worker.
func TestRunWorkerInfoRefusesAnUnknownWorker(t *testing.T) {
	disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)

	ghost := fullTestID("worker-ghost")
	out := runControl(t, "worker", "info", ghost).failed(t)

	got := envelopeError(t, out)
	assert.Equal(t, "not_found", got["code"])
	assert.Equal(t, "no such worker: "+ghost, got["message"])
	assert.Empty(t, disp.called(), "a rejected worker id must not reach the worker")
}

// An in-band refusal from the worker prints one envelope with the worker's
// code, and no report.
func TestRunWorkerInfoReportsARefusalFromTheWorker(t *testing.T) {
	disp := &systemInfoDispatcher{refusal: "this worker belongs to another account"}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)

	out := runControl(t, "worker", "info", "worker-A").failed(t)

	got := envelopeError(t, out)
	assert.Equal(t, "permission_denied", got["code"])
	assert.Contains(t, got["message"], "this worker belongs to another account")
	assert.NotContains(t, string(out), `"home_dir"`)
}

// unreachableSibling is a cross-worker client whose target is offline: the hub
// relay refuses to reach it, so no handler answers.
type unreachableSibling struct {
	controlipc.CrossWorkerClient
}

func (unreachableSibling) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "worker is offline")
}

// Over the local transport, an offline sibling worker answers one
// worker_offline envelope, the code a script sees on the hub transport.
func TestRunWorkerInfoReportsAnOfflineWorkerOverLocalIPC(t *testing.T) {
	disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
	hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
	startSpawnIPC(t, hub, disp, func(r *controlipc.Router) { r.CrossWorker = unreachableSibling{} })

	out := runControl(t, "worker", "info", "worker-B").failed(t)

	got := envelopeError(t, out)
	assert.Equal(t, "worker_offline", got["code"])
	assert.Contains(t, got["message"], "worker is offline")
	assert.Empty(t, disp.called(), "the local worker must not answer for its sibling")
}

// offlineWorkerHub is the part of a hub that `worker info` meets when the
// worker exists but is offline: GetWorker finds the row, and the channel
// refuses to open, the way ChannelService refuses for an offline worker.
type offlineWorkerHub struct {
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler
	leapmuxv1connect.UnimplementedChannelServiceHandler
}

func (offlineWorkerHub) GetWorker(_ context.Context, req *leapmuxv1.GetWorkerRequest) (*leapmuxv1.GetWorkerResponse, error) {
	return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: req.GetWorkerId()}}, nil
}

func (offlineWorkerHub) GetWorkerHandshakeParams(context.Context, *leapmuxv1.GetWorkerHandshakeParamsRequest) (*leapmuxv1.GetWorkerHandshakeParamsResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "worker is offline")
}

// Over the hub transport, `worker info` needs a channel to the worker, so an
// offline worker answers one worker_offline envelope. `worker get` keeps
// working in that state, because only the hub answers it.
func TestRunWorkerInfoReportsAnOfflineWorkerOverTheHub(t *testing.T) {
	isolateCLIEnv(t)
	clearRemoteEnv(t)

	hub := offlineWorkerHub{}
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(server, hub)
	leapmuxv1connect.RegisterChannelServiceHandler(server, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_at_1", ExpiresAt: time.Now().Add(time.Hour),
	}))

	workerB := fullTestID("worker-B")
	out := runControl(t, "worker", "info", "--hub", srv.URL, workerB).failed(t)

	got := envelopeError(t, out)
	assert.Equal(t, "worker_offline", got["code"])
	assert.Contains(t, got["message"], "worker is offline")

	out = runControl(t, "worker", "get", "--hub", srv.URL, workerB).ok(t)
	assert.Equal(t, workerB, envelopeData(t, out)["id"])
}

type localSoloWorkerHub struct {
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler

	mu        sync.Mutex
	emailReqs []*leapmuxv1.EmailRegistrationInstructionsRequest
}

func (*localSoloWorkerHub) CreateRegistrationKey(context.Context, *leapmuxv1.CreateRegistrationKeyRequest) (*leapmuxv1.CreateRegistrationKeyResponse, error) {
	return &leapmuxv1.CreateRegistrationKeyResponse{
		RegistrationKey: "lmx_reg_solo",
		ExpiresAt:       timestamppb.New(time.Now().Add(5 * time.Minute)),
	}, nil
}

func (*localSoloWorkerHub) ExtendRegistrationKey(context.Context, *leapmuxv1.ExtendRegistrationKeyRequest) (*leapmuxv1.ExtendRegistrationKeyResponse, error) {
	return &leapmuxv1.ExtendRegistrationKeyResponse{
		ExpiresAt: timestamppb.New(time.Now().Add(10 * time.Minute)),
	}, nil
}

func (*localSoloWorkerHub) RevokeRegistrationKey(context.Context, *leapmuxv1.RevokeRegistrationKeyRequest) (*leapmuxv1.RevokeRegistrationKeyResponse, error) {
	return &leapmuxv1.RevokeRegistrationKeyResponse{}, nil
}

func (h *localSoloWorkerHub) EmailRegistrationInstructions(_ context.Context, req *leapmuxv1.EmailRegistrationInstructionsRequest) (*leapmuxv1.EmailRegistrationInstructionsResponse, error) {
	h.mu.Lock()
	h.emailReqs = append(h.emailReqs, &leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: req.GetRegistrationKey(),
		Command:         req.GetCommand(),
	})
	h.mu.Unlock()
	return &leapmuxv1.EmailRegistrationInstructionsResponse{}, nil
}

func (h *localSoloWorkerHub) emailRequests() []*leapmuxv1.EmailRegistrationInstructionsRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*leapmuxv1.EmailRegistrationInstructionsRequest(nil), h.emailReqs...)
}

func (*localSoloWorkerHub) ListWorkers(_ context.Context, req *leapmuxv1.ListWorkersRequest) (*leapmuxv1.ListWorkersResponse, error) {
	w := &leapmuxv1.Worker{Id: fullTestID("worker-solo"), Online: true}
	if prefix := req.GetIdPrefix(); prefix != "" && !strings.HasPrefix(w.GetId(), prefix) {
		return &leapmuxv1.ListWorkersResponse{}, nil
	}
	return &leapmuxv1.ListWorkersResponse{
		Workers: []*leapmuxv1.Worker{w},
	}, nil
}

func (*localSoloWorkerHub) DeregisterWorker(context.Context, *leapmuxv1.DeregisterWorkerRequest) (*leapmuxv1.DeregisterWorkerResponse, error) {
	return &leapmuxv1.DeregisterWorkerResponse{}, nil
}

// Direct-hub worker management commands work over a local solo Hub IPC socket
// without stored credentials.
func TestRunWorkerDirectCommands_LocalIPCHubWorksWithoutStoredCredentials(t *testing.T) {
	isolateCLIEnv(t)

	hub := &localSoloWorkerHub{}
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(connectServer, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	sockURL := hubtransporttest.NewSocketServer(t, "solo-worker", mux)

	created := runControl(t, "worker", "key", "create", "--hub", sockURL).Data(t)
	assert.Equal(t, "lmx_reg_solo", created["key"])

	extended := runControl(t, "worker", "key", "extend", "--hub", sockURL, "lmx_reg_solo").Data(t)
	assert.Equal(t, "lmx_reg_solo", extended["key"])

	emailed := runControl(t, "worker", "key", "email", "--hub", sockURL, "lmx_reg_solo").Data(t)
	assert.Equal(t, true, emailed["sent"])
	emailReqs := hub.emailRequests()
	require.Len(t, emailReqs, 1)
	assert.Equal(t, "lmx_reg_solo", emailReqs[0].GetRegistrationKey())
	assert.Equal(t, "leapmux daemon worker register --hub "+sockURL+" --registration-key lmx_reg_solo", emailReqs[0].GetCommand())

	revoked := runControl(t, "worker", "key", "revoke", "--hub", sockURL, "lmx_reg_solo").Data(t)
	assert.Equal(t, true, revoked["revoked"])

	deregistered := runControl(t, "worker", "deregister", "--hub", sockURL, fullTestID("worker-solo")).Data(t)
	assert.Equal(t, true, deregistered["deregistered"])
}

type recordingDirectWorkerHub struct {
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler

	workers []*leapmuxv1.Worker

	mu             sync.Mutex
	deregisteredID string
	listCalls      int
}

func (h *recordingDirectWorkerHub) ListWorkers(_ context.Context, req *leapmuxv1.ListWorkersRequest) (*leapmuxv1.ListWorkersResponse, error) {
	h.mu.Lock()
	h.listCalls++
	h.mu.Unlock()
	if prefix := req.GetIdPrefix(); prefix != "" {
		var out []*leapmuxv1.Worker
		for _, w := range h.workers {
			if strings.HasPrefix(w.GetId(), prefix) {
				out = append(out, w)
			}
		}
		return &leapmuxv1.ListWorkersResponse{Workers: out}, nil
	}
	return &leapmuxv1.ListWorkersResponse{Workers: h.workers}, nil
}

func (h *recordingDirectWorkerHub) DeregisterWorker(_ context.Context, req *leapmuxv1.DeregisterWorkerRequest) (*leapmuxv1.DeregisterWorkerResponse, error) {
	h.mu.Lock()
	h.deregisteredID = req.GetWorkerId()
	h.mu.Unlock()
	return &leapmuxv1.DeregisterWorkerResponse{}, nil
}

// worker deregister resolves prefixes and executes DeregisterWorker against the
// same direct Hub even when LEAPMUX_CONTROL_SOCK points to a different Hub.
func TestRunWorkerDeregister_ResolvesAndDeregistersAgainstSameHub(t *testing.T) {
	ipcHub := &recordingHub{workers: workerRows("worker-shared-ipc")}
	startSpawnIPC(t, ipcHub, nil)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	directHub := &recordingDirectWorkerHub{
		workers: []*leapmuxv1.Worker{{Id: "worker-shared-direct", Online: false}},
	}
	cs := connect.NewServer()
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(cs, directHub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, cs, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_a_at_direct",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	t.Setenv("LEAPMUX_HUB", srv.URL)

	data := runControl(t, "worker", "deregister", "worker-shared").Data(t)
	assert.Equal(t, map[string]any{"id": "worker-shared-direct", "deregistered": true}, data)
	assert.Equal(t, "worker-shared-direct", directHub.deregisteredID)
	assert.Equal(t, 1, directHub.listCalls)
	assert.Empty(t, ipcHub.called(), "worker deregister must not resolve through the tab's local IPC socket")
}

// worker get reuses the Hub worker row and Worker display name fetched during
// prefix or name resolution instead of making redundant RPCs.
func TestRunWorkerGet_ReusesResolvedWorkerAndName(t *testing.T) {
	t.Run("by name skips GetWorker and second GetWorkerSystemInfo", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{
			Id:           "worker-named-1",
			Online:       true,
			RegisteredBy: "u-1",
		}}}
		h := startHubChannelHarness(t, hub, disp)

		data := runControl(t, "worker", "get", "--hub", h.url, "name:build-box").Data(t)
		assert.Equal(t, "worker-named-1", data["id"])
		assert.Equal(t, "build-box", data["name"])
		assert.Equal(t, true, data["online"])
		assert.Equal(t, "u-1", data["registered_by"])
		assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called(),
			"name resolution already loaded the worker row; GetWorker must not run")
		assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure}, disp.called(),
			"name resolution already queried GetWorkerSystemInfo once")
		assert.Equal(t, 1, h.channelOpens(), "must reuse the resolved name without opening a second E2EE channel")
	})

	t.Run("by prefix skips GetWorker and queries name once when online", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{
			Id:     "worker-prefix-1",
			Online: true,
		}}}
		h := startHubChannelHarness(t, hub, disp)

		data := runControl(t, "worker", "get", "--hub", h.url, "worker-pref").Data(t)
		assert.Equal(t, "worker-prefix-1", data["id"])
		assert.Equal(t, "build-box", data["name"])
		assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called(),
			"prefix resolution already loaded the worker row; GetWorker must not run")
		assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure}, disp.called())
		assert.Equal(t, 1, h.channelOpens())
	})

	t.Run("by prefix skips GetWorker and GetWorkerSystemInfo when offline", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{
			Id:     "worker-off-1",
			Online: false,
		}}}
		h := startHubChannelHarness(t, hub, disp)

		data := runControl(t, "worker", "get", "--hub", h.url, "worker-off").Data(t)
		assert.Equal(t, "worker-off-1", data["id"])
		assert.Equal(t, false, data["online"])
		assert.NotContains(t, data, "name")
		assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceListWorkersProcedure}, hub.called())
		assert.Empty(t, disp.called())
		assert.Equal(t, 0, h.channelOpens())
	})

	t.Run("omitted worker resolves via workerScope and fetches worker and name once", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{
			Id:     "worker-scoped-1",
			Online: true,
		}}}
		h := startHubChannelHarness(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-scoped-1")

		data := runControl(t, "worker", "get", "--hub", h.url).Data(t)
		assert.Equal(t, "worker-scoped-1", data["id"])
		assert.Equal(t, "build-box", data["name"])
		assert.Equal(t, true, data["online"])
		assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure}, hub.called())
		assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure}, disp.called())
		assert.Equal(t, 1, h.channelOpens())
	})
}

func TestFindPin_SupportsExactAndPrefixMatchingWithIDQualifier(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	const hub = "http://127.0.0.1:4327"
	pins, err := control.NewPinStore(hub)
	require.NoError(t, err)

	for _, wid := range []string{"worker-alpha-1234", "w1"} {
		commit, err := pins.Resolve(t.Context(), wid, []byte{1}, []byte{2}, []byte{3})
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))
	}

	got, err := findPin(t.Context(), pins, "worker-alpha")
	require.NoError(t, err)
	assert.Equal(t, "worker-alpha-1234", got.WorkerID)

	_, err = findPin(t.Context(), pins, "wor")
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	gotPrefix, err := findPin(t.Context(), pins, "id:worker-alpha")
	require.NoError(t, err)
	assert.Equal(t, "worker-alpha-1234", gotPrefix.WorkerID)

	gotShortExact, err := findPin(t.Context(), pins, "id:w1")
	require.NoError(t, err)
	assert.Equal(t, "w1", gotShortExact.WorkerID)

	_, err = findPin(t.Context(), pins, "id:wor")
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	gotExact, err := findPin(t.Context(), pins, "id:worker-alpha-1234")
	require.NoError(t, err)
	assert.Equal(t, "worker-alpha-1234", gotExact.WorkerID)

	_, err = findPin(t.Context(), pins, "id:")
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	commit2, err := pins.Resolve(t.Context(), "worker-alpha-5678", []byte{4}, []byte{5}, []byte{6})
	require.NoError(t, err)
	require.NoError(t, commit2(t.Context()))

	for _, selector := range []string{"worker-alpha", "id:worker-alpha"} {
		_, err = findPin(t.Context(), pins, selector)
		require.Error(t, err)
		ambiguousErr := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, ambiguousErr.Code, selector)
		assert.Equal(t, []cli.Candidate{
			{ID: "worker-alpha-1234", Kind: "worker"},
			{ID: "worker-alpha-5678", Kind: "worker"},
		}, ambiguousErr.Candidates, selector)
	}
}

func TestRunWorkerList_WkAliasRoutesToWorkerList(t *testing.T) {
	hub := &recordingHub{listWorkerPages: [][]string{{"worker-A", "worker-B"}}}
	startSpawnIPC(t, hub, nil)

	data := runControl(t, "wk", "list").Data(t)
	workers, ok := data["workers"].([]any)
	require.True(t, ok)
	require.Len(t, workers, 2)
	assert.Equal(t, "worker-A", workers[0].(map[string]any)["id"])
	assert.Equal(t, "worker-B", workers[1].(map[string]any)["id"])
}

func TestRunWorkerKeyCommands_RejectEmptyKeyBeforeDialing(t *testing.T) {
	isolateCLIEnv(t)
	for _, verb := range []string{"extend", "revoke", "email"} {
		t.Run(verb, func(t *testing.T) {
			f := runControl(t, "worker", "key", verb, "   ").requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "<key> is empty")
		})
	}
}
