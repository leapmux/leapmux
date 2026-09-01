// Package worker provides an exported entry point for running the
// LeapMux worker as a library (e.g. from the solo/dev binary).
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/bootstrap"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/util/clockjump"
)

// RunConfig holds configuration for running the worker as a library.
type RunConfig struct {
	HubURL string // Hub server URL: http[s]://host:port, unix:<socket-path>, or npipe:<name>

	DataDir              string                      // Directory for persistent state
	AuthToken            string                      // Pre-provisioned auth token (skip registration)
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
	// RegisteredBy seeds the worker's owner, which gates every machine-scoped RPC
	// family (tunnels, file, git, sysinfo) -- see service.requireWorkerOwner. It is a
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
}

// Run starts the worker and blocks until ctx is cancelled.
// If AuthToken is set, registration is skipped and the worker connects directly.
func Run(ctx context.Context, cfg RunConfig) error {
	// Report the periods in which this process did not run. A standalone Worker
	// suspends with its laptop exactly as the desktop app does, and wakes into
	// closed streams and a Hub that already dropped its registration; without
	// this each of those reads as its own failure. Process-wide and idempotent,
	// so a solo process that also runs a Hub reports each pause once.
	clockjump.StartLoop(ctx)

	// Open the Worker-local database for persistent state.
	dbPath := filepath.Join(cfg.DataDir, "worker.db")
	sqlDB, err := workerdb.Open(dbPath, sqlitedb.Config{
		MaxConns:  cfg.DBMaxConns,
		CacheSize: cfg.DBCacheSize,
		MmapSize:  cfg.DBMmapSize,
	})
	if err != nil {
		return fmt.Errorf("open worker db: %w", err)
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			slog.Error("failed to close worker db", "error", closeErr)
		}
	}()

	if err := workerdb.Migrate(ctx, sqlDB); err != nil {
		return fmt.Errorf("migrate worker db: %w", err)
	}

	// ONE endpoint for the whole process: the worker's own Connect stream and
	// every hub call a spawned agent makes then share one connection pool per
	// protocol and one h2c verdict.
	hubEndpoint, err := hubtransport.New(cfg.HubURL)
	if err != nil {
		return fmt.Errorf("hub URL %q: %w", cfg.HubURL, err)
	}
	client := hub.New(hubEndpoint)
	defer client.Stop()

	// runShutdown drains service state. It must run BEFORE the bidi
	// stream is torn down, not merely before client.Stop(): Client.Connect
	// unregisters every watcher via channelMgr.CloseAll() as it unwinds,
	// so a Shutdown deferred behind ConnectWithReconnect broadcasts the
	// "[Worker disconnected - Press Enter to restart]" notice to an empty
	// registry and only reaches the database. The `leapmux worker` CLI
	// already ordered it this way; this entry point did not, which is the
	// same one-does-it-the-other-doesn't defect the bootstrap package
	// exists to remove -- startup was unified, teardown was not.
	var shutdownOnce sync.Once
	runShutdown := func() {}

	// Set up E2EE channel manager if composite key is provided.
	if cfg.CompositeKey != nil {
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

		wiring := bootstrap.Wire(bootstrap.Params{
			Ctx:                  ctx,
			Client:               client,
			DB:                   sqlDB,
			CompositeKey:         cfg.CompositeKey,
			EncryptionMode:       cfg.EncryptionMode,
			MaxIncompleteChunked: cfg.MaxIncompleteChunked,
			MaxMessageSize:       cfg.MaxMessageSize,
			WorkerID:             cfg.WorkerID,
			Name:                 workerName,
			HomeDir:              homeDir,
			DataDir:              cfg.DataDir,
			AuthToken:            cfg.AuthToken,
			SeedRegisteredBy:     cfg.RegisteredBy,
			AgentStartupTimeout:  cfg.AgentStartupTimeout,
			APITimeout:           cfg.APITimeout,
			// 0 (the default) lets the worker resolve the concurrency from this
			// machine's core count.
			AgentStartupConcurrency: cfg.AgentStartupConcurrency,
			UseLoginShell:           cfg.UseLoginShell,
			WakeLock:                wakeLockTracker,
		})

		runShutdown = func() {
			shutdownOnce.Do(func() {
				wiring.Shutdown()
			})
		}
		defer runShutdown()
	} else {
		// No composite key means no E2EE channel and therefore no service
		// to wire, but the retention loops are about rows on disk and still
		// have to run.
		bootstrap.StartRetentionLoops(ctx, sqlDB, cfg.DataDir)
	}

	// Detach the connect loop from ctx so cancellation reaches it only
	// after runShutdown has finished. Watching ctx directly here instead
	// would race: the loop and this goroutine would both observe the
	// cancellation, and CloseAll could win.
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()
	go func() {
		<-ctx.Done()
		runShutdown()
		// Announced BEFORE cancelRun, which is the whole point: everything after
		// this line is local teardown, so a launcher waiting on this can stop
		// the Hub as soon as the last frame is away instead of outliving a
		// database close it has no stake in.
		if cfg.Drained != nil {
			cfg.Drained()
		}
		cancelRun()
	}()

	client.ConnectWithReconnect(runCtx, cfg.AuthToken)
	return nil
}
