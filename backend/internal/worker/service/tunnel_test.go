package service

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)


func tunnelTestSetup(t *testing.T) (*Context, *channel.Dispatcher, *testResponseWriter) {
	t.Helper()
	svc, d, w := setupTestService(t)
	svc.RegisteredBy = "user-1"
	return svc, d, w
}


func TestOpenTunnelConn_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.errors, 0, "expected no errors")
	require.Len(t, w.responses, 1, "expected one response")

	var resp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetConnId(), "expected non-empty conn_id")
}

func TestOpenTunnelConn_OwnershipEnforcement(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	w2 := &testResponseWriter{channelID: "test-ch"}
	// Dispatch as user-2 (not the owner "user-1").
	payload, _ := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: "127.0.0.1",
		TargetPort: 1234,
	})
	d.DispatchWith("user-2", &leapmuxv1.InnerRpcRequest{
		Method:  "OpenTunnelConn",
		Payload: payload,
	}, w2)

	require.Len(t, w2.errors, 1)
	assert.Equal(t, int32(7), w2.errors[0].code, "expected PERMISSION_DENIED")
}

func TestOpenTunnelConn_DialFailure(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	// Use localhost with a port that nothing is listening on.
	// Port 1 requires root on most systems, so dial should fail immediately.
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: "127.0.0.1",
		TargetPort: 1,
	}, w)

	require.Len(t, w.errors, 1, "expected dial error")
	assert.Equal(t, int32(13), w.errors[0].code, "expected INTERNAL error")
}

func TestOpenTunnelConn_InvalidTargetAddr(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: "",
		TargetPort: 80,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
}

func TestOpenTunnelConn_InvalidTargetPort(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: "127.0.0.1",
		TargetPort: 0,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
}

func TestSendTunnelData_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("hello"),
	}, w2)

	require.Len(t, w2.errors, 0, "expected no errors")
	require.Len(t, w2.responses, 1, "expected success response")
}

func TestSendTunnelData_UnknownConnID(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: "nonexistent",
		Data:   []byte("hello"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(5), w.errors[0].code, "expected NOT_FOUND")
}

func TestCloseTunnelConn_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: connID,
	}, w2)

	require.Len(t, w2.errors, 0, "expected no errors")
	require.Len(t, w2.responses, 1, "expected success response")

	// Subsequent SendTunnelData should fail.
	w3 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("hello"),
	}, w3)
	require.Len(t, w3.errors, 1)
	assert.Equal(t, int32(5), w3.errors[0].code, "expected NOT_FOUND after close")
}

func TestCloseTunnelConn_UnknownConnID(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: "nonexistent",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(5), w.errors[0].code, "expected NOT_FOUND")
}

func TestTunnelTargetEOF(t *testing.T) {
	// Server writes "hello" then closes.
	serverAddr := testutil.StartWriteThenCloseServer(t, []byte("hello"))
	host, port := testutil.ParseAddr(serverAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))

	// Wait for stream messages (data + EOF).
	time.Sleep(200 * time.Millisecond)

	// Check stream messages.
	hasData := false
	hasEOF := false
	for _, s := range w.streams {
		var event leapmuxv1.TunnelConnEvent
		if proto.Unmarshal(s.GetPayload(), &event) == nil {
			if len(event.GetData()) > 0 {
				hasData = true
				assert.Equal(t, "hello", string(event.GetData()))
			}
			if event.GetEof() {
				hasEOF = true
			}
		}
	}
	assert.True(t, hasData, "expected data stream message")
	assert.True(t, hasEOF, "expected EOF stream message")
}

func TestTunnelConcurrentConnections(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, _ := tunnelTestSetup(t)

	const numConns = 10
	var wg sync.WaitGroup
	connIDs := make([]string, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := &testResponseWriter{channelID: "test-ch"}
			dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
				TargetAddr: host,
				TargetPort: port,
			}, w)

			require.Len(t, w.responses, 1)
			var resp leapmuxv1.OpenTunnelConnResponse
			require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
			connIDs[idx] = resp.GetConnId()
		}(i)
	}
	wg.Wait()

	// All conn_ids should be unique.
	idSet := make(map[string]bool)
	for _, id := range connIDs {
		assert.NotEmpty(t, id)
		assert.False(t, idSet[id], "duplicate conn_id: %s", id)
		idSet[id] = true
	}

	// Close all connections.
	for _, connID := range connIDs {
		w := &testResponseWriter{channelID: "test-ch"}
		dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
			ConnId: connID,
		}, w)
		require.Len(t, w.errors, 0)
	}
}

