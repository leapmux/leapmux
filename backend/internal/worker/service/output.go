// Package service output provides agent output persistence and broadcasting.
// It implements the agent.OutputSink interface, backing the generic primitives
// with DB queries, notification threading, and WatcherManager fan-out.
package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
)

// --- Span Tracker ---

// ActiveSpan tracks a single open subagent span.
type ActiveSpan struct {
	SpanID     string
	ColorIndex int
	Column     int
}

// SpanLineType describes how the frontend should render a span line column.
type SpanLineType string

const (
	SpanLineActive            SpanLineType = "active"             // Vertical line only.
	SpanLineConnector         SpanLineType = "connector"          // Vertical + horizontal branch to the message (├).
	SpanLineConnectorEnd      SpanLineType = "connector_end"      // Bottom-corner + horizontal branch (└), span closes after this.
	SpanLinePassthrough       SpanLineType = "passthrough"        // Horizontal line only (empty slot after connector).
	SpanLineActivePassthrough SpanLineType = "active_passthrough" // Vertical + horizontal passthrough.
)

// SpanLine represents a single span line entry in the JSON array.
type SpanLine struct {
	SpanID           string       `json:"span_id"`
	Color            int          `json:"color"`
	Type             SpanLineType `json:"type"`
	PassthroughColor int          `json:"passthrough_color,omitempty"`
}

// spanPaletteSize is the number of colors in the frontend span palette.
// Color indices are 1-based and wrap around within [1, spanPaletteSize].
const spanPaletteSize = 8

// pendingSpan holds the color reserved for a span by ReserveSpanColor that
// hasn't yet been committed by OpenSpan. Treated as "in use" by chooseColor
// so a back-to-back reservation cannot pick the same color.
type pendingSpan struct {
	spanID string
	color  int
}

// SpanTracker manages hierarchical span state for an agent's message threading.
type SpanTracker struct {
	mu        sync.Mutex
	spans     []ActiveSpan
	spanTypes map[string]string // spanID → span type (tool name / item type)
	parentMap map[string]string // spanID → parentSpanID (persists after close for ancestry lookups)
	pending   *pendingSpan      // color reserved by ReserveSpanColor; consumed by matching OpenSpan.
	rng       *rand.Rand        // lazy-initialized random source for color choice; tests inject directly.
	deck      []int             // shuffled draw pile for primary-rule color selection; refilled when empty.
}

// randIntn returns a random integer in [0, n) from the tracker's RNG, lazy
// initializing it on first use. Must be called with t.mu held.
func (t *SpanTracker) randIntn(n int) int {
	t.ensureRNG()
	return t.rng.IntN(n)
}

