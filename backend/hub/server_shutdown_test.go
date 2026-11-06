package hub

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// A Hub that stops because something failed must SAY so, at the moment it
// stops. Reporting the cause only through the aggregate error Serve returns is
// what left a user's bug report showing a Hub that stopped for no stated
// reason.
func TestLogShutdownCauseReportsAFatalCause(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	fatal := errors.New("revocation watcher lease lost: holder h1")
	cancel(fatal)

	logShutdownCause(ctx)

	out := logs.String()
	assert.Contains(t, out, "hub shutting down after a fatal error")
	assert.Contains(t, out, "revocation watcher lease lost", "the cause must be readable without the returned error")
	assert.Contains(t, out, "level=ERROR", "a fatal stop must not be logged at INFO alongside ordinary shutdowns")
}

// An exit somebody asked for carries nothing worth a field, and must not be
// dressed up as a failure: a Ctrl-C that logged at ERROR would train operators
// to ignore the level that matters.
func TestLogShutdownCauseStaysQuietForARequestedStop(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil) // what a Ctrl-C reaching Serve's parent context looks like

	logShutdownCause(ctx)

	out := logs.String()
	assert.Contains(t, out, "hub shutting down...")
	assert.NotContains(t, out, "fatal")
	assert.NotContains(t, out, "level=ERROR")
}

type closeErrorListener struct {
	err error
}

func (*closeErrorListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *closeErrorListener) Close() error            { return l.err }
func (*closeErrorListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestServerTeardownErrorsPreserveServeAndCleanupFailures(t *testing.T) {
	serveErr := errors.New("serve failed")
	tcpListenerErr := errors.New("tcp listener failed")
	localListenerErr := errors.New("local listener failed")
	httpShutdownErr := errors.New("http shutdown failed")
	httpCloseErr := errors.New("http close failed")
	watcherErr := errors.New("watcher close failed")
	storeErr := errors.New("store close failed")

	err := serverTeardownErrors{
		primary:       serveErr,
		tcpListener:   tcpListenerErr,
		localListener: localListenerErr,
		httpShutdown:  httpShutdownErr,
		httpClose:     httpCloseErr,
		watcherClose:  watcherErr,
		storeClose:    storeErr,
	}.finalize()
	require.ErrorIs(t, err, serveErr)
	require.ErrorIs(t, err, tcpListenerErr)
	require.ErrorIs(t, err, localListenerErr)
	require.ErrorIs(t, err, httpShutdownErr)
	require.ErrorIs(t, err, httpCloseErr)
	require.ErrorIs(t, err, watcherErr)
	require.ErrorIs(t, err, storeErr)
	require.ErrorContains(t, err, "TCP listener")
	require.ErrorContains(t, err, "local listener")
	require.ErrorContains(t, err, "shut down HTTP server")
	require.ErrorContains(t, err, "force-close HTTP server")
	require.ErrorContains(t, err, "close revocation watcher")
	require.ErrorContains(t, err, "close store")
}

func TestServerTeardownErrorsReturnNilWithoutFailures(t *testing.T) {
	require.NoError(t, (serverTeardownErrors{}).finalize())
}

// A watcher lease-loss that raced a listener error into Serve's select is left
// buffered in Errors() when the listener case wins; foldPendingWatcherError must
// recover it into primary so the aggregate reports the most process-fatal cause.
func TestFoldPendingWatcherErrorRecoversRacedLeaseLoss(t *testing.T) {
	leaseLost := errors.New("revocation watcher lease lost")
	watcherErrors := make(chan error, 1)
	watcherErrors <- leaseLost

	// The listener case won the select, so primary carries only the listener error.
	teardownErrs := serverTeardownErrors{tcpListener: errors.New("serve: use of closed network connection")}
	teardownErrs.foldPendingWatcherError(watcherErrors)

	require.ErrorIs(t, teardownErrs.finalize(), leaseLost, "the raced lease-loss must not be dropped")
	require.ErrorContains(t, teardownErrs.finalize(), "revocation watcher failed")
}

// When the select already consumed the watcher error (primary set),
// foldPendingWatcherError must NOT drain the channel again or overwrite it.
func TestFoldPendingWatcherErrorSkipsWhenPrimarySet(t *testing.T) {
	watcherErrors := make(chan error, 1)
	watcherErrors <- errors.New("a second fatal that must be ignored")

	primary := errors.New("revocation watcher failed: original cause")
	teardownErrs := serverTeardownErrors{primary: primary}
	teardownErrs.foldPendingWatcherError(watcherErrors)

	require.Equal(t, primary, teardownErrs.primary, "an already-set primary must be preserved")
	require.Len(t, watcherErrors, 1, "the channel must not be drained when primary is already set")
}

// With no fatal buffered, foldPendingWatcherError must be a non-blocking no-op.
func TestFoldPendingWatcherErrorNoOpWhenEmpty(t *testing.T) {
	teardownErrs := serverTeardownErrors{}
	teardownErrs.foldPendingWatcherError(make(chan error, 1))
	require.NoError(t, teardownErrs.finalize())
}

func TestConstructionServerErrorsPreserveListenerCloseFailures(t *testing.T) {
	primaryErr := errors.New("construction failed")
	tcpErr := errors.New("close TCP failed")
	localErr := errors.New("close local failed")

	err := acquiredResources{
		tcpLns:   []net.Listener{&closeErrorListener{err: tcpErr}},
		localLns: []net.Listener{&closeErrorListener{err: localErr}},
	}.close(primaryErr)
	require.ErrorIs(t, err, primaryErr)
	require.ErrorIs(t, err, tcpErr)
	require.ErrorIs(t, err, localErr)
	require.ErrorContains(t, err, "TCP listener")
	require.ErrorContains(t, err, "local listener")
}

// A hijacked WebSocket handler must be joined before the store closes: Serve's
// drain waits for it, so a handler that reads the store after Serve returned
// still finds it open.
func TestServeWaitsForARunningWebSocketHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := newShutdownTestServer(t)
		release := make(chan struct{})
		releaseFn := sync.OnceFunc(func() { close(release) })
		defer releaseFn()
		entered := make(chan struct{})
		readDone := make(chan error, 1)
		go trackWebSocket(srv.webSockets, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			_, err := srv.store.Settings().Get(r.Context(), "nonexistent-key")
			readDone <- err
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))
		<-entered
		cancel, served := serveForShutdownTest(t, srv)
		cancel()
		synctest.Wait()
		select {
		case err := <-served:
			t.Fatalf("Serve returned before the WebSocket handler finished: %v", err)
		default:
		}
		releaseFn()
		assert.ErrorIs(t, <-readDone, store.ErrNotFound, "the joined handler must read an open store")
		require.NoError(t, <-served)
	})
}