func TestTunnelEchoIntegration(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send data.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("echo test"),
	}, w2)
	require.Len(t, w2.errors, 0)

	// Wait for echo response via stream.
	time.Sleep(200 * time.Millisecond)

	// The echo data should appear in stream messages.
	var echoed []byte
	for _, s := range w.streams {
		var event leapmuxv1.TunnelConnEvent
		if proto.Unmarshal(s.GetPayload(), &event) == nil {
			echoed = append(echoed, event.GetData()...)
		}
	}
	assert.Equal(t, "echo test", string(echoed), "expected echoed data")

	// Clean up.
	w3 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: connID,
	}, w3)
	require.Len(t, w3.errors, 0)
}

func TestSendTunnelData_AfterClose(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Close the connection.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w2)
	require.Len(t, w2.errors, 0)

	// Sending data after close should fail.
	w3 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("should fail"),
	}, w3)
	require.Len(t, w3.errors, 1)
	assert.Equal(t, int32(5), w3.errors[0].code, "expected NOT_FOUND after close")
}

func TestTunnelLargeDataTransfer(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send 1 MB of data in 32 KB chunks.
	totalSize := 1024 * 1024
	chunkSize := 32 * 1024
	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	for sent := 0; sent < totalSize; sent += chunkSize {
		w2 := &testResponseWriter{channelID: "test-ch"}
		dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   chunk,
		}, w2)
		require.Len(t, w2.errors, 0, "send chunk at offset %d failed", sent)
	}

	// Wait for echo data.
	time.Sleep(500 * time.Millisecond)

	// Verify we received data back via stream.
	var totalReceived int
	for _, s := range w.streams {
		var event leapmuxv1.TunnelConnEvent
		if proto.Unmarshal(s.GetPayload(), &event) == nil {
			totalReceived += len(event.GetData())
		}
	}
	assert.Equal(t, totalSize, totalReceived, "expected all data echoed back")

	w3 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w3)
	require.Len(t, w3.errors, 0)
}

func TestTunnelMultipleSequentialConnections(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, _ := tunnelTestSetup(t)

	for i := 0; i < 5; i++ {
		w := &testResponseWriter{channelID: "test-ch"}
		dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
			TargetAddr: host,
			TargetPort: port,
		}, w)
		require.Len(t, w.errors, 0, "open %d failed", i)
		require.Len(t, w.responses, 1)

		var openResp leapmuxv1.OpenTunnelConnResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
		connID := openResp.GetConnId()

		// Send and verify data.
		w2 := &testResponseWriter{channelID: "test-ch"}
		dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   []byte(fmt.Sprintf("msg-%d", i)),
		}, w2)
		require.Len(t, w2.errors, 0)

		// Close.
		w3 := &testResponseWriter{channelID: "test-ch"}
		dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w3)
		require.Len(t, w3.errors, 0)
	}
}

func TestTunnelHalfClose_TargetClosesFirst(t *testing.T) {
	// Start a server that reads one message, echoes it, then closes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 1024)
				n, _ := conn.Read(buf)
				if n > 0 {
					_, _ = conn.Write(buf[:n])
				}
				// Close after echoing.
			}()
		}
	}()

	host, port := testutil.ParseAddr(ln.Addr().String())

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send data.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("half-close-test"),
	}, w2)
	require.Len(t, w2.errors, 0)

	// Wait for echo + EOF.
	time.Sleep(300 * time.Millisecond)

	hasData := false
	hasEOF := false
	for _, s := range w.streams {
		var event leapmuxv1.TunnelConnEvent
		if proto.Unmarshal(s.GetPayload(), &event) == nil {
			if len(event.GetData()) > 0 {
				hasData = true
				assert.Equal(t, "half-close-test", string(event.GetData()))
			}
			if event.GetEof() {
				hasEOF = true
			}
		}
	}
	assert.True(t, hasData, "expected echo data")
	assert.True(t, hasEOF, "expected EOF after target closes")
}
