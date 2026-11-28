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

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// gitReadDispatcher answers ReadGitFile with resp, and records each request
// that it decodes. Every other method fails.
type gitReadDispatcher struct {
	resp *leapmuxv1.ReadGitFileResponse

	mu       sync.Mutex
	requests []*leapmuxv1.ReadGitFileRequest
}

func (d *gitReadDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerGitServiceReadGitFileProcedure {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	var r leapmuxv1.ReadGitFileRequest
	if err := proto.Unmarshal(req.GetPayload(), &r); err != nil {
		_ = w.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	d.mu.Lock()
	d.requests = append(d.requests, &r)
	d.mu.Unlock()
	payload, err := proto.Marshal(d.resp)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
}

func (d *gitReadDispatcher) received() []*leapmuxv1.ReadGitFileRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*leapmuxv1.ReadGitFileRequest(nil), d.requests...)
}

// `git read --limit` reaches the worker as the limit of the request, and
// without the flag the request carries 0, which the worker reads as its
// default. The output carries total_size beside the content, so a reader sees
// where the limit cut the file.
func TestRunGitRead_SendsTheLimitAndEmitsTheTotalSize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		limit int64
	}{
		{name: "no flag sends the default", limit: 0},
		{name: "the flag sends its value", flags: []string{"--limit", "4"}, limit: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disp := &gitReadDispatcher{resp: &leapmuxv1.ReadGitFileResponse{
				Path:      "/repo/tracked.txt",
				Content:   []byte("0123"),
				Exists:    true,
				TotalSize: 11,
			}}
			startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)

			o := runControl(t, append([]string{
				"git", "read", "/repo/tracked.txt", "--worker", "worker-A", "--ref", "staged",
			}, tc.flags...)...)
			require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
			assert.Equal(t, map[string]any{
				"ref":        "staged",
				"path":       "/repo/tracked.txt",
				"exists":     true,
				"content":    "0123",
				"total_size": float64(11),
			}, o.Data(t))

			requests := disp.received()
			require.Len(t, requests, 1)
			assert.Equal(t, tc.limit, requests[0].GetLimit())
			assert.Equal(t, leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED, requests[0].GetRef())
			assert.Equal(t, "/repo/tracked.txt", requests[0].GetPath())
		})
	}
}

// A file that does not exist at the ref and an empty file both carry no
// content, so only `exists` tells a reader which one it read.
func TestRunGitRead_EmitsWhetherTheFileExistsAtTheRef(t *testing.T) {
	disp := &gitReadDispatcher{resp: &leapmuxv1.ReadGitFileResponse{
		Path:   "/repo/untracked.txt",
		Exists: false,
	}}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)

	o := runControl(t, "git", "read", "/repo/untracked.txt", "--worker", "worker-A")
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	assert.Equal(t, map[string]any{
		"ref":        "head",
		"path":       "/repo/untracked.txt",
		"exists":     false,
		"content":    "",
		"total_size": float64(0),
	}, o.Data(t))
}

