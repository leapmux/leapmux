package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingWriteCloser struct {
	started chan struct{}
	closed  chan struct{}
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.closed
	return 0, net.ErrClosed
}

func (w *blockingWriteCloser) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

func TestIsBenignSessionReadError(t *testing.T) {
	t.Run("eof", func(t *testing.T) {
		if !isBenignSessionReadError(io.EOF) {
			t.Fatal("expected io.EOF to be benign")
		}
	})

	t.Run("unexpected eof", func(t *testing.T) {
		if !isBenignSessionReadError(io.ErrUnexpectedEOF) {
			t.Fatal("expected io.ErrUnexpectedEOF to be benign")
		}
	})

	t.Run("wrapped eof", func(t *testing.T) {
		// A wrapped EOF must still be classified benign. == comparison would miss
		// it and flip a clean peer-disconnect to a non-zero sidecar exit.
		err := fmt.Errorf("read frame: %w", io.EOF)
		if !isBenignSessionReadError(err) {
			t.Fatal("expected wrapped io.EOF to be benign")
		}
	})

	t.Run("wrapped net err closed", func(t *testing.T) {
		err := fmt.Errorf("read frame: %w", net.ErrClosed)
		if !isBenignSessionReadError(err) {
			t.Fatal("expected wrapped net.ErrClosed to be benign")
		}
	})

	t.Run("closed connection string", func(t *testing.T) {
		err := errors.New("read unix /tmp/test.sock->: use of closed network connection")
		if !isBenignSessionReadError(err) {
			t.Fatal("expected closed network connection string to be benign")
		}
	})

	t.Run("other error", func(t *testing.T) {
		if isBenignSessionReadError(errors.New("boom")) {
			t.Fatal("did not expect arbitrary error to be benign")
		}
	})
}

func TestTerminalSessionError(t *testing.T) {
	require.NoError(t, terminalSessionError(io.EOF))
	require.NoError(t, terminalSessionError(context.Canceled))
	protocolErr := errors.New("malformed protobuf")
	require.ErrorIs(t, terminalSessionError(protocolErr), protocolErr)
}

func TestHandleCreateTunnelRejectsMissingConfig(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	require.NotPanics(t, func() {
		session.handleRequest(context.Background(), &desktoppb.Request{
			Id: 1,
			Method: &desktoppb.Request_CreateTunnel{
				CreateTunnel: &desktoppb.CreateTunnelRequest{},
			},
		})
	})
	response, err := ReadFrame(&output)
	require.NoError(t, err)
	require.ErrorContains(t, errors.New(response.GetResponse().GetError()), "config is required")
}

func TestHandleResetTunnelsAcksOK(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	before := app.tunnels.currentRevision()
	session.handleRequest(context.Background(), &desktoppb.Request{
		Id: 1,
		Method: &desktoppb.Request_ResetTunnels{
			ResetTunnels: &desktoppb.ResetTunnelsRequest{},
		},
	})
	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	response := frame.GetResponse()
	require.Empty(t, response.GetError())
	require.True(t, response.GetBoolValue().GetValue(), "a successful reset acks with the shared void-method OK")
	require.Greater(t, app.tunnels.currentRevision(), before,
		"the dispatch arm must actually route to App.ResetTunnels")
}

