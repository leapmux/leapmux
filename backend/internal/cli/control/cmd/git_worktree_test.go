package cmd

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestGitWorktreeLookupDisplaysCheckoutFields(t *testing.T) {
	result := gitWorktreeLookup{
		GitWorktreeRow: GitWorktreeRow{Path: "/repo/checkout", Branch: "feature/selected", Locked: true, LockReason: "selected for review"},
		WorkerID:       "worker-a", RepoRoot: "/repo/main",
	}
	for _, format := range []cli.Format{cli.FormatText, cli.FormatJSON} {
		var out bytes.Buffer
		printer := cli.Printer{Out: &out, Format: format}
		require.NoError(t, printer.Result(result))
		for _, field := range []string{"/repo/checkout", "feature/selected", "selected for review", "worker-a", "/repo/main"} {
			assert.Contains(t, out.String(), field)
		}
	}
}

func TestGitWorktreeBranchRequiresTheSelectedWorkersDirectory(t *testing.T) {
	hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
	dispatcher := &creationPublicationDispatcher{}
	startSpawnIPC(t, hub, dispatcher)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "/local/repo")
	outcome := runControl(t, "worktree", "get", "branch:topic", "--worker", "worker-B")
	failure := outcome.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, failure.Message, "requires --dir on the selected worker")
	assert.Empty(t, dispatcher.recorded())
}

type worktreeTestDispatcher struct {
	mu             sync.Mutex
	methods        []string
	getResp        *leapmuxv1.GetGitWorktreeResponse
	getReqs        []*leapmuxv1.GetGitWorktreeRequest
	createReqs     []*leapmuxv1.CreateGitWorktreeRequest
	removedPaths   []string
	inspectedPaths []string
	removalArrived chan struct{}
	branchArrived  chan struct{}
	removalErr     string
	branchErr      string
}

func (d *worktreeTestDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	switch req.GetMethod() {
	case leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure:
		var in leapmuxv1.GetGitWorktreeRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.getReqs = append(d.getReqs, &in)
		d.mu.Unlock()
		resp := d.getResp
		if resp == nil {
			resp = &leapmuxv1.GetGitWorktreeResponse{
				Worktree: &leapmuxv1.GitWorktreeEntry{Path: "/wt/feat", Branch: "feat"},
				RepoRoot: "/repo",
			}
		}
		writeProtoResponse(w, resp)
	case leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure:
		var in leapmuxv1.CreateGitWorktreeRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.createReqs = append(d.createReqs, &in)
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.CreateGitWorktreeResponse{
			WorktreePath: "/wt/" + in.GetBranch(),
			Branch:       in.GetBranch(),
			RepoRoot:     in.GetPath(),
		})
	case leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure:
		var in leapmuxv1.RemoveGitWorktreeRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.removedPaths = append(d.removedPaths, in.GetPath())
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.RemoveGitWorktreeResponse{})
	case leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure:
		var in leapmuxv1.InspectWorktreeRemovalRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.inspectedPaths = append(d.inspectedPaths, in.GetPath())
		d.mu.Unlock()
		if d.removalArrived != nil && d.branchArrived != nil {
			close(d.removalArrived)
			select {
			case <-d.branchArrived:
			case <-ctx.Done():
				_ = w.SendError(int32(codes.Canceled), ctx.Err().Error())
				return
			}
		}
		if d.removalErr != "" {
			_ = w.SendError(int32(codes.Internal), d.removalErr)
			return
		}
		writeProtoResponse(w, &leapmuxv1.InspectWorktreeRemovalResponse{BlockedReason: "worktree is locked"})
	case leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure:
		var in leapmuxv1.InspectBranchDeletionRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		if d.removalArrived != nil && d.branchArrived != nil {
			close(d.branchArrived)
			select {
			case <-d.removalArrived:
			case <-ctx.Done():
				_ = w.SendError(int32(codes.Canceled), ctx.Err().Error())
				return
			}
		}
		if d.branchErr != "" {
			_ = w.SendError(int32(codes.Internal), d.branchErr)
			return
		}
		writeProtoResponse(w, &leapmuxv1.InspectBranchDeletionResponse{
			IsWorktree:   true,
			WorktreePath: in.GetPath(),
			BranchName:   "feat",
			GitState:     &leapmuxv1.BranchGitState{HasUncommittedChanges: true, UnpushedCommitCount: 1},
		})
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (d *worktreeTestDispatcher) calledMethods() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