func TestGitCommands_ValidateArgumentsBeforeDialing(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "git read empty path", args: []string{"git", "read", ""}},
		{name: "git read blank path", args: []string{"git", "read", "   "}},
		{name: "git checkout blank branch", args: []string{"git", "checkout", " ", "--dir", "/repo"}},
		{name: "git checkout blank dir", args: []string{"git", "checkout", "main", "--dir", " "}},
		{name: "git branch create blank name", args: []string{"git", "branch", "create", " ", "--dir", "/repo"}},
		{name: "git branch create blank base", args: []string{"git", "branch", "create", "feat", "--base", " ", "--dir", "/repo"}},
		{name: "git branch delete blank name", args: []string{"git", "branch", "delete", " ", "--switch-to", "main", "--dir", "/repo"}},
		{name: "git branch delete blank switch-to", args: []string{"git", "branch", "delete", "foo", "--switch-to", " ", "--dir", "/repo"}},
		{name: "git push blank dir", args: []string{"git", "push", "--dir", " "}},
		{name: "git diff negative limit", args: []string{"git", "diff", "--limit", "-1", "--dir", "/repo"}},
		{name: "git log zero limit", args: []string{"git", "log", "--limit", "0", "--dir", "/repo"}},
		{name: "git log limit above max", args: []string{"git", "log", "--limit", "201", "--dir", "/repo"}},
		{name: "git commit blank message", args: []string{"git", "commit", "-m", "   ", "--dir", "/repo"}},
		{name: "git commit blank dir", args: []string{"git", "commit", "-m", "checkpoint", "--dir", "   "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A")}
			disp := &gitTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")

			runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Empty(t, hub.called(), "local validation must fail before dialing the hub")
			assert.Empty(t, disp.calledMethods(), "local validation must fail before dialing the worker")
		})
	}

	t.Run("git commit whitespace LEAPMUX_CONTROL_WORKING_DIR fails before dialing", func(t *testing.T) {
		hub := &recordingHub{workers: workerRows("worker-A")}
		disp := &gitTestDispatcher{}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "   \t  ")

		f := runControl(t, "git", "commit", "-m", "checkpoint").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is empty")
		assert.Empty(t, hub.called(), "local validation must fail before dialing the hub")
		assert.Empty(t, disp.calledMethods(), "local validation must fail before dialing the worker")
	})
}

type gitTestDispatcher struct {
	mu           sync.Mutex
	methods      []string
	checkoutReqs []*leapmuxv1.CheckoutBranchRequest
	deleteReqs   []*leapmuxv1.DeleteBranchRequest
	pushReqs     []*leapmuxv1.PushBranchRequest
	commitReqs   []*leapmuxv1.GitCommitRequest
	statusPaths  []string
	infoArrived  chan struct{}
	filesArrived chan struct{}
}

func (d *gitTestDispatcher) calledMethods() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