// The ListTunnels dispatch arm must map the manager's listing through
// tunnelInfosToProto -- every field the shell renders rides this mapping, and
// the arm was previously uncovered (only App.ListTunnels itself was tested).
func TestHandleListTunnelsMapsTunnelInfos(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app.tunnels.mu.Lock()
	app.tunnels.tunnels["t-1"] = &tunnel{
		info: TunnelInfo{
			ID: "t-1", WorkerID: "w1", Type: "port_forward",
			BindAddr: "127.0.0.1", BindPort: ln.Addr().(*net.TCPAddr).Port,
			TargetAddr: "10.0.0.9", TargetPort: 8080,
		},
		listener: ln,
		cancel:   cancel,
	}
	app.tunnels.mu.Unlock()

	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)
	session.handleRequest(context.Background(), &desktoppb.Request{
		Id:     7,
		Method: &desktoppb.Request_ListTunnels{ListTunnels: &desktoppb.ListTunnelsRequest{}},
	})

	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	resp := frame.GetResponse()
	require.Empty(t, resp.GetError())
	tunnels := resp.GetListTunnels().GetTunnels()
	require.Len(t, tunnels, 1)
	assert.Equal(t, "t-1", tunnels[0].GetId())
	assert.Equal(t, "w1", tunnels[0].GetWorkerId())
	assert.Equal(t, "10.0.0.9", tunnels[0].GetTargetAddr())
	assert.Equal(t, int32(8080), tunnels[0].GetTargetPort())
}

// The ListEditors dispatch arm must map the registry's listing through
// detectedEditorsToProto; previously uncovered like ListTunnels above.
func TestHandleListEditorsMapsDetectedEditors(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	app.editors.mu.Lock()
	app.editors.cached = true
	app.editors.cache = []DetectedEditor{{ID: "vscode", DisplayName: "VS Code"}}
	app.editors.mu.Unlock()

	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)
	session.handleRequest(context.Background(), &desktoppb.Request{
		Id:     8,
		Method: &desktoppb.Request_ListEditors{ListEditors: &desktoppb.ListEditorsRequest{Refresh: false}},
	})

	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	resp := frame.GetResponse()
	require.Empty(t, resp.GetError())
	editors := resp.GetListEditors().GetEditors()
	require.Len(t, editors, 1)
	assert.Equal(t, "vscode", editors[0].GetId())
	assert.Equal(t, "VS Code", editors[0].GetDisplayName())
}

func TestSwitchModeResponseCarriesLauncherStateWithCleanupError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wantErr := errors.New("lease release failed")
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), failingSoloInstance{err: wantErr}, "")
	app.config.Mode = "solo"
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	session.handleRequest(context.Background(), &desktoppb.Request{
		Id: 1,
		Method: &desktoppb.Request_SwitchMode{
			SwitchMode: &desktoppb.SwitchModeRequest{},
		},
	})
	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	response := frame.GetResponse()
	require.Empty(t, response.GetError())
	result := response.GetLifecycle()
	require.Len(t, result.GetCleanupErrors(), 1)
	require.ErrorContains(t, errors.New(result.GetCleanupErrors()[0]), wantErr.Error())
	require.Equal(t, desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_LAUNCHER, result.GetSidecarInfo().GetShellMode())
}

func TestSwitchModePersistenceFailureReturnsOnlyTopLevelError(t *testing.T) {
	makeConfigUnwritable(t)
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("https://hub.example"), &recordingSoloInstance{}, "https://hub.example")
	app.config.Mode = "solo"
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	session.handleRequest(context.Background(), &desktoppb.Request{
		Id: 1,
		Method: &desktoppb.Request_SwitchMode{
			SwitchMode: &desktoppb.SwitchModeRequest{},
		},
	})
	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	response := frame.GetResponse()
	require.NotEmpty(t, response.GetError())
	require.Nil(t, response.GetResult(), "a failed transition must not be encoded as a successful result")

	app.config = &DesktopConfig{}
	require.NoError(t, app.Shutdown())
}

func TestShutdownResponseReportsCleanupError(t *testing.T) {
	wantErr := errors.New("lease release failed")
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), failingSoloInstance{err: wantErr}, "")
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	session.handleRequest(context.Background(), &desktoppb.Request{
		Id: 1,
		Method: &desktoppb.Request_Shutdown{
			Shutdown: &desktoppb.ShutdownRequest{},
		},
	})
	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	response := frame.GetResponse()
	require.Empty(t, response.GetError())
	require.Len(t, response.GetLifecycle().GetCleanupErrors(), 1)
	require.ErrorContains(t, errors.New(response.GetLifecycle().GetCleanupErrors()[0]), wantErr.Error())
}

