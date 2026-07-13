package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetWindowSizePersistsMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &App{config: &DesktopConfig{WindowWidth: 1280, WindowHeight: 800, WindowMode: WindowModeNormal}}

	// Entering fullscreen: the frontend sends 0 dimensions so the last windowed
	// size survives while the mode flips.
	if err := a.SetWindowSize(0, 0, WindowModeFullscreen); err != nil {
		t.Fatalf("SetWindowSize: %v", err)
	}
	if a.config.WindowWidth != 1280 || a.config.WindowHeight != 800 {
		t.Fatalf("windowed size not preserved: got %dx%d", a.config.WindowWidth, a.config.WindowHeight)
	}
	if a.config.WindowMode != WindowModeFullscreen {
		t.Fatalf("mode = %q, want %q", a.config.WindowMode, WindowModeFullscreen)
	}

	// The change round-trips to disk.
	reloaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reloaded.WindowMode != WindowModeFullscreen || reloaded.WindowWidth != 1280 || reloaded.WindowHeight != 800 {
		t.Fatalf("reloaded config = %+v", reloaded)
	}

	// Returning to a windowed size updates both dimensions and mode.
	if err := a.SetWindowSize(1000, 700, WindowModeNormal); err != nil {
		t.Fatalf("SetWindowSize: %v", err)
	}
	if a.config.WindowWidth != 1000 || a.config.WindowHeight != 700 || a.config.WindowMode != WindowModeNormal {
		t.Fatalf("windowed restore failed: %+v", a.config)
	}
}

func TestShutdownWaitsForAdmittedConfigPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")

	app.configMu.Lock()
	setterStarted := make(chan struct{})
	setterDone := make(chan error, 1)
	go func() {
		close(setterStarted)
		setterDone <- app.SetWindowSize(900, 700, WindowModeNormal)
	}()
	<-setterStarted
	// The setter is now admitted but blocked on configMu.
	time.Sleep(20 * time.Millisecond)

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	select {
	case err := <-shutdownDone:
		app.configMu.Unlock()
		<-setterDone
		t.Fatalf("Shutdown returned before admitted persistence completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	app.configMu.Unlock()
	require.NoError(t, <-setterDone)
	require.NoError(t, <-shutdownDone)
}

// A shutdown must not hang forever on an admitted operation that ignores a.ctx
// (a non-cancellable editor launch or filesystem scan). drainOperations bounds
// the wait, mirroring the sibling handler/relay drains bounded in this change.
func TestShutdownBoundsCtxIgnoringOperation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	orig := operationDrainTimeout
	operationDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { operationDrainTimeout = orig })

	// Hold configMu so an admitted SetWindowSize blocks past a.ctx cancellation,
	// standing in for a ctx-ignoring editor/filesystem operation.
	app.configMu.Lock()
	setterStarted := make(chan struct{})
	setterDone := make(chan struct{})
	go func() {
		defer close(setterDone)
		close(setterStarted)
		_ = app.SetWindowSize(900, 700, WindowModeNormal)
	}()
	<-setterStarted
	time.Sleep(20 * time.Millisecond)

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	select {
	case <-shutdownDone:
		// Returned despite the still-blocked operation: the drain is bounded.
	case <-time.After(2 * time.Second):
		app.configMu.Unlock()
		t.Fatal("Shutdown did not bound its wait on a ctx-ignoring admitted operation")
	}
	app.configMu.Unlock()
	// The abandoned straggler writes the config once it finally takes configMu.
	// Join it before returning: otherwise that write races t.TempDir()'s cleanup
	// (HOME points there), which intermittently fails the test with "directory not
	// empty" -- an artefact of the abandonment this test exists to assert.
	<-setterDone
}

// Shutdown's bounded drain is worthless if the step after it is unbounded. The
// straggler the drain abandons is precisely the one holding the transition gate
// -- ConnectSolo's beginOperation done() runs AFTER its transition release
// (defers unwind LIFO), so the drain can only time out while the gate is still
// held -- and the gate holder is parked in hub.NewServer (net.Listen, store open,
// migrations), which takes no context and so cannot be cancelled. Blocking on the
// gate would therefore hang Shutdown, and with it the process exit main defers it
// from, indefinitely.
func TestShutdownBoundsWaitForStuckTransition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	orig := operationDrainTimeout
	operationDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { operationDrainTimeout = orig })

	// An un-cancellable startSolo, standing in for a flocked SQLite file or a
	// stuck migration: it ignores ctx exactly as hub.NewServer's context-less
	// store open does.
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSolo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSolo) // so a failed assertion below never leaks the goroutine
	app.startSolo = func(context.Context) (*soloRuntime, error) {
		close(entered)
		<-release
		return nil, errors.New("solo startup abandoned")
	}

	connectDone := make(chan error, 1)
	go func() { connectDone <- app.ConnectSolo(context.Background()) }()
	<-entered

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	select {
	case <-shutdownDone:
		// Returned while the transition is still stuck: the wait is bounded.
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown blocked on the transition gate held by an un-cancellable operation")
	}

	// The abandoned straggler must still unwind cleanly once released, without
	// committing a connection into the torn-down app.
	releaseSolo()
	require.Error(t, <-connectDone, "a solo start abandoned by shutdown must not commit")
	app.lifecycleMu.RLock()
	defer app.lifecycleMu.RUnlock()
	assert.Nil(t, app.connection)
}

