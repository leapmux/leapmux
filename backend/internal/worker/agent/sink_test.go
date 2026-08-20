package agent

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
)

// testSinkSettingsRefreshed records the args of a PersistSettingsRefresh call.
type testSinkSettingsRefreshed struct {
	Model          string
	Effort         string
	PermissionMode string
	Options        map[string]string
}

// testSinkModeChange records the args of a NotifyPermissionModeChanged call.
type testSinkModeChange struct {
	Old string
	New string
}

// testSink is a test implementation of OutputSink that records calls.
type testSink struct {
	// persistErr, when set, is what PersistMessage returns. Read without the
	// lock: a test sets it at construction and never changes it afterwards.
	persistErr         error
	mu                 sync.Mutex
	messages           []testSinkMessage
	notifications      []testSinkMessage
	streamChunks       []testSinkStreamChunk
	streamEnds         []string
	sessionIDs         []string
	permissionModes    []string
	modeChanges        []testSinkModeChange
	settingsRefreshes  []testSinkSettingsRefreshed
	sessionInfos       []map[string]interface{}
	openSpans          []testSinkSpanOpen
	closedSpans        []string
	reservedColorSpans []testSinkSpanOpen
	// tracker is the REAL span engine. Delegating to it is what keeps this
	// double from drifting from the behavior it stands in for.
	tracker          spantrack.SpanTracker
	resetSpanCount   int
	statusActives    []string
	autoSchedules    []AutoContinueSchedule
	autoCancels      []AutoContinueReason
	planModeToolUses sync.Map
	// childSinkMu + children let testSink serve ChildSink as a per-child testSink
	// so provider tests can assert what got routed into a subagent transcript.
	childSinkMu sync.Mutex
	children    map[string]*testSink
	childIDMu   sync.Mutex
	childIDVal  string
	// bgTasks records the latest registry state per row key (owner == this sink).
	bgTasks map[string]bgtask.Item
	// revivedTasks records every row key ReviveBackgroundTask actually reopened,
	// in order. The effect alone cannot prove the call: a revive leaves the row
	// running, which is also how it looked before it ever finished.
	revivedTasks []string
	// reviveErr, when set, is what ReviveBackgroundTask returns INSTEAD of
	// reopening the row. The only way to exercise the caller's failure path: a
	// revive that cannot fail leaves the "arm is spent, message is lost" branch
	// unreachable from a test. Read without the lock -- set at construction.
	reviveErr error
	// lookupErr, when set, is what LookupBackgroundTask returns instead of an
	// answer -- the "registry unreadable" third case, which a miss cannot stand
	// in for. Read without the lock: set at construction.
	lookupErr error
	bgTasksMu sync.Mutex
	// notifSuppressBroadcast makes PersistNotification report broadcast=false,
	// simulating the service layer collapsing a flapping notification
	// byte-identically into the existing thread tail (no frontend clear). Default
	// false: notifications report a broadcast, like a normal standalone persist.
	notifSuppressBroadcast bool
}

type testSinkMessage struct {
	Source          leapmuxv1.MessageSource
	Content         []byte
	ParentSpanID    string
	ConnectorSpanID string
	SpanID          string
	SpanType        string
	Closing         bool
	SpanColor       int32
	MarkType        leapmuxv1.MarkType
	// NoSpan mirrors SpanInfo.NoSpan: the row carries a span id but owns no
	// span, so its span_color of 0 is the answer and the persist path must not
	// fill it from the connector.
	NoSpan bool
	// TurnEnd is set on entries recorded by PersistTurnEnd so tests can
	// distinguish the turn-end divider from regular AGENT messages
	// without inspecting the inner content.
	TurnEnd bool
	// SpansOpenAtPersist snapshots the spans this sink held open at the moment
	// the message was persisted. The real sink derives a row's span_lines from
	// exactly that state, so it is the observable that pins the ordering rule:
	// a tool_use row must persist BEFORE its own span opens (empty span_lines),
	// and its tool_result must persist WHILE the span is open (connector_end).
	SpansOpenAtPersist []testSinkSpanOpen
}

