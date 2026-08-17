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
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/h2cdeadline"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/cleanup"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/frontend"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/memlimit"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// crdtShutdownTimeout caps the CRDT registry's drain, both on a construction
// failure and on runtime shutdown -- one constant so the two paths cannot drift.
const crdtShutdownTimeout = 10 * time.Second

// handlerGrace is how long in-flight HTTP handlers are given to finish on their
// own once shutdown begins before their shared base context is cancelled to force
// unwinding. It must stay strictly below httpDrainTimeout (asserted in
// server_handler_context_test.go) so the forced cancellation still leaves the
// drain time to observe the handlers returning and close their connections. 5s is
// generous for a handler making progress (a Connect unary is already capped at
// APITimeout) and far short of the drain's 10s. See BaseContext on the http.Server
// literal in NewServer and the grace timer in the shutdown goroutine in Serve.
const handlerGrace = 5 * time.Second

// httpDrainTimeout caps http.Server.Shutdown's wait for in-flight handlers to
// return. handlerGrace cancels the laggards before this expires, so the drain
// should not need its full budget in practice; the constant is the hard ceiling.
const httpDrainTimeout = 10 * time.Second

// maxConnectRequestBytes limits every inbound message on the hub's Connect
// surface. Per MESSAGE, not per stream -- see connect.WithReadMaxBytes, which
// also documents that "both clients and handlers default to allowing any request
// size". Without it an authenticated SubmitOps body is buffered whole before the
// handler ever sees it, and is then applied on that user's SINGLE-WRITER manager
// goroutine, so one oversized batch serializes every other tab's submits behind
// its own unmarshal + validate.
//
// Sized from crdt.MaxResumeDeltaBytes, the budget a resume may read back out of
// the journal, rather than from a round number: a batch bigger than that is one
// whose OWN later resume is guaranteed to blow the budget and force that client
// onto a full snapshot, so accepting it buys nothing. It is far above any
// legitimate message on this surface -- the largest are a worker's whole-machine
// tab inventory (identity fields only) and a relayed ChannelMessage, itself
// chunk-capped at channelwire.MaxCiphertextForChunk (64 KiB).
//
// READS only; responses are deliberately not capped (no WithSendMaxBytes),
// because GetMaterialized legitimately returns a multi-MB snapshot for a large
// account.
const maxConnectRequestBytes = crdt.MaxResumeDeltaBytes

// connectOptions is the option set EVERY Connect handler on the hub mux is
// registered with. A function rather than an inline literal so the request-size
// cap is reachable from a test without standing up a whole Server (NewServer
// needs a live store, listeners and a keystore), and so a future handler cannot
// be mounted with a hand-rolled option list that quietly omits the cap.
func connectOptions(interceptors ...connect.Interceptor) connect.Option {
	return connect.WithOptions(
		connect.WithInterceptors(interceptors...),
		connect.WithReadMaxBytes(maxConnectRequestBytes),
	)
}

