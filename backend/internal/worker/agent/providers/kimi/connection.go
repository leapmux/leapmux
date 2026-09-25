package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"

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
//   - Durable events carry a rising `seq`, and a re-subscribe that states the
//     last one received replays what the socket missed. So a lost socket is
//     reconnected, not reported. A volatile event is never replayed, and a
//     server that kept fewer events than the socket missed replays none, so
//     the caller restores what the replay cannot (reconnected).
//   - A session that is not loaded in the server is not subscribable: the ack
//     lists it under `not_found`. A REST read of the session loads it, which is
//     the caller's job before it subscribes.
//
// Every goroutine of the stream runs under a context of its own, which close
// cancels. The process context is not enough: the server exits by itself on a
// stop, inside the stop's grace period, and on a crash, and neither exit
// cancels the process context.

// kimiFrame is one WebSocket frame: an event, an ack, a ping, or the hello.
type kimiFrame struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Epoch     string          `json:"epoch,omitempty"`
	Volatile  bool            `json:"volatile,omitempty"`
	Code      int             `json:"code,omitempty"`
	Msg       string          `json:"msg,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// kimiCursor is the position of one session's durable event stream.
type kimiCursor struct {
	Seq   int64  `json:"seq"`
	Epoch string `json:"epoch"`
}

// kimiAck is the payload of the ack a subscribe receives.
type kimiAck struct {
	Accepted       []string              `json:"accepted"`
	NotFound       []string              `json:"not_found"`
	ResyncRequired []string              `json:"resync_required"`
	Cursors        map[string]kimiCursor `json:"cursors"`
}

// errKimiSessionNotLoaded reports a subscribe the server refused because the
// session is not loaded. A REST read loads it.
var errKimiSessionNotLoaded = errors.New("the session is not loaded in the Kimi server")

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

// kimiReconnectHandler restores what a re-subscribe did not replay. It runs
// on the goroutine that re-subscribed, once for each session the server
// answered for. replayed is true when the server replayed every durable event
// the socket missed. It is false when the server replayed nothing: it kept
// fewer events than the socket missed, it no longer had the session loaded, or
// the stream held no cursor to replay from. ctx ends when the stream closes.
type kimiReconnectHandler func(ctx context.Context, sessionID string, replayed bool)

// kimiStream owns the event WebSocket of one kap-server.
type kimiStream struct {
	endpoint *providerkit.HTTPEndpoint
	// dispatch handles one event frame. It runs on the dispatcher goroutine, in
	// the order the frames arrived.
	dispatch func(kimiFrame)
	// reconnected runs after each re-subscribe. See kimiReconnectHandler.
	reconnected kimiReconnectHandler
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
}

func newKimiStream(endpoint *providerkit.HTTPEndpoint, agentID string, clock quartz.Clock, dispatch func(kimiFrame), reconnected kimiReconnectHandler) *kimiStream {
	return &kimiStream{
		endpoint:    endpoint,
		dispatch:    dispatch,
		reconnected: reconnected,
		clock:       clock,
		backoff:     kimiStreamFirstBackoff,
		agentID:     agentID,
		queue:       make(chan kimiFrame, kimiStreamQueueDepth),
		acks:        make(map[string]chan kimiFrame),
		sessions:    make(map[string]kimiCursor),
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
	s.closed = true
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
			s.dispatch(frame)
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
		case kimiFrameServerHello:
			// The hello states the heartbeat interval and the protocol version. The
			// reader's timeout already assumes the 10-second heartbeat.
		default:
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

// noteCursor records the position of a durable event, so a reconnect replays
// only what came after it. A volatile event carries the CURRENT durable seq
// rather than a new one, so it never moves the cursor.
func (s *kimiStream) noteCursor(frame kimiFrame) {
	if frame.Volatile || frame.Seq <= 0 || frame.SessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, tracked := s.sessions[frame.SessionID]
	if !tracked {
		return
	}
	if frame.Epoch != "" && frame.Epoch != cursor.Epoch {
		// A new epoch restarts the numbering, so the old seq means nothing in it.
		cursor = kimiCursor{Epoch: frame.Epoch}
	}
	if frame.Seq > cursor.Seq {
		cursor.Seq = frame.Seq
	}
	s.sessions[frame.SessionID] = cursor
}

// subscribe subscribes the current connection to sessionID and returns the ack.
// A session the server has not loaded returns errKimiSessionNotLoaded.
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
	ack, err := s.sendSubscribe(ctx, []string{sessionID}, nil)
	if err == nil && slices.Contains(ack.NotFound, sessionID) {
		err = errKimiSessionNotLoaded
	}
	if err != nil {
		if !tracked {
			s.forget(sessionID)
		}
		return ack, err
	}
	s.adoptAckCursors(ack)
	return ack, nil
}

// unsubscribe stops the events of sessionID. A failure is logged: the session's
// events are dropped by the dispatcher either way, because they no longer match
// the agent's session.
func (s *kimiStream) unsubscribe(sessionID string) {
	s.forget(sessionID)
	if err := s.write(map[string]any{
		"type":    kimiFrameUnsubscribe,
		"payload": map[string][]string{"session_ids": {sessionID}},
	}); err != nil {
		slog.Debug("kimi event stream unsubscribe failed", "agent_id", s.agentID, "session_id", sessionID, "error", err)
	}
}

func (s *kimiStream) forget(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
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
		// A cursor replays what came after it, even at seq 0: the session had no
		// durable event yet when the stream subscribed. A cursor with no epoch
		// cannot replay, because the server answers any epoch it does not hold
		// with resync_required.
		if cursor.Epoch != "" {
			cursors[id] = cursor
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	ack, err := s.sendSubscribe(ctx, ids, cursors)
	if err != nil {
		slog.Warn("kimi event stream resubscribe failed", "agent_id", s.agentID, "error", err)
		return
	}
	s.adoptAckCursors(ack)

	// The server lists a session it accepted but could not replay under both
	// accepted and resync_required.
	var unreplayed []string
	for _, id := range ack.ResyncRequired {
		slog.Warn("kimi event stream missed more events than the server keeps", "agent_id", s.agentID, "session_id", id)
		unreplayed = append(unreplayed, id)
	}
	for _, id := range ack.NotFound {
		slog.Warn("kimi event stream lost a session on reconnect", "agent_id", s.agentID, "session_id", id)
		unreplayed = append(unreplayed, id)
	}
	if s.reconnected == nil {
		return
	}
	for _, id := range ack.Accepted {
		if slices.Contains(unreplayed, id) {
			continue
		}
		_, replayable := cursors[id]
		s.reconnected(ctx, id, replayable)
	}
	for i, id := range unreplayed {
		if !slices.Contains(unreplayed[:i], id) {
			s.reconnected(ctx, id, false)
		}
	}
}

// adoptAckCursors records the cursor the server stated for each accepted
// session, when it is ahead of the one the stream holds.
func (s *kimiStream) adoptAckCursors(ack kimiAck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cursor := range ack.Cursors {
		current, tracked := s.sessions[id]
		if !tracked {
			continue
		}
		if cursor.Epoch != current.Epoch || cursor.Seq > current.Seq {
			s.sessions[id] = cursor
		}
	}
}

// sendSubscribe writes one subscribe frame and waits for its ack.
func (s *kimiStream) sendSubscribe(ctx context.Context, ids []string, cursors map[string]kimiCursor) (kimiAck, error) {
	// A caller that gave up sends nothing: the frame would only be an ack that
	// nobody reads.
	if err := ctx.Err(); err != nil {
		return kimiAck{}, err
	}
	s.mu.Lock()
	s.nextID++
	id := strconv.FormatInt(s.nextID, 10)
	reply := make(chan kimiFrame, 1)
	s.acks[id] = reply
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.acks, id)
		s.mu.Unlock()
	}()

	payload := map[string]any{"session_ids": ids}
	if len(cursors) > 0 {
		payload["cursors"] = cursors
	}
	if err := s.write(map[string]any{"type": kimiFrameSubscribe, "id": id, "payload": payload}); err != nil {
		return kimiAck{}, err
	}
	select {
	case frame, ok := <-reply:
		if !ok {
			// The connection failed. When the caller gave up as well, its own end
			// is the answer that it waits for.
			if err := ctx.Err(); err != nil {
				return kimiAck{}, err
			}
			return kimiAck{}, errors.New("the Kimi event stream closed before it acknowledged the subscribe")
		}
		if frame.Code != kimiCodeOK {
			return kimiAck{}, fmt.Errorf("the Kimi server refused the subscribe: %s (code %d)", frame.Msg, frame.Code)
		}
		var ack kimiAck
		if err := json.Unmarshal(frame.Payload, &ack); err != nil {
			return kimiAck{}, fmt.Errorf("decode the subscribe ack: %w", err)
		}
		return ack, nil
	case <-ctx.Done():
		return kimiAck{}, ctx.Err()
	}
}

// deliverAck hands an ack to the subscribe that waits for it.
func (s *kimiStream) deliverAck(frame kimiFrame) {
	s.mu.Lock()
	reply := s.acks[frame.ID]
	delete(s.acks, frame.ID)
	s.mu.Unlock()
	if reply != nil {
		reply <- frame
	}
}

// failPendingAcks releases every subscribe that waits on the connection that
// just failed. Its ack cannot arrive any more, and the reconnect re-subscribes.
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
	return conn.Write(writeCtx, websocket.MessageText, data)
}
