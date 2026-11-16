package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's event WebSocket.
//
// The server pushes every event of a subscribed session on one WebSocket. Three
// of its rules shape this file:
//
//   - It sends `ping` every 10 seconds and CLOSES the socket when no frame
//     arrives from the client for 20 seconds (its own docs claim otherwise; the
//     code does this). So one goroutine does nothing but read frames and answer
//     pings, and the events go to a second goroutine through a queue. An event
//     handler that blocks -- on the database, on a broadcast -- then delays the
//     transcript and never the pong.
//   - Each agent's transcript carries its own sequence number. A subscription
//     sends those positions to replay missed operations. When replay is
//     unavailable, the server sends a potentially incomplete transcript reset before its ack.
//     Transcript pagination recovers the turns that the reset omits.
//   - subscribe_v2 resumes the session itself. Its ack lists an unavailable
//     session under not_found. Global events arrive automatically and have no
//     replay cursor.
//
// Every goroutine of the stream runs under its own context. Stop closes that
// context, and Wait joins the goroutines. The process context ends after native
// exit, but it does not control this independent stream context.

// kimiFrame is one WebSocket frame, including native control interactions.
type kimiFrame struct {
	// afterDispatch is an internal barrier, never a native wire frame.
	afterDispatch func()
	Type          string          `json:"type"`
	ID            string          `json:"id,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	Seq           int64           `json:"seq,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	Timestamp     string          `json:"timestamp,omitempty"`
	Epoch         string          `json:"epoch,omitempty"`
	Volatile      bool            `json:"volatile,omitempty"`
	Code          int             `json:"code,omitempty"`
	Msg           string          `json:"msg,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Task          json.RawMessage `json:"task,omitempty"`
	// Raw preserves native frames whose fields are outside payload.
	Raw      json.RawMessage `json:"-"`
	wireSize int
	// transcriptSeq is decoded once at the transcript boundary.
	transcriptSeq *int64
}

func (f *kimiFrame) UnmarshalJSON(data []byte) error {
	type wireFrame kimiFrame
	var decoded wireFrame
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = kimiFrame(decoded)
	f.wireSize = len(data)
	if f.Type == contracts.KimiFrameInteraction || f.Type == contracts.KimiEventAgent || f.Type == contracts.KimiEventUsage || f.Type == contracts.KimiEventTask || f.Type == contracts.KimiEventTaskList || f.Type == contracts.KimiEventGoal || f.Type == contracts.KimiEventTodo {
		f.Raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// kimiCursor holds each agent's transcript position and counts received resets.
type kimiCursor struct {
	Positions map[string]int64
	Resets    uint64
}

// kimiAck is the payload of the ack a subscribe receives.
type kimiAck struct {
	Accepted []string `json:"accepted"`
	NotFound []string `json:"not_found"`
}

// errKimiSessionUnavailable reports a subscribe the server refused because the
// session is unavailable.
var errKimiSessionUnavailable = errors.New("the session is unavailable in the Kimi server")

// errKimiStreamClosed reports a start of a stream that close already ended.
var errKimiStreamClosed = errors.New("the Kimi event stream is closed")

// errKimiStreamSilent reports a connection that sent no frame for
// kimiStreamReadTimeout.
var errKimiStreamSilent = errors.New("the Kimi event stream sent no frame within the read timeout")

// kimiStreamQueueDepth caps the events the reader holds for the dispatcher.
// A dispatcher that falls this far behind makes the reader wait, and a reader
// that waits stops answering pings -- the server then closes the socket, and
// the re-subscribe replays what was lost. That is the designed outcome of a
// stall this long, not an error.
const kimiStreamQueueDepth = 4096

// kimiStreamReadTimeout ends a read that received no frame for this long. The
// server pings every 10 seconds, so a silence three times as long is a dead
// connection, and the stream reconnects rather than wait on it for good.
const kimiStreamReadTimeout = 45 * time.Second

// kimiStreamWriteTimeout limits one frame the client writes.
const kimiStreamWriteTimeout = 10 * time.Second

// kimiStreamFirstBackoff is the wait before the first reconnect attempt. Each
// failed attempt doubles it, up to kimiStreamMaxBackoff.
const kimiStreamFirstBackoff = 250 * time.Millisecond

// kimiStreamMaxBackoff caps the wait between two reconnect attempts.
const kimiStreamMaxBackoff = 5 * time.Second

// kimiStreamReconnectTimerTag and kimiStreamReadTimerTag label the stream's two
// timers for a test's clock trap: the wait before a reconnect attempt, and the
// timer that ends a silent read.
const (
	kimiStreamReconnectTimerTag = "kimi-stream-reconnect"
	kimiStreamReadTimerTag      = "kimi-stream-read"
)

// kimiStreamReadLimit caps one frame. An event carries a tool call's whole
// arguments or output, which can hold a large file.
const kimiStreamReadLimit = 64 << 20

// kimiReconnectHandler refreshes unsequenced state after each subscription.
// replayed is true when an existing transcript position resumed without a reset.
// ctx ends when the stream closes.
type kimiReconnectHandler func(ctx context.Context, sessionID string, replayed bool)

// kimiStream owns the event WebSocket of one kap-server.
type kimiStream struct {
	endpoint *providerkit.HTTPEndpoint
	// dispatch handles one event frame. It runs on the dispatcher goroutine, in
	// the order the frames arrived.
	dispatch func(kimiFrame)
	// reconnected runs after each re-subscribe. See kimiReconnectHandler.
	reconnected kimiReconnectHandler
	// recovering starts a projection fence before a native resubscription.
	recovering func(context.Context, string) func(bool)
	// clock drives the wait before a reconnect attempt and the read timeout.
	clock quartz.Clock
	// backoff is the first wait between reconnect attempts. It doubles up to
	// kimiStreamMaxBackoff. It is kimiStreamFirstBackoff, and a test that
	// reconnects on the real clock shortens it before start.
	backoff time.Duration
	// agentID labels the log lines.
	agentID string

	queue chan kimiFrame
	// workers counts the goroutines the stream runs: the reader, the
	// dispatcher, and each re-subscribe. wait blocks until every one returned.
	workers sync.WaitGroup

	mu sync.Mutex
	// ctx is the context that every goroutine of the stream runs under, and
	// cancel ends it. start sets both, and close calls cancel. Each write takes
	// its deadline from ctx (see write).
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn
	closed bool
	nextID int64
	acks   map[string]chan kimiFrame
	// sessions holds every subscribed session and the cursor of the last
	// durable event received for it. A reconnect re-subscribes each one from
	// its cursor.
	sessions map[string]kimiCursor
	// states holds the current native main-agent state for each tracked session.
	states       map[string]kimiSessionPushState
	stateChanged chan struct{}
}

func newKimiStream(endpoint *providerkit.HTTPEndpoint, agentID string, clock quartz.Clock, dispatch func(kimiFrame), reconnected kimiReconnectHandler) *kimiStream {
	return &kimiStream{
		endpoint:     endpoint,
		dispatch:     dispatch,
		reconnected:  reconnected,
		clock:        clock,
		backoff:      kimiStreamFirstBackoff,
		agentID:      agentID,
		queue:        make(chan kimiFrame, kimiStreamQueueDepth),
		acks:         make(map[string]chan kimiFrame),
		sessions:     make(map[string]kimiCursor),
		states:       make(map[string]kimiSessionPushState),
		stateChanged: make(chan struct{}),
	}
}

// start opens the first connection and starts the reader and the dispatcher.
// Both run until close runs or parent ends. The first dial must succeed: a
// server that stated its address and then refuses the socket cannot run the
// agent.
func (s *kimiStream) start(parent context.Context, dialTimeout time.Duration) error {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return errKimiStreamClosed
	}
	s.ctx, s.cancel = ctx, cancel
	s.mu.Unlock()

	conn, err := s.dial(ctx, dialTimeout)
	if err != nil {
		cancel()
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return errKimiStreamClosed
	}
	s.conn = conn
	s.mu.Unlock()
	s.workers.Add(2)
	go func() {
		defer s.workers.Done()
		s.runDispatcher(ctx)
	}()
	go func() {
		defer s.workers.Done()
		s.runReader(ctx, conn)
	}()
	return nil
}

func (s *kimiStream) dial(ctx context.Context, timeout time.Duration) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := s.endpoint.OpenWebSocket(dialCtx, kimiRouteWS, nil)
	if err != nil {
		return nil, fmt.Errorf("open the Kimi event stream: %w", err)
	}
	conn.SetReadLimit(kimiStreamReadLimit)
	return conn, nil
}

// close ends the stream: it cancels the context that every goroutine of the
// stream selects on, and it closes the current connection. It returns at once,
// and it is safe to call more than once. wait blocks until the goroutines
// returned.
func (s *kimiStream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	clear(s.states)
	s.signalStateChangeLocked()
	cancel, conn := s.cancel, s.conn
	s.conn = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

// wait blocks until every goroutine of the stream returned. Only close, or the
// end of the context start took, makes them return.
func (s *kimiStream) wait() {
	s.workers.Wait()
}

// runDispatcher hands every queued event to dispatch, in order, until the
// stream closes.
func (s *kimiStream) runDispatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-s.queue:
			// A select picks at random among the ready cases, so a frame can win
			// over a close that came first. A closed stream dispatches nothing.
			if ctx.Err() != nil {
				return
			}
			if frame.afterDispatch != nil {
				frame.afterDispatch()
			} else {
				s.dispatch(frame)
			}
		}
	}
}

// runReader reads frames from conn until it fails, then reconnects and
// re-subscribes. It returns when the stream closes.
func (s *kimiStream) runReader(ctx context.Context, conn *websocket.Conn) {
	for {
		err := s.readUntilFailure(ctx, conn)
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
			clear(s.states)
			s.signalStateChangeLocked()
		}
		s.mu.Unlock()
		slog.Warn("kimi event stream lost; reconnecting", "agent_id", s.agentID, "error", err)
		next, ok := s.reconnect(ctx)
		if !ok {
			return
		}
		conn = next
		// The reader already counts in workers, so the count is above zero and the
		// Add cannot race a wait that already returned.
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.resubscribeAll(ctx)
		}()
	}
}

// reconnect dials until a dial succeeds, and waits longer after each failure.
// It returns false when the stream closes first.
func (s *kimiStream) reconnect(ctx context.Context) (*websocket.Conn, bool) {
	backoff := s.backoff
	for {
		timer := s.clock.NewTimer(backoff, kimiStreamReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		conn, err := s.dial(ctx, kimiStreamWriteTimeout)
		if err != nil {
			slog.Debug("kimi event stream reconnect failed", "agent_id", s.agentID, "error", err)
			backoff = min(backoff*2, kimiStreamMaxBackoff)
			continue
		}
		s.mu.Lock()
		if s.closed {
			// close ran while the dial was in flight, and it could not reach this
			// connection.
			s.mu.Unlock()
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return nil, false
		}
		s.conn = conn
		s.mu.Unlock()
		return conn, true
	}
}

// readUntilFailure reads frames until the connection fails.
func (s *kimiStream) readUntilFailure(ctx context.Context, conn *websocket.Conn) error {
	defer func() { _ = conn.CloseNow() }()
	for {
		data, err := s.read(ctx, conn)
		if err != nil {
			s.failPendingAcks()
			return err
		}
		var frame kimiFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			slog.Warn("kimi event stream sent a frame that is not JSON", "agent_id", s.agentID, "error", err)
			continue
		}
		switch frame.Type {
		case kimiFramePing:
			s.pong(ctx, conn, frame)
		case kimiFrameAck:
			s.deliverAck(frame)
		case kimiFrameTaskResponse:
			frame.ID = frame.RequestID
			s.deliverAck(frame)
		case kimiFrameServerHello:
			// The hello states the heartbeat interval and the protocol version. The
			// reader's timeout already assumes the 10-second heartbeat.
		default:
			s.observeSessionState(frame)
			s.noteCursor(frame)
			select {
			case s.queue <- frame:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// read reads one frame. A read that receives nothing for kimiStreamReadTimeout
// on the stream's clock fails with errKimiStreamSilent. The failed read closes
// the connection, as every read that its context ends does.
func (s *kimiStream) read(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	readCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timeout := s.clock.AfterFunc(kimiStreamReadTimeout, func() { cancel(errKimiStreamSilent) }, kimiStreamReadTimerTag)
	defer timeout.Stop()
	_, data, err := conn.Read(readCtx)
	if err != nil && errors.Is(context.Cause(readCtx), errKimiStreamSilent) {
		return nil, errKimiStreamSilent
	}
	return data, err
}

// pong answers one ping. The write runs on the reader's goroutine: a pong that
// waited behind an event would let the server's 20-second silence timer fire.
func (s *kimiStream) pong(ctx context.Context, conn *websocket.Conn, ping kimiFrame) {
	var payload struct {
		Nonce string `json:"nonce"`
	}
	_ = json.Unmarshal(ping.Payload, &payload)
	frame, err := json.Marshal(map[string]any{"type": kimiFramePong, "payload": map[string]string{"nonce": payload.Nonce}})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, kimiStreamWriteTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, frame); err != nil {
		slog.Debug("kimi event stream pong failed", "agent_id", s.agentID, "error", err)
	}
}

// noteCursor records the native position of each agent's transcript.
// A reset replaces its position, including when a server restarts numbering.
func (s *kimiStream) noteCursor(frame kimiFrame) {
	if frame.SessionID == "" || (frame.Type != contracts.KimiEventTranscriptReset && frame.Type != contracts.KimiEventTranscriptOps) {
		return
	}
	var position struct {
		AgentID string `json:"agent_id"`
		Seq     *int64 `json:"seq"`
	}
	if json.Unmarshal(frame.Payload, &position) != nil || position.AgentID == "" || position.Seq == nil || *position.Seq < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, tracked := s.sessions[frame.SessionID]
	if !tracked {
		return
	}
	if cursor.Positions == nil {
		cursor.Positions = make(map[string]int64)
	}
	if frame.Type == contracts.KimiEventTranscriptReset {
		cursor.Resets++
		cursor.Positions[position.AgentID] = *position.Seq
	} else if previous, exists := cursor.Positions[position.AgentID]; !exists || *position.Seq > previous {
		cursor.Positions[position.AgentID] = *position.Seq
	}
	s.sessions[frame.SessionID] = cursor
}

// subscribe subscribes the current connection to sessionID and returns the ack.
// An unavailable session returns errKimiSessionUnavailable.
//
// A subscribe that fails forgets a session that it added: the caller does not
// drive that session, and a reconnect must not subscribe it again. A session
// that was tracked before the call stays tracked.
func (s *kimiStream) subscribe(ctx context.Context, sessionID string) (kimiAck, error) {
	s.mu.Lock()
	_, tracked := s.sessions[sessionID]
	if !tracked {
		s.sessions[sessionID] = kimiCursor{}
	}
	s.mu.Unlock()
	ack, err := s.sendSessionSubscribe(ctx, sessionID, nil)
	if err == nil && slices.Contains(ack.NotFound, sessionID) {
		err = errKimiSessionUnavailable
	}
	if err != nil {
		if !tracked {
			s.forget(sessionID)
		}
		return ack, err
	}
	return ack, nil
}

// unsubscribe stops the events of sessionID. A failure is logged: the session's
// events are dropped by the dispatcher either way, because they no longer match
// the agent's session.
func (s *kimiStream) unsubscribe(sessionID string) {
	s.forget(sessionID)
	if err := s.write(map[string]any{
		"type":    kimiFrameUnsubscribe,
		"payload": map[string]string{"session_id": sessionID},
	}); err != nil {
		slog.Debug("kimi event stream unsubscribe failed", "agent_id", s.agentID, "session_id", sessionID, "error", err)
	}
}

func (s *kimiStream) forget(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	delete(s.states, sessionID)
	if !s.closed {
		s.signalStateChangeLocked()
	}
	s.mu.Unlock()
}

// resubscribeAll re-subscribes every tracked session after a reconnect, from
// the cursor of the last durable event received, and then runs reconnected for
// each session the server answered for.
func (s *kimiStream) resubscribeAll(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	cursors := make(map[string]kimiCursor, len(s.sessions))
	for id, cursor := range s.sessions {
		ids = append(ids, id)
		cursor.Positions = maps.Clone(cursor.Positions)
		cursors[id] = cursor
	}
	s.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		var applied func(bool)
		if s.recovering != nil {
			applied = s.recovering(ctx, id)
		}
		ack, err := s.sendSessionSubscribe(ctx, id, cursors[id].Positions)
		accepted := err == nil && slices.Contains(ack.Accepted, id)
		if applied != nil {
			select {
			case s.queue <- kimiFrame{afterDispatch: func() { applied(accepted) }}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			slog.Warn("kimi event stream resubscribe failed", "agent_id", s.agentID, "session_id", id, "error", err)
			continue
		}
		if s.reconnected == nil {
			continue
		}
		if slices.Contains(ack.NotFound, id) {
			s.reconnected(ctx, id, false)
			continue
		}
		s.mu.Lock()
		current, tracked := s.sessions[id]
		s.mu.Unlock()
		if tracked && slices.Contains(ack.Accepted, id) {
			previous := cursors[id]
			s.reconnected(ctx, id, len(previous.Positions) > 0 && current.Resets == previous.Resets)
		}
	}
}

// sendSessionSubscribe subscribes one session and waits for its acknowledgement.
func (s *kimiStream) sendSessionSubscribe(ctx context.Context, sessionID string, since map[string]int64) (kimiAck, error) {
	payload := map[string]any{
		"session_id": sessionID,
		"transcript": map[string]string{"*": "delta"},
	}
	if len(since) > 0 {
		payload["transcript_since"] = since
	}
	frame, err := s.requestFrame(ctx, "subscribe", func(id string) any {
		return map[string]any{"type": kimiFrameSubscribe, "id": id, "payload": payload}
	})
	if err != nil {
		return kimiAck{}, err
	}
	if frame.Code != kimiCodeOK {
		return kimiAck{}, fmt.Errorf("the Kimi server refused the subscribe: %s (code %d)", frame.Msg, frame.Code)
	}
	var ack kimiAck
	if err := json.Unmarshal(frame.Payload, &ack); err != nil {
		return kimiAck{}, fmt.Errorf("decode the subscribe ack: %w", err)
	}
	if len(ack.Accepted)+len(ack.NotFound) != 1 ||
		(len(ack.Accepted) == 1 && ack.Accepted[0] != sessionID) ||
		(len(ack.NotFound) == 1 && ack.NotFound[0] != sessionID) {
		return kimiAck{}, fmt.Errorf("invalid subscribe acknowledgement for Kimi session %q", sessionID)
	}
	return ack, nil
}

// deliverAck hands a response to the request that waits for it.
func (s *kimiStream) deliverAck(frame kimiFrame) {
	s.mu.Lock()
	reply := s.acks[frame.ID]
	delete(s.acks, frame.ID)
	s.mu.Unlock()
	if reply != nil {
		reply <- frame
	}
}

// failPendingAcks releases every request waiting on the failed connection.
// Its response cannot arrive after that connection closes.
func (s *kimiStream) failPendingAcks() {
	s.mu.Lock()
	pending := s.acks
	s.acks = make(map[string]chan kimiFrame)
	s.mu.Unlock()
	for _, reply := range pending {
		close(reply)
	}
}

// write sends one control frame on the current connection.
//
// The write's deadline comes from the stream's own context, never from the
// caller's. coder/websocket closes the whole connection when the context of a
// write ends (setupWriteTimeout), and the connection carries every session of
// the agent. So a caller that gives up must not end the stream of the rest.
func (s *kimiStream) write(frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode a Kimi event stream frame: %w", err)
	}
	s.mu.Lock()
	conn, streamCtx := s.conn, s.ctx
	s.mu.Unlock()
	if conn == nil {
		return errors.New("the Kimi event stream is not connected")
	}
	writeCtx, cancel := context.WithTimeout(streamCtx, kimiStreamWriteTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("%w: %w", agent.ErrDeliveryUncertain, err)
	}
	return nil
}
