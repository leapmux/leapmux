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
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/cleanup"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/frontend"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
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
	"github.com/leapmux/leapmux/util/clockjump"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// crdtShutdownTimeout caps the CRDT registry's drain, both on a construction
// failure and on runtime shutdown -- one constant so the two paths cannot drift.
const crdtShutdownTimeout = 10 * time.Second

// handlerGrace is how long the server gives in-flight HTTP handlers to finish on
// their own once shutdown begins, before it cancels their shared base context to
// force unwinding. It must stay strictly below httpDrainTimeout (asserted in
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
// size". Without it the hub buffers an authenticated SubmitOps body whole before
// the handler ever sees it, and then applies it on that user's SINGLE-WRITER
// manager goroutine, so one oversized batch serializes every other tab's submits behind
// its own unmarshal + validate.
//
// Sized from crdt.MaxResumeDeltaBytes, the budget a resume may read back out of
// the journal, rather than from a round number: a batch bigger than that is one
// whose OWN later resume is guaranteed to exceed the budget and force that client
// onto a full snapshot, so accepting it buys nothing. It is far above any
// legitimate message on this surface -- the largest are a worker's whole-machine
// tab inventory (identity fields only) and a relayed ChannelMessage, itself
// chunk-capped at contracts.MaxCiphertextForChunk (64 KiB).
//
// READS only; this option set deliberately caps no response (no
// WithSendMaxBytes), because GetMaterialized legitimately returns a multi-MB snapshot for a large
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
// printed that diagnosis three times inside this single line, which hid the
// thing it exists to surface. Each budget still accounts for its own figure:
// Source states the share it took of the basis logged beside it, or says the
// operator set it outright.
//
// The failure gets a Warn of its own rather than riding the Info line because
// it is an operational problem, not a fact: a confined host that sized its
// queues off the HOST's memory budgets whatever the ratio between the two
// is, and the next place that surfaces is the OOM kill. It carries the error
// and not the figure, which the Info record beside it already states -- the
// whole point here is that one probe produces one mention. This function emits
// nothing at all on a healthy host: CgroupErr is nil both when a cgroup limit WAS the
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

