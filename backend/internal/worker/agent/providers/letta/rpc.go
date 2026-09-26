package letta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The WebSocket command/response transport. One socket per agent, opened after
// the ready line states its URL.

// lettaWSReadLimit caps one inbound frame.
const lettaWSReadLimit = 8 << 20

// lettaDialTimeout limits the WebSocket handshake.
const lettaDialTimeout = 15 * time.Second

// wsConn owns the socket write side. Writes are serialized so two frames never
// interleave.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

// writeJSON sends one command frame.
func (w *wsConn) writeJSON(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.Write(ctx, websocket.MessageText, raw)
}

// close ends the socket.
func (w *wsConn) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.Close(websocket.StatusNormalClosure, "")
}

// sendCommand writes one protocol_v2 command.
func (a *Agent) sendCommand(command any) error {
	a.Mu.Lock()
	conn := a.ws
	a.Mu.Unlock()
	if conn == nil {
		return errAgentStopped
	}
	ctx, cancel := context.WithTimeout(a.Context(), lettaSendWait)
	defer cancel()
	return conn.writeJSON(ctx, command)
}

// readLoop pumps inbound frames until the socket or process ends.
func (a *Agent) readLoop(ctx context.Context, conn *wsConn) {
	for {
		_, data, err := conn.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !a.IsStopped() {
				slog.Debug("letta: websocket read ended", "agent_id", a.AgentID(), "error", err)
			}
			// A handshake that never settles must fail at once, not at its
			// timeout: the socket is gone, so no response can arrive.
			a.settleRuntime(fmt.Errorf("the WebSocket ended before runtime_start was answered: %w", err))
			return
		}
		a.handleFrame(data)
	}
}

// runtimeWaiter holds the runtime_start handshake. The response settles it with
// the error that ended the handshake, or nil once the runtime identity is known.
//
// Start waits on it. An input sent before the identity exists carries an empty
// runtime scope, and the App Server DROPS that input without an error -- no
// `input_accepted`, no `loop_error`, nothing. The turn then never starts and
// the worker logs only silence.
type runtimeWaiter struct {
	once sync.Once
	done chan struct{}
	err  error
}

// newRuntimeWaiter returns an unsettled handshake waiter.
func newRuntimeWaiter() *runtimeWaiter {
	return &runtimeWaiter{done: make(chan struct{})}
}

// settle records the handshake outcome and releases the waiter. A repeat call
// changes nothing, so the first outcome wins.
func (w *runtimeWaiter) settle(err error) {
	w.once.Do(func() {
		w.err = err
		close(w.done)
	})
}

