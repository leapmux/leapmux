package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

// startNotifyWorker serves w as the Worker of the tab that runs the command,
// as a spawned agent's socket does: LEAPMUX_CONTROL_WORKER_ID is worker-A, and
// the token belongs to the tab agent-1 (see startSpawnIPC).
func startNotifyWorker(t *testing.T, w *privateCommandWorker) *recordingHub {
	t.Helper()
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, w)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
	return hub
}

// requireOneNotify returns the one Notify request that the fake Worker
// received, and the caller that the router dispatched it under.
func requireOneNotify(t *testing.T, w *privateCommandWorker) (*leapmuxv1.NotifyRequest, privateCommandCall) {
	t.Helper()
	calls := w.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, calls[0].method)
	return calls[0].request.(*leapmuxv1.NotifyRequest), calls[0]
}

// The command goes to the Worker of the tab that runs it, and the router
// stamps that tab from the control token: the CLI sends no tab, so it cannot
// name another one.
func TestNotifyOverIPC_SendsTheNotificationToTheTabsWorker(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Tests passed", "--body", "All 42 tests passed").Data(t)
	assert.Equal(t, map[string]any{"worker_id": "worker-A", "title": "Tests passed", "body": "All 42 tests passed"}, data)

	req, call := requireOneNotify(t, w)
	assert.True(t, proto.Equal(&leapmuxv1.NotifyRequest{Title: "Tests passed", Body: "All 42 tests passed"}, req), "request: %v", req)
	assert.Equal(t, "agent-1", call.caller.SourceTabID, "the router stamps the tab of the token")
}

// A body read from stdin goes to the Worker as the pipe wrote it. The Worker
// owns the trim, so the CLI sends the line ending that the pipe left and
// prints the text that the Worker answered with.
func TestNotifyOverIPC_ReadsTheBodyFromStdin(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: &leapmuxv1.NotifyResponse{
			Title: "Build failed",
			Body:  "error: 3 tests failed\nFAIL ./internal/cli",
		},
	}}
	startNotifyWorker(t, w)

	o := runControlIn(t, "error: 3 tests failed\nFAIL ./internal/cli\n", "notify", " Build failed ", "--body", "-", "--json")
	data := o.Data(t)
	assert.Equal(t, "Build failed", data["title"])
	assert.Equal(t, "error: 3 tests failed\nFAIL ./internal/cli", data["body"])

	req, _ := requireOneNotify(t, w)
	assert.Equal(t, " Build failed ", req.GetTitle())
	assert.Equal(t, "error: 3 tests failed\nFAIL ./internal/cli\n", req.GetBody())
}

// The result is the Worker's answer, not the command line: the Worker decides
// what it stored, and the CLI keeps no copy of that rule.
func TestNotifyOverIPC_PrintsTheWorkersAnswer(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: &leapmuxv1.NotifyResponse{Title: "Stored title"},
	}}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Sent title", "--body", "Sent body").Data(t)
	assert.Equal(t, map[string]any{"worker_id": "worker-A", "title": "Stored title"}, data,
		"a body that the Worker did not store is not printed")
}

// The body is optional, and an absent body leaves the result without one.
func TestNotifyOverIPC_SendsATitleAlone(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Done").Data(t)
	assert.NotContains(t, data, "body")

	req, _ := requireOneNotify(t, w)
	assert.True(t, proto.Equal(&leapmuxv1.NotifyRequest{Title: "Done"}, req), "request: %v", req)
}

// --worker selects another Worker. Over the socket of a tab, that call
// travels to the other Worker; the socket's own Worker serves the call here,
// because the selected Worker is the socket's own.
func TestNotifyOverIPC_SendsToTheSelectedWorker(t *testing.T) {
	w := &privateCommandWorker{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, w)

	data := runControl(t, "notify", "Deploy finished", "--worker", "worker-A").Data(t)
	assert.Equal(t, "worker-A", data["worker_id"])
	requireOneNotify(t, w)
}

func TestNotifyOverIPC_PrintsTheNotificationAsText(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	o := runControlText(t, "notify", "Tests passed", "--body", "All green")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "worker_id: worker-A")
	assert.Contains(t, o.Stdout, "title:     Tests passed")
	assert.Contains(t, o.Stdout, "body:      All green")
}

// What the CLI refuses, it refuses before it sends anything.
func TestNotify_RefusesAnInvalidCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"an empty title", []string{"notify", ""}, "the title is empty"},
		{"a title of spaces", []string{"notify", "  \t"}, "the title is empty"},
		{"no title", []string{"notify"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &privateCommandWorker{}
			startNotifyWorker(t, w)

			f := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.message)
			assert.Empty(t, w.recorded(), "a refused command line sends nothing")
		})
	}
}

// Outside a tab, nothing names a Worker, so the command asks for --worker.
func TestNotify_NeedsAWorkerOutsideATab(t *testing.T) {
	w := &privateCommandWorker{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, w)

	f := runControl(t, "notify", "Tests passed").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "no worker selected", f.Message)
	assert.Contains(t, f.Hints, "pass --worker <worker>, or run the command inside a LeapMux tab")
	assert.Empty(t, w.recorded())
}

// The Worker owns the length limits, so its refusal reaches the caller with
// its own words and code.
func TestNotifyOverIPC_ReportsTheWorkersRefusal(t *testing.T) {
	w := &privateCommandWorker{refusals: map[string]privateCommandRefusal{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: {codes.InvalidArgument, "title is 300 bytes long; the limit is 256 bytes"},
	}}
	startNotifyWorker(t, w)

	f := runControl(t, "notify", "A long title").requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, "the limit is 256 bytes")
}