func TestWorktreeCommands_ValidateEmptySelectorsBeforeDialing(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "worktree get blank branch selector", args: []string{"worktree", "get", "branch:   "}},
		{name: "worktree get empty selector", args: []string{"worktree", "get", ""}},
		{name: "worktree get blank selector", args: []string{"worktree", "get", "   "}},
		{name: "worktree inspect empty path", args: []string{"worktree", "inspect", ""}},
		{name: "worktree inspect blank path", args: []string{"worktree", "inspect", "   "}},
		{name: "worktree inspect blank path selector", args: []string{"worktree", "inspect", "path:   "}},
		{name: "worktree inspect blank branch selector", args: []string{"worktree", "inspect", "branch:   "}},
		{name: "worktree remove blank branch selector", args: []string{"worktree", "remove", "branch:   "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A")}
			disp := &worktreeTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")
			t.Setenv(controlenv.WorkingDirVariable, "/repo")

			runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Empty(t, hub.called(), "local validation must fail before dialing the hub")
			assert.Empty(t, disp.calledMethods(), "local validation must fail before dialing the worker")
		})
	}
}

func TestWorktreeGet_AmbiguityOnOmittedSelectorReportsSelf(t *testing.T) {
	disp := &worktreeTestDispatcher{
		getResp: &leapmuxv1.GetGitWorktreeResponse{
			AmbiguousPaths: []string{"/wt/b", "/wt/a"},
		},
	}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	failure := runControl(t, "worktree", "get").requireFailure(t, cli.CodeAmbiguousTarget)
	assert.Equal(t, `worktree selector "self" matches 2 checkouts`, failure.Message)
	assert.Equal(t, []string{"give one of the path selectors"}, failure.Hints)
	assert.Equal(t, []cli.Candidate{
		{ID: "path:/wt/a", Kind: "worktree"},
		{ID: "path:/wt/b", Kind: "worktree"},
	}, failure.Candidates)
}

func TestWorktreeCommands_UnifiedAmbiguityFormatting(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "get", args: []string{"worktree", "get", "branch:feat", "--dir", "/repo"}},
		{name: "remove", args: []string{"worktree", "remove", "branch:feat", "--dir", "/repo"}},
		{name: "inspect", args: []string{"worktree", "inspect", "branch:feat", "--dir", "/repo"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			disp := &worktreeTestDispatcher{
				getResp: &leapmuxv1.GetGitWorktreeResponse{
					AmbiguousPaths: []string{"/wt/zeta", "/wt/alpha"},
				},
			}
			startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")

			failure := runControl(t, tc.args...).requireFailure(t, cli.CodeAmbiguousTarget)
			assert.Equal(t, `worktree selector "branch:feat" matches 2 checkouts`, failure.Message)
			assert.Equal(t, []string{"give one of the path selectors"}, failure.Hints)
			assert.Equal(t, []cli.Candidate{
				{ID: "path:/wt/alpha", Kind: "worktree"},
				{ID: "path:/wt/zeta", Kind: "worktree"},
			}, failure.Candidates)
			assert.Empty(t, disp.removedPaths)
			assert.Empty(t, disp.inspectedPaths)
		})
	}
}

func TestWorktreeRemoveAndInspect_BranchSelectorSharesOneChannel(t *testing.T) {
	t.Run("remove", func(t *testing.T) {
		disp := &worktreeTestDispatcher{}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, channel.Registration{}, disp.DispatchWith)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, channel.Registration{}, disp.DispatchWith)

		data := runControl(t, "worktree", "remove", "branch:feat", "--dir", "/repo", "--hub", h.url, "--worker", "worker-A").Data(t)
		assert.Equal(t, 1, h.channelOpens(), "branch resolution and removal must share one E2EE channel")
		assert.Equal(t, "/wt/feat", data["path"])
		assert.Equal(t, true, data["removed"])
		assert.Equal(t, []string{"/wt/feat"}, disp.removedPaths)
	})

	t.Run("inspect", func(t *testing.T) {
		disp := &worktreeTestDispatcher{}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, channel.Registration{}, disp.DispatchWith)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, channel.Registration{}, disp.DispatchWith)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, channel.Registration{}, disp.DispatchWith)

		data := runControl(t, "worktree", "inspect", "branch:feat", "--dir", "/repo", "--hub", h.url, "--worker", "worker-A").Data(t)
		assert.Equal(t, 1, h.channelOpens(), "branch resolution and inspect RPCs must share one E2EE channel")
		assert.Equal(t, "/wt/feat", data["path"])
		assert.Equal(t, "worktree is locked", data["removal_blocked_reason"])
		assert.Equal(t, float64(1), data["unpushed_commits"])
		assert.Equal(t, []string{"/wt/feat"}, disp.inspectedPaths)
	})
}

