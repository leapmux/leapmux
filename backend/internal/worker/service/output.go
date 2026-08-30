// Package service output provides agent output persistence and broadcasting.
// It implements the agent.OutputSink interface, backing the generic primitives
// with DB queries, notification threading, and WatcherManager fan-out.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
)

// The span engine lives in its own package (see spantrack) so the agent
// package's test double can delegate to the real thing instead of
// re-implementing it. These aliases keep this package's API unchanged for its
// callers and its tests.
type (
	SpanTracker  = spantrack.SpanTracker
	SpanLine     = spantrack.SpanLine
	SpanLineType = spantrack.SpanLineType
	ActiveSpan   = spantrack.ActiveSpan
)

const (
	SpanLineActive       = spantrack.SpanLineActive
	SpanLineConnector    = spantrack.SpanLineConnector
	SpanLineConnectorEnd = spantrack.SpanLineConnectorEnd
	SpanLinePassthrough  = spantrack.SpanLinePassthrough
)

// resolveConnectorSpanID determines which span a message should visually
// connect to. For span closers (tool_result), the span is already open so
// we connect to it directly. For span openers (tool_use) and other messages,
// the span isn't open yet so we connect to the parent span instead.
func resolveConnectorSpanID(spanID, connectorSpanID, parentSpanID string, closing bool) string {
	if connectorSpanID != "" {
		return connectorSpanID
	}
	// For span closers (tool_result), the span is already open.
	if closing && spanID != "" {
		return spanID
	}
	// For span openers (tool_use) and other messages, connect to the parent.
	if parentSpanID != "" {
		return parentSpanID
	}
	return spanID
}

// --- Notification threading ---

// notifThreadRef tracks the current notification thread for an agent.
// `source` lets the cross-source guard short-circuit before the DB read —
// adjacent USER↔AGENT↔LEAPMUX flips just open a new thread and never
// touch sqlite or zstd.
type notifThreadRef struct {
	msgID  string
	seq    int64
	source leapmuxv1.MessageSource
}

// notifThreadWrapperType is the constant value of the wrapper's `type`
// discriminator. The frontend's content-shape probe keys on this string
// alone, so it must never collide with any inner-envelope `type` value
// produced by an agent provider.
const notifThreadWrapperType = contracts.NotificationThreadWrapperType

// notifThreadWrapper is the content envelope stored in the DB for notification
// thread messages. It consolidates multiple notifications into a single DB row.
// The Type field is an explicit discriminator so consumers can identify the
// wrapper from content shape alone, decoupled from the persisted source.
type notifThreadWrapper struct {
	Type     string            `json:"type"`
	OldSeqs  []int64           `json:"old_seqs,omitempty"`
	Messages []json.RawMessage `json:"messages"`
}

// wrapNotifContent wraps a single raw notification JSON into a notifThreadWrapper.
func wrapNotifContent(rawJSON []byte) []byte {
	w := notifThreadWrapper{
		Type:     notifThreadWrapperType,
		Messages: []json.RawMessage{rawJSON},
	}
	data, err := json.Marshal(w)
	if err != nil {
		slog.Warn("marshal notification wrapper", "error", err)
		return []byte(`{"type":"` + notifThreadWrapperType + `","messages":[]}`)
	}
	return data
}

// unwrapNotifContent parses a notifThreadWrapper from content bytes.
func unwrapNotifContent(data []byte) (*notifThreadWrapper, error) {
	var w notifThreadWrapper
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// --- OutputHandler ---

// agentTodoCache mirrors an agent's agent_todos rows in memory so the
// worker can build the post-mutation broadcast without re-fetching after each
// event, and pick the next seq for an INSERT without a pre-SELECT round-trip.
// Initialized lazily from the DB on first touch per agent; the cache and DB
// stay in lock-step because every successful write goes through applyTodoEvent.
//
// It is an alias for the shared registryCache mechanics (seed/snapshot/index/
// evict), with the type-specific behaviour supplied by todoOps. The todo-
// specific dual-key lookups (indexByID by task id, itemsEqual for snapshot
// no-op detection, todoItems projection) live as free functions below; the
// cache's own mutex (registryCache.Mu) serializes the multi-step
// "check existence -> DB write -> in-place mutation" sequences and gates the
// lazy DB seed.
type agentTodoCache = registryCache[cachedTodo]

// cachedTodo pairs an Item with the agent_todos `row_key` it persists
// under. The row_key is the Item's ID when set (incremental Task*
// path) and a synthetic `snap-N` otherwise (snapshot path for
// providers without stable per-task ids — TodoWrite / Codex plan /
// ACP plan). Eviction and per-row updates address rows by row_key.
type cachedTodo struct {
	item   todoevents.Item
	rowKey string
}

// OutputHandler manages agent output persistence and broadcasting.
// It holds shared state accessed by per-agent OutputSink instances.
type OutputHandler struct {
	queries *db.Queries
	// db backs the agent_todos snapshot transaction (delete-all + N
	// inserts). Tests that don't exercise the snapshot path may pass
	// nil; the snapshot writer then falls back to a non-transactional
	// loop.
	db      *sql.DB
	watcher *WatcherManager
	agents  *agent.Manager
	DataDir string

	// Per-agent notification threading state (concurrent access).
	notifMu         sync.Map // agentID -> *sync.Mutex
	lastNotifThread sync.Map // agentID -> *notifThreadRef

	// Per-agent span tracking. The registry records whether each tracker
	// belongs to a root main agent or a virtual child, so cleanup and the
	// orphan sweep can reason about each population explicitly. See
	// spanTrackerRegistry for the lifecycle contract.
	trackers *spanTrackerRegistry

	// Per-agent in-memory to-do mirror. Keyed by agent_id; each
	// agentTodoCache carries its own mutex for the multi-step event
	// transitions, matching the sync.Map pattern used by the other
	// per-agent state above.
	todos sync.Map // agentID -> *agentTodoCache

	// Per-ROOT-agent in-memory background-task registry mirror. Keyed by the
	// ROOT owner agent id; each bgTaskCache carries its own mutex. Rows for
	// descendants at any depth live under the root, so a child sink writes
	// through to its root's cache. Mirrors `todos`.
	bgtasks sync.Map // rootAgentID -> *bgTaskCache

	// rootSinks tracks the root agentOutputSink per root agent id so CleanupAgent
	// can prune a closed child from its parent's childSinks cache (which would
	// otherwise retain the child's SpanTracker + OutputHandler ref for the
	// parent's lifetime). Populated by NewSink; entry per root.
	rootSinks sync.Map // rootAgentID -> *agentOutputSink

	// shuttingDown is set by Service.Shutdown so a shutdown-driven StopAll
	// leaves background-task rows active (the next boot marks them
	// 'interrupted' -- a truthful "worker restart" label -- rather than
	// 'stopped'). MarkAgentBackgroundTasksExited observes it.
	shuttingDown atomic.Bool

	// shutdownCtx is cancelled (via shutdownCancel) AFTER the Shutdown drains
	// finish, right before the caller closes the DB. The bgtask write paths use
	// it instead of a bare context.Background() so an in-flight write that the
	// drains did not cover fails fast (context.Canceled) instead of racing
	// sqlDB.Close() and leaving the in-memory cache disagreeing with the DB.
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// Plan mode tool_use tracking (shared across agents).
	planModeToolUse sync.Map // tool_use_id -> target mode string ("plan" or "default")

	// Auto-continue timers keyed by agent_id + reason.
	autoContinue sync.Map // scheduleKey -> *autoContinueTimerState

	// sendMessageFunc is called by auto-continue to inject a synthetic
	// user message. Set via SetSendMessageFunc in service.New.
	sendMessageFunc func(agentID, content string)

	// agentStarting reports whether the agent is still in its startup window
	// (registered in the AgentStartup registry). Set via SetAgentStartingFunc
	// in service.New; nil in tests that build an OutputHandler directly, where
	// no startup is in progress.
	// PersistSettingsRefresh consults it to avoid clobbering a settings change
	// that landed mid-startup with the agent's confirmed launch settings.
	agentStarting func(agentID string) bool

	// wakeLock prevents system sleep while there is agent/terminal activity.
	wakeLock *wakelock.ActivityTracker

	now func() time.Time
}

// NewOutputHandler creates a new OutputHandler. sqlDB is used for the
// agent_todos snapshot transaction; tests that never trigger a
// snapshot may pass nil.
func NewOutputHandler(sqlDB *sql.DB, queries *db.Queries, watcher *WatcherManager, agents *agent.Manager, wl *wakelock.ActivityTracker) *OutputHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &OutputHandler{
		queries:        queries,
		db:             sqlDB,
		watcher:        watcher,
		agents:         agents,
		wakeLock:       wl,
		now:            time.Now,
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
		trackers:       newSpanTrackerRegistry(),
	}
}

// CancelBackgroundCtx cancels the shutdown context used by the background-task
// write paths. Service.Shutdown calls it AFTER the in-flight drains finish, so
// any write the drains did not cover fails fast instead of racing sqlDB.Close().
func (h *OutputHandler) CancelBackgroundCtx() {
	if h.shutdownCancel != nil {
		h.shutdownCancel()
	}
}

// bgTaskCtx returns the shutdown-aware context for background-task DB work.
// Falls back to context.Background() when no shutdown context is wired (a
// hand-built OutputHandler in a test that never shuts down).
func (h *OutputHandler) bgTaskCtx() context.Context {
	if h.shutdownCtx != nil {
		return h.shutdownCtx
	}
	return context.Background()
}

// ResetSpanTracker resets the span tracker for the given agent, clearing all
// active spans. Used when the agent's context is cleared or plan execution restarts.
func (h *OutputHandler) ResetSpanTracker(agentID string) {
	h.trackers.reset(agentID)
}

// SetSendMessageFunc sets the callback used by auto-continue to inject
// a synthetic user message into an agent. Must be called before any
// agent output is processed.
func (h *OutputHandler) SetSendMessageFunc(fn func(agentID, content string)) {
	h.sendMessageFunc = fn
}

// SetAgentStartingFunc wires the predicate PersistSettingsRefresh uses to detect
// the startup window (see the agentStarting field). Call before any agent output
// is processed.
func (h *OutputHandler) SetAgentStartingFunc(fn func(agentID string) bool) {
	h.agentStarting = fn
}

// CleanupAgent removes all per-agent state from the handler's maps.
// Call this when an agent is permanently closed.
func (h *OutputHandler) CleanupAgent(agentID string) {
	h.notifMu.Delete(agentID)
	h.lastNotifThread.Delete(agentID)
	h.trackers.delete(agentID)
	h.todos.Delete(agentID)
	// bgtasks is keyed by ROOT owner id; a child id keys no entry here (its
	// rows live under the root), so this delete is a safe no-op for children.
	// A root close reaps the root's own registry cache.
	h.bgtasks.Delete(agentID)
	// Prune the child-sink cache so a closed child's SpanTracker + sink ref are
	// not retained for the parent's lifetime. A root id is its own root: clear
	// its whole childSinks map (every child dies with the root). A child id is
	// pruned from whichever root sink cached it.
	if root, ok := h.rootSinks.Load(agentID); ok {
		rootSink := root.(*agentOutputSink)
		rootSink.childMu.Lock()
		rootSink.childSinks = nil
		rootSink.childMu.Unlock()
		h.rootSinks.Delete(agentID)
	} else {
		h.rootSinks.Range(func(_, v any) bool {
			rootSink := v.(*agentOutputSink)
			rootSink.childMu.Lock()
			if rootSink.childSinks != nil {
				delete(rootSink.childSinks, agentID)
			}
			rootSink.childMu.Unlock()
			return true
		})
	}
	h.cleanupAutoContinue(agentID)
	// The control-response answer claims are DURABLE rows (control_response_answers), not in-memory
	// state, so there is nothing to reclaim here -- a reused request_id is deduped per INSTANCE by its
	// claim_token (no release needed) and rows are cleaned up in bulk with the agent via ON DELETE CASCADE.
}

// CleanupChildAgents prunes per-child state for a set of descendants in one
// pass when a root closes, instead of calling CleanupAgent per child. Each
// per-child CleanupAgent call would otherwise do a full rootSinks.Range scan to
// find the child's cached sink (O(children x roots) on a root close). Here the
// root is known, so its whole childSinks map clears once and only the cheap
// per-agent maps (trackers/todos/etc.) delete per child.
func (h *OutputHandler) CleanupChildAgents(childIDs []string) {
	for _, childID := range childIDs {
		h.cleanupChildMaps(childID)
	}
}

