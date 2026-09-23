package agenttest

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
)

// SettingsRefresh records the arguments of a PersistSettingsRefresh call.
type SettingsRefresh struct {
	Model          string
	Effort         string
	PermissionMode string
	Options        map[string]string
}

// ModeChange records the args of a NotifyPermissionModeChanged call.
type ModeChange struct {
	Old string
	New string
}

// childSpawnSpanTable is the child-agent-id -> spawn-span map the whole sink
// tree shares. It stands in for the `agents` row, which is one table the worker
// reads by primary key however it got there -- so a root sink, a child sink and
// a sink built after a restart must all give one answer.
type childSpawnSpanTable struct {
	mu        sync.Mutex
	byChildID map[string]string
}

// Sink is a test implementation of ProviderServices that records calls.
type Sink struct {
	// PersistErr, when set, is what PersistMessage returns. Read without the
	// lock: a test sets it at construction and never changes it afterwards.
	PersistErr        error
	mu                sync.Mutex
	messages          []Message
	notifications     []Message
	progress          []agent.ProgressUpdate
	progressCount     agent.ProgressCounter
	sessionIDs        []string
	permissionModes   []string
	modeChanges       []ModeChange
	settingsRefreshes []SettingsRefresh
	sessionInfos      []map[string]interface{}
	// leapMuxNotifications holds every PersistLeapMuxNotification payload in
	// arrival order. These are the worker's OWN notification envelopes, which no
	// provider writes, so a test reads the meaning rather than provider bytes.
	leapMuxNotifications []map[string]interface{}
	openSpans            []SpanOpen
	closedSpans          []string
	// TurnActiveCalls records every SetTurnState value in order. The ORDER is
	// the observable that matters: a provider that clears its turn flag before
	// it publishes the turn-end envelope, or that never clears it on a path the
	// happy case does not reach, latches the agent busy forever.
	TurnActiveCalls []bool
	// turnKinds records the queue classification that accompanied each turn
	// state.
	turnStates []agent.TurnState
	// interruptIgnoredReports records when a provider proves that an accepted
	// interrupt did not end its turn.
	interruptIgnoredReports int
	// turnLifecycle interleaves the turn-end envelope with the turn-flag
	// transitions, which the two slices above cannot show apart. See
	// TurnLifecycle.
	turnLifecycle      []string
	reservedColorSpans []SpanOpen
	// tracker is the REAL span engine. Delegating to it is what keeps this
	// double from drifting from the behavior it stands in for.
	tracker        spantrack.SpanTracker
	resetSpanCount int
	turnSeqs       []uint64
	statusActives  []string
	// goals records every UpsertGoal in arrival order, and goalClears counts
	// ClearGoal. A provider's goal parser is tested through these: they hold the
	// neutral GoalUpdate, so a test asserts what the parser MEANT rather than
	// the provider bytes it read.
	goals       []agent.GoalUpdate
	currentGoal *agent.GoalUpdate
	goalClears  int
	// goalClearSnapshots records the snapshot flag of every ClearGoal, in order.
	// The count alone cannot tell a restatement from a real removal, and that is
	// the whole distinction the flag exists to carry.
	goalClearSnapshots      []bool
	goalCapabilityPublishes int
	autoSchedules           []agent.AutoContinueSchedule
	autoCancels             []agent.AutoContinueReason
	planModeToolUses        sync.Map
	// childSinkMu + children let Sink serve ChildSink as a per-child Sink
	// so provider tests can assert what got routed into a subagent transcript.
	childSinkMu sync.Mutex
	children    map[string]*Sink
	childIDMu   sync.Mutex
	childIDVal  string
	// spawnSpans is the child-agent-id -> spawn-span table ChildSpawnSpan reads.
	// Every sink of one tree holds the SAME pointer, because production answers
	// the same for a root sink, a child sink and a sink built after a restart.
	// EnsureChildAgent creates it under childSinkMu, so a sink that never spawned
	// a child holds nil and answers "" -- which is what production answers for an
	// id no row carries. Nil is therefore a usable zero value, and a bare
	// &Sink{} needs no constructor.
	spawnSpans *childSpawnSpanTable
	// bgTasks records the latest registry state per row key (owner == this sink).
	bgTasks map[string]bgtask.Item
	// bgTaskStatuses records distinct status values per row key, in order.
	// A fast-exiting shell can Close before a test reads bgTasks, so the
	// trail is the only way to prove Running landed first. Duplicate writes
	// (no-op upsert, absorbed reject) are skipped so length asserts stay
	// meaningful.
	bgTaskStatuses map[string][]bgtask.Status
	// OnCloseBackgroundTask runs at the START of CloseBackgroundTask, before the lock,
	// so a test can observe what the rest of the process can see at the moment a
	// row reaches its final status. Set it before the first close.
	OnCloseBackgroundTask func(rowKey string, status bgtask.Status)
	// revivedTasks records every row key ReviveBackgroundTask actually reopened,
	// in order. The effect alone cannot prove the call: a revive leaves the row
	// running, which is also how it looked before it ever finished.
	revivedTasks []string
	// ReviveErr, when set, is what ReviveBackgroundTask returns INSTEAD of
	// reopening the row. The only way to exercise the caller's failure path: a
	// revive that cannot fail leaves the "arm is spent, message is lost" branch
	// unreachable from a test. Read without the lock -- set at construction.
	ReviveErr error
	// LookupErr, when set, is what LookupBackgroundTask returns instead of an
	// answer -- the "registry unreadable" third case, which a miss cannot stand
	// in for. Read without the lock: set at construction.
	LookupErr error
	// SpawnSpanErr, when set, is what ChildSpawnSpan returns instead of a span --
	// the same "could not be read" third case LookupErr covers, and the only way
	// to reach the branch that decides what a restart does when the child's spawn
	// span is unknowable. Read without the lock: set at construction.
	SpawnSpanErr error
	bgTasksMu    sync.Mutex
	// SuppressNotificationBroadcast makes PersistNotification report false. It
	// simulates the service layer that folds a changing notification into an
	// existing thread tail. The zero value reports a broadcast.
	SuppressNotificationBroadcast bool
	// reportIDs models the database uniqueness rule for provider-neutral reports.
	reportIDs map[string]struct{}
}

