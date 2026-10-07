package providerkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// JSONRPCProcess shares request correlation across JSON-RPC providers.
type JSONRPCProcess struct {
	Process
	Correlator[int64]
	nextReqID atomic.Int64
	// A nil encoder selects newline framing. Native Copilot selects Content-Length framing.
	FrameMessage func([]byte) []byte

	// outstandingMu guards outstandingControls and withdrawGeneration.
	outstandingMu sync.Mutex
	// outstandingControls holds unanswered control requests by LeapMux request ID.
	// Publication adds an entry. An answer or withdrawal removes it.
	// A failed write restores the entry only when no bytes reached the provider.
	// These rules keep native requests and browser controls consistent.
	outstandingControls map[string]outstandingControlRequest
	// withdrawGeneration distinguishes withdrawal from an answer during publication.
	// Both paths remove a request. Only withdrawal increments this counter.
	// A presence check alone can cancel a control that the reader already answered.
	withdrawGeneration uint64
}

// frameJSONRPCMessage supplies the same framing for waiting and detached writes.
// A newline transport needs a final newline, or its scanner waits indefinitely.
func (b *JSONRPCProcess) frameJSONRPCMessage(data []byte) []byte {
	if b.FrameMessage != nil {
		return b.FrameMessage(data)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return data
}

func (b *JSONRPCProcess) writeJSONRPCMessage(data []byte) error {
	return b.WriteStdin(b.frameJSONRPCMessage(data))
}

// SendRawInput preserves provider JSON inside the selected frame and removes its answered control.
// Remove the control before the write because withdrawal can occur during that write.
// A write that delivers no bytes restores the control for another answer.
// ErrDeliveryUncertain leaves it removed because the provider can already hold the answer.
// Restoring it in that case can send a second answer to a retired request.
func (b *JSONRPCProcess) SendRawInput(data []byte, stop agent.StopContext) error {
	restore := b.takeOutstandingControl(data)
	err := b.writeRawFrame(data)
	if err != nil && !errors.Is(err, agent.ErrDeliveryUncertain) {
		restore()
	}
	return err
}

func (b *JSONRPCProcess) writeRawFrame(data []byte) error {
	if b.FrameMessage == nil {
		return b.Process.SendRawInput(data, agent.StopContext{})
	}
	if b.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	if err := b.writeJSONRPCMessage(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

// jsonrpcMessage encodes requests and notifications. Local request IDs start at one.
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponseMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

type jsonrpcResponsePayload struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// decodeJSONRPCResponse keeps application fields separate from protocol errors.
func decodeJSONRPCResponse(raw json.RawMessage) (json.RawMessage, error) {
	var response jsonrpcResponsePayload
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode json-rpc response: %w", err)
	}
	if !isJSONNull(response.Error) {
		// A native error can include an unused result member with an explicit null.
		// Null states no result, so preserve the native error code in this case.
		// Rejecting it would report uncertain delivery instead of the provider's refusal.
		if !isJSONNull(response.Result) {
			return nil, errors.New("json-rpc response contains both a result and an error")
		}
		responseErr, ok := parseJSONRPCError(response.Error)
		if !ok {
			return nil, errors.New("json-rpc response contains an invalid error")
		}
		return nil, responseErr
	}
	if len(response.Result) == 0 {
		return nil, errors.New("json-rpc response has no result")
	}
	return response.Result, nil
}

func (b *JSONRPCProcess) SendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	return b.SendRequestObserved(method, params, timeout, nil)
}

// SendRequestObserved observes a decoded reply before the waiting caller resumes.
// The observer runs on the reader. It must not wait for another request.
// Write failures, cancellation, and timeouts do not produce a reply observation.
func (b *JSONRPCProcess) SendRequestObserved(method string, params json.RawMessage, timeout time.Duration, observe func(json.RawMessage, error)) (json.RawMessage, error) {
	reqID := b.nextReqID.Add(1)

	var rawObserver func(json.RawMessage)
	if observe != nil {
		rawObserver = func(raw json.RawMessage) {
			result, err := decodeJSONRPCResponse(raw)
			observe(result, err)
		}
	}
	ch, release, err := b.RegisterObserved(reqID, rawObserver)
	if err != nil {
		return nil, &jsonRPCRequestNotSentError{cause: err}
	}
	defer release()

	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, &jsonRPCRequestNotSentError{cause: fmt.Errorf("marshal request: %w", err)}
	}

	if err := b.writeJSONRPCMessage(data); err != nil {
		return nil, jsonRPCRequestWriteError(err)
	}

	resp, err := b.AwaitResponse(ch, method, timeout)
	if err != nil {
		return nil, err
	}
	return decodeJSONRPCResponse(resp)
}