type testSinkStreamChunk struct {
	Content []byte
	SpanID  string
	Method  string
}

type testSinkSpanOpen struct {
	SpanID       string
	ParentSpanID string
}

// liveSpansLocked snapshots the spans the REAL tracker holds open, in column
// order. The service layer derives a row's span_lines from exactly that state,
// so this is the observable that pins the ordering rule a provider must follow.
func (s *testSink) liveSpansLocked() []testSinkSpanOpen {
	active := s.tracker.ActiveSpans()
	if len(active) == 0 {
		return nil
	}
	open := make([]testSinkSpanOpen, 0, len(active))
	for _, a := range active {
		open = append(open, testSinkSpanOpen{SpanID: a.SpanID, ParentSpanID: s.tracker.ParentOf(a.SpanID)})
	}
	return open
}

// PersistMessage records the row. Set persistErr to make it fail, which is the
// only way to exercise a caller's error path: every caller LOGS the error and
// carries on, so a test that cannot make the persist fail cannot tell "carries
// on" from "returns early".
func (s *testSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, testSinkMessage{Source: source, Content: append([]byte(nil), content...), ParentSpanID: span.ParentSpanID, ConnectorSpanID: span.ConnectorSpanID, SpanID: span.SpanID, SpanType: span.SpanType, Closing: span.Closing, SpanColor: span.SpanColor, MarkType: span.MarkType, NoSpan: span.NoSpan, SpansOpenAtPersist: s.liveSpansLocked()})
	return s.persistErr
}

func (s *testSink) PersistTurnEnd(content []byte, span SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, testSinkMessage{
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:            append([]byte(nil), content...),
		ParentSpanID:       span.ParentSpanID,
		ConnectorSpanID:    span.ConnectorSpanID,
		SpanID:             span.SpanID,
		SpanType:           span.SpanType,
		Closing:            span.Closing,
		MarkType:           span.MarkType,
		TurnEnd:            true,
		SpansOpenAtPersist: s.liveSpansLocked(),
	})
	return nil
}

func (s *testSink) PersistNotification(source leapmuxv1.MessageSource, content []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, testSinkMessage{Source: source, Content: append([]byte(nil), content...)})
	return !s.notifSuppressBroadcast, nil
}

// The span methods DELEGATE to a real SpanTracker rather than re-implementing
// it. The double used to keep its own active set and span-type map, which drifted
// from the engine twice: a closed span kept its type here after the tracker
// stopped keeping it, and again after it started. Recording slices stay
// alongside, because a test still wants to assert WHICH calls were made.
func (s *testSink) OpenSpan(spanID string, parentSpanID string) {
	s.mu.Lock()
	s.openSpans = append(s.openSpans, testSinkSpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
	s.mu.Unlock()
	s.tracker.OpenSpan(spanID, parentSpanID)
}

func (s *testSink) CloseSpan(spanID string) {
	s.mu.Lock()
	s.closedSpans = append(s.closedSpans, spanID)
	s.mu.Unlock()
	s.tracker.CloseSpan(spanID)
}

func (s *testSink) ResetSpans() {
	s.mu.Lock()
	s.resetSpanCount++
	s.mu.Unlock()
	s.tracker.Reset()
}

func (s *testSink) SetSpanType(spanID, spanType string) {
	s.tracker.SetSpanType(spanID, spanType)
}

func (s *testSink) GetSpanType(spanID string) string {
	return s.tracker.GetSpanType(spanID)
}

// ReserveSpanColor records which spans asked for a color, and under which
// parent. A span that never opens must never reserve one either: the real
// tracker parks the reservation on its single pending slot, which blocks that
// color from the next real span. The parent is recorded because it decides the
// column the reservation is computed for, and the child transcript reserves
// under the spawn span rather than at the root.
func (s *testSink) ReserveSpanColor(spanID, parentSpanID string) int32 {
	s.mu.Lock()
	s.reservedColorSpans = append(s.reservedColorSpans, testSinkSpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
	s.mu.Unlock()
	// Delegated, so the reservation really is parked on the tracker's single
	// pending slot: a span that reserves and never opens then blocks that color
	// exactly as it would in production.
	return s.tracker.ReserveSpanColor(spanID, parentSpanID)
}

// ReservedColorSpans returns a copy of the span IDs that reserved a color.
func (s *testSink) ReservedColorSpans() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.reservedColorSpans))
	for _, r := range s.reservedColorSpans {
		ids = append(ids, r.SpanID)
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// ReservedColors returns each reservation with the parent span it was made
// under.
func (s *testSink) ReservedColors() []testSinkSpanOpen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkSpanOpen(nil), s.reservedColorSpans...)
}