// Sink implements the service facets. A test passes it to a provider through
// NewProviderServices, as the production sink does.
var _ agent.ServiceFacets = (*Sink)(nil)

type Message struct {
	Source               leapmuxv1.MessageSource
	Content              []byte
	SupplementalContent  []byte
	Metadata             []byte
	SupplementalRevision int64
	Completion           agent.MessageCompletion
	ParentSpanID         string
	ConnectorSpanID      string
	SpanID               string
	SpanType             string
	Closing              bool
	SpanColor            int32
	MarkType             leapmuxv1.MarkType
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
	SpansOpenAtPersist []SpanOpen
}

type SpanOpen struct {
	SpanID       string
	ParentSpanID string
}

// liveSpansLocked snapshots the spans the REAL tracker holds open, in column
// order. The service layer derives a row's span_lines from exactly that state,
// so this is the observable that pins the ordering rule a provider must follow.
func (s *Sink) liveSpansLocked() []SpanOpen {
	active := s.tracker.ActiveSpans()
	if len(active) == 0 {
		return nil
	}
	open := make([]SpanOpen, 0, len(active))
	for _, a := range active {
		open = append(open, SpanOpen{SpanID: a.SpanID, ParentSpanID: s.tracker.ParentOf(a.SpanID)})
	}
	return open
}

// PersistMessage records the row. Set PersistErr to make it fail, which is the
// only way to exercise a caller's error path: every caller LOGS the error and
// carries on, so a test that cannot make the persist fail cannot tell "carries
// on" from "returns early".
func (s *Sink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, Message{Source: source, Content: append([]byte(nil), content.Original...), SupplementalContent: append([]byte(nil), content.Supplemental...),
		Metadata: append([]byte(nil), content.Metadata...), Completion: content.Completion, ParentSpanID: span.ParentSpanID, ConnectorSpanID: span.ConnectorSpanID, SpanID: span.SpanID, SpanType: span.SpanType, Closing: span.Closing, SpanColor: span.SpanColor, MarkType: span.MarkType, NoSpan: span.NoSpan, SpansOpenAtPersist: s.liveSpansLocked()})
	return s.PersistErr
}

func (s *Sink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PersistErr != nil {
		return false, s.PersistErr
	}
	for index := len(s.messages) - 1; index >= 0; index-- {
		if change.Seq > 0 && change.Seq != int64(index+1) {
			continue
		}
		message := &s.messages[index]
		if message.SpanID == change.SpanID && message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
			// The real sink asks whether the supplement SAYS anything new, not
			// whether its bytes differ. See JSONCanonicalEqual.
			if agent.JSONCanonicalEqual(message.SupplementalContent, change.SupplementalContent) {
				return false, nil
			}
			if bytes.Equal(message.Content, change.OriginalContent) && message.SupplementalRevision == change.PreviousRevision {
				message.SupplementalContent = append([]byte(nil), change.SupplementalContent...)
				message.SupplementalRevision++
				return true, nil
			}
			break
		}
	}
	return false, nil
}

func (s *Sink) ReadToolRequest(spanID string) (*agent.StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, message := range s.messages {
		if s.spanRowMatches(message, spanID) {
			return s.storedSpanRow(index), nil
		}
	}
	return nil, nil
}

func (s *Sink) ReadToolResult(spanID string) (*agent.StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.messages) - 1; index >= 0; index-- {
		if s.spanRowMatches(s.messages[index], spanID) {
			return s.storedSpanRow(index), nil
		}
	}
	return nil, nil
}

func (s *Sink) spanRowMatches(message Message, spanID string) bool {
	return spanID != "" && message.SpanID == spanID && message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
}

// storedSpanRow copies one row out. The seq is the 1-based index, which is what the
// real store allocates for a transcript that no reseq touched. The caller holds mu.
func (s *Sink) storedSpanRow(index int) *agent.StoredMessage {
	message := s.messages[index]
	return &agent.StoredMessage{Seq: int64(index + 1), Revision: message.SupplementalRevision, Content: agent.MessageContent{
		Original: append([]byte(nil), message.Content...), Supplemental: append([]byte(nil), message.SupplementalContent...), Metadata: append([]byte(nil), message.Metadata...),
	}}
}

