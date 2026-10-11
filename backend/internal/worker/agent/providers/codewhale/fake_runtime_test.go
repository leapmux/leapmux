package codewhale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// fakeRuntime stands in for `codewhale app-server --http` in a test: a REST mux
// that records every request, and an event stream the test feeds.
//
// A route answers 404 until the test states its reply, except the two that
// every runtime serves: /health and /v1/runtime/info.
type fakeRuntime struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	handlers map[string]http.HandlerFunc
	// streams holds the open event-stream connections, oldest first.
	streams []*fakeStream
	// streamOpened receives one value for each stream connection.
	streamOpened chan string
}

// recordedRequest is one request the runtime received.
type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Body     []byte
	Auth     string
}

// fakeStream is one open event-stream connection.
type fakeStream struct {
	events chan []byte
	done   chan struct{}
	once   sync.Once
}

func (s *fakeStream) close() {
	s.once.Do(func() { close(s.done) })
}

func newFakeRuntime(t *testing.T) *fakeRuntime {
	t.Helper()
	rt := &fakeRuntime{
		handlers:     make(map[string]http.HandlerFunc),
		streamOpened: make(chan string, 16),
	}
	rt.respondJSON(http.MethodGet, routeHealth, http.StatusOK, map[string]any{"status": "ok"})
	rt.respondJSON(http.MethodGet, routeRuntimeInfo, http.StatusOK, map[string]any{"codewhale_version": "0.9.13"})
	rt.server = httptest.NewServer(http.HandlerFunc(rt.serve))
	t.Cleanup(func() {
		rt.closeStreams()
		rt.server.Close()
	})
	return rt
}

// URL is the runtime's base address.
func (rt *fakeRuntime) URL() string { return rt.server.URL }

// handle states the reply of one route. path excludes the query.
func (rt *fakeRuntime) handle(method, path string, handler http.HandlerFunc) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.handlers[method+" "+path] = handler
}

// handlerFor returns the reply that one route states now, so a test can wrap
// it.
func (rt *fakeRuntime) handlerFor(method, path string) http.HandlerFunc {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.handlers[method+" "+path]
}

// respondJSON states a fixed JSON reply for one route.
func (rt *fakeRuntime) respondJSON(method, path string, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	rt.handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(encoded)
	})
}

// writeFakeJSON writes one JSON reply.
func writeFakeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// respondStatus states a bare status reply, with the runtime's error body.
func (rt *fakeRuntime) respondStatus(method, path string, status int, message string) {
	rt.respondJSON(method, path, status, map[string]any{"error": map[string]any{"message": message, "status": status}})
}

// requestsTo returns every recorded request to one route.
func (rt *fakeRuntime) requestsTo(method, path string) []recordedRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var matched []recordedRequest
	for _, request := range rt.requests {
		if request.Method == method && request.Path == path {
			matched = append(matched, request)
		}
	}
	return matched
}

// lastBody decodes the body of the last request to one route.
func (rt *fakeRuntime) lastBody(t *testing.T, method, path string) map[string]any {
	t.Helper()
	requests := rt.requestsTo(method, path)
	require.NotEmpty(t, requests, "no request reached %s %s", method, path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(requests[len(requests)-1].Body, &body))
	return body
}

func (rt *fakeRuntime) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	// The handler reads the body too, so it gets the bytes this read took.
	r.Body = io.NopCloser(bytes.NewReader(body))
	rt.mu.Lock()
	rt.requests = append(rt.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Body: body, Auth: r.Header.Get("Authorization")})
	handler := rt.handlers[r.Method+" "+r.URL.Path]
	rt.mu.Unlock()
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, threadRouteEvents) && handler == nil {
		rt.serveStream(w, r)
		return
	}
	if handler == nil {
		http.Error(w, `{"error":{"message":"not found","status":404}}`, http.StatusNotFound)
		return
	}
	handler(w, r)
}

// serveStream holds one event-stream connection open and writes each pushed
// event as one server-sent event.
func (rt *fakeRuntime) serveStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	stream := &fakeStream{events: make(chan []byte, 64), done: make(chan struct{})}
	rt.mu.Lock()
	rt.streams = append(rt.streams, stream)
	rt.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	rt.streamOpened <- r.URL.RawQuery
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stream.done:
			return
		case event := <-stream.events:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", event)
			flusher.Flush()
		}
	}
}

// push writes one event to the newest stream connection.
func (rt *fakeRuntime) push(t *testing.T, event []byte) {
	t.Helper()
	rt.mu.Lock()
	var stream *fakeStream
	if len(rt.streams) > 0 {
		stream = rt.streams[len(rt.streams)-1]
	}
	rt.mu.Unlock()
	require.NotNil(t, stream, "no event stream is open")
	stream.events <- event
}

// dropStreams ends every open stream connection, as a runtime restart would.
func (rt *fakeRuntime) dropStreams() {
	rt.mu.Lock()
	streams := rt.streams
	rt.streams = nil
	rt.mu.Unlock()
	for _, stream := range streams {
		stream.close()
	}
}

func (rt *fakeRuntime) closeStreams() { rt.dropStreams() }

// awaitStream waits for the next stream connection and returns its query.
func (rt *fakeRuntime) awaitStream(t *testing.T) string {
	t.Helper()
	select {
	case query := <-rt.streamOpened:
		return query
	case <-time.After(30 * time.Second):
		t.Fatal("the agent opened no event stream")
		return ""
	}
}