func (s *testSink) BroadcastStreamChunk(content []byte, spanID string, method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamChunks = append(s.streamChunks, testSinkStreamChunk{
		Content: append([]byte(nil), content...),
		SpanID:  spanID,
		Method:  method,
	})
}

func (s *testSink) BroadcastStreamEnd(spanID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamEnds = append(s.streamEnds, spanID)
}

func (s *testSink) PersistControlRequest(string, []byte) string    { return "" }
func (s *testSink) DeleteControlRequest(string)                    {}
func (s *testSink) BroadcastControlRequest(string, []byte, string) {}
func (s *testSink) BroadcastControlCancel(string)                  {}
func (s *testSink) UpdateSessionID(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionIDs = append(s.sessionIDs, sessionID)
}
func (s *testSink) UpdatePermissionMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissionModes = append(s.permissionModes, mode)
}
func (s *testSink) NotifyPermissionModeChanged(oldMode, newMode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modeChanges = append(s.modeChanges, testSinkModeChange{Old: oldMode, New: newMode})
}
func (s *testSink) PersistSettingsRefresh(refresh optionmap.Map) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Split the unified refresh map back into the named fields the assertions read:
	// the three well-known axes plus every other key as an "extra". An axis the
	// provider omitted reads back as "" (absent), matching the old "" sentinel.
	options := make(map[string]string)
	for k, v := range refresh {
		switch k {
		case OptionIDModel, OptionIDEffort, OptionIDPermissionMode:
		default:
			options[k] = v
		}
	}
	s.settingsRefreshes = append(s.settingsRefreshes, testSinkSettingsRefreshed{
		Model:          refresh[OptionIDModel],
		Effort:         refresh[OptionIDEffort],
		PermissionMode: refresh[OptionIDPermissionMode],
		Options:        options,
	})
}
func (s *testSink) BroadcastStatusActive(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusActives = append(s.statusActives, sessionID)
}
func (s *testSink) BroadcastSessionInfo(info map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy the map to avoid aliasing.
	cp := make(map[string]interface{}, len(info))
	for k, v := range info {
		cp[k] = v
	}
	s.sessionInfos = append(s.sessionInfos, cp)
}
func (s *testSink) PersistLeapMuxNotification(map[string]interface{}) {}
func (s *testSink) StorePlanModeToolUse(toolUseID, targetMode string) {
	s.planModeToolUses.Store(toolUseID, targetMode)
}

