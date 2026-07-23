// Package service implements the Worker-side business logic for E2EE channel
// requests. Each service registers its handlers with the inner RPC dispatcher,
// which routes decrypted InnerRpcRequests from the Frontend to the appropriate
// handler function.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SendFunc sends a ConnectRequest message to the Hub.
type SendFunc func(msg *leapmuxv1.ConnectRequest) error

// Service holds the shared dependencies and runtime state behind every
// worker-side RPC handler. Its methods ARE the handlers; RegisterAll wires
// them into the inner-RPC dispatcher.
type Service struct {
	// ---- Injected wiring: assigned during construction and bootstrap
	// (New, then the worker entry points cmd/leapmux/worker.go and
	// worker/runner.go; test setup additionally substitutes the function
	// seams, always before the first dispatch). Nothing here is written
	// once handlers start dispatching, so handlers read these without
	// synchronisation.
	//
	// Keep it that way: what makes it safe is bootstrap sequence, not a
	// lock. Every field here is assigned before RegisterAll, which is the
	// last point at which a write is unambiguously ordered ahead of any
	// handler goroutine. A write added after it would be a data race on
	// whatever handler reads the field. ----

	// Config is embedded rather than copied field by field, so a field
	// added to Config is reachable on Service the moment it is declared.
	// A hand-written copy in New made "declared but never wired" a silent
	// failure -- the one hazard a parameter object otherwise introduces
	// while removing the swapped-argument one.
	Config

	Queries  *db.Queries
	Watchers *WatcherManager // Fan-out manager for event broadcasting
	Output   *OutputHandler  // Agent output NDJSON processor

	// RemoteIPC supplies per-agent local-IPC servers for the
	// `leapmux remote` CLI. Nil disables remote control (env vars are
	// not injected and no socket is created).
	//
	// It is NOT part of Config: remoteipc.Factory takes the Service as its
	// Authorizers, so it cannot be built until New has returned. Bootstrap
	// assigns it before RegisterAll, which keeps it inside this block's
	// contract.
	RemoteIPC RemoteIPCFactory

	startAgentFn        func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error)
	startTerminalFn     func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error
	createAgentRecordFn func(context.Context, db.CreateAgentParams) error
	getAgentByIDFn      func(context.Context, string) (db.Agent, error)

	// ---- Mutable runtime state: everything that changes over the worker's
	// life, touched concurrently by the handler goroutines DispatchAsync
	// spawns. The fields mutated in place (registeredBy, Cleanup, the two
	// cleanup registries, the sync.Maps) each carry their own
	// synchronisation. The pointer fields are assigned once but own the
	// mutable state behind them -- the startup registries and PrivateEvents
	// guard theirs internally, and FileTabPaths keeps its rows in the DB.
	// Do not add a plain, unsynchronised mutable field to this block. ----

	// registeredBy is the user who registered this worker -- the fact
	// requireWorkerOwner gates every machine-scoped RPC family (file, git, sysinfo,
	// tunnel) on. The Hub delivers it on every Connect (leapmuxv1.WorkerIdentity);
	// see UpdateRegisteredBy.
	//
	// Atomic rather than a plain field because the two accesses genuinely race: the
	// connect loop writes it once per connection, while handlers read it per-RPC on
	// goroutines DispatchAsync spawned. Within ONE connection the spawn orders them,
	// but a RECONNECT does not -- Manager.CloseAll cancels session contexts without
	// waiting for in-flight handlers, so a handler from the previous connection can
	// still be inside requireWorkerOwner when the next connection's receive loop
	// writes. The value is identical every time, which is exactly what makes a plain
	// field's race invisible until the detector or a torn read finds it.
	registeredBy atomic.Pointer[userid.UserID]

	// AgentStartup / TerminalStartup track in-flight startups — the
	// window between OpenAgent/OpenTerminal returning and the subprocess
	// being ready. See startupstate.go.
	AgentStartup    *startupRegistry[leapmuxv1.AgentStatus]
	TerminalStartup *startupRegistry[leapmuxv1.TerminalStatus]

	// PrivateEvents is the worker-local pub/sub for E2EE-only events
	// (TabRenamed, FileTabPathRegistered, FileTabPathRevoked). Always
	// non-nil after New.
	PrivateEvents *PrivateEventsBus

	// FileTabPaths persists (org_id, tab_id) -> (workspace_id,
	// file_path) for FILE-typed tabs. Always non-nil after New.
	// The hub never sees these rows; clients fetch paths over E2EE.
	FileTabPaths *FileTabPathStore

	// Cleanup tracks in-flight close handlers so Shutdown() can wait for
	// them to finish before DB/data-dir teardown. Close handlers must
	// Add(1) at entry and defer Done() so Wait() in Shutdown observes
	// them even if a handler panics.
	Cleanup sync.WaitGroup

	// agentCleanups / terminalCleanups hold per-tab cleanup callbacks
	// registered by spawn*RemoteIPC and fired on close (or before a
	// restart mints a new token). Same shape, two embeddings keep the
	// terminal-vs-agent intent at the call site.
	agentCleanups    cleanupRegistry
	terminalCleanups cleanupRegistry

	// localAuthorizers maps synthetic local-IPC stream ids to their
	// LocalIPCAuthorizer. The router populates the entry around each
	// dispatch so requireAccessibleWorkspace can answer access checks
	// for callers that don't have an E2EE channel.
	localAuthorizers sync.Map

	// worktreeRemovalLocks serializes the read-modify-remove sequence
	// (drop tab link -> count remaining -> `git worktree remove`) per
	// worktree id. DeleteBranchDialog fires every tab's REMOVE close
	// concurrently (each on its own dispatcher goroutine), so without
	// this two closes could both observe CountWorktreeTabs == 0 and both
	// shell out `git worktree remove` + `git branch -D` on the same repo.
	// Keyed by worktree id -> *sync.Mutex; different worktrees never
	// contend. Entries are never deleted (bounded by the worker's
	// distinct-worktree count over its lifetime).
	worktreeRemovalLocks sync.Map
}