func (t *SpanTracker) ensureRNG() {
	if t.rng == nil {
		t.rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
}

// refillDeck repopulates the draw pile with a freshly shuffled palette.
// Must be called with t.mu held.
func (t *SpanTracker) refillDeck() {
	t.ensureRNG()
	if cap(t.deck) < spanPaletteSize {
		t.deck = make([]int, spanPaletteSize)
	} else {
		t.deck = t.deck[:spanPaletteSize]
	}
	for i := 0; i < spanPaletteSize; i++ {
		t.deck[i] = i + 1
	}
	t.rng.Shuffle(len(t.deck), func(i, j int) {
		t.deck[i], t.deck[j] = t.deck[j], t.deck[i]
	})
}

// drawFromDeck returns the first color in the deck that is not blocked,
// popping it from the pile. If no remaining card is eligible, the deck is
// refilled and the search retried — guaranteeing every "round" of 8 picks
// without external blocking constraints visits all 8 palette colors before
// any repeats. Caller is expected to ensure at least one of
// {1..spanPaletteSize} is not blocked; if every color is blocked anyway,
// returns a uniformly random palette color so output keeps flowing.
// Must be called with t.mu held.
func (t *SpanTracker) drawFromDeck(blocked map[int]bool) int {
	if len(t.deck) == 0 {
		t.refillDeck()
	}
	for i, c := range t.deck {
		if !blocked[c] {
			t.deck = slices.Delete(t.deck, i, i+1)
			return c
		}
	}
	// Every remaining card is blocked. Refill once: with a fresh shuffled
	// deck of all 8 cards, an unblocked color will be found unless the
	// caller has every palette color blocked.
	t.refillDeck()
	for i, c := range t.deck {
		if !blocked[c] {
			t.deck = slices.Delete(t.deck, i, i+1)
			return c
		}
	}
	// Defensive: caller is supposed to fall back to chooseColor's saturated
	// branch when every color is in use. If we end up here anyway, pick a
	// random palette color so output keeps flowing rather than crashing.
	slog.Warn("span color deck exhausted with all colors blocked; using random fallback")
	return 1 + t.randIntn(spanPaletteSize)
}

// resolveColumn computes the column index a new span with the given parent
// would receive if opened now. Mirrors the logic in OpenSpan so that
// ReserveSpanColor can pre-compute adjacency at peek time without committing
// any state. Must be called with t.mu held.
func (t *SpanTracker) resolveColumn(parentSpanID string) int {
	// Single pass: find parent column, build used-column set, and track the
	// rightmost active column for minCol computation below.
	parentCol := -1
	maxCol := -1
	used := make(map[int]bool, len(t.spans))
	for _, s := range t.spans {
		used[s.Column] = true
		if s.Column > maxCol {
			maxCol = s.Column
		}
		if s.SpanID == parentSpanID {
			parentCol = s.Column
		}
	}

	// Find the minimum starting column. When a parent is known, place the
	// new child to the right of all active spans that are to the right of
	// the parent so it doesn't reuse a column freed by a closed span,
	// which would place the connector_end at a position with no preceding
	// vertical line. Root-level spans opened while other spans are active
	// append to the right of the current active set instead of reusing a
	// left gap, keeping connector_end rendering aligned.
	minCol := parentCol + 1
	if parentCol >= 0 {
		if maxCol >= parentCol {
			minCol = maxCol + 1
		}
	} else if len(t.spans) > 0 {
		minCol = maxCol + 1
	}

	// Find first free column starting from minCol.
	for i := minCol; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// chooseColor picks a color for a new span using the two-tier rule:
//
//  1. Primary: draw the next eligible color from a shuffled deck of the
//     full palette, skipping any deck card currently in use by an active
//     span or pending reservation. The deck is refilled (reshuffled) when
//     emptied, so any 8-pick window with no blocking constraints visits
//     every palette color exactly once before any repeats.
//  2. Fallback (only when every palette color is in use): pick uniformly
//     at random from colors that are not the parent's, not the
//     column-immediately-left active span's, and not the
//     column-immediately-right active span's.
//
// Must be called with t.mu held.
func (t *SpanTracker) chooseColor(parentSpanID string, newColumn int) int {
	inUse := make(map[int]bool, len(t.spans)+1)
	allInUse := true
	for _, s := range t.spans {
		inUse[s.ColorIndex] = true
	}
	if t.pending != nil {
		inUse[t.pending.color] = true
	}
	for c := 1; c <= spanPaletteSize; c++ {
		if !inUse[c] {
			allInUse = false
			break
		}
	}
	if !allInUse {
		return t.drawFromDeck(inUse)
	}

	// Saturated palette: relax exclusion to parent + adjacents only.
	parentColor := 0
	leftCol := -1
	leftColor := 0
	rightCol := -1
	rightColor := 0
	for _, s := range t.spans {
		if s.SpanID == parentSpanID {
			parentColor = s.ColorIndex
		}
		if s.Column < newColumn && s.Column > leftCol {
			leftCol = s.Column
			leftColor = s.ColorIndex
		}
		if s.Column > newColumn && (rightCol == -1 || s.Column < rightCol) {
			rightCol = s.Column
			rightColor = s.ColorIndex
		}
	}

	excluded := make(map[int]bool, 3)
	if parentColor != 0 {
		excluded[parentColor] = true
	}
	if leftColor != 0 {
		excluded[leftColor] = true
	}
	if rightColor != 0 {
		excluded[rightColor] = true
	}

	candidates := make([]int, 0, spanPaletteSize)
	for c := 1; c <= spanPaletteSize; c++ {
		if !excluded[c] {
			candidates = append(candidates, c)
		}
	}
	// At most 3 exclusions vs 8 colors, so candidates is always non-empty.
	return candidates[t.randIntn(len(candidates))]
}

// OpenSpan registers a new subagent span.
func (t *SpanTracker) OpenSpan(spanID, parentSpanID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Record parentage (persists after close for ancestry lookups).
	if t.parentMap == nil {
		t.parentMap = make(map[string]string)
	}
	t.parentMap[spanID] = parentSpanID

	column := t.resolveColumn(parentSpanID)

	// Honor a pending reservation for this exact span so the persisted
	// span_color matches the rendered span color.
	var color int
	if t.pending != nil && t.pending.spanID == spanID {
		color = t.pending.color
		t.pending = nil
	} else {
		color = t.chooseColor(parentSpanID, column)
	}

	t.spans = append(t.spans, ActiveSpan{
		SpanID:     spanID,
		ColorIndex: color,
		Column:     column,
	})
}

// depthOf returns the nesting depth for a span by walking the parentMap.
// Returns 0 for unknown or root-level ("") spans. Must be called with t.mu held.
func (t *SpanTracker) depthOf(spanID string) int {
	depth := 0
	current := spanID
	for current != "" {
		depth++
		current = t.parentMap[current]
	}
	return depth
}

// isDescendantOf reports whether spanID is a descendant of ancestorSpanID
// by walking the parentMap. Must be called with t.mu held.
func (t *SpanTracker) isDescendantOf(spanID, ancestorSpanID string) bool {
	current := spanID
	for current != "" {
		parent := t.parentMap[current]
		if parent == ancestorSpanID {
			return true
		}
		current = parent
	}
	return false
}

// Reset clears all span tracking state, returning the tracker to its
// initial empty state. Used when the agent's context is cleared or interrupted.
func (t *SpanTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.spans = nil
	t.pending = nil
	t.deck = t.deck[:0]
	clear(t.spanTypes)
	clear(t.parentMap)
}

// CloseSpan removes a span, freeing its column.
func (t *SpanTracker) CloseSpan(spanID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.spans = slices.DeleteFunc(t.spans, func(s ActiveSpan) bool {
		return s.SpanID == spanID
	})
	if t.spanTypes != nil {
		delete(t.spanTypes, spanID)
	}
	// Defensive: if a reservation for this exact span was never consumed by
	// OpenSpan, drop it so the color goes back to Free.
	if t.pending != nil && t.pending.spanID == spanID {
		t.pending = nil
	}
	if len(t.spans) == 0 {
		clear(t.parentMap)
	}
}

// SetSpanType records the type (tool name / item type) for a span ID.
func (t *SpanTracker) SetSpanType(spanID, spanType string) {
	if spanID == "" || spanType == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.spanTypes == nil {
		t.spanTypes = make(map[string]string)
	}
	t.spanTypes[spanID] = spanType
}

// GetSpanType returns the stored type for a span ID, or "".
func (t *SpanTracker) GetSpanType(spanID string) string {
	if spanID == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spanTypes[spanID]
}

// ReserveSpanColor commits to the color that the next OpenSpan(spanID,
// parentSpanID) call will receive, so the caller can persist the color into
// a message before the span itself opens. The reservation is held on the
// tracker's pending slot and consumed when OpenSpan is called for the same
// spanID. Subsequent calls with the same spanID are idempotent and return
// the cached color; calls with a different spanID overwrite the slot.
//
// Safe to call only when output processing is sequential per agent (which
// it is for all Claude/Codex/ACP handlers).
func (t *SpanTracker) ReserveSpanColor(spanID, parentSpanID string) int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != nil && t.pending.spanID == spanID {
		return int32(t.pending.color)
	}
	column := t.resolveColumn(parentSpanID)
	color := t.chooseColor(parentSpanID, column)
	t.pending = &pendingSpan{spanID: spanID, color: color}
	return int32(color)
}

// Snapshot returns the depth and span lines for a given parentSpanID in a single
// atomic operation. connectorSpanID identifies the span this message connects to
// (used to compute passthrough hints for columns to the right of the connector).
// When closing is true, the connector column renders as └ instead of ├.
// This avoids the TOCTOU risk of calling DepthFor and SpanLines separately,
// and reduces mutex acquisitions.
func (t *SpanTracker) Snapshot(parentSpanID, connectorSpanID string, closing bool) (depth int32, spanLines string, connectorColorOut int32) {
	connectorColorOut = 0 // no connector found
	t.mu.Lock()
	defer t.mu.Unlock()

	// Span lines serialization.
	if len(t.spans) == 0 {
		// Depth lookup (no spans to search).
		return depth, "[]", connectorColorOut
	}

	// Depth lookup via parent chain; single pass for maxCol.
	if parentSpanID != "" {
		depth = int32(t.depthOf(parentSpanID))
	}
	maxCol := 0
	for _, s := range t.spans {
		if s.Column > maxCol {
			maxCol = s.Column
		}
	}

	lines := make([]*SpanLine, maxCol+1)
	for _, s := range t.spans {
		lines[s.Column] = &SpanLine{
			SpanID: s.SpanID,
			Color:  s.ColorIndex,
			Type:   SpanLineActive,
		}
	}

	// Find the connector column and apply rendering hints.
	connectorCol := -1
	connectorColor := 0
	if connectorSpanID != "" {
		for col, l := range lines {
			if l != nil && l.SpanID == connectorSpanID {
				connectorCol = col
				connectorColor = l.Color
				connectorColorOut = int32(l.Color)
				if closing {
					l.Type = SpanLineConnectorEnd
				} else {
					l.Type = SpanLineConnector
				}
				break
			}
		}
	}

	// Mark columns to the right of the connector as passthrough.
	if connectorCol >= 0 {
		for col := connectorCol + 1; col < len(lines); col++ {
			if lines[col] == nil {
				lines[col] = &SpanLine{
					Type:             SpanLinePassthrough,
					PassthroughColor: connectorColor,
				}
			} else {
				lines[col].Type = SpanLineActivePassthrough
				lines[col].PassthroughColor = connectorColor
			}
		}
	}

	data, err := json.Marshal(lines)
	if err != nil {
		slog.Warn("marshal span lines", "error", err)
		return depth, "[]", connectorColorOut
	}
	return depth, string(data), connectorColorOut
}

// ShouldBroadcastStreamChunk reports whether a live stream chunk should be
// broadcast. To keep the live UI uncluttered, all live deltas are suppressed
// whenever any span is active.
func (t *SpanTracker) ShouldBroadcastStreamChunk() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.spans) == 0
}

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
const notifThreadWrapperType = "notification_thread"

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

