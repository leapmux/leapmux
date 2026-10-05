package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type sessionStream struct {
	address         sessionAddress
	sessionID       string
	parentSessionID string
	childAgentID    string
	lastSeq         int64
	tools           int32
	pending         map[string][]byte
	pendingOrder    []string
	startedAt       int64
	ready           chan struct{}
	readyOnce       bool
	// followRetries counts the times that the follow of this stream opened again after
	// the native process reported that the descriptor of the child was not written yet.
	followRetries int
	// contextWindow is the native context window of the model of this Session, in tokens,
	// or 0 while the native process has stated none. Only the Remote reader reads and writes it.
	contextWindow int64
}

type sessionAddress struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"sessionId,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	Mode            string `json:"mode,omitempty"`
}

type historyRecord struct {
	Event json.RawMessage `json:"event"`
}

type historyPage struct {
	Records []historyRecord `json:"records"`
	HasMore bool            `json:"hasMore"`
}

// Native Session cursors use JavaScript safe integers.
const maxSessionSequence int64 = 1<<53 - 1

// The native process announces a one-shot workflow child (`subagent/catalog`) before it writes
// the descriptor of that child. A follow in that gap fails with this error code and reason
// (`@deepseek-ai/dsh` 0.2.0-rc.2, dsh-api-session-controller validateAddress: a descriptor
// projection of null). A live probe saw the descriptor about 26 ms after the announcement.
// The other reason of this code, "unsupported", names a child that has no usable descriptor at all.
//
// The delay of the first retry doubles for each later retry. The seven retries wait 3.175 s in total,
// more than 100 times the delay that the probe saw. A descriptor that is still absent after that
// is corrupt, and the failure then stops the agent as before.
const (
	catalogDiagnosticCode      = "subagent/catalog-diagnostic"
	descriptorNotWrittenReason = "corrupt"
	childFollowRetries         = 7
	childFollowFirstRetryDelay = 25 * time.Millisecond
	childFollowRetryTag        = "deepseekharness-child-follow-retry"
)