func (s *Sink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, Message{
		Source:              leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:             append([]byte(nil), content.Original...),
		SupplementalContent: append([]byte(nil), content.Supplemental...),
		Metadata:            append([]byte(nil), content.Metadata...),
		Completion:          content.Completion,
		ParentSpanID:        span.ParentSpanID,
		ConnectorSpanID:     span.ConnectorSpanID,
		SpanID:              span.SpanID,
		SpanType:            span.SpanType,
		Closing:             span.Closing,
		MarkType:            span.MarkType,
		TurnEnd:             true,
		SpansOpenAtPersist:  s.liveSpansLocked(),
	})
	s.turnLifecycle = append(s.turnLifecycle, "turn_end")
	return nil
}

// SetTurnState records the provider's turn flag transitions in order, so a
// provider test can assert the exact sequence it published.
func (s *Sink) SetTurnState(state agent.TurnState, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TurnActiveCalls = append(s.TurnActiveCalls, state.Active)
	s.turnStates = append(s.turnStates, state)
	s.turnSeqs = append(s.turnSeqs, seq)
	s.turnLifecycle = append(s.turnLifecycle, fmt.Sprintf("turn_active:%t", state.Active))
}

func (s *Sink) ReportInterruptIgnored() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptIgnoredReports++
}

func (s *Sink) InterruptIgnoredReports() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptIgnoredReports
}

// TurnKinds returns the queue classification of each publish, in arrival
// order. A provider that can classify its turn must not lose that fact before
// the queue computes CanSteer.
func (s *Sink) TurnKinds() []leapmuxv1.AgentInputKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]leapmuxv1.AgentInputKind, len(s.turnStates))
	for i, state := range s.turnStates {
		if state.Steerable {
			kinds[i] = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE
		}
	}
	return kinds
}

// TurnSeqs returns the ordering token of each publish, in arrival order. A
// provider test asserts these to pin that the token comes from the same
// critical section as the flag: without that, two goroutines publish out of
// order and the Worker latches a turn that is over.
func (s *Sink) TurnSeqs() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.turnSeqs...)
}

// TurnLifecycle returns the turn-end envelopes and the turn-flag transitions in
// the one order the provider produced them.
//
// The order is a REQUIREMENT on every provider, not an implementation detail:
// PersistTurnEnd hands the finished turn's tool-call count to the Worker's
// activity latch, and the clear that follows is the settle edge that spends it.
// A provider that clears first settles the agent with no count, and the client
// then rings the completion sound for a turn that used no tool. Two slices
// cannot show that, because neither records the other's position.
func (s *Sink) TurnLifecycle() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.turnLifecycle...)
}

// TurnActives returns the published turn states in order.
func (s *Sink) TurnActives() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.TurnActiveCalls...)
}

// LastTurnActive returns the most recently published turn state, and whether
// anything was published at all. A provider that never published is a distinct
// failure from one that published the wrong value: the first latches whatever
// the agent last showed, so it never clears.
func (s *Sink) LastTurnActive() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.TurnActiveCalls) == 0 {
		return false, false
	}
	return s.TurnActiveCalls[len(s.TurnActiveCalls)-1], true
}

func (s *Sink) PersistNotification(source leapmuxv1.MessageSource, content []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, Message{Source: source, Content: append([]byte(nil), content...)})
	return !s.SuppressNotificationBroadcast, nil
}

// The span methods DELEGATE to a real SpanTracker rather than re-implementing
// it. The double used to keep its own active set and span-type map, which drifted
// from the engine twice: a closed span kept its type here after the tracker
// stopped keeping it, and again after it started. Recording slices stay
// alongside, because a test still wants to assert WHICH calls were made.
func (s *Sink) OpenSpan(spanID string, parentSpanID string) {
	s.mu.Lock()
	s.openSpans = append(s.openSpans, SpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
	s.mu.Unlock()
	s.tracker.OpenSpan(spanID, parentSpanID)
}

func (s *Sink) CloseSpan(spanID string) {
	s.mu.Lock()
	s.closedSpans = append(s.closedSpans, spanID)
	s.mu.Unlock()
	s.tracker.CloseSpan(spanID)
}

// ResetSpans joins the turn lifecycle, because WHERE it falls relative to the
// clear is a requirement on every provider: the clear releases the Worker's
// input queue, and the next message must find the finished turn's spans already
// reset. A passthrough column captured before the reset draws the dead turn's
// bars beside the new message.
func (s *Sink) ResetSpans() {
	s.mu.Lock()
	s.resetSpanCount++
	s.turnLifecycle = append(s.turnLifecycle, "reset_spans")
	s.mu.Unlock()
	s.tracker.Reset()
}

func (s *Sink) SetSpanType(spanID, spanType string) {
	s.tracker.SetSpanType(spanID, spanType)
}

func (s *Sink) GetSpanType(spanID string) string {
	return s.tracker.GetSpanType(spanID)
}

// ReserveSpanColor records which spans asked for a color, and under which
// parent. A span that never opens must never reserve one either: the real
// tracker parks the reservation on its single pending slot, which blocks that
// color from the next real span. The parent is recorded because it decides the
// column the reservation is computed for, and the child transcript reserves
// under the spawn span rather than at the root.
func (s *Sink) ReserveSpanColor(spanID, parentSpanID string) int32 {
	s.mu.Lock()
	s.reservedColorSpans = append(s.reservedColorSpans, SpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
	s.mu.Unlock()
	// Delegated, so the reservation really is parked on the tracker's single
	// pending slot: a span that reserves and never opens then blocks that color
	// exactly as it would in production.
	return s.tracker.ReserveSpanColor(spanID, parentSpanID)
}

// ReservedColorSpans returns a copy of the span IDs that reserved a color.
func (s *Sink) ReservedColorSpans() []string {
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
func (s *Sink) ReservedColors() []SpanOpen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SpanOpen(nil), s.reservedColorSpans...)
}

func (s *Sink) ReportProgress(update agent.ProgressUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = append(s.progress, update)
	snapshot, changed := s.progressCount.Apply(update)
	if changed {
		s.sessionInfos = append(s.sessionInfos, map[string]interface{}{
			contracts.SessionInfoKeyThinkingTokens:     snapshot.ThinkingTokens,
			contracts.SessionInfoKeyOutputBytes:        snapshot.OutputBytes,
			contracts.SessionInfoKeyOutputBytesMinimum: snapshot.OutputBytesMinimum,
		})
	}
}

func (s *Sink) ProgressUpdates() []agent.ProgressUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.ProgressUpdate(nil), s.progress...)
}

