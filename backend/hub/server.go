// Package hub provides a reusable Hub server that can be embedded
// in other binaries (e.g. the solo/dev all-in-one binary).
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/cleanup"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/frontend"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// crdtShutdownTimeout bounds the CRDT registry's drain, both on a construction
// failure and on runtime shutdown -- one constant so the two paths cannot drift.
const crdtShutdownTimeout = 10 * time.Second

// ServerOption configures optional aspects of a Hub server.
type ServerOption func(*serverOptions)

type serverOptions struct {
	frontendHandler http.Handler
}

// WithFrontendHandler overrides the default frontend handler.
func WithFrontendHandler(h http.Handler) ServerOption {
	return func(o *serverOptions) {
		o.frontendHandler = h
	}
}

// Server is a reusable Hub server instance.
type Server struct {
	cfg               *config.Config
	store             store.Store
	keystore          *keystore.Keystore
	oauthHandler      *service.OAuthHandler
	server            *http.Server
	tcpLn             net.Listener
	localLn           net.Listener
	listenURL         string
	shutdownCh        chan struct{}
	authContexts      *auth.AuthContextRegistry
	workerMgr         *workermgr.Manager
	crdtRegistry      *crdt.Registry
	revocationWatcher *revocationwatcher.Watcher
}

// NewServer creates a new Hub server. It binds the TCP port and local IPC
// listener (to fail fast on conflicts), opens the database, runs migrations,
// bootstraps defaults, and wires all services. Call Serve() to start listening.
func NewServer(cfg *config.Config, opts ...ServerOption) (*Server, error) {
	var so serverOptions
	for _, opt := range opts {
		opt(&so)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// Records each resource as it is acquired, so every failure below closes
	// exactly what is open without restating the subset (see acquiredResources).
	var acquired acquiredResources

	// Bind both listeners before any database work so that concurrent
	// instances (e.g. solo + CLI, or two desktop apps sharing the per-user
	// pipe name) fail fast on conflict without a TOCTOU window. Binding
	// the local listener here also avoids a race where solo.Start's
	// dial-based readiness probe could connect to a foreign listener on
	// the same name (e.g. another running Solo instance) while our own
	// Serve goroutine is still propagating the bind failure.
	var tcpLn net.Listener
	if cfg.Listen != "" {
		var listenErr error
		tcpLn, listenErr = net.Listen("tcp", cfg.Listen)
		if listenErr != nil {
			return nil, fmt.Errorf("listen tcp: %w", listenErr)
		}
	}
	acquired.tcpLn = tcpLn

	listenURL, err := cfg.LocalListenURL()
	if err != nil {
		return nil, acquired.close(
			fmt.Errorf("resolve local-listen URL: %w", err))
	}
	localLn, err := locallisten.Listen(listenURL)
	if err != nil {
		return nil, acquired.close(
			fmt.Errorf("listen local: %w", err))
	}
	acquired.localLn = localLn

	st, err := storeopen.Open(context.Background(), cfg)
	if err != nil {
		return nil, acquired.close(
			fmt.Errorf("open store: %w", err))
	}
	acquired.store = st

	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		return nil, acquired.close(
			fmt.Errorf("load encryption keystore: %w", err))
	}
	slog.Info("encryption keystore loaded", "active_version", ks.ActiveVersion(), "versions", len(ks.Versions()))

	if err := bootstrap.Run(context.Background(), st, cfg.SoloMode); err != nil {
		return nil, acquired.close(
			fmt.Errorf("bootstrap: %w", err))
	}

	// In solo mode, bootstrap just created the solo user; load it once so
	// the auth interceptor and channel relay can synthesize auth without
	// repeating the lookup. A failure here indicates a broken bootstrap or
	// a DB fault — fail startup rather than letting every subsequent request
	// fail with an opaque 500.
	var soloUser *auth.UserInfo
	if cfg.SoloMode {
		u, loadErr := auth.LoadSoloUser(context.Background(), st)
		if loadErr != nil {
			return nil, acquired.close(
				fmt.Errorf("load solo user: %w", loadErr))
		}
		soloUser = u
	}

	shutdownCh := make(chan struct{})

	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)

	apiTokenPepper := ks.Pepper()
	tokenValidator, tvErr := auth.NewTokenValidator(st, apiTokenPepper[:])
	if tvErr != nil {
		return nil, acquired.close(
			fmt.Errorf("create token validator: %w", tvErr))
	}
	authInterceptor, authContexts := auth.NewInterceptorWithTokens(st, soloUser, tokenValidator, cfg.SecureCookies, cfg.EmailVerificationRequired)
	acquired.authContexts = authContexts
	// Let a sliding cookie session (and a rotated bearer, via the credential
	// lifecycle) extend its already-open channels' expiry, not just its leases
	// (which the registry owns directly).
	authContexts.SetChannelExpiryRescheduler(cMgr)
	connectOpts := connect.WithInterceptors(
		auth.NewShutdownInterceptor(shutdownCh),
		metrics.NewInterceptor(),
		auth.NewTimeoutInterceptor(cfg.APITimeout),
		authInterceptor,
	)

	mux := http.NewServeMux()

	// Mail sender: real SMTP when smtp_host is configured, otherwise the
	// no-op disabledSender that returns ErrEmailDisabled. Validation in
	// cfg.Validate() prevents email_verification_required=true paired
	// with an empty smtp_host, so the disabled sender should not be
	// reached during normal operation; it exists as a loud, matchable
	// fallback rather than a silent no-op (which is what the previous
	// StubSender used to do).
	var mailSender mail.Sender
	if cfg.SmtpHost != "" {
		mailSender = mail.NewSMTPSender(mail.SMTPConfig{
			Host:     cfg.SmtpHost,
			Port:     cfg.SmtpPort,
			Username: cfg.SmtpUsername,
			Password: cfg.SmtpPassword,
			From:     cfg.SmtpFromAddress,
			TLSMode:  cfg.SmtpTLSMode,
		})
	} else {
		mailSender = mail.NewDisabledSender()
	}
	// Renderer carries the hub's public URL once at construction so the
	// signup / email-change / resend / worker-registration paths don't
	// each have to thread cfg.BaseURL() through.
	mailRenderer := mail.Renderer{HubURL: cfg.BaseURL()}

	broadcaster := service.NewHubEventBroadcaster(cMgr)
	notifierSvc := notifier.New(st, wMgr, pendingReqs, cfg)

	// Per-org CRDT manager registry. The factory constructs a fully
	// bootstrapped manager (state loaded from disk, ops replayed) on
	// first reference per org. Lifecycle outbox / regular submits
	// route through the same registry. Built early so it can be
	// passed by constructor to every service that drives it (no
	// post-construction injection or initialization-order hazards).
	crdtJournal := service.NewCRDTJournal(st)
	crdtAuth := service.NewCRDTAuthChecker(st)
	crdtRegistry := crdt.NewRegistry(func(ctx context.Context, orgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(orgID, crdtJournal, crdtAuth, slog.Default(), time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, slog.Default())
	acquired.crdtRegistry = crdtRegistry

	connectorSvc := service.NewWorkerConnectorService(st, wMgr, cMgr, broadcaster, pendingReqs, notifierSvc, crdtRegistry, shutdownCh)
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, connectOpts)
	mux.Handle(connectorPath, connectorHandler)
	// One delegation-scope cache shared by SubmitOps (resolve) and worker
	// deregistration (evict); see auth.DelegationScopeCache.
	scopeCache := auth.NewDelegationScopeCache(st)
	mgmtSvc := service.NewWorkerManagementService(st, wMgr, broadcaster, notifierSvc, mailSender, mailRenderer, cfg, scopeCache)
	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(mgmtSvc, connectOpts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, authContexts)
	lifecycle := auth.NewCredentialLifecycleEffects(authContexts, channelSvc, cMgr)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, connectOpts)
	mux.Handle(channelPath, channelHandler)

	authSvc := service.NewAuthService(st, cfg, lifecycle, ks, mailSender, mailRenderer)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connectOpts)
	mux.Handle(authPath, authHandler)

	// WebSocket endpoint for encrypted channel relay (Frontend <-> Worker).
	channelRelay := service.NewChannelRelayHandler(st, wMgr, cMgr, authContexts, soloUser, cfg.SecureCookies).
		WithTokenValidator(tokenValidator).
		WithChannelCloseEnqueuer(channelSvc)
	mux.Handle("/ws/channel", channelRelay)

	// OAuth HTTP endpoints.
	oauthHandler := service.NewOAuthHandler(st, cfg, ks)
	oauthHandler.RegisterRoutes(mux)

	// CLI auth HTTP endpoints (PKCE local-redirect + RFC 8628 device code).
	apiAuthHandler := service.NewAPIAuthHandler(st, tokenValidator, lifecycle, cfg.BaseURL())
	apiAuthHandler.RegisterRoutes(mux)

	// Worker-issued delegation token mint/revoke endpoints. The credential
	// lifecycle effects are wired so revoking a delegation token evicts its
	// cached validation and authenticated leases and tears down any open E2EE
	// channels authorized by it (lifecycle.BearerRevoked).
	delegationHandler := service.NewWorkerDelegationHandler(st, tokenValidator, lifecycle)
	delegationHandler.RegisterRoutes(mux)

	// UserService drives credential-rotation paths (ChangePassword) through the
	// shared lifecycle, whose RevokeUserPreservingSession hard-closes every
	// channel a user owns alongside the delegation-token revocation.
	userSvc := service.NewUserService(st, cfg, lifecycle, mailSender, mailRenderer)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, connectOpts)
	mux.Handle(userPath, userHandler)

	sectionSvc := service.NewSectionService(st)
	sectionPath, sectionHandler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, connectOpts)
	mux.Handle(sectionPath, sectionHandler)

	workspaceSvc := service.NewWorkspaceService(st, crdtRegistry, channelSvc)
	workspacePath, workspaceHandler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaceSvc, connectOpts)
	mux.Handle(workspacePath, workspaceHandler)

	crdtSvc := service.NewCRDTService(st, crdtRegistry, slog.Default(), scopeCache)
	crdtPath, crdtHandler := leapmuxv1connect.NewOrgCRDTHandler(crdtSvc, connectOpts)
	mux.Handle(crdtPath, crdtHandler)

	// WebSocket endpoint for the CRDT event stream. Frontend opens a
	// single `/ws/orgevents?org_id=...&workspace_ids=...` connection
	// per org session and reads length-prefixed `WatchOrgEvent` proto
	// frames. This is the sole transport for org-event subscriptions
	// — the OrgCRDT ConnectRPC service exposes unary calls only
	// (SubmitOps, UpdatePresence). The WS path bypasses HTTP/1.1
	// chunked-stream buffering hazards (some proxies / Tauri's
	// buffered fetch) that motivated retiring the streaming RPC.
	orgEventsHandler := service.NewOrgEventsHandler(st, crdtRegistry, authContexts, soloUser, cfg.SecureCookies).
		WithTokenValidator(tokenValidator)
	mux.Handle("/ws/orgevents", orgEventsHandler)

	reconcilerSvc := service.NewWorkerReconcilerService(st)
	reconcilerPath, reconcilerHandler := leapmuxv1connect.NewWorkerReconcilerServiceHandler(reconcilerSvc, connectOpts)
	mux.Handle(reconcilerPath, reconcilerHandler)

	// Prometheus metrics endpoint.
	mux.Handle("/metrics", promhttp.Handler())

	// Unauthenticated /version endpoint. Exposes the hub's build
	// identity so `leapmux remote version` can report both CLI and
	// hub versions without needing an authenticated session.
	mux.HandleFunc("/version", versionHandler)

	// Frontend handler.
	if so.frontendHandler != nil {
		mux.Handle("/", so.frontendHandler)
	} else if cfg.DevFrontend != "" {
		devProxy, proxyErr := frontend.DevProxy(cfg.DevFrontend)
		if proxyErr != nil {
			return nil, acquired.close(
				fmt.Errorf("create dev proxy: %w", proxyErr))
		}
		mux.Handle("/", devProxy)
		slog.Info("dev mode: proxying frontend", "target", cfg.DevFrontend)
	} else {
		mux.Handle("/", frontend.Handler())
	}

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Handler:           logging.HTTPMiddleware(metrics.HTTPMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		Protocols:         protocols,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: 1000,
		},
	}

	// Watcher for cross-process revocations: admin CLI commands mutate
	// auth state and record durable revocation events, and the watcher
	// publishes + consumes that stream to drive the matching cache
	// eviction + channel teardown. In-process callers
	// (UserService.ChangePassword, the per-token revoke handler)
	// continue to invoke the close paths inline so they observe
	// zero-latency revocation; the watcher is the cross-process
	// safety net.
	revWatcher := revocationwatcher.New(st, lifecycle)

	return &Server{
		cfg:               cfg,
		store:             st,
		keystore:          ks,
		oauthHandler:      oauthHandler,
		server:            server,
		tcpLn:             tcpLn,
		localLn:           localLn,
		listenURL:         listenURL,
		shutdownCh:        shutdownCh,
		authContexts:      authContexts,
		workerMgr:         wMgr,
		crdtRegistry:      crdtRegistry,
		revocationWatcher: revWatcher,
	}, nil
}