func (s *testSink) LoadAndDeletePlanModeToolUse(toolUseID string) (string, bool) {
	v, ok := s.planModeToolUses.LoadAndDelete(toolUseID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (s *testSink) UpdatePlan([]byte, leapmuxv1.ContentCompression, string) {}
func (s *testSink) ScheduleAutoContinue(schedule AutoContinueSchedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoSchedules = append(s.autoSchedules, schedule)
}
func (s *testSink) CancelAutoContinue(reason AutoContinueReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoCancels = append(s.autoCancels, reason)
}

// --- Subagent transcripts and the background-task registry (test recording) ---

// EnsureChildAgent returns a stable synthetic child id keyed by spawnSpanID so
// provider tests can assert child resolution without a DB. The same span always
// resolves to the same id (idempotent replay).
func (s *testSink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
	// Resolve by ROW KEY before spawn span, as the real sink does. That order is
	// load-bearing, not an optimization: Claude re-registers a revived subagent
	// under the tool_use id of the call that restarted it, so a spawn-span-only
	// lookup would miss and hand back a SECOND transcript for the same subagent.
	// A double that resolved differently would report a split the real sink does
	// not have, and hide one it does.
	if providerChildKey != "" {
		s.bgTasksMu.Lock()
		existing := s.bgTasks[providerChildKey].ChildAgentID
		s.bgTasksMu.Unlock()
		if existing != "" {
			return existing, nil
		}
	}
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	if s.children == nil {
		s.children = make(map[string]*testSink)
	}
	if c, ok := s.children[spawnSpanID]; ok {
		return c.childAgentID(), nil
	}
	cid := "child-of-" + spawnSpanID
	child := &testSink{}
	child.setChildAgentID(cid)
	s.children[spawnSpanID] = child
	if providerChildKey != "" {
		// Normalized here for the reason the other three registry methods do it:
		// production derives a usable key for an unusable one, so the fake must
		// key its row the same way or a provider test reads a row under a
		// string the registry never stores.
		providerChildKey = bgtask.NormalizeRowKey(providerChildKey)
		s.bgTasksMu.Lock()
		if s.bgTasks == nil {
			s.bgTasks = make(map[string]bgtask.Item)
		}
		item := s.bgTasks[providerChildKey]
		item.RowKey = providerChildKey
		item.ChildAgentID = cid
		item.Kind = bgtask.KindSubagent
		if title != "" {
			item.Title = title
		}
		s.bgTasks[providerChildKey] = item
		s.bgTasksMu.Unlock()
	}
	return cid, nil
}

// childAgentID is the synthetic id this testSink reports for itself when it is
// used as a child sink (set by EnsureChildAgent). The field is read under the
// childSinkMu of the PARENT sink, so it lives here on the child as its own lock
// to avoid a parent->child lock ordering hazard.
func (s *testSink) childAgentID() string {
	s.childIDMu.Lock()
	defer s.childIDMu.Unlock()
	return s.childIDVal
}

func (s *testSink) setChildAgentID(id string) {
	s.childIDMu.Lock()
	defer s.childIDMu.Unlock()
	s.childIDVal = id
}

// ChildSink returns the per-child testSink created by EnsureChildAgent (or a
// fresh empty one), so messages routed into a subagent transcript are recorded
// on a distinct sink a test can assert against.
func (s *testSink) ChildSink(childAgentID string) OutputSink {
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	for _, c := range s.children {
		if c.childAgentID() == childAgentID {
			return c
		}
	}
	// A child id that was not EnsureChildAgent'd (e.g. an explicit PersistChild*):
	// create a fresh recording sink so the call still records.
	c := &testSink{}
	c.setChildAgentID(childAgentID)
	if s.children == nil {
		s.children = make(map[string]*testSink)
	}
	s.children["late:"+childAgentID] = c
	return c
}

func (s *testSink) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	cs, _ := s.ChildSink(childAgentID).(*testSink)
	if cs == nil {
		return nil
	}
	return cs.PersistMessage(source, content, span)
}

func (s *testSink) PersistChildTurnEnd(childAgentID string, content []byte, span SpanInfo) error {
	cs, _ := s.ChildSink(childAgentID).(*testSink)
	if cs == nil {
		return nil
	}
	return cs.PersistTurnEnd(content, span)
}

// PersistChildPrompt mirrors the real sink's contract: a USER message carrying
// {"content": prompt}, written only into a child transcript that has no message
// yet, and skipped for a blank prompt. Tests assert on the child's Messages().
func (s *testSink) PersistChildPrompt(childAgentID, prompt string) error {
	if childAgentID == "" || strings.TrimSpace(prompt) == "" {
		return nil
	}
	cs, _ := s.ChildSink(childAgentID).(*testSink)
	if cs == nil {
		return nil
	}
	if len(cs.Messages()) > 0 {
		return nil
	}
	content, err := json.Marshal(map[string]string{"content": prompt})
	if err != nil {
		return err
	}
	return cs.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, SpanInfo{})
}