func (s *Sink) PublishControlRequest(agent.ControlRequest) error { return nil }
func (s *Sink) CancelControlRequest(string)                      {}
func (s *Sink) UpdateSessionID(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionIDs = append(s.sessionIDs, sessionID)
}
func (s *Sink) UpdatePermissionMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissionModes = append(s.permissionModes, mode)
}
func (s *Sink) NotifyPermissionModeChanged(oldMode, newMode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modeChanges = append(s.modeChanges, ModeChange{Old: oldMode, New: newMode})
}
func (s *Sink) PersistSettingsRefresh(refresh optionmap.Map) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Split the unified refresh map back into the named fields the assertions read:
	// the three well-known axes plus every other key as an "extra". An axis the
	// provider omitted reads back as "" (absent), matching the old "" sentinel.
	options := make(map[string]string)
	for k, v := range refresh {
		switch k {
		case agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPermissionMode:
		default:
			options[k] = v
		}
	}
	s.settingsRefreshes = append(s.settingsRefreshes, SettingsRefresh{
		Model:          refresh[agent.OptionIDModel],
		Effort:         refresh[agent.OptionIDEffort],
		PermissionMode: refresh[agent.OptionIDPermissionMode],
		Options:        options,
	})
}
func (s *Sink) BroadcastStatusActive(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusActives = append(s.statusActives, sessionID)
}
func (s *Sink) BroadcastSessionInfo(info map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy the map to avoid aliasing.
	cp := make(map[string]interface{}, len(info))
	for k, v := range info {
		cp[k] = v
	}
	s.sessionInfos = append(s.sessionInfos, cp)
}

// PersistLeapMuxNotification records the worker-written notification payload.
// It keeps a copy, because the caller reuses its map.
func (s *Sink) PersistLeapMuxNotification(info map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]interface{}, len(info))
	for k, v := range info {
		cp[k] = v
	}
	s.leapMuxNotifications = append(s.leapMuxNotifications, cp)
}

func (s *Sink) PersistSubagentReport(write agent.SubagentReportWrite) (bool, error) {
	payload, err := write.NotificationPayload()
	if err != nil || payload == nil {
		return false, err
	}
	s.mu.Lock()
	if s.reportIDs == nil {
		s.reportIDs = make(map[string]struct{})
	}
	reportID := strings.TrimSpace(write.ReportID)
	if _, duplicate := s.reportIDs[reportID]; duplicate {
		s.mu.Unlock()
		return false, nil
	}
	s.reportIDs[reportID] = struct{}{}
	s.mu.Unlock()
	s.PersistLeapMuxNotification(payload)
	return true, nil
}
func (s *Sink) PersistChildSubagentReport(write agent.ChildSubagentReportWrite) (bool, error) {
	rowKey := strings.TrimSpace(write.RowKey)
	if rowKey == "" {
		return false, fmt.Errorf("child subagent report has no row key")
	}
	childID, _, found, err := s.LookupBackgroundTask(rowKey)
	if err != nil {
		return false, err
	}
	if !found || childID == "" {
		return false, fmt.Errorf("subagent report child for row %q is unavailable", rowKey)
	}
	return s.ChildSink(childID).PersistSubagentReport(write.Write)
}
func (s *Sink) StorePlanModeToolUse(toolUseID, targetMode string) {
	s.planModeToolUses.Store(toolUseID, targetMode)
}

