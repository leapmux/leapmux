package cmd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestRunWorkerListReadsEveryPage pins that `worker list` prints the workers
// of every page. The hub answers one page per call, so a single call printed
// only the first page and said nothing about the rest.
func TestRunWorkerListReadsEveryPage(t *testing.T) {
	hub := &recordingHub{listWorkerPages: [][]string{{"worker-A"}, {"worker-B", "worker-C"}}}
	startSpawnIPC(t, hub, nil)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWorkerList(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  []map[string]any `json:"data"`
		Error map[string]any   `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	ids := make([]any, 0, len(env.Data))
	for _, row := range env.Data {
		ids = append(ids, row["id"])
	}
	assert.Equal(t, []any{"worker-A", "worker-B", "worker-C"}, ids)
	assert.Equal(t, []string{"ListWorkers", "ListWorkers"}, hub.called())
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

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWorkerList(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	assert.JSONEq(t, "[]", string(env.Data))
	assert.Equal(t, []string{"ListWorkers"}, hub.called(), "an empty page with no cursor ends the listing")
}

// A hub that refuses the listing makes the verb print one error envelope and
// no data. The hub reports a denial and an absence alike, so the code is
// not_found.
func TestRunWorkerListReportsARefusedListing(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)

	out := withCapturedStdout(t, func() {
		require.Error(t, RunWorkerList(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  json.RawMessage   `json:"data"`
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
	assert.Equal(t, "not_found", env.Error["code"])
	assert.Contains(t, env.Error["message"], "ListWorkers")
	assert.Empty(t, env.Data)
}

// `worker get` prints the worker through the same explicit map as `worker
// list`. Each instant prints as a string in the CLI's one layout, where the
// raw proto printed a protojson object.
func TestRunWorkerGetPrintsTheWorkerRow(t *testing.T) {
	created := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	hub := &recordingHub{worker: &leapmuxv1.Worker{
		Id:           "worker-A",
		Online:       true,
		CreatedAt:    timestamppb.New(created),
		RegisteredBy: "u-1",
	}}
	startSpawnIPC(t, hub, nil)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWorkerGet(fakeCmdCtx{}, []string{"--worker-id", "worker-A"}))
	})

	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error)
	assert.Equal(t, map[string]any{
		"id":              "worker-A",
		"online":          true,
		"registered_by":   "u-1",
		"auto_registered": false,
		"created_at":      created.Format(timeFormat),
	}, env.Data)
	assert.Equal(t, []string{"GetWorker"}, hub.called())
}

// TestWorkerToMapPrintsInstantsAsStrings pins the worker JSON shape. Each
// instant prints in the CLI's one time layout, and an unset instant (a worker
// that never connected has no last-seen time) is omitted rather than printed
// as an empty object or a zero time.
func TestWorkerToMapPrintsInstantsAsStrings(t *testing.T) {
	created := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	row := workerToMap(&leapmuxv1.Worker{
		Id:             "worker-A",
		Online:         true,
		CreatedAt:      timestamppb.New(created),
		RegisteredBy:   "u-1",
		AutoRegistered: true,
	})
	assert.Equal(t, map[string]any{
		"id":              "worker-A",
		"online":          true,
		"registered_by":   "u-1",
		"auto_registered": true,
		"created_at":      created.Format(timeFormat),
	}, row)

	seen := created.Add(time.Minute)
	row = workerToMap(&leapmuxv1.Worker{Id: "worker-B", CreatedAt: timestamppb.New(created), LastSeenAt: timestamppb.New(seen)})
	assert.Equal(t, seen.Format(timeFormat), row["last_seen_at"])
	assert.Equal(t, false, row["online"], "an offline worker still prints its online flag")
}

// TestWorkerToMap_CarriesEveryFieldOfWorker is the tripwire for the explicit
// map. A field that the Worker message gains prints nowhere until workerToMap
// prints it, and an operator's script then reads an absent key, not an error.
// The two booleans also run one at a time, so a swap between them fails.
func TestWorkerToMap_CarriesEveryFieldOfWorker(t *testing.T) {
	worker := &leapmuxv1.Worker{}
	assertRowMapperCarriesEveryField(t, rowMapper{
		name:   "workerToMap",
		msg:    worker,
		render: func() map[string]any { return workerToMap(worker) },
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