// PersistChildUserMessage mirrors the real sink: it APPENDS, with no emptiness
// guard, and carries the scroll-rail mark the opening prompt does not. The
// missing guard is the whole difference from PersistChildPrompt above -- a
// delivered message belongs wherever the transcript currently ends.
func (s *testSink) PersistChildUserMessage(childAgentID, text string) error {
	if childAgentID == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	cs, _ := s.ChildSink(childAgentID).(*testSink)
	if cs == nil {
		return nil
	}
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	return cs.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, SpanInfo{
		MarkType: leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE,
	})
}

// testFakeEndedAt is the instant the fake stamps on an active -> final
// transition. A fixed value, because the fake models WHETHER ended_at is set,
// not when: an assertion that the stamp is absent for a non-final status was
// vacuously true while nothing ever set it.
//
// updated_at is deliberately NOT modeled, although every production applier
// stamps it. Nothing in this package can read it: LookupBackgroundTask hands
// back a status and a child id rather than an Item, so the field reaches no
// provider decision. Stamping a fixed instant would make "the second write
// advanced updated_at" fail against correct code, and stamping the real clock
// would make the fake non-deterministic. A test that needs the stamp belongs in
// the service package, against the real registry.
var testFakeEndedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (s *testSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if s.bgTasks == nil {
		s.bgTasks = make(map[string]bgtask.Item)
	}
	// task.Clean().ToItem().PreservingBlanksFrom(existing), the SAME chain
	// the production applier uses. No link may be hand-written here: the clean
	// so a provider test sees the title the registry really stores, the
	// projection so a new field on Upsert reaches this fake too, and the merge
	// so blank-means-keep holds for every descriptive field, not just the child
	// id. A partial upsert (Claude's task_notification carries only status +
	// description) blanked the Title against this fake while production
	// preserved it, so a test that asserted the real contract failed against
	// correct code.
	// The FIRST link of the chain, and the one the production sink applies
	// before the closure runs (see agentOutputSink.applyAndBroadcast): an
	// unusable provider key becomes a derived one rather than dropping the row.
	// A fake that kept the raw key would report a row under a string production
	// never stores, and a provider test asserting it would pass against code
	// that behaves differently.
	task.RowKey = bgtask.NormalizeRowKey(task.RowKey)
	existing := s.bgTasks[task.RowKey]
	item := task.Clean().ToItem().PreservingBlanksFrom(existing)
	// A final status is absorbing, as in the registry: a replayed non-final
	// upsert must not resurrect a row that already ended. Without this the fake
	// was MORE permissive than production, so a test pinning the guard failed
	// against code that has it.
	if existing.Status.IsFinished() && !item.Status.IsFinished() {
		item.Status = existing.Status
		item.EndedAt = existing.EndedAt
	}
	if !existing.Status.IsFinished() && item.Status.IsFinished() && item.EndedAt.IsZero() {
		item.EndedAt = testFakeEndedAt
	}
	s.bgTasks[task.RowKey] = item
	return nil
}

func (s *testSink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	rowKey = bgtask.NormalizeRowKey(rowKey)
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if item, ok := s.bgTasks[rowKey]; ok {
		// Monotonic and absorbing, as in the registry: a late or replayed
		// non-final update must not resurrect a finished row.
		if item.Status.IsFinished() && !status.IsFinished() {
			return nil
		}
		if !item.Status.IsFinished() && status.IsFinished() {
			item.EndedAt = testFakeEndedAt
		}
		item.Status = status
		item.ActiveForm = activeForm
		s.bgTasks[rowKey] = item
	}
	return nil
}

