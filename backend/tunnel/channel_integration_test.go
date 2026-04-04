package tunnel_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/tunnel"
)

// startTestSolo starts a solo Hub+Worker instance for integration testing.
// Returns the hub URL, socket path, admin token, admin user ID, and worker ID.
func startTestSolo(t *testing.T) (hubURL, socketPath, userID, workerID string) {
	t.Helper()

	// Use a short path under /tmp to stay within the 104-byte macOS Unix
	// socket path limit. The solo instance creates hub.sock inside dataDir.
	dataDir, err := os.MkdirTemp("", "lm")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Find a free port.
	ln, listenErr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, listenErr)
	addr := ln.Addr().String()
	_ = ln.Close()

	inst, err := solo.Start(ctx, solo.Config{
		Addr:       addr,
		ConfigDir:  dataDir,
		ConfigFile: dataDir + "/solo.yaml",
		SkipBanner: true,
	})
	require.NoError(t, err)
	t.Cleanup(inst.Stop)

	hubURL = "http://" + addr
	socketPath = inst.Server().SocketPath()

	// Wait for Hub to be ready.
	require.NoError(t, waitForHTTP(hubURL, 30*time.Second))

	// Solo mode auto-authenticates all requests — no login needed.
	// Token is empty; the auth interceptor auto-attaches the solo user.

	// Get user ID via auto-authenticated request.
	httpClient := &http.Client{Timeout: 10 * time.Second}
	authClient := leapmuxv1connect.NewAuthServiceClient(httpClient, hubURL)

	meResp, err := authClient.GetCurrentUser(ctx, connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	userID = meResp.Msg.GetUser().GetId()
	require.NotEmpty(t, userID)

	// Get worker ID by listing workers (poll until online).
	mgmtClient := leapmuxv1connect.NewWorkerManagementServiceClient(httpClient, hubURL)
	var wID string
	for i := 0; i < 60; i++ {
		listResp, listErr := mgmtClient.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
			OrgId: meResp.Msg.GetUser().GetOrgId(),
		}))
		if listErr == nil {
			for _, w := range listResp.Msg.GetWorkers() {
				if w.GetOnline() {
					wID = w.GetId()
					break
				}
			}
		}
		if wID != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NotEmpty(t, wID, "worker did not come online in time")

	return hubURL, socketPath, userID, wID
}

func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("server at %s not ready after %v", url, timeout)
}

func TestChannelOpenAndCallRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, userID, workerID := startTestSolo(t)
	ctx := context.Background()

	// Open an E2EE channel.
	ch, err := tunnel.OpenChannel(ctx, hubURL, userID, workerID, nil)
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Call GetWorkerSystemInfo to verify the channel works.
	payload, err := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
	require.NoError(t, err)

	resp, err := ch.CallRPC("GetWorkerSystemInfo", payload)
	require.NoError(t, err)

	var sysInfo leapmuxv1.GetWorkerSystemInfoResponse
	require.NoError(t, proto.Unmarshal(resp.GetPayload(), &sysInfo))
	assert.NotEmpty(t, sysInfo.GetOs())
	assert.NotEmpty(t, sysInfo.GetArch())
}

func TestChannelTunnelEchoFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	echoAddr := testutil.StartEchoServer(t)
	echoHost, echoPort := testutil.ParseAddr(echoAddr)

	hubURL, _, userID, workerID := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(ctx, hubURL, userID, workerID, nil)
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Open a tunnel connection to the echo server.
	openPayload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: echoHost,
		TargetPort: echoPort,
	})
	require.NoError(t, err)

	// Use SendRPCNoWait + RegisterPending so we can also register for stream events.
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := ch.SendRPCNoWait("OpenTunnelConn", openPayload)
	require.NoError(t, err)
	ch.RegisterPending(reqID, respCh)

	// Wait for the unary response.
	select {
	case resp := <-respCh:
		require.NotNil(t, resp)
		require.False(t, resp.GetIsError(), "expected success, got error: %s", resp.GetErrorMessage())

		var openResp leapmuxv1.OpenTunnelConnResponse
		require.NoError(t, proto.Unmarshal(resp.GetPayload(), &openResp))
		connID := openResp.GetConnId()
		require.NotEmpty(t, connID)

		ch.UnregisterPending(reqID)

		// Register for stream events (Worker → us).
		dataCh := make(chan []byte, 16)
		eofCh := make(chan struct{}, 1)
		ch.RegisterStream(reqID, func(msg *leapmuxv1.InnerStreamMessage) {
			var event leapmuxv1.TunnelConnEvent
			if err := proto.Unmarshal(msg.GetPayload(), &event); err != nil {
				return
			}
			if len(event.GetData()) > 0 {
				dataCh <- event.GetData()
			}
			if event.GetEof() {
				select {
				case eofCh <- struct{}{}:
				default:
				}
			}
		})
		defer ch.UnregisterStream(reqID)

		// Send data through the tunnel.
		sendPayload, _ := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   []byte("hello tunnel"),
		})
		_, err := ch.SendRPCNoWait("SendTunnelData", sendPayload)
		require.NoError(t, err)

		// Wait for echo data from the stream.
		select {
		case data := <-dataCh:
			assert.Equal(t, "hello tunnel", string(data))
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for echo data")
		}

		// Close the tunnel connection.
		closePayload, _ := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: connID})
		_, err = ch.SendRPCNoWait("CloseTunnelConn", closePayload)
		require.NoError(t, err)

	case <-time.After(30 * time.Second):
		ch.UnregisterPending(reqID)
		t.Fatal("timeout waiting for OpenTunnelConn response")
	}
}