// Store returns the Hub's store for direct database access
// (e.g. for solo/dev auto-registration).
func (s *Server) Store() store.Store {
	return s.store
}

// WorkerCredentials holds the credentials for a registered worker.
type WorkerCredentials struct {
	WorkerID  string
	AuthToken string
}

// RegisterWorker creates a worker record directly in the database,
// bypassing the normal registration-key flow. This is the in-process
// path used by the solo/dev binary to auto-register a co-located worker:
// since the caller is already running inside the same process as the
// hub, presenting a bearer token to a local RPC would just be
// security theatre. Outside solo mode, all worker registration must go
// through WorkerConnectorService.Register with a real registration key.
//
// Rows created here are flagged auto_registered so the deregister
// handler refuses them — re-registration on next launch would just
// undo the user's action while the running worker process noisily
// exits with "invalid auth token" in between.
func (s *Server) RegisterWorker(ctx context.Context, registeredBy string) (*WorkerCredentials, error) {
	workerID := id.Generate()
	authToken := id.Generate()

	if err := s.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       authToken,
		RegisteredBy:    registeredBy,
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
		AutoRegistered:  true,
	}); err != nil {
		return nil, fmt.Errorf("create worker: %w", err)
	}

	return &WorkerCredentials{
		WorkerID:  workerID,
		AuthToken: authToken,
	}, nil
}