// Both WebSocket routes are wrapped: a closed gate answers 503 on each of
// them, so neither upgrade can hijack a connection past the shutdown.
func TestNewServerTracksBothWebSocketRoutes(t *testing.T) {
	srv := startTestServer(t, &config.Config{})
	srv.webSockets.Close(nil)

	for _, route := range []string{contracts.WSRouteChannel, contracts.WSRouteUserEvents} {
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "%s must answer 503 behind a closed gate", route)
	}
}

// joinLoops waits under one deadline for the whole set: one already-stopped
// loop must not extend the wait past the budget, and one open loop must
// surface the expiry as exactly one Warn.
func TestJoinLoopsWaitsUnderOneDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := make(chan struct{})
		close(closed)
		open := make(chan struct{})
		defer close(open)

		previousLogger := slog.Default()
		var warns atomic.Int32
		slog.SetDefault(slog.New(&warnCountingHandler{warns: &warns}))
		t.Cleanup(func() { slog.SetDefault(previousLogger) })

		done := make(chan struct{})
		go func() {
			joinLoops(time.Second, []<-chan struct{}{closed, open})
			close(done)
		}()

		// The budget is fake time in the bubble, so the deadline arrives
		// without spending real seconds.
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("joinLoops did not return at the deadline")
		}
		assert.Equal(t, int32(1), warns.Load(), "the expiry must surface as exactly one Warn")
	})
}

// The join must precede the store close: a loop that reads the store after
// Serve returned must still find it open.
func TestServeWaitsForABackgroundLoopBeforeClosingTheStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := newShutdownTestServer(t)
		release := make(chan struct{})
		releaseFn := sync.OnceFunc(func() { close(release) })
		defer releaseFn()
		readDone := make(chan error, 1)
		loop := make(chan struct{})
		go func() {
			defer close(loop)
			<-release
			_, err := srv.store.Settings().Get(t.Context(), "nonexistent-key")
			readDone <- err
		}()
		srv.backgroundLoops = append(srv.backgroundLoops, loop)
		cancel, served := serveForShutdownTest(t, srv)
		cancel()
		synctest.Wait()
		select {
		case err := <-served:
			t.Fatalf("Serve returned before its background loop finished: %v", err)
		default:
		}
		releaseFn()
		assert.ErrorIs(t, <-readDone, store.ErrNotFound, "the joined loop must read an open store")
		require.NoError(t, <-served)
	})
}

// warnCountingHandler counts Warn records, so a test can assert the expiry
// logged once.
type warnCountingHandler struct {
	warns *atomic.Int32
	mu    sync.Mutex
}

func (h *warnCountingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *warnCountingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level == slog.LevelWarn {
		h.warns.Add(1)
	}
	return nil
}

func (h *warnCountingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *warnCountingHandler) WithGroup(name string) slog.Handler       { return h }