func (d *gitTestDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	switch req.GetMethod() {
	case leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure:
		var in leapmuxv1.CheckoutBranchRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.checkoutReqs = append(d.checkoutReqs, &in)
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.CheckoutBranchResponse{})
	case leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure:
		var in leapmuxv1.DeleteBranchRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.deleteReqs = append(d.deleteReqs, &in)
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.DeleteBranchResponse{})
	case leapmuxv1connect.WorkerGitServicePushBranchProcedure:
		var in leapmuxv1.PushBranchRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.pushReqs = append(d.pushReqs, &in)
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.PushBranchResponse{})
	case leapmuxv1connect.WorkerGitServiceGitCommitProcedure:
		var in leapmuxv1.GitCommitRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.commitReqs = append(d.commitReqs, &in)
		d.mu.Unlock()
		writeProtoResponse(w, &leapmuxv1.GitCommitResponse{Hash: "abc1234", Subject: in.GetMessage()})
	case leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure:
		writeProtoResponse(w, &leapmuxv1.GetGitDiffResponse{
			Diff:      "+line\n",
			Truncated: false,
			TotalSize: 6,
		})
	case leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure:
		var in leapmuxv1.GetGitInfoRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.statusPaths = append(d.statusPaths, in.GetPath())
		d.mu.Unlock()
		if d.infoArrived != nil && d.filesArrived != nil {
			close(d.infoArrived)
			select {
			case <-d.filesArrived:
			case <-ctx.Done():
				_ = w.SendError(int32(codes.Canceled), ctx.Err().Error())
				return
			}
		}
		writeProtoResponse(w, &leapmuxv1.GetGitInfoResponse{
			IsGitRepo:        true,
			RepoRoot:         "/repo",
			MainWorktreeRoot: "/repo",
			CurrentBranch:    "main",
		})
	case leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure:
		if d.infoArrived != nil && d.filesArrived != nil {
			close(d.filesArrived)
			select {
			case <-d.infoArrived:
			case <-ctx.Done():
				_ = w.SendError(int32(codes.Canceled), ctx.Err().Error())
				return
			}
		}
		writeProtoResponse(w, &leapmuxv1.GetGitFileStatusResponse{
			RepoRoot: "/repo",
			Status: &leapmuxv1.GitRepoStatus{
				Branch:   "main",
				Toplevel: "/repo",
				Ahead:    1,
			},
			Files: []*leapmuxv1.GitFileStatusEntry{
				{Path: "main.go", UnstagedStatus: leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED},
			},
		})
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestGitWrite_PreservesLiteralDirAndTrimsBranch(t *testing.T) {
	disp := &gitTestDispatcher{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	data := runControl(t, "git", "checkout", " main ", "--dir", "/repo/literal dir ").Data(t)
	assert.Equal(t, "/repo/literal dir ", data["dir"])
	assert.Equal(t, true, data["ok"])
	require.Len(t, disp.checkoutReqs, 1)
	assert.Equal(t, "/repo/literal dir ", disp.checkoutReqs[0].GetPath())
	assert.Equal(t, "main", disp.checkoutReqs[0].GetBranch())
}

func TestGitDiff_JSONEmitsFalseTruncated(t *testing.T) {
	disp := &gitTestDispatcher{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	out := runControl(t, "git", "diff", "--dir", "/repo")
	data := out.Data(t)
	truncated, hasKey := data["truncated"]
	require.True(t, hasKey, "truncated must be present in JSON output: %s", out.Stdout)
	assert.Equal(t, false, truncated)
}

func TestRunGitStatus_RunsBothRPCsConcurrentlyOnOneChannel(t *testing.T) {
	disp := &gitTestDispatcher{
		infoArrived:  make(chan struct{}),
		filesArrived: make(chan struct{}),
	}
	hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
	h := startHubChannelHarness(t, hub, disp)
	h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, channel.Registration{}, disp.DispatchWith)
	h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, channel.Registration{}, disp.DispatchWith)

	data := runControl(t, "git", "status", "--hub", h.url, "--worker", "worker-A", "--dir", "/repo").Data(t)
	assert.Equal(t, 1, h.channelOpens())
	assert.Equal(t, "main", data["branch"])
	assert.Equal(t, float64(1), data["ahead"])
	files, ok := data["files"].([]any)
	require.True(t, ok)
	require.Len(t, files, 1)
}

func TestGitReadCommands_DoNotUseTabWorkingDirForAnotherWorkerOverIPC(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "git status", args: []string{"git", "status", "--worker", "worker-B"}},
		{name: "git diff", args: []string{"git", "diff", "--worker", "worker-B"}},
		{name: "git log", args: []string{"git", "log", "--worker", "worker-B"}},
		{name: "git branch list", args: []string{"git", "branch", "list", "--worker", "worker-B"}},
		{name: "git branch inspect", args: []string{"git", "branch", "inspect", "--worker", "worker-B"}},
		{name: "worktree list", args: []string{"worktree", "list", "--worker", "worker-B"}},
		{name: "worktree inspect", args: []string{"worktree", "inspect", "--worker", "worker-B"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
			disp := &gitTestDispatcher{}
			startSpawnIPC(t, hub, disp)
			t.Setenv(controlenv.WorkerIDVariable, "worker-A")
			t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

			failure := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, failure.Message, "--dir is required when targeting another worker")
			assert.Empty(t, disp.calledMethods())
		})
	}
}