func (s *Sink) LoadAndDeletePlanModeToolUse(toolUseID string) (string, bool) {
	v, ok := s.planModeToolUses.LoadAndDelete(toolUseID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (s *Sink) UpdatePlan([]byte, leapmuxv1.ContentCompression, string) {}

// ownsGoal mirrors the production sink: only a ROOT sink may write a goal, and
// a child sink refuses and records nothing.
//
// The fake has to refuse too, or it hides the bug the production guard exists
// to catch. A provider that wrote a goal through a child sink would replace the
// session's objective with a subagent's; with an accepting fake, every
// provider's goal test still passes and only one service-level test could see
// it. A child Sink carries the child id this sink was created for.
func (s *Sink) ownsGoal() bool { return s.childAgentID() == "" }

// UpsertGoal and ClearGoal record what a provider reported, so a parser test
// asserts against the neutral GoalUpdate rather than the provider's wire bytes.
func (s *Sink) UpsertGoal(update agent.GoalUpdate) {
	if !s.ownsGoal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goals = append(s.goals, update)
	s.currentGoal = &update
}

func (s *Sink) UpdateGoalStatus(expected, status agent.GoalStatus) {
	if !s.ownsGoal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentGoal == nil || s.currentGoal.Status != expected {
		return
	}
	update := *s.currentGoal
	update.Status = status
	update.StatusDetail = ""
	s.currentGoal = &update
	s.goals = append(s.goals, update)
}

func (s *Sink) ClearGoal(snapshot bool) {
	if !s.ownsGoal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentGoal = nil
	s.goalClears++
	s.goalClearSnapshots = append(s.goalClearSnapshots, snapshot)
}

// PublishGoalCapabilities is counted rather than ignored: the Manager calls it
// once per start, and a provider test asserting the goal path needs to see that
// the capability was published after registration rather than during it.
func (s *Sink) PublishGoalCapabilities() {
	if !s.ownsGoal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalCapabilityPublishes++
}

// GoalClearSnapshots returns the snapshot flag of every ClearGoal, in order.
func (s *Sink) GoalClearSnapshots() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.goalClearSnapshots...)
}

// GoalCapabilityPublishes counts the PublishGoalCapabilities calls.
func (s *Sink) GoalCapabilityPublishes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalCapabilityPublishes
}

// Goals returns the goal reports in arrival order.
func (s *Sink) Goals() []agent.GoalUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.GoalUpdate(nil), s.goals...)
}

// LastGoal returns the most recent goal report, or false when none arrived.
func (s *Sink) LastGoal() (agent.GoalUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.goals) == 0 {
		return agent.GoalUpdate{}, false
	}
	return s.goals[len(s.goals)-1], true
}

// GoalClears counts the ClearGoal calls.
func (s *Sink) GoalClears() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalClears
}
func (s *Sink) ScheduleAutoContinue(schedule agent.AutoContinueSchedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoSchedules = append(s.autoSchedules, schedule)
}
func (s *Sink) CancelAutoContinue(reason agent.AutoContinueReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoCancels = append(s.autoCancels, reason)
}

// --- Subagent transcripts and the background-task registry (test recording) ---

// EnsureChildAgent returns a stable synthetic child id keyed by spawnSpanID so
// provider tests can assert child resolution without a DB. The same span always
// resolves to the same id (idempotent replay).
func (s *Sink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
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
		s.children = make(map[string]*Sink)
	}
	if c, ok := s.children[spawnSpanID]; ok {
		return c.childAgentID(), nil
	}
	cid := "child-of-" + spawnSpanID
	child := &Sink{}
	child.setChildAgentID(cid)
	// Recorded BEFORE the child takes the pointer, so the child inherits a table
	// that already exists. The child shares that table and the injected read
	// failure, so a child handle answers exactly as the root does -- production
	// reaches the same row through the same query whichever sink holds it.
	s.recordSpawnSpan(cid, spawnSpanID)
	child.spawnSpans = s.spawnSpans
	child.SpawnSpanErr = s.SpawnSpanErr
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

// childAgentID is the synthetic id this Sink reports for itself when it is
// used as a child sink (set by EnsureChildAgent). The field is read under the
// childSinkMu of the PARENT sink, so it lives here on the child as its own lock
// to avoid a parent->child lock ordering hazard.
func (s *Sink) childAgentID() string {
	s.childIDMu.Lock()
	defer s.childIDMu.Unlock()
	return s.childIDVal
}

func (s *Sink) setChildAgentID(id string) {
	s.childIDMu.Lock()
	defer s.childIDMu.Unlock()
	s.childIDVal = id
}

// recordSpawnSpan files the span EnsureChildAgent created a child for, in the
// table every sink of this tree shares. Caller must hold s.childSinkMu, which is
// what makes the lazy creation safe.
func (s *Sink) recordSpawnSpan(childAgentID, span string) {
	if s.spawnSpans == nil {
		s.spawnSpans = &childSpawnSpanTable{}
	}
	s.spawnSpans.mu.Lock()
	defer s.spawnSpans.mu.Unlock()
	if s.spawnSpans.byChildID == nil {
		s.spawnSpans.byChildID = make(map[string]string)
	}
	s.spawnSpans.byChildID[childAgentID] = span
}

// ChildSpawnSpan mirrors the real sink: production runs a PRIMARY KEY read that
// ignores which sink asks (TestChildSpawnSpan_AnswersFromTheChildRow pins it by
// asking a sink built from a fresh OutputHandler), so this answers from the
// shared table rather than from the receiver's own `children`. A per-sink scan
// answered "" for a child sink asking about itself, and it read whichever entry
// Go's random map order reached first once ChildSink minted a "late:" child
// carrying the same id.
//
// The empty id comes FIRST, as production has it: an empty id asks nothing of
// the database, so no read can fail for one.
func (s *Sink) ChildSpawnSpan(childAgentID string) (string, error) {
	if childAgentID == "" {
		return "", nil
	}
	if s.SpawnSpanErr != nil {
		return "", s.SpawnSpanErr
	}
	if s.spawnSpans == nil {
		return "", nil
	}
	s.spawnSpans.mu.Lock()
	defer s.spawnSpans.mu.Unlock()
	return s.spawnSpans.byChildID[childAgentID], nil
}

