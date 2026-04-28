package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordedRequest captures a JSON-RPC request written to an ACP agent's stdin
// in tests.
type recordedRequest struct {
	Method string
	Params map[string]interface{}
}

// newACPAgentForRPC constructs an ACP agent of type T for tests and starts a
// fake peer that echoes `{}` for every request. construct returns a
// zero-value agent; accessBase returns a pointer to its embedded acpBase so
// the helper can populate shared fields in-place (avoiding copylocks
// violations) and so the peer can look up pendingReqs.
func newACPAgentForRPC[T any](
	t *testing.T,
	construct func() *T,
	accessBase func(*T) *acpBase,
) (*T, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t, construct, accessBase, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})
}

// newACPAgentForRPCWithResponder is like newACPAgentForRPC but the caller
// supplies the response body for each inbound method.
func newACPAgentForRPCWithResponder[T any](
	t *testing.T,
	construct func() *T,
	accessBase func(*T) *acpBase,
	respond func(method string) json.RawMessage,
) (*T, func() []recordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := construct()
	ab := accessBase(agent)
	ab.agentID = "test-agent"
	ab.stdin = writePipe
	ab.ctx = ctx
	ab.cancel = cancel
	ab.processDone = make(chan struct{})
	ab.stderrDone = make(chan struct{})
	ab.sessionID = "session-1"
	close(ab.stderrDone)

	var (
		mu       sync.Mutex
		requests []recordedRequest
	)
	go func() {
		scanner := bufio.NewScanner(readPipe)
		for scanner.Scan() {
			var req struct {
				ID     int64                  `json:"id"`
				Method string                 `json:"method"`
				Params map[string]interface{} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			mu.Lock()
			requests = append(requests, recordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			body := json.RawMessage(`{}`)
			if respond != nil {
				body = respond(req.Method)
			}
			ab.deliver(req.ID, body)
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]recordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}