type sessionEvent struct {
	Type            string          `json:"type"`
	Seq             *int64          `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	SurfaceOp       json.RawMessage `json:"surfaceOp"`
	SourceEventSeqs []int64         `json:"sourceEventSeqs"`
}

type remoteMuxFrame struct {
	Type     string          `json:"type"`
	StreamID string          `json:"streamId"`
	Value    json.RawMessage `json:"value"`
	Error    *remoteFailure  `json:"error"`
}

func (a *Agent) writeMux(value any) error {
	if a.conn == nil {
		return fmt.Errorf("DeepSeek Harness has no Remote stream connection")
	}
	raw, err := encodeJSON(value)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.Write(a.Context(), websocket.MessageText, raw)
}

func (a *Agent) openConnection(ctx context.Context) error {
	conn, err := a.rpc.endpoint.OpenWebSocket(ctx, "/api/remote.mux", nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(64 << 20)
	a.conn = conn
	life, cancel := context.WithCancel(a.Context())
	a.streamCancel = cancel
	go func() {
		defer close(a.streamDone)
		for {
			kind, raw, err := conn.Read(life)
			if err != nil {
				if life.Err() == nil && !a.IsStopped() {
					a.reportStreamFailure(err)
				}
				return
			}
			if kind != websocket.MessageText {
				a.reportStreamFailure(fmt.Errorf("DeepSeek Harness sent a non-text Remote frame"))
				return
			}
			if err := a.handleFrame(raw); err != nil {
				a.reportStreamFailure(err)
				return
			}
		}
	}()
	return a.writeMux(map[string]any{"type": "open", "streamId": "events", "endpoint": "$events", "payload": map[string]any{"args": map[string]any{}}})
}

func (a *Agent) followSession(address sessionAddress, childAgentID string) error {
	sessionID := address.SessionID
	if address.Kind == "subagent" {
		sessionID = address.ChildSessionID
	}
	streamID := "session-" + uuid.NewString()
	stream := &sessionStream{address: address, sessionID: sessionID, parentSessionID: address.ParentSessionID, childAgentID: childAgentID, lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	a.Mu.Lock()
	a.streams[streamID] = stream
	a.Mu.Unlock()
	err := a.openFollow(streamID, stream)
	if err != nil {
		a.Mu.Lock()
		delete(a.streams, streamID)
		a.Mu.Unlock()
	}
	return err
}

// openFollow asks the native process to stream one Session under the given stream identity.
func (a *Agent) openFollow(streamID string, stream *sessionStream) error {
	return a.writeMux(map[string]any{"type": "open", "streamId": streamID, "endpoint": "session/follow", "payload": map[string]any{"args": map[string]any{"request": map[string]any{"address": stream.address, "assistantStream": true}}}})
}

// isUnwrittenDescriptor reports whether the native process refused a child follow because it
// has not written the descriptor of the child yet.
func isUnwrittenDescriptor(failure *remoteFailure) bool {
	if failure.Code != catalogDiagnosticCode {
		return false
	}
	var details struct {
		Reason string `json:"reason"`
	}
	return json.Unmarshal(failure.Details, &details) == nil && details.Reason == descriptorNotWrittenReason
}

// retryChildFollow schedules a new follow for a child stream that the native process refused
// before it wrote the descriptor of the child, and reports whether it did. The failed stream
// cannot open again, so the same stream moves to a new stream identity. Any other failure, a
// follow of the root Session, and a descriptor that stays absent past the last retry return false.
func (a *Agent) retryChildFollow(streamID string, failure *remoteFailure) bool {
	if !isUnwrittenDescriptor(failure) {
		return false
	}
	a.Mu.Lock()
	stream := a.streams[streamID]
	if stream == nil || stream.address.Kind != "subagent" || stream.followRetries >= childFollowRetries {
		a.Mu.Unlock()
		return false
	}
	delay := childFollowFirstRetryDelay << stream.followRetries
	stream.followRetries++
	retryID := "session-" + uuid.NewString()
	delete(a.streams, streamID)
	a.streams[retryID] = stream
	a.Mu.Unlock()
	a.Clock().AfterFunc(delay, func() {
		a.Mu.Lock()
		stopped := a.StoppedLocked() || a.streamFailure != nil
		a.Mu.Unlock()
		if stopped {
			return
		}
		if err := a.openFollow(retryID, stream); err != nil {
			a.reportStreamFailure(err)
		}
	}, childFollowRetryTag)
	return true
}

// historyRecords reads each older native page before it returns chronological records.
func (a *Agent) historyRecords(stream *sessionStream, cursor int64, page historyPage) ([]historyRecord, error) {
	pages := [][]historyRecord{}
	before := cursor + 1
	for {
		previous := int64(-1)
		for _, record := range page.Records {
			var event sessionEvent
			if json.Unmarshal(record.Event, &event) != nil || event.Seq == nil || *event.Seq < 0 || *event.Seq > maxSessionSequence || *event.Seq >= before || *event.Seq <= previous {
				return nil, fmt.Errorf("DeepSeek Harness history has an invalid native sequence")
			}
			previous = *event.Seq
		}
		pages = append(pages, page.Records)
		if !page.HasMore {
			break
		}
		if len(page.Records) == 0 {
			return nil, fmt.Errorf("DeepSeek Harness history has no earlier page cursor")
		}
		var first sessionEvent
		if err := json.Unmarshal(page.Records[0].Event, &first); err != nil || first.Seq == nil || *first.Seq <= 0 {
			return nil, fmt.Errorf("DeepSeek Harness history has an invalid earlier page cursor")
		}
		before = *first.Seq
		var older historyPage
		if err := a.rpc.request(a.Context(), "session/page", map[string]any{"address": stream.address, "throughSeq": cursor, "beforeSeq": before, "maxMessages": 50}, &older); err != nil {
			return nil, err
		}
		if len(older.Records) == 0 {
			return nil, fmt.Errorf("DeepSeek Harness returned an empty earlier history page")
		}
		page = older
	}
	records := []historyRecord{}
	for index := len(pages) - 1; index >= 0; index-- {
		records = append(records, pages[index]...)
	}
	return records, nil
}

func (a *Agent) handleFrame(raw []byte) error {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	failure := a.streamFailure
	a.Mu.Unlock()
	if failure != nil {
		return failure
	}
	var frame remoteMuxFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Type == "" || frame.StreamID == "" {
		return fmt.Errorf("DeepSeek Harness sent an invalid Remote frame")
	}
	if frame.Type == "error" {
		if frame.Error == nil {
			return fmt.Errorf("DeepSeek Harness Remote stream failed without a cause")
		}
		if a.retryChildFollow(frame.StreamID, frame.Error) {
			return nil
		}
		return frame.Error
	}
	if frame.Type == "end" {
		return nil
	}
	if frame.Type != "item" || len(frame.Value) == 0 {
		return fmt.Errorf("DeepSeek Harness sent an invalid Remote item")
	}
	if frame.StreamID == "events" {
		return a.handleRemoteEvent(frame.Value)
	}
	a.Mu.Lock()
	stream := a.streams[frame.StreamID]
	a.Mu.Unlock()
	if stream == nil {
		return nil
	}
	var item struct {
		Type        string          `json:"type"`
		Cursor      *int64          `json:"cursor"`
		Records     []historyRecord `json:"records"`
		HasMore     bool            `json:"hasMore"`
		Event       json.RawMessage `json:"event"`
		Frame       json.RawMessage `json:"frame"`
		Projections struct {
			Values map[string]json.RawMessage `json:"values"`
		} `json:"projections"`
	}
	if err := json.Unmarshal(frame.Value, &item); err != nil {
		return fmt.Errorf("DeepSeek Harness sent invalid Session data: %w", err)
	}
	switch item.Type {
	case "snapshot":
		if item.Cursor == nil || *item.Cursor < -1 || *item.Cursor > maxSessionSequence {
			return fmt.Errorf("DeepSeek Harness Session snapshot has an invalid cursor")
		}
		stream.seedContextWindow(item.Projections.Values)
		if stream.childAgentID != "" {
			ownSeq, err := childOwnSequence(stream, item.Projections.Values, *item.Cursor)
			if err != nil {
				return err
			}
			records, err := a.historyRecords(stream, *item.Cursor, historyPage{Records: item.Records, HasMore: item.HasMore})
			if err != nil {
				return err
			}
			for _, record := range records {
				var event sessionEvent
				if err := json.Unmarshal(record.Event, &event); err != nil || event.Seq == nil {
					return fmt.Errorf("DeepSeek Harness child history has no native sequence")
				}
				if *event.Seq < ownSeq {
					continue
				}
				if err := a.handleSessionEvent(stream, record.Event); err != nil {
					return err
				}
			}
		}
		if err := a.applyProjections(stream, item.Projections.Values, true); err != nil {
			return err
		}
		if *item.Cursor > stream.lastSeq {
			stream.lastSeq = *item.Cursor
		}
		if !stream.readyOnce {
			stream.readyOnce = true
			close(stream.ready)
		}
	case "event":
		return a.handleSessionEvent(stream, item.Event)
	case "assistant-stream":
		return a.handleAssistantStream(stream, item.Frame)
	default:
		return fmt.Errorf("DeepSeek Harness sent an unknown Session frame")
	}
	return nil
}

func (a *Agent) handleRemoteEvent(raw []byte) error {
	var frame struct {
		Type     string            `json:"type"`
		ClientID string            `json:"clientId"`
		Event    string            `json:"event"`
		EventID  string            `json:"eventId"`
		AgentID  string            `json:"agentId"`
		Request  json.RawMessage   `json:"request"`
		Args     []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return err
	}
	switch frame.Type {
	case "ready":
		if frame.ClientID == "" {
			return fmt.Errorf("DeepSeek Harness Remote generation has no identity")
		}
		a.Mu.Lock()
		a.clientID = frame.ClientID
		a.Mu.Unlock()
		a.readyOnce.Do(func() { close(a.ready) })
	case "waterfall":
		return a.publishNativeControl(frame.Event, frame.EventID, frame.AgentID, frame.Request, raw)
	case "cancel":
		a.Mu.Lock()
		control, ok := a.controls[frame.EventID]
		delete(a.controls, frame.EventID)
		a.Mu.Unlock()
		if ok {
			control.sink.CancelControlRequest(frame.EventID)
		}
	case "emit":
		if frame.Event == "api-session/status" && len(frame.Args) == 2 {
			var id string
			var running bool
			if json.Unmarshal(frame.Args[0], &id) == nil && json.Unmarshal(frame.Args[1], &running) == nil && id == a.session() && running {
				a.setTurnState(true)
			}
		}
		if frame.Event == "api-session/error" && len(frame.Args) == 2 {
			var id string
			var cause string
			if json.Unmarshal(frame.Args[0], &id) == nil && json.Unmarshal(frame.Args[1], &cause) == nil && id == a.session() {
				// Native agent errors leave the Remote transport healthy. The durable turn end releases the queue.
				a.sink.PersistLeapMuxNotification(map[string]any{"type": "error", "message": cause})
			}
		}
	default:
		return fmt.Errorf("DeepSeek Harness sent an unknown Remote event")
	}
	return nil
}

func (a *Agent) reportStreamFailure(err error) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	if a.streamFailure != nil {
		a.Mu.Unlock()
		return
	}
	a.streamFailure = err
	stopDone := make(chan struct{})
	a.failureStopDone = stopDone
	streams := make([]*sessionStream, 0, len(a.streams))
	for _, stream := range a.streams {
		streams = append(streams, stream)
	}
	controls := make(map[string]nativeControl, len(a.controls))
	for id, control := range a.controls {
		controls[id] = control
	}
	clear(a.controls)
	a.Mu.Unlock()
	var cleanupError error
	for _, stream := range streams {
		cleanupError = errors.Join(cleanupError, a.finishPendingTools(stream, agent.MessageCompletionError))
		if stream.childAgentID != "" {
			cleanupError = errors.Join(cleanupError, a.childTurnState(stream.sessionID, false, agent.MessageCompletionError))
		}
	}
	for id, control := range controls {
		control.sink.CancelControlRequest(id)
	}
	a.Mu.Lock()
	a.streamFailure = errors.Join(err, cleanupError)
	a.Mu.Unlock()
	slog.Warn("DeepSeek Harness Remote stream failed", "agent_id", a.AgentID(), "error", err)
	a.sink.PersistLeapMuxNotification(map[string]any{"type": "error", "message": err.Error()})
	a.setTurnState(false)
	// Stop can wait for the process. Keep the Remote reader free to finish during that wait.
	// Wait joins this owned stop operation before it removes the private output files.
	go func() {
		defer close(stopDone)
		a.Process.Stop()
	}()
}

func (a *Agent) streamSink(stream *sessionStream) agent.ProviderServices {
	if stream.childAgentID != "" {
		return a.sink.ChildSink(stream.childAgentID)
	}
	return a.sink
}
