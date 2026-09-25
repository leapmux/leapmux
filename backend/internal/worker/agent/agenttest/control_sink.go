package agenttest

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ControlRequestRecord captures one provider request for publication.
type ControlRequestRecord struct {
	// AgentSessionID is the provider session that the sink stores with the
	// request: the one that the request states, else the session that the sink
	// holds when it stores the request (see ControlSink.PublishControlRequest).
	AgentSessionID string
	RequestID      string
	Payload        []byte
	SourceSeq      int64
}

// PlanUpdateRecord captures a single UpdatePlan call.
type PlanUpdateRecord struct {
	Content     []byte
	Compression leapmuxv1.ContentCompression
	Title       string
}

// ControlSink extends Sink to also capture control requests, plan updates, and
// LeapMux notification broadcasts. The base Sink drops all three. A provider
// test uses it when it asserts on any of the three.
type ControlSink struct {
	Sink

	crMu              sync.Mutex
	publishedControls []ControlRequestRecord
	canceledControls  []string
	PublicationError  error
	planUpdates       []PlanUpdateRecord
	notifications     []map[string]interface{}
}

var _ agent.ServiceFacets = (*ControlSink)(nil)

// PublishControlRequest records the request. A request that states no session
// takes the session that the sink holds now, as the worker's own sink stores it
// (agentOutputSink.PublishControlRequest). Before the first UpdateSessionID
// that session is empty, which is the state that a request of a handshake
// meets.
func (s *ControlSink) PublishControlRequest(request agent.ControlRequest) error {
	sessionID := request.AgentSessionID
	if sessionID == "" {
		sessionID = s.LastSessionID()
	}
	s.crMu.Lock()
	defer s.crMu.Unlock()
	if s.PublicationError != nil {
		return s.PublicationError
	}
	s.publishedControls = append(s.publishedControls, ControlRequestRecord{
		AgentSessionID: sessionID,
		RequestID:      request.RequestID,
		Payload:        append([]byte(nil), request.Payload...),
		SourceSeq:      request.SourceSeq,
	})
	return nil
}

// CancelControlRequest records the retirement. Sink's own implementation is a
// no-op, so without this a superseded card would look retired either way.
func (s *ControlSink) CancelControlRequest(requestID string) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.canceledControls = append(s.canceledControls, requestID)
}

func (s *ControlSink) CanceledControls() []string {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]string(nil), s.canceledControls...)
}

// ResetCanceledControls forgets every retirement recorded so far, so a test can
// count only the ones that follow.
func (s *ControlSink) ResetCanceledControls() {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.canceledControls = nil
}

func (s *ControlSink) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.planUpdates = append(s.planUpdates, PlanUpdateRecord{
		Content:     append([]byte(nil), content...),
		Compression: compression,
		Title:       title,
	})
}

func (s *ControlSink) PersistLeapMuxNotification(info map[string]interface{}) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	cp := make(map[string]interface{}, len(info))
	for k, v := range info {
		cp[k] = v
	}
	s.notifications = append(s.notifications, cp)
}

// PublishedControls returns the requests in publication order.
func (s *ControlSink) PublishedControls() []ControlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]ControlRequestRecord(nil), s.publishedControls...)
}

func (s *ControlSink) PublishedControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.publishedControls)
}

func (s *ControlSink) LastPublishedControl() ControlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.publishedControls[len(s.publishedControls)-1]
}

func (s *ControlSink) PlanUpdateCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.planUpdates)
}

func (s *ControlSink) LastPlanUpdate() PlanUpdateRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.planUpdates[len(s.planUpdates)-1]
}

// Notifications returns a snapshot of every PersistLeapMuxNotification
// call in order.
func (s *ControlSink) Notifications() []map[string]interface{} {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return append([]map[string]interface{}(nil), s.notifications...)
}
