package ohmypi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// rpcResponse is a `{type:"response"}` frame. omp echoes the command's id and
// type, and states the outcome in `success`, `data` and `error`.
type rpcResponse struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Code    string          `json:"code"`
}

// commandError is a command that omp refused: a response with `success:false`.
// It keeps omp's own message, and the machine code that some refusals carry.
type commandError struct {
	Command string
	Message string
	Code    string
}

func (e *commandError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("omp %s failed", e.Command)
	}
	return fmt.Sprintf("omp %s failed: %s", e.Command, e.Message)
}

// readyFrame is the first frame omp writes in RPC mode: the protocol versions it
// supports and its frame limits.
type readyFrame struct {
	ProtocolVersion           int   `json:"protocolVersion"`
	SupportedProtocolVersions []int `json:"supportedProtocolVersions"`
	MaxFrameBytes             int   `json:"maxFrameBytes"`
	MaxReassembledFrameBytes  int   `json:"maxReassembledFrameBytes"`
}

// supports reports whether omp states that it speaks a protocol version.
func (r readyFrame) supports(version int) bool {
	for _, v := range r.SupportedProtocolVersions {
		if v == version {
			return true
		}
	}
	return false
}

// sendCommand writes one command and blocks until its response arrives, the
// process exits, or the timeout fires. A timeout of 0 waits without a limit.
//
// It returns the response's `data` on success, and a *commandError when omp
// refuses the command.
func (a *Agent) sendCommand(command string, payload map[string]any, timeout time.Duration) (json.RawMessage, error) {
	wait, err := a.beginCommand(command, payload)
	if err != nil {
		return nil, err
	}
	return wait(timeout)
}

// beginCommand writes one command and returns the waiter for its response's
// data. Call the waiter exactly once: it releases the response registration.
func (a *Agent) beginCommand(command string, payload map[string]any) (func(time.Duration) (json.RawMessage, error), error) {
	wait, err := a.beginCommandFrame(command, payload)
	if err != nil {
		return nil, err
	}
	return func(timeout time.Duration) (json.RawMessage, error) {
		frame, err := wait(timeout)
		if err != nil {
			return nil, err
		}
		return parseResponse(command, frame)
	}, nil
}

// beginCommandFrame writes one command and returns the waiter for its WHOLE
// response frame, success or refusal alike. A caller that persists omp's answer
// takes the frame omp sent rather than a copy rebuilt from its data. Call the
// waiter exactly once: it releases the response registration.
//
// The registration happens BEFORE the write, so a response that omp sends at once
// cannot arrive to an id nobody waits for.
func (a *Agent) beginCommandFrame(command string, payload map[string]any) (func(time.Duration) (json.RawMessage, error), error) {
	id := "leapmux-" + strconv.FormatInt(a.nextReqID.Add(1), 10)

	envelope := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		envelope[key] = value
	}
	envelope["id"] = id
	envelope["type"] = command

	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode omp %s: %w", command, err)
	}
	data = append(data, '\n')

	ch, release := a.Register(id)

	a.Mu.Lock()
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		release()
		return nil, fmt.Errorf("agent is stopped")
	}
	if err := a.WriteStdin(data); err != nil {
		release()
		return nil, fmt.Errorf("write omp %s: %w", command, err)
	}

	return func(timeout time.Duration) (json.RawMessage, error) {
		defer release()
		return a.AwaitResponse(ch, command, timeout)
	}, nil
}

// sendCommandDetached writes one command and hands its outcome to handle on a
// goroutine, so the caller does not wait for the response.
func (a *Agent) sendCommandDetached(command string, payload map[string]any, handle func(json.RawMessage, error)) error {
	wait, err := a.beginCommand(command, payload)
	if err != nil {
		return err
	}
	go func() {
		handle(wait(0))
	}()
	return nil
}

// parseResponse reads a response frame into its data, or into the error omp
// states.
func parseResponse(command string, raw json.RawMessage) (json.RawMessage, error) {
	var response rpcResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode omp %s response: %w", command, err)
	}
	if !response.Success {
		return nil, &commandError{Command: command, Message: response.Error, Code: response.Code}
	}
	return response.Data, nil
}

// interceptFrame is the read loop's interceptor. It consumes a response that a
// caller waits for, and every rpc_chunk frame. It returns false for every other
// frame, which then reaches handleFrame.
//
// A reassembled frame takes the SAME route as a whole one: a split response
// reaches its caller, and every other split frame reaches the dispatcher.
func (a *Agent) interceptFrame(line *providerkit.ParsedLine) bool {
	switch line.Type {
	case contracts.OhMyPiEventRpcChunk:
		a.handleChunk(line.Raw)
		return true
	case contracts.OhMyPiEventResponse:
		return a.deliverResponse(line)
	default:
		return false
	}
}

// deliverResponse hands a response to the caller that waits for it. It returns
// false for a response nobody waits for, which the dispatcher then reads.
func (a *Agent) deliverResponse(line *providerkit.ParsedLine) bool {
	id := line.IDString()
	if id == "" {
		return false
	}
	return a.Deliver(id, line.Raw)
}

// handleChunk feeds one rpc_chunk frame to the assembler, and dispatches the frame
// that its last chunk completes.
func (a *Agent) handleChunk(raw []byte) {
	var chunk rpcChunkFrame
	if err := json.Unmarshal(raw, &chunk); err != nil {
		slog.Warn("omp rpc_chunk decode failed", "agent_id", a.AgentID(), "error", err)
		a.chunks.reset()
		return
	}
	frame, err := a.chunks.add(chunk)
	if err != nil {
		// A frame with a missing slice is lost. If it was a response, its caller
		// times out; if it was an event, the transcript misses it. Neither can be
		// recovered, so the log line is what explains the gap.
		slog.Warn("omp dropped a split frame", "agent_id", a.AgentID(), "error", err)
	}
	if frame == nil {
		return
	}
	line := providerkit.ParseLine(frame)
	if !a.interceptFrame(line) {
		a.handleFrame(line)
	}
}

// HandleOutput parses one JSONL line and dispatches it through the same route the
// read loop uses. Tests and out-of-band feeds call it.
func (a *Agent) HandleOutput(content []byte) {
	line := providerkit.ParseLine(content)
	if !a.interceptFrame(line) {
		a.handleFrame(line)
	}
}