// slowFailingSoloInstance stands in for a real solo teardown, which routinely
// takes over a second (operation drain, then the hub's server/registry/watcher
// shutdown) and reports a cleanup error.
type slowFailingSoloInstance struct {
	delay time.Duration
	err   error
}

func (slowFailingSoloInstance) LocalListenURL() string { return "" }
func (s slowFailingSoloInstance) Stop() error {
	time.Sleep(s.delay)
	return s.err
}

// The Shutdown RPC must deliver its LifecycleResult through a live Run loop, not
// just when handleRequest is called directly.
//
// App.Shutdown's first act is cancelling app.ctx, which is exactly what makes
// Run return -- so the session tears itself down while its own Shutdown handler
// is still inside the hub teardown. With a writer grace shorter than that
// teardown, drainHandlers interrupted the writer first and writeLifecycleResult
// hit a closed pipe: the cleanup_errors this RPC exists to report were dropped
// and the shell burned its full 5s timeout on every quit waiting for a reply that
// never came. TestShutdownResponseReportsCleanupError calls handleRequest
// directly, so it never exercised this.
func TestShutdownResponseSurvivesSlowTeardownThroughRunLoop(t *testing.T) {
	wantErr := errors.New("lease release failed")
	app := NewApp("")
	// Longer than the old one-second writer grace: a real solo teardown budgets
	// ~10s each for the hub server, CRDT registry, and revocation watcher, so
	// exceeding a second is the norm, not the edge case.
	installTestConnection(app, newHTTPProxy("http://localhost"), slowFailingSoloInstance{
		delay: 1500 * time.Millisecond,
		err:   wantErr,
	}, "")

	clientConn, sidecarConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })

	session := NewRPCSession(app, sidecarConn, sidecarConn, nil)
	runDone := make(chan error, 1)
	go func() { runDone <- session.Run() }()

	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     42,
			Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
		}},
	}))

	frameCh := make(chan *desktoppb.Frame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, err := ReadFrame(bufio.NewReader(clientConn))
		if err != nil {
			errCh <- err
			return
		}
		frameCh <- frame
	}()

	select {
	case frame := <-frameCh:
		response := frame.GetResponse()
		require.Equal(t, uint64(42), response.GetId())
		require.Empty(t, response.GetError())
		require.Len(t, response.GetLifecycle().GetCleanupErrors(), 1,
			"the shutdown's cleanup error must reach the shell")
		require.Contains(t, response.GetLifecycle().GetCleanupErrors()[0], wantErr.Error())
	case err := <-errCh:
		t.Fatalf("shutdown reply lost -- the writer was interrupted under its own handler: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the shutdown reply")
	}

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
}

// The two drain phases must SHARE handlerDrainTimeout, not each get it.
//
// They run in sequence, so a per-phase budget lets teardown reach 2x the timeout --
// and the timeout is chosen to match the shell's own patience (request_shutdown_async
// waits handlerDrainTimeout for the Shutdown reply). A straggling handler would
// therefore make the shell give up and exit while the sidecar was still draining,
// leaving the process alive past the window it was sized to fit in.
func TestDrainHandlersSharesOneBudgetAcrossBothPhases(t *testing.T) {
	original := handlerDrainTimeout
	handlerDrainTimeout = 200 * time.Millisecond
	t.Cleanup(func() { handlerDrainTimeout = original })
	// interruptGrace is the post-interrupt phase's floor, not a second copy of the
	// budget: shortened here so what the bound below measures is the SHARED budget
	// rather than the grace on top of it.
	originalGrace := interruptGrace
	interruptGrace = 20 * time.Millisecond
	t.Cleanup(func() { interruptGrace = originalGrace })

	app := NewApp("")
	clientConn, sidecarConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	session := NewRPCSession(app, sidecarConn, sidecarConn, nil)

	// A handler that ignores sessionCtx entirely: it outlives any budget, so the
	// drain must be bounded by the budget rather than by the handler.
	var handlers waitCounter
	handlers.add()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		defer handlers.done()
		<-release
	}()

	start := time.Now()
	session.drainHandlers(&handlers, nil) // nil cause: takes the flush phase, then interrupts
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*handlerDrainTimeout,
		"both phases must come out of one budget, not one each")
	assert.GreaterOrEqual(t, elapsed, handlerDrainTimeout,
		"the drain must still spend its budget waiting for the straggler")
}