// cleanupChildMaps deletes the cheap per-agent maps for one child. Shared by
// CleanupChildAgents (root close, many children) and CleanupChildAgent (a single
// child's closing update driven from a provider's OutputSink). Does NOT touch
// rootSinks/bgtasks: those are keyed by root owner and are reaped on root close.
func (h *OutputHandler) cleanupChildMaps(childID string) {
	h.notifMu.Delete(childID)
	h.lastNotifThread.Delete(childID)
	h.trackers.delete(childID)
	h.todos.Delete(childID)
	h.cleanupAutoContinue(childID)
}

// ForgetChildSinks clears a root's entire cached childSinks map. Called on
// root teardown alongside CleanupChildAgents so no closed child sink outlives
// the root. No-op when the root has no cached sink.
func (h *OutputHandler) ForgetChildSinks(rootID string) {
	if root, ok := h.rootSinks.Load(rootID); ok {
		rootSink := root.(*agentOutputSink)
		rootSink.childMu.Lock()
		rootSink.childSinks = nil
		rootSink.childMu.Unlock()
	}
}

// claimControlResponseAnswer atomically records that (agentID, requestID, claimToken)'s answer is being
// persisted and reports whether THIS call is the first to claim it. A later duplicate answer for the
// same request INSTANCE -- an RPC retry, or a second window answering before it received the cancel
// broadcast, BOTH echoing the same claim_token -- gets false, so the caller skips persisting a second
// answer row and its scroll-rail dot. Handlers run concurrently (DispatchAsync, no per-agent lock), so
// the claim -- a single INSERT serialized by the (agent_id, request_id, claim_token) primary key -- is
// also what decides who deletes the pending request and applies the once-only plan-mode effects.
//
// claimToken (minted per PersistControlRequest, echoed by the frontend from the AgentControlRequest it
// answers) is what makes the dedup INSTANCE-scoped: a REUSED request_id -- a Codex/ACP JSON-RPC counter
// that reset across a plan-exec restart, or a Claude follow-up -- carries a FRESH token per instance, so
// the new instance's genuine answer claims a distinct key while a stale duplicate of the PRIOR instance
// (old token) still loses. No release-on-reissue is needed. An empty claimToken (a pre-token answer, or a
// frontend lookup miss) degrades to request_id-only dedup for that answer.
//
// The claim is a DURABLE row (control_response_answers) cleaned up in bulk with its agent via ON DELETE
// CASCADE. Because nothing else clears it, it survives BOTH a subprocess restart AND a worker-PROCESS
// restart, so a duplicate straddling either is still deduped instead of re-persisting (#258) -- no
// in-memory tracker to lose. On a query error it fails OPEN (returns true): dropping the user's answer
// on a transient DB error is the worse outcome. The fail-open cost is NOT merely a duplicate row -- a
// fail-open winner runs the FULL winner path (persist AND re-forward / re-restart). It bites when a
// genuine DUPLICATE's claim query errors (the first answer already claimed cleanly, so only the
// duplicate's INSERT need fail): fail-open then treats that duplicate as a fresh winner. That is rare by
// construction -- SQLite writes are serialized under a 60s busy_timeout (see sqlitedb.Open), so a claim
// error means a genuine DB failure, not routine write contention -- and the lesser evil accepts it.
func (h *OutputHandler) claimControlResponseAnswer(agentID, requestID, claimToken string) bool {
	rows, err := h.queries.ClaimControlResponseAnswer(bgCtx(), db.ClaimControlResponseAnswerParams{
		AgentID:    agentID,
		RequestID:  requestID,
		ClaimToken: claimToken,
	})
	if err != nil {
		slog.Warn("claim control response answer", "agent_id", agentID, "request_id", requestID, "error", err)
		return true
	}
	return rows > 0
}

// TrackedAgentIDs returns the set of agent ids that currently hold any in-memory
// per-agent tracker state (notification thread, todos, span hierarchy). The periodic
// orphan sweep uses it to find state that outlived its agent -- e.g. a subprocess that
// crashed and was later closed without routing through ClearAgentRuntimeState (the
// per-exit handler keeps this state for a possible relaunch, so it isn't cleared there).
func (h *OutputHandler) TrackedAgentIDs() []string {
	seen := make(map[string]struct{})
	// rootSinks is keyed by ROOT owner id (each entry caches that root's child
	// sinks). Include it so the orphan sweep can reclaim a root sink whose
	// agent was closed through a path that bypassed ClearAgentRuntimeState --
	// otherwise the root sink (and every cached child sink) leaks for the
	// worker's lifetime.
	for _, m := range []*sync.Map{&h.notifMu, &h.lastNotifThread, &h.todos, &h.bgtasks, &h.rootSinks} {
		m.Range(func(key, _ any) bool {
			if id, ok := key.(string); ok {
				seen[id] = struct{}{}
			}
			return true
		})
	}
	// The span-tracker registry holds root and child trackers in one typed
	// container; fold both populations into the candidate set.
	h.trackers.rangeAll(func(id string) bool {
		seen[id] = struct{}{}
		return true
	})
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// broadcastControlCancel fans out a single AgentControlCancelRequest to
// any registered watchers. Shared by ClearAgentRuntimeState and the
// per-agent sink so the envelope shape lives in one place.
func (h *OutputHandler) broadcastControlCancel(agentID, requestID string) {
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{
			ControlCancel: &leapmuxv1.AgentControlCancelRequest{
				AgentId:   agentID,
				RequestId: requestID,
			},
		},
	})
}

// ClearPendingControlRequests drops the agent's pending control_requests from the
// DB and broadcasts a controlCancel for each, so request_ids bound to a now-exited
// subprocess don't reappear stale on resume. It deletes by agent id ALONE (no per-process
// generation column), so it must run only when no LATER process for the same agent id could
// own pending requests. The worker wires it as the per-exit onExit handler, where that holds:
// agent.Manager.stopAndWait blocks every relaunch until the OLD process's exit goroutine
// (this handler included) has fully finished BEFORE registering the new provider, so a
// relaunch's old-process onExit can only ever clear requests that genuinely belong to the
// process that just went away -- never the freshly-restarted one's. It deliberately does NOT
// touch the TIMELINE per-agent trackers (CleanupAgent): the open notification thread, the to-do
// list, and the span hierarchy must SURVIVE a relaunch, or two settings-change notifications
// bracketing a model/effort switch land in separate threads and can no longer consolidate/cancel.
// The durable control-response answer claims are likewise untouched here -- a duplicate straddling the
// restart stays deduped because its claim survives, and a genuinely reissued request_id is disambiguated
// per instance by its fresh claim_token rather than by clearing the prior claim.
func (h *OutputHandler) ClearPendingControlRequests(agentID string) {
	deletedIDs, err := h.queries.DeleteControlRequestsByAgentID(bgCtx(), agentID)
	if err != nil {
		slog.Error("clear runtime state: delete control requests", "agent_id", agentID, "error", err)
	}
	for _, requestID := range deletedIDs {
		h.broadcastControlCancel(agentID, requestID)
	}
}

// ClearAgentRuntimeState tears down the state tied to a dying SUBPROCESS: pending
// control_requests in the DB plus the in-memory trackers cleared by CleanupAgent.
// It leaves the durable control-response answer claims intact, so a duplicate answer
// straddling a transient restart stays deduped.
//
// Call it when the current subprocess instance is being torn down (close, context
// clear, plan-exec restart) -- NOT from the per-exit onExit handler, which a
// relaunch also triggers and which must keep the in-memory timeline state (see
// ClearPendingControlRequests).
func (h *OutputHandler) ClearAgentRuntimeState(agentID string) {
	h.ClearPendingControlRequests(agentID)
	h.CleanupAgent(agentID)
}

// rootTracker returns the SpanTracker for a ROOT main agent, creating one if
// needed. Used by NewSink; the kind is recorded so cleanup and the orphan sweep
// can distinguish root trackers from child trackers.
func (h *OutputHandler) rootTracker(agentID string) *SpanTracker {
	return h.trackers.getOrCreate(agentID, spanTrackerRoot)
}

// childTracker returns the SpanTracker for a virtual child agent, creating one
// if needed. Used by ChildSink; the kind is recorded as child. A child id
// registered earlier as root (or vice versa) panics — see
// spanTrackerRegistry.getOrCreate.
func (h *OutputHandler) childTracker(agentID string) *SpanTracker {
	return h.trackers.getOrCreate(agentID, spanTrackerChild)
}

// trackerForSnapshot resolves the tracker for a read-only snapshot without
// seeding an entry. Used by paths whose agent is expected to already have a
// sink (and thus a tracker); creating one here would mask a missing-sink bug.
// Returns a zero-value fallback only when the agent is genuinely unknown so
// the snapshot still renders "[]" instead of nil-dereferencing, and logs a
// warning so a real missing-sink regression surfaces in telemetry instead of
// rendering silently empty spans.
func (h *OutputHandler) trackerForSnapshot(agentID string) *SpanTracker {
	if t, _, ok := h.trackers.get(agentID); ok {
		return t
	}
	slog.Warn("snapshot resolved no span tracker for agent; expected a sink to own one",
		"agent_id", agentID)
	return &SpanTracker{}
}

// snapshotPassthroughSpanLines returns the JSON-encoded span lines for a
// root-level message that is not part of any span (e.g. a user-typed input
// while a subagent is running). Active spans render as vertical passthrough
// bars so the surrounding span columns remain visually unbroken across the
// row. Returns "[]" when no spans are active.
func (h *OutputHandler) snapshotPassthroughSpanLines(agentID string) string {
	_, lines, _ := h.trackerForSnapshot(agentID).Snapshot("", "", false)
	return lines
}

// NewSink creates a per-agent OutputSink backed by this OutputHandler.
func (h *OutputHandler) NewSink(agentID string, agentProvider leapmuxv1.AgentProvider) agent.OutputSink {
	s := &agentOutputSink{
		h:             h,
		agentID:       agentID,
		rootAgentID:   agentID,
		agentProvider: agentProvider,
		plugin:        agent.ProviderFor(agentProvider),
		tracker:       h.rootTracker(agentID),
	}
	h.rootSinks.Store(agentID, s)
	return s
}

// agentOutputSink implements agent.OutputSink for a single agent.
type agentOutputSink struct {
	h *OutputHandler
	// agentID is THIS sink's agent id (a child id for a child sink).
	agentID string
	// rootAgentID is the ROOT main agent id that owns the registry; it equals
	// agentID for a root sink. A child sink shares its parent's rootAgentID so
	// every registry write (EnsureChildAgent, UpsertBackgroundTask, ...) lands
	// under the root and a child's own TodoWrite feeds agent_todos under the
	// child id (correct for the child tab).
	rootAgentID   string
	agentProvider leapmuxv1.AgentProvider
	plugin        agent.Provider
	tracker       *SpanTracker
	// childSinks caches ChildSink results by child agent id so repeated
	// PersistChild* / nested-spawn calls reuse the same child sink (and its
	// span tracker). Guarded by childMu.
	childMu    sync.Mutex
	childSinks map[string]*agentOutputSink

	// sessionInfoMu guards lastSessionInfo against concurrent
	// BroadcastSessionInfo calls. Agent handlers may broadcast from
	// multiple goroutines (Pi fans out from many event handlers in
	// particular), so the dedup state needs synchronized access.
	//
	// Values are stored as their JSON-marshaled bytes (not the original
	// `any` value) so per-key dedup is a `bytes.Equal` instead of a
	// recursive `reflect.DeepEqual`. The wire encoding of these values
	// is JSON anyway, so the cached bytes line up exactly with what the
	// frontend would observe.
	sessionInfoMu   sync.Mutex
	lastSessionInfo map[string][]byte

	// catalogMu serializes the read-build-persist of the option-group catalog in
	// BroadcastStatusActive. Every BroadcastStatusActive for an agent runs on this one
	// per-agent sink, but from several goroutines -- the reader goroutine folding a
	// config_option_update, the ClearContext RPC, Pi's multi-handler fan-out. Without
	// serialization two callers can read the same stale row, each capture a DIFFERENT live
	// catalog, and blind-write, last-writer-wins persisting an OLDER catalog over a newer one.
	// Holding catalogMu across the row read + status build (which captures the freshest live
	// catalog) + write makes the last writer always persist the most recent live state.
	catalogMu sync.Mutex
}

// --- OutputSink interface implementation ---

func (s *agentOutputSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	return s.h.persistAndBroadcast(s.agentID, s.agentProvider, source, content, span, s.tracker)
}