func TestChannelSocks5EchoFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	echoAddr := testutil.StartEchoServer(t)
	echoHost, echoPort := testutil.ParseAddr(echoAddr)

	hubURL, _, userID, workerID := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(ctx, hubURL, userID, workerID, nil)
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Create a local SOCKS5 proxy listener that forwards via the E2EE channel,
	// simulating what the desktop TunnelManager does for SOCKS5 tunnels.
	socks5Ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = socks5Ln.Close() })

	// Handle one SOCKS5 connection in a goroutine.
	proxyReady := make(chan struct{})
	proxyDone := make(chan error, 1)
	go func() {
		close(proxyReady)
		conn, acceptErr := socks5Ln.Accept()
		if acceptErr != nil {
			proxyDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()

		// Perform SOCKS5 handshake.
		// Phase 1: Greeting.
		header := make([]byte, 2)
		if _, readErr := io.ReadFull(conn, header); readErr != nil {
			proxyDone <- readErr
			return
		}
		nMethods := int(header[1])
		methods := make([]byte, nMethods)
		if _, readErr := io.ReadFull(conn, methods); readErr != nil {
			proxyDone <- readErr
			return
		}
		if _, writeErr := conn.Write([]byte{0x05, 0x00}); writeErr != nil {
			proxyDone <- writeErr
			return
		}

		// Phase 2: CONNECT request.
		reqHeader := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, reqHeader); readErr != nil {
			proxyDone <- readErr
			return
		}
		atyp := reqHeader[3]
		var targetAddrStr string
		switch atyp {
		case 0x01: // IPv4
			ip := make([]byte, 4)
			_, _ = io.ReadFull(conn, ip)
			targetAddrStr = net.IP(ip).String()
		case 0x03: // Domain
			lenBuf := make([]byte, 1)
			_, _ = io.ReadFull(conn, lenBuf)
			domain := make([]byte, lenBuf[0])
			_, _ = io.ReadFull(conn, domain)
			targetAddrStr = string(domain)
		}
		portBuf := make([]byte, 2)
		_, _ = io.ReadFull(conn, portBuf)
		targetPortVal := uint32(portBuf[0])<<8 | uint32(portBuf[1])

		// Open tunnel connection via E2EE channel.
		openPayload, _ := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
			TargetAddr: targetAddrStr,
			TargetPort: targetPortVal,
		})

		respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
		reqID, sendErr := ch.SendRPCNoWait("OpenTunnelConn", openPayload)
		if sendErr != nil {
			proxyDone <- sendErr
			return
		}
		ch.RegisterPending(reqID, respCh)

		resp := <-respCh
		ch.UnregisterPending(reqID)
		if resp == nil || resp.GetIsError() {
			// Send SOCKS5 failure.
			_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			proxyDone <- fmt.Errorf("open tunnel failed: %s", resp.GetErrorMessage())
			return
		}

		var openResp leapmuxv1.OpenTunnelConnResponse
		_ = proto.Unmarshal(resp.GetPayload(), &openResp)
		connID := openResp.GetConnId()

		// Send SOCKS5 success reply.
		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		// Register for stream events.
		dataCh := make(chan []byte, 16)
		ch.RegisterStream(reqID, func(msg *leapmuxv1.InnerStreamMessage) {
			var event leapmuxv1.TunnelConnEvent
			if proto.Unmarshal(msg.GetPayload(), &event) == nil && len(event.GetData()) > 0 {
				dataCh <- event.GetData()
			}
		})
		defer ch.UnregisterStream(reqID)

		// Bidirectional forwarding.
		done := make(chan struct{}, 2)

		// Worker -> client.
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case data := <-dataCh:
					if _, err := conn.Write(data); err != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		// Client -> Worker.
		go func() {
			defer func() { done <- struct{}{} }()
			buf := make([]byte, 32*1024)
			for {
				n, readErr := conn.Read(buf)
				if n > 0 {
					payload, _ := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{
						ConnId: connID,
						Data:   buf[:n],
					})
					if _, err := ch.SendRPCNoWait("SendTunnelData", payload); err != nil {
						return
					}
				}
				if readErr != nil {
					return
				}
			}
		}()

		<-done
		closePayload, _ := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: connID})
		_, _ = ch.SendRPCNoWait("CloseTunnelConn", closePayload)
		proxyDone <- nil
	}()

	<-proxyReady

	// Now act as a SOCKS5 client: connect to the proxy and request the echo server.
	client, err := net.Dial("tcp", socks5Ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// SOCKS5 greeting.
	_, err = client.Write([]byte{0x05, 0x01, 0x00})
	require.NoError(t, err)
	greetReply := make([]byte, 2)
	_, err = io.ReadFull(client, greetReply)
	require.NoError(t, err)
	assert.Equal(t, byte(0x05), greetReply[0])
	assert.Equal(t, byte(0x00), greetReply[1])

	// SOCKS5 CONNECT to echo server (IPv4).
	ip := net.ParseIP(echoHost).To4()
	require.NotNil(t, ip)
	connectReq := []byte{0x05, 0x01, 0x00, 0x01}
	connectReq = append(connectReq, ip...)
	connectReq = append(connectReq, byte(echoPort>>8), byte(echoPort))
	_, err = client.Write(connectReq)
	require.NoError(t, err)

	connectReply := make([]byte, 10)
	_, err = io.ReadFull(client, connectReply)
	require.NoError(t, err)
	assert.Equal(t, byte(0x05), connectReply[0])
	assert.Equal(t, byte(0x00), connectReply[1], "expected SOCKS5 success reply")

	// Now the SOCKS5 tunnel is established. Send data and verify echo.
	testData := []byte("hello via socks5")
	_, err = client.Write(testData)
	require.NoError(t, err)

	echoed := make([]byte, len(testData))
	_, err = io.ReadFull(client, echoed)
	require.NoError(t, err)
	assert.Equal(t, string(testData), string(echoed), "expected data echoed through SOCKS5 tunnel")
}

