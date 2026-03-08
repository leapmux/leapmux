// Package hub provides a reusable Hub server that can be embedded
// in other binaries (e.g. the standalone all-in-one binary).
package hub

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	"github.com/leapmux/leapmux/internal/hub/frontend"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/id"
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
	cfg        *config.Config
	queries    *gendb.Queries
	server     *http.Server
	sqlDB      *sql.DB
	shutdownCh chan struct{}
	workerMgr  *workermgr.Manager
}

// NewServer creates a new Hub server. It opens the database, runs
// migrations, bootstraps defaults, and wires all services. Call
// Serve() to start listening.
func NewServer(cfg *config.Config, opts ...ServerOption) (*Server, error) {
	var so serverOptions
	for _, opt := range opts {
		opt(&so)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	sqlDB, err := db.Open(cfg.DBPath(), cfg.DBMaxConns)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	queries := gendb.New(sqlDB)

	if err := bootstrap.Run(context.Background(), queries); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("bootstrap: %w", err)
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

	connectOpts := connect.WithInterceptors(
		auth.NewShutdownInterceptor(shutdownCh),
		metrics.NewInterceptor(),
		auth.NewTimeoutInterceptor(cfg.APITimeout),
		auth.NewInterceptor(queries),
	)

	mux := http.NewServeMux()

	authSvc := service.NewAuthService(queries, cfg)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connectOpts)
	mux.Handle(authPath, authHandler)

	connectorSvc := service.NewWorkerConnectorService(queries, wMgr)
	connectorSvc.SetShutdownCh(shutdownCh)
	connectorSvc.SetChannelMgr(cMgr)
	connectorSvc.SetPendingRequests(pendingReqs)
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, connectOpts)
	mux.Handle(connectorPath, connectorHandler)

	notifierSvc := notifier.New(queries, wMgr, pendingReqs, cfg)
	mgmtSvc := service.NewWorkerManagementService(queries, wMgr, notifierSvc)
	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(mgmtSvc, connectOpts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(queries, wMgr, cMgr, pendingReqs)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, connectOpts)
	mux.Handle(channelPath, channelHandler)

	// WebSocket endpoint for encrypted channel relay (Frontend ↔ Worker).
	channelRelay := service.NewChannelRelayHandler(queries, wMgr, cMgr)
	mux.Handle("/ws/channel", channelRelay)

	orgSvc := service.NewOrgService(queries, nil)
	orgPath, orgHandler := leapmuxv1connect.NewOrgServiceHandler(orgSvc, connectOpts)
	mux.Handle(orgPath, orgHandler)

	userSvc := service.NewUserService(queries, cfg)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, connectOpts)
	mux.Handle(userPath, userHandler)

	sectionSvc := service.NewSectionService(queries)
	sectionPath, sectionHandler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, connectOpts)
	mux.Handle(sectionPath, sectionHandler)

	adminSvc := service.NewAdminService(queries)
	adminPath, adminHandler := leapmuxv1connect.NewAdminServiceHandler(adminSvc, connectOpts)
	mux.Handle(adminPath, adminHandler)

	workspaceSvc := service.NewWorkspaceService(queries)
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
			_ = sqlDB.Close()
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
		cfg:        cfg,
		queries:    queries,
		server:     server,
		sqlDB:      sqlDB,
		shutdownCh: shutdownCh,
		workerMgr:  wMgr,
	}, nil
}

// Queries returns the Hub's store queries for direct database access
// (e.g. for standalone auto-registration).
func (s *Server) Queries() *gendb.Queries {
	return s.queries
}

// SocketPath returns the Unix domain socket path for this server.
func (s *Server) SocketPath() string {
	return s.cfg.SocketPath()
}

// WorkerCredentials holds the credentials for a registered worker.
type WorkerCredentials struct {
	WorkerID  string
	AuthToken string
}

// RegisterWorker creates a worker record directly in the database,
// bypassing the normal registration flow. This is used by the standalone
// binary to auto-register a local worker.
func (s *Server) RegisterWorker(ctx context.Context, orgID, registeredBy string) (*WorkerCredentials, error) {
	workerID := id.Generate()
	authToken := id.Generate()

	if err := s.queries.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:           workerID,
		OrgID:        orgID,
		AuthToken:    authToken,
		RegisteredBy: registeredBy,
		PublicKey:    []byte{},
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
	_, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	return err
}

// GetAdminUser returns the admin user's ID and org ID.
func (s *Server) GetAdminUser(ctx context.Context) (userID, orgID string, err error) {
	user, err := s.queries.GetUserByUsername(ctx, "admin")
	if err != nil {
		return "", "", fmt.Errorf("get admin user: %w", err)
	}
	return user.ID, user.OrgID, nil
}

// Serve starts the Hub server on TCP and Unix socket listeners.
// It blocks until ctx is cancelled, then performs graceful shutdown.
func (s *Server) Serve(ctx context.Context) error {
	sockPath := s.cfg.SocketPath()
	if err := removeStaleSocket(sockPath); err != nil {
		_ = s.sqlDB.Close()
		return fmt.Errorf("remove stale socket: %w", err)
	}

	var tcpLn net.Listener
	if s.cfg.Addr != "" {
		var err error
		tcpLn, err = net.Listen("tcp", s.cfg.Addr)
		if err != nil {
			_ = s.sqlDB.Close()
			return fmt.Errorf("listen tcp: %w", err)
		}
	}

	unixLn, err := net.Listen("unix", sockPath)
	if err != nil {
		if tcpLn != nil {
			_ = tcpLn.Close()
		}
		_ = s.sqlDB.Close()
		return fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		if tcpLn != nil {
			_ = tcpLn.Close()
		}
		_ = unixLn.Close()
		_ = s.sqlDB.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		slog.Info("hub shutting down...")

		// 1. Reject all new RPCs.
		close(s.shutdownCh)

		// 2. Notify connected workers to delay reconnection.
		s.workerMgr.NotifyShutdown(10)

		// 3. Drain in-flight HTTP requests.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)

		close(shutdownDone)
	}()

	listenerCount := 1 // unix listener always present
	if tcpLn != nil {
		listenerCount = 2
	}
	errCh := make(chan error, listenerCount)

	if tcpLn != nil {
		go func() { errCh <- s.server.Serve(tcpLn) }()
	}
	go func() { errCh <- s.server.Serve(unixLn) }()

	if tcpLn != nil {
		slog.Info("hub listening", "addr", s.cfg.Addr, "socket", sockPath)
	} else {
		slog.Info("hub listening", "socket", sockPath)
	}

	if err := <-errCh; err != http.ErrServerClosed {
		_ = s.sqlDB.Close()
		return fmt.Errorf("serve: %w", err)
	}
	// Wait for the remaining listener(s) to finish.
	for i := 1; i < listenerCount; i++ {
		<-errCh
	}

	// 4. Wait for the shutdown goroutine to complete.
	<-shutdownDone

	// 5. Clean up socket.
	_ = os.Remove(sockPath)

	// 6. Checkpoint WAL into main DB file before closing.
	if _, err := s.sqlDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		slog.Warn("WAL checkpoint failed", "error", err)
	}

	// 7. Close database.
	_ = s.sqlDB.Close()
	return nil
}

// removeStaleSocket removes a leftover socket file from a previous crash.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode().Type() == fs.ModeSocket {
		return os.Remove(path)
	}
	return fmt.Errorf("%s exists but is not a socket", path)
}