// ChildSink returns the services of Child(childAgentID), so messages routed into
// a subagent transcript are recorded on a distinct sink a test can assert against.
func (s *Sink) ChildSink(childAgentID string) agent.ProviderServices {
	return agent.NewProviderServices(s.Child(childAgentID))
}

// Child returns the per-child Sink created by EnsureChildAgent, or a fresh
// empty one for a child id that EnsureChildAgent never created.
func (s *Sink) Child(childAgentID string) *Sink {
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	for _, c := range s.children {
		if c.childAgentID() == childAgentID {
			return c
		}
	}
	// A child id that was not EnsureChildAgent'd (e.g. an explicit PersistChild*):
	// create a fresh recording sink so the call still records.
	c := &Sink{}
	c.setChildAgentID(childAgentID)
	if s.children == nil {
		s.children = make(map[string]*Sink)
	}
	s.children["late:"+childAgentID] = c
	return c
}

func (s *Sink) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	cs := s.Child(childAgentID)
	return cs.PersistMessage(source, agent.MessageContent{Original: content}, span)
}

func (s *Sink) PersistChildTurnEnd(childAgentID string, content agent.MessageContent, span agent.SpanInfo) error {
	cs := s.Child(childAgentID)
	return cs.PersistTurnEnd(content, span)
}

// PersistChildPrompt mirrors the real sink's contract: a USER message carrying
// {"content": prompt}, written only into a child transcript that has no message
// yet, and skipped for a blank prompt. Tests assert on the child's Messages().
func (s *Sink) PersistChildPrompt(childAgentID, prompt string) error {
	if childAgentID == "" || strings.TrimSpace(prompt) == "" {
		return nil
	}
	cs := s.Child(childAgentID)
	if len(cs.Messages()) > 0 {
		return nil
	}
	content, err := json.Marshal(map[string]string{"content": prompt})
	if err != nil {
		return err
	}
	return cs.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: content}, agent.SpanInfo{})
}

// PersistChildUserMessage mirrors the real sink: it APPENDS, with no emptiness
// guard, and carries the scroll-rail mark the opening prompt does not. The
// missing guard is the whole difference from PersistChildPrompt above -- a
// delivered message belongs wherever the transcript currently ends.
func (s *Sink) PersistChildUserMessage(childAgentID, text string) error {
	if childAgentID == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	cs := s.Child(childAgentID)
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	return cs.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: content}, agent.SpanInfo{
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

func (s *Sink) UpsertBackgroundTask(task bgtask.Upsert) error {
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
	s.recordBgTaskStatusLocked(task.RowKey, item.Status)
	return nil
}

func (s *Sink) recordBgTaskStatusLocked(rowKey string, status bgtask.Status) {
	if s.bgTaskStatuses == nil {
		s.bgTaskStatuses = make(map[string][]bgtask.Status)
	}
	log := s.bgTaskStatuses[rowKey]
	if len(log) > 0 && log[len(log)-1] == status {
		return
	}
	s.bgTaskStatuses[rowKey] = append(log, status)
}

func (s *Sink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
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
		s.recordBgTaskStatusLocked(rowKey, status)
	}
	return nil
}

func (s *Sink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if s.OnCloseBackgroundTask != nil {
		s.OnCloseBackgroundTask(rowKey, status)
	}
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
		s.recordBgTaskStatusLocked(rowKey, status)
	}
	return nil
}

func (s *Sink) LookupBackgroundTask(rowKey string) (string, bgtask.Status, bool, error) {
	var noStatus bgtask.Status
	if s.LookupErr != nil {
		return "", noStatus, false, s.LookupErr
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
func (s *Sink) ReviveBackgroundTask(rowKey string) error {
	if s.ReviveErr != nil {
		return s.ReviveErr
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
	s.recordBgTaskStatusLocked(rowKey, item.Status)
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
func (s *Sink) UnlinkBackgroundTask(rowKey string) {
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
func (s *Sink) ChildAgentIDs() []string {
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	ids := make([]string, 0, len(s.children))
	for _, c := range s.children {
		ids = append(ids, c.childAgentID())
	}
	return ids
}

// RevivedTasks returns the row keys ReviveBackgroundTask actually reopened.
func (s *Sink) RevivedTasks() []string {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	return append([]string(nil), s.revivedTasks...)
}

func (s *Sink) RenameBackgroundTask(oldKey, newKey string) error {
	// The empty-key no-op FIRST, as production does: NormalizeRowKey("") derives a
	// digest, so normalizing first would rename onto a key no registry stores.
	if oldKey == "" || newKey == "" {
		return nil
	}
	// BOTH keys, as production does: normalizing one and not the other is how a
	// rename stops finding its own row.
	oldKey, newKey = bgtask.NormalizeRowKey(oldKey), bgtask.NormalizeRowKey(newKey)
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	item, ok := s.bgTasks[oldKey]
	if !ok || oldKey == newKey {
		return nil
	}
	// (owner, row_key) is the PRIMARY KEY, so production resolves a rename onto an
	// OCCUPIED key by keeping the row already at newKey -- it carries the lifecycle
	// that reached the rename -- and dropping the duplicate at oldKey. A fake that
	// overwrote the destination instead let a test assert the opposite outcome
	// under the same row key, which is exactly what the key-set assertions cannot
	// see: Claude's restart rename reaches this collision on every reorder.
	if _, occupied := s.bgTasks[newKey]; occupied {
		delete(s.bgTasks, oldKey)
		return nil
	}
	delete(s.bgTasks, oldKey)
	item.RowKey = newKey
	s.bgTasks[newKey] = item
	return nil
}

// RowKeys lists the registry row keys this sink holds, sorted. The KEYS
// and not the count: a duplicate row is only recognizable by the key it took.
func RowKeys(sink *Sink) []string {
	rows := sink.BackgroundTasks()
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.RowKey)
	}
	return keys
}

// CleanupChildAgent is a no-op on the test fake: tests that exercise the
// per-child cleanup use the real OutputHandler via svc.Output.NewSink.
func (s *Sink) CleanupChildAgent(childAgentID string) {}

// BackgroundTasks returns a snapshot of the recorded registry rows, SORTED by
// row key. The rows live in a map, so an unsorted snapshot ordered them at
// random and every caller that wanted a specific row scanned the slice by hand.
// A stable order lets a test assert the whole list.
func (s *Sink) BackgroundTasks() []bgtask.Item {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	out := make([]bgtask.Item, 0, len(s.bgTasks))
	for _, item := range s.bgTasks {
		out = append(out, item)
	}
	slices.SortFunc(out, func(a, b bgtask.Item) int { return cmp.Compare(a.RowKey, b.RowKey) })
	return out
}

// MessageCount returns the number of persisted messages.
func (s *Sink) MessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *Sink) NotificationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notifications)
}