// TestDrainHandlersDoesNotLeakWaiterOnAbandonedStraggler is the drainHandlers
// side of the #297 reproducer -- the path the leak was actually reported on
// (one timed-out handler drain per webview reconnect in dev-socket mode).
// drain_test.go's TestDrainDoesNotLeakWaiterOnAbandonedStraggler covers the
// operationGate side; both drains share waitCounter today, but only a per-path
// test catches a future divergence that reintroduces a spawn-waiter on one
// side alone.
func TestDrainHandlersDoesNotLeakWaiterOnAbandonedStraggler(t *testing.T) {
	original := handlerDrainTimeout
	handlerDrainTimeout = time.Millisecond
	t.Cleanup(func() { handlerDrainTimeout = original })
	originalGrace := interruptGrace
	interruptGrace = time.Millisecond
	t.Cleanup(func() { interruptGrace = originalGrace })

	app := NewApp("")
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	// A handler that never returns until Cleanup: every drain against it times
	// out and abandons it, exactly the straggler that leaked one parked waiter
	// per drain pre-fix.
	var handlers waitCounter
	handlers.add()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		defer handlers.done()
		<-release
	}()

	const drains = 8
	for range drains {
		session.drainHandlers(&handlers, nil)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		dump := allGoroutineStacks()
		leaked := countDrainWaiterFrames(dump)
		if leaked == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected no drain waiter goroutines after %d abandoned handler drains; found %d\n%s",
				drains, leaked, dump)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An event frame that cannot be delivered must tear down its RELAY and tell the
// frontend, not be logged and dropped -- and not kill the whole session.
//
// A relay stream is ordered and gap-intolerant: `channel:message` carries Noise
// ciphertext whose nonce counter advances per message, so one dropped frame
// permanently desyncs every later decrypt on a relay that still looks healthy.
// Closing the relay routes the failure into the one recovery the frontend already
// implements (reconnect + re-handshake). Killing the SESSION instead would strand
// the Tauri shell, which has no sidecar respawn and awaits every request without a
// timeout -- one bad frame would wedge the whole UI.
func TestRPCSessionClosesRelayOnUndeliverableEventFrame(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		<-r.Context().Done()
		_ = conn.CloseNow()
	}))
	t.Cleanup(server.Close)

	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), nil, "")

	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{Subprotocols: []string{"channel-relay"}})
	require.NoError(t, err)
	relayCtx, relayCancel := context.WithCancel(app.ctx)
	t.Cleanup(relayCancel)
	relay := &ChannelRelay{
		wsRelay: wsRelay{ws: ws, ctx: relayCtx, cancel: relayCancel, done: make(chan struct{}), emit: app.EmitEvent},
	}
	relay.owner = 1 // the wrapper id this relay was installed under
	go relay.runReadLoop()
	app.connection.relay = relay

	var mu sync.Mutex
	var closeEvents []*desktoppb.ChannelCloseEvent
	app.SetEventSink(func(event *desktoppb.Event) {
		if ev := event.GetChannelClose(); ev != nil {
			mu.Lock()
			closeEvents = append(closeEvents, ev)
			mu.Unlock()
		}
	})

	// Stand in for a frame the sidecar could not put on the wire (one past the
	// frame budget, or a short write): the ordered stream now has a hole. The
	// emitter's owner id (1) matches the installed relay, so the close fires.
	app.CloseRelayForUndeliverableEvent(1, &desktoppb.Event{
		Payload: &desktoppb.Event_ChannelMessage{
			ChannelMessage: &desktoppb.ChannelMessageEvent{Data: []byte("frame that never made it")},
		},
	})

	app.lifecycleMu.RLock()
	installed := app.connection.relay
	app.lifecycleMu.RUnlock()
	assert.Nil(t, installed, "the relay whose frame was lost is torn down, not left silently desynced")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, closeEvents, 1, "the frontend is told, so it reconnects instead of going quiet")
	assert.False(t, closeEvents[0].GetWasClean(), "an undeliverable frame is not a clean close")
	assert.Contains(t, closeEvents[0].GetReason(), "could not deliver")
}

