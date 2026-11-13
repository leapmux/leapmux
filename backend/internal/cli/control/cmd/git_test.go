package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
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

			var runErr error
			out := withCapturedStdout(t, func() {
				runErr = RunGitRead(fakeCmdCtx{}, append([]string{
					"--path", "/repo/tracked.txt", "--worker-id", "worker-A", "--ref", "staged",
				}, tc.flags...))
			})
			require.NoError(t, runErr, "stdout: %s", out)

			var env struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
			assert.Equal(t, map[string]any{
				"ref":        "staged",
				"path":       "/repo/tracked.txt",
				"exists":     true,
				"content":    "0123",
				"total_size": float64(11),
			}, env.Data)

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

	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunGitRead(fakeCmdCtx{}, []string{"--path", "/repo/untracked.txt", "--worker-id", "worker-A"})
	})
	require.NoError(t, runErr, "stdout: %s", out)

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
	assert.Equal(t, map[string]any{
		"ref":        "head",
		"path":       "/repo/untracked.txt",
		"exists":     false,
		"content":    "",
		"total_size": float64(0),
	}, env.Data)
}
