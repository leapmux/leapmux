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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
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
	// `leapmux control` CLI. Nil disables remote control (env vars are
	// not injected and no socket is created).
	//
	// It is NOT part of Config: controlipc.Factory takes the Service as its
	// Authorizers, so it cannot be built until New has returned. Bootstrap
	// assigns it before RegisterAll, which keeps it inside this block's
	// contract.
	ControlIPC ControlIPCFactory

	startAgentFn func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error)
	// startBackgroundAgentFn is startAgentFn's sibling for a spawn nobody waits
	// on. The two are separate seams because they reach different manager entry
	// points, and only the background one takes a startup permit.
	startBackgroundAgentFn func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error)
	startTerminalFn        func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error
	createAgentRecordFn    func(context.Context, db.CreateAgentParams) error
	getAgentByIDFn         func(context.Context, string) (db.Agent, error)
	// batchGitStatusFn runs the concurrent git-status batch for a watch
	// catch-up. A seam (same contract as its siblings above) so tests can
	// observe the overlap's cancellation without spawning real git processes.
	batchGitStatusFn func(ctx context.Context, dirs []string) []*leapmuxv1.GitRepoStatus

	// ---- Mutable runtime state: everything that changes over the worker's
	// life, touched concurrently by the handler goroutines DispatchAsync
	// spawns. The fields mutated in place (registeredBy, Cleanup, the two
	// cleanup registries, the sync.Maps) each carry their own
	// synchronisation. The pointer fields are assigned once but own the
	// mutable state behind them -- the startup registries and PrivateEvents
	// guard theirs internally, and TabPayloads keeps its rows in the DB.
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
	// (TabRenamed, TabPayloadRegistered, TabPayloadRevoked). Always
	// non-nil after New.
	PrivateEvents *PrivateEventsBus

	// TabPayloads persists (user_id, tab_id) -> TabPayload for every
	// payload-backed tab kind (FILE, IMAGE). Always non-nil after New.
	// The hub never sees these rows; clients fetch payloads over E2EE.
	TabPayloads *TabPayloadStore

	// Cleanup tracks in-flight close handlers so Shutdown() can wait for
	// them to finish before DB/data-dir teardown. Close handlers must
	// Add(1) at entry and defer Done() so Wait() in Shutdown observes
	// them even if a handler panics.
	Cleanup sync.WaitGroup

	// tunnels is the worker-singleton tunnel manager built in RegisterAll. It
	// owns the half-closed idle reaper goroutine, which Shutdown stops for clean
	// teardown hygiene. registerAllClassified stops any prior manager before
	// reassigning this field, so a re-registration (production reconnect, or a
	// test that re-runs RegisterAll to inspect the gate map on a throwaway
	// dispatcher) cannot orphan the prior manager's reaper goroutine -- its Stop
	// would otherwise never be called, the exact leak the reaper exists to
	// prevent.
	//
	// It is an atomic.Pointer (not a plain field) for the same reason
	// registeredBy is: the connect loop / a reconnect can re-run RegisterAll on
	// one goroutine while a signal-driven Shutdown reads it on another, with no
	// lock ordering them in code. The bootstrap sequence makes that race latent
	// today, but a plain field would be a data race under -race and a torn/nil
	// read under a future reconnect-during-shutdown; atomic.Load/Store make the
	// access safe by construction rather than by convention. nil in tests that
	// never call RegisterAll; Shutdown's nil guard covers those.
	tunnels atomic.Pointer[tunnelManager]

	// shuttingDown is set as Shutdown's FIRST act and never cleared -- the
	// process is on its way out.
	//
	// Shutdown documents a precondition ("callers must have already stopped
	// dispatching new Open/Close requests") that nothing enforced: both entry
	// points deliberately keep the Hub stream live ACROSS Shutdown so its
	// broadcasts can leave, and the dispatcher has no gate. So an OpenTerminal
	// arriving during the drains spawned a PTY that installed after both
	// disconnect-notice sweeps -- an orphaned shell with no notice -- and did
	// `wg.Add(1)` on the startup WaitGroup that WaitForInFlight was already
	// inside, which is a documented sync.WaitGroup misuse rather than merely a
	// missed message. The tab-opening handlers refuse once it is set.
	shuttingDown atomic.Bool

	// stopBackgroundLoops stops the service's background loops -- the orphan
	// reconciler and the boot-time agent resume sweep -- and blocks until an
	// in-flight pass returns. Assigned by bootstrap after the loops start, read
	// by Shutdown. See SetStopBackgroundLoops for why it cannot be part of
	// Config.
	//
	// One field for both, composed at the call site: bootstrap starts the loops
	// and is therefore the only place that knows what order they must stop in.
	stopBackgroundLoops func()
	resumeSchedulerMu   sync.Mutex
	agentResumer        *AgentResumer

	// agentCleanups / terminalCleanups hold per-tab cleanup callbacks
	// registered by spawn*RemoteIPC and fired on close (or before a
	// restart mints a new token). Same shape, two embeddings keep the
	// terminal-vs-agent intent at the call site.
	agentCleanups    cleanupRegistry
	terminalCleanups cleanupRegistry

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
	// archiveTabLocks serializes one tab's whole archive lifecycle -- the flag
	// write AND the process teardown or resume that follows it -- keyed by tab.
	//
	// It must span both, not the write alone. The teardown reads no state after
	// the transaction commits, so an unarchive that committed in between would
	// have its freshly resumed process stopped by the earlier archive's still
	// running drain, its runtime state cleared, and an INACTIVE broadcast sent
	// for a tab whose row says active. The user sees an agent they just
	// unarchived go inactive again, with no error.
	//
	// Keyed by tab rather than one process-wide mutex, because the drain has no
	// upper time limit: StopAndWaitAgent and WaitForExit both wait for a
	// subprocess. A global lock made every archive on the worker -- and the
	// orphan reconciler's whole loop, which runs this teardown inline -- queue
	// behind one agent that ignores its stop signal. Different tabs never
	// contend. Entries are never deleted, bounded by the worker's distinct-tab
	// count over its lifetime, exactly like worktreeRemovalLocks above.
	archiveTabLocks sync.Map

	// gitIndexLocks serializes INDEX-MUTATING git commands per repository:
	// `worktree add`/`remove`, `checkout`, `checkout -b`, `branch -D`, `add -A`,
	// `commit`, `push`. Git guards its own index with `index.lock` and simply
	// FAILS when another process holds it -- "Unable to create
	// '.git/worktrees/<branch>/index.lock': File exists. Another git process
	// seems to be running in this repository" -- so two of the worker's own git
	// commands in one repo do not queue, they break the loser.
	//
	// That is reachable on ordinary paths, not just under test load: two agents
	// can be starting in the same repository at once (each running its git-mode
	// mutation on its own startup goroutine), and DeleteBranchDialog fires every
	// tab's REMOVE close concurrently.
	//
	// Keyed by MAIN REPO ROOT (gitPathInfo.RepoRoot), not by working dir: linked
	// worktrees share one object store and one `.git/worktrees/` admin tree, so a
	// checkout inside a worktree and a `worktree add` in the main repo contend on
	// the same locks. Keying by working dir would let exactly the observed failure
	// through. Entries are never deleted, bounded by the worker's distinct-repo
	// count over its lifetime.
	//
	// LOCK ORDER: worktreeRemovalLocks (outer) -> gitIndexLocks (inner).
	// closeTabCommon holds the per-worktree removal lock across a sequence that
	// ends in `git worktree remove`, so the inner acquisition happens there.
	// Nothing may take a removal lock while holding an index lock.
	gitIndexLocks sync.Map

	// terminalBellMu / lastBellAt coalesce TerminalBell broadcasts per terminal
	// (programs can emit BEL per keystroke).
	terminalBellMu sync.Mutex
	lastBellAt     map[string]time.Time
}

