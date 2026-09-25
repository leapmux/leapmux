package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Cline's hub WebSocket, the transport of every command and event.
//
// One connection carries JSON frames of three kinds in each direction: a
// command, its reply, and an event. Four rules of the daemon shape this file:
//
//   - The first command on a connection must be `client.register`, and every
//     later command must state the same client id. The worker picks the id.
//   - A client receives events only after `stream.subscribe`. The daemon
//     registers the subscription as soon as it reads the frame, so a command
//     written after it cannot race it.
//   - The daemon keeps every event in a durable log with a rising sequence, and
//     a subscribe that states the last sequence it received replays what the
//     connection missed. So the client reconnects a lost connection and
//     reports nothing. Every production daemon of the data directory shares
//     the log, so the client always scopes a replay to the agent's session.
//   - The daemon pings every 30 seconds and closes a connection that does not
//     answer. coder/websocket answers a ping only while a Read runs, so one
//     goroutine does nothing but read, and the events go to a second goroutine
//     through a queue. An event handler that blocks -- on the database, on a
//     broadcast -- then delays the transcript and never the pong.

// hubFrame is one frame of the hub transport.
type hubFrame struct {
	Kind          string          `json:"kind"`
	Envelope      json.RawMessage `json:"envelope,omitempty"`
	ClientID      string          `json:"clientId,omitempty"`
	SessionID     string          `json:"sessionId,omitempty"`
	SinceSequence *int64          `json:"sinceSequence,omitempty"`
}

// hubCommandEnvelope is the envelope of one command.
type hubCommandEnvelope struct {
	Version   string `json:"version"`
	Command   string `json:"command"`
	RequestID string `json:"requestId"`
	ClientID  string `json:"clientId"`
	SessionID string `json:"sessionId,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

// hubReply is the envelope of one reply.
type hubReply struct {
	RequestID string          `json:"requestId"`
	OK        bool            `json:"ok"`
	Payload   json.RawMessage `json:"payload"`
	Error     *hubError       `json:"error"`
}

// hubError is the error of a refused command.
type hubError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// hubEvent is one event. Raw is the envelope as the daemon sent it, which is
// what the worker persists as a transcript row.
type hubEvent struct {
	Event     string `json:"event"`
	EventID   string `json:"eventId"`
	SessionID string `json:"sessionId"`
	Sequence  int64  `json:"sequence"`
	// Timestamp is when the daemon published the event, in milliseconds since
	// the Unix epoch.
	Timestamp int64           `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Raw       json.RawMessage `json:"-"`
}

// HubCommandError reports a command that the daemon refused.
type HubCommandError struct {
	Command string
	Code    string
	Message string
}

func (e *HubCommandError) Error() string {
	return fmt.Sprintf("the Cline hub refused %s: %s (%s)", e.Command, e.Message, e.Code)
}

// hubErrorCode returns the code of a refused command, and whether err is one.
func hubErrorCode(err error) (string, bool) {
	var refused *HubCommandError
	if errors.As(err, &refused) {
		return refused.Code, true
	}
	return "", false
}

// errHubConnectionLost ends a command whose connection failed before its reply
// arrived. The daemon may have run the command anyway.
var errHubConnectionLost = errors.New("the connection to the Cline hub was lost")

// errHubClosed refuses a command after close.
var errHubClosed = errors.New("the Cline hub connection is closed")

// The limits of the connection.
const (
	// hubReadLimit caps one frame. A reply of `session.messages` carries a whole
	// conversation, and an event can carry a tool's whole output.
	hubReadLimit = 128 << 20
	// hubWriteTimeout limits one frame the worker writes.
	hubWriteTimeout = 10 * time.Second
	// hubQueueDepth caps the events the reader holds for the dispatcher. A
	// dispatcher that falls this far behind makes the reader wait, and a reader
	// that waits stops answering pings. The daemon then closes the connection,
	// and the re-subscribe replays what the connection missed. That is the
	// intended outcome of a stall this long, not an error.
	hubQueueDepth = 4096
	// hubMaxBackoff caps the wait between two reconnect attempts.
	hubMaxBackoff = 5 * time.Second
)

