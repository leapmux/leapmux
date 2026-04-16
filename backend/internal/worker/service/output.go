// Package service output provides agent output persistence and broadcasting.
// It implements the agent.OutputSink interface, backing the generic primitives
// with DB queries, notification threading, and WatcherManager fan-out.
package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// SpanTracker manages hierarchical span state for an agent's message threading.
type SpanTracker struct {
	mu        sync.Mutex
	spans     []ActiveSpan
	spanTypes map[string]string // spanID → span type (tool name / item type)
	parentMap map[string]string // spanID → parentSpanID (persists after close for ancestry lookups)
	nextColor int
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
	column := -1
	for i := minCol; ; i++ {
		if !used[i] {
			column = i
			break
		}
	}

	t.nextColor = t.nextColor%spanPaletteSize + 1
	t.spans = append(t.spans, ActiveSpan{
		SpanID:     spanID,
		ColorIndex: t.nextColor,
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
	t.nextColor = 0
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

// PeekNextColor returns the color index that will be assigned to the next
// span opened via OpenSpan. Safe to call only when output processing is
// sequential per agent (which it is for both Claude and Codex handlers).
func (t *SpanTracker) PeekNextColor() int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int32(t.nextColor%spanPaletteSize + 1)
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
type notifThreadRef struct {
	msgID string
	seq   int64
}

// notifThreadWrapper is the content envelope stored in the DB for notification
// thread messages. It consolidates multiple notifications into a single DB row.
type notifThreadWrapper struct {
	OldSeqs  []int64           `json:"old_seqs,omitempty"`
	Messages []json.RawMessage `json:"messages"`
}

// wrapNotifContent wraps a single raw notification JSON into a notifThreadWrapper.
func wrapNotifContent(rawJSON []byte) []byte {
	w := notifThreadWrapper{
		Messages: []json.RawMessage{rawJSON},
	}
	data, err := json.Marshal(w)
	if err != nil {
		slog.Warn("marshal notification wrapper", "error", err)
		return []byte(`{"messages":[]}`)
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
		h:                        h,
		agentID:                  agentID,
		agentProvider:            agentProvider,
		notificationConsolidator: agent.NotificationConsolidatorForProvider(agentProvider),
		tracker:                  h.spanTracker(agentID),
	}
}

// agentOutputSink implements agent.OutputSink for a single agent.
type agentOutputSink struct {
	h                        *OutputHandler
	agentID                  string
	agentProvider            leapmuxv1.AgentProvider
	notificationConsolidator agent.NotificationConsolidator
	tracker                  *SpanTracker
}

// --- OutputSink interface implementation ---

func (s *agentOutputSink) PersistMessage(role leapmuxv1.MessageRole, content []byte, span agent.SpanInfo) error {
	return s.h.persistAndBroadcast(s.agentID, s.agentProvider, role, content, span, s.tracker)
}

func (s *agentOutputSink) PersistNotification(role leapmuxv1.MessageRole, content []byte) error {
	return s.h.persistNotificationThreaded(s.agentID, s.agentProvider, s.notificationConsolidator, role, content)
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

func (s *agentOutputSink) PeekNextSpanColor() int32 {
	return s.tracker.PeekNextColor()
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
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{
			ControlCancel: &leapmuxv1.AgentControlCancelRequest{
				AgentId:   s.agentID,
				RequestId: requestID,
			},
		},
	})
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
		AgentId:        s.agentID,
		Status:         status,
		AgentSessionId: sessionID,
		WorkerOnline:   true,
		PermissionMode: permissionMode,
		Model:          modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:         dbAgent.Effort,
		GitStatus:      gitutil.GetGitStatus(dbAgent.WorkingDir),
		AgentProvider:  s.agentProvider,
		ExtraSettings:  loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider),
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
		s.BroadcastNotification(map[string]interface{}{
			"type": "settings_changed",
			"changes": map[string]interface{}{
				agent.OptionGroupKeyPermissionMode: map[string]string{"old": oldMode, "new": mode},
			},
		})
	}
}

func (s *agentOutputSink) BroadcastSettingsRefreshed(model, effort, permissionMode string, extraSettings map[string]string) {
	// Persist the refreshed settings to the DB.
	_ = s.h.queries.UpdateAgentAllSettings(bgCtx(), db.UpdateAgentAllSettingsParams{
		Model:          model,
		Effort:         effort,
		PermissionMode: permissionMode,
		ExtraSettings:  marshalExtraSettings(extraSettings),
		ID:             s.agentID,
	})

	// Broadcast a StatusChange so connected frontends see the new values.
	dbAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for settings broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}
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

func (s *agentOutputSink) BroadcastSessionInfo(info map[string]interface{}) {
	s.h.broadcastAgentSessionInfo(s.agentID, info)
}

