package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setUniqueSoloLocalListen scopes the hub's local-listen URL per-test so
// multiple tests running in the same process don't collide on the
// per-platform default endpoint (the Windows default embeds the current
// user's SID, which is identical across tests as the same user).
func setUniqueSoloLocalListen(t *testing.T) {
	t.Helper()
	t.Setenv(locallisten.EnvLocalListen, locallistentest.UniqueListenURL(t, "leapmux-desktop-test"))
}

func TestApp_OpenChannelRelay_Solo(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	if err := app.ConnectSolo(context.Background()); err != nil {
		t.Fatalf("ConnectSolo() failed: %v", err)
	}

	if err := app.OpenChannelRelay(context.Background(), 1); err != nil {
		t.Fatalf("OpenChannelRelay() failed: %v", err)
	}

	if err := app.CloseChannelRelay(1); err != nil {
		t.Fatalf("CloseChannelRelay(1) failed: %v", err)
	}
}

func TestCanceledOrgEventsRelayDoesNotEmitClose(t *testing.T) {
	serverRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"orgevents-relay"}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		<-serverRelease
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"orgevents-relay"}})
	require.NoError(t, err)
	events := make(chan *desktoppb.Event, 1)
	relay := &OrgEventsRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, emit: func(event *desktoppb.Event) { events <- event }}}
	done := make(chan struct{})
	go func() { relay.readLoop(); close(done) }()
	cancel()
	_ = ws.CloseNow()
	<-done
	close(serverRelease)
	select {
	case event := <-events:
		t.Fatalf("intentional cancellation emitted stale close event: %T", event.GetPayload())
	default:
	}
}

func TestChannelRelayPreservesAbnormalCloseDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusInternalError, "worker failed")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"channel-relay"}})
	require.NoError(t, err)
	events := make(chan *desktoppb.Event, 1)
	relay := &ChannelRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: func(event *desktoppb.Event) { events <- event }}}
	go relay.runReadLoop()

	select {
	case event := <-events:
		closeEvent := event.GetChannelClose()
		require.NotNil(t, closeEvent)
		require.Equal(t, uint32(websocket.StatusInternalError), closeEvent.GetCode())
		require.Equal(t, "worker failed", closeEvent.GetReason())
		require.False(t, closeEvent.GetWasClean())
	case <-time.After(time.Second):
		t.Fatal("channel relay did not emit its close event")
	}
	<-relay.done
}

func TestOrgEventsRelayPreservesAbnormalCloseDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"orgevents-relay"}})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusProtocolError, "bad frame")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"orgevents-relay"}})
	require.NoError(t, err)
	events := make(chan *desktoppb.Event, 1)
	relay := &OrgEventsRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: func(event *desktoppb.Event) { events <- event }}}
	go relay.runReadLoop()

	select {
	case event := <-events:
		closeEvent := event.GetOrgEventsClose()
		require.NotNil(t, closeEvent)
		require.Equal(t, uint32(websocket.StatusProtocolError), closeEvent.GetCode())
		require.Equal(t, "bad frame", closeEvent.GetReason())
		require.False(t, closeEvent.GetWasClean())
	case <-time.After(time.Second):
		t.Fatal("org-events relay did not emit its close event")
	}
	<-relay.done
}

func TestOrgEventsRelayPreservesGoingAwayCloseDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"orgevents-relay"}})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusGoingAway, "hub restart")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"orgevents-relay"}})
	require.NoError(t, err)
	events := make(chan *desktoppb.Event, 1)
	relay := &OrgEventsRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: func(event *desktoppb.Event) { events <- event }}}
	go relay.runReadLoop()

	select {
	case event := <-events:
		closeEvent := event.GetOrgEventsClose()
		require.NotNil(t, closeEvent)
		require.Equal(t, uint32(websocket.StatusGoingAway), closeEvent.GetCode())
		require.Equal(t, "hub restart", closeEvent.GetReason())
		require.True(t, closeEvent.GetWasClean())
	case <-time.After(time.Second):
		t.Fatal("org-events relay did not emit its close event")
	}
	<-relay.done
}