// cleanupRegistry holds id → cleanup callbacks under a single mutex.
// Used twice on Service (agentCleanups, terminalCleanups) so the two
// tab kinds keep distinct namespaces.
type cleanupRegistry struct {
	mu sync.Mutex
	m  map[string]func()
	// claimed holds ids whose row exists but whose cleanup has not been
	// registered yet -- the startup window between createAgentRecord / the
	// terminal upsert and spawnControlIPC's register call.
	claimed map[string]struct{}
	// closedWhileClaimed holds claimed ids that run() was called for before their
	// cleanup arrived. register() sees the mark and retires the new resource
	// immediately instead of storing a cleanup for a tab that is already closed.
	//
	// Without this pair, a reap landing in that window made run() a silent no-op
	// and the cleanup registered afterwards was never fired by anyone: the tab's
	// unix-socket listener stayed open and its delegation token unrevoked for the
	// life of the worker process. It is also what lets the orphan reconciler read
	// only OPEN rows -- it no longer needs to re-tear-down closed rows on every
	// pass just to cover this gap.
	closedWhileClaimed map[string]struct{}
}

// claim records that id's row exists and a cleanup for it is coming. Callers
// claim as soon as the row is durable, and must abandonClaim if they abort
// before registering.
func (r *cleanupRegistry) claim(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimLocked(id)
}

// claimLocked is claim for a caller that already holds r.mu, so the claimed
// map's lazy initialization has one definition. replace claims as part of a
// wider critical section and cannot call claim itself.
func (r *cleanupRegistry) claimLocked(id string) {
	if r.claimed == nil {
		r.claimed = map[string]struct{}{}
	}
	r.claimed[id] = struct{}{}
}

// abandonClaim drops a claim whose spawn aborted before registering, so a failed
// startup cannot leave an entry behind.
//
// It leaves closedWhileClaimed alone. That mark records a fact -- a close ran
// for this tab -- and abandoning a claim does not undo it. Erasing it lets a
// LATER spawn for the same tab store a cleanup that nothing will ever run: a
// relaunch whose factory degrades abandons its claim, and the relaunch after
// that one registers a live socket for a tab whose teardown already finished.
// register is the only consumer of the mark, and it deletes it when it acts on
// it, so the mark costs one empty map entry for a tab that closes and never
// registers again.
func (r *cleanupRegistry) abandonClaim(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.claimed, id)
}

// register stores the cleanup for id, and reports whether it stored it. A false
// return means register retired the resource on the spot because the tab closed
// while the spawn was still starting; the caller then owns nothing, and a
// deferred retire of its own would find no entry and do nothing.
func (r *cleanupRegistry) register(id string, fn func()) (stored bool) {
	r.mu.Lock()
	if _, closed := r.closedWhileClaimed[id]; closed {
		// The tab was closed while this spawn was still starting. Retire the
		// resource now rather than storing a cleanup nobody will ever run.
		delete(r.closedWhileClaimed, id)
		delete(r.claimed, id)
		r.mu.Unlock()
		if fn != nil {
			fn()
		}
		return false
	}
	delete(r.claimed, id)
	if r.m == nil {
		r.m = map[string]func(){}
	}
	r.m[id] = fn
	r.mu.Unlock()
	return true
}