func (s *testSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	rowKey = bgtask.NormalizeRowKey(rowKey)
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if item, ok := s.bgTasks[rowKey]; ok {
		// First close wins, as in the registry: a re-close cannot relabel a row
		// that already ended.
		if item.Status.IsFinished() {
			return nil
		}
		item.Status = status
		item.EndedAt = testFakeEndedAt
		s.bgTasks[rowKey] = item
	}
	return nil
}

func (s *testSink) LookupBackgroundTask(rowKey string) (string, bgtask.Status, bool, error) {
	var noStatus bgtask.Status
	if s.lookupErr != nil {
		return "", noStatus, false, s.lookupErr
	}
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	item, ok := s.bgTasks[rowKey]
	if !ok {
		return "", noStatus, false, nil
	}
	return item.ChildAgentID, item.Status, true, nil
}

// ReviveBackgroundTask mirrors the registry's revive: a finished row returns to
// running with its ended_at cleared, and an absent or already-active row is a
// no-op. The row keys it actually revived are recorded so a test can assert the
// call happened rather than only its effect.
func (s *testSink) ReviveBackgroundTask(rowKey string) error {
	if s.reviveErr != nil {
		return s.reviveErr
	}
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	item, ok := s.bgTasks[rowKey]
	if !ok || !item.Status.IsFinished() {
		return nil
	}
	item.Status = bgtask.StatusRunning
	// ActiveForm AND Description, the pair the real ReviveAgentBackgroundTask
	// clears. Both describe the run that ENDED -- the last activity text, and the
	// output file its task_notification identified -- so a fake that cleared only
	// one would pass a test that asserts the finished run's output path survives
	// a restart, which the registry makes certain it does not.
	item.ActiveForm = ""
	item.Description = ""
	item.EndedAt = time.Time{}
	s.bgTasks[rowKey] = item
	s.revivedTasks = append(s.revivedTasks, rowKey)
	return nil
}

// UnlinkBackgroundTask clears a row's child linkage, leaving the row and the
// child transcript in place. This is the one state a provider can still meet in
// which a finished subagent's row identifies no transcript: EnsureChildAgent
// created the child agent row and the registry upsert that links it then failed.
// Cap eviction does NOT produce it -- a linked row survives the display cap in
// the store (see registryOps.retention) -- so a test must not use eviction to
// reach it.
func (s *testSink) UnlinkBackgroundTask(rowKey string) {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	item, ok := s.bgTasks[rowKey]
	if !ok {
		return
	}
	item.ChildAgentID = ""
	s.bgTasks[rowKey] = item
}

// ChildAgentIDs lists every child transcript this sink handed out, so a test can
// assert that a resolution did NOT open a second one. It returns the child agent
// IDS, not the spawn spans that key the map: an assertion reads
// "child-of-<span>", which no spawn span can ever equal, so returning the keys
// made every NotContains on it pass whatever the code did.
func (s *testSink) ChildAgentIDs() []string {
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	ids := make([]string, 0, len(s.children))
	for _, c := range s.children {
		ids = append(ids, c.childAgentID())
	}
	return ids
}

// RevivedTasks returns the row keys ReviveBackgroundTask actually reopened.
func (s *testSink) RevivedTasks() []string {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	return append([]string(nil), s.revivedTasks...)
}

func (s *testSink) RenameBackgroundTask(oldKey, newKey string) error {
	// BOTH keys, as production does: normalizing one and not the other is how a
	// rename stops finding its own row.
	oldKey, newKey = bgtask.NormalizeRowKey(oldKey), bgtask.NormalizeRowKey(newKey)
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if item, ok := s.bgTasks[oldKey]; ok {
		delete(s.bgTasks, oldKey)
		item.RowKey = newKey
		s.bgTasks[newKey] = item
	}
	return nil
}

// CleanupChildAgent is a no-op on the test fake: tests that exercise the
// per-child cleanup use the real OutputHandler via svc.Output.NewSink.
func (s *testSink) CleanupChildAgent(childAgentID string) {}

