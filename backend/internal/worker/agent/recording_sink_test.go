package agent

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// controlRequestRecord captures a single PersistControlRequest /
// BroadcastControlRequest call.
type controlRequestRecord struct {
	RequestID string
	Payload   []byte
}

// planUpdateRecord captures a single UpdatePlan call.
type planUpdateRecord struct {
	Content     []byte
	Compression leapmuxv1.ContentCompression
	Title       string
}

// recordingControlSink extends testSink to also capture control requests,
// plan updates, and LeapMux notification broadcasts. The base testSink
// drops all three. Used by Codex and Pi tests; ACP-family tests fall
// back to plain testSink.
type recordingControlSink struct {
	testSink

	crMu              sync.Mutex
	persistedControls []controlRequestRecord
	broadcastControls []controlRequestRecord
	planUpdates       []planUpdateRecord
	notifications     []map[string]interface{}
}

func (s *recordingControlSink) PersistControlRequest(requestID string, payload []byte) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.persistedControls = append(s.persistedControls, controlRequestRecord{
		RequestID: requestID,
		Payload:   append([]byte(nil), payload...),
	})
}

func (s *recordingControlSink) BroadcastControlRequest(requestID string, payload []byte) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.broadcastControls = append(s.broadcastControls, controlRequestRecord{
		RequestID: requestID,
		Payload:   append([]byte(nil), payload...),
	})
}

func (s *recordingControlSink) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.planUpdates = append(s.planUpdates, planUpdateRecord{
		Content:     append([]byte(nil), content...),
		Compression: compression,
		Title:       title,
	})
}

func (s *recordingControlSink) PersistLeapMuxNotification(info map[string]interface{}) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	cp := make(map[string]interface{}, len(info))
	for k, v := range info {
		cp[k] = v
	}
	s.notifications = append(s.notifications, cp)
}

// PersistedControls returns a snapshot of every PersistControlRequest
// call in order.
func (s *recordingControlSink) PersistedControls() []controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]controlRequestRecord(nil), s.persistedControls...)
}

// BroadcastControls returns a snapshot of every BroadcastControlRequest
// call in order.
func (s *recordingControlSink) BroadcastControls() []controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]controlRequestRecord(nil), s.broadcastControls...)
}

func (s *recordingControlSink) PersistedControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.persistedControls)
}

func (s *recordingControlSink) BroadcastControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.broadcastControls)
}

func (s *recordingControlSink) LastPersistedControl() controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.persistedControls[len(s.persistedControls)-1]
}

func (s *recordingControlSink) LastBroadcastControl() controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.broadcastControls[len(s.broadcastControls)-1]
}

func (s *recordingControlSink) PlanUpdateCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.planUpdates)
}

func (s *recordingControlSink) LastPlanUpdate() planUpdateRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.planUpdates[len(s.planUpdates)-1]
}

// Notifications returns a snapshot of every PersistLeapMuxNotification
// call in order.
func (s *recordingControlSink) Notifications() []map[string]interface{} {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]map[string]interface{}(nil), s.notifications...)
}
