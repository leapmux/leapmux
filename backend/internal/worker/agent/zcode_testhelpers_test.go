package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/require"
)

// zcodeRecordedStdin is the stdin a test agent writes its requests to.
//
// A real pipe is not needed and a nil one would panic: an event handler can start an
// RPC of its own (a finished turn re-reads the session's usage), so the frames have
// to land somewhere. They are kept, so a test can assert on what was SENT.
type zcodeRecordedStdin struct {
	mu     sync.Mutex
	frames [][]byte
}

func (w *zcodeRecordedStdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, append([]byte(nil), p...))
	return len(p), nil
}

func (w *zcodeRecordedStdin) Close() error { return nil }

// Frames returns every line written to stdin, in order.
func (w *zcodeRecordedStdin) Frames() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.frames...)
}

// Requests decodes the frames as request envelopes, so a test can assert which
// method was called with which params.
func (w *zcodeRecordedStdin) Requests(t *testing.T) []zcodeSentRequest {
	t.Helper()
	out := []zcodeSentRequest{}
	for _, frame := range w.Frames() {
		var req zcodeSentRequest
		if json.Unmarshal(frame, &req) != nil {
			continue
		}
		out = append(out, req)
	}
	return out
}

// zcodeSentRequest is a frame LeapMux wrote, in either direction: a request of our
// own (method + params) or a reply to a server request (id + result).
type zcodeSentRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *zcodeError     `json:"error"`
}

// newZCodeTestAgent builds a zcodeAgent with no process behind it.
//
// Every handler under test is fed through HandleOutput or called directly, so no
// child process is needed -- and none is started, which keeps the suite free of the
// ZCode installation the launch resolver looks for. The context is live so a code
// path that consults a.ctx does not read a nil channel.
func newZCodeTestAgent(t *testing.T, sink OutputSink) *zcodeAgent {
	t.Helper()
	return newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})
}

func newZCodeTestAgentWithStdin(t *testing.T, sink OutputSink, stdin *zcodeRecordedStdin) *zcodeAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// A handler may start an RPC whose reply never comes (nothing answers this
	// stdin). Cancelling at cleanup unblocks that goroutine instead of leaving it
	// parked on the API timeout for the rest of the run.
	t.Cleanup(cancel)
	a := &zcodeAgent{
		processBase: processBase{
			agentID:     "test-agent",
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stdin:       stdin,
		},
		sink:            sink,
		workingDir:      "/tmp/zcode-workspace",
		workspace:       zcodeWorkspaceFor("/tmp/zcode-workspace"),
		sessionID:       "sess-1",
		mode:            contracts.ZCodeDefaultMode,
		toolCalls:       map[string]*zcodeToolCall{},
		pendingControls: map[string]json.RawMessage{},
	}
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)
	return a
}

// zcodeEventLine renders one session/event notification exactly as the app-server
// sends it: the envelope flat inside `params`, with no wrapper object.
func zcodeEventLine(t *testing.T, seq int64, eventType string, payload string) []byte {
	t.Helper()
	body := map[string]any{
		"eventId":   "evt",
		"sessionId": "sess-1",
		"seq":       seq,
		"type":      eventType,
	}
	if payload != "" {
		body["payload"] = json.RawMessage(payload)
	}
	params, err := json.Marshal(body)
	require.NoError(t, err)
	line, err := json.Marshal(map[string]any{
		"method": ZCodeNotifySessionEvent,
		"params": json.RawMessage(params),
	})
	require.NoError(t, err)
	return line
}

// zcodeStateLine renders one top-level state.updated notification.
func zcodeStateLine(t *testing.T, scope, reason, patch string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"method": ZCodeNotifyStateUpdated,
		"params": map[string]any{
			"scope":     scope,
			"sessionId": "sess-1",
			"revision":  7,
			"reason":    reason,
			"patch":     json.RawMessage(patch),
		},
	})
	require.NoError(t, err)
	return line
}