// A read-loop failure cancels the relay's lifetime BEFORE it emits its close
// event, mirroring ChannelRelay.readLoop: the two relays share wsRelay.run /
// openRelay, so their emit/cancel order must stay identical. No org-events adopt
// path gates on ctx.Err()==nil today (OpenOrgEventsRelay supersedes by owner id,
// not by ctx), but a future shared adopt-on-ctx path must not be able to adopt a
// relay whose read loop has already failed during the (potentially blocking)
// emit window -- so the cancel must precede the emit. The emit closure records
// ctx.Err() at the moment the close event fires; reading it after relay.done
// (the read loop has fully returned) is a happens-before edge, so no atomic is
// needed.
func TestOrgEventsRelayCancelsBeforeEmittingClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"orgevents-relay"}})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusProtocolError, "bad frame")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"orgevents-relay"}})
	require.NoError(t, err)

	var ctxErrAtEmit error
	events := make(chan *desktoppb.Event, 1)
	relay := &OrgEventsRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: func(event *desktoppb.Event) {
		if event.GetOrgEventsClose() != nil {
			ctxErrAtEmit = ctx.Err()
		}
		events <- event
	}}}
	go relay.runReadLoop()

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("org-events relay did not emit its close event")
	}
	<-relay.done

	require.NotNil(t, ctxErrAtEmit, "the close event must have been emitted")
	assert.ErrorIs(t, ctxErrAtEmit, context.Canceled,
		"the relay lifetime must be cancelled BEFORE the close event is emitted")
}

// parkedEmitRelay dials a real WebSocket that sends one frame and returns a wsRelay
// whose emit callback parks (signalling emitEntered) until releaseEmit is closed. The
// caller wraps the returned base in ChannelRelay/OrgEventsRelay, runs its read loop,
// and closes readDone when the loop exits. Chan/func fields are shared with any copy
// of the base, so a &ChannelRelay{wsRelay: base} drives the same socket and emit.
func parkedEmitRelay(t *testing.T, subprotocol string) (base wsRelay, releaseEmit, emitEntered, readDone chan struct{}) {
	t.Helper()
	serverRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_ = conn.Write(r.Context(), websocket.MessageBinary, []byte("frame"))
		<-serverRelease
	}))
	t.Cleanup(func() {
		close(serverRelease)
		server.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{subprotocol}})
	require.NoError(t, err)
	emitEntered = make(chan struct{})
	releaseEmit = make(chan struct{})
	readDone = make(chan struct{})
	base = wsRelay{
		ws:     ws,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		emit: func(*desktoppb.Event) {
			close(emitEntered)
			<-releaseEmit
		},
	}
	return base, releaseEmit, emitEntered, readDone
}

// The internal closeChannelRelay must DETACH the relay (cancel + force-close) and
// return promptly under the lock rather than block for the read loop to drain: the
// blocking drain moved to the exported CloseChannelRelay, off the lock. Blocking here
// (the old behavior) froze every lifecycle reader for up to relayDrainTimeout when a
// read loop was wedged mid-emit.
func TestCloseChannelRelayDetachIsPromptUnderLock(t *testing.T) {
	base, releaseEmit, emitEntered, readDone := parkedEmitRelay(t, "channel-relay")
	relay := &ChannelRelay{wsRelay: base}
	go func() {
		defer close(readDone)
		relay.runReadLoop()
	}()
	<-emitEntered

	app := &App{connection: &desktopConnection{relay: relay}}
	returned := make(chan (<-chan struct{}), 1)
	go func() {
		app.lifecycleMu.Lock()
		drain := app.closeChannelRelay()
		app.lifecycleMu.Unlock()
		returned <- drain
	}()
	var drain <-chan struct{}
	select {
	case drain = <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("closeChannelRelay blocked under the lock instead of detaching promptly")
	}
	require.NotNil(t, drain, "detach must return the relay's done channel to drain off the lock")
	require.Nil(t, app.connection.relay, "the relay slot must be cleared by detach")
	// The read loop is still parked in emit, so the drain channel must not be closed yet.
	select {
	case <-drain:
		t.Fatal("drain channel closed before the wedged read loop exited")
	default:
	}
	close(releaseEmit)
	<-readDone
	select {
	case <-drain:
	case <-time.After(time.Second):
		t.Fatal("drain channel never closed after the read loop exited")
	}
}

