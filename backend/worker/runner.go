// Package worker provides an exported entry point for running the
// LeapMux worker as a library (e.g. from the solo/dev binary).
package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/bootstrap"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/clockjump"
	"github.com/leapmux/leapmux/util/drain"
)

// RunConfig holds configuration for running the worker as a library.
type RunConfig struct {
	HubURL string // Hub server URL: http[s]://host:port, unix:<socket-path>, or npipe:<name>
	// HubEndpoint, when non-nil, replaces HubURL. A caller that registered this
	// worker passes the endpoint it registered through, so it and its spawned
	// agents share transport configuration and the h2c verdict. Unary calls
	// reuse its connection pools; the Worker stream uses its WebSocket lane.
	HubEndpoint *hubtransport.Endpoint

	// Directory is the held, ownership-validated lease of the worker's
	// data directory. Run takes ownership of it: it validates the lease
	// before anything opens, derives every storage path from
	// Directory.Path(), and releases it after the database closes on
	// every return path.
	Directory            *lifecycle.DirectoryLease
	AuthToken            string                      // Pre-provisioned auth token Run connects with
	CompositeKey         *noiseutil.CompositeKeypair // Worker's composite keypair for E2EE channels
	WorkerID             string                      // Worker ID (from registration)
	Name                 string                      // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	DBMaxConns           int                         // Maximum number of open database connections (0 = default)
	DBCacheSize          int                         // SQLite page cache size (positive = pages, negative = KiB; 0 = default)
	DBMmapSize           int                         // SQLite memory-mapped I/O size in bytes (0 = disabled)
	MaxIncompleteChunked int                         // Maximum in-flight chunked sequences per channel (0 = 4 default)
	MaxMessageSize       int                         // Maximum application payload size in bytes (0 = 16 MiB default)
	AgentStartupTimeout  time.Duration               // Timeout for agent startup handshake (0 = 5m default)
	APITimeout           time.Duration               // Timeout for JSON-RPC requests (0 = 10s default)
	// AgentStartupConcurrency caps how many agent processes may be inside their
	// startup handshake at once (0 = 4, or the CPU core count when that is
	// lower). It does not limit how many agents run.
	AgentStartupConcurrency int
	EncryptionMode          leapmuxv1.EncryptionMode // Encryption mode (classic, post-quantum)
	UseLoginShell           bool                     // Wrap claude invocation in user's login shell
	// RegisteredBy seeds the worker's owner, which restricts every machine-scoped
	// RPC family (tunnels, file, git, sysinfo) -- see service.ownerOnlyRegistrar. It is a
	// DB-sourced seed for the in-process launchers (solo reads it from
	// workers.registered_by); the Hub's connect-time WorkerIdentity overrides it, and
	// is the authority. Leave it empty and the Hub still establishes it.
	RegisteredBy string
	// Drained, when non-nil, is called once the shutdown that NEEDS A LIVE HUB
	// has finished -- the goodbye broadcast and the outbound flush -- and before
	// the purely local teardown (connect loop, database, wake locks) that
	// follows it.
	//
	// It exists for the in-process launchers, which stop the Hub themselves: the
	// only signal they had was the whole of Run returning, so they either
	// stopped the Hub while the goodbye was still in flight or padded a
	// wall-clock guess over work the Hub has no reason to outlive. Called at
	// most once, on the shutdown path only; a Run that fails before shutdown
	// never calls it, so callers must not treat it as a required signal.
	Drained func()
	// OnDeregister, when non-nil, is called when the Hub tells this worker to
	// stop for good: a deregistration notification on the stream, or a Connect
	// refusal that says the worker was deleted. The Hub-directed path calls it
	// before requesting drain. Cancellation can begin draining concurrently;
	// Run still joins the callback before closing storage.
	// Run drains, calls Drained, and returns callback or cleanup failures.
	OnDeregister func() error
	// OnReady, when non-nil, runs after local database initialization
	// and runtime wiring complete: the point at which the worker is
	// locally ready whatever its Hub connection is doing. It runs at
	// most once, and never on a startup that fails before it.
	OnReady func()
	// ShutdownDeadline ends every wait of the shutdown; see
	// lifecycle.ShutdownDrainTimeout. Run starts it when ctx ends. A combined
	// instance passes the one it shares with its Hub; the combined shutdown
	// starts it before it cancels ctx. Nil makes Run create its own.
	//
	// A shutdown that the Hub starts by deregistering this worker does not
	// start it: no service manager counts down to SIGKILL then. A cancelled
	// ctx starts it, also during that shutdown, and its waits then end at
	// the deadline.
	ShutdownDeadline *drain.Deadline
	// OnConnectionState, when non-nil, reports the Hub connection's
	// transitions: the accepted Hub identity once the first stream is
	// admitted, and each later disconnection. A worker whose Hub is
	// unavailable becomes locally ready and reports reconnecting through
	// this hook, so a launcher can distinguish "registered and ready"
	// from "registered and connected".
	OnConnectionState func(state ConnectionState)
}