func TestRegistration_AutoApproveViaUnixSocket_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, socketPath, _, _ := startTestSolo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Connect a WebSocket to /ws/channel (solo mode auto-authenticates).
	wsURL := "ws" + hubURL[len("http"):] + "/ws/channel"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"channel-relay"},
	})
	require.NoError(t, err)
	defer func() { _ = ws.CloseNow() }()

	// Register a new worker via Unix socket — should be auto-approved.
	unixClient := newUnixHTTPClient(socketPath)
	connClient := leapmuxv1connect.NewWorkerConnectorServiceClient(unixClient, "http://localhost")

	regResp, err := connClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "0.1.0"},
	))
	require.NoError(t, err)
	regToken := regResp.Msg.GetRegistrationToken()
	require.NotEmpty(t, regToken)

	// Poll should immediately return APPROVED.
	pollResp, err := connClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: regToken},
	))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, pollResp.Msg.GetStatus())
	assert.NotEmpty(t, pollResp.Msg.GetWorkerId())
	assert.NotEmpty(t, pollResp.Msg.GetAuthToken())

	// Read from the WebSocket and verify a HubControlFrame with WORKERS_CHANGED
	// arrives. The Hub debounces control frames (default 3s), so we wait.
	var frame leapmuxv1.HubControlFrame
	for {
		msg, readErr := channelwire.ReadChannelMessage(ctx, ws)
		require.NoError(t, readErr, "timeout waiting for control frame on WebSocket")

		if msg.GetChannelId() != channelmgr.HubControlChannelID {
			continue // skip non-control messages (e.g. channel close notifications)
		}

		require.NoError(t, proto.Unmarshal(msg.GetCiphertext(), &frame))
		break
	}

	assert.Contains(t, frame.GetEvents(), leapmuxv1.HubControlEvent_HUB_CONTROL_EVENT_WORKERS_CHANGED)
}

func newUnixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
}

func TestChannelMultipleRPCs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, userID, workerID := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(ctx, hubURL, userID, workerID, nil)
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Send multiple RPCs to verify the channel handles sequential calls.
	for i := 0; i < 5; i++ {
		payload, _ := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
		resp, err := ch.CallRPC("GetWorkerSystemInfo", payload)
		require.NoError(t, err, "RPC %d failed", i)

		var sysInfo leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, proto.Unmarshal(resp.GetPayload(), &sysInfo))
		assert.NotEmpty(t, sysInfo.GetOs())
	}
}
