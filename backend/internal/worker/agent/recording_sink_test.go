package agent

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// controlRequestRecord captures one provider request for publication.
type controlRequestRecord struct {
	RequestID string
	Payload   []byte
	SourceSeq int64
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
	publishedControls []controlRequestRecord
	publicationError  error
	planUpdates       []planUpdateRecord
	notifications     []map[string]interface{}
}

func (s *recordingControlSink) PublishControlRequest(request ControlRequest) error {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	if s.publicationError != nil {
		return s.publicationError
	}
	s.publishedControls = append(s.publishedControls, controlRequestRecord{
		RequestID: request.RequestID,
		Payload:   append([]byte(nil), request.Payload...),
		SourceSeq: request.SourceSeq,
	})
	return nil
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

// PublishedControls returns the requests in publication order.
func (s *recordingControlSink) PublishedControls() []controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]controlRequestRecord(nil), s.publishedControls...)
}

func (s *recordingControlSink) PublishedControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.publishedControls)
}

func (s *recordingControlSink) LastPublishedControl() controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.publishedControls[len(s.publishedControls)-1]
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