// testThreadID and testTurnID identify the thread and the turn every test
// frame addresses.
const (
	testThreadID = "thr_60dc67d7"
	testTurnID   = "turn_dc67891a"
	testToken    = "test-token"
)

// newTestAgent builds an agent that talks to rt, with no process behind it.
// The agent owns the thread testThreadID.
func newTestAgent(t *testing.T, rt *fakeRuntime) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	return newTestAgentWithClock(t, rt, quartz.NewReal())
}

func newTestAgentWithClock(t *testing.T, rt *fakeRuntime, clock quartz.Clock) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	return newTestAgentWith(t, rt, testAgentOptions{clock: clock})
}

// testAgentOptions states what a test agent differs in.
type testAgentOptions struct {
	// clock is the agent's clock. Nil is the real clock.
	clock quartz.Clock
	// apiTimeout limits each request. Zero is 10 seconds.
	apiTimeout time.Duration
	// services wraps the services that the agent starts with. Nil keeps them.
	services func(agent.ProviderServices) agent.ProviderServices
}

func newTestAgentWith(t *testing.T, rt *fakeRuntime, options testAgentOptions) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	clock := options.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	apiTimeout := options.apiTimeout
	if apiTimeout == 0 {
		apiTimeout = 10 * time.Second
	}
	sink := &agenttest.ControlSink{}
	services := agent.NewProviderServices(sink)
	if options.services != nil {
		services = options.services(services)
	}
	a := newAgent(agent.Options{AgentID: "cw-1", WorkingDir: t.TempDir()}, services, clock)
	ctx, cancelContext := context.WithCancel(context.Background())
	// The process "exits" when its context ends, as the runtime does when the
	// worker cancels it, so a Stop does not wait for a process that never ran.
	processDone := make(chan struct{})
	var exitOnce sync.Once
	cancel := func() {
		cancelContext()
		exitOnce.Do(func() { close(processDone) })
	}
	t.Cleanup(cancel)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:      "cw-1",
		ProviderName: codewhaleProviderName,
		Stdin:        &agenttest.Stdin{},
		Ctx:          ctx,
		Cancel:       cancel,
		APITimeout:   apiTimeout,
		ProcessDone:  processDone,
	})
	a.stopProcess = cancel
	a.children = newCodewhaleChildren(ctx)
	t.Cleanup(a.children.stopAll)
	if rt != nil {
		endpoint, err := providerkit.NewHTTPEndpoint(rt.URL(), providerkit.BearerAuth(testToken))
		require.NoError(t, err)
		a.endpoint = endpoint
	}
	a.threadID = testThreadID
	return a, sink
}

// runtimeEvent builds one runtime event envelope, as the stream delivers it.
func runtimeEvent(seq uint64, name, turnID, itemID string, payload any) []byte {
	encoded, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"seq":            seq,
		"event":          name,
		"kind":           name,
		"thread_id":      testThreadID,
		"turn_id":        nullable(turnID),
		"item_id":        nullable(itemID),
		"timestamp":      "2026-09-23T18:18:36.078701+00:00",
		"payload":        payload,
	})
	if err != nil {
		panic(err)
	}
	return encoded
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// toolStartEvent is the item.started of one tool call.
func toolStartEvent(seq uint64, itemID, callID, name string, input map[string]any) []byte {
	encodedInput, _ := json.Marshal(input)
	return runtimeEvent(seq, "item.started", testTurnID, itemID, map[string]any{
		"item": map[string]any{
			"id": itemID, "turn_id": testTurnID, "kind": "tool_call", "status": "in_progress",
			"summary": name + " started", "detail": string(encodedInput),
			"metadata": map[string]any{"tool_use_id": callID, "tool_name": name, "tool_input": string(encodedInput)},
		},
		"tool": map[string]any{"id": callID, "name": name, "input": input},
	})
}

// toolEndEvent is the final event of one tool call.
func toolEndEvent(seq uint64, name, itemID, callID, tool, detail string, input map[string]any, metadata map[string]any) []byte {
	encodedInput, _ := json.Marshal(input)
	md := map[string]any{"tool_use_id": callID, "tool_name": tool, "tool_input": string(encodedInput)}
	for key, value := range metadata {
		md[key] = value
	}
	status := strings.TrimPrefix(name, "item.")
	return runtimeEvent(seq, name, testTurnID, itemID, map[string]any{
		"item": map[string]any{
			"id": itemID, "turn_id": testTurnID, "kind": "tool_call", "status": status,
			"summary": tool + ": " + detail, "detail": detail, "metadata": md,
		},
	})
}

// itemEvent is the final event of a non-tool item.
func itemEvent(seq uint64, name, itemID, kind, detail string, metadata map[string]any) []byte {
	item := map[string]any{"id": itemID, "turn_id": testTurnID, "kind": kind, "status": "completed", "summary": detail, "detail": detail}
	if metadata != nil {
		item["metadata"] = metadata
	}
	return runtimeEvent(seq, name, testTurnID, itemID, map[string]any{"item": item})
}

// turnStartedEvent and turnCompletedEvent bracket one turn.
func turnStartedEvent(seq uint64, turnID string) []byte {
	return runtimeEvent(seq, "turn.started", turnID, "", map[string]any{"turn": map[string]any{"id": turnID, "status": "in_progress"}})
}

func turnCompletedEvent(seq uint64, turnID, status string) []byte {
	return runtimeEvent(seq, "turn.completed", turnID, "", map[string]any{"turn": map[string]any{"id": turnID, "status": status, "duration_ms": 1200}})
}

// mustJSON marshals a test value.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// decodeJSON unmarshals a persisted row for an assertion.
func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