// TestCloseRelayForUndeliverableEvent_LeavesSuccessorRelayAlone pins WRAPPERS-1:
// the undeliverable close is spawned on its own goroutine, so by the time it
// acquires lifecycleMu a successor wrapper's open may have installed a new relay.
// The close must gate on the EMITTER's owner id (threaded through emitForOwner ->
// EmitRelayEvent -> failFrameForRelay) and leave the successor's relay alone --
// without the gate, the close would tear down the successor for the emitter's
// fault, forcing a spurious reconnect.
func TestCloseRelayForUndeliverableEvent_LeavesSuccessorRelayAlone(t *testing.T) {
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), nil, "")

	// Install the successor relay (owner 2) the way a later open would. The
	// emitter (owner 1) is no longer installed -- its undeliverable close runs
	// AFTER the supersede, exactly the race the gate exists for.
	successor := &ChannelRelay{
		wsRelay: wsRelay{
			ws:     nil, // no real socket; this relay only needs to survive the gate
			ctx:    app.ctx,
			cancel: func() {},
			done:   make(chan struct{}),
			emit:   app.EmitEvent,
		},
	}
	successor.owner = 2
	app.lifecycleMu.Lock()
	app.connection.relay = successor
	app.lifecycleMu.Unlock()

	var mu sync.Mutex
	var closeEvents []*desktoppb.ChannelCloseEvent
	app.SetEventSink(func(event *desktoppb.Event) {
		if ev := event.GetChannelClose(); ev != nil {
			mu.Lock()
			closeEvents = append(closeEvents, ev)
			mu.Unlock()
		}
	})

	// The emitter's close carries owner 1; the installed relay's owner is 2, so
	// the gate must refuse the close rather than tearing the successor down.
	app.CloseRelayForUndeliverableEvent(1, &desktoppb.Event{
		Payload: &desktoppb.Event_ChannelMessage{
			ChannelMessage: &desktoppb.ChannelMessageEvent{Data: []byte("frame from the superseded relay")},
		},
	})

	app.lifecycleMu.RLock()
	installed := app.connection.relay
	app.lifecycleMu.RUnlock()
	assert.Same(t, successor, installed,
		"the successor relay (owner 2) must survive the superseded emitter's (owner 1) undeliverable close")

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, closeEvents,
		"no close event is emitted for a close that the ownership gate refused -- the successor never saw the failing frame")
}

func TestRPCSessionWaitsForAdmittedHandlers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blocking := blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})}
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), blocking, "")
	app.config.Mode = "solo"
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()
	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     1,
			Method: &desktoppb.Request_SwitchMode{SwitchMode: &desktoppb.SwitchModeRequest{}},
		}},
	}))
	<-blocking.entered
	require.NoError(t, clientConn.Close())
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before an admitted handler completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(blocking.release)
	require.NoError(t, <-runDone)
}