// The exported CloseChannelRelay drains the read loop OFF lifecycleMu, so a
// concurrent lifecycle reader (SidecarInfo takes the read lock) is not frozen for the
// drain window when a relay is wedged mid-emit. This is the availability fix: the
// old code held the write lock across the 5s drain.
func TestCloseChannelRelayDrainsOffLockWithoutFreezingReaders(t *testing.T) {
	base, releaseEmit, emitEntered, readDone := parkedEmitRelay(t, "channel-relay")
	relay := &ChannelRelay{wsRelay: base}
	relay.owner = 1
	go func() {
		defer close(readDone)
		relay.runReadLoop()
	}()
	<-emitEntered

	app := &App{connection: &desktopConnection{relay: relay}}
	closeReturned := make(chan struct{})
	go func() {
		_ = app.CloseChannelRelay(1)
		close(closeReturned)
	}()

	// While CloseChannelRelay is draining the wedged read loop (off the lock),
	// SidecarInfo's RLock must be served promptly rather than frozen for the drain.
	sidecarReturned := make(chan struct{})
	go func() {
		_ = app.SidecarInfo()
		close(sidecarReturned)
	}()
	select {
	case <-sidecarReturned:
	case <-time.After(time.Second):
		t.Fatal("SidecarInfo was frozen behind a relay drain holding lifecycleMu")
	}

	// Releasing the parked emit lets the read loop exit, and only then does the
	// exported close return -- it still waits for the drain, just not under the lock.
	close(releaseEmit)
	<-readDone
	select {
	case <-closeReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseChannelRelay never returned after the read loop drained")
	}
}

// The internal closeOrgEventsRelay mirrors closeChannelRelay: it detaches promptly
// under the lock and hands the drain to the caller.
func TestCloseOrgEventsRelayDetachIsPromptUnderLock(t *testing.T) {
	base, releaseEmit, emitEntered, readDone := parkedEmitRelay(t, "orgevents-relay")
	relay := &OrgEventsRelay{wsRelay: base}
	go func() {
		defer close(readDone)
		relay.runReadLoop()
	}()
	<-emitEntered

	app := &App{connection: &desktopConnection{orgEventsRelay: relay}}
	returned := make(chan (<-chan struct{}), 1)
	go func() {
		app.lifecycleMu.Lock()
		drain := app.closeOrgEventsRelay()
		app.lifecycleMu.Unlock()
		returned <- drain
	}()
	var drain <-chan struct{}
	select {
	case drain = <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("closeOrgEventsRelay blocked under the lock instead of detaching promptly")
	}
	require.NotNil(t, drain, "detach must return the relay's done channel")
	require.Nil(t, app.connection.orgEventsRelay, "the relay slot must be cleared by detach")
	close(releaseEmit)
	<-readDone
	select {
	case <-drain:
	case <-time.After(time.Second):
		t.Fatal("drain channel never closed after the read loop exited")
	}
}

func TestApp_SidecarLogEvents_EmittedAfterSoloStart(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	var mu sync.Mutex
	var events []*desktoppb.SidecarLogEvent
	emitFunc := func(event *desktoppb.Event) {
		if log := event.GetSidecarLog(); log != nil {
			mu.Lock()
			events = append(events, log)
			mu.Unlock()
		}
	}

	app := NewApp("")
	app.SetEventSink(emitFunc)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	require.NoError(t, app.ConnectSolo(context.Background()))
	require.NoError(t, app.OpenChannelRelay(context.Background(), 1))

	// Give the hub a moment to process the WebSocket upgrade and log.
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, app.CloseChannelRelay(1))

	// Wait for disconnect log.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	captured := append([]*desktoppb.SidecarLogEvent(nil), events...)
	mu.Unlock()

	// We should have at least "channel relay connected" emitted.
	require.NotEmpty(t, captured, "expected sidecar:log events to be emitted")

	var messages []string
	for _, e := range captured {
		messages = append(messages, e.Message)
	}
	assert.Contains(t, messages, "channel relay connected")
}

// TestApp_OpenChannelRelay_ReusesAliveRelay reproduces the dev-refresh bug:
// when the persistent sidecar is asked to open the channel relay again, it
// must reuse the existing live relay rather than churning the connection.
// Churning would tear down the hub's relay binding and (combined with the
// race in UnregisterUnboundByUser) wipe channels the new page just opened.
func TestApp_OpenChannelRelay_ReusesAliveRelay(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	var mu sync.Mutex
	var connectCount, disconnectCount int
	emitFunc := func(event *desktoppb.Event) {
		log := event.GetSidecarLog()
		if log == nil {
			return
		}
		mu.Lock()
		switch log.Message {
		case "channel relay connected":
			connectCount++
		case "channel relay disconnected":
			disconnectCount++
		}
		mu.Unlock()
	}

	app := NewApp("")
	app.SetEventSink(emitFunc)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	require.NoError(t, app.ConnectSolo(context.Background()))
	require.NoError(t, app.OpenChannelRelay(context.Background(), 1))
	time.Sleep(100 * time.Millisecond)

	firstRelay := app.connection.relay
	require.NotNil(t, firstRelay)

	// Second OpenChannelRelay simulates the page-refresh path. It must
	// reuse the existing relay rather than opening a new one.
	require.NoError(t, app.OpenChannelRelay(context.Background(), 1))
	time.Sleep(100 * time.Millisecond)

	assert.Same(t, firstRelay, app.connection.relay, "relay must be reused across OpenChannelRelay calls")

	mu.Lock()
	gotConnects := connectCount
	gotDisconnects := disconnectCount
	mu.Unlock()
	assert.Equal(t, 1, gotConnects, "hub should see exactly one relay connect")
	assert.Equal(t, 0, gotDisconnects, "hub should not see a disconnect")

	require.NoError(t, app.CloseChannelRelay(1))
}