func (s *Sink) LastNotification() Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifications[len(s.notifications)-1]
}

// PersistedNotifications returns a snapshot of every PersistNotification
// call in order. Distinct from ControlSink.Notifications, which
// captures PersistLeapMuxNotification map payloads.
func (s *Sink) PersistedNotifications() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.notifications...)
}

// LeapMuxNotifications returns a snapshot of every PersistLeapMuxNotification
// call in order.
func (s *Sink) LeapMuxNotifications() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]interface{}(nil), s.leapMuxNotifications...)
}

// Messages returns a copy of all persisted messages.
func (s *Sink) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// SessionIDCount returns the number of UpdateSessionID calls.
func (s *Sink) SessionIDCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessionIDs)
}

// LastSessionID returns the most recently recorded session ID.
func (s *Sink) LastSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionIDs) == 0 {
		return ""
	}
	return s.sessionIDs[len(s.sessionIDs)-1]
}

func (s *Sink) SettingsRefreshCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.settingsRefreshes)
}

func (s *Sink) StatusActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.statusActives)
}

func (s *Sink) ModeChanges() []ModeChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ModeChange(nil), s.modeChanges...)
}

func (s *Sink) LastSettingsRefresh() SettingsRefresh {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsRefreshes[len(s.settingsRefreshes)-1]
}

func (s *Sink) PermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.permissionModes) == 0 {
		return ""
	}
	return s.permissionModes[len(s.permissionModes)-1]
}

// SessionInfoCount returns the number of BroadcastSessionInfo calls.
func (s *Sink) SessionInfoCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessionInfos)
}

// LastSessionInfo returns the most recently recorded session info.
func (s *Sink) LastSessionInfo() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessionInfos) == 0 {
		return nil
	}
	return s.sessionInfos[len(s.sessionInfos)-1]
}

// OpenSpans returns a copy of all opened span IDs.
func (s *Sink) OpenSpans() []SpanOpen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SpanOpen(nil), s.openSpans...)
}

// ClosedSpans returns a copy of all closed span IDs.
func (s *Sink) ClosedSpans() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.closedSpans...)
}

// ClosedSpanCount returns the number of CloseSpan calls.
func (s *Sink) ClosedSpanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.closedSpans)
}

func (s *Sink) ResetSpanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetSpanCount
}

func (s *Sink) AutoScheduleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.autoSchedules)
}

func (s *Sink) LastAutoSchedule() agent.AutoContinueSchedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoSchedules[len(s.autoSchedules)-1]
}

func (s *Sink) AutoCancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.autoCancels)
}

func (s *Sink) LastAutoCancel() agent.AutoContinueReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCancels[len(s.autoCancels)-1]
}

// nop is a no-op implementation of ServiceFacets.
type nop struct{}

var _ agent.ServiceFacets = nop{}

// Nop returns provider services that discard all output.
func Nop() agent.ProviderServices { return agent.NewProviderServices(nop{}) }

