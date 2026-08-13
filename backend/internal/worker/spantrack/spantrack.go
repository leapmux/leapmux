// Package spantrack owns the span engine behind the chat transcript's rails:
// which spans are open, which column and palette color each one holds, and the
// per-row span_lines the frontend renders from.
//
// It lives in its own package so both the service layer (which drives it) and
// the agent package's tests (which must behave like it) can import it. The
// agent package cannot import service -- service imports agent -- so a test
// double there used to re-implement this engine, and drifted from it.
package spantrack

import (
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
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

// removeSpanLocked drops a span from the active set, freeing its column and
// its color. The recorded span type is NOT touched: only Reset clears types.
// Must be called with t.mu held.
func (t *SpanTracker) removeSpanLocked(spanID string) {
	t.spans = slices.DeleteFunc(t.spans, func(s ActiveSpan) bool {
		return s.SpanID == spanID
	})
	// Defensive: if a reservation for this exact span was never consumed by
	// OpenSpan, drop it so the color goes back to Free.
	if t.pending != nil && t.pending.spanID == spanID {
		t.pending = nil
	}
	if len(t.spans) == 0 {
		clear(t.parentMap)
	}
}

// CloseSpan removes a span, freeing its column and its color.
//
// The recorded span type SURVIVES, and Reset clears it at the turn boundary.
// Every reader wants the type after the span leaves the active set: a provider's
// closing message reads it back through GetSpanType to persist span_type, and an
// ACP history replay re-delivers that closing message long afterwards. Deleting
// it here degraded the replayed row to the provider's fallback type, and it
// forced a second removal method for the callers that must keep it.
//
// A caller that takes a span back while its tool call KEEPS RUNNING -- a spawn
// recognized late -- calls this too. The column is freed either way; nothing
// downstream distinguishes "ended" from "given back".
func (t *SpanTracker) CloseSpan(spanID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.removeSpanLocked(spanID)
}

// ActiveSpans returns the spans currently open, ordered by column. The service
// layer derives a persisted row's span_lines from this state, so a test double
// that must mirror the real geometry reads it here instead of keeping its own.
func (t *SpanTracker) ActiveSpans() []ActiveSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) == 0 {
		return nil
	}
	out := append([]ActiveSpan(nil), t.spans...)
	slices.SortFunc(out, func(a, b ActiveSpan) int { return a.Column - b.Column })
	return out
}

// ParentOf returns the parent recorded for a span, or "" when it is a root span
// or unknown. Parentage outlives a close until the active set empties.
func (t *SpanTracker) ParentOf(spanID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parentMap[spanID]
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
// broadcast. spanID is the span the chunk belongs to, or "" for the agent's
// free-form text.
//
// A chunk that belongs to an ACTIVE span is its own tool's live output, and the
// frontend routes it to that tool's card, so it always goes. Testing only
// "is any span open" suppressed every one of them: a tool's output delta is
// emitted while that tool's span is open by construction, so the whole
// per-tool live stream never reached the UI for any provider.
//
// Free-form text (spanID "") stays suppressed while any span is active, which
// is what the suppression was written for -- it keeps the agent's prose from
// interleaving with a running tool.
//
// A chunk naming a span that is NOT active (already closed, or a spawn that
// owns none) falls under the same free-form rule: with nothing open it goes,
// otherwise it waits.
func (t *SpanTracker) ShouldBroadcastStreamChunk(spanID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if spanID != "" {
		for _, s := range t.spans {
			if s.SpanID == spanID {
				return true
			}
		}
	}
	return len(t.spans) == 0
}
