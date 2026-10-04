package agenttest

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
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

// Sink is a test implementation of ProviderServices that records calls.
type Sink struct {
	// nativeChildKey belongs to this sink's direct parent and uses that parent's childSinkMu.
	nativeChildKey string
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
	// TurnActiveCalls records each SetTurnState value in order.
	// A provider must publish its turn-end envelope before it clears the active flag.
	// A missing or early clear can leave the agent busy forever.
	TurnActiveCalls []bool
	// turnKinds records the queue classification that accompanied each turn
	// state.
	turnStates []agent.TurnState
	// interruptIgnoredReports records when a provider proves that an accepted
	// interrupt did not end its turn.
	interruptIgnoredReports int
	// requeuedInputs records each RequeueDroppedInput call in order, including repeats.
	// The Worker ignores a repeated drop.
	// The test sink retains repeats so a test can detect the provider defect.
	requeuedInputs []RequeuedInput
	// RequeueErr makes RequeueDroppedInput return an error instead of recording the call.
	// The provider must report that the queue refused the input.
	// Set this field at construction. Reads require no lock.
	RequeueErr error
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
	// goals records each UpsertGoal in arrival order. goalClears counts ClearGoal calls.
	// Both fields retain neutral GoalUpdate values.
	// A parser test can check the meaning without reading native protocol bytes.
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
	children    map[childSinkIdentity]*Sink
	childIDMu   sync.Mutex
	childIDVal  string
	// spawnSpans records this sink's direct children under childSinkMu.
	// The Worker requires the same direct parent when it reads a child row.
	spawnSpans map[string]string
	// bgTasks records the latest registry state per row key (owner == this sink).
	bgTasks map[string]bgtask.Item
	// bgTaskStatuses records distinct status values per row key, in order.
	// A fast-exiting shell can Close before a test reads bgTasks, so the
	// trail is the only way to prove Running landed first. Duplicate writes
	// (no-op upsert, absorbed reject) are skipped so length asserts stay
	// meaningful.
	bgTaskStatuses map[string][]bgtask.Status
	// OnCloseBackgroundTask runs before CloseBackgroundTask takes the lock.
	// A test can observe the process state when a row reaches its final status.
	// Set this hook before the first close.
	OnCloseBackgroundTask func(rowKey string, status bgtask.Status)
	// revivedTasks records every row key ReviveBackgroundTask actually reopened,
	// in order. The effect alone cannot prove the call: a revive leaves the row
	// running, which is also how it looked before it ever finished.
	revivedTasks []string
	// ReviveErr makes ReviveBackgroundTask return an error without reopening the row.
	// A test can reach the failure path where a late message cannot reopen a finished task.
	// Set this field at construction. Reads require no lock.
	ReviveErr error
	// LookupErr, when set, is what LookupBackgroundTask returns instead of an
	// answer -- the "registry unreadable" third case, which a miss cannot stand
	// in for. Read without the lock: set at construction.
	LookupErr error
	// lookups counts the LookupBackgroundTask calls for each row key, so a test
	// can prove that a caller keeps an answer rather than reading it again.
	// Guarded by bgTasksMu.
	lookups map[string]int
	// SpawnSpanErr makes ChildSpawnSpan return an error instead of a span.
	// This differs from a missing child, as LookupErr differs from a missing registry row.
	// A restart test can check the path where the spawn span cannot be read.
	// Set this field at construction. Reads require no lock.
	SpawnSpanErr error
	bgTasksMu    sync.Mutex
	// SuppressNotificationBroadcast makes PersistNotification report false. It
	// simulates the service layer that folds a changing notification into an
	// existing thread tail. The zero value reports a broadcast.
	SuppressNotificationBroadcast bool
	// reportIDs models the database uniqueness rule for provider-neutral reports.
	reportIDs   map[string]struct{}
	messageKeys map[nativeMessageIdentity]struct{}
	turnEndKeys map[nativeMessageIdentity]struct{}
}

type nativeMessageIdentity struct{ sessionID, key string }

// Sink implements the service facets. A test passes it to a provider through
// NewProviderServices, as the production sink does.
var _ agent.ServiceFacets = (*Sink)(nil)