// BackgroundTasks returns a snapshot of the recorded registry rows (test helper).
func (s *testSink) BackgroundTasks() []bgtask.Item {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	out := make([]bgtask.Item, 0, len(s.bgTasks))
	for _, item := range s.bgTasks {
		out = append(out, item)
	}
	return out
}

// MessageCount returns the number of persisted messages.
func (s *testSink) MessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *testSink) NotificationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notifications)
}

func (s *testSink) LastNotification() testSinkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifications[len(s.notifications)-1]
}

// PersistedNotifications returns a snapshot of every PersistNotification
// call in order. Distinct from recordingControlSink.Notifications, which
// captures PersistLeapMuxNotification map payloads.
func (s *testSink) PersistedNotifications() []testSinkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkMessage(nil), s.notifications...)
}

// Messages returns a copy of all persisted messages.
func (s *testSink) Messages() []testSinkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkMessage(nil), s.messages...)
}

// StreamChunkCount returns the number of broadcast stream chunks.
func (s *testSink) StreamChunkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streamChunks)
}

func (s *testSink) LastStreamChunk() testSinkStreamChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamChunks[len(s.streamChunks)-1]
}

// StreamChunks returns a copy of all broadcast stream chunks in order.
func (s *testSink) StreamChunks() []testSinkStreamChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkStreamChunk(nil), s.streamChunks...)
}

func (s *testSink) StreamEndCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streamEnds)
}

func (s *testSink) LastStreamEnd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streamEnds) == 0 {
		return ""
	}
	return s.streamEnds[len(s.streamEnds)-1]
}

// SessionIDCount returns the number of UpdateSessionID calls.
func (s *testSink) SessionIDCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessionIDs)
}

// LastSessionID returns the most recently recorded session ID.
func (s *testSink) LastSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionIDs) == 0 {
		return ""
	}
	return s.sessionIDs[len(s.sessionIDs)-1]
}

func (s *testSink) SettingsRefreshCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.settingsRefreshes)
}

func (s *testSink) StatusActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.statusActives)
}

func (s *testSink) ModeChanges() []testSinkModeChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkModeChange(nil), s.modeChanges...)
}

func (s *testSink) LastSettingsRefresh() testSinkSettingsRefreshed {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsRefreshes[len(s.settingsRefreshes)-1]
}

func (s *testSink) PermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.permissionModes) == 0 {
		return ""
	}
	return s.permissionModes[len(s.permissionModes)-1]
}

// SessionInfoCount returns the number of BroadcastSessionInfo calls.
func (s *testSink) SessionInfoCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessionInfos)
}

// LastSessionInfo returns the most recently recorded session info.
func (s *testSink) LastSessionInfo() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionInfos) == 0 {
		return nil
	}
	return s.sessionInfos[len(s.sessionInfos)-1]
}

// OpenSpans returns a copy of all opened span IDs.
func (s *testSink) OpenSpans() []testSinkSpanOpen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]testSinkSpanOpen(nil), s.openSpans...)
}

// ClosedSpans returns a copy of all closed span IDs.
func (s *testSink) ClosedSpans() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.closedSpans...)
}

// ClosedSpanCount returns the number of CloseSpan calls.
func (s *testSink) ClosedSpanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.closedSpans)
}

func (s *testSink) ResetSpanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetSpanCount
}

func (s *testSink) AutoScheduleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.autoSchedules)
}

func (s *testSink) LastAutoSchedule() AutoContinueSchedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoSchedules[len(s.autoSchedules)-1]
}

func (s *testSink) AutoCancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.autoCancels)
}

func (s *testSink) LastAutoCancel() AutoContinueReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCancels[len(s.autoCancels)-1]
}

// noopSink is a no-op implementation of OutputSink for tests that don't
// need to verify output.
type noopSink struct{}