// resolveFrontend picks the handler that serves "/" and the
// Content-Security-Policy that goes with it.
//
// The two are returned TOGETHER, from one function, because the script-src
// hashes come from the assets that this process actually mounts. Choosing the
// handler in one place and the policy in another lets the two drift, and a
// policy built for a different frontend than the one this process serves is an
// OUTAGE rather than a weaker defence: the browser refuses the app's own
// script and the user gets a blank page.
//
// A function rather than an inline branch so all three branches are reachable
// from a test without standing up a whole Server, which needs a live store,
// listeners and a keystore. connectOptions above exists for the same reason.
//
// The three branches, in the order this function tests them:
//
//   - An INJECTED handler (hub.WithFrontendHandler) brings assets this process
//     cannot read, so it gets no policy at all. See
//     frontend.UnknownAssetsPolicy.
//   - A DEV proxy fronts the Vite dev server, whose HMR client injects inline
//     scripts and evaluates source maps, so its policy is report-only. See
//     frontend.DevPolicy.
//   - Otherwise the hub serves the EMBEDDED assets, under an enforced policy
//     derived from those exact bytes. See frontend.Policy.
func resolveFrontend(injected http.Handler, devFrontend string) (http.Handler, httpsec.Policy, error) {
	if injected != nil {
		return injected, frontend.UnknownAssetsPolicy(), nil
	}
	if devFrontend != "" {
		devProxy, err := frontend.DevProxy(devFrontend)
		if err != nil {
			return nil, httpsec.Policy{}, fmt.Errorf("create dev proxy: %w", err)
		}
		slog.Info("dev mode: proxying frontend", "target", devFrontend)
		return devProxy, frontend.DevPolicy(), nil
	}
	return frontend.Handler(), frontend.Policy(), nil
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
	cfg        *config.Config
	store      store.Store
	keystore   *keystore.Keystore
	settings   *settings.Manager
	idpHandler *service.IdPHandler
	server     *http.Server
	// listeners owns every TCP socket, including the one -listen gave, and
	// rebinds them when the extra_listen_addresses setting changes.
	listeners *listenerSet
	// listenerErrs carries a TCP listener that stopped without being asked
	// to. Serve selects on it; a deliberate close never reaches it.
	listenerErrs      chan error
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
	// Serve goroutine still propagates the bind failure.
	// The extra listen addresses do NOT join here, although they are bound
	// the same way. They live in a settings row, which is unreadable until
	// the store opens below, and moving this bind after the store would give
	// up the fail-fast property the paragraph above describes. The listener
	// set applies them further down, once the settings manager has loaded.
	var tcpLn net.Listener
	var baseAddr *listenset.Addr
	if cfg.Listen != "" {
		parsed, parseErr := listenset.Parse(cfg.Listen)
		if parseErr != nil {
			return nil, fmt.Errorf("listen tcp: %w", parseErr)
		}
		baseAddr = &parsed
		var listenErr error
		tcpLn, listenErr = net.Listen("tcp", parsed.DialAddr())
		if listenErr != nil {
			return nil, fmt.Errorf("listen tcp: %w", listenErr)
		}
	}
	acquired.tcpLn = tcpLn

	// The listener set takes over the base listener HERE, so the failure paths
	// below release it through the set rather than twice, and so the reporter
	// can hold it by value. It gets its http.Server later, once the mux and the
	// CSP that server is built from exist; see setServer.
	//
	// listenerErrs is buffered by one: Serve reads the FIRST fault and tears
	// the hub down, so a listener that fails while that teardown runs must not
	// park its goroutine on an unread channel.
	listenerErrs := make(chan error, 1)
	listeners := newListenerSet(tcpLn, baseAddr, listenerErrs)
	acquired.tcpLn = nil
	acquired.listeners = listeners

	// listenReports answers "which TCP address does a browser reach this hub
	// at", and every URL the hub derives for a browser or a remote worker asks
	// it instead of cfg.Listen -- because cfg.Listen is no longer the whole
	// answer: a desktop hub binds NO TCP address and can still gain one from
	// the extra_listen_addresses setting.
	//
	// The REPORTER owns the fallback to cfg.Listen, so the rule has one home
	// and no consumer repeats it. Extra addresses are hidden_in_hub, so only a
	// solo hub has any, and in `leapmux hub` and `leapmux dev` this returns
	// exactly what it always did.
	listenReports := listenReporter{set: listeners, configured: cfg.Listen}

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
	// synchronously so a broken store fails startup, before the hub constructs
	// any pool from a value it must never see change.
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
	rateLimitMgr := ratelimit.NewManager(setMgr)

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
	// that scope of damage structural rather than a property of whichever
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
		// AFTER the auth interceptor, because it reports state that belongs to
		// the credential that one resolved. It installs the holder
		// slideElevation writes into, and copies the new deadline onto the
		// response, so a client learns that its own restricted action moved the
		// window. Without it the client's mirror is early by up to
		// auth.ElevationWindow for the whole window, because a slide
		// deliberately emits no event.
		service.NewElevationSlideInterceptor(),
		captcha.NewInterceptor(captchaMgr),
		ratelimit.NewInterceptor(rateLimitMgr),
	)

	mux := http.NewServeMux()

	// Mail sender: resolves the SMTP configuration from the settings
	// snapshot on every Send, so `admin settings set smtp ...` applies to
	// the next email without a restart. With SMTP unconfigured it returns
	// the loud, matchable ErrEmailDisabled rather than silently dropping
	// mail. Email verification follows SMTP: EmailVerificationEffective is
	// true only when SMTP is enabled (no separate toggle). The recipient
	// cap wraps it so every mail type -- verification, recovery, worker
	// instructions, credential notices -- shares one per-recipient budget.
	mailSender := mail.NewRecipientLimitedSender(mail.NewSettingsSender(setMgr), setMgr, time.Now)
	// The renderer derives its base URL per render so a public_url change
	// reaches the next email's links and footers without a restart.
	// ONE closure for the browser-facing base URL. The mail renderer and the
	// OAuth issuer both need it, and two copies of the same two-call body are
	// two places a later edit can change one of.
	hubBaseURL := func() string {
		return settings.BaseURL(setMgr.Snapshot(context.Background()), listenReports.PrimaryListenAddr())
	}
	mailRenderer := mail.Renderer{BaseURL: hubBaseURL}

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
	// can act on -- it states the key and happens at registration. The registry
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
		Listen: listenReports, SoloGate: authContexts.SoloGate(),
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
	mux.Handle(contracts.WSRouteChannel, channelRelay)

	// INBOUND OAuth: the hub as a client of an identity provider, at
	// /auth/idp/*.
	idpHandler := service.NewIdPHandler(st, cfg, setMgr, lifecycle, ks)
	idpHandler.RegisterRoutes(mux)

	// OUTBOUND OAuth: the hub as an OAuth 2.1 authorization server, at
	// /oauth/* plus the two well-known metadata documents.
	oauthServer := service.NewOAuthServerHandler(service.OAuthServerDeps{
		SoloUser:  soloUser,
		SoloGate:  authContexts.SoloGate(),
		Store:     st,
		Validator: tokenValidator,
		Lifecycle: lifecycle,
		Settings:  setMgr,
		HubURL:    hubBaseURL,
		// The three anonymous legs share the interceptor's budget table; see
		// ratelimit.AllowHTTP.
		Limiter:  rateLimitMgr,
		Mail:     mailSender,
		Renderer: mailRenderer,
	})
	oauthServer.RegisterRoutes(mux)

	// Worker-issued delegation token mint/revoke endpoints. The credential
	// lifecycle effects are wired so revoking a delegation token evicts its
	// cached validation and authenticated leases and tears down any open E2EE
	// channels authorized by it (lifecycle.BearerRevoked).
	delegationHandler := service.NewWorkerDelegationHandler(st, tokenValidator, lifecycle)
	delegationHandler.RegisterRoutes(mux)

	// UserService drives credential-rotation paths (ChangePassword) through the
	// shared lifecycle, whose RevokeUserPreservingSession hard-closes every
	// channel a user owns alongside the delegation-token revocation.
	userSvc := service.NewUserService(st, cfg, setMgr, lifecycle, mailSender, mailRenderer, ks)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, connectOpts)
	mux.Handle(userPath, userHandler)

	// The admin services: the authenticated, online surface of hub
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
		// A captcha the operator enabled can still stand down at read time,
		// because ALTCHA needs a secure browser context this hub cannot
		// publish. Report the flag that is ENFORCED beside the one that is
		// stored, so the admin surface cannot say "enabled" for a control
		// that admits every request.
		settings.WithEffective(captcha.CaptchaEnabledKey.Name(), captcha.EnabledEffective),
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
		// The one rule that spans a settings key and an ACCOUNT: a solo hub
		// may not store an address other machines can reach while the account
		// that would guard it has no password. It is attached here rather than
		// in settingsregistry because it needs the gate, which the interceptor
		// above owns.
		settings.WithCrossValidation(refuseUnguardedExposure(cfg.SoloMode, authContexts.SoloGate())),
	)
	adminNetworkSvc := service.NewAdminNetworkService(service.AdminNetworkServiceDeps{
		Config: cfg, Settings: setMgr, SoloGate: authContexts.SoloGate(), Listen: listenReports,
	})
	adminNetworkPath, adminNetworkHandler := leapmuxv1connect.NewAdminNetworkServiceHandler(adminNetworkSvc, connectOpts)
	mux.Handle(adminNetworkPath, adminNetworkHandler)

	adminSettingsSvc := service.NewAdminSettingsService(setMgr, cfg, st)
	adminSettingsPath, adminSettingsHandler := leapmuxv1connect.NewAdminSettingsServiceHandler(adminSettingsSvc, connectOpts)
	mux.Handle(adminSettingsPath, adminSettingsHandler)

	adminUserSvc := service.NewAdminUserService(service.AdminUserServiceDeps{
		Store:         st,
		Settings:      setMgr,
		Validator:     tokenValidator,
		Lifecycle:     lifecycle,
		WorkerEffects: workerEffects,
		Mail:          mailSender,
		Renderer:      mailRenderer,
	})
	adminUserPath, adminUserHandler := leapmuxv1connect.NewAdminUserServiceHandler(adminUserSvc, connectOpts)
	mux.Handle(adminUserPath, adminUserHandler)

	adminWorkerSvc := service.NewAdminWorkerService(st, workerEffects)
	adminWorkerPath, adminWorkerHandler := leapmuxv1connect.NewAdminWorkerServiceHandler(adminWorkerSvc, connectOpts)
	mux.Handle(adminWorkerPath, adminWorkerHandler)

	adminIdPSvc := service.NewAdminIdPService(st, ks, idpHandler)
	adminIdPPath, adminIdPHandler := leapmuxv1connect.NewAdminIdPServiceHandler(adminIdPSvc, connectOpts)
	mux.Handle(adminIdPPath, adminIdPHandler)

	// AppService is the app REGISTRATION surface, and it is deliberately NOT an
	// admin service: an ordinary user registers apps for themself through it,
	// and the ownership rule -- not a role -- decides what each caller sees.
	// The two verbs that do need an administrator (a hub-wide registration and
	// a vouch) say so in their own handlers.
	appSvc := service.NewAppService(st, setMgr, tokenValidator, lifecycle)
	appPath, appHandler := leapmuxv1connect.NewAppServiceHandler(appSvc, connectOpts)
	mux.Handle(appPath, appHandler)

	sectionSvc := service.NewSectionService(st)
	sectionPath, sectionHandler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, connectOpts)
	mux.Handle(sectionPath, sectionHandler)

	workspaceSvc := service.NewWorkspaceService(st, crdtRegistry, reconcileNudger)
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
	mux.Handle(contracts.WSRouteUserEvents, userEventsHandler)

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
	frontendHandler, csp, frontendErr := resolveFrontend(so.frontendHandler, cfg.DevFrontend)
	if frontendErr != nil {
		return nil, acquired.close(frontendErr)
	}
	// The one application route that lives UNDER a Go subtree, mounted with an
	// EXACT pattern so http.ServeMux prefers it to idpHandler's `/auth/idp/`.
	// See service.IdPCompleteSignupPath: without this line the subtree swallows
	// the page every provider sign-up redirects to, and answers 400.
	mux.Handle(service.IdPCompleteSignupPath, frontendHandler)
	mux.Handle("/", frontendHandler)

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	// BaseContext seeds the context of EVERY accepted connection (and thus every
	// request and h2c stream) with handlerCtx. Cancelling handlerCtx during
	// shutdown cascades to r.Context() for in-flight handlers on both HTTP/1.1
	// and h2c, without per-handler wiring -- the structural limit for the class
	// of bug where a mux route doing unlimited I/O parks the drain. net/http
	// derives connCtx from BaseContext in Serve->conn.serve, and h2c's
	// sc.baseCtx comes from the same chain.
	server := &http.Server{
		// httpsec wraps the WHOLE mux, not the frontend handler alone: nosniff
		// and Referrer-Policy protect every response, and the hub renders HTML
		// outside the frontend handler (the device-code and PKCE callback
		// pages), which deserve the same treatment as the app document.
		Handler: requestsource.Middleware(setMgr,
			logging.HTTPMiddleware(metrics.HTTPMiddleware(httpsec.Middleware(csp, mux)))),
		// Limits reading request HEADERS on HTTP/1.1 connections (the
		// slowloris guard). It must NOT govern h2c connections, whose streams
		// outlive any header deadline. net/http disarms this deadline itself at
		// the HTTP/2 handoff, so the two shapes that also avoid the problem --
		// ReadHeaderTimeout=0 (which drops the slowloris guard on the public
		// listener) and ReadTimeout>0 (which arms a per-stream watchdog that
		// kills ACTIVE streams) -- are both unnecessary here. See
		// TestH2CStreamSurvivesHeaderTimeout, which pins that stdlib contract.
		ReadHeaderTimeout: 10 * time.Second,
		// The LISTENER decides whether a caller may skip authentication, and
		// this is where the hub records which one accepted the connection.
		// Solo authenticates a local IPC caller with no credentials, and every
		// TCP caller signs in once the account holds a password; see
		// auth.SoloGate.CredentialFree, which both ladders ask.
		//
		// Comparing the listener rather than the accepted connection's address
		// is what makes the mark reliable. A named-pipe connection reports a
		// network name that a third-party package owns, so a rename there
		// would silently turn every desktop request into a remote one -- while
		// this pointer is the listener the hub itself created.
		BaseContext: func(ln net.Listener) context.Context {
			if ln == localLn {
				return peer.WithLocalIPC(handlerCtx)
			}
			return handlerCtx
		},
		// The peer address, for the address-keyed budgets that guard the
		// unauthenticated endpoints. ConnContext derives from BaseContext, so
		// a local IPC connection carries both marks.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return peer.WithTransportAddr(ctx, c.RemoteAddr())
		},
		Protocols: protocols,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: 1000,
			// Without this a peer that stops draining its socket parks the
			// writer forever, and workermgr.Conn.Send holds the conn's mutex
			// across that write -- so ONE stalled worker blocks every other
			// sender to it (notifier, channel relay, shutdown notice) for the
			// process's life, and its Connect handler can never return. The
			// timeout is extended whenever any byte is written, so it caps
			// zero PROGRESS on a write, not the write's total duration: a slow
			// but moving consumer never trips it, however large the frame. 30s
			// is therefore about how long a stalled-but-recoverable socket may
			// take to accept a single byte -- generous for a paused VM or a
			// saturated link, and far short of leaving a stalled peer to hold
			// the conn mutex for the process's life.
			//
			// It does NOT limit a peer that stalls its HTTP/2 flow-control
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
	// safeguard.
	revWatcher := revocationwatcher.New(st, lifecycle)

	// The set serves through THIS server from here on; see setServer.
	listeners.setServer(server)

	// Bind the stored extra addresses, BEST EFFORT. A stored address stops
	// existing whenever a VPN drops or a DHCP lease moves, and a hub that
	// refuses to start because one of them is gone is worse than a hub that
	// starts and reports it: the operator needs the hub running to correct
	// the setting. The failures travel to the administration surface.
	//
	// This is also where a merge takes effect at startup: a stored `*:4327`
	// closes the `127.0.0.1:4327` socket bound above and takes the port.
	//
	// SOLO ONLY, and the mode test is here rather than in the listener set
	// because the key is HiddenInHub: `leapmux hub` and `leapmux dev` do not
	// list it, do not let an operator write it, and refuse the write with
	// "not administrable in hub mode". A data directory a `leapmux solo` run
	// wrote `0.0.0.0:8080` into and a later `leapmux hub` opened would
	// otherwise bind that address at every start, with no surface an operator
	// could see it in and no verb that could clear it.
	if cfg.SoloMode {
		extraAddrs, extraErr := settings.ExtraListenAddresses(startupSnap)
		if extraErr != nil {
			slog.Warn("the stored extra listen addresses could not be read; serving only the -listen address",
				"error", extraErr)
		} else {
			listeners.ApplyBestEffort(extraAddrs)
		}
	}

	// Apply a later change to the same row, so an operator who adds an address
	// in the preferences dialog is served on it without a restart. That is the
	// whole point of the setting; a restart-class key would store the intent
	// and answer nothing.
	//
	// A SECOND subscription rather than a branch inside the limits one above:
	// the two push unrelated state, the limits callback runs long before this
	// set exists, and the manager appends subscribers.
	//
	// BEST EFFORT, because the write already committed by the time this runs.
	// Refusing to bind the rest of the list would leave the hub in a state its
	// stored row does not describe, and would hide which entry is the problem;
	// the administration surface reports each failure against its address.
	//
	// The manager fires EVERY subscriber on every write -- a subscriber is
	// registered for the snapshot, not for one key -- so this runs for a
	// session duration or an SMTP change too. ApplyBestEffort returns at once
	// when the address list did not change, which is what keeps an unrelated
	// save off the sockets.
	//
	// SOLO ONLY, for the reason the startup apply gives.
	if cfg.SoloMode {
		setMgr.Subscribe(func(s *settings.Snapshot) {
			addrs, err := settings.ExtraListenAddresses(s)
			if err != nil {
				slog.Warn("the stored extra listen addresses could not be read; the served addresses are unchanged",
					"error", err)
				return
			}
			listeners.ApplyBestEffort(addrs)
		})
	}

	return &Server{
		cfg:               cfg,
		store:             st,
		keystore:          ks,
		settings:          setMgr,
		idpHandler:        idpHandler,
		server:            server,
		listeners:         listeners,
		listenerErrs:      listenerErrs,
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

// PrimaryListenAddr is the TCP address a browser reaches this hub at, in the
// form -listen carries, or "" when the hub binds no TCP address at all.
//
// The launchers print it. They cannot read cfg.Listen for that any more: a
// stored extra address can widen it (127.0.0.1:4327 merged into *:4327) or
// supply the only one there is (a desktop hub, which starts with none), and a
// banner naming an address the hub no longer answers on is worse than none.
func (s *Server) PrimaryListenAddr() string {
	return s.listeners.PrimaryListenAddr()
}

// PrintBannerURL prints the address an operator should open, to stderr.
//
// The two launchers gathered the same two arguments -- the public_url setting
// and the primary listen address -- and both are facts this server owns, so a
// third launcher would have copied the gather rather than called it. The RULE
// that public_url wins still lives once, in logging.PrintBannerURL.
func (s *Server) PrintBannerURL() {
	logging.PrintBannerURL(
		settings.KeyPublicURL.Of(s.settings.Snapshot(context.Background())),
		s.PrimaryListenAddr())
}

// HasNonLoopbackAddress reports whether the hub answers on an address another
// machine can reach.
//
// The solo launcher asks, AFTER NewServer returns, so its exposure warning
// reads the sockets the hub actually bound rather than the -listen string. A
// stored extra address is bound by then, and that is the whole difference:
// -listen alone cannot see the address a settings row published.
func (s *Server) HasNonLoopbackAddress() bool {
	return listenset.AnyNonLoopback(s.listeners.Bound())
}

// WorkerCredentials holds the credentials for a registered worker.
type WorkerCredentials struct {
	WorkerID  string
	AuthToken string
}

// RegisterWorker creates a worker record directly in the database,
// bypassing the normal registration-key flow. This is the in-process
// path used by the solo/dev binary to auto-register a co-located worker:
// since the caller already runs inside the same process as the
// hub, presenting a bearer token to a local RPC would add no
// security. Outside solo mode, all worker registration must go
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

// GetWorkerOwner returns the id of the user who registered workerID, and returns
// an error when the worker is unknown (or soft-deleted, which GetByID filters).
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
// should retry once one does.
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
	localLn := s.localLn
	listenURL := s.listenURL
	serveCtx, cancelServe := context.WithCancelCause(ctx)
	defer cancelServe(nil)

	// Register the watcher before starting listeners or other background work.
	// Without the singleton runtime lease, serving authenticated traffic would let
	// cleanup compact revocations this process did not observe.
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
			tcpListener:   s.listeners.Close(),
			localListener: closeServerListener(localLn),
			httpClose:     s.server.Close(),
			watcherClose:  watcherCloseErr,
			storeClose:    s.store.Close(),
		}.finalize()
	}

	// Start background OAuth token refresh.
	s.idpHandler.StartTokenRefresh(serveCtx)

	// Start periodic cleanup of soft-deleted records.
	cleanup.StartLoop(serveCtx, s.store)

	// Report the periods in which this process did not run, so a suspended laptop
	// explains the expired leases, ended streams, and refused credentials that
	// follow it instead of each one being read as its own failure. Process-wide
	// and idempotent, so a solo process starting a Hub and a Worker reports each
	// pause once.
	clockjump.StartLoop(serveCtx)

	// Start the revocation watcher: publishes and consumes the durable
	// revocation stream so admin-CLI mutations land in the hub's
	// in-memory caches and channelmgr without IPC. Seed past events that
	// predate this process so the first sweep only handles fresh work.
	s.revocationWatcher.StartLoop(serveCtx)

	shutdownDone := make(chan serverTeardownErrors, 1)
	go func() {
		<-serveCtx.Done()
		logShutdownCause(serveCtx)

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
		// unlimited outbound I/O) is cancelled. This is the structural limit
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
		// handlers to return first.) locallisten.CloseAccepted closes the
		// underlying pipe handles directly via the listener's own
		// accepted-connection tracking — the only level that sees every conn
		// THAT listener accepted.
		//
		// It is a safeguard, not the primary teardown: authContexts.Stop above
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

	// The TCP listeners are a SET that changes while the hub runs, so their
	// goroutines and their fan-in belong to it: it starts one per listener
	// here, starts another whenever a settings write adds an address, and
	// filters out the returns caused by its own deliberate closes. Only a
	// listener that stopped without being asked to reaches listenerErrs.
	//
	// The local IPC listener stays here, served directly. It is bound once
	// and never rebound, so it needs none of that machinery.
	s.listeners.Serve()
	localErrCh := make(chan error, 1)
	go func() { localErrCh <- s.server.Serve(localLn) }()

	if bound := s.listeners.Bound(); len(bound) > 0 {
		slog.Info("hub listening", "listen", boundAddressesForLog(bound), "local", listenURL)
	} else {
		slog.Info("hub listening", "local", listenURL)
	}

	var teardownErrs serverTeardownErrors
	localDone := false
	recordLocalResult := func(err error) {
		localDone = true
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		teardownErrs.localListener = errors.Join(teardownErrs.localListener, fmt.Errorf("serve: %w", err))
	}
	select {
	case err := <-localErrCh:
		recordLocalResult(err)
		cancelServe(err)
	case err := <-s.listenerErrs:
		teardownErrs.tcpListener = errors.Join(teardownErrs.tcpListener, err)
		cancelServe(err)
	case err := <-s.revocationWatcher.Errors():
		teardownErrs.primary = fmt.Errorf("revocation watcher failed: %w", err)
		cancelServe(err)
	case <-serveCtx.Done():
	}

	// Shutdown closes every listener. Drain their results before releasing
	// the store so no handler can race a closed database.
	if !localDone {
		recordLocalResult(<-localErrCh)
	}
	// The set's goroutines end when Shutdown closes their listeners; Close
	// waits for every one of them, so no handler outlives the store below.
	teardownErrs.tcpListener = errors.Join(teardownErrs.tcpListener, s.listeners.Close())

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
	teardownErrs.foldPendingListenerError(s.listenerErrs)
	teardownErrs.foldPendingWatcherError(s.revocationWatcher.Errors())
	teardownErrs.storeClose = s.store.Close()
	return teardownErrs.finalize()
}

