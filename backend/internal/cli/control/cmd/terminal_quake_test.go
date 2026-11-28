package cmd

import (
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
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

type quakeTestDispatcher struct {
	mu           sync.Mutex
	listedDirs   []string
	openedDirs   []string
	panelDirs    []string
	terminalInfo *leapmuxv1.TerminalInfo
}

func (d *quakeTestDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
		var in leapmuxv1.ListTerminalsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.listedDirs = append(d.listedDirs, in.GetQuakeWorkingDirs()...)
		info := d.terminalInfo
		d.mu.Unlock()
		var list []*leapmuxv1.TerminalInfo
		if info != nil {
			list = append(list, info)
		}
		writeProtoResponse(rw, &leapmuxv1.ListTerminalsResponse{Terminals: list})
	case leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure:
		var in leapmuxv1.OpenTerminalRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.openedDirs = append(d.openedDirs, in.GetWorkingDir())
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.OpenTerminalResponse{
			TerminalId: "quake-new-1",
			Title:      "Quake",
		})
	case leapmuxv1connect.WorkerTerminalServiceSendInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.SendInputResponse{})
	case leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure:
		var in leapmuxv1.SetQuakePanelRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.panelDirs = append(d.panelDirs, in.GetWorkingDir())
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.SetQuakePanelResponse{})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestQuakeCommands_OptionalDirResolution(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	t.Run("uses local CWD fallback over IPC when tab working dir is unset", func(t *testing.T) {
		disp := &quakeTestDispatcher{
			terminalInfo: &leapmuxv1.TerminalInfo{
				TerminalId: "quake-1",
				Title:      "Quake",
				WorkingDir: cwd,
				Quake:      true,
				Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
			},
		}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

		data := runControl(t, "terminal", "quake", "get").Data(t)
		assert.Equal(t, "quake-1", data["id"])
		assert.Equal(t, []string{cwd}, disp.listedDirs)
	})

	t.Run("run and send default to tab working dir and reject blank --dir before dialing", func(t *testing.T) {
		disp := &quakeTestDispatcher{}
		hub := &recordingHub{workers: workerRows("worker-A")}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/dir")

		f := runControl(t, "terminal", "quake", "run", "echo hi", "--dir", "   ").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is empty")
		assert.Empty(t, hub.called())

		runData := runControl(t, "terminal", "quake", "run", "echo hi").Data(t)
		assert.Equal(t, "quake-new-1", runData["id"])
		assert.Equal(t, []string{"/tab/dir"}, disp.openedDirs)

		sendData := runControl(t, "terminal", "quake", "send", "ls", "--key", "enter").Data(t)
		assert.Equal(t, "quake-new-1", sendData["id"])
	})

	t.Run("refuses omitted --dir when targeting another worker", func(t *testing.T) {
		disp := &quakeTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A", "worker-B")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/dir")

		for _, args := range [][]string{
			{"terminal", "quake", "get", "--worker", "worker-B"},
			{"terminal", "quake", "run", "pwd", "--worker", "worker-B"},
			{"terminal", "quake", "send", "pwd", "--worker", "worker-B"},
		} {
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "--dir is required when targeting another worker")
		}
		assert.Empty(t, disp.listedDirs)
		assert.Empty(t, disp.openedDirs)
	})
}