// hubClient owns the WebSocket of one private daemon.
type hubClient struct {
	endpoint *providerkit.HTTPEndpoint
	path     string
	token    string
	clientID string
	agentID  string
	// registration is the payload of client.register, which a reconnect sends
	// again with the same client id.
	registration map[string]any
	// dispatch handles one event, on the dispatcher goroutine, in the order the
	// events arrived.
	dispatch func(hubEvent)
	// backoff is the first wait between reconnect attempts. It doubles up to
	// hubMaxBackoff. A test changes it before start.
	backoff time.Duration
	clock   quartz.Clock

	queue   chan hubEvent
	workers sync.WaitGroup

	mu sync.Mutex
	// ctx is the context that every goroutine of the client runs under, and
	// cancel ends it. start sets both, and close calls cancel. Each write takes
	// its deadline from ctx (see writeFrame).
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn
	closed bool
	nextID uint64
	// pending holds the reply channel of each command that waits.
	pending map[string]chan hubReply
	// session is the session that the subscription covers, and lastSequence
	// the sequence of the last event that the client received for it. A
	// reconnect replays from there.
	session      string
	lastSequence int64
	// leaving is the session that a subscribe in flight leaves, and
	// leavingSequence its cursor. The events of that session can still arrive
	// until the frame's outcome is known, and they move this cursor, so a
	// subscribe that fails restores a cursor that covers them. Both are empty
	// while no subscribe moves the stream.
	leaving         string
	leavingSequence int64
}

func newHubClient(endpoint *providerkit.HTTPEndpoint, path, token, clientID, agentID string, registration map[string]any, dispatch func(hubEvent), clock quartz.Clock) *hubClient {
	return &hubClient{
		clock:        clock,
		endpoint:     endpoint,
		path:         path,
		token:        token,
		clientID:     clientID,
		agentID:      agentID,
		registration: registration,
		dispatch:     dispatch,
		backoff:      250 * time.Millisecond,
		queue:        make(chan hubEvent, hubQueueDepth),
		pending:      make(map[string]chan hubReply),
	}
}

// start opens the first connection, registers the client, and starts the
// reader and the dispatcher. Both run until close runs or parent ends. The
// first connection must succeed: a daemon that published its address and then
// refuses the connection cannot run the agent.
func (c *hubClient) start(parent context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return errHubClosed
	}
	c.ctx, c.cancel = ctx, cancel
	c.mu.Unlock()

	conn, err := c.dial(ctx, timeout)
	if err != nil {
		cancel()
		return err
	}
	if !c.adopt(conn) {
		cancel()
		return errHubClosed
	}
	c.workers.Add(2)
	go func() {
		defer c.workers.Done()
		c.runDispatcher(ctx)
	}()
	go func() {
		defer c.workers.Done()
		c.runReader(ctx, conn)
	}()
	registerCtx, registerCancel := context.WithTimeout(ctx, timeout)
	defer registerCancel()
	if err := c.register(registerCtx); err != nil {
		c.close()
		return err
	}
	return nil
}

// adopt makes conn the current connection, unless close ran first.
func (c *hubClient) adopt(conn *websocket.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return false
	}
	c.conn = conn
	return true
}

// dial opens one connection. The token travels in the subprotocol, which is the
// only place the daemon reads it from.
func (c *hubClient) dial(ctx context.Context, timeout time.Duration) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.endpoint.OpenWebSocket(dialCtx, c.path, &websocket.DialOptions{
		Subprotocols: []string{hubAuthSubprotocolPrefix + c.token},
	})
	if err != nil {
		return nil, fmt.Errorf("open the Cline hub connection: %w", err)
	}
	conn.SetReadLimit(hubReadLimit)
	return conn, nil
}

// register registers the client on the current connection.
func (c *hubClient) register(ctx context.Context) error {
	if _, err := c.command(ctx, commandClientRegister, "", c.registration); err != nil {
		return fmt.Errorf("register with the Cline hub: %w", err)
	}
	return nil
}

// subscribe scopes the event stream to sessionID, with live events only. The
// cursor of a new session starts empty: the daemons share the durable log, and
// it holds the events of every earlier run of a resumed session, which the
// worker must not dispatch again.
//
// A caller that gave up changes nothing and sends nothing. A frame that cannot
// leave changes nothing either: the stream still follows the session that it
// followed, so a reconnect subscribes that session again, from its cursor.
//
// The client moves to sessionID before the write, for two reasons. The daemon
// streams the session's events as soon as it reads the frame, and each of them
// moves the cursor (decodeEvent). And a reconnect that comes between the write
// and its outcome must subscribe the session that the frame asked for. The
// client keeps the cursor of the session that it leaves until the outcome is
// known (hubClient.leaving).
func (c *hubClient) subscribe(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	switching := c.session != sessionID
	if switching {
		c.leaving, c.leavingSequence = c.session, c.lastSequence
		c.session, c.lastSequence = sessionID, 0
	}
	c.mu.Unlock()
	err := c.writeFrame(hubFrame{Kind: frameStreamSubscribe, ClientID: c.clientID, SessionID: sessionID})
	if switching {
		c.mu.Lock()
		if err != nil && c.session == sessionID {
			c.session, c.lastSequence = c.leaving, c.leavingSequence
		}
		c.leaving, c.leavingSequence = "", 0
		c.mu.Unlock()
	}
	return err
}