// worktreeRemovalLock returns the per-worktree mutex that serializes the
// count-then-remove critical section in closeTabCommon.
func (svc *Service) worktreeRemovalLock(worktreeID string) *sync.Mutex {
	v, _ := svc.worktreeRemovalLocks.LoadOrStore(worktreeID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// cleanupRegistry holds id → cleanup callbacks under a single mutex.
// Used twice on Service (agentCleanups, terminalCleanups) so the two
// tab kinds keep distinct namespaces.
type cleanupRegistry struct {
	mu sync.Mutex
	m  map[string]func()
}

func (r *cleanupRegistry) register(id string, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]func(){}
	}
	r.m[id] = fn
}

func (r *cleanupRegistry) run(id string) {
	r.mu.Lock()
	fn, ok := r.m[id]
	delete(r.m, id)
	r.mu.Unlock()
	if ok && fn != nil {
		fn()
	}
}

// spawnRemoteIPC mints LEAPMUX_REMOTE_* env vars and registers the
// matching cleanup so a later close (or pre-restart re-mint) retires
// the token. kind is the user-facing tab type ("agent" or "terminal")
// — embedded in the log message and the slog id field. phase is an
// optional correlation tag ("open" / "restart" for terminals, empty
// for agents). Returns (nil, nil) when RemoteIPC is disabled or the
// factory fails DEGRADABLY; in both cases the tab still spawns, just
// without remote control.
//
// It returns a non-nil error ONLY for ErrMissingIdentity, which is
// fatal: callers MUST abort the spawn rather than continue without
// remote control, because a tab started as nobody surfaces to the user
// as an unrelated "socket not configured" error from `leapmux remote`
// with nothing naming the cause. The factory call is supplied as a closure so the
// generic helper doesn't have to know about the AgentSpawnInfo /
// TerminalSpawnInfo type split.
func (svc *Service) spawnRemoteIPC(
	kind, tabID, phase string,
	register func(string, func()),
	call func() ([]string, func(), error),
) ([]string, error) {
	if svc.RemoteIPC == nil {
		return nil, nil
	}
	envs, cleanup, err := call()
	if err != nil {
		attrs := []any{kind + "_id", tabID, "error", err}
		if phase != "" {
			attrs = append(attrs, "phase", phase)
		}
		// A missing identity is FATAL, not degradable. Every other factory
		// failure loses remote control and keeps the tab; this one would start
		// the tab as nobody, and the symptom the user hits is an unrelated
		// "socket not configured" error from `leapmux remote` with nothing
		// naming the cause. Fail the spawn so it reports itself.
		if errors.Is(err, ErrMissingIdentity) {
			slog.Error("remote IPC spawn has no user identity; refusing to start "+kind, attrs...)
			return nil, err
		}
		slog.Warn("remote IPC factory failed; "+kind+" will start without remote control", attrs...)
		return nil, nil
	}
	if cleanup != nil {
		register(tabID, cleanup)
	}
	return envs, nil
}

// agentStartupTimeout returns the configured agent startup timeout,
// or the default if not set.
func (svc *Service) agentStartupTimeout() time.Duration {
	if svc.AgentStartupTimeout > 0 {
		return svc.AgentStartupTimeout
	}
	return time.Duration(config.DefaultAgentStartupTimeoutSeconds) * time.Second
}

// agentAPITimeout returns the configured API timeout, or the default if not set.
func (svc *Service) agentAPITimeout() time.Duration {
	if svc.APITimeout > 0 {
		return svc.APITimeout
	}
	return agent.DefaultAPITimeout
}

// Config is the full injected wiring for a Service. Every field a worker
// entry point supplies lives here, so "did I remember to set Send?" is a
// question the struct literal answers rather than one Init discovers at
// runtime.
//
// It is a struct rather than a parameter list because the two adjacent
// path strings are otherwise silently swappable, and because the set has
// grown past what positional arguments can keep readable. Service embeds
// it, so every field below is readable as svc.<Field>.
type Config struct {
	// Enforced: New panics without Channels or Send, since a Service
	// missing either cannot answer a single RPC.
	Channels *channel.Manager // E2EE channel manager (for workspace access lookups)
	Send     SendFunc         // Forwards messages to the Hub via WebSocket

	// Supplied by every worker entry point, but NOT validated: a zero
	// value here surfaces as a nil dereference at first use rather than a
	// startup panic. Listed apart from the enforced pair above so the
	// comment states what the code actually checks.
	DB        *sql.DB
	Agents    *agent.Manager
	Terminals *terminal.Manager
	HomeDir   string // Empty disables ~ expansion in workspace paths
	DataDir   string // Empty makes data-dir-relative paths resolve to CWD

	// Optional.
	WorkerID string // This worker's ID (set after registration)
	Name     string // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	// SeedRegisteredBy seeds the worker owner. The Hub is the authority
	// and re-delivers it on every Connect, so an entry point that expects
	// the Hub to supply it leaves this empty (see UpdateRegisteredBy).
	//
	// Named "Seed" rather than "RegisteredBy" because Service embeds
	// Config and already has a RegisteredBy() accessor over the atomic the
	// Hub writes; a promoted field of that name would compile while
	// shadowing nothing and reading like the live value.
	SeedRegisteredBy    string
	AgentStartupTimeout time.Duration             // Timeout for agent startup handshake (default: 5m)
	APITimeout          time.Duration             // Timeout for JSON-RPC requests (default: 10s)
	UseLoginShell       bool                      // Wrap claude invocation in user's login shell
	WakeLock            *wakelock.ActivityTracker // Keep-awake tracker (nil = disabled)
}

