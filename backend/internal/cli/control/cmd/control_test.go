package cmd

import (
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestWorkerScope_WhitespaceEnvAndAmbientTabFallback(t *testing.T) {
	t.Run("whitespace-only LEAPMUX_CONTROL_WORKER_ID falls back to sole online worker", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.WorkerIDVariable, "   \n\t  ")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
	})

	t.Run("blank worker id with ambient LEAPMUX_CONTROL_TAB_ID derives worker from calling tab", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			// Two online workers ensure autoSelectSoleWorker cannot guess.
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
				{Id: "worker-B", Online: true},
			},
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-self"},
				TileId:      "tile-1",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-1",
			},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.WorkerIDVariable, "  ")
		t.Setenv(controlenv.TabIDVariable, "tab-self")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
		assert.Contains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	})

	t.Run("unlocatable ambient LEAPMUX_CONTROL_TAB_ID falls back to sole online worker", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers:        []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
			unlocatableTab: true,
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.TabIDVariable, "quake-1")

		out := runControl(t, "worker", "info").ok(t)
		assert.Equal(t, "worker-A", envelopeData(t, out)["id"])
	})

	t.Run("hub outage on ambient LEAPMUX_CONTROL_TAB_ID surfaces the error", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		hub := &recordingHub{
			workers:      []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
			locateTabErr: connect.NewError(connect.CodeUnavailable, "hub unreachable"),
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(controlenv.TabIDVariable, "tab-self")

		f := runControl(t, "worker", "info").requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "hub unreachable")
	})
}

func TestWorkerWatchTransport_Call(t *testing.T) {
	t.Run("hub mode reuses the open E2EE channel without a second handshake", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		harness := startHubChannelHarness(t, &recordingHub{}, disp)
		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)
		_, isHub := peer.(*control.HubClient)
		require.True(t, isHub)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()
		require.Equal(t, 1, harness.channelOpens())

		var first, second leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &first))
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &second))
		assert.Equal(t, "build-box", first.GetName())
		assert.Equal(t, "build-box", second.GetName())
		assert.Equal(t, 1, harness.channelOpens(), "Call must reuse the transport's open E2EE channel")
	})

	t.Run("hub mode preserves in-band worker error codes", func(t *testing.T) {
		harness := startHubChannelHarness(t, &recordingHub{}, inBandErrorDispatcher{
			code:    codes.NotFound,
			message: "no such agent",
		})
		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()

		var resp leapmuxv1.ListAgentsResponse
		err = wt.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, &resp)
		require.Error(t, err)
		var coded *codedRPCError
		require.True(t, errors.As(err, &coded))
		assert.Equal(t, "rpc_error", coded.Code)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
		assert.Equal(t, 1, harness.channelOpens())
	})

	t.Run("local IPC mode routes unary calls and preserves in-band errors", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		startSpawnIPC(t, &recordingHub{}, disp)
		peer, err := control.NewClientFromEnv("")
		require.NoError(t, err)
		_, isIPC := peer.(*control.WorkerIPCClient)
		require.True(t, isIPC)

		wt, err := newWorkerWatchTransport(t.Context(), peer, "worker-A")
		require.NoError(t, err)
		defer wt.close()

		var resp leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, wt.Call(t.Context(), leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp))
		assert.Equal(t, "build-box", resp.GetName())

		// Verify in-band refusal over local IPC as well.
		startSpawnIPC(t, &recordingHub{}, inBandErrorDispatcher{code: codes.PermissionDenied, message: "denied"})
		peerDenied, err := control.NewClientFromEnv("")
		require.NoError(t, err)
		wtDenied, err := newWorkerWatchTransport(t.Context(), peerDenied, "worker-A")
		require.NoError(t, err)
		defer wtDenied.close()

		err = wtDenied.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, &leapmuxv1.ListAgentsResponse{})
		require.Error(t, err)
		assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	})

	t.Run("nil receiver fails with usage error without panicking", func(t *testing.T) {
		var nilTransport *workerWatchTransport
		err := nilTransport.Call(t.Context(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{}, nil)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}