// GetWorkerOwner returns the id of the user who registered workerID, erroring if
// the worker is unknown (or soft-deleted, which GetByID filters).
//
// It returns the owner rather than just an existence check because workers.registered_by
// is the AUTHORITY on who owns a worker -- it is NOT NULL, set at registration, and
// the fact every machine-scoped gate (requireWorkerOwner) keys on. A caller that has
// the worker's id therefore never needs to source the owner from anywhere else, and
// must not: a local state file can lag or lose it, and "whoever the admin is now" is
// a different question that happens to share an answer on a fresh single-user install.
func (s *Server) GetWorkerOwner(ctx context.Context, workerID string) (string, error) {
	w, err := s.store.Workers().GetByID(ctx, workerID)
	if err != nil {
		return "", err
	}
	return w.RegisteredBy, nil
}

// GetAdminUser returns the ID and org ID of the user to attribute
// auto-registered local workers to. In solo mode this is the bootstrapped
// solo user. In dev/hub mode this is the first admin user registered via the
// /setup flow; the caller gets store.ErrNotFound when no admin exists yet and
// is expected to retry once one does.
func (s *Server) GetAdminUser(ctx context.Context) (userID, orgID string, err error) {
	if s.cfg.SoloMode {
		user, err := s.store.Users().GetByUsername(ctx, usernames.Solo)
		if err != nil {
			return "", "", fmt.Errorf("get solo user: %w", err)
		}
		return user.ID, user.OrgID, nil
	}

	user, err := s.store.Users().GetFirstAdmin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("get first admin: %w", err)
	}
	return user.ID, user.OrgID, nil
}