// zcodeReplyLine is one app-server response, keyed by the request id LeapMux minted.
func zcodeReplyLine(t *testing.T, id int64, result json.RawMessage) []byte {
	t.Helper()
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	line, err := json.Marshal(map[string]any{"id": id, "result": result})
	require.NoError(t, err)
	return line
}

// zcodeErrorReplyLine is one app-server error response, keyed by the request id
// LeapMux minted.
func zcodeErrorReplyLine(t *testing.T, id int64, code int, message string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id":    id,
		"error": map[string]any{"code": code, "message": message},
	})
	require.NoError(t, err)
	return line
}

// zcodeTestRPCTimeout limits an RPC that the test EXPECTS an answer for. It is a
// backstop only: a reply that never arrives fails the test in seconds, instead of
// parking the run until the package timeout.
const zcodeTestRPCTimeout = 5 * time.Second

// answerZCodeRequest replies to the next request for method with result.
//
// The reply goes out from a separate goroutine, because the RPC blocks the test
// goroutine until the reply lands. Each call waits for its OWN method, so a test
// prepares every answer of a multi-step exchange before it starts the exchange.
func answerZCodeRequest(t *testing.T, a *zcodeAgent, stdin *zcodeRecordedStdin, method, result string) {
	t.Helper()
	go func() {
		req := waitZCodeRequest(t, stdin, method)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(result)))
	}()
}

// refuseZCodeRequest answers the next request for method with an app-server error.
func refuseZCodeRequest(t *testing.T, a *zcodeAgent, stdin *zcodeRecordedStdin, method string, code int, message string) {
	t.Helper()
	go func() {
		req := waitZCodeRequest(t, stdin, method)
		a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), code, message))
	}()
}

// waitZCodeRequest waits until stdin holds a request for method (or any request
// when method is empty). The setters block until a reply arrives, so the test
// answers on this goroutine after this returns.
func waitZCodeRequest(t *testing.T, stdin *zcodeRecordedStdin, method string) zcodeSentRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, req := range stdin.Requests(t) {
			if method == "" || req.Method == method {
				return req
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a %s request", method)
	return zcodeSentRequest{}
}

func zcodeSentRequestID(t *testing.T, req zcodeSentRequest) int64 {
	t.Helper()
	var id int64
	require.NoError(t, json.Unmarshal(req.ID, &id))
	return id
}

// zcodeTestCatalog builds a catalog from an inline configuration, so a test states
// exactly the providers and models it needs.
func zcodeTestCatalog(t *testing.T, configJSON string) zcodeCatalog {
	t.Helper()
	var cfg zcodeConfigFile
	require.NoError(t, json.Unmarshal([]byte(configJSON), &cfg))
	catalog, _ := buildZCodeCatalog(cfg)
	return catalog
}

// zcodeTwoProviderConfig is the configuration most tests use: one enabled provider
// with a reasoning-capable text model and an image-capable one, and one disabled
// provider that still holds a key.
const zcodeTwoProviderConfig = `{
  "provider": {
    "builtin:zai": {
      "name": "Z.ai",
      "kind": "openai-compatible",
      "source": "builtin",
      "enabled": true,
      "options": {"apiKey": "zai-key", "baseURL": "https://api.z.ai/v1"},
      "models": {
        "GLM-5.3": {
          "name": "GLM-5.3",
          "reasoning": {"enabled": true, "variants": ["low", "high", "max"], "defaultVariant": "high"},
          "limit": {"context": 200000, "output": 32000},
          "modalities": {"input": ["text"], "output": ["text"]}
        },
        "GLM-5.3-Flash": {
          "name": "GLM-5.3 Flash",
          "limit": {"context": 128000, "output": 16000},
          "modalities": {"input": ["text", "image"], "output": ["text"]}
        }
      }
    },
    "acme": {
      "name": "Acme",
      "kind": "anthropic",
      "source": "custom",
      "options": {"apiKey": "acme-key"},
      "models": {"acme-1": {"name": "Acme One"}}
    }
  }
}`