func (noopSink) PersistMessage(leapmuxv1.MessageSource, []byte, SpanInfo) error {
	return nil
}
func (noopSink) PersistTurnEnd([]byte, SpanInfo) error                             { return nil }
func (noopSink) PersistNotification(leapmuxv1.MessageSource, []byte) (bool, error) { return true, nil }
func (noopSink) OpenSpan(string, string)                                           {}
func (noopSink) CloseSpan(string)                                                  {}
func (noopSink) ResetSpans()                                                       {}
func (noopSink) SetSpanType(string, string)                                        {}
func (noopSink) GetSpanType(string) string                                         { return "" }
func (noopSink) ReserveSpanColor(string, string) int32                             { return 0 }
func (noopSink) BroadcastStreamChunk([]byte, string, string)                       {}
func (noopSink) BroadcastStreamEnd(string)                                         {}
func (noopSink) PersistControlRequest(string, []byte) string                       { return "" }
func (noopSink) DeleteControlRequest(string)                                       {}
func (noopSink) BroadcastControlRequest(string, []byte, string)                    {}
func (noopSink) BroadcastControlCancel(string)                                     {}
func (noopSink) UpdateSessionID(string)                                            {}
func (noopSink) UpdatePermissionMode(string)                                       {}
func (noopSink) NotifyPermissionModeChanged(string, string)                        {}
func (noopSink) PersistSettingsRefresh(optionmap.Map)                              {}
func (noopSink) BroadcastStatusActive(string)                                      {}
func (noopSink) BroadcastSessionInfo(map[string]interface{})                       {}
func (noopSink) PersistLeapMuxNotification(map[string]interface{})                 {}
func (noopSink) StorePlanModeToolUse(string, string)                               {}
func (noopSink) LoadAndDeletePlanModeToolUse(string) (string, bool)                { return "", false }
func (noopSink) UpdatePlan([]byte, leapmuxv1.ContentCompression, string)           {}
func (noopSink) ScheduleAutoContinue(AutoContinueSchedule)                         {}
func (noopSink) CancelAutoContinue(AutoContinueReason)                             {}
func (noopSink) EnsureChildAgent(string, string, string) (string, error)           { return "", nil }
func (noopSink) ChildSink(string) OutputSink                                       { return noopSink{} }
func (noopSink) PersistChildMessage(string, leapmuxv1.MessageSource, []byte, SpanInfo) error {
	return nil
}
func (noopSink) PersistChildTurnEnd(string, []byte, SpanInfo) error { return nil }
func (noopSink) PersistChildPrompt(string, string) error            { return nil }
func (noopSink) UpsertBackgroundTask(bgtask.Upsert) error           { return nil }
func (noopSink) UpdateBackgroundTaskStatus(string, bgtask.Status, string) error {
	return nil
}
func (noopSink) CloseBackgroundTask(string, bgtask.Status) error { return nil }
func (noopSink) RenameBackgroundTask(string, string) error       { return nil }
func (noopSink) LookupBackgroundTask(string) (string, bgtask.Status, bool, error) {
	var noStatus bgtask.Status
	return "", noStatus, false, nil
}
func (noopSink) ReviveBackgroundTask(string) error            { return nil }
func (noopSink) PersistChildUserMessage(string, string) error { return nil }
func (noopSink) CleanupChildAgent(string)                     {}

// The one test in this helpers file, and it belongs here: every caller of
// ChildAgentIDs asserts NotContains against a child id, so a version that
// returned the map's spawn-span keys satisfied all of them and proved nothing.
// Pinning the return shape beside the fake keeps that from coming back.
func TestTestSink_ChildAgentIDsReturnsChildIDsNotSpawnSpans(t *testing.T) {
	t.Parallel()

	s := &testSink{}
	childID, err := s.EnsureChildAgent("tu-spawn", "task-1", "explore")
	require.NoError(t, err)
	require.Equal(t, "child-of-tu-spawn", childID)

	assert.Equal(t, []string{childID}, s.ChildAgentIDs())
	assert.NotContains(t, s.ChildAgentIDs(), "tu-spawn", "the spawn span is the key, not the id")
}