// OutputHandler manages agent output persistence and broadcasting.
// It holds shared state accessed by per-agent OutputSink instances.
type OutputHandler struct {
	queries *db.Queries
	watcher *WatcherManager
	agents  *agent.Manager
	DataDir string

	// Per-agent notification threading state (concurrent access).
	notifMu         sync.Map // agentID -> *sync.Mutex
	lastNotifThread sync.Map // agentID -> *notifThreadRef

	// Per-agent span tracking (concurrent access).
	spanTrackers sync.Map // agentID -> *SpanTracker

	// Plan mode tool_use tracking (shared across agents).
	planModeToolUse sync.Map // tool_use_id -> target mode string ("plan" or "default")

	// Auto-continue timers keyed by agent_id + reason.
	autoContinue sync.Map // scheduleKey -> *autoContinueTimerState

	// sendMessageFunc is called by auto-continue to inject a synthetic
	// user message. Set via SetSendMessageFunc during service Init.
	sendMessageFunc func(agentID, content string)

	// wakeLock prevents system sleep while there is agent/terminal activity.
	wakeLock *wakelock.ActivityTracker

	now func() time.Time
}

// NewOutputHandler creates a new OutputHandler.
func NewOutputHandler(queries *db.Queries, watcher *WatcherManager, agents *agent.Manager, wl *wakelock.ActivityTracker) *OutputHandler {
	return &OutputHandler{
		queries:  queries,
		watcher:  watcher,
		agents:   agents,
		wakeLock: wl,
		now:      time.Now,
	}
}