// replace retires the cleanup currently registered for id and claims id for the
// incoming one, for a caller that is SWAPPING one live resource for another
// rather than tearing the tab down. It returns after the old cleanup runs.
//
// It is not run()+claim(), and the difference is the closedWhileClaimed mark.
// run() leaves that mark when it finds a claim and no cleanup -- the window in
// which a spawn's row exists but its cleanup has not landed yet -- and the mark
// means "the tab was closed". A re-mint is not a close, so re-adding a claim
// after run() left one would make the re-mint's OWN register retire the socket
// it just created, and the relaunched process would come up with no remote
// control. Consuming the claim silently is the honest reading: nobody closed
// anything, the earlier spawn's cleanup is simply obsolete.
//
// A mark that was ALREADY there is left alone: that one records a real close,
// and the caller's register must still honour it.
func (r *cleanupRegistry) replace(id string) {
	r.mu.Lock()
	fn := r.m[id]
	delete(r.m, id)
	// The claim is set, not deleted and re-added: a claim already here belongs to
	// a spawn whose cleanup did not land yet, and this one subsumes it.
	r.claimLocked(id)
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// closeTab retires the cleanup for a tab that is going away FOR EVER, and marks
// the id so nothing registers a fresh resource under it again.
//
// The mark is unconditional, which is the whole difference from retire. A close
// runs its teardown BEFORE it stamps closed_at, so between the two a relaunch
// reads an open row, passes every guard, mints a socket and registers it -- and
// nothing runs that cleanup again, because this close already fired. Marking on
// the way out is what makes register retire such a socket on the spot instead of
// storing it for a tab no client can see.
//
// register consumes the mark, so it costs one empty map entry for a tab that
// closes and never registers again. Tab ids are unique and never reused, so a
// leftover entry can only ever be read by a spawn for that same dead tab.
func (r *cleanupRegistry) closeTab(id string) {
	r.runAndMark(id, true)
}

// retire runs the cleanup for id WITHOUT marking the tab closed, for a caller
// rolling back its own startup rather than tearing the tab down.
//
// A failed startup is not a close: the tab stays open and the user can restart
// it. Marking here would tombstone a live tab, and the next restart's register
// would retire the socket it had just minted -- the process would come up with
// no remote control, which is the exact failure the mark exists to prevent,
// reintroduced by the mark itself.
func (r *cleanupRegistry) retire(id string) {
	r.runAndMark(id, false)
}

func (r *cleanupRegistry) runAndMark(id string, markClosed bool) {
	r.mu.Lock()
	fn, ok := r.m[id]
	delete(r.m, id)
	_, claimed := r.claimed[id]
	delete(r.claimed, id)
	// Mark when the tab is gone, or when a spawn claimed the id and its cleanup
	// is still on its way -- register acts on the mark in both cases.
	if markClosed || (!ok && claimed) {
		if r.closedWhileClaimed == nil {
			r.closedWhileClaimed = map[string]struct{}{}
		}
		r.closedWhileClaimed[id] = struct{}{}
	}
	r.mu.Unlock()
	if ok && fn != nil {
		fn()
	}
}

// spawnControlIPC mints LEAPMUX_CONTROL_* env vars and registers the
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
// as an unrelated "socket not configured" error from `leapmux control`
// with nothing naming the cause. The factory call is supplied as a closure so the
// generic helper doesn't have to know about the AgentSpawnInfo /
// TerminalSpawnInfo type split.
func (svc *Service) spawnControlIPC(
	kind, tabID, phase string,
	register func(string, func()) bool,
	call func() ([]string, func(), error),
) (envs []string, owned bool, err error) {
	if svc.ControlIPC == nil {
		return nil, false, nil
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
		// "socket not configured" error from `leapmux control` with nothing
		// naming the cause. Fail the spawn so it reports itself.
		if errors.Is(err, ErrMissingIdentity) {
			slog.Error("remote IPC spawn has no user identity; refusing to start "+kind, attrs...)
			return nil, false, err
		}
		slog.Warn("remote IPC factory failed; "+kind+" will start without remote control", attrs...)
		return nil, false, nil
	}
	// owned is what the registry STORED, not what the env vars imply. The two
	// come from different values, so a factory result with one and not the other
	// would leave a caller's deferred retire pointed at an entry that is not
	// there -- and register itself declines to store when the tab closed while
	// this spawn was starting, because it retires the resource on the spot.
	if cleanup != nil {
		owned = register(tabID, cleanup)
	}
	return envs, owned, nil
}

// agentStartupTimeout returns the configured agent startup timeout,
// or the default if not set.
func (svc *Service) agentStartupTimeout() time.Duration {
	if svc.AgentStartupTimeout > 0 {
		return svc.AgentStartupTimeout
	}
	return config.DefaultAgentStartupTimeout
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
	Channels *channel.Manager // E2EE channel manager (for session lookups)
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
	// MaxMessageSize is the worker's configured application payload budget
	// (0 = contracts.MaxMessageSize). Raises agent stdout scanner ceiling
	// and ReadFile's maxReadLimit; other stream sends still reject at
	// sendEncrypted when the reassembled frame exceeds the negotiated gate.
	MaxMessageSize int
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
		lastBellAt:      make(map[string]time.Time),
	}
	// The seed is config data, so it is minted here -- the one place the raw
	// string exists -- rather than inside the setter.
	if seed, ok := userid.New(cfg.SeedRegisteredBy); ok {
		svc.SetRegisteredBy(seed)
	}
	svc.TabPayloads = NewTabPayloadStore(svc.Queries, svc.PrivateEvents)
	svc.startAgentFn = svc.Agents.StartAgent
	svc.startBackgroundAgentFn = svc.Agents.StartBackgroundAgent
	svc.startTerminalFn = svc.Terminals.StartTerminal
	svc.createAgentRecordFn = svc.Queries.CreateAgent
	svc.getAgentByIDFn = svc.Queries.GetAgentByID
	svc.batchGitStatusFn = gitutil.BatchGetGitStatus

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

// startBackgroundAgent is startAgent for a spawn nobody is waiting on. It is the
// only path that draws on the manager's startup permit pool, so the boot-time
// resume sweep cannot start every restorable tab at once while a tab the user
// opens by hand waits behind it.
//
// It has its own test seam. Sharing startAgentFn would let a resume test stub
// the interactive entry point and then quietly exercise the wrong one, which is
// exactly the distinction this pair exists to make.
func (svc *Service) startBackgroundAgent(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
	if svc.startBackgroundAgentFn != nil {
		return svc.startBackgroundAgentFn(ctx, opts, sink)
	}
	return svc.Agents.StartBackgroundAgent(ctx, opts, sink)
}

// restartAgentLocked preserves Manager.RestartAgent's stop-before-start ordering
// while routing the new process through the service-level starter seam. The
// caller must already hold the per-agent lifecycle lock.
//
// The lock belongs to the caller rather than to this function because every
// relaunch has to do work of its own inside it: minting the relaunch's control
// socket retires the previous spawn's cleanup and registers a new one under the
// same tab id, and two relaunches for one tab that interleaved those two steps
// would leave one socket registered to nobody. LockAgent is not reentrant, so
// the mint and the restart cannot each take it.
func (svc *Service) restartAgentLocked(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
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

// clearAgentSessionID forgets the session an agent was in, so the next cold
// start does not ask its provider to resume a session no process holds.
//
// Every relaunch failure path calls it: a relaunch that could not start left
// the row pointing at a session the previous process owned, and the next
// message would relaunch with --resume against it. The write is best-effort --
// the relaunch already failed, and the caller is reporting that failure to the
// user -- so the error is logged rather than returned.
func (svc *Service) clearAgentSessionID(agentID string) {
	if err := svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
		AgentSessionID: "",
		ID:             agentID,
	}); err != nil {
		slog.Warn("failed to clear the agent session id after a failed relaunch",
			"agent_id", agentID, "error", err)
	}
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
	// Before anything else, mark every still-active background-task row
	// 'interrupted': the previous worker process was cut off (crash/restart),
	// so a row left 'pending'/'running' is genuinely not making progress. Pure
	// DB -- caches do not exist yet at boot. Logs the affected-row count so a
	// surprising number surfaces.
	// One boot-time instant for ended_at/updated_at so every swept row carries
	// the same stamp (the in-memory caches do not exist yet at boot, so there is
	// no cache/DB drift to guard here -- this is purely for uniform labeling).
	bootNow := nowMillis()
	// The sweep REPORTS the child transcripts it ended, so the divider list and
	// the write are one statement. Each transcript gets a closing divider below,
	// for the same reason the exit sweep writes one -- a subagent cut off by a
	// worker restart would otherwise show an 'interrupted' row in the sidebar
	// beside a transcript that just stops, and the tab would keep a thinking
	// indicator that never resolves. When the sweep fails nothing moved, so
	// nothing is owed a divider and a later boot finds the rows still active.
	endedChildIDs, err := svc.Queries.MarkAllActiveAgentBackgroundTasksInterrupted(bgCtx(), db.MarkAllActiveAgentBackgroundTasksInterruptedParams{
		EndedAt:   sqltime.SQLiteNullTimeOf(bootNow),
		UpdatedAt: sqltime.NewSQLiteTime(bootNow),
	})
	if err != nil {
		slog.Warn("mark active background tasks interrupted failed", "error", err)
	} else if len(endedChildIDs) > 0 {
		slog.Info("marked background tasks interrupted on boot", "count", len(endedChildIDs))
		svc.Output.WriteSubagentEndDividers(endedChildIDs, bgtask.StatusInterrupted)
	}
	svc.Output.restoreAutoContinueSchedules()
}

