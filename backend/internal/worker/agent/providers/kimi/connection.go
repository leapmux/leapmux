package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's event WebSocket.
//
// The server pushes every event of a subscribed session on one WebSocket. Three
// of its rules shape this file:
//
//   - It sends `ping` every 10 seconds and closes the socket when no frame
//     arrives from the client for 20 seconds. One goroutine reads frames and answers
//     pings, and the events go to a second goroutine through a queue. An event
//     handler that blocks -- on the database, on a broadcast -- then delays the
//     transcript and never the pong.
//   - A subscription sends the latest page of each requested agent's turns.
//     Older pages come from REST. The acknowledgement precedes initial state,
//     so it does not prove that the conversation is ready.
//   - subscribe resumes the session itself. An unavailable session returns
//     SESSION_NOT_FOUND. Global events arrive automatically and have no replay cursor.
//
// Every goroutine of the stream runs under its own context. Stop closes that
// context, and Wait joins the goroutines. The process context ends after native
// exit, but it does not control this independent stream context.

// kimiFrame is one WebSocket frame, including native control interactions.
type kimiFrame struct {
	// afterDispatch is an internal barrier, never a native wire frame.
	afterDispatch func()
	Type          string          `json:"type"`
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
}

func (f *kimiFrame) UnmarshalJSON(data []byte) error {
	type wireFrame kimiFrame
	var decoded wireFrame
	wire := struct {
		*wireFrame
		Code json.RawMessage `json:"code"`
	}{wireFrame: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if decoded.Type == kimiFrameTaskResponse {
		var code *int
		if json.Unmarshal(wire.Code, &code) != nil || code == nil || decoded.RequestID == "" {
			return errors.New("the Kimi control response has no request identity or result code")
		}
		decoded.Code = *code
	}
	*f = kimiFrame(decoded)
	f.wireSize = len(data)
	f.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON retains the required result code when a control response succeeds.
func (f kimiFrame) MarshalJSON() ([]byte, error) {
	type wireFrame kimiFrame
	if f.Type != kimiFrameTaskResponse {
		return json.Marshal(wireFrame(f))
	}
	return json.Marshal(struct {
		wireFrame
		Code int `json:"code"`
	}{wireFrame(f), f.Code})
}

// kimiSubscription retains the agents whose native pages reconnect must restore.
type kimiSubscription struct {
	Agents []string
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
// server pings every 10 seconds. After repeated missed heartbeats, the stream
// reconnects instead of waiting indefinitely.
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
// ctx ends when the stream closes.
type kimiReconnectHandler func(ctx context.Context, sessionID string)

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
	// confirmControls verifies the complete pending-control replay after acceptance.
	confirmControls kimiReconnectHandler
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
	acks   map[string]kimiPendingResponse
	// sessions retains desired subscriptions independently from cached native state.
	sessions map[string]kimiSubscription
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
		acks:         make(map[string]kimiPendingResponse),
		sessions:     make(map[string]kimiSubscription),
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
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer func() { cancelConnection() }()
	for {
		err := s.readUntilFailure(connectionCtx, conn)
		// A later socket cannot confirm subscriptions or pending controls from
		// this socket. Cancel its work before installing the replacement.
		cancelConnection()
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
		connectionCtx, cancelConnection = context.WithCancel(ctx)
		// The reader already counts in workers, so the count is above zero and the
		// Add cannot race a wait that already returned.
		s.workers.Add(1)
		go func(replayCtx context.Context) {
			defer s.workers.Done()
			s.resubscribeAll(replayCtx)
		}(connectionCtx)
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
			var header struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(data, &header) == nil && header.Type == kimiFrameTaskResponse && header.RequestID != "" {
				s.deliverResponse(kimiFrame{RequestID: header.RequestID}, fmt.Errorf("%w: the Kimi server sent an invalid response: %w", agent.ErrDeliveryUncertain, err))
			}
			slog.Warn("Kimi event stream sent an invalid frame", "agent_id", s.agentID, "error", err)
			continue
		}
		switch frame.Type {
		case kimiFramePing:
			s.pong(ctx, conn)
		case kimiFrameTaskResponse:
			s.deliverResponse(frame, nil)
		default:
			s.observeSessionState(frame)
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
func (s *kimiStream) pong(ctx context.Context, conn *websocket.Conn) {
	frame, err := json.Marshal(map[string]string{"type": kimiFramePong})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, kimiStreamWriteTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, frame); err != nil {
		slog.Debug("kimi event stream pong failed", "agent_id", s.agentID, "error", err)
	}
}

// subscribe subscribes the current connection to sessionID and validates its response.
// An unavailable session returns errKimiSessionUnavailable.
//
// A subscribe that fails forgets a session that it added: the caller does not
// drive that session, and a reconnect must not subscribe it again. A session
// that was tracked before the call stays tracked.
func (s *kimiStream) subscribe(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	_, tracked := s.sessions[sessionID]
	if !tracked {
		s.sessions[sessionID] = kimiSubscription{Agents: []string{kimiMainAgentID}}
	}
	s.mu.Unlock()
	err := s.sendSessionSubscribe(ctx, sessionID, nil)
	if err != nil {
		if !tracked {
			s.forget(sessionID)
		}
		return err
	}
	return nil
}

func (s *kimiStream) subscribeAgents(ctx context.Context, sessionID string, agentIDs []string) error {
	if err := kimiCheckID("session", sessionID); err != nil {
		return err
	}
	ids := slices.Clone(agentIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return errors.New("the Kimi agent subscription has no agents")
	}
	for _, id := range ids {
		if err := kimiCheckID("agent", id); err != nil {
			return err
		}
	}
	s.mu.Lock()
	cursor, tracked := s.sessions[sessionID]
	if !tracked || s.closed {
		s.mu.Unlock()
		return errKimiSessionUnavailable
	}
	if len(cursor.Agents) == 0 {
		cursor.Agents = []string{kimiMainAgentID}
	}
	for _, id := range ids {
		if !slices.Contains(cursor.Agents, id) {
			cursor.Agents = append(cursor.Agents, id)
		}
	}
	s.sessions[sessionID] = cursor
	s.mu.Unlock()
	return s.sendSessionSubscribe(ctx, sessionID, nil, ids...)
}

func (s *kimiStream) subscribedAgents(sessionID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := slices.Clone(s.sessions[sessionID].Agents)
	if len(ids) == 0 {
		ids = []string{kimiMainAgentID}
	}
	return ids
}

// unsubscribe stops the events of sessionID. A failure is logged: the session's
// events are dropped by the dispatcher either way, because they no longer match
// the agent's session.
func (s *kimiStream) unsubscribe(sessionID string) {
	s.forget(sessionID)
	s.mu.Lock()
	streamCtx := s.ctx
	s.mu.Unlock()
	if streamCtx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(streamCtx, kimiStreamWriteTimeout)
	defer cancel()
	_, err := s.requestFrame(ctx, kimiFrameRequest{operation: "unsubscribe", build: func(id string) any {
		return map[string]any{"type": kimiFrameUnsubscribe, "request_id": id, "session_id": sessionID}
	}})
	if err != nil {
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

// resubscribeAll restores tracked subscriptions after a reconnect and runs each session's callback.
func (s *kimiStream) resubscribeAll(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	subscriptions := make(map[string]kimiSubscription, len(s.sessions))
	for id, subscription := range s.sessions {
		ids = append(ids, id)
		subscriptions[id] = kimiSubscription{Agents: slices.Clone(subscription.Agents)}
	}
	s.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		var applied func(bool)
		if s.recovering != nil {
			applied = s.recovering(ctx, id)
		}
		appliedDone := make(chan struct{})
		var barrierObserved atomic.Bool
		queueApplied := func(accepted bool) {
			if applied != nil && barrierObserved.CompareAndSwap(false, true) {
				s.queueResponseBarrier(func() { applied(accepted); close(appliedDone) })
			}
		}
		err := s.sendSessionSubscribe(ctx, id, func(response kimiFrame) {
			queueApplied(response.Type == kimiFrameTaskResponse && response.Code == kimiCodeOK)
		}, subscriptions[id].Agents...)
		if err != nil {
			queueApplied(false)
			slog.Warn("Kimi event stream resubscribe failed", "agent_id", s.agentID, "session_id", id, "error", err)
			if errors.Is(err, errKimiSessionUnavailable) && s.reconnected != nil {
				s.reconnected(ctx, id)
			}
			continue
		}
		if applied != nil && s.confirmControls != nil {
			select {
			case <-appliedDone:
				s.confirmControls(ctx, id)
			case <-ctx.Done():
				return
			}
		}
		s.mu.Lock()
		_, tracked := s.sessions[id]
		s.mu.Unlock()
		if tracked && s.reconnected != nil {
			s.reconnected(ctx, id)
		}
	}
}

// sendSessionSubscribe subscribes one session and waits for its acknowledgement.
func (s *kimiStream) sendSessionSubscribe(ctx context.Context, sessionID string, beforeDispatch func(kimiFrame), agentIDs ...string) error {
	frame, err := s.requestFrame(ctx, kimiFrameRequest{operation: "subscribe", beforeDispatch: beforeDispatch, build: func(id string) any {
		command := map[string]any{"type": kimiFrameSubscribe, "request_id": id, "session_id": sessionID}
		if len(agentIDs) != 0 {
			command["agent_ids"] = slices.Clone(agentIDs)
		}
		return command
	}})
	if err != nil {
		return err
	}
	if frame.Type != kimiFrameTaskResponse {
		return fmt.Errorf("%w: the Kimi server sent an invalid subscription response", agent.ErrDeliveryUncertain)
	}
	if frame.Code == kimiCodeSessionNotFound {
		return errKimiSessionUnavailable
	}
	if frame.Code != kimiCodeOK {
		return fmt.Errorf("the Kimi server refused the subscribe: %s (code %d)", frame.Msg, frame.Code)
	}
	return nil
}

// deliverResponse hands a response to the request that waits for it.
func (s *kimiStream) deliverResponse(frame kimiFrame, responseErr error) {
	s.mu.Lock()
	pending, exists := s.acks[frame.RequestID]
	delete(s.acks, frame.RequestID)
	s.mu.Unlock()
	if exists {
		if pending.beforeDispatch != nil {
			pending.beforeDispatch(frame)
		}
		pending.reply <- kimiFrameResult{frame: frame, err: responseErr}
	}
}

func (s *kimiStream) queueResponseBarrier(applied func()) {
	s.mu.Lock()
	ctx, closed := s.ctx, s.closed
	s.mu.Unlock()
	if ctx == nil || closed {
		return
	}
	select {
	case s.queue <- kimiFrame{afterDispatch: applied}:
	case <-ctx.Done():
	}
}

// failPendingAcks releases every request waiting on the failed connection.
// Its response cannot arrive after that connection closes.
func (s *kimiStream) failPendingAcks() {
	s.mu.Lock()
	pending := s.acks
	s.acks = make(map[string]kimiPendingResponse)
	s.mu.Unlock()
	for _, response := range pending {
		close(response.reply)
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