// ResetSpanTracker resets the span tracker for the given agent, clearing all
// active spans. Used when the agent's context is cleared or plan execution restarts.
func (h *OutputHandler) ResetSpanTracker(agentID string) {
	if v, ok := h.spanTrackers.Load(agentID); ok {
		v.(*SpanTracker).Reset()
	}
}

// SetSendMessageFunc sets the callback used by auto-continue to inject
// a synthetic user message into an agent. Must be called before any
// agent output is processed.
func (h *OutputHandler) SetSendMessageFunc(fn func(agentID, content string)) {
	h.sendMessageFunc = fn
}

// CleanupAgent removes all per-agent state from the handler's maps.
// Call this when an agent is permanently closed.
func (h *OutputHandler) CleanupAgent(agentID string) {
	h.notifMu.Delete(agentID)
	h.lastNotifThread.Delete(agentID)
	h.spanTrackers.Delete(agentID)
	h.cleanupAutoContinue(agentID)
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

// ClearAgentRuntimeState removes every piece of state tied to a dying
// agent process: pending control_requests in the DB plus the in-memory
// trackers cleared by CleanupAgent. A controlCancel is broadcast for
// each deleted row so live tabs drop the prompt without waiting for the
// reconnect-and-replay path.
func (h *OutputHandler) ClearAgentRuntimeState(agentID string) {
	deletedIDs, err := h.queries.DeleteControlRequestsByAgentID(bgCtx(), agentID)
	if err != nil {
		slog.Error("clear runtime state: delete control requests", "agent_id", agentID, "error", err)
	}
	for _, requestID := range deletedIDs {
		h.broadcastControlCancel(agentID, requestID)
	}
	h.CleanupAgent(agentID)
}

// spanTracker returns the per-agent SpanTracker, creating one if needed.
func (h *OutputHandler) spanTracker(agentID string) *SpanTracker {
	if v, ok := h.spanTrackers.Load(agentID); ok {
		return v.(*SpanTracker)
	}
	v, _ := h.spanTrackers.LoadOrStore(agentID, &SpanTracker{})
	return v.(*SpanTracker)
}

// NewSink creates a per-agent OutputSink backed by this OutputHandler.
func (h *OutputHandler) NewSink(agentID string, agentProvider leapmuxv1.AgentProvider) agent.OutputSink {
	return &agentOutputSink{
		h:             h,
		agentID:       agentID,
		agentProvider: agentProvider,
		plugin:        agent.ProviderFor(agentProvider),
		tracker:       h.spanTracker(agentID),
	}
}

// agentOutputSink implements agent.OutputSink for a single agent.
type agentOutputSink struct {
	h             *OutputHandler
	agentID       string
	agentProvider leapmuxv1.AgentProvider
	plugin        agent.Provider
	tracker       *SpanTracker

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
}

// --- OutputSink interface implementation ---

func (s *agentOutputSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	return s.h.persistAndBroadcast(s.agentID, s.agentProvider, source, content, span, s.tracker)
}

// PersistTurnEnd persists the universal turn-end divider envelope and
// fires the git-status auto-broadcast. Each provider's terminal
// envelope (Claude type:"result", Codex turn/completed, ACP prompt
// response, Pi agent_end) routes here, so the side effect is explicit
// at the call site. Runs BroadcastGitStatus on a goroutine so the
// agent's stdout-read loop is not blocked by the git subprocesses plus
// the DB lookup.
func (s *agentOutputSink) PersistTurnEnd(content []byte, span agent.SpanInfo) error {
	if err := s.h.persistAndBroadcast(s.agentID, s.agentProvider, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span, s.tracker); err != nil {
		return err
	}
	go s.BroadcastGitStatus()
	return nil
}

func (s *agentOutputSink) PersistNotification(source leapmuxv1.MessageSource, content []byte) error {
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
	if !s.tracker.ShouldBroadcastStreamChunk() {
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

func (s *agentOutputSink) PersistControlRequest(requestID string, payload []byte) {
	if err := s.h.queries.CreateControlRequest(bgCtx(), db.CreateControlRequestParams{
		AgentID:   s.agentID,
		RequestID: requestID,
		Payload:   payload,
	}); err != nil {
		slog.Error("persist control request", "agent_id", s.agentID, "request_id", requestID, "error", err)
	}
}

func (s *agentOutputSink) DeleteControlRequest(requestID string) {
	_ = s.h.queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   s.agentID,
		RequestID: requestID,
	})
}

func (s *agentOutputSink) BroadcastControlRequest(requestID string, payload []byte) {
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_ControlRequest{
			ControlRequest: &leapmuxv1.AgentControlRequest{
				AgentId:       s.agentID,
				RequestId:     requestID,
				Payload:       payload,
				AgentProvider: s.agentProvider,
			},
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

// buildStatusChange constructs an AgentStatusChange from the given DB agent
// and overrides.  Fields that are always the same across callers (agentID,
// workerOnline, agentProvider, gitStatus) are filled in
// automatically.
func (s *agentOutputSink) buildStatusChange(
	dbAgent db.Agent,
	status leapmuxv1.AgentStatus,
	sessionID, permissionMode string,
) *leapmuxv1.AgentStatusChange {
	return &leapmuxv1.AgentStatusChange{
		AgentId:               s.agentID,
		Status:                status,
		AgentSessionId:        sessionID,
		WorkerOnline:          true,
		PermissionMode:        permissionMode,
		Model:                 modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:                dbAgent.Effort,
		GitStatus:             gitutil.GetGitStatus(bgCtx(), dbAgent.WorkingDir),
		AgentProvider:         s.agentProvider,
		ExtraSettings:         loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider),
		AvailableModels:       s.h.agents.AvailableModels(s.agentID, dbAgent.AgentProvider),
		AvailableOptionGroups: s.h.agents.AvailableOptionGroups(s.agentID, dbAgent.AgentProvider),
	}
}

func (s *agentOutputSink) UpdatePermissionMode(mode string) {
	dbAgent, fetchErr := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	oldMode := ""
	if fetchErr == nil {
		oldMode = dbAgent.PermissionMode
	}

	_ = s.h.queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
		PermissionMode: mode,
		ID:             s.agentID,
	})

	// Broadcast statusChange so frontends update their permission mode display.
	if fetchErr == nil {
		sc := s.buildStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, dbAgent.AgentSessionID, mode)
		s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
			AgentId: s.agentID,
			Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
		})
	}

	// Broadcast settings_changed notification for the chat view.
	if oldMode != "" && oldMode != mode {
		s.PersistLeapMuxNotification(map[string]interface{}{
			"type": agent.NotificationTypeSettingsChanged,
			"changes": map[string]interface{}{
				agent.OptionGroupKeyPermissionMode: map[string]string{"old": oldMode, "new": mode},
			},
		})
	}
}