// ConnectionState is one Hub-connection transition a launcher observes.
type ConnectionState struct {
	// Connected is true when the Hub accepted this worker's identity.
	Connected bool
	// HubURL is the endpoint the transition concerns.
	HubURL string
	// Err states the disconnection's cause when Connected is false.
	Err error
}

type runCleanup struct {
	closeDatabase  func(*sql.DB) error
	closeDirectory func(*lifecycle.DirectoryLease) error
}

// Run starts the worker and blocks until ctx is cancelled, the Hub stops this
// worker for good, or a local failure ends it, such as a database that does not
// open. The Hub stops a worker by deregistering it, reporting it deleted, or
// refusing its keys. A nil return with ctx still live means that the Hub deleted
// or deregistered the worker. A refusal of the keys returns an error that wraps
// hub.ErrKeysRefused.
//
// Run can return while ctx is still live, so nothing it starts depends on ctx to
// end. Wiring.Shutdown stops and waits for every loop that reads the database
// before Run closes it: the reconciler, the resume sweep and the periodic loops.
// Each of those waits ends at the shutdown deadline at the latest (see
// RunConfig.ShutdownDeadline), so the database close starts in time.
// Run also waits for its own goroutine that watches ctx. Everything else is only
// cancelled, and ends shortly after Run returns: the delegation janitor, the
// clock-jump detector and the goroutines of the Hub connection.
//
// It requires a keypair with a complete key set, an auth token and an
// encryption mode. The Hub pins the keys a Worker registers with and refuses a
// hello that presents others, so a Worker with no usable keypair could never
// connect. An empty token, and an unspecified encryption mode, are refused for
// ever. So Run states all three as errors instead of starting a process that
// cannot work. Its two callers always pass them: solo.bringUpLocalWorker, which
// solo, dev and the desktop app all reach, and the `leapmux worker` command.
func Run(ctx context.Context, cfg RunConfig) error {
	return run(ctx, cfg, runCleanup{closeDatabase: (*sql.DB).Close, closeDirectory: (*lifecycle.DirectoryLease).Close})
}

