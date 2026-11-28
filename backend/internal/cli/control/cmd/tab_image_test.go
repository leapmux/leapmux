package cmd

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

type imageOpenWorker struct {
	mu       sync.Mutex
	register []*leapmuxv1.RegisterTabPayloadRequest
	reveals  []string
}

func (w *imageOpenWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:
		var in leapmuxv1.RegisterTabPayloadRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.register = append(w.register, &in)
		writeProtoResponse(rw, &leapmuxv1.RegisterTabPayloadResponse{
			Publication: &leapmuxv1.PublishedTab{
				Tab: &leapmuxv1.TabIdent{
					TabId:   in.GetCreation().GetTabId(),
					TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE,
				},
				WorkspaceId: in.GetCreation().GetPlacement().GetWorkspaceId(),
				TileId:      in.GetCreation().GetPlacement().GetTileId(),
				Position:    "n",
			},
		})
	case leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure:
		var in leapmuxv1.RevealTabRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.reveals = append(w.reveals, in.GetTabId())
		writeProtoResponse(rw, &leapmuxv1.RevealTabResponse{})
	default:
		_ = rw.SendError(12, "unexpected method: "+req.GetMethod())
	}
}

func TestTabOpenImage_RejectsInvalidWorkspaceAndTileInsteadOfFallback(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "n").
		tabAt("agent-2", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		workspaces:   []*leapmuxv1.Workspace{{Id: "ws-1", Title: "Main"}},
		workers:      workerRows("worker-A"),
		locateTab:    agentTab(),
		materialized: state,
	}
	w := &imageOpenWorker{}
	startSpawnIPC(t, hub, w)

	t.Run("invalid --workspace fails instead of falling back to agent workspace", func(t *testing.T) {
		runControl(t, "tab", "open-image", "agent-2", "42", "--workspace", "title:NoSuchWorkspace").
			requireFailure(t, cli.CodeNotFound)
		assert.Empty(t, w.register, "must not register a tab after explicit --workspace resolution fails")
	})

	t.Run("invalid --tile fails instead of falling back to agent tile", func(t *testing.T) {
		runControl(t, "tab", "open-image", "agent-2", "42", "--tile", "zzzz").
			requireFailure(t, cli.CodeNotFound)
		assert.Empty(t, w.register, "must not register a tab after explicit --tile resolution fails")
	})
}

func TestTabOpenImage_FocusSharesSingleWorkerChannel(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "n").
		tabAt("agent-2", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		workspaces:   []*leapmuxv1.Workspace{{Id: "ws-1", Title: "Main"}},
		workers:      workerRows("worker-A"),
		locateTab:    agentTab(),
		materialized: state,
	}
	w := &imageOpenWorker{}
	h := startHubChannelHarness(t, hub, w)
	h.dispatcher.Handle(leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, channel.Registration{}, w.DispatchWith)

	data := runControl(t, "tab", "open-image", "agent-2", "42", "--focus", "--hub", h.url).Data(t)
	assert.Equal(t, "image", data["type"])
	assert.Equal(t, "Image #42", data["title"])
	assert.Equal(t, "ws-1", data["workspace_id"])
	assert.Equal(t, "worker-A", data["worker_id"])
	assert.Equal(t, "root-1", data["tile_id"])

	require.Len(t, w.register, 1)
	assert.Equal(t, "agent-2", w.register[0].GetPayload().GetImage().GetAgentId())
	assert.Equal(t, int64(42), w.register[0].GetPayload().GetImage().GetSeq())
	require.Len(t, w.reveals, 1)
	assert.Equal(t, data["id"], w.reveals[0])
	assert.Equal(t, 1, h.channelOpens(), "RegisterTabPayload and RevealTab must share one E2EE channel")
}