// New creates a fully wired Service.
//
// It panics if Channels or Send are missing, because a Service without
// either cannot answer a single RPC and the two worker entry points
// always supply both. Config makes the omission visible at the struct
// literal; this is the backstop for an empty or partially-filled one, so
// the failure lands at the line that built it rather than on the first
// request.
//
// There is no second Init step. There used to be, because Channels and
// Send arrived after construction -- Config carries them now, so a
// separate call could only be forgotten. What genuinely cannot happen in
// a constructor is the DB read that re-arms persisted schedules; that is
// RestoreState, which says so in its name.
func New(cfg Config) *Service {
	if cfg.Channels == nil {
		panic("service.New: Channels must be set")
	}
	if cfg.Send == nil {
		panic("service.New: Send must be set")
	}

	queries := db.New(cfg.DB)
	watchers := NewWatcherManager()
	output := NewOutputHandler(cfg.DB, queries, watchers, cfg.Agents, cfg.WakeLock)
	output.DataDir = cfg.DataDir
	svc := &Service{
		Config:          cfg,
		Queries:         queries,
		Watchers:        watchers,
		Output:          output,
		AgentStartup:    newAgentStartupRegistry(),
		TerminalStartup: newTerminalStartupRegistry(),
		PrivateEvents:   NewPrivateEventsBus(),
	}
	// The seed is config data, so it is minted here -- the one place the raw
	// string exists -- rather than inside the setter.
	if seed, ok := userid.New(cfg.SeedRegisteredBy); ok {
		svc.SetRegisteredBy(seed)
	}
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, svc.PrivateEvents)
	svc.startAgentFn = svc.Agents.StartAgent
	svc.startTerminalFn = svc.Terminals.StartTerminal
	svc.createAgentRecordFn = svc.Queries.CreateAgent
	svc.getAgentByIDFn = svc.Queries.GetAgentByID

	// Wire auto-continue so OutputHandler can send synthetic user messages.
	// An auto-continue injection is not a human-typed input, so it stays
	// UNSPECIFIED (no scroll-rail jump dot).
	svc.Output.SetSendMessageFunc(func(agentID, content string) {
		svc.sendSyntheticUserMessage(agentID, content, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)
	})
	// Let PersistSettingsRefresh detect the startup window so it doesn't
	// clobber a settings change made mid-startup (see SetAgentStartingFunc).
	svc.Output.SetAgentStartingFunc(func(agentID string) bool {
		_, _, _, ok := svc.AgentStartup.status(agentID)
		return ok
	})

	return svc
}

func (svc *Service) startAgent(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
	if svc.startAgentFn != nil {
		return svc.startAgentFn(ctx, opts, sink)
	}
	return svc.Agents.StartAgent(ctx, opts, sink)
}

// restartAgent preserves Manager.RestartAgent's stop-before-start ordering while
// routing the new process through the service-level starter seam.
func (svc *Service) restartAgent(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
	unlock := svc.Agents.LockAgent(opts.AgentID)
	defer unlock()

	svc.Agents.StopAndWaitAgent(opts.AgentID)
	return svc.startAgent(ctx, opts, sink)
}

func (svc *Service) startTerminal(ctx context.Context, opts terminal.Options, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
	if svc.startTerminalFn != nil {
		return svc.startTerminalFn(ctx, opts, outputFn, exitFn)
	}
	return svc.Terminals.StartTerminal(ctx, opts, outputFn, exitFn)
}

func (svc *Service) createAgentRecord(ctx context.Context, params db.CreateAgentParams) error {
	// Every agent row must carry a real provider: the client renders each of the
	// agent's messages through that provider's renderers, and createMessageRow
	// refuses to persist a message row for an UNSPECIFIED provider. Enforce the
	// invariant where rows are born so a misconfigured caller fails at creation
	// with a clear error, rather than later with a confusing "failed to persist
	// message" on the agent's first output. The SendAgentMessage path already
	// defaults an UNSPECIFIED request to a real provider before reaching here, so
	// this is a backstop that should never fire in practice.
	if params.AgentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return fmt.Errorf("refusing to create agent %q with UNSPECIFIED agent provider", params.ID)
	}
	if svc.createAgentRecordFn != nil {
		return svc.createAgentRecordFn(ctx, params)
	}
	return svc.Queries.CreateAgent(ctx, params)
}

func (svc *Service) getAgentByID(ctx context.Context, agentID string) (db.Agent, error) {
	if svc.getAgentByIDFn != nil {
		return svc.getAgentByIDFn(ctx, agentID)
	}
	return svc.Queries.GetAgentByID(ctx, agentID)
}

// RestoreState re-arms what a previous worker process left persisted --
// today, the auto-continue schedules whose timers inject synthetic user
// messages when they fire.
//
// Separate from New because it reads the database and starts timers.
// Construction should not do either: it is what lets every unit test
// build a Service without touching the auto_continue tables or arming a
// background goroutine it then has to stop.
//
// Agents and terminals need no equivalent: their status is derived from
// runtime state (HasAgent/HasTerminal), not from the DB, so there is no
// stale row to clear at startup.
func (svc *Service) RestoreState() {
	svc.Output.restoreAutoContinueSchedules()
}