func run(ctx context.Context, cfg RunConfig, cleanup runCleanup) (result error) {
	// The lease is required, held for the worker component, and not
	// already released: every storage path below derives from it, and
	// its lock is what makes this process the directory's only worker.
	if cfg.Directory == nil {
		return errors.New("worker: RunConfig.Directory is required: Run owns the worker data directory's lease")
	}
	// Every supplied lease transfers at entry. The database's later close
	// defer runs first, so even rejection keeps the ownership contract.
	defer func() {
		if err := cleanup.closeDirectory(cfg.Directory); err != nil {
			result = errors.Join(result, fmt.Errorf("close worker data directory %s: %w", cfg.Directory.Path(), err))
		}
	}()
	if cfg.Directory.Mode() != lifecycle.RoleWorker {
		return fmt.Errorf("worker: RunConfig.Directory must hold the worker component's lease, not %s", cfg.Directory.Mode())
	}
	if cfg.Directory.Closed() {
		return errors.New("worker: RunConfig.Directory is a closed lease: acquire a fresh one for this run")
	}
	dataDir := cfg.Directory.Path()

	// workerkeys.Of refuses a nil keypair too. Checking here, before anything
	// opens, also keeps bootstrap.Wire from panicking on a keypair it cannot
	// describe, on a goroutine that solo does not recover.
	publicKeys, err := workerkeys.Of(cfg.CompositeKey)
	if err != nil {
		return fmt.Errorf("worker: RunConfig.CompositeKey must hold a complete key set, because the Hub pins the keys a Worker registers with: %w", err)
	}
	if cfg.AuthToken == "" {
		return errors.New("worker: RunConfig.AuthToken is required: the Hub refuses a Worker with no auth token")
	}
	if cfg.EncryptionMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED {
		return errors.New("worker: RunConfig.EncryptionMode is required: the Hub refuses a Worker hello with no encryption mode")
	}
	if err := channelwire.ValidateConfiguredMaxMessageSize(cfg.MaxMessageSize); err != nil {
		return fmt.Errorf("worker: validate runtime configuration: %w", err)
	}

	// Report the periods in which this process did not run. A standalone Worker
	// suspends with its laptop exactly as the desktop app does, and wakes into
	// closed streams and a Hub that already dropped its registration; without
	// this each of those reads as its own failure. Process-wide and idempotent,
	// so a solo process that also runs a Hub reports each pause once. Its context
	// ends with Run, which can return while ctx is live.
	clockCtx, cancelClock := context.WithCancel(ctx)
	defer cancelClock()
	clockjump.StartLoop(clockCtx)

	// Open the Worker-local database for persistent state.
	dbPath := filepath.Join(dataDir, "worker.db")
	sqlDB, err := workerdb.Open(dbPath, sqlitedb.Config{
		MaxConns:  cfg.DBMaxConns,
		CacheSize: cfg.DBCacheSize,
		MmapSize:  cfg.DBMmapSize,
	})
	if err != nil {
		return fmt.Errorf("open worker db: %w", err)
	}
	defer func() {
		if closeErr := cleanup.closeDatabase(sqlDB); closeErr != nil {
			result = errors.Join(result, fmt.Errorf("close worker database %s: %w", dbPath, closeErr))
		}
	}()

	if err := workerdb.Migrate(ctx, sqlDB); err != nil {
		return fmt.Errorf("migrate worker db: %w", err)
	}

	// One endpoint supplies transport configuration and the h2c verdict for
	// the whole process. Unary calls share its connection pools, while the
	// Worker stream uses its WebSocket lane.
	hubEndpoint := cfg.HubEndpoint
	if hubEndpoint == nil {
		hubEndpoint, err = hubtransport.New(cfg.HubURL)
		if err != nil {
			return fmt.Errorf("hub URL %q: %w", cfg.HubURL, err)
		}
	}
	slog.Info("worker initialized",
		"worker_id", cfg.WorkerID,
		"hub", hubEndpoint.URL(),
		"key_fingerprint", workerkeys.Fingerprint(publicKeys),
		"encryption_mode", cfg.EncryptionMode,
	)
	client := hub.New(hubEndpoint)
	// wiring is nil on every return before Wire succeeds, and WaitHandlers
	// tolerates nil. The closure is required: `defer wiring.WaitHandlers()`
	// captures its receiver here, while it is still nil, and so would skip the
	// join on every path. Deferred ABOVE client.Stop, so the LIFO unwind closes
	// the channels first, which ends the streaming handlers, then joins them,
	// and closes the database last:
	//   client.Stop -> WaitHandlers -> sqlDB.Close.
	var wiring *bootstrap.Wiring
	defer func() { wiring.WaitHandlers() }()
	defer client.Stop()

	homeDir, _ := os.UserHomeDir()

	wakeLockTracker := wakelock.NewActivityTracker()
	defer wakeLockTracker.Close()

	workerName := cfg.Name
	switch {
	case workerName != "":
	case os.Getenv("LEAPMUX_WORKER_NAME") != "":
		workerName = os.Getenv("LEAPMUX_WORKER_NAME")
	default:
		workerName, _ = os.Hostname()
	}

	shutdownDeadline := cfg.ShutdownDeadline
	if shutdownDeadline == nil {
		shutdownDeadline = drain.NewDeadline(quartz.NewReal(), lifecycle.ShutdownDrainTimeout)
	}

	// wireCtx limits what Wire starts: the delegation janitor, the cross-worker
	// client and the background loops, which runShutdown stops first. It ends with
	// ctx, and Run also ends it after its teardown, because Run can return while
	// ctx is live, when the Hub stops this worker for good. Deferred before
	// runShutdown, so it ends after the drain.
	wireCtx, cancelWire := context.WithCancel(ctx)
	defer cancelWire()

	wiring = bootstrap.Wire(bootstrap.Params{
		Ctx:                  wireCtx,
		Client:               client,
		DB:                   sqlDB,
		CompositeKey:         cfg.CompositeKey,
		EncryptionMode:       cfg.EncryptionMode,
		MaxIncompleteChunked: cfg.MaxIncompleteChunked,
		MaxMessageSize:       cfg.MaxMessageSize,
		WorkerID:             cfg.WorkerID,
		Name:                 workerName,
		HomeDir:              homeDir,
		DataDir:              dataDir,
		AuthToken:            cfg.AuthToken,
		SeedRegisteredBy:     cfg.RegisteredBy,
		AgentStartupTimeout:  cfg.AgentStartupTimeout,
		APITimeout:           cfg.APITimeout,
		// 0 (the default) lets the worker resolve the concurrency from this
		// machine's core count.
		AgentStartupConcurrency: cfg.AgentStartupConcurrency,
		UseLoginShell:           cfg.UseLoginShell,
		WakeLock:                wakeLockTracker,
		ShutdownDeadline:        shutdownDeadline,
	})

	// Local readiness: the database is open, migrated, and wired. A
	// registered worker with an unavailable Hub is ready HERE, and its
	// connection state says reconnecting -- local readiness and Hub
	// connectivity are separate facts and both hooks report them.
	if cfg.OnReady != nil {
		cfg.OnReady()
	}

	// runShutdown drains service state. It must run BEFORE the bidi
	// stream is torn down, not merely before client.Stop(): Client.Connect
	// unregisters every watcher via channelMgr.CloseAll() as it unwinds,
	// so a Shutdown deferred behind ConnectWithReconnect broadcasts the
	// "[Worker disconnected - Press Enter to restart]" notice to an empty
	// registry and only reaches the database. The sync.Once lets all exit paths
	// (ctx, a deregistration, the deferred fallback) converge on one invocation.
	var shutdownOnce sync.Once
	runShutdown := func() {
		shutdownOnce.Do(func() {
			slog.Info("draining worker state for graceful shutdown")
			wiring.Shutdown()
			slog.Info("worker state drained")
		})
	}
	defer runShutdown()

	// Detach the connect loop from ctx so cancellation reaches it only
	// after runShutdown has finished. Watching ctx directly here instead
	// would race: the loop and this goroutine would both observe the
	// cancellation, and CloseAll could win.
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()

	// stopGracefully is the one shutdown order of a worker that was told to
	// stop. Announcing Drained BEFORE cancelRun is the whole point: everything
	// after it is local teardown, so a launcher waiting on it can stop the Hub
	// as soon as the last frame is away instead of outliving a database close it
	// has no stake in.
	var stopOnce sync.Once
	stopGracefully := func() {
		stopOnce.Do(func() {
			runShutdown()
			if cfg.Drained != nil {
				cfg.Drained()
			}
			cancelRun()
		})
	}

	var deregisterErr error
	client.OnDeregister = func() {
		if cfg.OnDeregister != nil {
			if err := cfg.OnDeregister(); err != nil {
				deregisterErr = errors.Join(deregisterErr, err)
			}
		}
		stopGracefully()
	}

	// The goroutine that waits for ctx also ends when Run returns, and Run
	// waits for it. Run can return while ctx is live (the Hub stopped this
	// worker for good), and a goroutine still waiting then would hold the
	// whole wiring until the caller's context ends, and would call Drained
	// after Run had already torn down what Drained reports on.
	runReturned := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-ctx.Done():
			// The shutdown begins here. If a deregistration began it earlier,
			// its waits from now on end at the deadline too.
			shutdownDeadline.Start()
			stopGracefully()
		case <-runReturned:
		}
	})
	defer func() {
		close(runReturned)
		watcher.Wait()
	}()

	// The connection transitions reach the launcher before the stream
	// loop takes over.
	if cfg.OnConnectionState != nil {
		client.OnConnectionState = func(connected bool, err error) {
			cfg.OnConnectionState(ConnectionState{Connected: connected, HubURL: cfg.HubURL, Err: err})
		}
	}
	connectionErr := client.ConnectWithReconnect(runCtx, cfg.AuthToken)
	if deregisterErr != nil {
		return errors.Join(connectionErr, deregisterErr)
	}
	return connectionErr
}
