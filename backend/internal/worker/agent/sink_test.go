package agent

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// testSinkSettingsRefreshed records the args of a PersistSettingsRefresh call.
type testSinkSettingsRefreshed struct {
	Model          string
	Effort         string
	PermissionMode string
	ExtraSettings  map[string]string
}

// testSink is a test implementation of OutputSink that records calls.
type testSink struct {
	mu                sync.Mutex
	messages          []testSinkMessage
	notifications     []testSinkMessage
	streamChunks      []testSinkStreamChunk
	streamEnds        []string
	sessionIDs        []string
	permissionModes   []string
	settingsRefreshes []testSinkSettingsRefreshed
	sessionInfos      []map[string]interface{}
	spanTypes         map[string]string
	openSpans         []testSinkSpanOpen
	closedSpans       []string
	resetSpanCount    int
	statusActives     []string
	autoSchedules     []AutoContinueSchedule
	autoCancels       []AutoContinueReason
	planModeToolUses  sync.Map
}

type testSinkMessage struct {
	Role            leapmuxv1.MessageRole
	Content         []byte
	ParentSpanID    string
	ConnectorSpanID string
	SpanID          string
	SpanType        string
	Closing         bool
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

func (s *testSink) PersistMessage(role leapmuxv1.MessageRole, content []byte, span SpanInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, testSinkMessage{Role: role, Content: append([]byte(nil), content...), ParentSpanID: span.ParentSpanID, ConnectorSpanID: span.ConnectorSpanID, SpanID: span.SpanID, SpanType: span.SpanType, Closing: span.Closing})
	return nil
}

func (s *testSink) PersistNotification(role leapmuxv1.MessageRole, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, testSinkMessage{Role: role, Content: append([]byte(nil), content...)})
	return nil
}

func (s *testSink) OpenSpan(spanID string, parentSpanID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openSpans = append(s.openSpans, testSinkSpanOpen{SpanID: spanID, ParentSpanID: parentSpanID})
}
func (s *testSink) CloseSpan(spanID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closedSpans = append(s.closedSpans, spanID)
}
func (s *testSink) ResetSpans() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetSpanCount++
}
func (s *testSink) SetSpanType(spanID, spanType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spanTypes == nil {
		s.spanTypes = make(map[string]string)
	}
	s.spanTypes[spanID] = spanType
}

func (s *testSink) GetSpanType(spanID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spanTypes[spanID]
}

func (s *testSink) ReserveSpanColor(spanID, parentSpanID string) int32 { return 0 }

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

func (s *testSink) PersistControlRequest(string, []byte)   {}
func (s *testSink) DeleteControlRequest(string)            {}
func (s *testSink) BroadcastControlRequest(string, []byte) {}
func (s *testSink) BroadcastControlCancel(string)          {}
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
func (s *testSink) PersistSettingsRefresh(model, effort, permissionMode string, extraSettings map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]string, len(extraSettings))
	for k, v := range extraSettings {
		cp[k] = v
	}
	s.settingsRefreshes = append(s.settingsRefreshes, testSinkSettingsRefreshed{
		Model: model, Effort: effort, PermissionMode: permissionMode, ExtraSettings: cp,
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

func (noopSink) PersistMessage(leapmuxv1.MessageRole, []byte, SpanInfo) error {
	return nil
}
func (noopSink) PersistNotification(leapmuxv1.MessageRole, []byte) error          { return nil }
func (noopSink) OpenSpan(string, string)                                          {}
func (noopSink) CloseSpan(string)                                                 {}
func (noopSink) ResetSpans()                                                      {}
func (noopSink) SetSpanType(string, string)                                       {}
func (noopSink) GetSpanType(string) string                                        { return "" }
func (noopSink) ReserveSpanColor(string, string) int32                            { return 0 }
func (noopSink) BroadcastStreamChunk([]byte, string, string)                      {}
func (noopSink) BroadcastStreamEnd(string)                                        {}
func (noopSink) PersistControlRequest(string, []byte)                             {}
func (noopSink) DeleteControlRequest(string)                                      {}
func (noopSink) BroadcastControlRequest(string, []byte)                           {}
func (noopSink) BroadcastControlCancel(string)                                    {}
func (noopSink) UpdateSessionID(string)                                           {}
func (noopSink) UpdatePermissionMode(string)                                      {}
func (noopSink) PersistSettingsRefresh(string, string, string, map[string]string) {}
func (noopSink) BroadcastStatusActive(string)                                     {}
func (noopSink) BroadcastSessionInfo(map[string]interface{})                      {}
func (noopSink) PersistLeapMuxNotification(map[string]interface{})                {}
func (noopSink) StorePlanModeToolUse(string, string)                              {}
func (noopSink) LoadAndDeletePlanModeToolUse(string) (string, bool)               { return "", false }
func (noopSink) UpdatePlan([]byte, leapmuxv1.ContentCompression, string)          {}
func (noopSink) ScheduleAutoContinue(AutoContinueSchedule)                        {}
func (noopSink) CancelAutoContinue(AutoContinueReason)                            {}