func TestRPCSessionDisconnectInterruptsBlockedResponseWriter(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	reader, input := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = input.Close()
	})
	writer := newBlockingWriteCloser()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, reader, writer, nil).Run() }()
	require.NoError(t, WriteFrame(input, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     1,
			Method: &desktoppb.Request_GetConfig{GetConfig: &desktoppb.GetConfigRequest{}},
		}},
	}))
	<-writer.started
	require.NoError(t, input.Close())

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(250 * time.Millisecond):
		_ = writer.Close()
		<-runDone
		t.Fatal("RPC session did not interrupt a response writer after read-side EOF")
	}
}

func TestRPCSessionBurstDoesNotHidePeerDisconnect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	app := NewApp("")
	installTestConnection(app, newHTTPProxy(upstream.URL), nil, upstream.URL)
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	// A burst of in-flight proxy handlers (each blocked upstream) must not stop
	// the session from observing a peer disconnect. There is no admission cap,
	// so every request is admitted; Run still returns when the peer closes.
	const burst = 65
	for i := 0; i < burst; i++ {
		require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
			Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
				Id: uint64(i + 1),
				Method: &desktoppb.Request_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpRequest{
					Method: http.MethodGet,
					Path:   "/blocked",
				}},
			}},
		}))
	}
	require.NoError(t, clientConn.Close())

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.NoError(t, app.Shutdown())
		<-runDone
		t.Fatal("RPC saturation prevented the session from observing peer disconnect")
	}
	require.NoError(t, app.Shutdown())
}

// With no admission control, Shutdown is just another handler: it must
// proceed even when many proxy handlers are in flight (previously a full
// general pool could starve a reserved control-plane slot). The shutdown
// handler cancels app.ctx, which unwinds the blocked proxy handlers.
func TestRPCSessionShutdownProceedsWithInFlightProxyHandlers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	app := NewApp("")
	installTestConnection(app, newHTTPProxy(upstream.URL), nil, upstream.URL)
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	const inFlight = 65
	for i := 0; i < inFlight; i++ {
		require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
			Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
				Id: uint64(i + 1),
				Method: &desktoppb.Request_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpRequest{
					Method: http.MethodGet,
					Path:   "/blocked",
				}},
			}},
		}))
	}
	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     1000,
			Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
		}},
	}))

	select {
	case <-app.ctx.Done():
	case <-time.After(time.Second):
		_ = clientConn.Close()
		require.NoError(t, app.Shutdown())
		<-runDone
		t.Fatal("in-flight proxy handlers blocked the shutdown request")
	}
	_ = clientConn.Close()
	require.NoError(t, <-runDone)
}

func TestRPCSessionShutdownIsAdmittedWhileSwitchModeRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blocking := blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})}
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), blocking, "")
	app.config.Mode = "solo"
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     1,
			Method: &desktoppb.Request_SwitchMode{SwitchMode: &desktoppb.SwitchModeRequest{}},
		}},
	}))
	<-blocking.entered
	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     2,
			Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
		}},
	}))

	shutdownAdmitted := false
	select {
	case <-app.ctx.Done():
		shutdownAdmitted = true
	case <-time.After(250 * time.Millisecond):
	}
	close(blocking.release)
	_ = clientConn.Close()
	require.NoError(t, <-runDone)
	if !shutdownAdmitted {
		require.NoError(t, app.Shutdown())
		t.Fatal("an in-flight mode switch starved the shutdown request")
	}
}

func TestRPCSessionDisconnectCancelsBlockedProxyHandler(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	app := NewApp("")
	installTestConnection(app, newHTTPProxy(upstream.URL), nil, upstream.URL)
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id: 1,
			Method: &desktoppb.Request_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpRequest{
				Method: http.MethodGet,
				Path:   "/blocked",
			}},
		}},
	}))
	<-entered
	require.NoError(t, clientConn.Close())

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.NoError(t, app.Shutdown())
		<-runDone
		t.Fatal("RPC session did not cancel its blocked handler after peer disconnect")
	}
	require.NoError(t, app.Shutdown())
}