func (s *agentOutputSink) PersistSettingsRefresh(model, effort, permissionMode string, extraSettings map[string]string) {
	dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for settings broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}

	// Skip the DB write and the watcher broadcast when the refresh is a
	// no-op. Refresh fires after UpdateSettings (which has already
	// persisted the same values) and after startup-time readbacks that
	// often just confirm the stored row, so avoiding redundant
	// UpdateAgentAllSettings calls and StatusChange events spares
	// pointless DB churn and frontend reactivity ticks.
	newExtras := marshalExtraSettings(extraSettings)
	if dbAgent.Model == model &&
		dbAgent.Effort == effort &&
		dbAgent.PermissionMode == permissionMode &&
		dbAgent.ExtraSettings == newExtras {
		return
	}

	if err := s.h.queries.UpdateAgentAllSettings(bgCtx(), db.UpdateAgentAllSettingsParams{
		Model:          model,
		Effort:         effort,
		PermissionMode: permissionMode,
		ExtraSettings:  newExtras,
		ID:             s.agentID,
	}); err != nil {
		slog.Error("failed to persist refreshed settings",
			"agent_id", s.agentID, "error", err)
		return
	}

	// Patch the fetched row in-memory to reflect the values we just
	// persisted, avoiding a second GetAgentByID round-trip before we build
	// the status-change event below.
	dbAgent.Model = model
	dbAgent.Effort = effort
	dbAgent.ExtraSettings = newExtras

	sc := s.buildStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, dbAgent.AgentSessionID, permissionMode)
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