// PersistTurnEnd persists the universal turn-end divider envelope and
// fires the git-status auto-broadcast. Each provider's final
// envelope (Claude type:"result", Codex turn/completed, ACP prompt
// response, Pi agent_end) routes here, so the side effect is explicit
// at the call site. Runs BroadcastGitStatus on a goroutine so the
// agent's stdout-read loop is not blocked by the git subprocesses plus
// the DB lookup.
func (s *agentOutputSink) PersistTurnEnd(content []byte, span agent.SpanInfo) error {
	// Exactly ONE divider closes a subagent transcript, in EITHER arrival order.
	//
	// The two events are independent -- Claude's task_notification can reach the
	// registry before its forwarded result reaches here -- so whichever arrives
	// first CLAIMS the close, and the loser stands down. This side draws the
	// richer divider (the duration, and on failure the error label and detail),
	// so when it wins the transcript keeps that; when the registry won, its
	// neutral divider already carries the finished status and the transcript
	// still closes honestly.
	//
	// Claimed only for a turn end that actually ENDS the subagent, not every
	// turn: a Codex collab child draws a turn-end divider at the end of every
	// turn and then accepts another.
	//
	// Child sinks only (agentID != rootAgentID). A root turn end is not a
	// subagent boundary and must not claim anything.
	//
	// Only the PERSIST is suppressed. The turn genuinely ended, so the turn-end
	// event and the git-status refresh below still owe the client their work:
	// the event drives the completion sound and the off-screen tab's notification
	// dot, and the refresh re-reads a working tree the subagent just edited.
	// Returning here would drop both, nondeterministically, on whichever arrival
	// order won.
	endsSubagent := s.agentID != s.rootAgentID &&
		agent.ProviderFor(s.agentProvider).EndsSubagentTranscript(content)
	duplicateDivider := endsSubagent && !s.h.claimSubagentTranscriptClose(s.h.bgTaskCtx(), s.agentID)
	if !duplicateDivider {
		if err := s.h.persistAndBroadcast(s.agentID, s.agentProvider, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span, s.tracker); err != nil {
			return err
		}
	}
	count, ok := agent.ProviderFor(s.agentProvider).TurnEndToolUses(content)
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: turnEndEvent(count, ok)},
	})
	go s.BroadcastGitStatus()
	return nil
}

func turnEndEvent(count int32, ok bool) *leapmuxv1.AgentTurnEnd {
	if !ok {
		return &leapmuxv1.AgentTurnEnd{}
	}
	return &leapmuxv1.AgentTurnEnd{NumToolUses: &count}
}

func (s *agentOutputSink) PersistNotification(source leapmuxv1.MessageSource, content []byte) (bool, error) {
	return s.h.persistNotificationThreaded(s.agentID, s.agentProvider, s.plugin, source, content)
}

func (s *agentOutputSink) OpenSpan(spanID, parentSpanID string) {
	s.tracker.OpenSpan(spanID, parentSpanID)
}

func (s *agentOutputSink) CloseSpan(spanID string) {
	s.tracker.CloseSpan(spanID)
}

func (s *agentOutputSink) ResetSpans() {
	s.tracker.Reset()
}

func (s *agentOutputSink) SetSpanType(spanID, spanType string) {
	s.tracker.SetSpanType(spanID, spanType)
}

func (s *agentOutputSink) GetSpanType(spanID string) string {
	return s.tracker.GetSpanType(spanID)
}

func (s *agentOutputSink) ReserveSpanColor(spanID, parentSpanID string) int32 {
	return s.tracker.ReserveSpanColor(spanID, parentSpanID)
}

func (s *agentOutputSink) BroadcastStreamChunk(content []byte, spanID string, method string) {
	if !s.tracker.ShouldBroadcastStreamChunk(spanID) {
		return
	}
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_StreamChunk{
			StreamChunk: &leapmuxv1.AgentStreamChunk{
				MessageId:     s.agentID,
				Delta:         content,
				AgentProvider: s.agentProvider,
				SpanId:        spanID,
				Method:        method,
			},
		},
	})
}

func (s *agentOutputSink) BroadcastStreamEnd(spanID string) {
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_StreamEnd{
			StreamEnd: &leapmuxv1.AgentStreamEnd{
				MessageId: s.agentID,
				SpanId:    spanID,
			},
		},
	})
}

func (s *agentOutputSink) PersistControlRequest(requestID string, payload []byte) string {
	// Mint a fresh per-INSTANCE claim token. A reused request_id (a Codex/ACP counter that reset
	// across a plan-exec restart, or a Claude follow-up) gets a distinct token here, so the answer's
	// idempotency claim is scoped to THIS instance -- the genuine answer to the new instance claims a
	// fresh key while a stale duplicate of the prior instance (old token) still loses. The token is
	// stored on the row so the replay to a reconnecting window (ListControlRequestsByAgentID) carries
	// it, AND returned to the caller so the paired live BroadcastControlRequest carries the SAME token
	// the frontend echoes back -- without a second GetControlRequest to read back what we just wrote
	// (and without the readback-failure window that would broadcast an empty token).
	claimToken := id.Generate()
	if err := s.h.queries.CreateControlRequest(bgCtx(), db.CreateControlRequestParams{
		AgentID:    s.agentID,
		RequestID:  requestID,
		Payload:    payload,
		ClaimToken: claimToken,
	}); err != nil {
		slog.Error("persist control request", "agent_id", s.agentID, "request_id", requestID, "error", err)
	}
	return claimToken
}

func (s *agentOutputSink) DeleteControlRequest(requestID string) {
	_ = s.h.queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   s.agentID,
		RequestID: requestID,
	})
}

func (s *agentOutputSink) BroadcastControlRequest(requestID string, payload []byte, claimToken string) {
	// claimToken is the per-instance token PersistControlRequest just minted and returned, threaded
	// straight through by the paired caller so the frontend can echo it in its answer (see
	// AgentControlRequest.claim_token) -- no readback of the row we just wrote.
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_ControlRequest{
			ControlRequest: buildAgentControlRequest(s.agentID, s.agentProvider, requestID, payload, claimToken),
		},
	})
}

func (s *agentOutputSink) BroadcastControlCancel(requestID string) {
	s.h.broadcastControlCancel(s.agentID, requestID)
}

func (s *agentOutputSink) UpdateSessionID(sessionID string) {
	existingAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for session ID comparison",
			"agent_id", s.agentID, "error", err)
		return
	}

	if existingAgent.AgentSessionID != sessionID {
		if err := s.h.queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: sessionID,
			ID:             s.agentID,
		}); err != nil {
			slog.Error("failed to store agent session ID",
				"agent_id", s.agentID, "error", err)
			return
		}

		slog.Info("agent session ID updated",
			"agent_id", s.agentID, "session_id", sessionID)
	}
}

// buildStatusChange constructs an AgentStatusChange from the given DB agent.
// Fields that are always the same across callers (agentID, workerOnline,
// agentProvider, gitStatus) are filled in automatically. The option groups are
// projected straight from the row's persisted options -- every caller writes the
// new selections into the row before calling, so no per-axis override is needed.
func (s *agentOutputSink) buildStatusChange(
	dbAgent db.Agent,
	status leapmuxv1.AgentStatus,
	sessionID string,
) *leapmuxv1.AgentStatusChange {
	return &leapmuxv1.AgentStatusChange{
		AgentId:        s.agentID,
		Status:         status,
		AgentSessionId: sessionID,
		WorkerOnline:   true,
		GitStatus:      gitutil.GetGitStatus(bgCtx(), dbAgent.WorkingDir),
		AgentProvider:  s.agentProvider,
		OptionGroups:   optionGroupsView(s.h.agents, &dbAgent, nil),
	}
}

func (s *agentOutputSink) UpdatePermissionMode(mode string) {
	dbAgent, fetchErr := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	oldMode := ""
	if fetchErr == nil {
		oldMode = parseOptions(dbAgent.Options)[agent.OptionIDPermissionMode]
		// Merge ONLY the permission-mode key via compare-and-swap, not a full-map blob
		// write built on this snapshot: UpdatePermissionMode holds no lifecycle lock and
		// runs on the agent's output/reader goroutine, so a concurrent writer (an RPC
		// UpdateAgentSettings landing effort, or a PersistSettingsRefresh) could otherwise
		// have its change clobbered when our full-map write replays the stale snapshot --
		// the exact race casPersistOptions exists to prevent.
		if settled, wrote, err := s.casPersistOptions(dbAgent.Options, map[string]string{agent.OptionIDPermissionMode: mode}); err != nil {
			slog.Warn("failed to persist permission mode", "agent_id", s.agentID, "error", err)
		} else if wrote {
			dbAgent.Options = settled
		}
	}

	// Broadcast statusChange so frontends update their permission mode display.
	if fetchErr == nil {
		sc := s.buildStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, dbAgent.AgentSessionID)
		s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
			AgentId: s.agentID,
			Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
		})
	}

	// Broadcast settings_changed notification for the chat view.
	s.NotifyPermissionModeChanged(oldMode, mode)
}

// NotifyPermissionModeChanged emits the chat-view settings_changed notification for
// a permission-mode transition, without persisting the mode or broadcasting a
// StatusChange. A no-op when oldMode is empty (unknown prior value) or unchanged.
//
// Display labels are resolved here at the emit site -- exactly as the RPC-driven
// buildSettingsChanges path does -- so the notification is self-describing. The
// server-initiated mode change (an ACP config_option_update routed through
// UpdatePermissionMode) would otherwise carry only raw ids and depend entirely on the
// frontend settings-label cache being primed, rendering an opaque mode id (e.g. a
// Cursor/Copilot session-mode URL) on a cache miss.
func (s *agentOutputSink) NotifyPermissionModeChanged(oldMode, newMode string) {
	if oldMode == "" || oldMode == newMode {
		return
	}
	// On a successful row fetch, resolve labels through the shared typed builder
	// (optionGroupChangeEntry) so the {old,new,oldLabel,newLabel,label} keys are spelled in exactly
	// one place -- a misspelled key is a compile error, not a silently-absent UI field -- the same
	// goal the RPC-driven buildSettingsChanges path already relies on. On a fetch failure, fall back
	// to the bare {old,new} ids (no label keys), so the frontend resolves labels from its cache
	// rather than rendering blank for an explicit empty-string label (it honors an explicit "" over
	// the cache; see notificationRenderers).
	var change any = map[string]string{"old": oldMode, "new": newMode}
	if dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID); err == nil {
		groups := optionGroupsView(s.h.agents, &dbAgent, nil)
		change = optionGroupChangeEntry(oldMode, newMode,
			func(v string) string { return optionLabelInGroups(groups, agent.OptionIDPermissionMode, v) },
			optionGroupLabelInGroups(groups, agent.OptionIDPermissionMode))
	}
	s.PersistLeapMuxNotification(map[string]interface{}{
		"type": agent.NotificationTypeSettingsChanged,
		"changes": map[string]interface{}{
			agent.OptionIDPermissionMode: change,
		},
	})
}

