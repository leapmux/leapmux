package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

type compiledGitCall struct {
	worker string
	owner  userid.UserID
	method string
	wire   []byte
}

type compiledGitPeer struct{ calls chan compiledGitCall }

func (p compiledGitPeer) CallInner(_ context.Context, worker string, owner userid.UserID, req *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	p.calls <- compiledGitCall{worker: worker, owner: owner, method: req.GetMethod(), wire: req.GetPayload()}
	return nil, nil
}

func (compiledGitPeer) StreamInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest, func(*leapmuxv1.InnerStreamMessage) error, func(channel.StreamController)) error {
	return fmt.Errorf("unexpected git stream")
}

type compiledGitHub struct{ calls chan compiledGitCall }

func (h compiledGitHub) CallInner(_ context.Context, owner userid.UserID, method string, wire []byte) ([]byte, error) {
	h.calls <- compiledGitCall{owner: owner, method: method, wire: wire}
	return proto.Marshal(&leapmuxv1.GetWorkerResponse{})
}

func TestMainControlGitWritesSendExactRequestsToTheSelectedWorker(t *testing.T) {
	t.Parallel()
	const localWorker = "local-worker"
	targetWorker := id.Generate()
	const dir = "/worker/src/literal repo "
	owner := userid.MustNew("git-test-owner")
	peer := compiledGitPeer{calls: make(chan compiledGitCall, 16)}
	hub := compiledGitHub{calls: make(chan compiledGitCall, 16)}
	url := locallistentest.UniqueListenURL(t, "leapmux-cli-git")
	token := controlipc.MintToken()
	server, err := controlipc.Listen(controlipc.Options{
		SocketURL: url, Token: token,
		TokenInfo: controlipc.TokenInfo{UserID: owner, WorkerID: localWorker},
		Router:    &controlipc.Router{WorkerID: localWorker, UserID: owner, Hub: hub, CrossWorker: peer},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	env := []string{
		"LEAPMUX_HUB=",
		controlenv.CLIVariable + "=",
		controlenv.SocketVariable + "=" + url,
		controlenv.TokenVariable + "=" + token,
		controlenv.WorkerIDVariable + "=" + localWorker,
		controlenv.WorkingDirVariable + "=/caller/other-repo",
	}
	for _, test := range []struct {
		name   string
		args   []string
		method string
		body   proto.Message
	}{
		{"checkout", []string{"checkout", "origin/feature"}, leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure,
			&leapmuxv1.CheckoutBranchRequest{Path: dir, Branch: "origin/feature"}},
		{"create with base", []string{"branch", "create", "release/topic", "--base", "origin/release"}, leapmuxv1connect.WorkerGitServiceCreateBranchProcedure,
			&leapmuxv1.CreateBranchRequest{Path: dir, NewBranch: "release/topic", BaseBranch: "origin/release"}},
		{"create from HEAD", []string{"branch", "create", "topic"}, leapmuxv1connect.WorkerGitServiceCreateBranchProcedure,
			&leapmuxv1.CreateBranchRequest{Path: dir, NewBranch: "topic"}},
		{"delete", []string{"branch", "delete", "release/topic", "--switch-to", "stable"}, leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure,
			&leapmuxv1.DeleteBranchRequest{Path: dir, BranchToDelete: "release/topic", SwitchToBranch: "stable"}},
		{"push", []string{"push"}, leapmuxv1connect.WorkerGitServicePushBranchProcedure,
			&leapmuxv1.PushBranchRequest{WorkingDir: dir}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--json", "git"}, test.args...)
			args = append(args, "--dir", dir, "--worker", "id:"+targetWorker)
			code, stdout, stderr := runMain(t, args, env...)
			require.Zero(t, code, stderr)
			assert.JSONEq(t, fmt.Sprintf(`{"data":{"ok":true,"dir":%q}}`, dir), stdout)
			require.Len(t, peer.calls, 1)
			require.Len(t, hub.calls, 1)
			want, err := proto.Marshal(test.body)
			require.NoError(t, err)
			assert.Equal(t, compiledGitCall{worker: targetWorker, owner: owner, method: test.method, wire: want}, <-peer.calls)
			preflight, err := proto.Marshal(&leapmuxv1.GetWorkerRequest{WorkerId: targetWorker})
			require.NoError(t, err)
			assert.Equal(t, compiledGitCall{owner: owner, method: leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, wire: preflight}, <-hub.calls)
		})
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing directory", []string{"checkout", "main"}},
		{"blank directory", []string{"push", "--dir", "  "}},
		{"missing switch target", []string{"branch", "delete", "topic", "--dir", dir}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--json", "git"}, test.args...)
			code, stdout, stderr := runMain(t, args, env...)
			assert.Equal(t, cli.ExitUsage, code, "stdout: %s\nstderr: %s", stdout, stderr)
			assert.Empty(t, peer.calls)
			assert.Empty(t, hub.calls)
		})
	}
}