// HandleAgentProcessExit is the ExitHandler body: it clears the exited
// process's pending control requests (so request_ids bound to it don't reappear
// stale on resume) and gives its background-task registry a final status (stopped on an
// explicit Stop, interrupted on a crash). It fires for every exit including a
// relaunch's old-process stop; stopAndWait serializes this hook before a new
// process registers, so fresh spawns can never be clobbered.
func (svc *Service) HandleAgentProcessExit(agentID string, _ int, _ error, stopped bool) {
	svc.Output.ClearPendingControlRequests(agentID)
	svc.Output.MarkAgentBackgroundTasksExited(agentID, stopped)
}

// Shutdown persists in-memory final state to the database so it
// survives a worker restart. Call this before stopping the terminal
// manager (which clears in-memory state). Callers must have already
// stopped dispatching new OpenAgent/OpenTerminal/CloseAgent/CloseTerminal
// requests; otherwise the Wait calls below can race with a fresh Add.
func (svc *Service) Shutdown() {
	// Close the door before anything else, so the drains below are draining a
	// set that can no longer grow. See the field's comment for what got in
	// otherwise.
	svc.shuttingDown.Store(true)
	// Set the Output-level shutdown latch too: a shutdown-driven StopAll below
	// must leave background-task rows ACTIVE so the NEXT boot's RestoreState
	// sweep labels them 'interrupted' (a truthful "worker restart" label)
	// rather than MarkAgentBackgroundTasksExited stamping them 'stopped'.
	svc.Output.SetShuttingDown()

	// The user-visible goodbye goes FIRST, before anything that can block.
	//
	// This broadcast is the only chance a browser has to learn its terminal
	// died -- the process is about to exit, so there is no replay afterwards.
	// Everything below is about leaving the DB and filesystem consistent, and
	// the drains in particular can park for tens of seconds on an agent startup
	// that is still handshaking with its CLI. Long enough, under load, for the
	// Hub's idle timeout to declare this worker gone and tear down its channels
	// -- after which the notice is written to a stream nobody is relaying and
	// the terminal in the browser simply stops, with no error anywhere. Sending
	// first costs the drains nothing and is what makes the notice reliable.
	//
	// `notified` carries the ids the first pass stamped over to the second, so
	// "exactly once per terminal" is a property of the sweep rather than of
	// what the screen bytes happen to end with. See broadcastTerminalsDisconnected.
	notified := make(map[string]struct{})
	svc.broadcastTerminalsDisconnected(notified)

	// Stop the background loops FIRST, and before the drains below. There are two
	// of them: the orphan reconciler and the boot-time agent resume sweep.
	//
	// Both run work that svc.Cleanup does not cover, because only the
	// dispatcher's tracked handlers are registered with it. The reconciler runs
	// teardowns of its own (Close*TabForReconcile -> closeTabCommon), and the
	// resume sweep starts agent processes and registers a remote-IPC cleanup for
	// each one. The worker entry points run Shutdown BEFORE they cancel the
	// context those loops are bound to, so without this stop a nudge-triggered
	// pass -- or a sweep still working through its candidates -- runs DB writes
	// straight into the deferred sqlDB.Close().
	//
	// stop() blocks until both loops are quiet: the reconciler until its
	// in-flight pass returns, and the sweep until it joins the resumes it already
	// handed off. That second wait is not an added cost. Those resumes are
	// registered with svc.AgentStartup, so WaitForInFlight below waits for
	// exactly the same startups; joining here only moves the wait earlier, to the
	// point where the sweep can still be observed as running.
	if stop := svc.stopBackgroundLoops; stop != nil {
		stop()
	}

	// Drain any goroutines spawned by OpenAgent/OpenTerminal so their
	// trailing DB writes and filesystem work land before the caller
	// closes the DB or removes data directories.
	svc.AgentStartup.WaitForInFlight()
	svc.TerminalStartup.WaitForInFlight()
	// Also drain in-flight close handlers. The frontend fires close RPCs
	// as fire-and-forget, so a close can still be running on the
	// dispatcher goroutine while the worker receives a deregister/SIGTERM.
	svc.Cleanup.Wait()

	// Setsid (DetachFromTerminal, go-pty) takes agent CLIs out of the
	// worker's process group. Ctrl+C and SIGHUP therefore no longer
	// reap them. Stop them here, after startups have finished, while
	// Output.SetShuttingDown already latched so background-task rows
	// stay ACTIVE for the next boot.
	if svc.Agents != nil {
		svc.Agents.StopAll()
	}

	// Stop the tunnel idle reaper goroutine. The worker process is exiting, so
	// correctness does not depend on it, but stopping it keeps the goroutine
	// count tidy for an orderly shutdown and for tests that call Shutdown.
	if t := svc.tunnels.Load(); t != nil {
		t.Stop()
	}

	// Retire the private-event subscribers for the same reason. Each one parks
	// a goroutine on a background context (the handler deliberately passes
	// bgCtx, so a closing channel does not end the subscription), and nothing
	// else ever released them -- Stop existed but had no caller anywhere in the
	// repo's history.
	if svc.PrivateEvents != nil {
		svc.PrivateEvents.Stop()
	}

	// Re-run after the drains: a terminal whose startup was still in flight
	// above is only in the manager now, so the first pass could not have seen
	// it. Terminals the first pass already stamped are skipped by id.
	svc.broadcastTerminalsDisconnected(notified)

	// Cancel the background-task write context last, AFTER every drain. Any
	// in-flight bgtask write the drains did not cover now fails fast
	// (context.Canceled) instead of racing the caller's sqlDB.Close() and
	// leaving the in-memory cache disagreeing with the DB.
	svc.Output.CancelBackgroundCtx()
}

