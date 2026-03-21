package agent

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// testSink is a test implementation of OutputSink that records calls.
type testSink struct {
	mu               sync.Mutex
	messages         []testSinkMessage
	streamChunks     [][]byte
	sessionIDs       []string
	sessionInfos     []map[string]interface{}
	planModeToolUses sync.Map
}

type testSinkMessage struct {
	Role     leapmuxv1.MessageRole
	Content  []byte
	ThreadID string
}

func (s *testSink) PersistMessage(role leapmuxv1.MessageRole, content []byte, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, testSinkMessage{Role: role, Content: append([]byte(nil), content...), ThreadID: threadID})
	return nil
}

func (s *testSink) MergeIntoThread(string, []byte) bool { return false }

func (s *testSink) PersistNotification(leapmuxv1.MessageRole, []byte) error { return nil }

func (s *testSink) BroadcastStreamChunk(content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamChunks = append(s.streamChunks, append([]byte(nil), content...))
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
func (s *testSink) UpdatePermissionMode(string)                  {}
func (s *testSink) BroadcastStatusActive(string) {}
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
func (s *testSink) BroadcastNotification(map[string]interface{}) {}
func (s *testSink) SoftClearNotifThread()                        {}

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

func (s *testSink) UpdatePlan(string, []byte, leapmuxv1.ContentCompression, string) {}

// MessageCount returns the number of persisted messages.
func (s *testSink) MessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// StreamChunkCount returns the number of broadcast stream chunks.
func (s *testSink) StreamChunkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streamChunks)
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

// noopSink is a no-op implementation of OutputSink for tests that don't
// need to verify output.
type noopSink struct{}

func (noopSink) PersistMessage(leapmuxv1.MessageRole, []byte, string) error      { return nil }
func (noopSink) MergeIntoThread(string, []byte) bool                             { return false }
func (noopSink) PersistNotification(leapmuxv1.MessageRole, []byte) error         { return nil }
func (noopSink) BroadcastStreamChunk([]byte)                                     {}
func (noopSink) PersistControlRequest(string, []byte)                            {}
func (noopSink) DeleteControlRequest(string)                                     {}
func (noopSink) BroadcastControlRequest(string, []byte)                          {}
func (noopSink) BroadcastControlCancel(string)                                   {}
func (noopSink) UpdateSessionID(string)                                          {}
func (noopSink) UpdatePermissionMode(string)                                     {}
func (noopSink) BroadcastStatusActive(string)                                    {}
func (noopSink) BroadcastSessionInfo(map[string]interface{})                     {}
func (noopSink) BroadcastNotification(map[string]interface{})                    {}
func (noopSink) SoftClearNotifThread()                                           {}
func (noopSink) StorePlanModeToolUse(string, string)                             {}
func (noopSink) LoadAndDeletePlanModeToolUse(string) (string, bool)              { return "", false }
func (noopSink) UpdatePlan(string, []byte, leapmuxv1.ContentCompression, string) {}