func TestRPCSessionDisconnectCancelsDistributedProbe(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("HOME", t.TempDir())

	app := NewApp("")
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id: 1,
			Method: &desktoppb.Request_ConnectDistributed{ConnectDistributed: &desktoppb.ConnectDistributedRequest{
				HubUrl: upstream.URL,
			}},
		}},
	}))
	<-entered
	require.NoError(t, clientConn.Close())

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.NoError(t, app.Shutdown())
		<-runDone
		t.Fatal("RPC session did not cancel its distributed probe after peer disconnect")
	}
	require.NoError(t, app.Shutdown())
}

func TestRPCSessionDisconnectCancelsTunnelHandshake(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(upstream.Close)

	app := NewApp("")
	connection := installTestConnection(app, newHTTPProxy(upstream.URL), nil, upstream.URL)
	app.applyProxyToTunnels(connection.proxy)
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()

	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id: 1,
			Method: &desktoppb.Request_CreateTunnel{CreateTunnel: &desktoppb.CreateTunnelRequest{
				Config: &desktoppb.TunnelConfig{
					WorkerId: "worker", Type: tunnelTypePortForward,
					TargetAddr: "127.0.0.1", TargetPort: 80,
				},
			}},
		}},
	}))
	<-entered
	require.NoError(t, clientConn.Close())

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.NoError(t, app.Shutdown())
		<-runDone
		t.Fatal("RPC session did not cancel its tunnel handshake after peer disconnect")
	}
	require.NoError(t, app.Shutdown())
}

func TestWriteResponseReplacesOversizedFrameWithError(t *testing.T) {
	var output bytes.Buffer
	session := NewRPCSession(NewApp(""), bytes.NewReader(nil), &output, nil)
	t.Cleanup(func() { require.NoError(t, session.app.Shutdown()) })

	session.writeResponse(&desktoppb.Response{
		Id: 42,
		Result: &desktoppb.Response_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpResponse{
			Body: make([]byte, maxFrameSize),
		}},
	})

	frame, err := ReadFrame(&output)
	require.NoError(t, err)
	response := frame.GetResponse()
	require.Equal(t, uint64(42), response.GetId())
	require.Contains(t, response.GetError(), "frame budget")
}

// TestRPCSessionAdmitsEveryRequestWithoutExhaustion locks in the "no admission"
// contract: every desktop RPC is accepted and processed. A burst well beyond the
// old 64-request / 64 MiB caps must produce a response for every request (none
// rejected with an exhaustion error) and the session must stay up.
func TestRPCSessionAdmitsEveryRequestWithoutExhaustion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	app := NewApp("")
	installTestConnection(app, newHTTPProxy(upstream.URL), nil, upstream.URL)
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		require.NoError(t, app.Shutdown())
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
	})

	const burst = 128
	for i := 0; i < burst; i++ {
		require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
			Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
				Id:     uint64(i + 1),
				Method: &desktoppb.Request_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpRequest{Method: http.MethodGet, Path: "/ok"}},
			}},
		}))
	}

	reader := bufio.NewReader(clientConn)
	seen := make(map[uint64]bool, burst)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < burst; i++ {
			frame, err := ReadFrame(reader)
			if err != nil {
				t.Errorf("read response %d: %v", i, err)
				return
			}
			resp := frame.GetResponse()
			if resp == nil {
				t.Errorf("response %d: expected a response frame", i)
				return
			}
			if resp.GetError() != "" {
				t.Errorf("request %d was rejected instead of admitted: %s", resp.GetId(), resp.GetError())
				return
			}
			seen[resp.GetId()] = true
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all burst responses")
	}
	require.Len(t, seen, burst, "every request must receive a response")

	// The session survived the burst.
	select {
	case err := <-runDone:
		t.Fatalf("session tore down under a burst instead of admitting every request: %v", err)
	default:
	}
}