func (s *agentOutputSink) BroadcastStatusActive(sessionID string) {
	existingAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for status broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}

	sc := s.buildStatusChange(existingAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sessionID, existingAgent.PermissionMode)
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// BroadcastGitStatus emits a partial AgentStatusChange carrying only the
// agent id and a freshly-computed git status. Auto-fired by
// PersistTurnEnd so the working-tree view stays in sync without
// provider involvement; providers do not call this directly.
//
// The frontend's statusChange handler treats events whose Status is
// UNSPECIFIED as partial updates and applies only the populated fields,
// so this avoids re-shipping the full catalog/settings payload that
// BroadcastStatusActive carries.
func (s *agentOutputSink) BroadcastGitStatus() {
	dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for git status broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}
	sc := &leapmuxv1.AgentStatusChange{
		AgentId:   s.agentID,
		GitStatus: gitutil.GetGitStatus(bgCtx(), dbAgent.WorkingDir),
	}
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

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

// persistAndBroadcast persists a message and broadcasts it to watchers.
// tracker may be nil, in which case it is resolved from the agentID.
func (h *OutputHandler) persistAndBroadcast(agentID string, agentProvider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, contentJSON []byte, span agent.SpanInfo, tracker *SpanTracker) error {
	if h.wakeLock != nil {
		h.wakeLock.RecordActivity()
	}
	if tracker == nil {
		tracker = h.spanTracker(agentID)
	}
	connectorSpanID := resolveConnectorSpanID(span.SpanID, span.ConnectorSpanID, span.ParentSpanID, span.Closing)
	depth, spanLines, connectorColor := tracker.Snapshot(span.ParentSpanID, connectorSpanID, span.Closing)

	// Resolve span color: if the span is already active (e.g. tool_result
	// inside an open span), use the connector color from the snapshot.
	spanColor := span.SpanColor
	if span.SpanID != "" && spanColor == 0 && connectorColor > 0 {
		spanColor = connectorColor
	}

	msgID := id.Generate()
	compressed, compressionType := msgcodec.Compress(contentJSON)
	now := time.Now()

	seq, err := h.queries.CreateMessage(bgCtx(), db.CreateMessageParams{
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
		CreatedAt:          now,
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
	})
	return nil
}

