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

	"github.com/coder/quartz"
	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

// readLoop reads inbound frames until the socket or process ends.
func (a *Agent) readLoop(ctx context.Context, conn *wsConn) {
	for {
		_, data, err := conn.conn.Read(ctx)
		if err != nil {
			failure := fmt.Errorf("the Letta WebSocket ended before requests settled: %w", err)
			a.Mu.Lock()
			unexpected := ctx.Err() == nil && !a.StoppedLocked()
			if unexpected && a.transportErr == nil {
				a.transportErr = failure
			}
			cancelRuntime := a.cancelRuntime
			a.Mu.Unlock()
			a.settleRuntime(failure)
			a.failPendingRequests(failure)
			// This adapter has no reconnect path. A runtime without its socket reader cannot receive or complete commands.
			if unexpected {
				slog.Warn("letta native connection ended", "agent_id", a.AgentID(), "error", err)
				if cancelRuntime != nil {
					cancelRuntime()
				}
			}
			return
		}
		a.handleFrame(data)
	}
}

// runtimeWaiter retains one native control outcome for startup or a later request.
// Startup waits for the runtime identity before it accepts user input.
// The native server drops an input without a runtime scope and sends no receipt for it.
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

// wait returns the native reply outcome on the injected clock.
// A ready reply wins against process exit and timeout.
func (w *runtimeWaiter) wait(ctx context.Context, clock quartz.Clock, exited <-chan struct{}, timeout time.Duration) error {
	outcome := providerkit.AwaitSignal(ctx, clock, w.done, exited, timeout, "letta runtime")
	switch outcome {
	case providerkit.SignalArrived:
		return w.err
	case providerkit.SignalExited:
		return fmt.Errorf("%w before the reply arrived", errAgentStopped)
	case providerkit.SignalTimedOut:
		return fmt.Errorf("the Letta runtime did not reply within %s", timeout)
	case providerkit.SignalCancelled:
		return ctx.Err()
	default:
		return fmt.Errorf("unknown Letta wait outcome: %d", outcome)
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
// Each server message carries its body in a dedicated field:
//   - stream_delta uses delta.
//   - control_request uses request.
//   - update_loop_status uses loop_status.
//   - update_subagent_state uses subagents.
//
// Known protocol state and acknowledgements remain internal.
// Repeated snapshots would displace visible replies in the virtualized transcript.
// An unknown frame persists only when its runtime matches the current session.
func (a *Agent) handleFrame(line []byte) {
	if a.IsDiscardingOutput() {
		return
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.outputEnded || a.IsDiscardingOutput() {
		return
	}
	var head struct {
		Type       string          `json:"type"`
		Runtime    *runtimeScope   `json:"runtime"`
		Delta      json.RawMessage `json:"delta"`
		Request    json.RawMessage `json:"request"`
		LoopStatus json.RawMessage `json:"loop_status"`
		Subagents  json.RawMessage `json:"subagents"`
		SubagentID string          `json:"subagent_id"`
		RequestID  string          `json:"request_id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		slog.Debug("letta could not decode the frame header", "agent_id", a.AgentID(), "error", err)
		a.persistUnknownFrame(line)
		return
	}
	typ := head.Type

	slog.Debug("letta: frame", "agent_id", a.AgentID(), "type", typ, "len", len(line))
	switch typ {
	case contracts.LettaMessageStreamDelta:
		a.onStreamDelta(head.Delta, head.SubagentID, head.Runtime)
	case contracts.LettaMessageTurnFinished:
		a.onTurnFinished(line)
	case contracts.LettaMessageUpdateSubagentState:
		a.onSubagentState(head.Subagents)
	case contracts.LettaMessageUpdateLoopStatus:
		a.onLoopStatus(head.LoopStatus)
	case contracts.LettaMessageControlRequest:
		a.onControlRequest(line, head.Request, head.RequestID)
	case "runtime_start_response":
		a.adoptRuntime(line)
	case "list_models_response":
		a.handleListModelsResponse(line)
	case "update_model_response":
		a.handleUpdateModelResponse(line)
	case "update_device_status":
		a.handleDeviceStatus(line)
	case "sync_response":
		a.handleSettingsSyncResponse(line)
	case contracts.LettaMessageInputAccepted:
		a.handlePermissionReceipt(line, head.RequestID)
	case contracts.LettaMessageAbortMessageResponse:
		// The native turn events report the result of an interrupt.
	case contracts.LettaMessageUpdateQueue, contracts.LettaMessageAppServerInfoResponse:
		// These frames contain protocol state, not transcript content.
	case "file_ops":
		// Letta synchronizes its editor here. The native tool result already records the file write.
	default:
		a.persistUnknownFrame(line)
	}
}

func (a *Agent) failPendingRequests(err error) {
	a.Mu.Lock()
	var waiters []*runtimeWaiter
	if a.catalogPending != nil {
		waiters = append(waiters, a.catalogPending.waiter)
	}
	if a.modelPending != nil {
		waiters = append(waiters, a.modelPending.waiter)
	}
	if a.modePending != nil {
		waiters = append(waiters, a.modePending.waiter)
	}
	for _, delivery := range a.permissionDeliveries {
		waiters = append(waiters, delivery.waiter)
	}
	a.Mu.Unlock()
	for _, waiter := range waiters {
		waiter.settle(err)
	}
}

// persistRowTo stores one transcript row in its native session.
func (a *Agent) persistRowTo(target agent.ProviderServices, payload []byte, span agent.SpanInfo) {
	a.Mu.Lock()
	sessionID := a.conversationID
	a.Mu.Unlock()
	if err := target.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload, AgentSessionID: sessionID}, span); err != nil {
		slog.Error("letta: persist message failed", "agent_id", a.AgentID(), "error", err)
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
// A response that identifies no runtime, or reports a failure, settles it with that
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