// Renewal must keep running through the whole drain: with renewal derived
// from the serve context, it stopped when shutdown began, and the 30-second
// lease could lapse while the CRDT drain (up to 10 seconds) still wrote.
// The test keeps the drain observable:
//   - A 200ms lease renews every 50ms.
//   - A manager parked in a test seed holds the CRDT drain open.
//   - A second SQLite connection reads lease_expires_at during the drain.
func TestServeRenewsTheLeaseDuringTheDrain(t *testing.T) {
	cfg := &config.Config{
		DataDir: t.TempDir(),
		Listen:  []string{locallistentest.UniqueListenURL(t, "lmx-hub-test")},
		Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
	}
	srv, err := NewServer(cfg, withRevocationWatcherOptions(200*time.Millisecond))
	require.NoError(t, err)

	// The drain holder: a CRDT manager whose goroutine parks until the
	// release, so crdtRegistry.Shutdown stays inside its deadline for as
	// long as this test observes.
	m, err := srv.crdtRegistry.Get(context.Background(), "drain-holder-user")
	require.NoError(t, err)
	gate := make(chan struct{})
	var gateOnce sync.Once
	gateRelease := func() { gateOnce.Do(func() { close(gate) }) }
	t.Cleanup(gateRelease)
	seedEntered := make(chan struct{})
	go m.SeedStateForTest(func(*leapmuxv1.UserCrdtState) { close(seedEntered); <-gate })
	<-seedEntered

	cancelServe, served := serveForShutdownTest(t, srv)

	// Begin the shutdown and observe lease_expires_at while the drain runs.
	cancelServe()

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "hub.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expiry := func() (time.Time, bool) {
		var raw any
		if err := db.QueryRow("SELECT lease_expires_at FROM hub_runtime_lease WHERE singleton_id = 1").Scan(&raw); err != nil {
			return time.Time{}, false
		}
		switch v := raw.(type) {
		case string:
			parsed, parseErr := time.Parse("2006-01-02T15:04:05.000Z", v)
			if parseErr != nil {
				return time.Time{}, false
			}
			return parsed, true
		case time.Time:
			return v, true
		}
		return time.Time{}, false
	}

	before, ok := expiry()
	require.True(t, ok, "the lease row must exist when the drain begins")

	// The CRDT drain is parked; the renewal ticker must advance the stored
	// expiry inside it.
	assert.Eventually(t, func() bool {
		after, live := expiry()
		return live && after.After(before)
	}, 8*time.Second, 50*time.Millisecond,
		"lease_expires_at must advance while the drain runs")

	gateRelease()
	assert.NoError(t, <-served)
}

// servingCtx carries the detached work of every request, so it must end on each
// path out of Serve, also when the Hub never served.
func TestServeEndsServingCtxWhenTheLeaseIsRefused(t *testing.T) {
	cfg := &config.Config{
		DataDir: t.TempDir(),
		Listen:  []string{locallistentest.UniqueListenURL(t, "lmx-hub-test")},
		Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	_, err = srv.store.RevocationEvents().AcquireHubRuntimeLease(context.Background(), store.AcquireHubRuntimeLeaseParams{
		HolderID: "rival-holder", LeaseDuration: time.Hour,
	})
	require.NoError(t, err)

	require.ErrorIs(t, srv.Serve(context.Background()), store.ErrHubAlreadyRunning)

	require.ErrorIs(t, srv.servingCtx.Err(), context.Canceled)
}

// A credential notice can outlive every other drain, so Serve must join it, and
// servingCtx must stay live until it ends. On return servingCtx must have ended.
func TestServeJoinsACredentialNoticeBeforeItEndsServingCtx(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &parkedMailSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
		finishNotice := sync.OnceFunc(func() { close(sender.release) })
		defer finishNotice()
		srv := newShutdownTestServer(t, withMailSender(sender))
		api := httptest.NewServer(srv.server.Handler)
		defer api.Close()
		identity := newWiringIdentity(t, srv, api.URL)
		identity.mintThroughAdmin(t)
		<-sender.entered
		// A live HTTP listener prevents synctest from reaching quiescence. The
		// notice already runs, so its issuer no longer needs the HTTP fixture.
		api.Close()

		cancel, served := serveForShutdownTest(t, srv)
		cancel()
		synctest.Wait()
		select {
		case err := <-served:
			t.Fatalf("Serve returned while a notice still ran: %v", err)
		default:
		}
		assert.False(t, srv.credentialNotices.Wait(0, ""), "shutdown must retain the active notice")
		assert.NoError(t, srv.servingCtx.Err(), "servingCtx must outlive the notice join")
		finishNotice()
		require.NoError(t, <-served)
		assert.ErrorIs(t, srv.servingCtx.Err(), context.Canceled)
	})
}

func newShutdownTestServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	srv, err := NewServer(&config.Config{DataDir: t.TempDir(), Listen: []string{locallistentest.UniqueListenURL(t, "lmx-hub-test")}, Storage: config.StorageConfig{Type: config.StorageTypeSQLite}}, opts...)
	require.NoError(t, err)
	return srv
}

func serveForShutdownTest(t *testing.T, srv *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	select {
	case <-srv.LeaseHeld():
	case err := <-served:
		t.Fatalf("Serve ended before it held the runtime lease: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("Serve never acquired its runtime lease")
	}
	return cancel, served
}

// A construction failure has no Serve to end servingCtx, so its teardown must.
func TestAcquiredResourcesCloseEndsServingCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, acquiredResources{cancelServing: cancel}.close(nil))

	require.ErrorIs(t, ctx.Err(), context.Canceled)
}