// Shutdown persists in-memory terminal state to the database so it
// survives a worker restart. Call this before stopping the terminal
// manager (which clears in-memory state). Callers must have already
// stopped dispatching new OpenAgent/OpenTerminal/CloseAgent/CloseTerminal
// requests; otherwise the Wait calls below can race with a fresh Add.
func (svc *Service) Shutdown() {
	// Drain any goroutines spawned by OpenAgent/OpenTerminal so their
	// trailing DB writes and filesystem work land before the caller
	// closes the DB or removes data directories.
	svc.AgentStartup.WaitForInFlight()
	svc.TerminalStartup.WaitForInFlight()
	// Also drain in-flight close handlers. The frontend fires close RPCs
	// as fire-and-forget, so a close can still be running on the
	// dispatcher goroutine while the worker receives a deregister/SIGTERM.
	svc.Cleanup.Wait()

	for _, tid := range svc.Terminals.ListTerminalIDs() {
		// Already-exited terminals were persisted with their real exit
		// code by makeTerminalExitFn; don't clobber it with the shutdown
		// sentinel.
		if svc.Terminals.IsExited(tid) {
			continue
		}
		svc.persistTerminalOnExit(tid, exitCodeUnknown)
	}
}

// exitCodeUnknown is the sentinel used when the worker never observed
// the child's exit (forced Shutdown: the worker is tearing the child
// down without waiting for its exit code). Renders as the "Worker
// disconnected" wording rather than a literal "?" because in this path
// we *know* the worker killed the child — the exit code is unobserved,
// not unknown.
const exitCodeUnknown = -1

// terminalExitedNoticeSuffix is the constant trailing portion of every
// formatted notice. Idempotency checks use HasSuffix against this so a
// repeat call on a screen that already ends with any prior notice is a
// no-op. Both the "Terminal process exited (N)" and "Worker
// disconnected" notices end with this suffix so one check covers both.
var terminalExitedNoticeSuffix = []byte(" - Press Enter to restart]\r\n")

// formatTerminalExitedNotice renders the per-exit notice. The
// exitCodeUnknown sentinel produces the worker-disconnected wording;
// any other value produces the standard "Terminal process exited (N)"
// wording.
func formatTerminalExitedNotice(exitCode int) []byte {
	if exitCode == exitCodeUnknown {
		return []byte("\r\n\r\n[Worker disconnected - Press Enter to restart]\r\n")
	}
	return []byte("\r\n\r\n[Terminal process exited (" + strconv.Itoa(exitCode) + ") - Press Enter to restart]\r\n")
}

// persistTerminalOnExit injects the exit notice into the live terminal's
// screen buffer (so any subscriber sees it via the next broadcast) and
// writes the resulting row to the DB so it survives a worker restart.
// Single snapshot pass — the snapshot taken up front feeds both the
// idempotency check (skip AppendOutput when the notice is already there)
// and the DB-bound screen, avoiding the redundant RLock + HasSuffix that
// a separate appendTerminalExitedNotice + persist sequence would do.
// Falls back to metadata-only when no screen exists (e.g. terminal was
// killed before rendering) so title/dims are still saved. Returns false
// when neither snapshot nor meta is available — the row stays untouched.
//
// Always writes exitCode into the exit_code column EXCEPT when called
// with exitCodeUnknown on a screen that already carries the exit-notice
// suffix: that means the exit handler raced ahead and persisted the
// real exit code, so we skip to avoid clobbering it with the
// "worker disconnected" sentinel. The non-shutdown caller path (the
// exit handler itself) never passes exitCodeUnknown.
func (svc *Service) persistTerminalOnExit(tid string, exitCode int) bool {
	var (
		src    terminal.TerminalMeta
		screen []byte
		hasRow bool
	)
	if snap, ok := svc.Terminals.SnapshotTerminal(tid); ok {
		src, screen, hasRow = snap.TerminalMeta, snap.Screen, true
	} else if meta, ok := svc.Terminals.GetMeta(tid); ok {
		src, hasRow = meta, true
	}
	if !hasRow {
		return false
	}
	hasNotice := bytes.HasSuffix(screen, terminalExitedNoticeSuffix)
	if exitCode == exitCodeUnknown && hasNotice {
		// Shutdown lost the race to the exit handler — its persist already
		// landed a real exit code, leave the row alone.
		return true
	}
	if !hasNotice {
		notice := formatTerminalExitedNotice(exitCode)
		_ = svc.Terminals.AppendOutput(tid, notice)
		// SnapshotTerminal returns a freshly-allocated slice (tailBytesLocked),
		// so append can extend it directly without aliasing the manager's buffer.
		screen = append(screen, notice...)
	}
	// Shell column is INSERT-only (see UpsertTerminal SQL): UPDATE
	// preserves whatever was written at OpenTerminal time, so passing
	// the zero value here is a no-op on the existing row.
	params := db.UpsertTerminalParams{
		ID:            tid,
		WorkspaceID:   src.WorkspaceID,
		WorkingDir:    src.WorkingDir,
		HomeDir:       svc.HomeDir,
		ShellStartDir: src.ShellStartDir,
		Title:         src.Title,
		Cols:          int64(src.Cols),
		Rows:          int64(src.Rows),
		Screen:        screen,
		ExitCode:      int64(exitCode),
	}
	if err := svc.Queries.UpsertTerminal(bgCtx(), params); err != nil {
		slog.Error("failed to save terminal on exit", "terminal_id", tid, "error", err)
		return false
	}
	return true
}

// ownerOnlyRegistrar registers handlers that ONLY the worker's registered owner
// may call.
//
// It exists so the machine-scoped families cannot register an ungated handler by
// accident: they are handed this instead of the raw *channel.Dispatcher, so the
// gate is a property of where the handler is registered rather than a line each
// author must remember. Both Register and RegisterTracked are wrapped -- gating
// only one would leave a silent hole (git uses both). Each Register also records
// gateOwnerOnly on the shared registrar so TestEveryRegisteredMethodIsClassified
// sees the method without replaying the family register functions.
type ownerOnlyRegistrar struct {
	r registrar
}