// persistNotificationThreaded persists a notification message, appending it
// to the current notification thread if one exists.
func (h *OutputHandler) persistNotificationThreaded(agentID string, agentProvider leapmuxv1.AgentProvider, plugin agent.Provider, source leapmuxv1.MessageSource, contentJSON []byte) error {
	if h.wakeLock != nil {
		h.wakeLock.RecordActivity()
	}
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		err := h.appendToNotificationThread(agentID, agentProvider, plugin, threadRef, source, contentJSON)
		if err == nil {
			return nil
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
// Returns an error if the thread's source does not match the new
// notification's source — adjacent cross-source notifications must
// produce separate threads so that the persisted source remains a
// truthful per-thread provenance signal. The caller treats the error
// as a normal "fall through to a new standalone thread" signal, not as
// a failure.
func (h *OutputHandler) appendToNotificationThread(agentID string, agentProvider leapmuxv1.AgentProvider, plugin agent.Provider, threadRef *notifThreadRef, source leapmuxv1.MessageSource, contentJSON []byte) error {
	// Short-circuit cross-source flips before the DB hit — the in-memory
	// threadRef carries the source that was persisted when the thread
	// opened, so we don't need to fetch + decompress the row to learn it.
	if threadRef.source != source {
		return errSourceMismatch
	}

	parentRow, err := h.queries.GetMessageByAgentAndID(bgCtx(), db.GetMessageByAgentAndIDParams{
		ID:      threadRef.msgID,
		AgentID: agentID,
	})
	if err != nil {
		return err
	}

	parentData, err := msgcodec.Decompress(parentRow.Content, parentRow.ContentCompression)
	if err != nil {
		return err
	}

	wrapper, err := unwrapNotifContent(parentData)
	if err != nil {
		return err
	}

	// If a flapping ProviderScoped notification (e.g.
	// remoteControl/status/changed) collapses into the existing tail and
	// produces a byte-identical slice, skip the DB write + broadcast.
	oldMessages := wrapper.Messages
	nextMessages := append(slices.Clone(oldMessages), contentJSON)
	nextMessages = consolidateNotificationThread(nextMessages, plugin)
	if rawMessageSlicesEqual(oldMessages, nextMessages) {
		return nil
	}

	wrapper.Messages = nextMessages
	wrapper.OldSeqs = append(wrapper.OldSeqs, parentRow.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}

	merged, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("marshal notification thread: %w", err)
	}

	mergedCompressed, mergedCompType := msgcodec.Compress(merged)
	newSeq, err := h.queries.UpdateNotificationThread(bgCtx(), db.UpdateNotificationThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		ID:                 parentRow.ID,
		AgentID:            agentID,
	})
	if err != nil {
		return err
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
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
	})

	return nil
}

