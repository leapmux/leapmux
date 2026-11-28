package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// `file read --help` and `git read --help` state the default of --limit from the
// file-read contract, so the help cannot fall behind the default that the worker
// reads. The contract keeps the default a whole number of KiB.
func TestReadCommands_HelpStatesTheDefaultReadLimit(t *testing.T) {
	require.Zero(t, contracts.FileReadLimitBytes%1024, "the contract default is a whole number of KiB")
	want := fmt.Sprintf("the default of %d KiB", contracts.FileReadLimitBytes/1024)

	for _, path := range [][]string{{"file", "read"}, {"git", "read"}} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			o := runControlText(t, append(path, "--help")...)
			require.Equal(t, 0, o.Code)
			assert.Contains(t, o.Stdout, "--limit int")
			assert.Contains(t, o.Stdout, want)
		})
	}
}

type fileTestDispatcher struct {
	mu          sync.Mutex
	methods     []string
	listedPaths []string
	searchPaths []string
	globPaths   []string
	rootsResp   *leapmuxv1.ListFilesystemRootsResponse
	searchResp  *leapmuxv1.SearchFilesResponse
	globResp    *leapmuxv1.GlobFilesResponse
	listDirResp *leapmuxv1.ListDirectoryResponse
}

func (d *fileTestDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	switch req.GetMethod() {
	case leapmuxv1connect.WorkerFileServiceListDirectoryProcedure:
		var in leapmuxv1.ListDirectoryRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.listedPaths = append(d.listedPaths, in.GetPath())
		d.mu.Unlock()
		resp := d.listDirResp
		if resp == nil {
			resp = &leapmuxv1.ListDirectoryResponse{}
		}
		writeProtoResponse(w, resp)
	case leapmuxv1connect.WorkerFileServiceListFilesystemRootsProcedure:
		resp := d.rootsResp
		if resp == nil {
			resp = &leapmuxv1.ListFilesystemRootsResponse{}
		}
		writeProtoResponse(w, resp)
	case leapmuxv1connect.WorkerFileServiceSearchFilesProcedure:
		var in leapmuxv1.SearchFilesRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.searchPaths = append(d.searchPaths, in.GetPath())
		d.mu.Unlock()
		resp := d.searchResp
		if resp == nil {
			resp = &leapmuxv1.SearchFilesResponse{}
		}
		writeProtoResponse(w, resp)
	case leapmuxv1connect.WorkerFileServiceGlobFilesProcedure:
		var in leapmuxv1.GlobFilesRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.globPaths = append(d.globPaths, in.GetPath())
		d.mu.Unlock()
		resp := d.globResp
		if resp == nil {
			resp = &leapmuxv1.GlobFilesResponse{}
		}
		writeProtoResponse(w, resp)
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (d *fileTestDispatcher) calledMethods() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

func (d *fileTestDispatcher) paths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.listedPaths...)
}

func (d *fileTestDispatcher) searchedPaths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.searchPaths...)
}

func (d *fileTestDispatcher) globbedPaths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.globPaths...)
}

func TestFileCommands_ValidateArgumentsBeforeDialing(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "file list negative max-depth", args: []string{"file", "list", "/repo", "--max-depth", "-1"}},
		{name: "file list empty path", args: []string{"file", "list", ""}},
		{name: "file list blank path", args: []string{"file", "list", "   "}},
		{name: "file search empty path", args: []string{"file", "search", "foo", ""}},
		{name: "file search blank path", args: []string{"file", "search", "foo", "  "}},
		{name: "file glob empty path", args: []string{"file", "glob", "*.go", ""}},
		{name: "file glob blank path", args: []string{"file", "glob", "*.go", "  "}},
		{name: "file read empty path", args: []string{"file", "read", ""}},
		{name: "file read blank path", args: []string{"file", "read", "   "}},
		{name: "file stat empty path", args: []string{"file", "stat", ""}},
		{name: "file stat blank path", args: []string{"file", "stat", "   "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A")}
			disp := &fileTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")
			t.Setenv(controlenv.WorkingDirVariable, "/repo")

			runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Empty(t, hub.called(), "local validation must fail before dialing the hub")
			assert.Empty(t, disp.calledMethods(), "local validation must fail before dialing the worker")
		})
	}
}