// broadcastTerminalsDisconnected appends the "[Worker disconnected]" notice to
// every live terminal's screen, which both persists it and pushes it to
// subscribers.
//
// `notified` is the set of terminal ids an earlier pass in the same shutdown
// already stamped; ids in it are skipped and ids stamped here are added. It
// exists because Shutdown sweeps twice and persistTerminalOnExit's own
// idempotency check is `bytes.HasSuffix(screen, terminalExitedNoticeSuffix)`,
// which only holds while the screen STOPS at the notice. Shutdown does not stop
// the PTYs, so a terminal running a build or a `tail -f` appends more output
// between the two passes -- across stopBackgroundLoops, two WaitForInFlight
// drains that can park for tens of seconds, Cleanup.Wait and the tunnel/private-
// event stops. The suffix check then fails and the user gets the notice twice
// with live output sandwiched between. Tracking ids makes the guard independent
// of what the terminal did in between.
func (svc *Service) broadcastTerminalsDisconnected(notified map[string]struct{}) {
	for _, tid := range svc.Terminals.ListTerminalIDs() {
		if _, done := notified[tid]; done {
			continue
		}
		// Already-exited terminals were persisted with their real exit
		// code by makeTerminalExitFn; don't clobber it with the shutdown
		// sentinel.
		if svc.Terminals.IsExited(tid) {
			continue
		}
		if svc.persistTerminalOnExit(tid, exitCodeUnknown) {
			notified[tid] = struct{}{}
		}
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
	// Re-bind the row's own closed_at, the way the title-update path does.
	// UpsertTerminal's DO UPDATE assigns `closed_at = excluded.closed_at`, so
	// leaving the field at its zero value writes NULL and RESURRECTS a tab the
	// user just closed: the shutdown sweep snapshots a terminal, a CloseTerminal
	// handler still on the dispatcher goroutine runs RemoveTerminal +
	// Queries.CloseTerminal, and this upsert then lands after it. The terminal
	// comes back as live on the next worker start and its screen blob falls out
	// of reach of DeleteClosedTerminalsBefore. A read error leaves the field
	// zero, which is the pre-close state and the same answer as before.
	var closedAt sqltime.SQLiteNullTime
	if row, err := svc.Queries.GetTerminalForReady(bgCtx(), tid); err == nil {
		closedAt = row.ClosedAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("failed to read terminal closed_at before persisting exit",
			"terminal_id", tid, "error", err)
	}
	// Shell column is INSERT-only (see UpsertTerminal SQL): UPDATE
	// preserves whatever was written at OpenTerminal time, so passing
	// the zero value here is a no-op on the existing row.
	params := db.UpsertTerminalParams{
		ID:            tid,
		WorkingDir:    src.WorkingDir,
		HomeDir:       svc.HomeDir,
		ShellStartDir: src.ShellStartDir,
		Title:         src.Title,
		Cols:          int64(src.Cols),
		Rows:          int64(src.Rows),
		Screen:        screen,
		ExitCode:      int64(exitCode),
		ClosedAt:      closedAt,
	}
	if err := svc.Queries.UpsertTerminal(bgCtx(), params); err != nil {
		slog.Error("failed to save terminal on exit", "terminal_id", tid, "error", err)
		return false
	}
	return true
}

// ownerOnlyRegistrar registers handlers that ONLY the worker's registered owner
// may call -- which, since the Worker stopped tracking workspaces, is every
// handler except Ping.
//
// It exists so a family cannot register an ungated handler by accident: it is
// handed this instead of the raw *channel.Dispatcher, so the gate is a property
// of where the handler is registered rather than a line each author must
// remember. Both Register and RegisterTracked are wrapped -- gating only one
// would leave a silent hole (git uses both). Each Register also records
// gateOwnerOnly on the shared registrar so TestEveryRegisteredMethodIsClassified
// sees the method without replaying the family register functions.
type ownerOnlyRegistrar struct {
	r registrar
}

func (o ownerOnlyRegistrar) gate(handler channel.HandlerFunc) channel.HandlerFunc {
	return func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		if !requireWorkerOwner(o.r.svc, caller.UserID, sender) {
			return
		}
		handler(ctx, caller, req, sender)
	}
}