// createNotificationStandalone creates a new standalone notification message.
func (h *OutputHandler) createNotificationStandalone(agentID string, agentProvider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, contentJSON []byte) error {
	msgID := id.Generate()
	wrapped := wrapNotifContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := h.queries.CreateMessage(bgCtx(), db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Source:             source,
		Content:            compressed,
		ContentCompression: compressionType,
		Depth:              0,
		SpanID:             "",
		ParentSpanID:       "",
		SpanLines:          "[]",
		SpanColor:          0,
		AgentProvider:      agentProvider,
		CreatedAt:          now,
	})
	if err != nil {
		return err
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
	})
	return nil
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
	if err := h.persistNotificationThreaded(agentID, agentProvider, agent.ProviderFor(agentProvider), leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// updatePlan archives the agent's prior plan file (if any), writes the new
// content to a fresh canonical path, and emits a `plan_updated` notification
// when the user-visible title or path changed. The on-disk plan file is the
// sole source of truth for content; the agents row only stores the path and
// the most recent title. The canonical path is always derived from
// `<sanitized_title>.<agent_id>.md`.
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
	// nothing to archive, write, or broadcast — short-circuit before any
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
	canonicalPath := filepath.Join(dir, planFilename(title, agentID))

	// Archive whatever the agent's prior plan file is, regardless of
	// title — option (a) of the title-change semantics. The archive
	// preserves the prior name + agent id, so historical files retain
	// the title they had when written.
	if agentRow.PlanFilePath != "" {
		if _, err := h.archivePlanFile(agentRow.PlanFilePath, now); err != nil {
			slog.Warn("failed to archive prior plan file", "agent_id", agentID, "prior_path", agentRow.PlanFilePath, "error", err)
		}
	}

	if err := writePlanFile(canonicalPath, newContent); err != nil {
		slog.Warn("failed to write plan file", "agent_id", agentID, "path", canonicalPath, "error", err)
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
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
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