func (s *agentOutputSink) PersistSettingsRefresh(refresh optionmap.Map) {
	dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for settings broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}

	// Fold the refreshed values into the persisted options map using the wire
	// merge contract (mergeOptions): a key present with a non-empty value is set,
	// a key present with an empty value is deleted, and an ABSENT key is preserved.
	// This single uniform policy replaces the old scalar-vs-options split:
	//   - A provider that can't report an axis OMITS it so the stored value
	//     survives -- Claude omits permissionMode (keeps a startup-time
	//     set_permission_mode), ACP omits effort, ACP primary-agent providers omit
	//     model when the server advertises none.
	//   - A provider that cleared an option sends it as an empty value so the key
	//     is removed (e.g. an ACP option the agent stopped surfacing).
	// Every provider reports concrete values for what it manages, so this matches
	// the previous behavior without naming any axis specially.
	//
	// The merge-and-no-op-detect happens in exactly ONE place per path: the running-agent
	// path delegates it to casPersistAgentOptions (which returns wrote=false on a no-op);
	// only the startup-window path, which skips that CAS, merges inline for its broadcast.
	var newOptions string
	startupWindow := s.h.agentStarting != nil && s.h.agentStarting(s.agentID)
	if !startupWindow {
		// Persist via a compare-and-swap so two concurrent refreshes -- the ACP reader
		// goroutine folding a server-initiated config_option_update and an RPC-driven
		// UpdateSettings -- can't lose each other's option writes. They share no lock on
		// this path, so each merges onto the snapshot it read; a CAS that misses (the row
		// moved on between read and write) re-reads, re-merges, and retries instead of
		// overwriting the other writer's keys with a stale full-map blob.
		//
		// casPersistOptions also owns the no-op detection: it returns wrote=false when the
		// merge leaves the row unchanged -- the refresh just re-confirms the stored values
		// (refreshes fire after UpdateSettings, and startup readbacks often confirm the
		// row), or a concurrent writer already landed an equivalent merge and broadcast it.
		// Either way there is nothing new to persist or broadcast, so skip both.
		settled, wrote, err := s.casPersistOptions(dbAgent.Options, refresh)
		if err != nil {
			slog.Error("failed to persist refreshed settings",
				"agent_id", s.agentID, "error", err)
			return
		}
		if !wrote {
			return
		}
		newOptions = settled
	} else {
		// This refresh fires from the agent's first init message, which arrives
		// while the agent is still inside startAgent's provider handshake -- before
		// runAgentStartup's final settings handoff. A settings change made during
		// that startup window is written to the DB by UpdateAgentSettings but can't
		// be applied to the not-yet-ready agent, so persisting the confirmed LAUNCH
		// settings here would clobber the user's choice (e.g. overwrite a model
		// switched to during startup with the launch model). runAgentStartup's final
		// handoff persists the confirmed settings with that change preserved (and
		// relaunches to apply it), so skip the DB write while startup is in progress
		// and only broadcast the early ACTIVE/status event.
		//
		// DELIBERATE startup-window divergence: the in-memory patch + broadcast below
		// advertises the merged options even though the row on disk still holds the prior
		// options. This is intentional, not a bug -- the early ACTIVE/status event must
		// reflect the provider's just-confirmed catalog so the frontend isn't stuck on
		// stale launch values, while the DB write is deferred (above) precisely so a
		// mid-startup user change isn't clobbered. The transient where a concurrent DB read
		// sees the older options is reconciled at runAgentStartup's handoff. Do NOT "fix"
		// the divergence by gating the broadcast on a DB write -- that reintroduces the clobber.
		stored := parseOptions(dbAgent.Options)
		newOptions = marshalOptions(mergeOptions(stored, refresh))
		// Skip the broadcast when the refresh is a no-op (the startup readback often just
		// confirms the stored row), sparing a pointless frontend reactivity tick. Compare
		// canonicalized forms on both sides so key ordering can't read as a change.
		if newOptions == marshalOptions(stored) {
			return
		}
	}

	// Patch the fetched row in-memory to reflect the values we just persisted (or, in the
	// startup window, would persist), avoiding a second GetAgentByID round-trip before we
	// build the status-change event below.
	dbAgent.Options = newOptions

	sc := s.buildStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, dbAgent.AgentSessionID)

	// Persist the live catalog too, not just the option VALUES above. A single server-initiated
	// config_option_update that BOTH changes an option value AND grows the catalog (e.g. an ACP
	// dynamic-model provider whose model list arrives post-handshake and the user switches to a
	// just-revealed model in the same update) routes here -- the value change wins the
	// mutually-exclusive switch in handleACPConfigOptionUpdate, so the listChanged branch that
	// would persist the catalog via BroadcastStatusActive never runs. Without persisting here the
	// grown catalog is lost from the option_groups column, and the post-exit offline picker serves
	// the stale, narrower catalog. persistLiveCatalog shares BroadcastStatusActive's catalogMu +
	// no-op/never-truncate guards, so the common value-only refresh costs one proto.Equal diff and
	// no write. Skipped in the startup window for the same reason the options DB write is deferred
	// above: runAgentStartup's handoff persists the confirmed catalog atomically, and a mid-startup
	// write here could clobber a user change.
	if !startupWindow {
		s.persistLiveCatalog(sc.GetOptionGroups())
	}

	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// persistLiveCatalog persists the live option-group catalog to this agent's row when it has
// grown/changed beyond the stored copy, re-reading the row under catalogMu so a concurrent
// BroadcastStatusActive can't last-writer-wins an older catalog over a newer one. Shares the
// persistCatalogIfChanged no-op/never-truncate guards. Used by PersistSettingsRefresh, whose
// value-change broadcast does not flow through BroadcastStatusActive yet can still carry a
// freshly grown catalog. The catalog passed in is the same one just broadcast, so the persisted
// row matches the event the frontend received.
func (s *agentOutputSink) persistLiveCatalog(live []*leapmuxv1.AvailableOptionGroup) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()

	existing, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for catalog persist",
			"agent_id", s.agentID, "error", err)
		return
	}
	s.persistCatalogIfChanged(existing, live)
}

// optionsCASMaxAttempts bounds the PersistSettingsRefresh compare-and-swap retry loop.
// Each lost race is another concurrent writer winning; in practice at most a couple
// contend, so this is a generous ceiling that still fails loudly rather than spinning.
const optionsCASMaxAttempts = 8

// casPersistOptions persists a settings refresh for this sink's agent via the shared
// compare-and-swap helper. See casPersistAgentOptions for the contract.
func (s *agentOutputSink) casPersistOptions(expected string, refresh map[string]string) (string, bool, error) {
	return casPersistAgentOptions(bgCtx(), s.h.queries, s.agentID, expected, refresh)
}

// withoutStaleClears returns refresh with every STALE clear removed -- an empty-valued (clear)
// entry whose key is ABSENT from snapshot. Such a clear is a no-op against snapshot, so it
// carries no intent for THIS snapshot; re-applying it to a newer row (re-read after a lost CAS,
// or in the final last-writer-wins merge) would instead DELETE a value a concurrent writer set
// on that key. A clear of a key the snapshot DOES hold is genuine -- it removes a value the
// snapshot saw -- and is kept, so it still applies. Returns m unchanged (no allocation) when it
// holds no stale clear, the common case. Called against each CAS iteration's own base snapshot so
// a clear that turns stale only after a re-read is dropped on the next pass.
func withoutStaleClears(refresh, snapshot map[string]string) map[string]string {
	hasStale := false
	for k, v := range refresh {
		if v == "" {
			if _, ok := snapshot[k]; !ok {
				hasStale = true
				break
			}
		}
	}
	if !hasStale {
		return refresh
	}
	out := make(map[string]string, len(refresh))
	for k, v := range refresh {
		if v == "" {
			if _, ok := snapshot[k]; !ok {
				continue // stale clear: snapshot never held this key
			}
		}
		out[k] = v
	}
	return out
}

// narrowedOptionDelta canonicalizes one compare-and-swap attempt's inputs against the `expected`
// snapshot: it drops any STALE clear from `delta` (an empty-value key absent from `expected`, which
// carries no intent against this base and would clobber a concurrent writer's value of that key on a
// lost CAS), and returns the narrowed delta, the canonical base (the snapshot re-marshaled, the value
// the CAS compares against), and the canonical merge of the narrowed delta onto it. The three CAS
// sites (casPersistAgentOptions' loop body and last-writer-wins tail, casPersistConfirmedSettings'
// loop body) share this one "narrow, canonicalize, merge" contract -- and parse `expected` ONCE here
// instead of three times each -- so a change to the contract lands in one place. A genuine clear (a
// key present in `expected`) is kept and still applies.
func narrowedOptionDelta(expected string, delta map[string]string) (narrowed map[string]string, base, merged string) {
	snapshot := parseOptions(expected)
	narrowed = withoutStaleClears(delta, snapshot)
	base = marshalOptions(snapshot)
	merged = marshalOptions(mergeOptions(snapshot, narrowed))
	return narrowed, base, merged
}

// casPersistAgentOptions merges `refresh` onto the agent's stored options and writes the
// result via SetAgentOptionsIfUnchanged (compare-and-swap), re-reading and re-merging
// when a concurrent writer moves the row between the read and the write. `expected` is
// the caller's already-read options snapshot, tried first to avoid a redundant fetch.
// Returns (settled, true, nil) with the marshaled options actually persisted, or
// (snapshot, false, nil) when the merge is a no-op -- the delta changes nothing against the
// snapshot it was decided against (the caller's `expected` on the first try, or the row
// re-read after a lost CAS). `snapshot` is that marshaled map, so the caller can broadcast it
// without a re-fetch; on the common uncontended path it IS the current row.
//
// Shared by every options writer that holds no common lock: the ACP/RPC settings-refresh
// sink (PersistSettingsRefresh, on the agent reader goroutine) and the service-layer
// option-change setters (applyOptionChanges / applySettingsLive, on the RPC dispatcher).
// The per-agent lifecycle lock does NOT serialize against the reader-goroutine writer (it
// never takes the lock), so a blind full-map write could drop a key a refresh had just
// merged in. Merging only each writer's own delta under CAS makes the two converge instead.
func casPersistAgentOptions(ctx context.Context, q *db.Queries, agentID, expected string, refresh map[string]string) (string, bool, error) {
	for attempt := 0; attempt < optionsCASMaxAttempts; attempt++ {
		// Narrow stale clears, canonicalize the base, and merge -- against the re-read `expected` each
		// iteration, so a clear that turns stale only after a concurrent delete is dropped too. The
		// narrowed `refresh` is carried forward (monotonic narrowing across attempts).
		var base, newOptions string
		refresh, base, newOptions = narrowedOptionDelta(expected, refresh)
		if newOptions == base {
			// The refresh changes nothing against `expected` -- but `expected` is the caller's
			// snapshot, which a concurrent writer may have moved on. Decide the no-op against the
			// LIVE row, not the stale snapshot: re-read and re-merge so a refresh that WOULD
			// re-assert the agent's confirmed value over a key another writer just cleared isn't
			// silently skipped (which would leave the row diverged from the running agent until
			// the next refresh). Still a no-op against the live row -> settled; otherwise fall
			// through to the CAS below with the live row as the new base. (The next iteration's
			// withoutStaleClears narrows `refresh` against that live row.)
			dbAgent, err := q.GetAgentByID(ctx, agentID)
			if err != nil {
				return "", false, err
			}
			// Reaching the no-op branch means `refresh` is a no-op against `expected`: every SET
			// equals the stored value and -- after the top-of-loop narrowing -- it holds no clear
			// (a genuine clear of a key in `expected` would have changed base; a stale clear was
			// dropped). So merge it directly against the live row; the next iteration re-narrows.
			live := marshalOptions(parseOptions(dbAgent.Options))
			if marshalOptions(mergeOptions(parseOptions(dbAgent.Options), refresh)) == live {
				return live, false, nil
			}
			expected = dbAgent.Options
			continue
		}
		changed, err := q.SetAgentOptionsIfUnchanged(ctx, db.SetAgentOptionsIfUnchangedParams{
			Options: newOptions,
			ID:      agentID,
			// Compare against the CANONICAL form of the snapshot, the same value the no-op check
			// above decided against -- not the raw `expected` string. Every options write goes
			// through marshalOptions, so the stored column is always canonical; matching it with the
			// canonical `base` keeps the expected-value representation consistent within this function
			// rather than relying on the caller's snapshot already being canonical.
			ExpectedOptions: base,
		})
		if err != nil {
			return "", false, err
		}
		if changed > 0 {
			return newOptions, true, nil
		}
		// The row changed between read and write; re-read and re-merge onto the new value.
		dbAgent, err := q.GetAgentByID(ctx, agentID)
		if err != nil {
			return "", false, err
		}
		expected = dbAgent.Options
	}
	// Sustained contention lost every CAS attempt. Rather than DROP the write (which would
	// silently strand the settled options), do a final last-writer-wins merge: re-read, merge
	// only this writer's own delta onto the latest row, and write unconditionally. Merging
	// (not blind-overwriting) still preserves other writers' keys; the only loss window is a
	// write landing between this read and write -- the same window a single CAS iteration has,
	// so this is strictly better than dropping the delta entirely.
	dbAgent, err := q.GetAgentByID(ctx, agentID)
	if err != nil {
		return "", false, err
	}
	// Narrow stale clears against this final re-read too, so the unconditional write below can't
	// delete a key a concurrent writer set that this delta never legitimately cleared. This is the
	// final attempt, so the narrowed delta isn't carried forward -- only its merge is written.
	_, base, newOptions := narrowedOptionDelta(dbAgent.Options, refresh)
	if newOptions == base {
		return base, false, nil
	}
	if err := q.SetAgentOptions(ctx, db.SetAgentOptionsParams{Options: newOptions, ID: agentID}); err != nil {
		return "", false, err
	}
	slog.Warn("options CAS exhausted; applied final last-writer-wins merge",
		"agent_id", agentID, "attempts", optionsCASMaxAttempts)
	return newOptions, true, nil
}

