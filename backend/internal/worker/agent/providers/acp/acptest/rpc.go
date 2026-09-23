package acptest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

// acpTestPeer is the part of an ACP agent that the helpers below drive. The
// helper attaches a fake process whose stdin it reads, and it answers each
// request through Deliver.
type acpTestPeer interface {
	AttachPeerForTest(ctx context.Context, cancel func(), stdin io.WriteCloser, agentID, sessionID string)
	Deliver(id int64, raw json.RawMessage) bool
}

// NewAgentForRPC constructs an ACP agent of type T for tests and starts a
// fake peer that echoes `{}` for every request. construct returns a
// zero-value agent; peer returns the agent's embedded base, which the helper
// attaches in place, so no lock is copied.
func NewAgentForRPC[T any, P acpTestPeer](
	t *testing.T,
	construct func() *T,
	peer func(*T) P,
) (*T, func() []agenttest.RecordedRequest) {
	return NewAgentForRPCWithResponder(t, construct, peer, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
}

// NewAgentForRPCWithResponder is like NewAgentForRPC but the caller
// supplies the response body for each inbound method.
func NewAgentForRPCWithResponder[T any, P acpTestPeer](
	t *testing.T,
	construct func() *T,
	peer func(*T) P,
	respond func(method string) agenttest.RPCReply,
) (*T, func() []agenttest.RecordedRequest) {
	return NewAgentForRPCWithRequestResponder(t, construct, peer, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		return respond(req.Method)
	})
}

// NewAgentForRPCWithRequestResponder is like NewAgentForRPCWithResponder but the
// responder sees the full request (method + params), so a test can vary its reply by an
// RPC's arguments -- e.g. return different configOptions for a session/set_config_option
// that writes the model vs. one that writes the reasoning-effort axis.
func NewAgentForRPCWithRequestResponder[T any, P acpTestPeer](
	t *testing.T,
	construct func() *T,
	peer func(*T) P,
	respond func(req agenttest.RecordedRequest) agenttest.RPCReply,
) (*T, func() []agenttest.RecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := construct()
	base := peer(agent)
	base.AttachPeerForTest(ctx, cancel, writePipe, "test-agent", "session-1")

	var (
		mu       sync.Mutex
		requests []agenttest.RecordedRequest
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
			recorded := agenttest.RecordedRequest{Method: req.Method, Params: req.Params, Raw: scanner.Text()}
			mu.Lock()
			requests = append(requests, recorded)
			mu.Unlock()
			body := agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			if respond != nil {
				body = respond(recorded)
			}
			base.Deliver(req.ID, agenttest.JSONRPCResponse(req.ID, body))
		}
	}()

	t.Cleanup(func() {
		cancel()
		// Close the write end first so the scanner's pending blocking
		// Read returns EOF. On Windows, closing the read end while a
		// Read is in flight deadlocks on the FD refcount (the Close
		// waits for the in-flight Read, which never returns).
		_ = writePipe.Close()
		_ = readPipe.Close()
	})

	return agent, func() []agenttest.RecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]agenttest.RecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}