type Message struct {
	Source               leapmuxv1.MessageSource
	AgentSessionID       string
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
	// NoSpan mirrors SpanInfo.NoSpan.
	// The row carries a span ID but owns no span.
	// Its span_color stays zero. Persistence must not copy a color from the connector.
	NoSpan bool
	// TurnEnd is set on entries recorded by PersistTurnEnd so tests can
	// distinguish the turn-end divider from regular AGENT messages
	// without inspecting the inner content.
	TurnEnd bool
	// SpansOpenAtPersist records the open spans when the message reaches persistence.
	// The Worker derives span_lines from the same state.
	// Persist tool_use before its span opens, with empty span_lines.
	// Persist tool_result while its span remains open, with connector_end.
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

// PersistMessage records each write attempt. Set PersistErr to exercise the provider's failure path.
// Failed attempts remain observable but claim no native message key.
func (s *Sink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistMessageLocked(source, content, span, false)
}

// persistMessageLocked records failed attempts but claims only successful native keys. The caller holds mu.
func (s *Sink) persistMessageLocked(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo, turnEnd bool) error {
	identity := s.messageIdentityLocked(content)
	if identity.key != "" && s.PersistErr == nil {
		if _, exists := s.messageKeys[identity]; exists {
			return nil
		}
		if s.messageKeys == nil {
			s.messageKeys = make(map[nativeMessageIdentity]struct{})
		}
		s.messageKeys[identity] = struct{}{}
	}
	s.messages = append(s.messages, Message{Source: source, AgentSessionID: identity.sessionID, Content: append([]byte(nil), content.Original...), SupplementalContent: append([]byte(nil), content.Supplemental...),
		Metadata: append([]byte(nil), content.Metadata...), Completion: content.Completion, ParentSpanID: span.ParentSpanID, ConnectorSpanID: span.ConnectorSpanID, SpanID: span.SpanID, SpanType: span.SpanType, Closing: span.Closing, SpanColor: span.SpanColor, MarkType: span.MarkType, NoSpan: span.NoSpan, TurnEnd: turnEnd, SpansOpenAtPersist: s.liveSpansLocked()})
	return s.PersistErr
}

// messageIdentityLocked uses the message's native session or the sink's current session. The caller holds mu.
func (s *Sink) messageIdentityLocked(content agent.MessageContent) nativeMessageIdentity {
	identity := nativeMessageIdentity{sessionID: content.AgentSessionID, key: content.IdempotencyKey}
	if identity.sessionID == "" {
		identity.sessionID = s.currentSessionIDLocked()
	}
	return identity
}

// currentSessionIDLocked returns the current native session. The caller holds mu.
func (s *Sink) currentSessionIDLocked() string {
	if len(s.sessionIDs) == 0 {
		return ""
	}
	return s.sessionIDs[len(s.sessionIDs)-1]
}

func (s *Sink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if change.SpanID == "" || change.Seq < 0 || change.PreviousRevision < 0 {
		return false, nil
	}
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
		if change.Seq == 0 && message.AgentSessionID != s.currentSessionIDLocked() {
			continue
		}
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
	return spanID != "" && message.SpanID == spanID && message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && message.AgentSessionID == s.currentSessionIDLocked()
}

// storedSpanRow copies one row out. The seq is the 1-based index, which is what the
// real store allocates for a transcript that no reseq touched. The caller holds mu.
func (s *Sink) storedSpanRow(index int) *agent.StoredMessage {
	message := s.messages[index]
	return &agent.StoredMessage{Seq: int64(index + 1), Revision: message.SupplementalRevision, Content: agent.MessageContent{
		AgentSessionID: message.AgentSessionID, Original: append([]byte(nil), message.Content...), Supplemental: append([]byte(nil), message.SupplementalContent...), Metadata: append([]byte(nil), message.Metadata...),
	}}
}

func (s *Sink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity := s.messageIdentityLocked(content)
	if identity.key != "" && s.PersistErr == nil {
		if _, exists := s.turnEndKeys[identity]; exists {
			return nil
		}
	}
	err := s.persistMessageLocked(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span, true)
	if err != nil {
		return err
	}
	if identity.key != "" {
		if s.turnEndKeys == nil {
			s.turnEndKeys = make(map[nativeMessageIdentity]struct{})
		}
		s.turnEndKeys[identity] = struct{}{}
	}
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

// RequeuedInput is one input that a provider handed back through
// RequeueDroppedInput.
type RequeuedInput struct {
	DropID      string
	Content     string
	Attachments []*leapmuxv1.Attachment
	// TurnActive is the last turn state that the provider published before the
	// call, and false when it published none.
	TurnActive bool
}

// RequeueDroppedInput records the input that the provider handed back. It also
// records the call in TurnLifecycle as `requeue:<dropID>`, so a test can assert
// that the input reached the queue before the turn ended.
func (s *Sink) RequeueDroppedInput(dropID, content string, attachments []*leapmuxv1.Attachment) error {
	if s.RequeueErr != nil {
		return s.RequeueErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := len(s.TurnActiveCalls) > 0 && s.TurnActiveCalls[len(s.TurnActiveCalls)-1]
	s.requeuedInputs = append(s.requeuedInputs, RequeuedInput{DropID: dropID, Content: content, Attachments: attachments, TurnActive: active})
	s.turnLifecycle = append(s.turnLifecycle, "requeue:"+dropID)
	return nil
}

// RequeuedInputs returns every RequeueDroppedInput call that the sink took, in
// order.
func (s *Sink) RequeuedInputs() []RequeuedInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequeuedInput(nil), s.requeuedInputs...)
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

// TurnSeqs returns each published ordering token in arrival order.
// The provider must compute the token and active flag in one critical section.
// Otherwise, concurrent publishers can leave the Worker with an active flag for a finished turn.
func (s *Sink) TurnSeqs() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.turnSeqs...)
}