// logShutdownCause reports why the Hub stops, at the moment it decides to.
//
// Every cancelServe call site passes a cause, and without this the log could not
// tell them apart: a user bug report showed "hub shutting down..." reading
// identically for an ordinary Ctrl-C and for a fatal revocation-watcher failure,
// with the real reason surfacing only much later, folded into the aggregate
// error Serve returns. An operator reading forward saw a Hub stop for no stated
// reason.
//
// A plain cancellation is an exit somebody asked for and carries nothing worth a
// field, so it keeps the bare line it always had.
func logShutdownCause(serveCtx context.Context) {
	if cause := context.Cause(serveCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		slog.Error("hub shutting down after a fatal error", "cause", cause)
		return
	}
	slog.Info("hub shutting down...")
}

// foldPendingListenerError folds a still-buffered TCP listener fault into
// tcpListener. Serve's teardown select consumes exactly ONE of its cases, so a
// listener that died on its own while another cause was also ready leaves its
// error unread in the buffered channel -- and unlike the local listener, which
// the drain below it reads, nothing else ever reads this one. The aggregate
// then reports the winning cause and says nothing about the address the hub
// stopped answering on. This is a non-blocking drain, and a no-op when the
// select already consumed the fault or none occurred.
func (e *serverTeardownErrors) foldPendingListenerError(listenerErrs <-chan error) {
	select {
	case listenerErr := <-listenerErrs:
		if listenerErr != nil {
			e.tcpListener = errors.Join(e.tcpListener, listenerErr)
		}
	default:
	}
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

// acquiredResources tracks the Hub resources NewServer acquired so far, so
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
	// tcpLn is the base listener BEFORE the listener set exists to own it.
	// NewServer clears it and sets listeners at the handover, so the two are
	// never both populated and the socket is never closed twice.
	tcpLn          net.Listener
	listeners      *listenerSet
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
		tcpListener:   errors.Join(closeServerListener(r.tcpLn), closeListenerSet(r.listeners)),
	}.finalize()
}

func closeListenerSet(set *listenerSet) error {
	if set == nil {
		return nil
	}
	return set.Close()
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