func TestWorktreeRemoveAndInspect_NotFoundBranchSelectorStopsBeforeMutationOrInspect(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "remove", args: []string{"worktree", "remove", "branch:missing", "--dir", "/repo"}},
		{name: "inspect", args: []string{"worktree", "inspect", "branch:missing", "--dir", "/repo"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			disp := &worktreeTestDispatcher{
				getResp: &leapmuxv1.GetGitWorktreeResponse{},
			}
			startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")

			failure := runControl(t, tc.args...).requireFailure(t, cli.CodeNotFound)
			assert.Equal(t, `no worktree matches "branch:missing"`, failure.Message)
			assert.Empty(t, disp.removedPaths)
			assert.Empty(t, disp.inspectedPaths)
			assert.Equal(t, []string{leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure}, disp.calledMethods())
		})
	}
}

func TestRunGitWorktreeInspect_RunsInspectRPCsConcurrentlyAndPropagatesErrors(t *testing.T) {
	t.Run("runs removal and branch inspection concurrently on one channel", func(t *testing.T) {
		disp := &worktreeTestDispatcher{
			removalArrived: make(chan struct{}),
			branchArrived:  make(chan struct{}),
		}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, channel.Registration{}, disp.DispatchWith)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, channel.Registration{}, disp.DispatchWith)

		data := runControl(t, "worktree", "inspect", "/wt/feat", "--hub", h.url, "--worker", "worker-A").Data(t)
		assert.Equal(t, 1, h.channelOpens())
		assert.Equal(t, "/wt/feat", data["path"])
		assert.Equal(t, "worktree is locked", data["removal_blocked_reason"])
		assert.Equal(t, float64(1), data["unpushed_commits"])
		assert.Equal(t, []string{"/wt/feat"}, disp.inspectedPaths)
	})

	t.Run("propagates InspectWorktreeRemoval error", func(t *testing.T) {
		disp := &worktreeTestDispatcher{removalErr: "removal inspection failed"}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		failure := runControl(t, "worktree", "inspect", "/wt/feat").requireFailure(t, cli.CodeInternal)
		assert.Contains(t, failure.Message, "removal inspection failed")
	})

	t.Run("propagates InspectBranchDeletion error", func(t *testing.T) {
		disp := &worktreeTestDispatcher{branchErr: "branch inspection failed"}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		failure := runControl(t, "worktree", "inspect", "/wt/feat").requireFailure(t, cli.CodeInternal)
		assert.Contains(t, failure.Message, "branch inspection failed")
	})
}

func TestRunGitWorktreeInspect_OmittedPathUsesDirFlag(t *testing.T) {
	disp := &worktreeTestDispatcher{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "")

	data := runControl(t, "worktree", "inspect", "--dir", "/wt/feat").Data(t)
	assert.Equal(t, "/wt/feat", data["path"])
	assert.Equal(t, "worktree is locked", data["removal_blocked_reason"])
	assert.Equal(t, float64(1), data["unpushed_commits"])
	assert.Equal(t, []string{"/wt/feat"}, disp.inspectedPaths)
}

func TestWorktreeCreate_OptionalDirUsesTabOrLocalCwdAndRefusesOtherWorker(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	t.Run("defaults --dir to tab working dir", func(t *testing.T) {
		disp := &worktreeTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/repo")

		data := runControl(t, "worktree", "create", "feat/x").Data(t)
		assert.Equal(t, "/wt/feat/x", data["path"])
		require.Len(t, disp.createReqs, 1)
		assert.Equal(t, "/tab/repo", disp.createReqs[0].GetPath())
	})

	t.Run("falls back to local CWD over IPC when tab working dir is unset", func(t *testing.T) {
		disp := &worktreeTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

		data := runControl(t, "worktree", "create", "feat/y").Data(t)
		assert.Equal(t, "/wt/feat/y", data["path"])
		require.Len(t, disp.createReqs, 1)
		assert.Equal(t, cwd, disp.createReqs[0].GetPath())

		getData := runControl(t, "worktree", "get", "branch:feat").Data(t)
		assert.Equal(t, "/wt/feat", getData["path"])
		require.Len(t, disp.getReqs, 1)
		assert.Equal(t, cwd, disp.getReqs[0].GetRepoDir())
	})

	t.Run("refuses omitted --dir when targeting another worker", func(t *testing.T) {
		disp := &worktreeTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A", "worker-B")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/repo")

		f := runControl(t, "worktree", "create", "feat/z", "--worker", "worker-B").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is required when targeting another worker")
		assert.Empty(t, disp.createReqs)
	})
}