func (nop) PersistMessage(leapmuxv1.MessageSource, agent.MessageContent, agent.SpanInfo) error {
	return nil
}
func (nop) EnrichMessage(agent.MessageEnrichment) (bool, error)               { return false, nil }
func (nop) ReadToolRequest(string) (*agent.StoredMessage, error)              { return nil, nil }
func (nop) ReadToolResult(string) (*agent.StoredMessage, error)               { return nil, nil }
func (nop) PersistTurnEnd(agent.MessageContent, agent.SpanInfo) error         { return nil }
func (nop) SetTurnState(agent.TurnState, uint64)                              {}
func (nop) ReportInterruptIgnored()                                           {}
func (nop) PersistNotification(leapmuxv1.MessageSource, []byte) (bool, error) { return true, nil }
func (nop) OpenSpan(string, string)                                           {}
func (nop) CloseSpan(string)                                                  {}
func (nop) ResetSpans()                                                       {}
func (nop) SetSpanType(string, string)                                        {}
func (nop) GetSpanType(string) string                                         { return "" }
func (nop) ReserveSpanColor(string, string) int32                             { return 0 }
func (nop) ReportProgress(agent.ProgressUpdate)                               {}
func (nop) PublishControlRequest(agent.ControlRequest) error                  { return nil }
func (nop) CancelControlRequest(string)                                       {}
func (nop) UpdateSessionID(string)                                            {}
func (nop) UpdatePermissionMode(string)                                       {}
func (nop) NotifyPermissionModeChanged(string, string)                        {}
func (nop) PersistSettingsRefresh(optionmap.Map)                              {}
func (nop) BroadcastStatusActive(string)                                      {}
func (nop) BroadcastSessionInfo(map[string]interface{})                       {}
func (nop) PersistLeapMuxNotification(map[string]interface{})                 {}
func (nop) PersistSubagentReport(write agent.SubagentReportWrite) (bool, error) {
	payload, err := write.NotificationPayload()
	return payload != nil, err
}
func (nop) PersistChildSubagentReport(agent.ChildSubagentReportWrite) (bool, error) { return true, nil }
func (nop) StorePlanModeToolUse(string, string)                                     {}
func (nop) LoadAndDeletePlanModeToolUse(string) (string, bool)                      { return "", false }
func (nop) UpdatePlan([]byte, leapmuxv1.ContentCompression, string)                 {}
func (nop) UpsertGoal(agent.GoalUpdate)                                             {}
func (nop) UpdateGoalStatus(agent.GoalStatus, agent.GoalStatus)                     {}
func (nop) ClearGoal(bool)                                                          {}
func (nop) PublishGoalCapabilities()                                                {}
func (nop) ScheduleAutoContinue(agent.AutoContinueSchedule)                         {}
func (nop) CancelAutoContinue(agent.AutoContinueReason)                             {}
func (nop) EnsureChildAgent(string, string, string) (string, error)                 { return "", nil }
func (nop) ChildSpawnSpan(string) (string, error)                                   { return "", nil }
func (nop) ChildSink(string) agent.ProviderServices                                 { return Nop() }
func (nop) PersistChildMessage(string, leapmuxv1.MessageSource, []byte, agent.SpanInfo) error {
	return nil
}
func (nop) PersistChildTurnEnd(string, agent.MessageContent, agent.SpanInfo) error { return nil }
func (nop) PersistChildPrompt(string, string) error                                { return nil }
func (nop) UpsertBackgroundTask(bgtask.Upsert) error                               { return nil }
func (nop) UpdateBackgroundTaskStatus(string, bgtask.Status, string) error {
	return nil
}
func (nop) CloseBackgroundTask(string, bgtask.Status) error { return nil }
func (nop) RenameBackgroundTask(string, string) error       { return nil }
func (nop) LookupBackgroundTask(string) (string, bgtask.Status, bool, error) {
	var noStatus bgtask.Status
	return "", noStatus, false, nil
}
func (nop) ReviveBackgroundTask(string) error            { return nil }
func (nop) PersistChildUserMessage(string, string) error { return nil }
func (nop) CleanupChildAgent(string)                     {}

// LastSessionInfoValue returns the most recent value recorded for a session-info
// key, and whether any payload carried it.
func (s *Sink) LastSessionInfoValue(key string) (interface{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sessionInfos) - 1; i >= 0; i-- {
		if v, ok := s.sessionInfos[i][key]; ok {
			return v, true
		}
	}
	return nil, false
}

// SessionInfoValues returns every value recorded for a session-info key, in
// send order.
func (s *Sink) SessionInfoValues(key string) []interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []interface{}
	for _, info := range s.sessionInfos {
		if value, ok := info[key]; ok {
			values = append(values, value)
		}
	}
	return values
}

// LastThinkingTokens returns the most recently broadcast thinking_tokens value,
// or -1 when none has been broadcast yet. -1 (not 0) is the sentinel so a test
// can tell "never broadcast" apart from a real 0.
func (s *Sink) LastThinkingTokens() int64 {
	if v, ok := s.LastSessionInfoValue(contracts.SessionInfoKeyThinkingTokens); ok {
		return v.(int64)
	}
	return -1
}

// SessionInfos returns every recorded session-info payload, in order.
func (s *Sink) SessionInfos() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sessionInfos)
}

// StatusActives returns every recorded status-active broadcast, in order.
func (s *Sink) StatusActives() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.statusActives)
}

// SessionIDs returns every recorded session id, in order.
func (s *Sink) SessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sessionIDs)
}

// PermissionModes returns every recorded permission mode, in order.
func (s *Sink) PermissionModes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.permissionModes)
}

// ProgressSnapshot returns the snapshot of every progress update the sink
// counted.
func (s *Sink) ProgressSnapshot() agent.ProgressSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressCount.Snapshot()
}

// BackgroundTask returns the latest registry state of rowKey, and whether the
// sink holds a row for it.
func (s *Sink) BackgroundTask(rowKey string) (bgtask.Item, bool) {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	row, ok := s.bgTasks[rowKey]
	return row, ok
}

// BackgroundTaskStatuses returns the distinct statuses rowKey took, in order.
func (s *Sink) BackgroundTaskStatuses(rowKey string) []bgtask.Status {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	return slices.Clone(s.bgTaskStatuses[rowKey])
}