func (o ownerOnlyRegistrar) gate(handler channel.HandlerFunc) channel.HandlerFunc {
	return func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		if !requireWorkerOwner(o.r.svc, userID, sender) {
			return
		}
		handler(ctx, userID, req, sender)
	}
}

func (o ownerOnlyRegistrar) Register(method string, handler channel.HandlerFunc) {
	o.r.register(method, gateOwnerOnly, dispatchPlain, o.gate(handler))
}

func (o ownerOnlyRegistrar) RegisterTracked(method string, handler channel.HandlerFunc) {
	o.r.register(method, gateOwnerOnly, dispatchTracked, o.gate(handler))
}

// SetRegisteredBy seeds the worker's owner before the Hub has delivered one.
//
// This is the SEED path only: the startup DB/config value, which is a cache the
// first connect overrides. The Hub owns workers.registered_by and is the only
// authority, so the connect loop must use UpdateRegisteredBy instead -- it
// carries a drift warning a seed has no basis to emit.
//
// It takes an already-minted userid.UserID rather than a string so there is no
// blank-input branch to disagree with UpdateRegisteredBy about. Those two took
// the same shape and answered a blank id differently -- this one cleared the
// owner, that one keeps it -- which is a trap for a reader picking between
// them. New() is the single caller and already gates on a non-empty
// cfg.SeedRegisteredBy, so the branch this removes was unreachable anyway.
func (svc *Service) SetRegisteredBy(userID userid.UserID) {
	if userID.IsZero() {
		return
	}
	svc.registeredBy.Store(&userID)
}

// UpdateRegisteredBy applies an owner the Hub delivered on connect.
//
// Both worker entry points (worker.Run's connect loop and the standalone
// `leapmux worker` command) wire this as their OnWorkerIdentity handler, so the
// compare/warn/store policy lives once next to the state it guards rather than
// being copy-pasted per entry point -- which is exactly how one of them would end
// up missing the empty guard below.
//
// An empty identity is refused rather than stored: requireWorkerOwner denies an
// empty owner, so clobbering a good owner with "" would make the worker deny its
// own legitimate user for the life of the connection, indistinguishably from a
// real cross-tenant refusal -- the failure this owner became Hub-pushed to avoid.
// Keeping the previous owner is the safe direction: the Hub cannot legitimately
// un-own a live worker (deregistration flows through OnDeregister instead), so an
// empty push is a bug or a truncated payload, never an instruction.
func (svc *Service) UpdateRegisteredBy(userID string) {
	uid, ok := userid.New(userID)
	if !ok {
		slog.Warn("hub delivered an empty worker owner, keeping current",
			"current", svc.RegisteredBy())
		return
	}
	if prev := svc.RegisteredBy(); !prev.IsZero() && !prev.MatchesUser(uid) {
		slog.Warn("hub reported a different worker owner",
			"previous", prev, "current", uid)
	}
	svc.registeredBy.Store(&uid)
}

// RegisteredBy returns the user who registered this worker, or the zero UserID
// if the Hub has not delivered it yet. requireWorkerOwner refuses a zero id
// via MatchesUser, so an undelivered identity fails closed rather than matching
// another empty id.
func (svc *Service) RegisteredBy() userid.UserID {
	if p := svc.registeredBy.Load(); p != nil {
		return *p
	}
	return userid.UserID{}
}

// requireWorkerOwner gates a handler on the caller being the worker's own
// registered owner, rejecting everyone else.
//
// It is the right gate for the families whose reach is the MACHINE rather than a
// workspace -- tunnels (arbitrary TCP out of the host), the file and git handlers
// (any absolute path; validate.SanitizePath normalizes and blocks traversal, it
// does not confine to a root), and sysinfo (which discloses the owner's home
// path). The owner already has all of this: their agents run as them on their own
// machine, so granting it over the channel adds nothing. Anyone ELSE holding a
// channel must not have it -- notably a delegation bearer, which is pinned to one
// workspace and is handed to a prompt-injectable agent. The Hub only ever opens a
// channel for the worker's own owner, so this gate is defence in depth today; it is
// what keeps any future way of reaching a worker failing closed rather than
// silently exposing the filesystem.
//
// Workspace-scoped families (agent, terminal, tab moves, cleanup) must NOT use
// this: they legitimately serve non-owners and gate on the Hub-supplied
// accessible-workspace set instead (requireAccessibleWorkspace and friends).
// An empty id on either side is refused rather than matched. MatchesUser
// fails closed when either side is zero, so a worker whose owner never got populated
// would refuse a caller the Hub named with an empty user id -- a gate whose
// whole purpose is to fail closed, failing closed on the one input it cannot
// judge. An empty owner now means only "the Hub has not delivered WorkerIdentity
// yet", which no handler can observe (identity precedes the first ChannelOpen
// on the same stream), but the refusal stays as the layer that makes that
// ordering non-load-bearing. Its sibling in this package's Hub counterpart
// (verifyDelegationWorkerScope) refuses an unrecorded minter for the same reason.
func requireWorkerOwner(svc *Service, userID userid.UserID, sender channel.ResponseWriter) bool {
	if userID.MatchesUser(svc.RegisteredBy()) {
		return true
	}
	sendPermissionDenied(sender, "only the worker owner may use this")
	return false
}