// TestRPCSessionDrainAbandonsHandlerIgnoringSessionContext verifies the hard
// handlerDrainTimeout cap: a handler that blocks without observing sessionCtx
// (here, solo.Stop wedged on a test channel) cannot hang session teardown. Run
// returns within the cap instead of waiting forever for the wedged handler.
func TestRPCSessionDrainAbandonsHandlerIgnoringSessionContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := handlerDrainTimeout
	handlerDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { handlerDrainTimeout = original })

	blocking := blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(blocking.release) }) // let the abandoned handler exit
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("http://localhost"), blocking, "")
	app.config.Mode = "solo"
	serverConn, clientConn := net.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- NewRPCSession(app, serverConn, serverConn, nil).Run() }()
	require.NoError(t, WriteFrame(clientConn, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     1,
			Method: &desktoppb.Request_SwitchMode{SwitchMode: &desktoppb.SwitchModeRequest{}},
		}},
	}))
	<-blocking.entered
	// Close WITHOUT releasing the handler: it is blocked in solo.Stop and ignores
	// sessionCtx, so without the hard cap teardown would hang forever.
	require.NoError(t, clientConn.Close())
	select {
	case <-runDone:
		// Run returned within the hard cap instead of hanging on the wedged handler.
	case <-time.After(time.Second):
		t.Fatal("Run hung on a handler that ignores sessionCtx; the hard drain cap did not abandon it")
	}
}

// The post-interrupt drain phase must actually WAIT on the interrupt it just issued.
//
// waitBounded only returns false once its timer has fired, so reaching the interrupt
// on the clean path means the shared budget is spent by construction. Without
// interruptGrace phase 2 is one non-blocking look at `done` -- which a handler the
// interrupt just unblocked cannot win in the microseconds it needs to unwind -- so
// every clean drain past the flush window warned and abandoned handlers mid-write
// while socket.go closed the conn and looped to accept a new session.
func TestDrainHandlersJoinsHandlerReleasedByTheInterrupt(t *testing.T) {
	originalDrain := handlerDrainTimeout
	handlerDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { handlerDrainTimeout = originalDrain })

	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	writer := newBlockingWriteCloser()
	session := NewRPCSession(app, bytes.NewReader(nil), writer, nil)

	var handlers waitCounter
	handlers.add()
	var finished atomic.Bool
	go func() {
		defer handlers.done()
		// Blocked writing its response to a peer that stopped reading: exactly the
		// handler interruptWriter exists to release.
		session.writeOK(1)
		// Unwinding after the pipe is closed is not instantaneous (deferred cleanup,
		// the operation gate), so phase 2 has to be a real wait rather than a poll.
		time.Sleep(20 * time.Millisecond)
		finished.Store(true)
	}()
	<-writer.started

	session.drainHandlers(&handlers, nil) // nil cause: flush phase, then interrupt
	assert.True(t, finished.Load(),
		"a handler the interrupt released must be joined, not abandoned mid-write")
}

// A request dequeued under an already-cancelled session still gets a Response
// carrying its Id.
//
// readFrames cancels the session BEFORE it pushes the read error, so the last good
// frame is dequeued with no error under a session that is already dead; App.Shutdown
// cancels app.ctx the same way underneath a request that already passed Run's check.
// Skipping the handler there leaves the frame unanswered -- and the Tauri shell awaits
// every request without a timeout.
func TestDispatchAnswersRequestDequeuedUnderCancelledSession(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	var output bytes.Buffer
	session := NewRPCSession(app, bytes.NewReader(nil), &output, nil)

	sessionCtx, cancelSession := context.WithCancel(context.Background())
	cancelSession()
	session.dispatch(sessionCtx, &desktoppb.Request{
		Id:     7,
		Method: &desktoppb.Request_GetConfig{GetConfig: &desktoppb.GetConfigRequest{}},
	})

	frame, err := ReadFrame(&output)
	require.NoError(t, err, "the request must be answered, not dropped in silence")
	response := frame.GetResponse()
	require.Equal(t, uint64(7), response.GetId(), "the reply must name the request it answers")
	require.Contains(t, response.GetError(), "shutting down")
}