// A slow relay dial must not block lifecycle readers (SidecarInfo/ProxyHTTP),
// which take lifecycleMu.RLock(). Before the lock was released across the dial
// (and before the distributed hub path gained a dial deadline), a non-responsive
// hub could wedge all frontend hub traffic for the handshake duration.
func TestOpenChannelRelayDoesNotBlockLifecycleReadersDuringDial(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(accepted)
		<-release // hold the WebSocket handshake in flight
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		_ = conn.CloseNow()
	}))
	t.Cleanup(func() { close(release); server.Close() })

	app := NewApp("")
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	relayDone := make(chan error, 1)
	go func() { relayDone <- app.OpenChannelRelay(context.Background(), 1) }()
	<-accepted

	// SidecarInfo takes lifecycleMu.RLock(); it must complete while the dial is
	// still in flight (the write lock is NOT held across the dial).
	readerDone := make(chan struct{})
	go func() {
		_ = app.SidecarInfo()
		close(readerDone)
	}()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("SidecarInfo blocked behind the in-flight relay dial")
	}
}

// CloseChannelRelay must return within relayDrainTimeout even when the read
// loop's emit is permanently stalled (a peer that stopped draining the shell
// pipe). The drain runs off the lock now, but it is still bounded so the close
// (and thus the frontend's close RPC) cannot hang forever on a stalled emit.
func TestCloseChannelRelayBoundedByDrainTimeout(t *testing.T) {
	original := relayDrainTimeout
	relayDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { relayDrainTimeout = original })

	ctx, cancel := context.WithCancel(context.Background())
	stallRelease := make(chan struct{})
	t.Cleanup(func() { close(stallRelease) })
	ws := newStalledEmitRelayWS(t)
	relay := &ChannelRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), owner: 1, emit: func(*desktoppb.Event) { <-stallRelease }}}
	go relay.runReadLoop()

	app := &App{connection: &desktopConnection{relay: relay}}
	started := time.Now()
	require.NoError(t, app.CloseChannelRelay(1))
	elapsed := time.Since(started)
	require.Less(t, elapsed, time.Second, "CloseChannelRelay blocked past the drain timeout on a stalled emit")
}

// newStalledEmitRelayWS returns a WebSocket whose Reads block until context
// cancel/CloseNow so runReadLoop stays in emit (the test's select{} callback).
func newStalledEmitRelayWS(t *testing.T) *websocket.Conn {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		// Keep feeding bytes so the read loop keeps calling emit (which stalls).
		for r.Context().Err() == nil {
			if err := conn.Write(r.Context(), websocket.MessageBinary, []byte("frame")); err != nil {
				return
			}
		}
		_ = conn.CloseNow()
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{Subprotocols: []string{"channel-relay"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// TestSendChannelMessageRequestCancelDoesNotForceCloseRelay guards the shared
// relay against the regression where SendChannelMessage bound the WebSocket
// write to the per-RPC request context: coder/websocket force-closes the
// connection for any cancellable context passed to Write that cancels, so a
// single cancelled request tore down the relay for every subscriber. The write
// must run under the relay's lifetime context (mirroring tunnel.Channel's
// sendInnerContext); the request context may only gate entry.
func TestSendChannelMessageRequestCancelDoesNotForceCloseRelay(t *testing.T) {
	received := make(chan []byte, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				_ = conn.CloseNow()
				return
			}
			select {
			case received <- data:
			case <-r.Context().Done():
				_ = conn.CloseNow()
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wsURL := "ws" + server.URL[len("http"):]
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"channel-relay"}})
	require.NoError(t, err)

	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	relay := &ChannelRelay{wsRelay: wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: func(*desktoppb.Event) {}}}
	go relay.runReadLoop()
	connCtx, connStop := context.WithCancel(app.ctx)
	app.connection = &desktopConnection{ctx: connCtx, stop: connStop, relay: relay}

	// A request whose context is already cancelled must be rejected at the
	// entry gate without ever writing under a request-bound context.
	cancelledCtx, cancelReq := context.WithCancel(context.Background())
	cancelReq()
	require.Error(t, app.SendChannelMessage(cancelledCtx, []byte("first")))

	// The shared relay survived: a fresh request still delivers its message.
	require.NoError(t, app.SendChannelMessage(context.Background(), []byte("second")))
	select {
	case data := <-received:
		require.Equal(t, []byte("second"), data)
	case <-time.After(time.Second):
		t.Fatal("second message never arrived; the cancelled request force-closed the shared relay")
	}
}