// RegisterAll registers all service handlers with the dispatcher.
//
// Every method records a methodGate at registration time (default-deny: a
// method with no recorded gate fails TestEveryRegisteredMethodIsClassified):
//
//   - gateOwnerOnly  — machine-scoped families (file/git/sysinfo/tunnel) via
//     ownerOnlyRegistrar, plus the capability probes ListAvailableShells /
//     ListAvailableProviders (which enumerate installed shells/agent CLIs) via
//     registerOwnerOnly; only the worker owner may call.
//   - gateWorkspace  — structural workspace gate via registerWorkspaceGated /
//     registerAgentGated / registerTerminalGated (+ Tracked / ByID /
//     ForRestart variants). Unmarshal + access check run before the handler
//     body; the ByID variants authorize via a workspace_id-only lookup for
//     handlers that never read the row.
//   - gateInBody     — heterogeneous in-body gates (file-tab-path dual checks,
//     MoveTabWorkspace TabType switch); probe-enforced completeness.
//   - gateSetFilter  — ListAgents / ListTerminals / WatchEvents filter via
//     AccessibleSet(); denial is an empty result, not PERMISSION_DENIED.
//   - gateNone       — Ping; a liveness probe that does no work and discloses
//     nothing, ungated by design.
//
// RegisterAll also binds svc.Cleanup as the drain every RegisterTracked
// dispatch gates on, so a dispatcher cannot carry this service's tracked
// handlers without the WaitGroup Shutdown waits on. Both objects are
// already arguments here, which is what makes the binding derivable from
// the call rather than a second one an entry point has to remember: a
// worker entry point once shipped without it, and registering tracked
// handlers against an unbound dispatcher is silent -- the Add(1)/Done()
// pair is skipped, so Shutdown waits on an always-zero WaitGroup and
// returns while a close handler is still mutating the DB that teardown is
// about to shut.
func RegisterAll(d *channel.Dispatcher, svc *Service) {
	d.BindCleanup(&svc.Cleanup)
	_ = registerAllWithGates(d, svc)
}

// registerAllWithGates is the registration body shared by RegisterAll and
// tests that need the gate map. Production call sites use RegisterAll; tests
// call this on a throwaway dispatcher so the gate map and Methods() come from
// the same function production runs — replay drift is impossible.
func registerAllWithGates(d *channel.Dispatcher, svc *Service) map[string]methodGate {
	gates, _ := registerAllClassified(d, svc)
	return gates
}

// registerAllClassified is registerAllWithGates plus the reply-shape map,
// so a test can assert both classifications came from the one function
// production runs.
func registerAllClassified(d *channel.Dispatcher, svc *Service) (map[string]methodGate, map[string]methodShape) {
	r := newRegistrar(d, svc)
	registerPingHandler(r, svc)
	// Machine-scoped: owner-only by construction (see ownerOnlyRegistrar).
	ownerOnly := ownerOnlyRegistrar{r: r}
	registerFileHandlers(ownerOnly, svc)
	registerGitHandlers(ownerOnly, svc)
	registerTerminalHandlers(r, svc)
	registerAgentHandlers(r, svc)
	registerCleanupHandlers(r, svc)
	registerTabMoveHandlers(r, svc)
	registerSysInfoHandlers(ownerOnly, svc)
	registerTunnelHandlers(ownerOnly)
	return r.gates, r.shapes
}

// optionGroupLabelInGroups returns the display label of the option group with the
// given id within a specific catalog, falling back to the id when the group is absent
// or unlabeled. The catalog-scanning core shared by buildSettingsChanges /
// applyOptionChanges (which fetch the catalog once and pass it in) and the sink's
// server-initiated settings_changed notifications (NotifyPermissionModeChanged).
func optionGroupLabelInGroups(groups []*leapmuxv1.AvailableOptionGroup, key string) string {
	if label := optionids.GroupByID(groups, key).GetLabel(); label != "" {
		return label
	}
	return key
}

// findOptionInGroup returns the option with id `value` in the group keyed by `key`, or nil
// when the group or option is absent. Shared by optionLabelInGroups and optionGroupOffersValue,
// which differ only in what they read off the match (its display name vs its mere presence).
func findOptionInGroup(groups []*leapmuxv1.AvailableOptionGroup, key, value string) *leapmuxv1.AvailableOption {
	for _, opt := range optionids.GroupByID(groups, key).GetOptions() {
		if opt.GetId() == value {
			return opt
		}
	}
	return nil
}

// optionLabelInGroups resolves an option value's display name within a specific
// option-group catalog, falling back to the raw value when the group or option is
// absent (or carries no name). The catalog-scanning core shared by applyOptionChanges,
// which feeds it the agent's live catalog, and resolveOptionValueLabel, which also
// consults the catalog persisted on the agent row.
func optionLabelInGroups(groups []*leapmuxv1.AvailableOptionGroup, key, value string) string {
	if opt := findOptionInGroup(groups, key, value); opt != nil {
		if name := opt.GetName(); name != "" {
			return name
		}
	}
	return value
}

// resolveOptionValueLabel resolves an option value's display name, preferring the
// live catalog and falling back to a historical (e.g. row-persisted) catalog, then
// the raw value. The live label wins so a still-offered value shows its current
// name; a value the live catalog has since dropped -- the model a session just
// switched away from (Claude hides standard-context Opus behind "default", so
// "opus[1m]" is listed only while active), or an effort tier the new model doesn't
// offer (e.g. "xhigh" after Opus->Sonnet) -- still resolves via the historical
// catalog instead of leaking the raw bracketed id into the settings_changed
// notification.
func resolveOptionValueLabel(live, prev []*leapmuxv1.AvailableOptionGroup, key, value string) string {
	// Prefer the live catalog when it actually OFFERS the value -- a presence check, not a
	// label-equality one. A self-named option (display name == id, e.g. a primary-agent id or
	// a sandbox policy like "read-only") has label == value, which a `label != value` test
	// would misread as "absent from live" and then let a stale historical name from prev win.
	// Only when the value is genuinely absent from the live catalog do we fall back to prev.
	if optionGroupOffersValue(live, key, value) {
		return optionLabelInGroups(live, key, value)
	}
	return optionLabelInGroups(prev, key, value)
}