func (o ownerOnlyRegistrar) Register(method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	o.r.register(method, gateOwnerOnly, scope, dispatchPlain, o.gate(handler))
}

// RegisterMode is Register/RegisterTracked selected by an argument, so a gated
// helper can take the dispatch axis as a parameter instead of existing twice.
// Streaming is deliberately NOT reachable here: RegisterStream applies gateStream
// (a stream-frame denial) rather than gate, and the unary-vs-stream shape is what
// the methodShape table pins -- routing it through the same argument would let a
// caller pick a unary denial for a streaming method.
func (o ownerOnlyRegistrar) RegisterMode(method string, scope leapmuxv1.Scope, mode dispatchMode, handler channel.HandlerFunc) {
	switch mode {
	case dispatchTracked:
		o.RegisterTracked(method, scope, handler)
	case dispatchPlain, dispatchStreaming:
		// dispatchStreaming cannot arrive: the streaming helpers call
		// RegisterStream directly. Treating it as plain here would silently give a
		// streaming method a unary denial, so it is grouped with plain only to keep
		// the switch exhaustive.
		o.Register(method, scope, handler)
	}
}

func (o ownerOnlyRegistrar) RegisterTracked(method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	o.r.register(method, gateOwnerOnly, scope, dispatchTracked, o.gate(handler))
}