func (b *JSONRPCProcess) SendDetachedRequest(method string, params json.RawMessage, handle func(json.RawMessage, error)) error {
	reqID := b.nextReqID.Add(1)
	ch, release, err := b.Register(reqID)
	if err != nil {
		return &jsonRPCRequestNotSentError{cause: err}
	}
	data, err := json.Marshal(jsonrpcMessage{JSONRPC: "2.0", ID: reqID, Method: method, Params: params})
	if err != nil {
		release()
		return &jsonRPCRequestNotSentError{cause: fmt.Errorf("marshal request: %w", err)}
	}
	if err := b.writeJSONRPCMessage(data); err != nil {
		release()
		return jsonRPCRequestWriteError(err)
	}
	go func() {
		defer release()
		resp, err := b.AwaitResponse(ch, method, 0)
		if err == nil {
			resp, err = decodeJSONRPCResponse(resp)
		}
		handle(resp, err)
	}()
	return nil
}

func (b *JSONRPCProcess) SendNotification(method string, params json.RawMessage) error {
	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	if err := b.writeJSONRPCMessage(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

func (b *JSONRPCProcess) SendResponse(id json.RawMessage, result any) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (b *JSONRPCProcess) SendErrorResponse(id json.RawMessage, code int, message string) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

// jsonrpcErrMethodNotFound identifies a JSON-RPC method with no receiver handler.
const jsonrpcErrMethodNotFound = -32601

// RefuseUnsupportedRequest replies with -32601 for an unsupported inbound request.
// A notification has no ID and requires no reply.
// An unanswered request leaves the native turn waiting until its own timeout.
//
// The stdout reader must not wait for a stdin write.
// If the child stops reading stdin, that write blocks and stdout stops draining.
// Both processes then deadlock. Holding the output mutex also prevents Stop from closing stdin.
// The ZCode interceptResponse handler follows the same rule.
//
// One writer drains one queue. A goroutine per reply consumes an 8 KiB stack per unanswered frame.
// A single writer also preserves reply order.
// Preserve the raw request ID, including an integer above 2^53.
func (b *JSONRPCProcess) RefuseUnsupportedRequest(line *ParsedLine) {
	if line.Method == "" || !line.HasID() {
		return
	}
	b.SendErrorResponseDetached(line.ID, jsonrpcErrMethodNotFound,
		"Method not supported: "+line.Method, "refuse "+line.Method)
}

// SendResponseDetached queues a reply without waiting for its stdin write.
// The stdout reader uses this method because a waiting stdin write can deadlock both processes.
// One writer preserves queue order for waiting and detached writes.
// Base.Interrupt and Codex.Interrupt require a control answer before its following cancel.
// The describe argument identifies a frame in failure logs when no caller waits for its write.
func (b *JSONRPCProcess) SendResponseDetached(id json.RawMessage, result any, describe string) {
	b.writeDetachedJSONRPCResponse(jsonrpcResponseMessage{JSONRPC: "2.0", ID: id, Result: result}, describe)
}

// SendErrorResponseDetached is SendResponseDetached for an error reply.
func (b *JSONRPCProcess) SendErrorResponseDetached(id json.RawMessage, code int, message, describe string) {
	b.writeDetachedJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   map[string]interface{}{"code": code, "message": message},
	}, describe)
}

func (b *JSONRPCProcess) writeDetachedJSONRPCResponse(resp jsonrpcResponseMessage, describe string) {
	data, err := json.Marshal(resp)
	if err != nil {
		slog.Warn("Marshal a response", "agent_id", b.agentID, "frame", describe, "error", err)
		return
	}
	b.writeStdinDetached(b.frameJSONRPCMessage(data), describe)
}

func (b *JSONRPCProcess) writeJSONRPCResponse(resp jsonrpcResponseMessage) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	if err := b.writeJSONRPCMessage(data); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

// handleJSONRPCResponse delivers a response to its waiting request.
func (b *JSONRPCProcess) handleJSONRPCResponse(line *ParsedLine) bool {
	if !line.HasID() || line.Method != "" {
		return false
	}

	reqID, ok := line.IDInt64()
	if !ok {
		return false
	}

	return b.Deliver(reqID, line.Raw)
}