// wait returns the handshake outcome. It fails when exited closes first (the
// process ended), when the timeout elapses, or when ctx ends.
//
// The outcome wins a tie with the exit, the same way ListenWaiter reports an
// address a server printed and then died with.
func (w *runtimeWaiter) wait(ctx context.Context, exited <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return w.err
	default:
	}
	select {
	case <-w.done:
		return w.err
	case <-exited:
		select {
		case <-w.done:
			return w.err
		default:
		}
		return fmt.Errorf("%w: the server exited before runtime_start was answered", errAgentStopped)
	case <-timer.C:
		return fmt.Errorf("the server answered no runtime_start within %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settleRuntime settles the runtime_start handshake, when one is open.
func (a *Agent) settleRuntime(err error) {
	a.Mu.Lock()
	w := a.runtimeReady
	a.Mu.Unlock()
	if w != nil {
		w.settle(err)
	}
}

// handleFrame dispatches one protocol_v2 message.
//
// Each server->client message names its body in its OWN field, never under
// `payload`: `stream_delta` carries `delta`, `control_request` carries
// `request`, `update_loop_status` carries `loop_status` and
// `update_subagent_state` carries `subagents`. A handler that reads `payload`
// reads nothing and drops the frame.
//
// Only frames the transcript draws become rows. A protocol state snapshot or a
// command acknowledgement becomes nothing at all: each `update_device_status`
// alone is 3 KB, and a chat that stored them pushed the reader's own answer out
// of the virtualized transcript.
func (a *Agent) handleFrame(line []byte) {
	if a.IsDiscardingOutput() {
		return
	}
	// The wire discriminator is `type`, not `kind`, on both directions.
	var head struct {
		Type       string          `json:"type"`
		Kind       string          `json:"kind"`
		Delta      json.RawMessage `json:"delta"`
		Request    json.RawMessage `json:"request"`
		LoopStatus json.RawMessage `json:"loop_status"`
		Subagents  json.RawMessage `json:"subagents"`
		RequestID  string          `json:"request_id"`
		Error      string          `json:"error"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		slog.Debug("letta: unparseable frame", "agent_id", a.AgentID(), "error", err)
		return
	}
	typ := head.Type
	if typ == "" {
		typ = head.Kind
	}

	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	slog.Debug("letta: frame", "agent_id", a.AgentID(), "type", typ, "len", len(line))
	switch typ {
	case "stream_delta":
		a.onStreamDelta(head.Delta)
	case "turn_finished":
		a.onTurnFinished(line)
	case "update_subagent_state":
		a.onSubagentState(line, head.Subagents)
	case "update_loop_status":
		a.onLoopStatus(head.LoopStatus)
	case "control_request":
		a.onControlRequest(line, head.Request, head.RequestID)
	case "runtime_start_response":
		a.adoptRuntime(line)
	case "loop_error":
		a.persistNotification(line)
	case "input_accepted", "abort_message_response":
		// Delivery and interrupt acknowledgements. The turn flag already
		// carries what the reader sees.
	default:
		// An unknown message kind, a `*_response` ack and a state snapshot
		// (`update_queue`, `update_device_status`) all move nothing.
	}
}

// persistNotification stores a notification row verbatim.
func (a *Agent) persistNotification(payload []byte) {
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, payload); err != nil {
		slog.Debug("letta: persist notification failed", "agent_id", a.AgentID(), "error", err)
	}
}

// persistRow stores one transcript row verbatim.
func (a *Agent) persistRow(payload []byte, span agent.SpanInfo) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload}, span); err != nil {
		slog.Debug("letta: persist message failed", "agent_id", a.AgentID(), "error", err)
	}
}

// dial opens the WebSocket the ready line stated.
func dial(ctx context.Context, url string) (*wsConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, lettaDialTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: lettaDialTimeout},
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(lettaWSReadLimit)
	return &wsConn{conn: conn}, nil
}

// adoptRuntime records the agent and conversation ids the runtime_start
// response returns, and settles the handshake Start waits on.
//
// A response that names no runtime, or reports a failure, settles it with that
// error: an agent that never learned its runtime identity cannot take input.
func (a *Agent) adoptRuntime(payload []byte) {
	var result struct {
		Success bool `json:"success"`
		Runtime *struct {
			AgentID        string `json:"agent_id"`
			ConversationID string `json:"conversation_id"`
		} `json:"runtime"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		slog.Debug("letta: runtime_start response unreadable", "agent_id", a.AgentID(), "error", err)
		a.settleRuntime(fmt.Errorf("runtime_start response unreadable: %w", err))
		return
	}
	if !result.Success {
		slog.Warn("letta: runtime_start failed", "agent_id", a.AgentID(), "error", result.Error)
		a.settleRuntime(fmt.Errorf("runtime_start failed: %s", result.Error))
		return
	}
	if result.Runtime == nil {
		a.settleRuntime(errors.New("runtime_start response states no runtime"))
		return
	}
	a.Mu.Lock()
	// The worker's agent id is NOT Letta's agent id. Always adopt the
	// server's ids: a runtime scoped to the wrong agent id produces no turn.
	a.agentID = result.Runtime.AgentID
	a.conversationID = result.Runtime.ConversationID
	agentID := a.agentID
	conversationID := a.conversationID
	a.Mu.Unlock()
	a.sink.UpdateSessionID(conversationID)
	a.sink.BroadcastSessionInfo(map[string]any{
		"agentId":        agentID,
		"conversationId": conversationID,
	})
	// Settle last, so a waiter that returns has the ids to read.
	a.settleRuntime(nil)
}