func (s *agentOutputSink) BroadcastNotification(content map[string]interface{}) {
	s.h.BroadcastNotification(s.agentID, s.agentProvider, content)
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

func (s *agentOutputSink) UpdatePlan(filePath string, content []byte, compression leapmuxv1.ContentCompression, title string) {
	s.h.updatePlan(s.agentID, filePath, content, compression, title)
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
func (h *OutputHandler) persistAndBroadcast(agentID string, agentProvider leapmuxv1.AgentProvider, role leapmuxv1.MessageRole, contentJSON []byte, span agent.SpanInfo, tracker *SpanTracker) error {
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
		Role:               role,
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
		Role:               role,
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
func (h *OutputHandler) persistNotificationThreaded(agentID string, agentProvider leapmuxv1.AgentProvider, consolidator agent.NotificationConsolidator, role leapmuxv1.MessageRole, contentJSON []byte) error {
	if h.wakeLock != nil {
		h.wakeLock.RecordActivity()
	}
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if err := h.appendToNotificationThread(agentID, agentProvider, consolidator, threadRef, role, contentJSON); err == nil {
			return nil
		}
	}

	return h.createNotificationStandalone(agentID, agentProvider, role, contentJSON)
}

// appendToNotificationThread appends a message to an existing notification thread.
func (h *OutputHandler) appendToNotificationThread(agentID string, agentProvider leapmuxv1.AgentProvider, consolidator agent.NotificationConsolidator, threadRef *notifThreadRef, role leapmuxv1.MessageRole, contentJSON []byte) error {
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

	wrapper.OldSeqs = append(wrapper.OldSeqs, parentRow.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}
	wrapper.Messages = append(wrapper.Messages, contentJSON)
	wrapper.Messages = consolidateNotificationThread(wrapper.Messages, consolidator)

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
		Role:               parentRow.Role,
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		Seq:                newSeq,
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
	})

	return nil
}

// createNotificationStandalone creates a new standalone notification message.
func (h *OutputHandler) createNotificationStandalone(agentID string, agentProvider leapmuxv1.AgentProvider, role leapmuxv1.MessageRole, contentJSON []byte) error {
	msgID := id.Generate()
	wrapped := wrapNotifContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := h.queries.CreateMessage(bgCtx(), db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               role,
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
		msgID: msgID,
		seq:   seq,
	})

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               role,
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
		"type": "agent_session_info",
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
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                -1, // Ephemeral sentinel
	})
}