// casPersistConfirmedSettings atomically persists the confirmed option DELTA and the provider
// option-group catalog in ONE statement per attempt (UpdateAgentConfirmedSettings), so a concurrent
// options writer can't land BETWEEN two separate column writes and leave the row showing this
// handoff's options beside a foreign catalog. The options column merges only `delta` (preserving a
// concurrent writer's other keys) under a CAS-with-retry on `expectedOptions`; the option_groups
// column rides the SAME statement, written only on the successful options CAS so the two columns
// move together-or-neither. `expectedCatalog`/`catalog` are the catalog CAS pair (the catalog
// snapshot the row carried when this handoff began, and the new catalog); pass both "" to leave the
// catalog untouched (e.g. when its marshal failed). Returns the settled row for the broadcast.
func casPersistConfirmedSettings(ctx context.Context, q *db.Queries, agentID, expectedOptions string, delta map[string]string, expectedCatalog, catalog string) (db.Agent, error) {
	for attempt := 0; attempt < optionsCASMaxAttempts; attempt++ {
		// Drop stale clears against this base, exactly as casPersistAgentOptions does, so a delta
		// pairing a set with a clear of a key a concurrent writer set can't clobber it on a retry.
		var base, newOptions string
		delta, base, newOptions = narrowedOptionDelta(expectedOptions, delta)
		row, err := q.UpdateAgentConfirmedSettings(ctx, db.UpdateAgentConfirmedSettingsParams{
			ExpectedOptions:      base,
			Options:              newOptions,
			ExpectedOptionGroups: expectedCatalog,
			OptionGroups:         catalog,
			ID:                   agentID,
		})
		if err != nil {
			return db.Agent{}, err
		}
		// The options column equals newOptions iff our CAS hit -- OR a concurrent writer landed the
		// identical blob. Our CAS hit when the options CASE matched (options = expected_options at
		// statement time, so options = base = newOptions's source); then the gated option_groups CASE
		// also took the THEN branch and rode the same statement. But the concurrent writer that landed
		// the identical blob could be the options-only path (casPersistAgentOptions via
		// PersistSettingsRefresh/applyOptionChanges), which NEVER writes option_groups: it left
		// options = newOptions but option_groups = expected_option_groups, so our options CASE saw
		// options != base, took ELSE, and the gated option_groups CASE took ELSE too -- dropping the
		// catalog this handoff discovered. Re-assert the catalog with a standalone CAS so a richer one
		// we found still lands. The CAS is a no-op (keeping the richer one) if a running provider grew
		// the catalog past expectedCatalog in the meantime, mirroring the in-statement gate.
		if row.Options == newOptions {
			if catalog != "" && row.OptionGroups != catalog && row.OptionGroups == expectedCatalog {
				updated, err := q.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
					OptionGroups:         catalog,
					ExpectedOptionGroups: expectedCatalog,
					ID:                   agentID,
				})
				if err != nil {
					return db.Agent{}, err
				}
				if updated > 0 {
					row.OptionGroups = catalog
				}
			}
			return row, nil
		}
		// Lost the options CAS: the gated catalog CASE wrote nothing either, so re-merge the delta
		// onto the live row and retry -- both columns stay atomic.
		expectedOptions = row.Options
	}
	// Sustained contention lost every atomic attempt. Fall back to the (non-atomic) options-then-
	// catalog writes: options via the guaranteed-landing CAS helper, then the standalone catalog CAS.
	// This reintroduces the brief two-write window ONLY after optionsCASMaxAttempts failed atomic
	// tries -- far rarer than the window the atomic path closes -- and still lands the confirmation.
	slog.Warn("confirmed-settings atomic CAS exhausted; applied non-atomic fallback",
		"agent_id", agentID, "attempts", optionsCASMaxAttempts)
	if _, _, err := casPersistAgentOptions(ctx, q, agentID, expectedOptions, delta); err != nil {
		return db.Agent{}, err
	}
	if catalog != "" || expectedCatalog != "" {
		if _, err := q.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
			OptionGroups:         catalog,
			ExpectedOptionGroups: expectedCatalog,
			ID:                   agentID,
		}); err != nil {
			return db.Agent{}, err
		}
	}
	return q.GetAgentByID(ctx, agentID)
}

func (s *agentOutputSink) BroadcastStatusActive(sessionID string) {
	sc := s.persistCatalogAndBuildStatus(sessionID)
	if sc == nil {
		return
	}
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// persistCatalogAndBuildStatus reads the row, builds the ACTIVE status change (capturing the
// freshest live catalog), and persists that catalog -- all under catalogMu so concurrent
// BroadcastStatusActive callers on this one per-agent sink can't read the same stale row and
// blind-write different live catalogs, last-writer-wins persisting an older catalog over a newer
// one. The network broadcast is left to the caller so it runs OUTSIDE the lock. Returns nil (no
// broadcast) when the row read fails.
func (s *agentOutputSink) persistCatalogAndBuildStatus(sessionID string) *leapmuxv1.AgentStatusChange {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()

	existingAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for status broadcast",
			"agent_id", s.agentID, "error", err)
		return nil
	}

	sc := s.buildStatusChange(existingAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sessionID)

	// Persist the live catalog when it has grown/changed beyond the row's copy, so the
	// post-exit offline read reflects options the running agent discovered AFTER the
	// startup handoff already persisted a narrower catalog -- e.g. an ACP dynamic-model
	// provider (Copilot/OpenCode/Goose) whose model list arrives only via a post-handshake
	// config_option_update, which broadcasts through here. option_groups is a column separate
	// from options, so this never races the options-value CAS. catalogMu (held here) serializes
	// this against the OTHER BroadcastStatusActive calls on this one per-agent sink, so two
	// concurrent broadcasts can't read the same row and blind-write divergent live catalogs,
	// last-writer-wins persisting an older one. It does NOT serialize against the dispatcher/
	// startup paths, which persist the catalog via SetAgentOptionGroupsIfUnchanged (a CAS on the
	// same column, off this lock); the blind write here is still correct because `live` is the
	// authoritative manager catalog (optionGroupsView -> Manager.OptionGroups for the running
	// agent), which every catalog change re-broadcasts, so the row converges to the freshest live
	// catalog on the next push regardless of interleaving. The proto.Equal diff keeps the common
	// (unchanged-catalog) broadcast from writing on every push.
	s.persistCatalogIfChanged(existingAgent, sc.GetOptionGroups())
	return sc
}

// persistCatalogIfChanged writes the live option-group catalog to the row when it differs
// from the stored one. Never overwrites a populated catalog with an empty one (a transient
// empty live read must not wipe the persisted options), nor with a TRUNCATED one when a group
// fails to marshal -- a partial catalog would never compare equal and so would re-write every push.
func (s *agentOutputSink) persistCatalogIfChanged(existing db.Agent, live []*leapmuxv1.AvailableOptionGroup) {
	if len(live) == 0 || optionGroupsEqual(parseOptionGroups(existing.OptionGroups), live) {
		return
	}
	marshaled, err := marshalOptionGroups(live)
	if err != nil {
		slog.Warn("skipping discovered option-group catalog persist; marshal failed",
			"agent_id", s.agentID, "error", err)
		return
	}
	if err := s.h.queries.SetAgentOptionGroups(bgCtx(), db.SetAgentOptionGroupsParams{
		OptionGroups: marshaled,
		ID:           s.agentID,
	}); err != nil {
		slog.Warn("failed to persist discovered option-group catalog", "agent_id", s.agentID, "error", err)
	}
}

// BroadcastGitStatus emits a partial AgentStatusChange carrying the
// agent id, worker liveness, and a freshly-computed git status. Auto-fired by
// PersistTurnEnd so the working-tree view stays in sync without
// provider involvement; providers do not call this directly.
//
// The frontend's statusChange handler treats events whose Status is
// UNSPECIFIED as partial updates and applies only the populated fields,
// so this avoids re-shipping the full catalog/settings payload that
// BroadcastStatusActive carries. WorkerOnline is still set because only
// a live worker can emit this event; leaving it as the proto default false
// makes older clients interpret a git refresh as an offline transition.
func (s *agentOutputSink) BroadcastGitStatus() {
	dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for git status broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}
	sc := &leapmuxv1.AgentStatusChange{
		AgentId:      s.agentID,
		WorkerOnline: true,
		GitStatus:    gitutil.GetGitStatus(bgCtx(), dbAgent.WorkingDir),
	}
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// thinkingTokensSessionInfoKey is the agent_session_info key carrying Claude's
// per-turn thinking-token estimate. Unlike cost/rate-limit/context-usage keys
// (which carry meaningfully across turns and benefit from dedup), this is a
// per-turn running count the frontend clears at several boundaries the worker
// can't all observe (the turn-end divider, each interleaved thinking phase, a
// pause for input). Deduping it here would suppress a re-broadcast of a value
// the frontend already cleared -- leaving the counter hidden until a strictly
// different estimate arrives. It streams unique monotonic values normally and
// the frontend store dedups identical updates itself, so BroadcastSessionInfo
// skips the dedup cache for this one key entirely (always ship, never cache).
//
// Sourced from the agent package's broadcast key so the exemption keys off the
// exact same string the Claude handler ships, instead of a hand-copied literal
// that could silently drift and re-enable the dedup.
const thinkingTokensSessionInfoKey = agent.SessionInfoKeyThinkingTokens

// BroadcastSessionInfo emits an ephemeral agent_session_info update,
// but only for keys whose values changed since the previous broadcast.
// Agent handlers commonly re-emit identical payloads (Pi especially,
// since it fans out from many event handlers, but Claude/Codex/ACP also
// repeat usage and rate-limit values across successive turns); shipping
// unchanged keys wakes reactive frontend consumers for nothing. When
// every key is equal to the cached value, the broadcast is dropped
// entirely.
//
// Equality is per-key on the JSON-marshaled bytes of each value: scalars
// short-circuit to a tiny `bytes.Equal`, and nested maps (contextUsage,
// rateLimits) compare as their canonical JSON encoding (Go marshals map
// keys in sorted order). Any change inside a nested map ships the whole
// sub-map, which matches the frontend store's per-key merge semantics
// in agentSession.store.ts. A marshal failure is treated as "changed"
// so the value still passes through to the broadcast.
func (s *agentOutputSink) BroadcastSessionInfo(info map[string]interface{}) {
	if len(info) == 0 {
		return
	}
	s.sessionInfoMu.Lock()
	if s.lastSessionInfo == nil {
		s.lastSessionInfo = make(map[string][]byte, len(info))
	}
	changed := make(map[string]interface{}, len(info))
	for k, v := range info {
		// thinking_tokens is exempt from dedup -- always ship it and never cache
		// it, so a re-broadcast after a frontend-side clear is never suppressed.
		// See thinkingTokensSessionInfoKey.
		if k == thinkingTokensSessionInfoKey {
			changed[k] = v
			continue
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			// Can't dedup without canonical bytes — pass through.
			changed[k] = v
			continue
		}
		if prev, ok := s.lastSessionInfo[k]; ok && bytes.Equal(prev, encoded) {
			continue
		}
		changed[k] = v
		s.lastSessionInfo[k] = encoded
	}
	s.sessionInfoMu.Unlock()
	if len(changed) == 0 {
		return
	}
	s.h.broadcastAgentSessionInfo(s.agentID, changed)
}

func (s *agentOutputSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.h.PersistLeapMuxNotification(s.agentID, s.agentProvider, content)
}

func (s *agentOutputSink) StorePlanModeToolUse(toolUseID, targetMode string) {
	s.h.planModeToolUse.Store(toolUseID, targetMode)
}