// gateStream is gate for a STREAMING method: same predicate, different denial
// encoding (a stream frame, so the receiver has an End to terminate on -- see
// sendStreamError).
func (o ownerOnlyRegistrar) gateStream(handler channel.HandlerFunc) channel.HandlerFunc {
	return func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		if !requireWorkerOwnerStream(o.r.svc, caller.UserID, sender) {
			return
		}
		handler(ctx, caller, req, sender)
	}
}

// RegisterStream is the streaming lane's entry point, and it exists so the
// streaming helpers cannot record gateOwnerOnly without also applying it.
//
// The two stream helpers used to call r.register directly and re-implement the
// gate inline, which made "recorded owner-gated but never gated" a one-line
// mistake nothing would catch. Routing them through here leaves gateOwnerOnly
// mentioned in exactly three places, all inside this type.
func (o ownerOnlyRegistrar) RegisterStream(method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	o.r.register(method, gateOwnerOnly, scope, dispatchStreaming, o.gateStream(handler))
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

// SetStopBackgroundLoops registers a function that stops the service's background
// loops and blocks until an in-flight pass returns. Shutdown calls it before any
// drain. One function for every loop: the caller starts them and is the only
// place that knows what order they must stop in.
//
// Wired late, like Service.RemoteIPC: the loops need a fully-built Service, so
// they cannot exist when New runs. Left unset -- every test that never starts
// them -- Shutdown skips it.
func (svc *Service) SetStopBackgroundLoops(stop func()) {
	svc.stopBackgroundLoops = stop
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
// It is the ONLY authorization axis the Worker has, and that is by construction
// rather than by simplification: a Worker is registered by exactly one user
// (workers.registered_by), it stores no workspace id, and the Hub opens a
// channel only for that user -- so "the caller owns this Worker" and "the caller
// owns every row this Worker holds" are the same statement. There is nothing
// finer to narrow on.
//
// It matters most for the families whose reach is the MACHINE -- tunnels
// (arbitrary TCP out of the host), the file and git handlers (any absolute path;
// validate.SanitizePath normalizes and blocks traversal, it does not confine to
// a root), and sysinfo (which discloses the owner's home path). The owner
// already has all of this: their agents run as them on their own machine, so
// granting it over the channel adds nothing. Anyone ELSE holding a channel must
// not have it. Since the Hub only ever opens a channel for the worker's own
// owner, the gate is defence in depth today; it is what keeps any future way of
// reaching a Worker failing closed rather than silently exposing the filesystem.
//
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
	if callerIsWorkerOwner(svc, userID) {
		return true
	}
	sendPermissionDenied(sender, workerOwnerDenial)
	return false
}

// callerIsWorkerOwner is the ownership predicate both gates share, and
// workerOwnerDenial the message both send. Extracted because the unary and
// streaming gates are otherwise identical and differ only in how they encode the
// refusal: with the check written out twice, a change to the predicate could
// land in one and miss the other, and no test asserts either message string.
func callerIsWorkerOwner(svc *Service, userID userid.UserID) bool {
	return userID.MatchesUser(svc.RegisteredBy())
}

// workerOwnerDenial is quoted in crossworker/client.go's comments, so keep the
// wording and this constant in step.
const workerOwnerDenial = "only the worker owner may use this"

// requireWorkerOwnerStream is requireWorkerOwner for a STREAMING method: the
// refusal goes out as a stream frame.
//
// A unary InnerRpcResponse on a correlation id the browser holds in
// streamListeners is dropped on arrival, so a unary denial here would leave the
// caller subscribed to a stream that never carries anything and never errors --
// the exact failure the methodShape table exists to catch, in its
// security-relevant half.
func requireWorkerOwnerStream(svc *Service, userID userid.UserID, sender channel.ResponseWriter) bool {
	if callerIsWorkerOwner(svc, userID) {
		return true
	}
	sendStreamError(sender, codes.PermissionDenied, workerOwnerDenial)
	return false
}

// RegisterAll registers all service handlers with the dispatcher.
//
// Every method records a methodGate at registration time (default-deny: a
// method with no recorded gate fails TestEveryRegisteredMethodIsClassified):
//
//   - gateOwnerOnly — everything, via ownerOnlyRegistrar directly (the
//     machine-scoped file/git/sysinfo/tunnel families) or via the typed
//     registerOwnerGated / registerAgentGated / registerTerminalGated helpers
//     that layer an unmarshal and a row load on top of the same gate. The
//     ByID variants resolve the tab through an id-only existence probe for
//     handlers that never read the row.
//   - gateNone      — Ping; a liveness probe that does no work and discloses
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
	gates, _, _ := registerAllClassified(d, svc)
	return gates
}