// Serve starts the Hub server on the listeners that NewServer pre-bound.
// It blocks until ctx is cancelled, then performs graceful shutdown.
func (s *Server) Serve(ctx context.Context) error {
	tcpLn := s.tcpLn
	localLn := s.localLn
	listenURL := s.listenURL
	serveCtx, cancelServe := context.WithCancelCause(ctx)
	defer cancelServe(nil)

	// Register the watcher before starting listeners or other background work.
	// Without the singleton runtime lease, serving authenticated traffic would let
	// cleanup compact revocations this process has not observed.
	if err := s.revocationWatcher.SeedCursor(serveCtx); err != nil {
		s.authContexts.Stop()
		s.crdtRegistry.Shutdown(crdtShutdownTimeout)
		watcherCloseCtx, cancelWatcherClose := context.WithTimeout(context.Background(), 10*time.Second)
		watcherCloseErr := s.revocationWatcher.Close(watcherCloseCtx)
		cancelWatcherClose()
		return serverTeardownErrors{
			primary:       fmt.Errorf("seed revocation watcher: %w", err),
			tcpListener:   closeServerListener(tcpLn),
			localListener: closeServerListener(localLn),
			httpClose:     s.server.Close(),
			watcherClose:  watcherCloseErr,
			storeClose:    s.store.Close(),
		}.finalize()
	}

	// Start background OAuth token refresh.
	s.oauthHandler.StartTokenRefresh(serveCtx)

	// Start periodic cleanup of soft-deleted records.
	cleanup.StartLoop(serveCtx, s.store)

	// Start the revocation watcher: publishes and consumes the durable
	// revocation stream so admin-CLI mutations land in the hub's
	// in-memory caches and channelmgr without IPC. Seed past events that
	// predate this process so the first sweep only handles fresh work.
	s.revocationWatcher.StartLoop(serveCtx)

	shutdownDone := make(chan serverTeardownErrors, 1)
	go func() {
		<-serveCtx.Done()
		slog.Info("hub shutting down...")

		// 1. Reject all new RPCs and stop background tasks.
		close(s.shutdownCh)
		s.authContexts.Stop()

		// 2. Notify connected workers to delay reconnection.
		notifyCtx, cancelNotify := context.WithTimeout(context.Background(), 2*time.Second)
		s.workerMgr.NotifyShutdown(notifyCtx, 10)
		cancelNotify()

		// 3. Drain in-flight HTTP requests, then force-close any connections
		// the drain left behind. On Windows each accepted named-pipe
		// connection is its own pipe instance; if any survive, the next
		// ListenPipe with FILE_FLAG_FIRST_PIPE_INSTANCE on the same name
		// fails with ERROR_ACCESS_DENIED.
		//
		// http.Server.Close() only iterates net/http's own activeConn map.
		// h2c-upgraded connections (worker bidi gRPC streams, channel-relay
		// websockets) are hijacked and handed off to http2.Server, which
		// removes them from activeConn — so http.Server.Close() can't reach
		// them. locallisten.CloseAccepted closes the underlying pipe handles
		// directly via the listener's own accepted-connection tracking,
		// which is the only level that sees every accepted conn.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpShutdownErr := s.server.Shutdown(shutdownCtx)
		httpCloseErr := s.server.Close()
		locallisten.CloseAccepted(s.localLn)

		shutdownDone <- serverTeardownErrors{
			httpShutdown: httpShutdownErr,
			httpClose:    httpCloseErr,
		}
	}()

	listenerCount := 1 // local listener always present
	if tcpLn != nil {
		listenerCount = 2
	}
	type listenerResult struct {
		isTCP bool
		err   error
	}
	errCh := make(chan listenerResult, listenerCount)

	if tcpLn != nil {
		go func() { errCh <- listenerResult{isTCP: true, err: s.server.Serve(tcpLn)} }()
	}
	go func() { errCh <- listenerResult{err: s.server.Serve(localLn)} }()

	if tcpLn != nil {
		slog.Info("hub listening", "listen", s.cfg.Listen, "local", listenURL)
	} else {
		slog.Info("hub listening", "local", listenURL)
	}

	var teardownErrs serverTeardownErrors
	recordListenerResult := func(result listenerResult) {
		if result.err == nil || errors.Is(result.err, http.ErrServerClosed) {
			return
		}
		listenerErr := fmt.Errorf("serve: %w", result.err)
		if result.isTCP {
			teardownErrs.tcpListener = errors.Join(teardownErrs.tcpListener, listenerErr)
		} else {
			teardownErrs.localListener = errors.Join(teardownErrs.localListener, listenerErr)
		}
	}
	remainingListeners := listenerCount
	select {
	case result := <-errCh:
		remainingListeners--
		recordListenerResult(result)
		cancelServe(result.err)
	case err := <-s.revocationWatcher.Errors():
		teardownErrs.primary = fmt.Errorf("revocation watcher failed: %w", err)
		cancelServe(err)
	case <-serveCtx.Done():
	}

	// Shutdown closes every listener. Drain their results before releasing
	// the store so no handler can race a closed database.
	for i := 0; i < remainingListeners; i++ {
		recordListenerResult(<-errCh)
	}

	// 5. Wait for the shutdown goroutine to complete.
	shutdownErrs := <-shutdownDone
	teardownErrs.httpShutdown = shutdownErrs.httpShutdown
	teardownErrs.httpClose = shutdownErrs.httpClose

	// 6. Stop CRDT managers while their journal store is still available.
	s.crdtRegistry.Shutdown(crdtShutdownTimeout)

	// 7. Stop the watcher before removing its durable cursor, then close the
	// store. A bounded context prevents a broken backend from hanging shutdown.
	watcherCloseCtx, cancelWatcherClose := context.WithTimeout(context.Background(), 10*time.Second)
	teardownErrs.watcherClose = s.revocationWatcher.Close(watcherCloseCtx)
	cancelWatcherClose()
	// A watcher lease-loss can race a listener error into the select above; when
	// the listener case wins, the fatal watcher cause is left buffered in Errors()
	// and would otherwise be discarded, leaving the aggregate reporting only the
	// listener error and the watcher's separate Close() error -- not the lease-loss
	// that is the most process-fatal cause. Close has now drained the watcher's
	// goroutines, so any pending fatal is available; fold it in.
	teardownErrs.foldPendingWatcherError(s.revocationWatcher.Errors())
	teardownErrs.storeClose = s.store.Close()
	return teardownErrs.finalize()
}