func (s *agentOutputSink) LoadAndDeletePlanModeToolUse(toolUseID string) (string, bool) {
	v, ok := s.h.planModeToolUse.LoadAndDelete(toolUseID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (s *agentOutputSink) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) {
	s.h.updatePlan(s.agentID, content, compression, title)
}

func (s *agentOutputSink) ScheduleAutoContinue(schedule agent.AutoContinueSchedule) {
	s.h.scheduleAutoContinue(s.agentID, schedule)
}

func (s *agentOutputSink) CancelAutoContinue(reason agent.AutoContinueReason) {
	s.h.cancelAutoContinue(s.agentID, reason)
}

// --- Internal helpers ---

// notifMutex returns a per-agent mutex for notification threading.
func (h *OutputHandler) notifMutex(agentID string) *sync.Mutex {
	v, _ := h.notifMu.LoadOrStore(agentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// clearNotifThread clears the current notification thread boundary so
// that the next notification starts a new wrapper.
func (h *OutputHandler) clearNotifThread(agentID string) {
	if _, ok := h.lastNotifThread.Load(agentID); !ok {
		return
	}
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()
	h.lastNotifThread.Delete(agentID)
}

// createMessageRow persists a chat-message row, refusing invalid boundary values.
// Every persisted message must carry a real provider so the client can render it
// through that provider's renderers; an UNSPECIFIED provider is a persistence bug
// (the frontend surfaces such a row as `unsupported_provider`). mark_type is also
// stored as an integer and later rendered as a rail label, so reject unknown enum
// values before they become misleading clickable dots.
func createMessageRow(ctx context.Context, q *db.Queries, params db.CreateMessageParams) (int64, error) {
	if params.AgentProvider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return 0, fmt.Errorf("refusing to persist message %q for agent %q with UNSPECIFIED agent provider", params.ID, params.AgentID)
	}
	switch params.MarkType {
	case leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED,
		leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE,
		leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE:
	default:
		return 0, fmt.Errorf("refusing to persist message %q for agent %q with unknown mark_type %d", params.ID, params.AgentID, params.MarkType)
	}
	return q.CreateMessage(ctx, params)
}

// persistAndBroadcast persists a message and broadcasts it to watchers.
// tracker may be nil, in which case it is resolved from the agentID.
func (h *OutputHandler) persistAndBroadcast(agentID string, agentProvider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, contentJSON []byte, span agent.SpanInfo, tracker *SpanTracker) error {
	if h.wakeLock != nil {
		h.wakeLock.RecordActivity()
	}
	if tracker == nil {
		// Read-only resolution: the nil-tracker callers (control-response
		// echoes, synthetic user turns) target an agent that already owns a
		// sink and thus a tracker. Seeding one here would mask a missing-sink
		// bug; a fallback empty tracker renders "[]" for a genuinely unknown
		// agent, and trackerForSnapshot logs the miss.
		tracker = h.trackerForSnapshot(agentID)
	}
	connectorSpanID := resolveConnectorSpanID(span.SpanID, span.ConnectorSpanID, span.ParentSpanID, span.Closing)
	depth, spanLines, connectorColor := tracker.Snapshot(span.ParentSpanID, connectorSpanID, span.Closing)

	// Resolve span color: if the span is already active (e.g. tool_result
	// inside an open span), use the connector color from the snapshot.
	//
	// NoSpan opts out. A row that owns no span reports color 0 as its answer,
	// so filling it from the connector would tint a subagent spawn card with a
	// color no rail anywhere draws.
	spanColor := span.SpanColor
	if span.SpanID != "" && spanColor == 0 && connectorColor > 0 && !span.NoSpan {
		spanColor = connectorColor
	}

	msgID := id.Generate()
	compressed, compressionType := msgcodec.Compress(contentJSON)
	now := nowMillis()

	seq, err := createMessageRow(bgCtx(), h.queries, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Source:             source,
		Content:            compressed,
		ContentCompression: compressionType,
		Depth:              int64(depth),
		SpanID:             span.SpanID,
		ParentSpanID:       span.ParentSpanID,
		SpanType:           span.SpanType,
		SpanColor:          int64(spanColor),
		SpanLines:          spanLines,
		AgentProvider:      agentProvider,
		MarkType:           span.MarkType,
		CreatedAt:          sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return err
	}

	// Any persisted non-notification message breaks notification adjacency.
	h.clearNotifThread(agentID)

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Source:             source,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(now),
		Depth:              depth,
		SpanId:             span.SpanID,
		ParentSpanId:       span.ParentSpanID,
		SpanType:           span.SpanType,
		SpanColor:          spanColor,
		SpanLines:          spanLines,
		MarkType:           span.MarkType,
	})

	// Update the provider-neutral to-do list off the just-persisted
	// message. Failures are logged but do not propagate — the chat
	// transcript is the source of truth, and the next event can
	// reconcile any transient inconsistency.
	if err := h.applyTodoEventForMessage(agentID, span, contentJSON); err != nil {
		slog.Warn("apply todo event", "agent_id", agentID, "span_type", span.SpanType, "error", err)
	}
	return nil
}

// couldProduceTodoEvent is a cheap gate keeping the >99% of messages
// that can't produce a to-do mutation out of the extract / paired-use
// lookup hot path. Claude tool spans carry span_type for every message
// — when one is present, only the to-do family matters. Only the
// span-typeless path (Codex JSON-RPC notifications, ACP session
// updates) needs the byte-pattern probe.
func couldProduceTodoEvent(span agent.SpanInfo, contentJSON []byte) bool {
	if span.SpanType != "" {
		return todoevents.IsTodoToolSpanType(span.SpanType)
	}
	return todoevents.LooksLikeProviderPlan(contentJSON)
}

// applyTodoEventForMessage extracts a to-do event from the just-persisted
// message, applies it to agent_todos, and broadcasts AgentTodosChanged
// on mutation. No-ops (unknown-id update, unknown-id delete, empty
// snapshot replay) skip the broadcast.
func (h *OutputHandler) applyTodoEventForMessage(agentID string, span agent.SpanInfo, contentJSON []byte) error {
	if !couldProduceTodoEvent(span, contentJSON) {
		return nil
	}
	pairedJSON, err := h.lookupPairedToolUseJSON(agentID, span)
	if err != nil {
		return err
	}
	ev, ok := todoevents.Extract(span.SpanType, contentJSON, pairedJSON)
	if !ok {
		return nil
	}
	items, mutated, err := h.applyTodoEvent(agentID, ev)
	if err != nil {
		return err
	}
	if !mutated {
		return nil
	}
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_TodosChanged{
			TodosChanged: &leapmuxv1.AgentTodosChanged{
				AgentId: agentID,
				Todos:   todoevents.ItemsToProto(items),
			},
		},
	})
	return nil
}

// lookupPairedToolUseJSON returns the decompressed JSON of the paired
// tool_use message for Claude Task* tool_results that need their input
// fields. Returns nil with no error when not applicable.
func (h *OutputHandler) lookupPairedToolUseJSON(agentID string, span agent.SpanInfo) ([]byte, error) {
	switch span.SpanType {
	case todoevents.ToolTaskCreate, todoevents.ToolTaskUpdate:
		// fall through — these need the paired tool_use input.
	default:
		return nil, nil
	}
	if span.SpanID == "" {
		return nil, nil
	}
	row, err := h.queries.GetAgentMessageBySpanIDAndSource(bgCtx(), db.GetAgentMessageBySpanIDAndSourceParams{
		AgentID: agentID,
		SpanID:  span.SpanID,
		Source:  leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Race or rolled-up tool_use — extraction proceeds with the
		// result-only fields and returns a less detailed row.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup paired tool_use %s: %w", span.SpanID, err)
	}
	return msgcodec.Decompress(row.Content, row.ContentCompression)
}

// applyTodoEvent persists a single to-do event into agent_todos AND
// applies it to the in-memory mirror. Returns the post-mutation list
// (so the caller can broadcast without re-fetching) and a `mutated`
// flag that is false when the event was a no-op (unknown-id update,
// unknown-id delete, empty id).
func (h *OutputHandler) applyTodoEvent(agentID string, ev todoevents.Event) ([]todoevents.Item, bool, error) {
	ctx := bgCtx()
	cache := h.todoCache(agentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, agentID); err != nil {
		return nil, false, err
	}

	switch ev.Kind {
	case todoevents.KindSnapshot:
		return h.applySnapshotLocked(ctx, agentID, cache, ev.Snapshot)

	case todoevents.KindCreate, todoevents.KindDetail:
		if ev.Item.ID == "" {
			return nil, false, nil
		}
		existingIdx := todoIndexByID(cache.Rows, ev.Item.ID)
		merged := ev.Item
		if existingIdx >= 0 {
			if ev.Kind == todoevents.KindDetail {
				merged = todoevents.MergeDetail(cache.Rows[existingIdx].item, ev.Item)
			}
			// Idempotent replay: skip the DB write and the broadcast
			// when the post-merge row equals the existing row.
			if cache.Rows[existingIdx].item == merged {
				return todoItems(cache.Rows), false, nil
			}
		} else if len(cache.Rows) >= todoevents.MaxTodos {
			// At cap: evict the oldest finished row (completed or
			// deleted) to make room for the new task. When no finished
			// row exists, drop the new task and log so operators see
			// the cap pressure.
			evicted, err := h.evictOldestFinishedLocked(ctx, agentID, cache)
			if err != nil {
				return nil, false, err
			}
			if !evicted {
				slog.Warn("agent_todos cap reached, no completed or deleted task to evict; dropping new task",
					"agent_id", agentID, "task_id", ev.Item.ID, "cap", todoevents.MaxTodos)
				return todoItems(cache.Rows), false, nil
			}
		}
		// Pick the next seq from the in-memory mirror so a fresh insert
		// gets the right ordering without a `MAX(seq)` round-trip. On
		// CONFLICT the existing row's seq is preserved (the UPSERT
		// excludes seq from the SET list), so the choice is harmless
		// when we end up updating in place.
		if err := h.queries.UpsertAgentTodo(ctx, db.UpsertAgentTodoParams{
			AgentID:     agentID,
			RowKey:      ev.Item.ID,
			Seq:         cache.nextSeq,
			TaskID:      ev.Item.ID,
			Content:     merged.Content,
			ActiveForm:  merged.ActiveForm,
			Description: merged.Description,
			Status:      todoevents.StatusWire(merged.Status),
		}); err != nil {
			return nil, false, err
		}
		if existingIdx < 0 {
			cache.Rows = append(cache.Rows, cachedTodo{item: merged, rowKey: ev.Item.ID})
			cache.nextSeq++
		} else {
			cache.Rows[existingIdx].item = merged
		}
		return todoItems(cache.Rows), true, nil

	case todoevents.KindUpdate:
		if ev.ID == "" {
			return nil, false, nil
		}
		idx := todoIndexByID(cache.Rows, ev.ID)
		if idx < 0 {
			return nil, false, nil
		}
		merged := todoevents.ApplyPatch(cache.Rows[idx].item, ev.Patch)
		// No-op patch (every Patch field nil or already-matching): skip
		// the DB write and broadcast.
		if merged == cache.Rows[idx].item {
			return todoItems(cache.Rows), false, nil
		}
		if err := h.queries.UpdateAgentTodo(ctx, db.UpdateAgentTodoParams{
			Content:     merged.Content,
			ActiveForm:  merged.ActiveForm,
			Description: merged.Description,
			Status:      todoevents.StatusWire(merged.Status),
			AgentID:     agentID,
			RowKey:      cache.Rows[idx].rowKey,
		}); err != nil {
			return nil, false, err
		}
		cache.Rows[idx].item = merged
		return todoItems(cache.Rows), true, nil

	case todoevents.KindDelete:
		if ev.ID == "" {
			return nil, false, nil
		}
		idx := todoIndexByID(cache.Rows, ev.ID)
		if idx < 0 {
			return nil, false, nil
		}
		// Soft delete: mark the row deleted instead of removing it.
		// Keeps the tombstone in the broadcast snapshot so the chat
		// thread and sidebar can render a "deleted" visual, and lets
		// the cap-eviction pool include deleted rows.
		if cache.Rows[idx].item.Status == todoevents.StatusDeleted {
			return nil, false, nil
		}
		if err := h.queries.UpdateAgentTodoStatus(ctx, db.UpdateAgentTodoStatusParams{
			Status:  todoevents.StatusWire(todoevents.StatusDeleted),
			AgentID: agentID,
			RowKey:  cache.Rows[idx].rowKey,
		}); err != nil {
			return nil, false, err
		}
		cache.Rows[idx].item.Status = todoevents.StatusDeleted
		return todoItems(cache.Rows), true, nil
	}
	return nil, false, nil
}

// evictOldestFinishedLocked removes the oldest finished row (completed or
// deleted) from the cache and the DB via the shared registryCache mechanics.
// Returns false (with no error) when no finished row exists; the caller drops
// the incoming event in that case. Logs the eviction for operator visibility.
// Caller must hold the cache's mutex.
//
// agent_todos is a SINGLE-pool registry (todoOps sets no bucketOf), so "" names
// its one pool.
func (h *OutputHandler) evictOldestFinishedLocked(ctx context.Context, agentID string, cache *agentTodoCache) (bool, error) {
	// The generic returns the row it deleted, so the log names that row rather
	// than one a second scan here picked out.
	evicted, evictedHappened, err := cache.evictOldestFinishedInBucketLocked(ctx, agentID, "")
	if err != nil {
		return false, err
	}
	if evictedHappened {
		slog.Info("agent_todos cap reached; evicted oldest completed/deleted task",
			"agent_id", agentID, "evicted_task_id", evicted.item.ID, "evicted_status", todoevents.StatusWire(evicted.item.Status), "cap", todoevents.MaxTodos)
	}
	return evictedHappened, nil
}

// applySnapshotLocked replaces every agent_todos row with the supplied
// list, capped at todoevents.MaxTodos. The delete-all + N inserts are
// wrapped in a transaction when h.db is set (production); tests that
// construct the handler with a nil *sql.DB fall through to a
// non-transactional loop.
func (h *OutputHandler) applySnapshotLocked(ctx context.Context, agentID string, cache *agentTodoCache, snapshot []todoevents.Item) ([]todoevents.Item, bool, error) {
	capped := snapshot
	if len(capped) > todoevents.MaxTodos {
		capped = capped[:todoevents.MaxTodos]
	}
	// Re-emitted plans (Codex re-broadcasts on every tick; TaskList
	// polled twice with no change) carry a structurally identical
	// snapshot. Skip the delete-and-insert tx + broadcast on a no-op.
	if todoItemsEqual(cache.Rows, capped) {
		return todoItems(cache.Rows), false, nil
	}
	if err := h.snapshotWriteToDB(ctx, agentID, capped); err != nil {
		return nil, false, err
	}
	cache.Rows = make([]cachedTodo, len(capped))
	for i, it := range capped {
		cache.Rows[i] = cachedTodo{item: it, rowKey: rowKeyFor(it, i+1)}
	}
	cache.nextSeq = int64(len(capped)) + 1
	return todoItems(cache.Rows), true, nil
}

func (h *OutputHandler) snapshotWriteToDB(ctx context.Context, agentID string, capped []todoevents.Item) error {
	if h.db == nil {
		return h.snapshotWriteNonTx(ctx, h.queries, agentID, capped)
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := h.snapshotWriteNonTx(ctx, h.queries.WithTx(tx), agentID, capped); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	return nil
}

func (h *OutputHandler) snapshotWriteNonTx(ctx context.Context, q *db.Queries, agentID string, capped []todoevents.Item) error {
	if err := q.DeleteAllAgentTodos(ctx, agentID); err != nil {
		return fmt.Errorf("delete agent_todos: %w", err)
	}
	for i, it := range capped {
		if err := q.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
			AgentID:     agentID,
			RowKey:      rowKeyFor(it, i+1),
			Seq:         int64(i + 1),
			TaskID:      it.ID,
			Content:     it.Content,
			ActiveForm:  it.ActiveForm,
			Description: it.Description,
			Status:      todoevents.StatusWire(it.Status),
		}); err != nil {
			return fmt.Errorf("insert agent_todo: %w", err)
		}
	}
	return nil
}