// optionGroupOffersValue reports whether the group keyed by `key` lists `value` as one of
// its selectable options, regardless of whether that option carries a display name.
func optionGroupOffersValue(groups []*leapmuxv1.AvailableOptionGroup, key, value string) bool {
	return findOptionInGroup(groups, key, value) != nil
}

// protoJSONValue defers protojson.Format until a handler actually emits
// the record it is attached to.
//
// Go evaluates call arguments eagerly, so passing protojson.Format(msg)
// straight to slog.Debug runs a full reflection-driven proto->JSON
// serialization on every call and throws the string away at the default
// INFO level. These payloads ride the per-RPC and per-event hot paths,
// so that is real work per request and per streamed frame. slog resolves
// a LogValuer only once the record survives the level check, which for
// a disabled level is never.
type protoJSONValue struct{ msg proto.Message }

// LogValue implements slog.LogValuer.
func (p protoJSONValue) LogValue() slog.Value {
	return slog.StringValue(protojson.Format(p.msg))
}

// lazyProtoJSON wraps msg so its protojson rendering happens only if the
// log record is actually emitted.
func lazyProtoJSON(msg proto.Message) protoJSONValue {
	return protoJSONValue{msg: msg}
}

// sendProtoResponse is a helper that serializes a proto response and sends it.
func sendProtoResponse(sender channel.ResponseWriter, msg proto.Message) {
	slog.Debug("response payload", "payload", lazyProtoJSON(msg))
	data, err := proto.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal response", "error", err)
		_ = sender.SendError(int32(codes.Internal), "internal: marshal response")
		return
	}
	if err := sender.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: data}); err != nil {
		// A rejected message is the one send failure worth answering: the
		// channel refused THIS payload on its own terms (it is over the
		// size cap), so the transport is fine and a small reply will get
		// through. Discarding it left the caller with nothing at all on
		// the wire, waiting out its request timeout with no diagnosis --
		// and every oversize unary response looked identical to a hung
		// worker.
		//
		// Any other error means the transport itself is gone, where a
		// second send would fail the same way and there is nobody left to
		// read it.
		if errors.Is(err, channel.ErrMessageRejected) {
			slog.Warn("response too large for the channel; answering with an error instead",
				"bytes", len(data), "error", err)
			_ = sender.SendError(int32(codes.ResourceExhausted),
				"response too large to deliver; request a smaller range")
		}
	}
}

// unmarshalRequest is a helper that deserializes an InnerRpcRequest payload.
func unmarshalRequest(req *leapmuxv1.InnerRpcRequest, msg proto.Message) error {
	if err := proto.Unmarshal(req.GetPayload(), msg); err != nil {
		return err
	}
	slog.Debug("request payload",
		"method", req.GetMethod(),
		"payload", lazyProtoJSON(msg),
	)
	return nil
}

// sendInternalError sends an Internal error response.
func sendInternalError(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.Internal), msg)
}

// sendNotFoundError sends a NotFound error response.
func sendNotFoundError(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.NotFound), msg)
}

// sendPermissionDenied sends a PermissionDenied error response.
func sendPermissionDenied(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.PermissionDenied), msg)
}

// sendInvalidArgument sends an InvalidArgument error response.
func sendInvalidArgument(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.InvalidArgument), msg)
}

// sendStreamError reports a terminal failure on a STREAMING method.
//
// The sender helpers above emit an InnerRpcResponse, which the frontend
// routes through pendingRequests. A streaming call's correlation id lives
// in streamListeners instead, and deliverResponse drops a frame it finds
// no pending request for -- so an ordinary SendError on a stream is
// discarded silently and the client waits forever. Streaming handlers
// must report failure in-band, as an InnerStreamMessage with IsError.
func sendStreamError(sender channel.ResponseWriter, code codes.Code, msg string) {
	_ = sender.SendStream(&leapmuxv1.InnerStreamMessage{
		// End as well as IsError: this frame ENDS the stream, and saying so
		// is what lets a receiver terminate on it generically instead of
		// having to special-case the error flag. The browser checks isError
		// before end (see deliverStream), so it still reports the error
		// rather than a clean close.
		End:          true,
		IsError:      true,
		ErrorCode:    int32(code),
		ErrorMessage: msg,
	})
}

// sendFailedPrecondition sends a FailedPrecondition error response.
// Used when the request is valid but the target is not in a state that
// permits the operation (e.g. sending a message to an agent that is
// still starting up).
func sendFailedPrecondition(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.FailedPrecondition), msg)
}

// sendValidationError routes a validation failure to the most accurate
// gRPC code: context cancellation/timeout (client disconnect, hub-side
// deadline) maps to Canceled / DeadlineExceeded; everything else is the
// caller's bad input → InvalidArgument. Without the split, a client
// disconnect during a slow `git worktree list` validation surfaced as
// "invalid_argument: context canceled", which is misleading both for
// the user-facing toast and for any client-side telemetry that filters
// by code.
func sendValidationError(sender channel.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		_ = sender.SendError(int32(codes.Canceled), err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		_ = sender.SendError(int32(codes.DeadlineExceeded), err.Error())
	default:
		sendInvalidArgument(sender, err.Error())
	}
}

