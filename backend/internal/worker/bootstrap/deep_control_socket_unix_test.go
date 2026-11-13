//go:build unix

package bootstrap

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireDeepDataDirectoryRetainsAgentAndTerminalControl(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), strings.Repeat("deep", 30))
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	ignoredTemp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", ignoredTemp)
	t.Setenv("TMPDIR", ignoredTemp)
	wiring, _, _ := wireForTestWithKeypair(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.DataDir = dataDir })
	factory, ok := wiring.Service.ControlIPC.(*controlipc.Factory)
	require.True(t, ok)
	require.Greater(t, len(strings.TrimPrefix(controlipc.SocketURL(controlipc.SocketDir(dataDir), "worker-1", controlipc.SocketKindAgent, "12345678"), "unix:")), locallisten.MaxUnixSocketPathBytes())
	// Cleanup removes only this test's selected namespace after Wiring closes it.
	t.Cleanup(func() {
		wiring.Shutdown()
		if factory.SocketDir != controlipc.SocketDir(dataDir) {
			require.NoError(t, os.RemoveAll(factory.SocketDir))
		}
	})
	agent, agentCleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: "agent-one"})
	require.NoError(t, err, "a deep data directory must retain the agent control endpoint")
	t.Cleanup(agentCleanup)
	terminal, terminalCleanup, err := factory.TerminalSpawning(service.TerminalSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-1", TabID: "term-one"})
	require.NoError(t, err, "a deep data directory must retain the terminal control endpoint")
	t.Cleanup(terminalCleanup)
	var sockets []string
	for _, envs := range [][]string{agent, terminal} {
		var socket, token string
		for _, entry := range envs {
			if strings.HasPrefix(entry, "LEAPMUX_CONTROL_SOCK=") {
				socket = strings.TrimPrefix(entry, "LEAPMUX_CONTROL_SOCK=")
			}
			if strings.HasPrefix(entry, "LEAPMUX_CONTROL_TOKEN=") {
				token = strings.TrimPrefix(entry, "LEAPMUX_CONTROL_TOKEN=")
			}
		}
		require.NotEmpty(t, socket)
		require.NotEmpty(t, token)
		assert.False(t, strings.HasPrefix(socket, "unix:"+ignoredTemp+"/"))
		endpoint, err := hubtransport.New(socket)
		require.NoError(t, err)
		t.Cleanup(endpoint.CloseIdleConnections)
		client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(endpoint.UnaryClient(30*time.Second), endpoint.BaseURL(), connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
		ctx, info := connect.NewClientContext(t.Context())
		info.RequestHeader().Set(controlipc.AuthHeader, token)
		who, err := client.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		require.NoError(t, err)
		assert.Equal(t, "user-1", who.GetUserId())
		sockets = append(sockets, strings.TrimPrefix(socket, "unix:"))
	}
	wiring.Shutdown()
	for _, socket := range sockets {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "Wiring.Shutdown must close every retained control listener")
	}
}
