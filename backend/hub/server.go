// Package hub provides a reusable Hub server that can be embedded
// in other binaries (e.g. the solo/dev all-in-one binary).
package hub

import (
	"context"
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
	"github.com/leapmux/leapmux/internal/hub/frontend"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

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
	cfg          *config.Config
	store        store.Store
	keystore     *keystore.Keystore
	oauthHandler *service.OAuthHandler
	server       *http.Server
	tcpLn        net.Listener
	shutdownCh   chan struct{}
	sessionCache *auth.SessionCache
	workerMgr    *workermgr.Manager
}

// NewServer creates a new Hub server. It binds the TCP port (to fail
// fast on conflicts), opens the database, runs migrations, bootstraps
// defaults, and wires all services. Call Serve() to start listening.
func NewServer(cfg *config.Config, opts ...ServerOption) (*Server, error) {
	var so serverOptions
	for _, opt := range opts {
		opt(&so)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// Bind the TCP port before any database work so that concurrent
	// instances (e.g. solo + CLI) fail fast on port conflict without
	// a TOCTOU window.
	var tcpLn net.Listener
	if cfg.Addr != "" {
		var listenErr error
		tcpLn, listenErr = net.Listen("tcp", cfg.Addr)
		if listenErr != nil {
			return nil, fmt.Errorf("listen tcp: %w", listenErr)
		}
	}

	closeTCP := func() {
		if tcpLn != nil {
			_ = tcpLn.Close()
		}
	}

	st, err := storeopen.Open(context.Background(), cfg)
	if err != nil {
		closeTCP()
		return nil, fmt.Errorf("open store: %w", err)
	}

	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		_ = st.Close()
		closeTCP()
		return nil, fmt.Errorf("load encryption keystore: %w", err)
	}
	slog.Info("encryption keystore loaded", "active_version", ks.ActiveVersion(), "versions", len(ks.Versions()))

	if err := bootstrap.Run(context.Background(), st, cfg.SoloMode); err != nil {
		_ = st.Close()
		closeTCP()
		return nil, fmt.Errorf("bootstrap: %w", err)
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
			_ = st.Close()
			closeTCP()
			return nil, fmt.Errorf("load solo user: %w", loadErr)
		}
		soloUser = u
	}

	shutdownCh := make(chan struct{})

	wMgr := workermgr.New()
	var cMgrOpts []channelmgr.Option
	if cfg.MaxMessageSize > 0 {
		cMgrOpts = append(cMgrOpts, channelmgr.WithMaxMessageSize(cfg.MaxMessageSize))
	}
	if cfg.MaxIncompleteChunked > 0 {
		cMgrOpts = append(cMgrOpts, channelmgr.WithMaxIncompleteChunked(cfg.MaxIncompleteChunked))
	}
	cMgr := channelmgr.New(cMgrOpts...)
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)

	authInterceptor, sessionCache := auth.NewInterceptor(st, soloUser, cfg.SecureCookies, cfg.EmailVerificationRequired)
	connectOpts := connect.WithInterceptors(
		auth.NewShutdownInterceptor(shutdownCh),
		metrics.NewInterceptor(),
		auth.NewTimeoutInterceptor(cfg.APITimeout),
		authInterceptor,
	)

	mux := http.NewServeMux()

	mailSender := mail.NewStubSender()

	authSvc := service.NewAuthService(st, cfg, sessionCache, ks, mailSender)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connectOpts)
	mux.Handle(authPath, authHandler)

	broadcaster := service.NewHubEventBroadcaster(cMgr)
	notifierSvc := notifier.New(st, wMgr, pendingReqs, cfg)

	connectorSvc := service.NewWorkerConnectorService(st, wMgr, cMgr, broadcaster, pendingReqs, notifierSvc, shutdownCh)
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, connectOpts)
	mux.Handle(connectorPath, connectorHandler)
	mgmtSvc := service.NewWorkerManagementService(st, wMgr, broadcaster, notifierSvc, mailSender)
	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(mgmtSvc, connectOpts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, connectOpts)
	mux.Handle(channelPath, channelHandler)

	// WebSocket endpoint for encrypted channel relay (Frontend <-> Worker).
	channelRelay := service.NewChannelRelayHandler(st, wMgr, cMgr, soloUser, cfg.SecureCookies)
	mux.Handle("/ws/channel", channelRelay)

	// OAuth HTTP endpoints.
	oauthHandler := service.NewOAuthHandler(st, cfg, ks)
	oauthHandler.RegisterRoutes(mux)

	orgSvc := service.NewOrgService(st, cfg.SoloMode)
	orgPath, orgHandler := leapmuxv1connect.NewOrgServiceHandler(orgSvc, connectOpts)
	mux.Handle(orgPath, orgHandler)

	userSvc := service.NewUserService(st, cfg, sessionCache, mailSender)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, connectOpts)
	mux.Handle(userPath, userHandler)

	sectionSvc := service.NewSectionService(st)
	sectionPath, sectionHandler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, connectOpts)
	mux.Handle(sectionPath, sectionHandler)

	workspaceSvc := service.NewWorkspaceService(st, cfg.SoloMode)
	workspacePath, workspaceHandler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaceSvc, connectOpts)
	mux.Handle(workspacePath, workspaceHandler)

	// Prometheus metrics endpoint.
	mux.Handle("/metrics", promhttp.Handler())

	// Frontend handler.
	if so.frontendHandler != nil {
		mux.Handle("/", so.frontendHandler)
	} else if cfg.DevFrontend != "" {
		devProxy, proxyErr := frontend.DevProxy(cfg.DevFrontend)
		if proxyErr != nil {
			_ = st.Close()
			closeTCP()
			return nil, fmt.Errorf("create dev proxy: %w", proxyErr)
		}
		mux.Handle("/", devProxy)
		slog.Info("dev mode: proxying frontend", "target", cfg.DevFrontend)
	} else {
		mux.Handle("/", frontend.Handler())
	}

	h2cHandler := h2c.NewHandler(logging.HTTPMiddleware(metrics.HTTPMiddleware(mux)), &http2.Server{
		MaxConcurrentStreams: 1000,
	})

	server := &http.Server{
		Handler:           h2cHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return &Server{
		cfg:          cfg,
		store:        st,
		keystore:     ks,
		oauthHandler: oauthHandler,
		server:       server,
		tcpLn:        tcpLn,
		shutdownCh:   shutdownCh,
		sessionCache: sessionCache,
		workerMgr:    wMgr,
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

// GetWorkerByID looks up a worker by ID. Returns an error if not found.
func (s *Server) GetWorkerByID(ctx context.Context, workerID string) error {
	_, err := s.store.Workers().GetByID(ctx, workerID)
	return err
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

// Serve starts the Hub server on TCP and local IPC listeners.
// It blocks until ctx is cancelled, then performs graceful shutdown.
func (s *Server) Serve(ctx context.Context) error {
	tcpLn := s.tcpLn

	listenURL, err := s.cfg.LocalListenURL()
	if err != nil {
		if tcpLn != nil {
			_ = tcpLn.Close()
		}
		_ = s.store.Close()
		return fmt.Errorf("resolve local-listen URL: %w", err)
	}

	localLn, err := locallisten.Listen(listenURL)
	if err != nil {
		if tcpLn != nil {
			_ = tcpLn.Close()
		}
		_ = s.store.Close()
		return fmt.Errorf("listen local: %w", err)
	}

	// Start background OAuth token refresh.
	s.oauthHandler.StartTokenRefresh(ctx)

	// Start periodic cleanup of soft-deleted records.
	cleanup.StartLoop(ctx, s.store)

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		slog.Info("hub shutting down...")

		// 1. Reject all new RPCs and stop background tasks.
		close(s.shutdownCh)
		s.sessionCache.Stop()

		// 2. Notify connected workers to delay reconnection.
		s.workerMgr.NotifyShutdown(10)

		// 3. Drain in-flight HTTP requests.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)

		close(shutdownDone)
	}()

	listenerCount := 1 // local listener always present
	if tcpLn != nil {
		listenerCount = 2
	}
	errCh := make(chan error, listenerCount)

	if tcpLn != nil {
		go func() { errCh <- s.server.Serve(tcpLn) }()
	}
	go func() { errCh <- s.server.Serve(localLn) }()

	if tcpLn != nil {
		slog.Info("hub listening", "addr", s.cfg.Addr, "local", listenURL)
	} else {
		slog.Info("hub listening", "local", listenURL)
	}

	if err := <-errCh; err != http.ErrServerClosed {
		_ = s.store.Close()
		return fmt.Errorf("serve: %w", err)
	}
	// Wait for the remaining listener(s) to finish.
	for i := 1; i < listenerCount; i++ {
		<-errCh
	}

	// 4. Wait for the shutdown goroutine to complete.
	<-shutdownDone

	// 5. Close store. The local listener cleans up its own endpoint (unix
	// socket file unlinks itself on Close; named pipes auto-release).
	_ = s.store.Close()
	return nil
}