// A close for a relay that has since been handed to a successor must be IGNORED.
//
// The frontend dispatches open and close as separate RPCs and RPCSession runs each
// on its own goroutine with no ordering, so a close sent while wrapper A owned the
// relay can execute after wrapper B's open adopted it. Without the ownership fence
// that close tears down the relay B is using -- and because shutdown cancels the
// relay context before the read loop can emit, no channel:close ever reaches the
// frontend: B sits at readyState OPEN forever with every send failing, and nothing
// reopens it until the page reloads.
//
// The wrapper-id claim in JS orders the two REQUESTS but has no say over when the
// sidecar runs them, so the fence has to live here.
func TestApp_CloseChannelRelay_IgnoresStaleOwner(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	require.NoError(t, app.ConnectSolo(context.Background()))

	// Wrapper A opens and owns the relay.
	require.NoError(t, app.OpenChannelRelay(context.Background(), 1))

	// Wrapper B adopts the same live relay (the reuse path a reconnect takes).
	require.NoError(t, app.OpenChannelRelay(context.Background(), 2))

	app.lifecycleMu.RLock()
	relayBefore := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayBefore, "the relay must be live after B adopted it")
	require.Equal(t, uint64(2), relayBefore.owner, "adopting the relay must transfer ownership to B")

	// A's close arrives late. It must not touch B's relay.
	require.NoError(t, app.CloseChannelRelay(1), "a stale close is satisfied, not an error")

	app.lifecycleMu.RLock()
	relayAfter := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "a stale close must not tear down the successor's relay")
	assert.Same(t, relayBefore, relayAfter, "the relay B adopted must survive A's late close")
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")

	// B's own close still works.
	require.NoError(t, app.CloseChannelRelay(2))
	app.lifecycleMu.RLock()
	relayFinal := app.connection.relay
	app.lifecycleMu.RUnlock()
	assert.Nil(t, relayFinal, "the owner's close must tear the relay down")
}

// Ownership dies with the relay it named, so a later close from that owner cannot
// reach a relay opened afterwards.
func TestApp_CloseChannelRelay_DropsOwnerOnTeardown(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	require.NoError(t, app.ConnectSolo(context.Background()))

	require.NoError(t, app.OpenChannelRelay(context.Background(), 7))
	require.NoError(t, app.CloseChannelRelay(7))

	app.lifecycleMu.RLock()
	torndown := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.Nil(t, torndown, "the owner's close must tear the relay down")

	// A fresh relay for a new wrapper must not be closable by the previous owner.
	require.NoError(t, app.OpenChannelRelay(context.Background(), 8))
	require.NoError(t, app.CloseChannelRelay(7), "the stale owner's close must be a no-op")

	app.lifecycleMu.RLock()
	relay := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relay, "a stale owner must not close a relay opened after its own")
	assert.Equal(t, uint64(8), relay.owner, "the fresh relay belongs to the wrapper that opened it")
}

// channelRelayTestServer accepts channel-relay WebSockets and holds each open until
// the test ends, so a relay dialed against it stays live for ownership assertions.
func channelRelayTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"channel-relay"}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	return server
}

// blockFirstChannelRelayDial makes the FIRST channel-relay dial wait until the
// returned release channel is closed, and reports (via dialing) when it has entered.
// Later dials run straight through. This is the seam that makes the adopt-vs-supersede
// window -- otherwise a race no test could pin down -- deterministic. Mirrors
// blockFirstOrgEventsDial.
func blockFirstChannelRelayDial(t *testing.T) (dialing chan struct{}, release chan struct{}) {
	t.Helper()
	dialing = make(chan struct{})
	release = make(chan struct{})
	original := dialChannelRelay
	t.Cleanup(func() { dialChannelRelay = original })
	var blocked atomic.Bool
	dialChannelRelay = func(ctx context.Context, proxy *HubProxy) (*websocket.Conn, error) {
		if blocked.CompareAndSwap(false, true) {
			close(dialing)
			<-release
		}
		return original(ctx, proxy)
	}
	return dialing, release
}