func TestFileCommands_JSONEmitsEmptyRootsAndFalseTruncated(t *testing.T) {
	disp := &fileTestDispatcher{
		rootsResp:  &leapmuxv1.ListFilesystemRootsResponse{},
		searchResp: &leapmuxv1.SearchFilesResponse{Truncated: false},
		globResp:   &leapmuxv1.GlobFilesResponse{Truncated: false},
	}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	rootsOut := runControl(t, "file", "roots")
	assert.Equal(t, []any{}, rootsOut.Data(t)["roots"])
	assert.Contains(t, rootsOut.Stdout, `"roots":[]`)

	searchOut := runControl(t, "file", "search", "needle", "/repo")
	searchData := searchOut.Data(t)
	truncated, hasKey := searchData["truncated"]
	require.True(t, hasKey, "truncated must be present in JSON output: %s", searchOut.Stdout)
	assert.Equal(t, false, truncated)

	globOut := runControl(t, "file", "glob", "*.go", "/repo")
	globData := globOut.Data(t)
	globTruncated, hasGlobKey := globData["truncated"]
	require.True(t, hasGlobKey, "truncated must be present in JSON output: %s", globOut.Stdout)
	assert.Equal(t, false, globTruncated)
}

func TestFileList_DoesNotSendLocalCwdToRemoteWorkerOnLocalHub(t *testing.T) {
	hub := &recordingHub{
		workers: []*leapmuxv1.Worker{
			{Id: "local-box", Online: true},
			{Id: "remote-box", Online: true},
		},
	}
	disp := &fileTestDispatcher{}
	h := startLocalHubChannelHarness(t, hub, disp)
	h.dispatcher.Handle(leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, channel.Registration{}, disp.DispatchWith)

	out := runControl(t, "file", "list", "--hub", h.url, "--worker", "remote-box")
	failure := out.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, failure.Message, "<path> is required")
	assert.Equal(t, 0, h.channelOpens(), "must not open a worker channel when path is missing for a remote worker")
	assert.Empty(t, disp.paths())
}

func TestFileList_UsesLocalCwdForImplicitSoleWorkerOnLocalHub(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	hub := &recordingHub{
		workers: []*leapmuxv1.Worker{{Id: "local-box", Online: true}},
	}
	disp := &fileTestDispatcher{}
	h := startLocalHubChannelHarness(t, hub, disp)
	h.dispatcher.Handle(leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, channel.Registration{}, disp.DispatchWith)

	_ = runControl(t, "file", "list", "--hub", h.url).Data(t)
	assert.Equal(t, 1, h.channelOpens())
	assert.Equal(t, []string{cwd}, disp.paths())
}

func TestFileList_DoesNotUseTabWorkingDirForAnotherWorkerOverIPC(t *testing.T) {
	hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
	disp := &fileTestDispatcher{}
	startSpawnIPC(t, hub, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

	out := runControl(t, "file", "list", "--worker", "worker-B")
	failure := out.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, failure.Message, "<path> is required")
	assert.Empty(t, disp.calledMethods())
}

func TestFileList_RejectsLocalCwdFallbackWithSavedContextWorkerOrRemoteHub(t *testing.T) {
	t.Run("local unix hub with saved context worker and no --worker flag", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "local-box", Online: true},
				{Id: "remote-box", Online: true},
			},
		}
		disp := &fileTestDispatcher{}
		h := startLocalHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, channel.Registration{}, disp.DispatchWith)
		require.NoError(t, control.SaveContext(control.ContextConfig{Worker: "remote-box"}))

		failure := runControl(t, "file", "list", "--hub", h.url).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, failure.Message, "<path> is required")
		assert.Equal(t, 0, h.channelOpens())
		assert.Empty(t, disp.paths())
	})

	t.Run("remote HTTP hub with implicit sole online worker and no path", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{{Id: "remote-sole", Online: true}},
		}
		disp := &fileTestDispatcher{}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, channel.Registration{}, disp.DispatchWith)

		failure := runControl(t, "file", "list", "--hub", h.url).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, failure.Message, "<path> is required")
		assert.Equal(t, 0, h.channelOpens())
		assert.Empty(t, disp.paths())
	})
}

func TestFileSearchAndGlob_OmittedPathUsesSameWorkerTabDirAndRejectsCrossWorker(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		readPaths func(*fileTestDispatcher) []string
	}{
		{
			name:      "file search",
			args:      []string{"file", "search", "needle"},
			readPaths: (*fileTestDispatcher).searchedPaths,
		},
		{
			name:      "file glob",
			args:      []string{"file", "glob", "*.go"},
			readPaths: (*fileTestDispatcher).globbedPaths,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" same-worker IPC uses tab working dir", func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A")}
			disp := &fileTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")
			t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

			data := runControl(t, tc.args...).Data(t)
			assert.Equal(t, "/local/tab", data["path"])
			assert.Equal(t, []string{"/local/tab"}, tc.readPaths(disp))
		})

		t.Run(tc.name+" cross-worker rejects omitted path", func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
			disp := &fileTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")
			t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

			args := append(append([]string(nil), tc.args...), "--worker", "worker-B")
			failure := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, failure.Message, "<path> is required")
			assert.Empty(t, disp.calledMethods())
			assert.Empty(t, tc.readPaths(disp))
		})
	}
}