func TestGitReadCommands_CheckGitDirInputBeforeDialing(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{name: "git status", args: []string{"git", "status"}},
		{name: "git diff", args: []string{"git", "diff"}},
		{name: "git log", args: []string{"git", "log"}},
		{name: "git branch list", args: []string{"git", "branch", "list"}},
		{name: "git branch inspect", args: []string{"git", "branch", "inspect"}},
		{name: "worktree list", args: []string{"worktree", "list"}},
		{name: "worktree inspect", args: []string{"worktree", "inspect"}},
	}

	for _, cmdCase := range commands {
		t.Run(cmdCase.name, func(t *testing.T) {
			t.Run("explicit blank --dir overrides env", func(t *testing.T) {
				hub := &recordingHub{workers: workerRows("worker-A")}
				disp := &gitTestDispatcher{}
				startSpawnIPC(t, hub, disp)
				t.Setenv(controlenv.WorkerIDVariable, "worker-A")
				t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

				args := append(append([]string(nil), cmdCase.args...), "--dir", "  ")
				failure := runControl(t, args...).requireFailure(t, cli.CodeUsage)
				assert.Contains(t, failure.Message, "--dir is empty")
				assert.Empty(t, hub.called())
				assert.Empty(t, disp.calledMethods())
			})

			t.Run("whitespace-only env with no --dir", func(t *testing.T) {
				hub := &recordingHub{workers: workerRows("worker-A")}
				disp := &gitTestDispatcher{}
				startSpawnIPC(t, hub, disp)
				t.Setenv(controlenv.WorkerIDVariable, "worker-A")
				t.Setenv(controlenv.WorkingDirVariable, "   ")

				failure := runControl(t, cmdCase.args...).requireFailure(t, cli.CodeUsage)
				assert.Contains(t, failure.Message, "--dir is empty")
				assert.Empty(t, hub.called())
				assert.Empty(t, disp.calledMethods())
			})

			t.Run("unset env with no --dir over remote hub fails before dialing", func(t *testing.T) {
				disp := &gitTestDispatcher{}
				hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
				h := startHubChannelHarness(t, hub, disp)
				t.Setenv(controlenv.WorkingDirVariable, "")
				require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

				args := append(append([]string(nil), cmdCase.args...), "--hub", h.url, "--worker", "worker-A")
				failure := runControl(t, args...).requireFailure(t, cli.CodeUsage)
				assert.Contains(t, failure.Message, "--dir is required")
				assert.Empty(t, hub.called())
				assert.Empty(t, disp.calledMethods())
			})

			t.Run("explicit remote --hub inside a tab fails before dialing and never leaks tab dir", func(t *testing.T) {
				disp := &gitTestDispatcher{}
				hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
				h := startHubChannelHarness(t, hub, disp)
				t.Setenv(controlenv.SocketVariable, "unix:/tmp/tab-sock")
				t.Setenv(controlenv.TokenVariable, "tok")
				t.Setenv(controlenv.WorkerIDVariable, "worker-A")
				t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

				args := append(append([]string(nil), cmdCase.args...), "--hub", h.url, "--worker", "worker-A")
				failure := runControl(t, args...).requireFailure(t, cli.CodeUsage)
				assert.Contains(t, failure.Message, "--dir is required")
				assert.Empty(t, hub.called())
				assert.Empty(t, disp.calledMethods())
			})

			t.Run("explicit --worker outside a tab fails before dialing", func(t *testing.T) {
				disp := &gitTestDispatcher{}
				hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
				h := startLocalHubChannelHarness(t, hub, disp)
				t.Setenv(controlenv.WorkingDirVariable, "")
				require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

				args := append(append([]string(nil), cmdCase.args...), "--hub", h.url, "--worker", "worker-A")
				failure := runControl(t, args...).requireFailure(t, cli.CodeUsage)
				assert.Contains(t, failure.Message, "--dir is required")
				assert.Empty(t, hub.called())
				assert.Empty(t, disp.calledMethods())
			})
		})
	}
}