// unsubscribe ends the event stream of sessionID.
func (c *hubClient) unsubscribe(sessionID string) {
	if err := c.writeFrame(hubFrame{Kind: frameStreamUnsubscribe, ClientID: c.clientID, SessionID: sessionID}); err != nil {
		slog.Debug("cline hub unsubscribe failed", "agent_id", c.agentID, "session_id", sessionID, "error", err)
	}
}

// close ends the client: it cancels the context every goroutine selects on,
// closes the current connection, and fails every command that waits. It
// returns at once, and it is safe to call more than once.
func (c *hubClient) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	cancel, conn := c.cancel, c.conn
	c.conn = nil
	pending := c.pending
	c.pending = make(map[string]chan hubReply)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	for _, reply := range pending {
		close(reply)
	}
}

// wait blocks until every goroutine of the client returned. Only close, or the
// end of the context start took, makes them return.
func (c *hubClient) wait() {
	c.workers.Wait()
}

// command sends one command and waits for its reply. A refused command returns
// a HubCommandError.
func (c *hubClient) command(ctx context.Context, name, sessionID string, payload any) (json.RawMessage, error) {
	_, replies, err := c.send(ctx, name, sessionID, payload, nil)
	if err != nil {
		return nil, err
	}
	return c.await(ctx, name, replies)
}

// send writes one command and returns its request id and the channel its reply
// arrives on, without waiting. The channel closes without a reply when the
// connection fails or the client closes.
//
// prepare, when set, runs with the request id BEFORE the frame leaves. A caller
// that waits for an event which states the request id -- run.started -- records
// the id there, because the event can arrive before send returns.
func (c *hubClient) send(ctx context.Context, name, sessionID string, payload any, prepare func(requestID string)) (string, <-chan hubReply, error) {
	// A caller that gave up sends nothing: the command would run with nobody to
	// read its reply.
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf("%s: %w", name, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", nil, errHubClosed
	}
	c.nextID++
	requestID := "leapmux_" + strconv.FormatUint(c.nextID, 10)
	replies := make(chan hubReply, 1)
	c.pending[requestID] = replies
	c.mu.Unlock()
	if prepare != nil {
		prepare(requestID)
	}

	envelope, err := json.Marshal(hubCommandEnvelope{
		Version:   hubProtocolVersion,
		Command:   name,
		RequestID: requestID,
		ClientID:  c.clientID,
		SessionID: sessionID,
		Payload:   payload,
	})
	if err != nil {
		c.forget(requestID)
		return "", nil, fmt.Errorf("encode the Cline hub command %s: %w", name, err)
	}
	if err := c.writeFrame(hubFrame{Kind: frameCommand, Envelope: envelope}); err != nil {
		c.forget(requestID)
		return "", nil, err
	}
	return requestID, replies, nil
}

// await waits for the reply of one command.
func (c *hubClient) await(ctx context.Context, name string, replies <-chan hubReply) (json.RawMessage, error) {
	select {
	case reply, ok := <-replies:
		if !ok {
			// The connection failed. When the caller gave up as well, its own end
			// is the answer that it waits for.
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return nil, fmt.Errorf("%s: %w", name, errHubConnectionLost)
		}
		if !reply.OK {
			refused := &HubCommandError{Command: name}
			if reply.Error != nil {
				refused.Code, refused.Message = reply.Error.Code, reply.Error.Message
			}
			return nil, refused
		}
		return reply.Payload, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	}
}

// forget drops the reply channel of a command that will not wait.
func (c *hubClient) forget(requestID string) {
	c.mu.Lock()
	delete(c.pending, requestID)
	c.mu.Unlock()
}

// writeFrame sends one frame on the current connection.
//
// The write's deadline comes from the client's own context, never from the
// caller's. coder/websocket closes the whole connection when the context of a
// write ends (setupWriteTimeout), and the connection carries the agent's
// session. So a caller that gives up must not end the stream of the agent.
func (c *hubClient) writeFrame(frame hubFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode a Cline hub frame: %w", err)
	}
	c.mu.Lock()
	conn, closed, clientCtx := c.conn, c.closed, c.ctx
	c.mu.Unlock()
	if closed {
		return errHubClosed
	}
	if conn == nil {
		return errHubConnectionLost
	}
	writeCtx, cancel := context.WithTimeout(clientCtx, hubWriteTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write to the Cline hub: %w", err)
	}
	return nil
}

// runDispatcher hands every queued event to dispatch, in order, until the
// client closes.
func (c *hubClient) runDispatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-c.queue:
			// A select picks at random among the ready cases, so an event can win
			// over a close that came first. A closed client dispatches nothing.
			if ctx.Err() != nil {
				return
			}
			c.dispatch(event)
		}
	}
}

