package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// SendRawInput keeps provider JSON unchanged inside the selected transport frame.
func (b *jsonrpcBase) SendRawInput(data []byte) error {
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
	if len(response.Error) > 0 && string(response.Error) != "null" {
		if len(response.Result) > 0 {
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
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if err := b.writeJSONRPCMessage(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
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
		return fmt.Errorf("marshal request: %w", err)
	}
	if err := b.writeJSONRPCMessage(data); err != nil {
		release()
		return fmt.Errorf("write request: %w", err)
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

// parseJSONRPCError extracts the code and message from a JSON-RPC error
// response. Returns ok=false if resp is empty, null, or not an error object.
func parseJSONRPCError(resp json.RawMessage) (code int, message string, ok bool) {
	if len(resp) == 0 || string(resp) == "null" {
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

func classifyJSONRPCDeliveryError(operation string, err error) error {
	var responseErr *jsonRPCResponseError
	if errors.As(err, &responseErr) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%w: provider did not confirm delivery for %s: %v", ErrDeliveryUncertain, operation, err)
}