// ReadOutputLoop reads framed JSON messages and dispatches requests and events.
func (b *JSONRPCProcess) ReadOutputLoop(scanner *bufio.Scanner, handle LineHandler) {
	b.ReadOutput(scanner, b.handleJSONRPCResponse, handle)
}

// isJSONNull treats an explicit JSON null and an absent member as empty.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// parseJSONRPCError reads a native protocol error's code, message and data.
// It rejects an absent value, JSON null, and a value without both required fields.
func parseJSONRPCError(resp json.RawMessage) (*JSONRPCResponseError, bool) {
	if isJSONNull(resp) {
		return nil, false
	}
	var rpcErr struct {
		Code    *int            `json:"code"`
		Message *string         `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return nil, false
	}
	if rpcErr.Code == nil || rpcErr.Message == nil {
		return nil, false
	}
	responseErr := &JSONRPCResponseError{Code: *rpcErr.Code, Message: *rpcErr.Message}
	if !isJSONNull(rpcErr.Data) {
		responseErr.Data = rpcErr.Data
	}
	return responseErr, true
}

// JSONRPCResponseError is the error member of a JSON-RPC 2.0 response.
//
// Data is the optional `data` member, as the agent sent it. An agent can state
// the cause of a failure only there: the ACP TypeScript SDK answers an
// unexpected failure with message "Internal error" and the cause in
// `{"details": ...}` (Qwen Code), and Grok Build states its model failure in
// `{"message": ..., "http_status": ...}`. Error states Data too, so a failure
// note shows the cause.
type JSONRPCResponseError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *JSONRPCResponseError) Error() string {
	text := fmt.Sprintf("json-rpc error %d", e.Code)
	if e.Message != "" {
		text += ": " + e.Message
	}
	if data := e.dataText(); data != "" {
		text += ": " + data
	}
	return text
}

// dataText states Data as text: a JSON string as its trimmed value, and any
// other JSON value in its compact form, with its members in the agent's order.
// It is empty for an absent or null member and for a blank string.
func (e *JSONRPCResponseError) dataText() string {
	if isJSONNull(e.Data) {
		return ""
	}
	var text string
	if json.Unmarshal(e.Data, &text) == nil {
		return strings.TrimSpace(text)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, e.Data) != nil {
		return string(e.Data)
	}
	return compact.String()
}

func HasJSONRPCErrorCode(err error, codes ...int) bool {
	var responseErr *JSONRPCResponseError
	return errors.As(err, &responseErr) && slices.Contains(codes, responseErr.Code)
}

// jsonRPCRequestNotSentError records a failure before any request bytes reached the transport.
type jsonRPCRequestNotSentError struct {
	cause error
}

func (e *jsonRPCRequestNotSentError) Error() string { return e.cause.Error() }
func (e *jsonRPCRequestNotSentError) Unwrap() error { return e.cause }

func jsonRPCRequestWriteError(err error) error {
	err = fmt.Errorf("write request: %w", err)
	if errors.Is(err, agent.ErrDeliveryUncertain) {
		return err
	}
	return &jsonRPCRequestNotSentError{cause: err}
}

func ClassifyJSONRPCDeliveryError(operation string, err error) error {
	var responseErr *JSONRPCResponseError
	var notSent *jsonRPCRequestNotSentError
	if errors.As(err, &responseErr) || errors.As(err, &notSent) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%w: provider did not confirm delivery for %s: %w", agent.ErrDeliveryUncertain, operation, err)
}

// OutstandingControlForTest reports whether the provider still waits on the
// control request requestID.
func (b *JSONRPCProcess) OutstandingControlForTest(requestID string) bool {
	b.outstandingMu.Lock()
	defer b.outstandingMu.Unlock()
	_, ok := b.outstandingControls[requestID]
	return ok
}

// OutstandingControlCountForTest returns how many control requests the
// provider still waits on.
func (b *JSONRPCProcess) OutstandingControlCountForTest() int {
	b.outstandingMu.Lock()
	defer b.outstandingMu.Unlock()
	return len(b.outstandingControls)
}

// HandleJSONRPCResponseForTest delivers line to the request that waits for it,
// as the reader does.
func (b *JSONRPCProcess) HandleJSONRPCResponseForTest(line *ParsedLine) bool {
	return b.handleJSONRPCResponse(line)
}