func makeConfigUnwritable(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config"), []byte("not a directory"), 0o600))
}

func installTestConnection(app *App, proxy *HubProxy, solo soloInstance, hubURL string) *desktopConnection {
	ctx, stop := context.WithCancel(app.ctx)
	connection := &desktopConnection{
		ctx:    ctx,
		stop:   stop,
		proxy:  proxy,
		hubURL: hubURL,
	}
	if solo != nil {
		connection.solo = &soloRuntime{instance: solo}
	}
	app.connection = connection
	return connection
}

func TestConnectDistributedRollsBackWhenConfigSaveFails(t *testing.T) {
	makeConfigUnwritable(t)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hub.Close)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	require.Error(t, app.ConnectDistributed(context.Background(), hub.URL))
	require.Nil(t, app.connection)
	require.Empty(t, app.config.Mode)
	require.Empty(t, app.config.HubURL)
}

type recordingSoloInstance struct {
	stops int
}

func (*recordingSoloInstance) LocalListenURL() string { return "" }
func (s *recordingSoloInstance) Stop() error {
	s.stops++
	return nil
}

func TestSwitchModeDoesNotDisconnectWhenConfigSaveFails(t *testing.T) {
	makeConfigUnwritable(t)
	solo := &recordingSoloInstance{}
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("https://hub.example"), solo, "https://hub.example")
	app.config.Mode = "solo"

	_, err := app.SwitchMode()
	require.Error(t, err)
	require.Equal(t, solo, app.connection.solo.instance)
	require.NotNil(t, app.connection.proxy)
	require.Equal(t, "solo", app.config.Mode)
	require.Zero(t, solo.stops)

	app.config = &DesktopConfig{}
	require.NoError(t, app.Shutdown())
}

func TestShutdownCancelsDistributedProbe(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hub.Close)
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	connectDone := make(chan error, 1)
	go func() { connectDone <- app.ConnectDistributed(context.Background(), hub.URL) }()
	<-entered

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		close(release)
		<-connectDone
		<-shutdownDone
		t.Fatal("Shutdown did not cancel the in-flight Hub probe")
	}
	require.ErrorIs(t, <-connectDone, context.Canceled)
	close(release)
}

func TestConnectRequiresLauncherState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	installTestConnection(app, newHTTPProxy("https://original.example"), nil, "https://original.example")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	err := app.ConnectDistributed(context.Background(), "https://replacement.example")
	require.ErrorContains(t, err, "already connected")
	require.Equal(t, "https://original.example", app.connection.proxy.baseURL)
}

func TestWindowModeProtoRoundTrip(t *testing.T) {
	for _, mode := range []string{WindowModeNormal, WindowModeMaximized, WindowModeFullscreen} {
		if got := windowModeFromProto(windowModeToProto(mode)); got != mode {
			t.Errorf("round-trip %q -> %q", mode, got)
		}
	}

	// Empty / unknown strings collapse to normal (fresh-config default).
	if got := windowModeToProto(""); got != desktoppb.WindowMode_WINDOW_MODE_NORMAL {
		t.Errorf("empty mode -> %v, want NORMAL", got)
	}
	if got := windowModeToProto("bogus"); got != desktoppb.WindowMode_WINDOW_MODE_NORMAL {
		t.Errorf("bogus mode -> %v, want NORMAL", got)
	}

	// UNSPECIFIED on the wire maps back to normal.
	if got := windowModeFromProto(desktoppb.WindowMode_WINDOW_MODE_UNSPECIFIED); got != WindowModeNormal {
		t.Errorf("unspecified -> %q, want %q", got, WindowModeNormal)
	}
}