// runReader reads frames until the connection fails, then reconnects,
// registers again and replays what it missed. It returns when the client
// closes.
func (c *hubClient) runReader(ctx context.Context, conn *websocket.Conn) {
	for {
		err := c.readUntilFailure(ctx, conn)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("cline hub connection lost; reconnecting", "agent_id", c.agentID, "error", err)
		next, ok := c.reconnect(ctx)
		if !ok {
			return
		}
		conn = next
		// The reader already counts in workers, so the count is above zero and the
		// Add cannot race a wait that already returned.
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			c.resume(ctx)
		}()
	}
}

// reconnect dials until a dial succeeds, and waits longer after each failure.
// It returns false when the client closes first.
func (c *hubClient) reconnect(ctx context.Context) (*websocket.Conn, bool) {
	backoff := c.backoff
	timer := c.clock.NewTimer(backoff, "cline", "hub-reconnect")
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
		}
		conn, err := c.dial(ctx, hubWriteTimeout)
		if err != nil {
			slog.Debug("cline hub reconnect failed", "agent_id", c.agentID, "error", err)
			backoff = min(backoff*2, hubMaxBackoff)
			timer.Reset(backoff)
			continue
		}
		if !c.adopt(conn) {
			return nil, false
		}
		return conn, true
	}
}

// resume registers the client on a new connection and replays the events of
// the session that the old one missed.
func (c *hubClient) resume(ctx context.Context) {
	registerCtx, cancel := context.WithTimeout(ctx, hubWriteTimeout)
	defer cancel()
	if err := c.register(registerCtx); err != nil {
		slog.Warn("cline hub re-registration failed", "agent_id", c.agentID, "error", err)
		return
	}
	c.mu.Lock()
	session, since := c.session, c.lastSequence
	c.mu.Unlock()
	if session == "" {
		return
	}
	frame := hubFrame{Kind: frameStreamSubscribe, ClientID: c.clientID, SessionID: session}
	if since > 0 {
		// The replay starts after the last event received. With no event received
		// yet the worker holds no cursor, and a replay from the start would bring
		// back the events of every earlier run of the session, so the stream goes
		// on live.
		frame.SinceSequence = &since
	}
	if err := c.writeFrame(frame); err != nil {
		slog.Warn("cline hub re-subscribe failed", "agent_id", c.agentID, "session_id", session, "error", err)
	}
}

// readUntilFailure reads frames until the connection fails. It fails every
// command that waits on the connection, because its reply cannot arrive any
// more.
func (c *hubClient) readUntilFailure(ctx context.Context, conn *websocket.Conn) error {
	defer func() {
		_ = conn.CloseNow()
		c.failPending()
	}()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var frame hubFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			slog.Warn("cline hub sent a frame that is not JSON", "agent_id", c.agentID, "error", err)
			continue
		}
		switch frame.Kind {
		case frameReply:
			c.deliverReply(frame.Envelope)
		case frameEvent:
			event, ok := c.decodeEvent(frame.Envelope)
			if !ok {
				continue
			}
			select {
			case c.queue <- event:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			slog.Debug("cline hub sent a frame of an unknown kind", "agent_id", c.agentID, "kind", frame.Kind)
		}
	}
}

// decodeEvent reads one event envelope and records its sequence, so a
// reconnect replays only what came after it.
func (c *hubClient) decodeEvent(raw json.RawMessage) (hubEvent, bool) {
	var event hubEvent
	if err := json.Unmarshal(raw, &event); err != nil || event.Event == "" {
		slog.Warn("cline hub sent an event that cannot be read", "agent_id", c.agentID, "error", err)
		return hubEvent{}, false
	}
	event.Raw = append(json.RawMessage(nil), raw...)
	if event.Sequence > 0 {
		c.mu.Lock()
		switch {
		case event.SessionID == c.session:
			c.lastSequence = max(c.lastSequence, event.Sequence)
		case c.leaving != "" && event.SessionID == c.leaving:
			c.leavingSequence = max(c.leavingSequence, event.Sequence)
		}
		c.mu.Unlock()
	}
	return event, true
}

// deliverReply hands a reply to the command that waits for it.
func (c *hubClient) deliverReply(raw json.RawMessage) {
	var reply hubReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		slog.Warn("cline hub sent a reply that cannot be read", "agent_id", c.agentID, "error", err)
		return
	}
	c.mu.Lock()
	replies := c.pending[reply.RequestID]
	delete(c.pending, reply.RequestID)
	c.mu.Unlock()
	if replies != nil {
		replies <- reply
	}
}

// failPending closes the reply channel of every command that waits.
func (c *hubClient) failPending() {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan hubReply)
	c.mu.Unlock()
	for _, replies := range pending {
		close(replies)
	}
}