// logQueueMemoryBudgets writes the startup account of outbound queue memory:
// the process's memory basis ONCE, each budget that divides it, and -- only
// when the cgroup probe actually failed -- a warning of its own.
//
// Once, because the basis is a property of the process, probed once
// (config.QueueMemoryBasis), and not of any one budget. It used to be rendered
// into every budget's Source, so a machine whose cgroup limit could not be read
// printed that diagnosis three times inside this single line, burying the thing
// it exists to surface. Each budget still accounts for its own figure: Source
// names the share it took of the basis logged beside it, or says the operator
// set it outright.
//
// The failure gets a Warn of its own rather than riding the Info line because
// it is an operational problem, not a fact: a confined host that sized its
// queues off the HOST's memory has budgeted whatever the ratio between the two
// is, and the next place that surfaces is the OOM kill. It carries the error
// and not the figure, which the Info record beside it already states -- the
// whole point here is that one probe produces one mention. Nothing at all is
// emitted on a healthy host: CgroupErr is nil both when a cgroup limit WAS the
// basis and when the probe ran fine and found none, so an unconfined machine
// gains no new line, and a warning that is always on is not one.
//
// Each budget is keyed by its own Name, the string that also labels its
// /metrics series, so the log and the metric cannot drift apart.
func logQueueMemoryBudgets(logger *slog.Logger, basis memlimit.Basis, budgets ...config.QueueMemoryBudget) {
	attrs := make([]any, 0, 2+2*len(budgets))
	attrs = append(attrs, "basis", basis.Figure())
	for _, b := range budgets {
		attrs = append(attrs, b.Name, b.Source)
	}
	logger.Info("outbound queue memory budgets", attrs...)
	if basis.CgroupErr != nil {
		logger.Warn("cgroup memory limit could not be read; outbound queue budgets may be sized off a figure that does not bind this process",
			"error", basis.CgroupErr)
	}
}

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
	settings          *settings.Manager
	oauthHandler      *service.OAuthHandler
	server            *http.Server
	tcpLn             net.Listener
	localLn           net.Listener
	listenURL         string
	cancelHandlers    context.CancelFunc
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

	// The settings manager is the hub's runtime configuration authority:
	// every DB-backed setting (auth policy, SMTP, timeouts, limits,
	// captcha, rate limits) resolves through its snapshot, so admin CLI
	// writes propagate within the TTL and restart-class values (queue
	// budgets, max message size) are fixed for the process's life. Loaded
	// synchronously so a broken store fails startup, before any pool is
	// constructed from a value it must never see change.
	setMgr := settingsregistry.NewManager(st, ks)
	if err := setMgr.Load(context.Background()); err != nil {
		return nil, acquired.close(fmt.Errorf("load settings: %w", err))
	}
	startupSnap := setMgr.Snapshot(context.Background())
	startupLimits := settings.KeyLimits.Of(startupSnap)

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

	// Bot-protection managers, shared between the interceptor chain and the
	// auth service so issuance and enforcement cannot disagree.
	captchaMgr := captcha.NewManager(st, setMgr, cfg.SoloMode)
	rateLimitMgr := ratelimit.NewManager(setMgr, cfg.SoloMode)

	// Provision the default captcha row at startup so the request path
	// never writes: a first Login on a fresh install must not depend on a
	// store write completing mid-request (a read-only or locked store
	// would deny logins with the uniform captcha error, and the first
	// admin setup flow would fail the same opaque way). Failing startup
	// here states the real problem. The resolve path's lazy provisioning
	// stays as an idempotent self-heal behind it.
	if err := captchaMgr.EnsureProvisioned(context.Background()); err != nil {
		return nil, acquired.close(fmt.Errorf("provision captcha config: %w", err))
	}

	// handlerCtx is the parent of every in-flight HTTP handler's request context
	// (via http.Server.BaseContext below). It is cancelled handlerGrace into
	// shutdown so a handler the per-registry teardown paths cannot reach (every
	// mux route, not just the worker Connect streams) is forced to unwind before
	// the drain's deadline. Created beside shutdownCh so the two shutdown signals
	// live together; cancelHandlers is released through acquiredResources on any
	// construction failure.
	handlerCtx, cancelHandlers := context.WithCancel(context.Background())
	acquired.cancelHandlers = cancelHandlers

	// The registry is built WITH its gate: ConnForUser cannot serve a
	// user-supplied worker id without the ownership + delegation-scope check,
	// and a composition that forgot to supply one would not compile.
	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	// Restart-class: the reassembly ceiling is fixed for the process's
	// life, read once here from the startup snapshot.
	cMgr := channelmgr.New(channelwire.ResolveMaxMessageSize(
		int(settings.KeyMaxMessageSizeBytes.Of(startupSnap))))

	// Outbound queue memory, limited per CLASS of connection rather than per
	// connection: a per-connection budget does not compose into a process limit
	// when nothing limits the connection count.
	//
	// Three pools, not one shared total. Within a pool the admission rule
	// reclaims from the largest holder, and no class may reclaim from another,
	// because the three failures cost wildly different things: dropping a
	// channel relay costs a reconnect plus a Noise re-handshake per channel,
	// dropping a worker takes every user's channels on that machine and has no
	// replay in the frontend->worker direction, and dropping a user-event
	// subscriber costs a reconnect and a delta resume. Separate budgets make
	// that blast radius structural rather than a property of whichever
	// connection happened to be biggest.
	// Each pool is built from its class's whole budget, not just the total:
	// QueueMemoryBudget.PoolConfig carries the class's largest frame through to
	// sendq as the guaranteed working set, so an otherwise-idle member can
	// always place one legitimate message. Built with sendq's generic defaults
	// instead, a merely-full user-events pool refused every 16 MiB bootstrap
	// against a 4 MiB floor.
	// newQueuePool is the one place a budget becomes a pool AND gets published,
	// so a fourth class cannot arrive with its metrics registration forgotten.
	// The name travels on the budget (see queueClass.name) rather than being
	// spelled here, which is what keeps the config key, the metric label and the
	// startup log's key for this pool from drifting apart.
	newQueuePool := func(budget config.QueueMemoryBudget) *sendq.Pool {
		pool := sendq.NewPool(budget.PoolConfig())
		metrics.SetSendqPool(budget.Name, pool)
		return pool
	}
	// Probed ONCE, here, and handed to all three: the three shares divide one
	// number because there is one number, not because three accessors agreed.
	queueBasis := memlimit.Detect()
	// Restart-class like max_message_size: pool minimum floors are derived
	// from these at startup, so an admin's change applies on the next
	// restart. An unset (zero) field still auto-sizes from the process's
	// own memory limit, keeping per-process budgets on multi-instance
	// deployments.
	startupBudgets := settings.KeyQueueBudget.Of(startupSnap)
	relayBudget := config.ResolveRelayQueueMemoryBudget(startupBudgets.RelayBytes, queueBasis)
	relayQueuePool := newQueuePool(relayBudget)

	workerBudget := config.ResolveWorkerQueueMemoryBudget(startupBudgets.WorkerBytes, queueBasis)
	workerQueuePool := newQueuePool(workerBudget)

	userEventsBudget := config.ResolveUserEventsQueueMemoryBudget(startupBudgets.UserEventsBytes, queueBasis)
	userEventsQueuePool := newQueuePool(userEventsBudget)

	logQueueMemoryBudgets(slog.Default(), queueBasis,
		relayBudget, workerBudget, userEventsBudget)
	// The API timeout is hot: the closure re-resolves the snapshot on every
	// call, exactly like the per-request config reads it replaces.
	apiTimeout := func() time.Duration {
		return settings.KeyTimeouts.Of(setMgr.Snapshot(context.Background())).APITimeout()
	}
	pendingReqs := workermgr.NewPendingRequests(apiTimeout)

	apiTokenPepper := ks.Pepper()
	tokenValidator, tvErr := auth.NewTokenValidator(st, apiTokenPepper[:])
	if tvErr != nil {
		return nil, acquired.close(
			fmt.Errorf("create token validator: %w", tvErr))
	}
	authInterceptor, authContexts := auth.NewInterceptor(auth.InterceptorOptions{
		Store:          st,
		SoloUser:       soloUser,
		TokenValidator: tokenValidator,
		// The auth policy (cookie name, verification gate, session
		// duration) is DB-backed and hot: re-resolved per use so an admin
		// change applies to the next request.
		Policy: func() auth.Policy {
			return auth.PolicyFromSettings(setMgr.Snapshot(context.Background()))
		},
	})
	acquired.authContexts = authContexts
	// Let a sliding cookie session (and a rotated bearer, via the credential
	// lifecycle) extend its already-open channels' expiry, not just its leases
	// (which the registry owns directly).
	authContexts.SetChannelExpiryRescheduler(cMgr)
	// The count term the queue pools cannot supply themselves: they limit the
	// bytes one CLASS of connection may hold, and this limits how many one user
	// may open, so a member's guaranteed working set stops multiplying by a
	// number nothing limits.
	authContexts.SetMaxConnectionsPerUser(startupLimits.MaxConnectionsPerUser)
	// Logged like the queue budgets beside it, and for the same reason: the
	// setter is one unenforced line, so a wiring omission is otherwise invisible
	// until the day the cap was supposed to hold and did not.
	slog.Info("per-user connection limit",
		"max_connections_per_user", startupLimits.MaxConnectionsPerUser, "unlimited", startupLimits.MaxConnectionsPerUser == 0)
	connectOpts := connectOptions(
		auth.NewShutdownInterceptor(shutdownCh),
		metrics.NewInterceptor(),
		auth.NewTimeoutInterceptor(apiTimeout),
		authInterceptor,
		captcha.NewInterceptor(captchaMgr),
		ratelimit.NewInterceptor(rateLimitMgr),
	)

	mux := http.NewServeMux()

	// Mail sender: resolves the SMTP configuration from the settings
	// snapshot on every Send, so `admin settings set smtp ...` applies to
	// the next email without a restart. With SMTP unconfigured it returns
	// the loud, matchable ErrEmailDisabled rather than silently dropping
	// mail. The write path's cross-validation keeps
	// email_verification_required=true from pairing with an absent SMTP
	// host; the read path degrades that combination to verification-off.
	mailSender := mail.NewSettingsSender(setMgr)
	// The renderer derives its base URL per render so a public_url change
	// reaches the next email's links and footers without a restart.
	mailRenderer := mail.Renderer{BaseURL: func() string {
		return settings.BaseURL(setMgr.Snapshot(context.Background()), cfg.Listen)
	}}

	broadcaster := service.NewHubEventBroadcaster(cMgr)
	notifierSvc := notifier.New(st, wMgr, pendingReqs, apiTimeout)

	// Per-user CRDT manager registry. The factory constructs a fully
	// bootstrapped manager (state loaded from disk, ops replayed) on
	// first reference per user. Lifecycle outbox / regular submits
	// route through the same registry. Built early so it can be
	// passed by constructor to every service that drives it (no
	// post-construction injection or initialization-order hazards).
	crdtJournal := service.NewCRDTJournal(st)
	crdtAuth := service.NewCRDTAuthChecker(st)
	// wMgr is already built above, so the nudger can be constructed here and
	// handed to every manager -- no post-construction injection.
	reconcileNudger := service.NewReconcileNudger(wMgr, slog.Default())
	crdtRegistry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(owner, crdtJournal, crdtAuth, slog.Default(), time.Now,
			crdt.WithReconcileNudger(reconcileNudger))
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, slog.Default())
	acquired.crdtRegistry = crdtRegistry

	connectorSvc := service.NewWorkerConnectorService(st, wMgr, cMgr, broadcaster, pendingReqs, notifierSvc, crdtRegistry, workerQueuePool)
	// The worker pool's count term. Its members take no lease, so the per-user
	// connection cap cannot see them, and nothing else keeps the floors this
	// pool guarantees from summing without limit.
	//
	// One config key, two enforcement points, because one of them cannot do the
	// job alone. The service caps ACTIVE ROWS, which is the refusal an operator
	// can act on -- it names the key and happens at registration. The registry
	// caps LIVE CONNECTIONS, which is the one the pool actually feels: a
	// deregistering worker keeps its stream (that is how it learns to stop) but
	// stops counting as a row, so the row cap alone lets register/deregister
	// cycles add members forever.
	connectorSvc.SetMaxWorkersPerUser(startupLimits.MaxWorkersPerUser)
	wMgr.SetMaxWorkersPerUser(startupLimits.MaxWorkersPerUser)
	// Logged like the connection limit above, and for the same reason: two
	// unenforced setter lines, so a wiring omission is otherwise invisible until
	// the day the cap was supposed to hold and did not.
	slog.Info("per-user worker limit",
		"max_workers_per_user", startupLimits.MaxWorkersPerUser, "unlimited", startupLimits.MaxWorkersPerUser <= 0)
	// Hot propagation for both per-user caps: the settings manager fires
	// subscribers on every effective snapshot change, and the push lands in
	// the same atomic setters the startup values used, so an admin's
	// `settings set limits ...` holds from the next registration onward
	// without a restart.
	var limitsStateMu sync.Mutex
	lastLimits := startupLimits
	setMgr.Subscribe(func(s *settings.Snapshot) {
		l := settings.KeyLimits.Of(s)
		authContexts.SetMaxConnectionsPerUser(l.MaxConnectionsPerUser)
		connectorSvc.SetMaxWorkersPerUser(l.MaxWorkersPerUser)
		wMgr.SetMaxWorkersPerUser(l.MaxWorkersPerUser)
		// The subscription fires on every effective snapshot change, not
		// only the limits: the log reports the limits, so it fires only when
		// they actually moved (an smtp write must not tell the operator a
		// limits change happened).
		limitsStateMu.Lock()
		changed := l != lastLimits
		lastLimits = l
		limitsStateMu.Unlock()
		if changed {
			slog.Info("per-user limits changed",
				"max_connections_per_user", l.MaxConnectionsPerUser,
				"max_workers_per_user", l.MaxWorkersPerUser)
		}
	})
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, connectOpts)
	mux.Handle(connectorPath, connectorHandler)
	// One delegation-scope cache shared by SubmitOps (resolve) and worker
	// deregistration (evict); see auth.DelegationScopeCache.
	scopeCache := auth.NewDelegationScopeCache(st)
	// The post-deregister effects, shared by the per-user surface, the
	// admin surface, and the user-deletion cascade -- so a worker torn down
	// by any of the three is told to stop, loses its memoized delegation
	// scope, and leaves its owner's client informed.
	workerEffects := service.NewWorkerDeregisterEffects(scopeCache, notifierSvc, broadcaster)
	mgmtSvc := service.NewWorkerManagementService(st, wMgr, broadcaster, notifierSvc, mailSender, mailRenderer, setMgr, scopeCache)
	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(mgmtSvc, connectOpts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, authContexts)
	lifecycle := auth.NewCredentialLifecycleEffects(authContexts, channelSvc, cMgr)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, connectOpts)
	mux.Handle(channelPath, channelHandler)

	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store: st, Config: cfg, Settings: setMgr, Lifecycle: lifecycle, Keystore: ks,
		Mail: mailSender, Renderer: mailRenderer, Captcha: captchaMgr,
	})
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connectOpts)
	mux.Handle(authPath, authHandler)

	// WebSocket endpoint for encrypted channel relay (Frontend <-> Worker).
	// The cookie-name policy resolves per connection, so a secure_cookies
	// change applies to the next socket (existing sockets keep the name
	// they authenticated under).
	secureCookies := func() bool {
		return settings.KeySecureCookies.Of(setMgr.Snapshot(context.Background()))
	}
	channelRelay := service.NewChannelRelayHandler(st, wMgr, cMgr, authContexts, soloUser, secureCookies, relayQueuePool).
		WithTokenValidator(tokenValidator).
		WithChannelCloseEnqueuer(channelSvc)
	mux.Handle("/ws/channel", channelRelay)

	// OAuth HTTP endpoints.
	oauthHandler := service.NewOAuthHandler(st, cfg, setMgr, ks)
	oauthHandler.RegisterRoutes(mux)

	// CLI auth HTTP endpoints (PKCE local-redirect + RFC 8628 device code).
	apiAuthHandler := service.NewAPIAuthHandler(st, tokenValidator, lifecycle, func() string {
		return settings.BaseURL(setMgr.Snapshot(context.Background()), cfg.Listen)
	})
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
	userSvc := service.NewUserService(st, cfg, setMgr, lifecycle, mailSender, mailRenderer)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, connectOpts)
	mux.Handle(userPath, userHandler)

	// The admin services: the authenticated, online face of hub
	// administration, restricted to admin users by the auth interceptor's
	// adminProcedures map (tripwired against these handlers' proto
	// descriptors in internal/hub/auth and against the mounted mux paths
	// in this package's tests). `leapmux control admin ...` and the
	// preferences dialog's administration panels are the callers; the
	// offline break-glass tree is `leapmux recover`.
	//
	// The per-key rules the admin surface reports through. Each one is a
	// property of its KEY, so it is registered on the settings manager and
	// every surface that asks "what is in effect" gets the same answer; a
	// handler that held them would carry per-key knowledge it cannot keep
	// correct. They are registered HERE rather than at NewManager because
	// each closes over something that exists only after the manager loads:
	// the pool capacities are sized from the startup snapshot, and the
	// captcha manager is itself built on this manager.
	setMgr.Configure(
		settings.WithEffective(settings.KeySignupEnabled.Name(), func(s *settings.Snapshot) (any, bool) {
			return settings.SignupEnabledEffective(s, cfg.DevMode), true
		}),
		settings.WithEffective(settings.KeyEmailVerificationRequired.Name(), func(s *settings.Snapshot) (any, bool) {
			return settings.EmailVerificationEffective(s), true
		}),
		settings.WithEffective(captcha.CaptchaSelectedKey.Name(), func(s *settings.Snapshot) (any, bool) {
			// A selected provider that is not fully configured degrades at
			// read time; report the provider that actually serves challenges.
			// A complete selection needs no override.
			effective, note := captcha.Effective(s)
			if note == "" {
				return nil, false
			}
			return captcha.ProviderAlias(effective.Provider), true
		}),
		settings.WithEffective(settings.KeyQueueBudget.Name(), func(*settings.Snapshot) (any, bool) {
			// The startup-resolved capacities, not the stored document: a
			// stored 0 auto-sizes from the process memory limit, and reporting
			// that 0 told an admin the pool was zero-sized. queue_budget is
			// restart-class, so these stay correct for the life of the process.
			return settings.QueueBudgetValue{
				RelayBytes:      relayBudget.Capacity,
				WorkerBytes:     workerBudget.Capacity,
				UserEventsBytes: userEventsBudget.Capacity,
			}, true
		}),
		// A reset of the ALTCHA row removes the signing key every outstanding
		// challenge carries. Re-provision before the reset answers, so the
		// next unauthenticated login does not write hub_settings from inside
		// its own request handler.
		settings.WithAfterReset(captcha.AltchaKey.Name(), captchaMgr.EnsureProvisioned),
	)
	adminSettingsSvc := service.NewAdminSettingsService(setMgr, cfg)
	adminSettingsPath, adminSettingsHandler := leapmuxv1connect.NewAdminSettingsServiceHandler(adminSettingsSvc, connectOpts)
	mux.Handle(adminSettingsPath, adminSettingsHandler)

	adminUserSvc := service.NewAdminUserService(st, tokenValidator, lifecycle, workerEffects)
	adminUserPath, adminUserHandler := leapmuxv1connect.NewAdminUserServiceHandler(adminUserSvc, connectOpts)
	mux.Handle(adminUserPath, adminUserHandler)

	adminWorkerSvc := service.NewAdminWorkerService(st, workerEffects)
	adminWorkerPath, adminWorkerHandler := leapmuxv1connect.NewAdminWorkerServiceHandler(adminWorkerSvc, connectOpts)
	mux.Handle(adminWorkerPath, adminWorkerHandler)

	adminOAuthSvc := service.NewAdminOAuthService(st, ks)
	adminOAuthPath, adminOAuthHandler := leapmuxv1connect.NewAdminOAuthServiceHandler(adminOAuthSvc, connectOpts)
	mux.Handle(adminOAuthPath, adminOAuthHandler)

	sectionSvc := service.NewSectionService(st)
	sectionPath, sectionHandler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, connectOpts)
	mux.Handle(sectionPath, sectionHandler)

	workspaceSvc := service.NewWorkspaceService(st, crdtRegistry)
	workspacePath, workspaceHandler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaceSvc, connectOpts)
	mux.Handle(workspacePath, workspaceHandler)

	crdtSvc := service.NewCRDTService(st, crdtRegistry, slog.Default(), scopeCache)
	crdtPath, crdtHandler := leapmuxv1connect.NewUserCRDTHandler(crdtSvc, connectOpts)
	mux.Handle(crdtPath, crdtHandler)

	// WebSocket endpoint for the CRDT event stream. Frontend opens a
	// single `/ws/userevents?workspace_ids=...` connection per session
	// and reads length-prefixed `WatchUserEvent` proto frames. This is
	// the sole transport for user-event subscriptions — the UserCRDT
	// ConnectRPC service exposes unary calls only (SubmitOps,
	// UpdatePresence). The WS path bypasses HTTP/1.1 chunked-stream
	// buffering hazards (some proxies / Tauri's buffered fetch) that
	// motivated retiring the streaming RPC.
	userEventsHandler := service.NewUserEventsHandler(st, crdtRegistry, authContexts, soloUser, secureCookies, userEventsQueuePool).
		WithTokenValidator(tokenValidator)
	mux.Handle("/ws/userevents", userEventsHandler)

	reconcilerSvc := service.NewWorkerReconcilerService(st)
	reconcilerPath, reconcilerHandler := leapmuxv1connect.NewWorkerReconcilerServiceHandler(reconcilerSvc, connectOpts)
	mux.Handle(reconcilerPath, reconcilerHandler)

	// Prometheus metrics endpoint.
	mux.Handle("/metrics", promhttp.Handler())

	// Unauthenticated /version endpoint. Exposes the hub's build
	// identity so `leapmux control version` can report both CLI and
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

	// BaseContext seeds the context of EVERY accepted connection (and thus every
	// request and h2c stream) with handlerCtx. Cancelling handlerCtx during
	// shutdown cascades to r.Context() for in-flight handlers on both HTTP/1.1
	// and h2c, without per-handler wiring -- the structural bound for the class
	// of bug where a mux route doing unbounded I/O parks the drain. net/http
	// derives connCtx from BaseContext in Serve->conn.serve, and h2c's
	// sc.baseCtx comes from the same chain.
	server := &http.Server{
		Handler: logging.HTTPMiddleware(metrics.HTTPMiddleware(mux)),
		// Bounds reading request HEADERS on HTTP/1.1 connections (the
		// slowloris guard). It must NOT govern h2c connections, whose streams
		// outlive any header deadline — see the h2cdeadline.Wrap call in
		// Serve for why this needs a workaround on current Go.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return handlerCtx },
		Protocols:         protocols,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: 1000,
			// Without this a peer that stops draining its socket parks the
			// writer forever, and workermgr.Conn.Send holds the conn's mutex
			// across that write -- so ONE wedged worker blocks every other
			// sender to it (notifier, channel relay, shutdown notice) for the
			// process's life, and its Connect handler can never return. The
			// timeout is extended whenever any byte is written, so it caps
			// zero PROGRESS on a write, not the write's total duration: a slow
			// but moving consumer never trips it, however large the frame. 30s
			// is therefore about how long a stalled-but-recoverable socket may
			// take to accept a single byte -- generous for a paused VM or a
			// saturated link, and far short of leaving a wedged peer to hold
			// the conn mutex for the process's life.
			//
			// It does NOT bound a peer that stalls its HTTP/2 flow-control
			// window instead, because then no socket write is attempted; see
			// workermgr.FenceAll for that residual.
			WriteByteTimeout: 30 * time.Second,
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
		settings:          setMgr,
		oauthHandler:      oauthHandler,
		server:            server,
		tcpLn:             tcpLn,
		localLn:           localLn,
		listenURL:         listenURL,
		cancelHandlers:    cancelHandlers,
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

// SettingsManager returns the Hub's settings manager; the solo/dev
// launchers read instance settings through it when bringing up the local
// worker.
func (s *Server) SettingsManager() *settings.Manager {
	return s.settings
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

	// A worker with a blank registrant is owned by nobody: requireWorkerOwner
	// would refuse its real owner for the process's life. Refuse to create it.
	registrantUID, ok := userid.New(registeredBy)
	if !ok {
		return nil, errors.New("register worker: registeredBy is required")
	}
	if err := s.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       authToken,
		RegisteredBy:    registrantUID,
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

// GetAdminUser returns the ID of the user to attribute auto-registered
// local workers to. In solo mode this is the bootstrapped solo user. In
// dev/hub mode this is the first admin user registered via the /setup
// flow; the caller gets store.ErrNotFound when no admin exists yet and
// is expected to retry once one does.
func (s *Server) GetAdminUser(ctx context.Context) (userID string, err error) {
	if s.cfg.SoloMode {
		user, err := s.store.Users().GetByUsername(ctx, usernames.Solo)
		if err != nil {
			return "", fmt.Errorf("get solo user: %w", err)
		}
		return user.ID, nil
	}

	user, err := s.store.Users().GetFirstAdmin(ctx)
	if err != nil {
		return "", fmt.Errorf("get first admin: %w", err)
	}
	return user.ID, nil
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
		// A cancel landing here is an exit somebody ASKED for, not a failure to
		// report. NewServer binds BOTH listeners before Serve runs, so a caller's
		// connect-and-close readiness probe succeeds -- and its Stop can arrive --
		// while this seed is still in flight. Reporting it made `leapmux hub` and
		// `leapmux solo` exit NON-ZERO on an ordinary Ctrl-C during startup,
		// printing "seed revocation watcher: ... context canceled" -- or, when the
		// store aborted first, modernc sqlite's "interrupted (9)", which wraps no
		// context error and so slips through every errors.Is(err, context.Canceled)
		// filter downstream. The teardown below still runs, and a genuine failure
		// in it is still reported.
		primary := fmt.Errorf("seed revocation watcher: %w", err)
		if serveCtx.Err() != nil {
			primary = nil
		}
		return serverTeardownErrors{
			primary:       primary,
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

		// 2. Notify connected workers to delay reconnection, then end their
		// Connect streams. Both halves matter to the drain below: a notified
		// worker still holds its stream until it decides to reconnect, and the
		// drain waits on the handler that stream is parked in. This releases the
		// drain for the handlers the worker registry knows about. The handlerCtx
		// cancel below releases the handlers it does not know about.
		notifyCtx, cancelNotify := context.WithTimeout(context.Background(), 2*time.Second)
		s.workerMgr.NotifyShutdownAndFence(notifyCtx, 10)
		cancelNotify()

		// 3. Cancel every in-flight handler the per-registry teardown above did
		// not reach, then drain what is left. handlerCtx is the BaseContext of
		// the http.Server, so cancelling it cascades to r.Context() for every
		// in-flight request (HTTP/1.1 and h2c). handlerGrace lets short handlers
		// finish on their own before the forced unwind, so a quick store read is
		// not cut off; only a handler parked past the grace (e.g. a mux route on
		// unbounded outbound I/O) is cancelled. This is the structural bound
		// FenceAll cannot express, since FenceAll only reaches handlers the
		// worker registry knows about.
		graceTimer := time.AfterFunc(handlerGrace, s.cancelHandlers)
		defer graceTimer.Stop()

		// Drain in-flight HTTP requests, then force-close any connections
		// the drain left behind. On Windows each accepted named-pipe
		// connection is its own pipe instance; if any survive, the next
		// ListenPipe with FILE_FLAG_FIRST_PIPE_INSTANCE on the same name
		// fails with ERROR_ACCESS_DENIED.
		//
		// http.Server.Close() only iterates net/http's own activeConn map.
		// A channel-relay websocket is hijacked, which removes it from that
		// map — so Close() cannot reach it. (An unencrypted-HTTP/2 connection
		// carrying a worker's bidi stream stays in the map, but marked ACTIVE
		// for its whole life, which is why the drain above needs the fenced
		// handlers to have returned.) locallisten.CloseAccepted closes the
		// underlying pipe handles directly via the listener's own
		// accepted-connection tracking — the only level that sees every conn
		// THAT listener accepted.
		//
		// It is a backstop, not the primary teardown: authContexts.Stop above
		// cancels each relay's auth lease, which ends its handler and closes
		// the socket. This catches one hijacked before its lease existed. The
		// TCP listener needs no equivalent — a lingering accepted TCP conn does
		// not block rebinding the port, whereas a surviving named-pipe instance
		// does block the next ListenPipe(FIRST_PIPE_INSTANCE).
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpDrainTimeout)
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

	// h2cdeadline.Wrap disarms the ReadHeaderTimeout read deadline on
	// connections that turn out to speak h2c. Without it (golang/go#80876,
	// unfixed as of the Go this builds on), the deadline net/http arms before
	// the protocol probe survives the HTTP/2 handoff and kills the connection
	// 10s after ACCEPT — which is every worker Connect stream: in solo/dev
	// over the local IPC listener (the 10s reconnect loop), and for remote
	// workers over the TCP listener. HTTP/1.1 connections keep the deadline,
	// so the slowloris guard on the public listener is unchanged. See the
	// package doc for why ReadHeaderTimeout=0 and ReadTimeout>0 — the two
	// shapes that avoid the bug — were rejected.
	if tcpLn != nil {
		go func() { errCh <- listenerResult{isTCP: true, err: s.server.Serve(h2cdeadline.Wrap(tcpLn))} }()
	}
	go func() { errCh <- listenerResult{err: s.server.Serve(h2cdeadline.Wrap(localLn))} }()

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
	// store. A context with a deadline stops a broken backend from hanging shutdown.
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
	tcpLn          net.Listener
	localLn        net.Listener
	store          store.Store
	authContexts   *auth.AuthContextRegistry
	crdtRegistry   *crdt.Registry
	cancelHandlers context.CancelFunc
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
	if r.cancelHandlers != nil {
		r.cancelHandlers()
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