// TurnLifecycle returns turn-end envelopes, flag changes, and returned input in their combined arrival order.
//
// Every provider must preserve this order.
// PersistTurnEnd supplies the finished turn's tool count to the Worker activity latch.
// The following clear completes the turn with that count.
// An early clear loses the count and causes a completion sound for a turn that used no tool.
// Separate slices cannot show the relative order.
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

// LastTurnActive returns the last published turn state and whether any state exists.
// A missing publish differs from an incorrect value.
// A provider that never publishes a clear leaves the previous active flag in place.
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

// The span methods call the real SpanTracker.
// The previous local active set and type map drifted twice from the tracker.
// They retained a closed span's type after the tracker removed it.
// They drifted again after the tracker began to retain it.
// Separate recording slices let tests check each call.
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

// ResetSpans records its position in the turn lifecycle.
// The provider must reset the finished turn's spans before it clears the active flag.
// That clear releases the Worker input queue.
// Otherwise, a passthrough column can draw the finished turn's span beside the next message.
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

// ReserveSpanColor records the requested span and its parent.
// A span that never opens must never reserve a color.
// The tracker retains one pending reservation, which prevents the next span from using that color.
// The parent determines the column.
// A child transcript reserves its color under the spawn span.
func (s *Sink) ReserveSpanColor(spanID, parentSpanID string) int32 {
	s.mu.Lock()
	s.reservedColorSpans = append(s.reservedColorSpans, SpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
	s.mu.Unlock()
	// The real tracker retains this reservation in its pending slot.
	// A span that reserves a color without opening prevents the next span from using that color.
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

// ownsGoal matches the Worker: only a root sink can write a goal.
// A child sink refuses the write and records nothing.
// An accepting test sink would hide a provider that replaces the session objective with a child objective.
// Only the service test would detect that defect.
// A child Sink retains its child ID.
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

// PublishGoalCapabilities records each call.
// The Manager calls it once per start.
// A provider test must check that publication follows registration.
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

type childSinkIdentity struct{ spanID, providerKey, lateChildID string }

// EnsureChildAgent resolves a child by its spawn span or native key without a database.
// Replaying the same identity returns the same child ID.
func (s *Sink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if spec.SpawnSpanID == "" && spec.ProviderChildKey == "" {
		return "", errors.New("a child requires a native key or a spawn span")
	}
	rowKey := ""
	if spec.ProviderChildKey != "" {
		rowKey = bgtask.NormalizeRowKey(spec.ProviderChildKey)
	}
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	if s.children == nil {
		s.children = make(map[childSinkIdentity]*Sink)
	}
	var child *Sink
	if rowKey != "" {
		s.bgTasksMu.Lock()
		childID := s.bgTasks[rowKey].ChildAgentID
		s.bgTasksMu.Unlock()
		for _, candidate := range s.children {
			if childID != "" && candidate.childAgentID() == childID {
				child = candidate
				break
			}
		}
	}
	identity := childSinkIdentity{spanID: spec.SpawnSpanID}
	if spec.SpawnSpanID == "" {
		identity = childSinkIdentity{providerKey: spec.ProviderChildKey}
	}
	if child == nil {
		child = s.children[identity]
	}
	if child != nil && child.nativeChildKey != "" && spec.ProviderChildKey != "" && child.nativeChildKey != spec.ProviderChildKey {
		return "", fmt.Errorf("the child spawn span belongs to another native child: %w", agent.ErrChildIdentityRefused)
	}
	if child != nil && spec.AgentSessionID != "" && child.LastSessionID() != spec.AgentSessionID {
		return "", fmt.Errorf("the child belongs to another native session: %w", agent.ErrChildIdentityRefused)
	}
	if child == nil {
		child = &Sink{nativeChildKey: spec.ProviderChildKey}
		if spec.AgentSessionID != "" {
			child.UpdateSessionID(spec.AgentSessionID)
		}
		cid := "child-of-" + spec.SpawnSpanID
		if spec.SpawnSpanID == "" {
			cid = "child-key-" + rowKey
		}
		child.setChildAgentID(cid)
		if len(spec.Options) > 0 {
			child.PersistSettingsRefresh(spec.Options)
		}
		child.SpawnSpanErr = s.SpawnSpanErr
		s.children[identity] = child
	} else if child.nativeChildKey == "" && spec.ProviderChildKey != "" {
		child.nativeChildKey = spec.ProviderChildKey
	}
	cid := child.childAgentID()
	if spec.SpawnSpanID != "" {
		if s.spawnSpans == nil {
			s.spawnSpans = make(map[string]string)
		}
		if s.spawnSpans[cid] == "" {
			s.spawnSpans[cid] = spec.SpawnSpanID
		}
	}
	if rowKey != "" {
		s.bgTasksMu.Lock()
		if s.bgTasks == nil {
			s.bgTasks = make(map[string]bgtask.Item)
		}
		item := s.bgTasks[rowKey]
		item.RowKey, item.ChildAgentID, item.Kind = rowKey, cid, bgtask.KindSubagent
		if spec.Title != "" {
			item.Title = spec.Title
		}
		s.bgTasks[rowKey] = item
		s.bgTasksMu.Unlock()
	}
	return cid, nil
}

// childAgentID returns the synthetic ID that EnsureChildAgent assigns to this child sink.
// The parent reads it while it holds childSinkMu.
// The child uses its own lock for this field, which prevents a parent-to-child lock ordering hazard.
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

// ChildSpawnSpan reads a span only for this sink's direct child, as the Worker does.
// An empty ID requires no read and returns no error.
func (s *Sink) ChildSpawnSpan(childAgentID string) (string, error) {
	if childAgentID == "" {
		return "", nil
	}
	if s.SpawnSpanErr != nil {
		return "", s.SpawnSpanErr
	}
	s.childSinkMu.Lock()
	defer s.childSinkMu.Unlock()
	return s.spawnSpans[childAgentID], nil
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
		s.children = make(map[childSinkIdentity]*Sink)
	}
	s.children[childSinkIdentity{lateChildID: childAgentID}] = c
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

// PersistChildPrompt writes a USER message with {"content": prompt} into an empty child transcript.
// It ignores a blank prompt.
// Tests read the child's Messages snapshot.
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

// PersistChildUserMessage appends to an existing child transcript, as the Worker does.
// It ignores a blank message or an empty child ID.
// Its scroll-rail mark differs from the opening prompt's mark.
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

// testFakeEndedAt supplies a fixed instant for each active-to-final transition.
// The test sink models whether ended_at exists, rather than its exact time.
// A non-final status check proved nothing when the test sink never set this field.
//
// The test sink does not model updated_at, although every production update sets it.
// LookupBackgroundTask returns a status and child ID rather than an Item.
// No provider decision can read updated_at through that method.
// A fixed instant cannot prove that a later write advances updated_at.
// The real clock would make the test sink nondeterministic.
// Test that timestamp in the service package with the real registry.
var testFakeEndedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (s *Sink) UpsertBackgroundTask(task bgtask.Upsert) error {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if s.bgTasks == nil {
		s.bgTasks = make(map[string]bgtask.Item)
	}
	// The Worker uses task.Clean().ToItem().PreservingBlanksFrom(existing).
	// Keep the same chain here.
	// Clean supplies the stored title. ToItem includes each new Upsert field.
	// PreservingBlanksFrom keeps each existing descriptive field when the update supplies a blank value.
	// Claude task_notification can supply only status and description.
	// The previous test sink cleared Title for that update, although production retained it.
	//
	// Normalize the row key before this chain, as agentOutputSink.applyAndBroadcast does.
	// An unusable native key gets a derived key rather than losing the row.
	// A test sink that retains the raw key would accept a row key that production never stores.
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
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	if s.lookups == nil {
		s.lookups = make(map[string]int)
	}
	s.lookups[rowKey]++
	if s.LookupErr != nil {
		return "", noStatus, false, s.LookupErr
	}
	if rowKey == "" {
		return "", noStatus, false, nil
	}
	rowKey = bgtask.NormalizeRowKey(rowKey)
	item, ok := s.bgTasks[rowKey]
	if !ok {
		return "", noStatus, false, nil
	}
	return item.ChildAgentID, item.Status, true, nil
}

// LookupBackgroundTaskCalls returns how many times LookupBackgroundTask read
// rowKey.
func (s *Sink) LookupBackgroundTaskCalls(rowKey string) int {
	s.bgTasksMu.Lock()
	defer s.bgTasksMu.Unlock()
	return s.lookups[rowKey]
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
	// ReviveAgentBackgroundTask clears both ActiveForm and Description.
	// These fields describe the finished run's activity and output file.
	// The test sink must clear both fields also.
	// Otherwise, a test could incorrectly accept an output path that survives a restart.
	item.ActiveForm = ""
	item.Description = ""
	item.EndedAt = time.Time{}
	s.bgTasks[rowKey] = item
	s.recordBgTaskStatusLocked(rowKey, item.Status)
	s.revivedTasks = append(s.revivedTasks, rowKey)
	return nil
}

// UnlinkBackgroundTask clears the child linkage but retains the row and child transcript.
// This models a child that EnsureChildAgent creates before its registry linkage write fails.
// A finished child can then exist without a linked transcript in the registry row.
// Display-cap eviction does not create this state.
// The stored linkage survives that cap, as registryOps.retention requires.
// Do not use eviction to test missing linkage.
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

// ChildAgentIDs lists each child transcript that this sink supplies.
// A test can detect a second transcript after resolution.
// Return child IDs rather than the spawn spans that identify the map entries.
// Returning the keys made existing NotContains checks pass without testing the expected behavior.
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
	// The Worker keeps the existing destination when a rename reaches an occupied (owner, row_key) primary key.
	// It removes the duplicate source row and retains the destination lifecycle.
	// The test sink must preserve that behavior.
	// Overwriting the destination would let tests accept the opposite result.
	// Claude restart events can reach this collision after each reorder.
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

// BackgroundTasks returns a snapshot sorted by row key.
// The map does not supply a stable order.
// Sorting lets a test check the complete list without searching for each row.
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
	if len(s.messages) == 0 {
		return nil
	}
	messages := make([]Message, len(s.messages))
	for index, message := range s.messages {
		message.Content = slices.Clone(message.Content)
		message.SupplementalContent = slices.Clone(message.SupplementalContent)
		message.Metadata = slices.Clone(message.Metadata)
		message.SpansOpenAtPersist = slices.Clone(message.SpansOpenAtPersist)
		messages[index] = message
	}
	return messages
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
	return s.currentSessionIDLocked()
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
func (nop) RequeueDroppedInput(string, string, []*leapmuxv1.Attachment) error { return nil }
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
func (nop) EnsureChildAgent(agent.ChildAgentSpec) (string, error)                   { return "", nil }
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