func TestGitCommands_OptionalDirLocalCwdAndWriteCommands(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	t.Run("git status falls back to local CWD over IPC when env var is unset", func(t *testing.T) {
		disp := &gitTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

		data := runControl(t, "git", "status").Data(t)
		assert.Equal(t, "main", data["branch"])
		assert.Equal(t, []string{cwd}, disp.statusPaths)
	})

	t.Run("git write commands default --dir to tab working dir or local CWD", func(t *testing.T) {
		disp := &gitTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/repo")

		out := runControl(t, "git", "checkout", "main").Data(t)
		assert.Equal(t, "/tab/repo", out["dir"])
		require.Len(t, disp.checkoutReqs, 1)
		assert.Equal(t, "/tab/repo", disp.checkoutReqs[0].GetPath())

		out = runControl(t, "git", "push").Data(t)
		assert.Equal(t, "/tab/repo", out["dir"])
		require.Len(t, disp.pushReqs, 1)
		assert.Equal(t, "/tab/repo", disp.pushReqs[0].GetWorkingDir())

		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))
		commitData := runControl(t, "git", "commit", "-m", "checkpoint").Data(t)
		assert.Equal(t, "abc1234", commitData["hash"])
		require.Len(t, disp.commitReqs, 1)
		assert.Equal(t, cwd, disp.commitReqs[0].GetPath())
	})

	t.Run("git write commands refuse omitted --dir when targeting another worker", func(t *testing.T) {
		disp := &gitTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A", "worker-B")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/repo")

		for _, args := range [][]string{
			{"git", "checkout", "main", "--worker", "worker-B"},
			{"git", "push", "--worker", "worker-B"},
			{"git", "commit", "-m", "msg", "--worker", "worker-B"},
			{"git", "branch", "delete", "feat", "--worker", "worker-B"},
		} {
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "--dir is required when targeting another worker")
		}
		assert.Empty(t, disp.calledMethods())
	})
}

func TestGitBranchDelete_OptionalSwitchTo(t *testing.T) {
	t.Run("omitted --switch-to sends empty SwitchToBranch in one RPC", func(t *testing.T) {
		disp := &gitTestDispatcher{}
		hub := &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure, channel.Registration{}, disp.DispatchWith)

		data := runControl(t, "git", "branch", "delete", "feature/old", "--hub", h.url, "--worker", "worker-A", "--dir", "/repo").Data(t)
		assert.Equal(t, true, data["ok"])
		assert.Equal(t, 1, h.channelOpens())
		assert.Equal(t, []string{leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure}, disp.calledMethods(),
			"branch deletion must delegate optional switch-to resolution to the worker without calling ListGitBranches")
		require.Len(t, disp.deleteReqs, 1)
		assert.Equal(t, "/repo", disp.deleteReqs[0].GetPath())
		assert.Equal(t, "feature/old", disp.deleteReqs[0].GetBranchToDelete())
		assert.Equal(t, "", disp.deleteReqs[0].GetSwitchToBranch())
	})

	t.Run("explicit --switch-to passes the requested branch", func(t *testing.T) {
		disp := &gitTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/repo")

		data := runControl(t, "git", "branch", "delete", "feature/old", "--switch-to", "main").Data(t)
		assert.Equal(t, true, data["ok"])
		assert.Equal(t, []string{leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure}, disp.calledMethods())
		require.Len(t, disp.deleteReqs, 1)
		assert.Equal(t, "feature/old", disp.deleteReqs[0].GetBranchToDelete())
		assert.Equal(t, "main", disp.deleteReqs[0].GetSwitchToBranch())
	})
}

func TestGitDiffResult_RenderText(t *testing.T) {
	t.Run("truncated diff appends truncation notice after ensuring newline", func(t *testing.T) {
		var buf bytes.Buffer
		err := (gitDiffResult{
			Diff:      "+line",
			Truncated: true,
			TotalSize: 120,
		}).RenderText(&buf)
		require.NoError(t, err)
		assert.Equal(t, "+line\n\n(diff truncated at 5 of 120 bytes; pass --limit to show more)\n", buf.String())
	})

	t.Run("non-truncated diff omits truncation notice", func(t *testing.T) {
		var buf bytes.Buffer
		err := (gitDiffResult{
			Diff:      "+line\n",
			Truncated: false,
			TotalSize: 6,
		}).RenderText(&buf)
		require.NoError(t, err)
		assert.Equal(t, "+line\n", buf.String())
	})
}