// foldPendingWatcherError folds a still-buffered fatal watcher error into
// primary. Serve's teardown select consumes exactly one of {listener error,
// watcher error, ctx-done}; a watcher lease-loss racing a listener error is left
// unread in the buffered Errors() channel, so the aggregate would otherwise drop
// the most process-fatal cause. This is a non-blocking drain: it is a no-op when
// the select already consumed the watcher error (primary set), when the store
// construction/seed path set primary, or when no fatal occurred.
func (e *serverTeardownErrors) foldPendingWatcherError(watcherErrors <-chan error) {
	if e.primary != nil {
		return
	}
	select {
	case watcherErr := <-watcherErrors:
		if watcherErr != nil {
			e.primary = fmt.Errorf("revocation watcher failed: %w", watcherErr)
		}
	default:
	}
}

// serverTeardownErrors is the single error boundary for acquired Hub
// resources. Construction failures, watcher startup failures, and normal
// runtime shutdown all populate the resources they owned so no cleanup error
// is silently dropped.
type serverTeardownErrors struct {
	primary       error
	tcpListener   error
	localListener error
	httpShutdown  error
	httpClose     error
	watcherClose  error
	storeClose    error
}

func (e serverTeardownErrors) finalize() error {
	return errors.Join(
		e.primary,
		errwrap.Wrap(e.tcpListener, "TCP listener"),
		errwrap.Wrap(e.localListener, "local listener"),
		errwrap.Wrap(e.httpShutdown, "shut down HTTP server"),
		errwrap.Wrap(e.httpClose, "force-close HTTP server"),
		errwrap.Wrap(e.watcherClose, "close revocation watcher"),
		errwrap.Wrap(e.storeClose, "close store"),
	)
}