func TestQuakeDirOf_AndCanUseLocalCwdWithoutSession(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	parseQuakeOpenInv := func(t *testing.T, flags ...string) *cli.Invocation {
		t.Helper()
		root := NewCommand()
		cmd, _, err := root.Find([]string{"terminal", "quake", "open"})
		require.NoError(t, err)
		require.NoError(t, cmd.ParseFlags(flags))
		return &cli.Invocation{Command: cmd}
	}

	t.Run("local CWD fallback works over tab IPC socket", func(t *testing.T) {
		isolateCLIEnv(t)
		disp := &quakeTestDispatcher{}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

		inv := parseQuakeOpenInv(t)
		assert.True(t, canUseLocalCwdWithoutSession(inv))
		got, err := dirOf(inv)
		require.NoError(t, err)
		assert.Equal(t, cwd, got)

		data := runControl(t, "terminal", "quake", "open").Data(t)
		assert.Equal(t, cwd, data["working_dir"])
		assert.Equal(t, []string{cwd}, disp.panelDirs)
	})

	t.Run("local CWD fallback works over explicit and default local Hub, and is refused over remote Hub", func(t *testing.T) {
		isolateCLIEnv(t)
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))

		explicitLocal := parseQuakeOpenInv(t, "--hub", "unix:/tmp/local-hub.sock")
		assert.True(t, canUseLocalCwdWithoutSession(explicitLocal))
		got, err := dirOf(explicitLocal)
		require.NoError(t, err)
		assert.Equal(t, cwd, got)

		t.Setenv("LEAPMUX_HUB", "unix:/tmp/local-hub.sock")
		defaultLocal := parseQuakeOpenInv(t)
		assert.True(t, canUseLocalCwdWithoutSession(defaultLocal))
		got, err = dirOf(defaultLocal)
		require.NoError(t, err)
		assert.Equal(t, cwd, got)

		remoteInv := parseQuakeOpenInv(t, "--hub", "https://hub.example.test")
		assert.False(t, canUseLocalCwdWithoutSession(remoteInv))
		_, err = dirOf(remoteInv)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		assert.Contains(t, err.Error(), "--dir is required")
	})

	t.Run("local CWD fallback is blocked when context.json saves a Worker", func(t *testing.T) {
		isolateCLIEnv(t)
		t.Setenv(controlenv.WorkingDirVariable, "")
		require.NoError(t, os.Unsetenv(controlenv.WorkingDirVariable))
		require.NoError(t, control.SaveContext(control.ContextConfig{Worker: "worker-remote"}))

		explicitLocal := parseQuakeOpenInv(t, "--hub", "unix:/tmp/local-hub.sock")
		assert.False(t, canUseLocalCwdWithoutSession(explicitLocal))
		_, err := dirOf(explicitLocal)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

		t.Setenv("LEAPMUX_HUB", "unix:/tmp/local-hub.sock")
		defaultLocal := parseQuakeOpenInv(t)
		assert.False(t, canUseLocalCwdWithoutSession(defaultLocal))
		_, err = dirOf(defaultLocal)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})

	t.Run("explicit --worker without --dir returns CodeUsage before dialing", func(t *testing.T) {
		disp := &quakeTestDispatcher{}
		hub := &recordingHub{workers: workerRows("worker-A", "worker-B")}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/dir")

		inv := parseQuakeOpenInv(t, "--worker", "worker-B")
		assert.False(t, canUseLocalCwdWithoutSession(inv))
		_, err := dirOf(inv)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		assert.Contains(t, err.Error(), "--dir is required when targeting another worker")

		f := runControl(t, "terminal", "quake", "open", "--worker", "worker-B").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is required when targeting another worker")
		assert.Empty(t, hub.called())
		assert.Empty(t, disp.panelDirs)
	})

	t.Run("blank --dir and whitespace LEAPMUX_CONTROL_WORKING_DIR fail before dialing", func(t *testing.T) {
		disp := &quakeTestDispatcher{}
		hub := &recordingHub{workers: workerRows("worker-A")}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")
		t.Setenv(controlenv.WorkingDirVariable, "/tab/dir")

		f := runControl(t, "terminal", "quake", "open", "--dir", "   ").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is empty")
		assert.Empty(t, hub.called())
		assert.Empty(t, disp.panelDirs)

		t.Setenv(controlenv.WorkingDirVariable, "  \t ")
		f = runControl(t, "terminal", "quake", "open").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is empty")
		assert.Empty(t, hub.called())
		assert.Empty(t, disp.panelDirs)
	})
}