// registerAllClassified is registerAllWithGates plus the reply-shape and
// scope maps, so a test can assert that every classification came from the
// one function production runs -- including that each method's declared scope
// sits inside the delegation grant a sibling-worker caller carries.
func registerAllClassified(d *channel.Dispatcher, svc *Service) (map[string]methodGate, map[string]methodShape, map[string]leapmuxv1.Scope) {
	r := newRegistrar(d, svc)
	registerPingHandler(r, svc)
	// Machine-scoped: owner-only by construction (see ownerOnlyRegistrar).
	ownerOnly := r.ownerOnly()
	registerFileHandlers(ownerOnly, svc)
	registerGitHandlers(ownerOnly, svc)
	registerTerminalHandlers(r, svc)
	registerAgentHandlers(r, svc)
	registerAgentSessionHandlers(r, svc)
	registerCleanupHandlers(r, svc)
	registerSysInfoHandlers(ownerOnly, svc)
	tunnels := registerTunnelHandlers(ownerOnly)
	// Swap in the new manager and Stop the prior one it displaced. A
	// re-registration (a future reconnect flow, or a test re-running RegisterAll
	// on a throwaway dispatcher) would otherwise orphan the prior manager's
	// reaper goroutine -- its Stop would never be called, the exact leak the
	// reaper exists to prevent. Stopping the displaced manager makes "no orphaned
	// sweeper" an invariant rather than a convention. Stop is a no-op on a manager
	// whose sweeper never started (the common case for re-registration before any
	// half-close). The Swap is atomic so a concurrent Shutdown.Load sees either
	// the prior or the new manager, never a torn pointer -- the same reason
	// registeredBy is an atomic.Pointer.
	if prior := svc.tunnels.Swap(tunnels); prior != nil {
		prior.Stop()
	}
	return r.gates, r.shapes, r.scopes
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
// The sender helpers above emit an InnerRpcResponse, which the frontend routes
// through pendingRequests. A streaming call's correlation id lives in
// streamListeners instead. That frame is not lost -- deliverResponse has an arm
// that hands a unary ERROR on a stream id to the listener's onError -- but it
// carries no End flag, so a receiver cannot terminate on it generically, and the
// sibling non-error case IS dropped. Streaming handlers therefore report failure
// in-band, as an InnerStreamMessage with IsError, so one reply shape covers every
// outcome on the stream.
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

// refuseIfShuttingDown rejects a tab-opening request once Shutdown has begun,
// and reports whether it did. The three handlers that call `begin` on a startup
// registry use it: an explicit error is a better answer than a tab whose
// process is about to be killed unannounced, and it is what makes Shutdown's
// documented "no new requests" precondition true rather than merely asserted.
func (svc *Service) refuseIfShuttingDown(sender channel.ResponseWriter) bool {
	if !svc.shuttingDown.Load() {
		return false
	}
	sendFailedPrecondition(sender, "worker is shutting down")
	return true
}

// sendFailedPrecondition sends a FailedPrecondition error response.
// Used when the request is valid but the target is not in a state that
// permits the operation (e.g. sending a message to an agent that is
// still starting up).
func sendFailedPrecondition(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.FailedPrecondition), msg)
}

// sendUnavailable signals a transient failure (the caller should retry). The
// frontend's STARTING-state queue re-queues messages that receive this code.
func sendUnavailable(sender channel.ResponseWriter, msg string) {
	_ = sender.SendError(int32(codes.Unavailable), msg)
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

// normalizeWorkingDir resolves the value stored in a tab's working_dir column,
// for all three tab types. `raw` is whatever the client sent; `fallback` is the
// dir to use when it sent nothing (the home dir for agents and terminals, the
// file's own directory for file tabs).
//
// The absolute-path requirement is the point of sharing this. A working dir is
// the one field every branch-context operation hands to `git -C` --
// getTabWorkingDir, linkTabToWorktree, the branch-sibling scan, PushBranch
// -- and `git -C` resolves a relative path against the WORKER PROCESS's cwd,
// which is never the client's. A stored `.` or `../peer-repo` therefore does not
// fail loudly; it silently answers for whatever repository the worker happens to
// sit in, so a close inspection reports the wrong branch's dirty state and
// `--worktree=push` can commit and push a repository the user never named.
// Refusing at the write point is the same call TabPayloadStore.Register already
// makes for file_path, applied to the other half of the same lookup.
//
// It runs validate.SanitizePath, which is the SAME normalizer every reader of
// this column applies to the value it looks the column up by. Sharing it is
// what makes the two agree, and the agreement is load-bearing: SanitizePath
// cleans, so a write that stored `/repo/` while a read asked for `/repo` matched
// nothing. For ListAgentSessions that silence is not merely a missing row -- the
// worker's rows are also the set of handles a live process holds, so an empty
// answer let the provider's own store offer a session that was already open,
// and two processes against one session store corrupt it.
//
// SanitizePath also refuses `..`, which this function used to accept whenever
// the path was otherwise absolute. That is the same hazard the paragraph above
// describes, one level down: `/repo/../other` is absolute, so it passed, and it
// answers for a repository the user never named.
// The error names the FIELD, because file_path's own guard refuses with the
// same "must be absolute" tail: a caller that reads only the tail cannot tell
// which half of a file-tab registration it broke.
func normalizeWorkingDir(raw, fallback, homeDir string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		dir = fallback
	}
	clean, err := validate.SanitizePath(dir, homeDir)
	if err != nil {
		if errors.Is(err, validate.ErrNotAbsolute) {
			return "", fmt.Errorf("working_dir must be absolute, got %q: %w", dir, err)
		}
		return "", fmt.Errorf("working_dir is not a usable path, got %q: %w", dir, err)
	}
	return clean, nil
}

// expandTilde expands a leading `~` in a path against the PROCESS's home
// directory. Other forms (`~user/`, `~~`) are left unchanged.
//
// The rule lives in pathutil; this wrapper supplies only the home. Its one
// caller is the terminal's shell start directory, which a client may send in
// the `~/proj` form the shell itself would accept.
func expandTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return pathutil.ExpandHome(path, home)
}

// bgCtx returns a background context for database operations.
func bgCtx() context.Context {
	return context.Background()
}