// acquiredResources tracks the Hub resources NewServer has acquired so far, so
// a construction failure closes exactly what was opened and aggregates every
// cleanup error.
//
// NewServer keeps ONE of these and records each resource as it is obtained, so a
// failure site says only "close whatever is open" (`acquired.close(err)`) rather
// than restating the subset it believes is open. Re-listing the subset per site
// made every new acquisition step -- or any reordering of the existing ones -- a
// silent chance to leak a listener or a store handle by forgetting to extend the
// sites below it; the accumulator can only ever describe what was actually
// acquired. Nil fields are no-ops.
//
// It covers EVERY resource NewServer acquires, not just the cheap ones. The two
// that hold goroutines -- the auth-context registry and the CRDT registry -- were
// hand-closed at the one failure site that happened to sit below them, which is the
// same "remember to extend the sites below" trap in miniature: a new acquired.close
// site added after them would have leaked both.
type acquiredResources struct {
	tcpLn        net.Listener
	localLn      net.Listener
	store        store.Store
	authContexts *auth.AuthContextRegistry
	crdtRegistry *crdt.Registry
}

// close releases the acquired resources, joining the primary construction
// error with every cleanup error.
//
// Order mirrors reverse acquisition: the two subsystems that hold goroutines and
// live state come down before the store they read through, which comes down before
// the listeners.
func (r acquiredResources) close(primary error) error {
	if r.crdtRegistry != nil {
		r.crdtRegistry.Shutdown(crdtShutdownTimeout)
	}
	if r.authContexts != nil {
		r.authContexts.Stop()
	}
	return serverTeardownErrors{
		primary:       primary,
		storeClose:    closeStore(r.store),
		localListener: closeServerListener(r.localLn),
		tcpListener:   closeServerListener(r.tcpLn),
	}.finalize()
}

func closeStore(st store.Store) error {
	if st == nil {
		return nil
	}
	return st.Close()
}

func closeServerListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	// Return the raw error; finalize() adds the "TCP listener" / "local
	// listener" prefix, mirroring closeStore so every teardown error reads at
	// a single depth instead of nesting ("TCP listener: close: <err>").
	return listener.Close()
}