// requireAccessibleWorkspace verifies the workspace_id is accessible on the
// sender's channel. Sends PERMISSION_DENIED and returns false when the channel
// has no context or the workspace is not in its accessible set (populated at
// channel handshake by the hub's list of workspaces the user owns). The
// caller is responsible for rejecting empty workspace_id up front.
//
// Authorization is delegated to AuthorizerFor so both E2EE channels
// (channelmgr-backed) and local-IPC callers (registered LocalIPCAuthorizer)
// take the same code path. Callers that need the authorizer for follow-up
// checks (list filters, watcher subscriber ids) should use
// AuthorizerFor directly.
func (svc *Service) requireAccessibleWorkspace(sender channel.ResponseWriter, workspaceID string) bool {
	if !svc.workspaceAccessible(sender.ChannelID(), workspaceID) {
		sendPermissionDenied(sender, "workspace is not accessible")
		return false
	}
	return true
}

// workspaceAccessible is requireAccessibleWorkspace's decision without
// its reply. It exists for callers that must shape their own denial --
// a streaming registration, whose errors have to be stream frames -- so
// they can ask the same question rather than reimplementing it.
func (svc *Service) workspaceAccessible(channelID, workspaceID string) bool {
	return svc.AuthorizerFor(channelID).IsAccessible(workspaceID)
}

// requireAccessibleAgent looks up the agent and verifies its workspace is
// accessible on the sender's channel. Sends the appropriate error response
// and returns ok=false on empty id, missing row, db error, or denial. The
// returned Agent is the freshly-loaded row so callers can reuse it.
func (svc *Service) requireAccessibleAgent(sender channel.ResponseWriter, agentID string) (db.Agent, bool) {
	return requireAccessibleRow(
		svc, sender, agentID, "agent",
		svc.Queries.GetAgentByID,
		func(a db.Agent) string { return a.WorkspaceID },
	)
}

// requireAccessibleTerminal looks up the terminal and verifies its workspace
// is accessible on the sender's channel. Mirror of requireAccessibleAgent.
func (svc *Service) requireAccessibleTerminal(sender channel.ResponseWriter, terminalID string) (db.Terminal, bool) {
	return requireAccessibleRow(
		svc, sender, terminalID, "terminal",
		svc.Queries.GetTerminal,
		func(t db.Terminal) string { return t.WorkspaceID },
	)
}

// requireAccessibleTerminalForRestart is the narrow-query variant used
// by the RestartTerminal handler: returns metadata + length(screen)
// without loading the screen BLOB. See GetTerminalForRestart for why.
func (svc *Service) requireAccessibleTerminalForRestart(sender channel.ResponseWriter, terminalID string) (db.GetTerminalForRestartRow, bool) {
	return requireAccessibleRow(
		svc, sender, terminalID, "terminal",
		svc.Queries.GetTerminalForRestart,
		func(t db.GetTerminalForRestartRow) string { return t.WorkspaceID },
	)
}

// requireAccessibleAgentID verifies the agent's workspace is accessible
// without loading the full row: GetAgentWorkspaceID fetches only the
// workspace_id column, skipping the options / option-group JSON blobs a
// full GetAgentByID deserializes. Both queries share the bare `id = ?`
// predicate, so the error mapping (empty id, missing row, db error,
// denial) is identical to requireAccessibleAgent — use that one instead
// when the handler body needs the row.
func (svc *Service) requireAccessibleAgentID(sender channel.ResponseWriter, agentID string) bool {
	_, ok := requireAccessibleRow(
		svc, sender, agentID, "agent",
		svc.Queries.GetAgentWorkspaceID,
		func(wsID string) string { return wsID },
	)
	return ok
}

// requireAccessibleTerminalID is the terminal mirror of
// requireAccessibleAgentID: a workspace_id-only lookup that skips the
// screen BLOB a full GetTerminal would read. Same predicate and error
// mapping as requireAccessibleTerminal.
func (svc *Service) requireAccessibleTerminalID(sender channel.ResponseWriter, terminalID string) bool {
	_, ok := requireAccessibleRow(
		svc, sender, terminalID, "terminal",
		svc.Queries.GetTerminalWorkspaceID,
		func(wsID string) string { return wsID },
	)
	return ok
}

// requireAccessibleRow factors the ACL + error-mapping shell shared by
// every "load a row by id, then check workspace access" helper. kind is
// the user-facing entity label embedded in error messages ("agent",
// "terminal"); fetch is the sqlc query; workspaceID extracts the row's
// workspace id for the access check.
func requireAccessibleRow[T any](
	svc *Service,
	sender channel.ResponseWriter,
	id, kind string,
	fetch func(context.Context, string) (T, error),
	workspaceID func(T) string,
) (T, bool) {
	var zero T
	if id == "" {
		sendInvalidArgument(sender, kind+"_id is required")
		return zero, false
	}
	row, err := fetch(bgCtx(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			sendNotFoundError(sender, kind+" not found")
			return zero, false
		}
		slog.Error("failed to load "+kind+" for access check", kind+"_id", id, "error", err)
		sendInternalError(sender, "failed to load "+kind)
		return zero, false
	}
	if !svc.requireAccessibleWorkspace(sender, workspaceID(row)) {
		return zero, false
	}
	return row, true
}

// sanitizeOptionalTitle normalizes an OpenAgent/OpenTerminal title. An empty
// title is allowed (falls back to "Agent N"/"Terminal N" assignments
// downstream); a non-empty title goes through SanitizeName, which caps
// length at 128 chars and strips control characters + the set known to
// cause trouble in downstream string templating.
func sanitizeOptionalTitle(title string) (string, error) {
	if title == "" {
		return "", nil
	}
	sanitized, err := validate.SanitizeName(title)
	if err != nil {
		return "", fmt.Errorf("invalid title: %w", err)
	}
	return sanitized, nil
}

// expandTilde expands a leading "~" or "~/" in a path to the user's home
// directory. Other forms (e.g. "~user/", "~~") are left unchanged.
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// bgCtx returns a background context for database operations.
func bgCtx() context.Context {
	return context.Background()
}