// TestConnectSoloDoesNotBlockLifecycleReadersDuringStartup guards the invariant
// that ConnectSolo releases lifecycleMu across solo.Start: SidecarInfo,
// ProxyHTTP, SendChannelMessage and CreateTunnel take the read lock, so
// holding the write lock across the (multi-second on a cold DB) Hub startup
// wedges every frontend reader. The sibling relay/distributed paths release
// the lock across their blocking I/O for the same reason.
func TestConnectSoloDoesNotBlockLifecycleReadersDuringStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	entered := make(chan struct{})
	app.startSolo = func(ctx context.Context) (*soloRuntime, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	connectDone := make(chan error, 1)
	go func() { connectDone <- app.ConnectSolo(context.Background()) }()
	<-entered

	// ConnectSolo is still parked in startSolo here.
	select {
	case <-connectDone:
		t.Fatal("ConnectSolo returned before the simulated startup completed")
	default:
	}

	// SidecarInfo takes lifecycleMu.RLock(); it must complete while the
	// (simulated) Hub startup is still in flight.
	readerDone := make(chan struct{})
	go func() {
		_ = app.SidecarInfo()
		close(readerDone)
	}()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("SidecarInfo blocked behind the in-flight solo startup")
	}

	// Cleanup's Shutdown cancels a.ctx (and thus connectionCtx), which unblocks
	// startSolo; ConnectSolo then unwinds without ever committing a connection.
}

// TestSwitchModeDoesNotBlockLifecycleReadersDuringSoloStop guards the symmetric
// invariant to the start path above: SwitchMode/Shutdown must release
// lifecycleMu across the blocking solo.Stop() (the Hub's full graceful
// shutdown), or every SidecarInfo/ProxyHTTP/SendChannelMessage reader wedges
// behind the write lock for that whole window.
func TestSwitchModeDoesNotBlockLifecycleReadersDuringSoloStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	entered := make(chan struct{})
	release := make(chan struct{})
	solo := blockingSoloInstance{entered: entered, release: release}
	installTestConnection(app, newHTTPProxy("https://hub.example"), solo, "https://hub.example")
	app.config.Mode = "solo"

	switchDone := make(chan error, 1)
	go func() {
		_, err := app.SwitchMode()
		switchDone <- err
	}()
	<-entered

	// SwitchMode is parked in solo.Stop() here.
	select {
	case <-switchDone:
		t.Fatal("SwitchMode returned before the simulated solo stop completed")
	default:
	}

	// SidecarInfo takes lifecycleMu.RLock(); it must complete while solo.Stop()
	// is still in flight, i.e. lifecycleMu is released across stopSolo.
	readerDone := make(chan struct{})
	go func() {
		_ = app.SidecarInfo()
		close(readerDone)
	}()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("SidecarInfo blocked behind the in-flight solo stop")
	}

	close(release)
	require.NoError(t, <-switchDone)
}

// TestResetTunnelsBumpsChannelEpochAndKeepsConnection pins the identity-change
// reset the frontend fires on logout: ResetTunnels must tear down tunnels and
// invalidate the pooled-channel epoch (so a cached channel authenticated as the
// previous user can never be reused) WITHOUT dropping the sidecar<->Hub
// connection, which the next login still needs.
func TestResetTunnelsBumpsChannelEpochAndKeepsConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	// Simulate an established connection; ResetTunnels must leave it alone.
	connectionCtx, connectionStop := context.WithCancel(context.Background())
	defer connectionStop()
	app.lifecycleMu.Lock()
	app.connection = &desktopConnection{ctx: connectionCtx, stop: connectionStop, hubURL: "https://hub.example"}
	app.lifecycleMu.Unlock()

	before := app.tunnels.currentRevision()
	require.NoError(t, app.ResetTunnels())

	assert.Greater(t, app.tunnels.currentRevision(), before,
		"ResetTunnels must rotate the channel epoch so no cached channel survives the identity change")
	assert.Empty(t, app.ListTunnels(), "no tunnel may survive the reset")
	app.lifecycleMu.RLock()
	defer app.lifecycleMu.RUnlock()
	require.NotNil(t, app.connection, "ResetTunnels must not drop the sidecar<->Hub connection")
	assert.NoError(t, app.connection.ctx.Err(), "the connection lifetime must survive the reset")
}

// TestResetTunnelsRejectedDuringShutdown confirms the reset rides the same
// operation gate as every other RPC: once shutdown begins it is refused
// instead of racing the teardown.
func TestResetTunnelsRejectedDuringShutdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := NewApp("")
	require.NoError(t, app.Shutdown())

	err := app.ResetTunnels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
}
