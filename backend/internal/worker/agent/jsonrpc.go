package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// jsonrpcBase shares request correlation across JSON-RPC providers.
type jsonrpcBase struct {
	processBase
	responseCorrelator[int64]
	nextReqID atomic.Int64
	// A nil encoder selects newline framing. Native Copilot selects Content-Length framing.
	frameMessage func([]byte) []byte

	// outstandingMu guards outstandingControls.
	outstandingMu sync.Mutex
	// outstandingControls holds every control request the provider still waits on,
	// keyed by the LeapMux request id. publishControlRequest is the one writer that
	// adds an entry, and withdrawControlRequest the one that removes it, so the
	// provider-side record and the browser-side card cannot move apart.
	outstandingControls map[string]outstandingControlRequest
}

func (b *jsonrpcBase) writeJSONRPCMessage(data []byte) error {
	if b.frameMessage != nil {
		return b.writeStdin(b.frameMessage(data))
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return b.writeStdin(data)
}

// SendRawInput keeps provider JSON unchanged inside the selected transport frame, then
// drops the control request that the frame answers.
//
// The drop follows the write. A write that fails leaves the request waiting, so the
// reader can answer it again. ErrDeliveryUncertain drops it: the bytes may have reached
// the provider, and a second answer to a request the provider already retired is the
// worse outcome.
func (b *jsonrpcBase) SendRawInput(data []byte) error {
	err := b.writeRawFrame(data)
	if err == nil || errors.Is(err, ErrDeliveryUncertain) {
		b.forgetOutstandingControl(data)
	}
	return err
}

func (b *jsonrpcBase) writeRawFrame(data []byte) error {
	if b.frameMessage == nil {
		return b.processBase.SendRawInput(data)
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
		// A provider that reports an error can still spell the unused result member as
		// an explicit null. That states no result, so the response does not carry both.
		// Rejecting it hid the error code from every caller that classifies one, and the
		// caller reported an unconfirmed delivery in place of the provider's own reason.
		if !isJSONNull(response.Result) {
			return nil, errors.New("json-rpc response contains both a result and an error")
		}
		code, message, ok := parseJSONRPCError(response.Error)
		if !ok {
			return nil, errors.New("json-rpc response contains an invalid error")
		}
		return nil, &jsonRPCResponseError{Code: code, Message: message}
	}
	if len(response.Result) == 0 {
		return nil, errors.New("json-rpc response has no result")
	}
	return response.Result, nil
}

func (b *jsonrpcBase) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := b.nextReqID.Add(1)

	ch, release := b.register(reqID)
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

	resp, err := b.awaitResponse(ch, method, timeout)
	if err != nil {
		return nil, err
	}
	return decodeJSONRPCResponse(resp)
}

func (b *jsonrpcBase) sendDetachedRequest(method string, params json.RawMessage, handle func(json.RawMessage, error)) error {
	reqID := b.nextReqID.Add(1)
	ch, release := b.register(reqID)
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
		resp, err := b.awaitResponse(ch, method, 0)
		if err == nil {
			resp, err = decodeJSONRPCResponse(resp)
		}
		handle(resp, err)
	}()
	return nil
}

func (b *jsonrpcBase) sendNotification(method string, params json.RawMessage) error {
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

func (b *jsonrpcBase) sendResponse(id json.RawMessage, result any) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (b *jsonrpcBase) sendErrorResponse(id json.RawMessage, code int, message string) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

// jsonrpcErrMethodNotFound is the JSON-RPC code for a method the receiver
// implements no handler for.
const jsonrpcErrMethodNotFound = -32601

// refuseUnsupportedRequest answers an inbound REQUEST this worker implements no
// handler for, with -32601.
//
// A notification carries no id and needs no answer, so this returns for one. A
// request does: the runtime waits for a response, and without one it waits for its
// own timeout while the turn appears to hang. Every JSON-RPC dispatcher's default
// branch owes that answer, and each one spelled it again -- with two constants for
// the same code and two message strings -- until Codex, the fourth, never spelled
// it at all.
//
// It answers on its OWN goroutine, and that is the load-bearing part. Every caller
// runs inside the read loop, so the reply's stdin write happens while nothing
// drains the child's stdout. A child that is not reading its stdin then blocks the
// write, the unread stdout backs up against it, and neither side moves again. One
// caller made it worse still by holding its own output mutex across the write,
// which left Stop unable to take the lock it needs to close stdin and end the
// stall. `zcodeReplyFrame` states the same rule for the same reason.
//
// The raw identifier travels back unchanged, so an id above 2^53 keeps its exact
// value rather than rounding through a float.
func (b *jsonrpcBase) refuseUnsupportedRequest(line *parsedLine) {
	if line.Method == "" || !line.HasID() {
		return
	}
	id, method := line.ID, line.Method
	go func() {
		if err := b.sendErrorResponse(id, jsonrpcErrMethodNotFound, "Method not supported: "+method); err != nil {
			slog.Warn("Answer an unsupported request", "agent_id", b.agentID, "method", method, "error", err)
		}
	}()
}

func (b *jsonrpcBase) writeJSONRPCResponse(resp jsonrpcResponseMessage) error {
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
func (b *jsonrpcBase) handleJSONRPCResponse(line *parsedLine) bool {
	if !line.HasID() || line.Method != "" {
		return false
	}

	reqID, ok := line.IDInt64()
	if !ok {
		return false
	}

	return b.deliver(reqID, line.Raw)
}

// readOutputLoop reads framed JSON messages and dispatches requests and events.
func (b *jsonrpcBase) readOutputLoop(scanner *bufio.Scanner, handle outputHandler) {
	b.readOutput(scanner, b.handleJSONRPCResponse, handle)
}

// isJSONNull reports an explicit JSON null, which states the same thing as an absent
// member. An absent member reports true also.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// parseJSONRPCError extracts the code and message from a JSON-RPC error
// response. Returns ok=false if resp is empty, null, or not an error object.
func parseJSONRPCError(resp json.RawMessage) (code int, message string, ok bool) {
	if isJSONNull(resp) {
		return 0, "", false
	}
	var rpcErr struct {
		Code    *int    `json:"code"`
		Message *string `json:"message"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return 0, "", false
	}
	if rpcErr.Code == nil || rpcErr.Message == nil {
		return 0, "", false
	}
	return *rpcErr.Code, *rpcErr.Message, true
}

type jsonRPCResponseError struct {
	Code    int
	Message string
}

func (e *jsonRPCResponseError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("json-rpc error %d", e.Code)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

func hasJSONRPCErrorCode(err error, codes ...int) bool {
	var responseErr *jsonRPCResponseError
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
	if errors.Is(err, ErrDeliveryUncertain) {
		return err
	}
	return &jsonRPCRequestNotSentError{cause: err}
}

func classifyJSONRPCDeliveryError(operation string, err error) error {
	var responseErr *jsonRPCResponseError
	var notSent *jsonRPCRequestNotSentError
	if errors.As(err, &responseErr) || errors.As(err, &notSent) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%w: provider did not confirm delivery for %s: %w", ErrDeliveryUncertain, operation, err)
}