// rowKeyFor returns the agent_todos.row_key for a snapshot Item:
// the Item's ID when set (Claude TaskList), else a synthetic
// `snap-<seq>` (TodoWrite / Codex / ACP carry no stable per-row id).
func rowKeyFor(it todoevents.Item, seq int) string {
	if it.ID != "" {
		return it.ID
	}
	return fmt.Sprintf("snap-%d", seq)
}

// itemFromRow projects a persisted agent_todos row into the in-memory
// Item shape. Used by ensureSeededLocked when populating the cache
// from DB.
func itemFromRow(r db.AgentTodo) todoevents.Item {
	return todoevents.Item{
		ID:          r.TaskID,
		Content:     r.Content,
		Status:      todoevents.StatusFromWire(r.Status),
		ActiveForm:  r.ActiveForm,
		Description: r.Description,
	}
}

// todoCache returns the per-agent cache, creating an empty (unseeded)
// one if none exists. Concurrent first-touch callers receive the same
// instance via sync.Map.LoadOrStore; the actual DB seed runs once via
// ensureSeededLocked under the cache's own mutex.
func (h *OutputHandler) todoCache(agentID string) *agentTodoCache {
	if v, ok := h.todos.Load(agentID); ok {
		return v.(*agentTodoCache)
	}
	fresh := &agentTodoCache{ops: h.todoOps()}
	actual, _ := h.todos.LoadOrStore(agentID, fresh)
	return actual.(*agentTodoCache)
}

// todoOps is the registryOps for agent_todos, capturing the handler's queries so
// every cache instance shares the same type-specific behaviour. Mirrors bgTaskOps.
func (h *OutputHandler) todoOps() registryOps[cachedTodo] {
	return registryOps[cachedTodo]{
		// agent_todos is a SINGLE-pool registry, so `bucket` is always "".
		listRows: func(ctx context.Context, ownerID, _ string, limit int32) ([]seedEntry[cachedTodo], error) {
			rows, err := h.queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{
				AgentID: ownerID,
				Limit:   int64(limit),
			})
			if err != nil {
				return nil, fmt.Errorf("list agent_todos: %w", err)
			}
			entries := make([]seedEntry[cachedTodo], len(rows))
			for i, r := range rows {
				entries[i] = seedEntry[cachedTodo]{item: cachedTodo{item: itemFromRow(r), rowKey: r.RowKey}, seq: r.Seq}
			}
			return entries, nil
		},
		keyOf:  func(r cachedTodo) string { return r.rowKey },
		setKey: func(r *cachedTodo, key string) { r.rowKey = key },
		isFinished: func(r cachedTodo) bool {
			return r.item.Status.IsFinished()
		},
		deleteByKey: func(ctx context.Context, ownerID, key string) error {
			_, err := h.queries.DeleteAgentTodoByRowKey(ctx, db.DeleteAgentTodoByRowKeyParams{
				AgentID: ownerID,
				RowKey:  key,
			})
			return err
		},
		// One cap pool (bucketOf nil), so the seed limit is the cap itself.
		cap:   todoevents.MaxTodos,
		label: "agent_todos",
	}
}

// LoadTodos returns the agent's to-do list, seeding the in-memory
// cache from agent_todos on first access. Cold-start RPCs route
// through here so a warm cache returns without a DB read and every
// subsequent caller observes the same authoritative snapshot.
func (h *OutputHandler) LoadTodos(ctx context.Context, agentID string) ([]todoevents.Item, error) {
	cache := h.todoCache(agentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, agentID); err != nil {
		return nil, err
	}
	return todoItems(cache.Rows), nil
}

// todoItems projects the cache's rows into a freshly-allocated slice of the
// items (no row_keys). Used to build the post-mutation broadcast payload.
// Caller must hold the cache's mutex.
func todoItems(rows []cachedTodo) []todoevents.Item {
	out := make([]todoevents.Item, len(rows))
	for i, r := range rows {
		out[i] = r.item
	}
	return out
}

// todoIndexByID returns the row index for the given task id, or -1 when not
// present. Todos are addressed by task id for create/update/delete (the
// registry row_key matches the id for incremental Task* events but diverges to
// a synthetic `snap-N` for snapshot rows), so this lookup is by item.ID rather
// than by the registryCache row_key. Caller must hold the cache's mutex.
func todoIndexByID(rows []cachedTodo, id string) int {
	return slices.IndexFunc(rows, func(r cachedTodo) bool { return r.item.ID == id })
}

// todoItemsEqual reports whether the cache's items are element-wise equal to
// `other`. Used by the snapshot path to short-circuit no-op re-broadcasts.
// Caller must hold the cache's mutex.
func todoItemsEqual(rows []cachedTodo, other []todoevents.Item) bool {
	if len(rows) != len(other) {
		return false
	}
	for i := range rows {
		if rows[i].item != other[i] {
			return false
		}
	}
	return true
}

// persistNotificationThreaded persists a notification message, appending it
// to the current notification thread if one exists. It reports whether the
// notification produced a frontend-visible broadcast (false when a flapping
// notification collapses byte-identically into the existing thread tail and the
// broadcast is skipped).
func (h *OutputHandler) persistNotificationThreaded(agentID string, agentProvider leapmuxv1.AgentProvider, plugin agent.Provider, source leapmuxv1.MessageSource, contentJSON []byte) (bool, error) {
	if h.wakeLock != nil {
		h.wakeLock.RecordActivity()
	}
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		broadcast, err := h.appendToNotificationThread(agentID, agentProvider, plugin, threadRef, source, contentJSON)
		if err == nil {
			return broadcast, nil
		}
		// errSourceMismatch is the documented fall-through signal — start a
		// fresh standalone thread silently. Any other error is a real failure
		// (DB read/write, decompress, marshal); log it but still fall through
		// so the notification reaches users via a new standalone row.
		if !errors.Is(err, errSourceMismatch) {
			slog.Error("append to notification thread failed; creating standalone", "agent_id", agentID, "error", err)
		}
	}

	return h.createNotificationStandalone(agentID, agentProvider, source, contentJSON)
}

// errSourceMismatch is returned by appendToNotificationThread when the
// existing thread's source does not match the incoming notification's.
// It is a normal fall-through signal, not a failure — the caller starts
// a fresh standalone thread.
var errSourceMismatch = errors.New("notification thread source mismatch")

// appendToNotificationThread appends a message to an existing notification thread.
// Returns whether a frontend-visible broadcast was emitted (false when the
// notification collapses byte-identically into the existing tail), and an error
// if the thread's source does not match the new notification's source — adjacent
// cross-source notifications must produce separate threads so that the persisted
// source remains a truthful per-thread provenance signal. The caller treats the
// error as a normal "fall through to a new standalone thread" signal, not as a
// failure.
func (h *OutputHandler) appendToNotificationThread(agentID string, agentProvider leapmuxv1.AgentProvider, plugin agent.Provider, threadRef *notifThreadRef, source leapmuxv1.MessageSource, contentJSON []byte) (bool, error) {
	// Short-circuit cross-source flips before the DB hit — the in-memory
	// threadRef carries the source that was persisted when the thread
	// opened, so we don't need to fetch + decompress the row to learn it.
	if threadRef.source != source {
		return false, errSourceMismatch
	}

	parentRow, err := h.queries.GetMessageByAgentAndID(bgCtx(), db.GetMessageByAgentAndIDParams{
		ID:      threadRef.msgID,
		AgentID: agentID,
	})
	if err != nil {
		return false, err
	}

	parentData, err := msgcodec.Decompress(parentRow.Content, parentRow.ContentCompression)
	if err != nil {
		return false, err
	}

	wrapper, err := unwrapNotifContent(parentData)
	if err != nil {
		return false, err
	}

	// If a flapping ProviderScoped notification (e.g.
	// remoteControl/status/changed) collapses into the existing tail and
	// produces a byte-identical slice, skip the DB write + broadcast. The
	// false return tells the reset decorator no frontend clear fired, so it must
	// not reset the thinking-token estimate for this collapsed notification.
	oldMessages := wrapper.Messages
	nextMessages := append(slices.Clone(oldMessages), contentJSON)
	nextMessages = consolidateNotificationThread(nextMessages, plugin)
	if rawMessageSlicesEqual(oldMessages, nextMessages) {
		return false, nil
	}

	wrapper.Messages = nextMessages
	wrapper.OldSeqs = append(wrapper.OldSeqs, parentRow.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}

	merged, err := json.Marshal(wrapper)
	if err != nil {
		return false, fmt.Errorf("marshal notification thread: %w", err)
	}

	mergedCompressed, mergedCompType := msgcodec.Compress(merged)

	// Re-snapshot active spans at append time. The thread row's seq is
	// bumped to the latest position, so its span_lines must reflect the
	// spans active *now* — not whatever was active when the thread was
	// originally created.
	spanLines := h.snapshotPassthroughSpanLines(agentID)

	newSeq, err := h.queries.UpdateNotificationThread(bgCtx(), db.UpdateNotificationThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		SpanLines:          spanLines,
		ID:                 parentRow.ID,
		AgentID:            agentID,
	})
	if err != nil {
		return false, err
	}

	threadRef.seq = newSeq
	h.lastNotifThread.Store(agentID, threadRef)

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 parentRow.ID,
		Source:             parentRow.Source,
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		Seq:                newSeq,
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt.Time),
		Depth:              0,
		SpanLines:          spanLines,
		// Carry the row's scroll-rail mark so this MOVE broadcast matches what a
		// refetch (messageToProto) would report. UpdateNotificationThread leaves
		// mark_type untouched, so parentRow.MarkType is still authoritative. Today
		// threaded rows are unmarked (0), but a future marked-and-threaded row would
		// otherwise show its dot only after a reload.
		MarkType: parentRow.MarkType,
		// This broadcast is a MOVE: the consolidated thread row jumped from its old
		// seq (parentRow.Seq, read before UpdateNotificationThread) to newSeq. Mark it
		// so consumers reconcile by id instead of treating it as a new message. Only set
		// here -- the persisted row + replays carry no previous_seq (0).
		PreviousSeq: parentRow.Seq,
	})

	return true, nil
}