// An open whose connection is swapped out mid-dial (a mode transition landing
// while the lock is released) must abandon itself at the post-dial re-check
// rather than installing a relay onto the replaced connection. This is
// reacquireConnectionForInstall's connection-changed branch; the success
// branch hands the caller the unlock it now owes.
func TestApp_OpenChannelRelay_AbandonsWhenConnectionSwappedDuringDial(t *testing.T) {
	server := channelRelayTestServer(t)
	app := NewApp("")
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	dialing, release := blockFirstChannelRelayDial(t)
	relayDone := make(chan error, 1)
	go func() { relayDone <- app.OpenChannelRelay(context.Background(), 1) }()
	<-dialing

	// A mode transition swaps the connection while the dial is parked.
	app.lifecycleMu.Lock()
	swapped := installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	app.lifecycleMu.Unlock()

	close(release)
	err := <-relayDone
	require.ErrorContains(t, err, "connection changed during relay setup")

	app.lifecycleMu.RLock()
	defer app.lifecycleMu.RUnlock()
	require.Nil(t, swapped.relay, "the abandoned open must not install onto the replacement connection")
}

// A stale open (a lower wrapper id) that reaches the adopt check AFTER its successor
// already owns the relay must abandon itself, not steal ownership back. Stealing would
// re-stamp owner to the stale id, and that wrapper's later Close would then tear the
// relay down out from under the active successor -- silently (shutdown cancels the
// relay context before the read loop can emit a channel:close), stranding the page at
// readyState OPEN with every send failing until a reload.
//
// This exercises the pre-dial policy path: a successor is already installed when the
// stale open runs, so the stale open must refuse before it even dials.
func TestApp_OpenChannelRelay_StaleOpenDoesNotStealFromSuccessor(t *testing.T) {
	server := channelRelayTestServer(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)

	// Wrapper 2 opens and owns the relay.
	require.NoError(t, app.OpenChannelRelay(context.Background(), 2))
	app.lifecycleMu.RLock()
	relayBefore := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayBefore)
	require.Equal(t, uint64(2), relayBefore.owner)

	// Wrapper 1 -- an EARLIER request the sidecar ran late -- must not steal it.
	require.Error(t, app.OpenChannelRelay(context.Background(), 1),
		"a stale open whose successor already owns the relay must abandon itself")

	app.lifecycleMu.RLock()
	relayAfter := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "the successor's relay must survive the stale open")
	assert.Same(t, relayBefore, relayAfter, "the stale open must not have replaced the relay")
	assert.Equal(t, uint64(2), relayAfter.owner, "the stale open must not have re-stamped the owner")
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")
}

// The install path has the same window: a stale open that blocked in its dial while
// its successor installed must abandon its just-dialed socket instead of adopting the
// successor's relay (and re-stamping its owner) at install. Mirrors the org-events
// concurrent-open fence.
func TestApp_OpenChannelRelay_ConcurrentOpenDoesNotStealFromSuccessor(t *testing.T) {
	server := channelRelayTestServer(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)

	dialing, release := blockFirstChannelRelayDial(t)

	// Wrapper 1 opens first and blocks inside its dial.
	open1 := make(chan error, 1)
	go func() { open1 <- app.OpenChannelRelay(context.Background(), 1) }()
	<-dialing

	// Wrapper 2 -- a later attempt -- opens and installs while 1 is still dialing.
	require.NoError(t, app.OpenChannelRelay(context.Background(), 2))
	app.lifecycleMu.RLock()
	relay2 := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relay2, "wrapper 2's relay must be installed")
	require.Equal(t, uint64(2), relay2.owner)

	close(release)
	require.Error(t, <-open1,
		"a superseded open must report that it lost, not adopt a relay its successor owns")

	app.lifecycleMu.RLock()
	relayAfter := app.connection.relay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "the successor's relay must survive the loser's install")
	assert.Same(t, relay2, relayAfter, "the loser must not have replaced the successor's relay")
	assert.Equal(t, uint64(2), relayAfter.owner, "the loser must not have re-stamped the owner")
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")
}