// BroadcastNotification persists and broadcasts a LEAPMUX notification.
func (h *OutputHandler) BroadcastNotification(agentID string, agentProvider leapmuxv1.AgentProvider, content map[string]interface{}) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		slog.Warn("marshal notification content", "agent_id", agentID, "error", err)
		return
	}
	if err := h.persistNotificationThreaded(agentID, agentProvider, agent.NotificationConsolidatorForProvider(agentProvider), leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// updatePlan persists a plan file path, content, and title for an agent.
func (h *OutputHandler) updatePlan(agentID, filePath string, compressed []byte, compression leapmuxv1.ContentCompression, title string) {
	agentRow, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for plan update", "agent_id", agentID, "error", err)
		return
	}

	// Preserve existing plan_title when the new content yields no title.
	if title == "" {
		title = agentRow.PlanTitle
	}

	canonicalPath := agentRow.PlanFilePath
	if len(compressed) > 0 {
		content, err := msgcodec.Decompress(compressed, compression)
		if err != nil {
			slog.Warn("failed to decompress plan content", "agent_id", agentID, "error", err)
		} else if len(content) > 0 {
			materializedPath, err := h.materializePlanFile(title, content, h.now())
			if err != nil {
				slog.Warn("failed to materialize plan file", "agent_id", agentID, "error", err)
			} else {
				canonicalPath = materializedPath
			}
		}
	}

	if filePath != "" && canonicalPath == "" {
		slog.Debug("ignoring provider-native plan path in favor of worker materialization", "agent_id", agentID, "provider_path", filePath)
	}

	shouldAutoRename := title != "" &&
		title != agentRow.Title &&
		(agentRow.Title == agentRow.PlanTitle ||
			agentAutoTitlePattern.MatchString(agentRow.Title))

	if shouldAutoRename {
		if err := h.queries.UpdateAgentPlanAndTitle(bgCtx(), db.UpdateAgentPlanAndTitleParams{
			PlanFilePath:           canonicalPath,
			PlanContent:            compressed,
			PlanContentCompression: compression,
			PlanTitle:              title,
			Title:                  title,
			ID:                     agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
		} else {
			h.BroadcastNotification(agentID, agentRow.AgentProvider, map[string]interface{}{
				"type":  "agent_renamed",
				"title": title,
			})
		}
	} else {
		if err := h.queries.UpdateAgentPlan(bgCtx(), db.UpdateAgentPlanParams{
			PlanFilePath:           canonicalPath,
			PlanContent:            compressed,
			PlanContentCompression: compression,
			PlanTitle:              title,
			ID:                     agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
		}
	}
}

// consolidateNotificationThread consolidates a notification thread's messages.
// Service-owned LeapMux notification types are merged centrally, while
// provider-owned raw payloads are classified through the injected consolidator.
// Ordering is preserved by the last occurrence index of each retained entry.
func consolidateNotificationThread(messages []json.RawMessage, providerConsolidator agent.NotificationConsolidator) []json.RawMessage {
	if providerConsolidator == nil {
		providerConsolidator = agent.NotificationConsolidatorForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
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

	mergedChanges := map[string]settingsChange{}
	settingsLastIdx := -1

	contextClearedRaw := json.RawMessage(nil)
	contextClearedLastIdx := -1

	interruptedRaw := json.RawMessage(nil)
	interruptedLastIdx := -1

	planExecRaw := json.RawMessage(nil)
	planExecLastIdx := -1

	type indexedRaw struct {
		idx  int
		raw  json.RawMessage
		kind agent.NotificationKind
	}

	rateLimitByType := map[string]indexedRaw{}
	providerEntries := map[string]indexedRaw{}

	var latestStatus indexedRaw
	statusLastIdx := -1

	var latestAPIRetry indexedRaw
	apiRetryLastIdx := -1

	var keepAll []indexedRaw

	for i, raw := range messages {
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			slog.Warn("consolidate notification unmarshal failed", "error", err)
			keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			continue
		}

		switch env.Type {
		case "settings_changed":
			for key, val := range env.Changes {
				if existing, ok := mergedChanges[key]; ok {
					mergedChanges[key] = settingsChange{Old: existing.Old, New: val.New}
				} else {
					mergedChanges[key] = val
				}
			}
			settingsLastIdx = i

		case "context_cleared":
			contextClearedRaw = raw
			contextClearedLastIdx = i
			keepAll = slices.DeleteFunc(keepAll, func(ir indexedRaw) bool {
				return ir.kind == agent.NotificationKindCompactionBoundary
			})

		case "plan_execution":
			planExecRaw = raw
			planExecLastIdx = i

		case "interrupted":
			interruptedRaw = raw
			interruptedLastIdx = i

		case "rate_limit":
			key := "unknown"
			if env.RLInfo != nil && env.RLInfo.RateLimitType != "" {
				key = env.RLInfo.RateLimitType
			}
			rateLimitByType[key] = indexedRaw{idx: i, raw: raw}
		case "compacting":
			latestStatus = indexedRaw{idx: i, raw: raw, kind: agent.NotificationKindStatus}
			statusLastIdx = i

		default:
			class := providerConsolidator.Classify(raw)
			switch class.Kind {
			case agent.NotificationKindStatus:
				latestStatus = indexedRaw{idx: i, raw: raw, kind: class.Kind}
				statusLastIdx = i
			case agent.NotificationKindAPIRetry:
				latestAPIRetry = indexedRaw{idx: i, raw: raw, kind: class.Kind}
				apiRetryLastIdx = i
			case agent.NotificationKindCompactionBoundary:
				statusLastIdx = -1
				latestStatus = indexedRaw{}
				if contextClearedLastIdx >= 0 && i > contextClearedLastIdx {
					contextClearedRaw = nil
					contextClearedLastIdx = -1
				}
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw, kind: class.Kind})
			case agent.NotificationKindProviderScoped:
				prev, ok := providerEntries[class.Key]
				if ok {
					merged, err := providerConsolidator.Merge(class, prev.raw, raw)
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

	if settingsLastIdx >= 0 {
		effective := map[string]settingsChange{}
		for key, val := range mergedChanges {
			if val.Old != val.New {
				effective[key] = val
			}
		}
		if len(effective) > 0 {
			entry := map[string]interface{}{
				"type":    "settings_changed",
				"changes": effective,
			}
			if data, err := json.Marshal(entry); err == nil {
				entries = append(entries, indexedRaw{idx: settingsLastIdx, raw: data})
			}
		}
	}

	if contextClearedLastIdx >= 0 {
		entries = append(entries, indexedRaw{idx: contextClearedLastIdx, raw: contextClearedRaw})
	}

	if planExecLastIdx >= 0 {
		entries = append(entries, indexedRaw{idx: planExecLastIdx, raw: planExecRaw})
	}

	if interruptedLastIdx >= 0 {
		entries = append(entries, indexedRaw{idx: interruptedLastIdx, raw: interruptedRaw})
	}

	for _, rateLimit := range rateLimitByType {
		entries = append(entries, rateLimit)
	}

	for _, providerEntry := range providerEntries {
		entries = append(entries, providerEntry)
	}

	if statusLastIdx >= 0 {
		entries = append(entries, latestStatus)
	}

	if apiRetryLastIdx >= 0 {
		entries = append(entries, latestAPIRetry)
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
