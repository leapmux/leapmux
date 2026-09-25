package providerkit

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
	// outstandingControls holds every control request the provider still waits on,
	// keyed by the LeapMux request id. PublishControlRequest adds each entry. The
	// answer in SendRawInput and each withdrawal remove it, and SendRawInput puts
	// it back only for a write that certainly delivered nothing. So the
	// provider-side record and the browser-side card cannot move apart.
	outstandingControls map[string]outstandingControlRequest
	// withdrawGeneration counts the WITHDRAWALS, so an absent record tells
	// PublishControlRequest which of the two removers took it.
	//
	// The ANSWER path removes a record too: every control response reaches the
	// provider through SendRawInput, which calls takeOutstandingControl. A bare
	// presence test therefore reads "the reader answered while I published" and "a
	// stop stole this record" as the same state, and the publisher then cancelled a
	// card the reader had just decided. Only a withdrawal raises this.
	withdrawGeneration uint64
}

// frameJSONRPCMessage wraps one message in the transport's frame.
//
// ONE framing, shared by the waiting write and the detached one. Spelled twice, the
// detached copy omitted the newline that a line transport needs, and every reply it
// sent sat in the pipe as an unterminated line the peer's scanner never returned.
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

// SendRawInput keeps provider JSON unchanged inside the selected transport frame, and
// drops the control request that the frame answers.
//
// The drop comes before the write, because a withdrawal can run during the write
// (see takeOutstandingControl). A write that certainly fails puts the request back,
// so the reader can answer it again and a later withdrawal still reaches it.
// ErrDeliveryUncertain keeps it dropped: the bytes may have reached the provider, and
// a second answer to a request the provider already retired is the worse outcome.
func (b *JSONRPCProcess) SendRawInput(data []byte) error {
	restore := b.takeOutstandingControl(data)
	err := b.writeRawFrame(data)
	if err != nil && !errors.Is(err, agent.ErrDeliveryUncertain) {
		restore()
	}
	return err
}

func (b *JSONRPCProcess) writeRawFrame(data []byte) error {
	if b.FrameMessage == nil {
		return b.Process.SendRawInput(data)
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
		return nil, &JSONRPCResponseError{Code: code, Message: message}
	}
	if len(response.Result) == 0 {
		return nil, errors.New("json-rpc response has no result")
	}
	return response.Result, nil
}

func (b *JSONRPCProcess) SendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := b.nextReqID.Add(1)

	ch, release := b.Register(reqID)
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
	ch, release := b.Register(reqID)
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

// jsonrpcErrMethodNotFound is the JSON-RPC code for a method the receiver
// implements no handler for.
const jsonrpcErrMethodNotFound = -32601

// RefuseUnsupportedRequest answers an inbound REQUEST this worker implements no
// handler for, with -32601.
//
// A notification carries no id and needs no answer, so this returns for one. A
// request does: the runtime waits for a response, and without one it waits for its
// own timeout while the turn appears to hang. Every JSON-RPC dispatcher's default
// branch owes that answer, and each one spelled it again -- with two constants for
// the same code and two message strings -- until Codex, the fourth, never spelled
// it at all.
//
// It does NOT wait for the write, and that is the load-bearing part. Every caller
// runs inside the read loop, so the reply's stdin write happens while nothing
// drains the child's stdout. A child that is not reading its stdin then blocks the
// write, the unread stdout backs up against it, and neither side moves again. One
// caller made it worse still by holding its own output mutex across the write,
// which left Stop unable to take the lock it needs to close stdin and end the
// stall. `interceptResponse` in zcode/rpc.go states the same rule for the same
// reason.
//
// One QUEUE behind one writer, not a goroutine for each reply. A goroutine each
// made the cost of an unresponsive child unlimited -- one 8 KiB stack per
// unanswered frame -- and left the replies in no particular order relative to each
// other.
//
// The raw identifier travels back unchanged, so an id above 2^53 keeps its exact
// value rather than rounding through a float.
func (b *JSONRPCProcess) RefuseUnsupportedRequest(line *ParsedLine) {
	if line.Method == "" || !line.HasID() {
		return
	}
	b.SendErrorResponseDetached(line.ID, jsonrpcErrMethodNotFound,
		"Method not supported: "+line.Method, "refuse "+line.Method)
}

// SendResponseDetached queues a reply WITHOUT waiting for its write, for a caller
// on the goroutine that drains the child's stdout.
//
// That goroutine must not wait for a stdin write. A child that is not reading its
// stdin blocks the write, the unread stdout backs up against it, and neither side
// moves again -- which is the deadlock the reply exists to prevent. The frames stay
// in ORDER behind one writer, so a reply sent this way still reaches the child
// before anything queued after it: the "answers go FIRST, then the cancel"
// invariant that Base.Interrupt and codex.Agent.Interrupt both rely on holds
// whether the sender waits or not.
//
// `describe` labels the frame in the writer's failure log, because no caller is
// left to report the error.
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

type JSONRPCResponseError struct {
	Code    int
	Message string
}

func (e *JSONRPCResponseError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("json-rpc error %d", e.Code)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
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