// createNotificationStandalone creates a new standalone notification message.
// It always broadcasts on success, so it reports broadcast=true.
func (h *OutputHandler) createNotificationStandalone(agentID string, agentProvider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, contentJSON []byte) (bool, error) {
	msgID := id.Generate()
	wrapped := wrapNotifContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := nowMillis()

	// Capture currently-active spans so the notification renders with
	// passthrough vertical bars instead of breaking the column.
	spanLines := h.snapshotPassthroughSpanLines(agentID)

	seq, err := createMessageRow(bgCtx(), h.queries, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Source:             source,
		Content:            compressed,
		ContentCompression: compressionType,
		Depth:              0,
		SpanID:             "",
		ParentSpanID:       "",
		SpanLines:          spanLines,
		SpanColor:          0,
		AgentProvider:      agentProvider,
		CreatedAt:          sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return false, err
	}

	h.lastNotifThread.Store(agentID, &notifThreadRef{
		msgID:  msgID,
		seq:    seq,
		source: source,
	})

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Source:             source,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(now),
		Depth:              0,
		SpanLines:          spanLines,
	})
	return true, nil
}

// broadcastMessage broadcasts a single agent message event to all watchers.
func (h *OutputHandler) broadcastMessage(agentID string, msg *leapmuxv1.AgentChatMessage) {
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_AgentMessage{
			AgentMessage: msg,
		},
	})
}

// broadcastAgentSessionInfo broadcasts ephemeral agent session metadata.
func (h *OutputHandler) broadcastAgentSessionInfo(agentID string, info map[string]interface{}) {
	content := map[string]interface{}{
		"type": agent.NotificationTypeAgentSessionInfo,
		"info": info,
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		slog.Warn("marshal agent session info", "agent_id", agentID, "error", err)
		return
	}
	compressed, compressionType := msgcodec.Compress(contentJSON)
	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 id.Generate(),
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                -1, // Ephemeral sentinel
	})
}

// PersistLeapMuxNotification persists and broadcasts a LEAPMUX notification.
func (h *OutputHandler) PersistLeapMuxNotification(agentID string, agentProvider leapmuxv1.AgentProvider, content map[string]interface{}) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		slog.Warn("marshal notification content", "agent_id", agentID, "error", err)
		return
	}
	if _, err := h.persistNotificationThreaded(agentID, agentProvider, agent.ProviderFor(agentProvider), leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// updatePlan snapshots the agent's prior plan file (if any), writes the new
// content to a fresh canonical path, and emits a `plan_updated` notification
// when the user-visible title or path changed. The on-disk plan file is the
// sole source of truth for content; the agents row only stores the path and
// the most recent title. The canonical path is `<sanitized_title>.md` for
// the first plan with a given title in a month directory, and
// `<sanitized_title>.<n>.md` for subsequent agents that pick the same title.
func (h *OutputHandler) updatePlan(agentID string, compressed []byte, compression leapmuxv1.ContentCompression, title string) {
	agentRow, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for plan update", "agent_id", agentID, "error", err)
		return
	}

	// Decompress the new content. An empty payload after decompression is
	// treated as "no new plan content" — we only fall through to the
	// title/path bookkeeping below when the caller has actual bytes.
	var newContent []byte
	if len(compressed) > 0 {
		decompressed, err := msgcodec.Decompress(compressed, compression)
		if err != nil {
			slog.Warn("failed to decompress plan content", "agent_id", agentID, "error", err)
			return
		}
		newContent = decompressed
	}
	if len(newContent) == 0 {
		return
	}

	// Preserve existing plan_title when the caller's payload had no first
	// line we could extract from. The title comparison below is then a
	// no-op for that field.
	if title == "" {
		title = agentRow.PlanTitle
	}

	now := h.now()

	// Disk-based no-op detection. If the canonical content on disk matches
	// the new payload byte-for-byte and the title is unchanged, there is
	// nothing to snapshot, write, or broadcast — short-circuit before any
	// filesystem mutation.
	if title == agentRow.PlanTitle && agentRow.PlanFilePath != "" {
		if existing, err := os.ReadFile(agentRow.PlanFilePath); err == nil && bytes.Equal(existing, newContent) {
			return
		}
	}

	dir, err := h.resolvePlanDir(agentRow.PlanFilePath, now)
	if err != nil {
		slog.Warn("failed to resolve plan dir", "agent_id", agentID, "error", err)
		return
	}

	// Snapshot whatever the agent's prior plan file is, regardless of
	// title — option (a) of the title-change semantics. The snapshot
	// preserves the prior stem, so historical files retain the title
	// they had when written. Doing this before writePlanFile frees the
	// agent's prior canonical slot for reuse on a same-title rewrite.
	if agentRow.PlanFilePath != "" {
		if _, err := h.snapshotPlanFile(agentRow.PlanFilePath, now); err != nil {
			slog.Warn("failed to snapshot prior plan file", "agent_id", agentID, "prior_path", agentRow.PlanFilePath, "error", err)
		}
	}

	canonicalPath, err := writePlanFile(dir, title, newContent)
	if err != nil {
		slog.Warn("failed to write plan file", "agent_id", agentID, "dir", dir, "title", title, "error", err)
		return
	}

	titleChanged := title != agentRow.PlanTitle
	pathChanged := canonicalPath != agentRow.PlanFilePath
	shouldAutoRename := titleChanged && title != "" &&
		title != agentRow.Title &&
		(agentRow.Title == agentRow.PlanTitle ||
			agentAutoTitlePattern.MatchString(agentRow.Title))

	if shouldAutoRename {
		if err := h.queries.UpdateAgentPlanAndTitle(bgCtx(), db.UpdateAgentPlanAndTitleParams{
			PlanFilePath: canonicalPath,
			PlanTitle:    title,
			Title:        title,
			ID:           agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
			return
		}
	} else if titleChanged || pathChanged {
		if err := h.queries.UpdateAgentPlan(bgCtx(), db.UpdateAgentPlanParams{
			PlanFilePath: canonicalPath,
			PlanTitle:    title,
			ID:           agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
			return
		}
	}

	if !titleChanged && !pathChanged {
		// Path and title unchanged — content differed (we wouldn't be here
		// otherwise), but the user-visible header is the same. The new
		// file is already on disk; no notification needed.
		return
	}

	// A plan with no title — no first line we could extract and no prior
	// plan_title to fall back to — has nothing meaningful to announce. The
	// client renders "Plan updated:" with an empty title as a raw-JSON bubble
	// (plan_updated needs a title to form its label), so a titleless
	// notification is pure noise. The plan file and the agents row are already
	// written above; only the user-facing notification is skipped.
	if title == "" {
		return
	}

	payload := map[string]interface{}{
		"type":           agent.NotificationTypePlanUpdated,
		"plan_title":     title,
		"plan_file_path": canonicalPath,
	}
	if shouldAutoRename {
		payload["update_agent_title"] = true
	}
	h.PersistLeapMuxNotification(agentID, agentRow.AgentProvider, payload)
}

// indexedRaw bundles a message's original index, raw bytes, and (optional)
// classification so downstream sort-and-emit can reconstruct the persisted
// thread in original order. idx == -1 marks an empty slot.
type indexedRaw struct {
	idx  int
	raw  json.RawMessage
	kind agent.NotificationKind
}

// rawMessageSlicesEqual reports whether two slices of raw JSON messages have
// identical bytes at every position. Used to short-circuit no-op writes in
// the notification-thread append path.
func rawMessageSlicesEqual(a, b []json.RawMessage) bool {
	return slices.EqualFunc(a, b, func(x, y json.RawMessage) bool {
		return bytes.Equal(x, y)
	})
}

// consolidateNotificationThread consolidates a notification thread's messages.
// Service-owned LeapMux notification types are merged centrally, while
// provider-owned raw payloads are classified through the injected plugin.
// Ordering is preserved by the last occurrence index of each retained entry.
func consolidateNotificationThread(messages []json.RawMessage, plugin agent.Provider) []json.RawMessage {
	if plugin == nil {
		plugin = agent.ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	}

	type settingsChange struct {
		Old string `json:"old"`
		New string `json:"new"`
	}

	type envelope struct {
		Type    string                    `json:"type"`
		Subtype string                    `json:"subtype"`
		Changes map[string]settingsChange `json:"changes,omitempty"`
		RLInfo  *struct {
			RateLimitType string `json:"rateLimitType"`
		} `json:"rate_limit_info,omitempty"`
	}

	// Last-by-index slots: each holds the most recent occurrence of one
	// notification class. settings is special — its raw payload is rebuilt
	// at emit time from mergedChanges so the persisted entry reflects only
	// the net effective diff across the thread.
	settings := indexedRaw{idx: -1}
	contextCleared := indexedRaw{idx: -1}
	interrupted := indexedRaw{idx: -1}
	planExec := indexedRaw{idx: -1}
	planUpdated := indexedRaw{idx: -1}
	status := indexedRaw{idx: -1}
	apiRetry := indexedRaw{idx: -1}

	mergedChanges := map[string]settingsChange{}

	rateLimitByType := map[string]indexedRaw{}
	providerEntries := map[string]indexedRaw{}

	var keepAll []indexedRaw

	for i, raw := range messages {
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			slog.Warn("consolidate notification unmarshal failed", "error", err)
			keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			continue
		}

		switch env.Type {
		case agent.NotificationTypeSettingsChanged:
			for key, val := range env.Changes {
				if existing, ok := mergedChanges[key]; ok {
					mergedChanges[key] = settingsChange{Old: existing.Old, New: val.New}
				} else {
					mergedChanges[key] = val
				}
			}
			settings.idx = i

		case agent.NotificationTypeContextCleared:
			contextCleared = indexedRaw{idx: i, raw: raw}
			keepAll = slices.DeleteFunc(keepAll, func(ir indexedRaw) bool {
				return ir.kind == agent.NotificationKindCompactionBoundary
			})

		case agent.NotificationTypePlanExecution:
			planExec = indexedRaw{idx: i, raw: raw}

		case agent.NotificationTypePlanUpdated:
			// Multiple plan_updated entries within one notification thread
			// fold to the most recent — same pattern as plan_execution. The
			// frontend extractor already prefers the latest, but keeping
			// only the most recent in the persisted thread also keeps the
			// chat readable when an agent iterates on a plan title.
			planUpdated = indexedRaw{idx: i, raw: raw}

		case agent.NotificationTypeInterrupted:
			interrupted = indexedRaw{idx: i, raw: raw}

		case agent.NotificationTypeRateLimit:
			key := "unknown"
			if env.RLInfo != nil && env.RLInfo.RateLimitType != "" {
				key = env.RLInfo.RateLimitType
			}
			rateLimitByType[key] = indexedRaw{idx: i, raw: raw}

		case agent.NotificationTypeCompacting:
			status = indexedRaw{idx: i, raw: raw, kind: agent.NotificationKindStatus}

		default:
			class := plugin.Classify(raw)
			switch class.Kind {
			case agent.NotificationKindStatus:
				status = indexedRaw{idx: i, raw: raw, kind: class.Kind}
			case agent.NotificationKindAPIRetry:
				apiRetry = indexedRaw{idx: i, raw: raw, kind: class.Kind}
			case agent.NotificationKindCompactionBoundary:
				status = indexedRaw{idx: -1}
				if contextCleared.idx >= 0 && i > contextCleared.idx {
					contextCleared = indexedRaw{idx: -1}
				}
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw, kind: class.Kind})
			case agent.NotificationKindProviderScoped:
				prev, ok := providerEntries[class.Key]
				if ok {
					merged, err := plugin.Merge(class, prev.raw, raw)
					if err != nil {
						slog.Warn("consolidate provider notification merge failed", "key", class.Key, "error", err)
						merged = raw
					}
					providerEntries[class.Key] = indexedRaw{idx: i, raw: merged, kind: class.Kind}
				} else {
					providerEntries[class.Key] = indexedRaw{idx: i, raw: raw, kind: class.Kind}
				}
			default:
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			}
		}
	}

	var entries []indexedRaw

	// Settings is rebuilt at emit time so the persisted payload reflects only
	// effective net changes; intermediate flips that cancel out are dropped.
	if settings.idx >= 0 {
		effective := map[string]settingsChange{}
		for key, val := range mergedChanges {
			if val.Old != val.New {
				effective[key] = val
			}
		}
		if len(effective) > 0 {
			entry := map[string]interface{}{
				"type":    agent.NotificationTypeSettingsChanged,
				"changes": effective,
			}
			if data, err := json.Marshal(entry); err == nil {
				entries = append(entries, indexedRaw{idx: settings.idx, raw: data})
			}
		}
	}

	for _, slot := range []indexedRaw{contextCleared, planExec, planUpdated, interrupted, status, apiRetry} {
		if slot.idx >= 0 {
			entries = append(entries, slot)
		}
	}

	for _, rateLimit := range rateLimitByType {
		entries = append(entries, rateLimit)
	}

	for _, providerEntry := range providerEntries {
		entries = append(entries, providerEntry)
	}

	entries = append(entries, keepAll...)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].idx < entries[j].idx
	})

	result := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.raw)
	}

	if len(result) == 0 {
		return []json.RawMessage{}
	}

	return result
}
