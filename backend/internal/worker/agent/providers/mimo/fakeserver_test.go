package mimo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer stands in for `mimo serve`. It serves every REST route the
// provider calls with the shape MiMo 0.1.14 answers with, records each request,
// and streams the events a test sends it.
//
// A test changes one route with handle. The unit tests run it in-process with
// httptest; the start tests run the same server in a helper process behind a
// fake `mimo` on PATH (start_unix_test.go).
type fakeServer struct {
	// password is the Basic-auth password the server takes, with the user
	// serverUser. Empty takes any request, as MiMo does with no password set.
	password string

	mu       sync.Mutex
	requests []fakeRequest
	routes   map[string]fakeRoute
	// streams holds each open event stream. emit writes to every one of them.
	streams map[*fakeStream]struct{}
	// streamOpened receives one value each time an event stream opens.
	streamOpened chan struct{}
	// dropStreams closes to end every open stream, as a server restart would.
	dropStreams chan struct{}
}

// fakeRequest is one request the server received.
type fakeRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// fakeStream is one open event stream.
type fakeStream struct {
	events chan []byte
	// done closes when the stream's handler returns, so an emit that races the
	// close does not block.
	done chan struct{}
}

// fakeRoute answers one route. body is the request body, already read.
type fakeRoute func(w http.ResponseWriter, r *http.Request, body []byte)

func newFakeServer(password string) *fakeServer {
	return &fakeServer{
		password:     password,
		routes:       map[string]fakeRoute{},
		streams:      map[*fakeStream]struct{}{},
		streamOpened: make(chan struct{}, 16),
		dropStreams:  make(chan struct{}),
	}
}

// startFakeServer runs a fake server in-process for the life of the test.
func startFakeServer(t *testing.T) (*fakeServer, string) {
	t.Helper()
	server := newFakeServer("test-secret")
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.endStreams()
		httpServer.Close()
	})
	return server, httpServer.URL
}

// handle replaces the answer to one route, given as "METHOD /path".
func (s *fakeServer) handle(route string, answer fakeRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = answer
}

// respond makes one route answer with status and a JSON body.
func (s *fakeServer) respond(route string, status int, body string) {
	s.handle(route, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, status, body)
	})
}

// requestsTo returns the requests of one route, given as "METHOD /path", in
// arrival order.
func (s *fakeServer) requestsTo(route string) []fakeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fakeRequest
	for _, request := range s.requests {
		if request.Method+" "+request.Path == route {
			out = append(out, request)
		}
	}
	return out
}

// allRequests returns every request in arrival order.
func (s *fakeServer) allRequests() []fakeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeRequest(nil), s.requests...)
}

// emit sends one event to every open stream.
func (s *fakeServer) emit(event string) {
	s.mu.Lock()
	streams := make([]*fakeStream, 0, len(s.streams))
	for stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		select {
		case stream.events <- []byte(event):
		case <-stream.done:
		}
	}
}

// endStreams ends every open stream. A stream that opens afterwards stays open.
func (s *fakeServer) endStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.dropStreams)
	s.dropStreams = make(chan struct{})
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.password != "" {
		user, password, ok := r.BasicAuth()
		if !ok || user != serverUser || password != s.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	route := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.requests = append(s.requests, fakeRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
	})
	answer := s.routes[route]
	s.mu.Unlock()
	if answer != nil {
		answer(w, r, body)
		return
	}
	s.serveDefault(w, r)
}

// serveDefault answers a route as MiMo does for a new, idle session.
func (s *fakeServer) serveDefault(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == routeEvents:
		s.serveEvents(w, r)
	case r.Method == http.MethodGet && path == routeHealth:
		writeJSON(w, http.StatusOK, `{"healthy":true,"version":"0.1.14"}`)
	case r.Method == http.MethodGet && path == routeConfigProviders:
		writeJSON(w, http.StatusOK, fakeProviders)
	case r.Method == http.MethodGet && path == routeConfig:
		writeJSON(w, http.StatusOK, `{"model":"mock/beta"}`)
	case r.Method == http.MethodGet && path == routeAgents:
		writeJSON(w, http.StatusOK, fakeAgents)
	case r.Method == http.MethodPost && path == routeSessions:
		writeJSON(w, http.StatusOK, `{"id":"ses_created","directory":"/work","title":"New session"}`)
	case r.Method == http.MethodGet && path == routeSessionStatus:
		writeJSON(w, http.StatusOK, `{}`)
	case r.Method == http.MethodGet && (path == routePermissions || path == routeQuestions || path == routeBashInteractive):
		writeJSON(w, http.StatusOK, `[]`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/prompt_async"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/message"):
		writeJSON(w, http.StatusOK, `[]`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, routeSessions+"/") && strings.Count(path, "/") == 2:
		id := strings.TrimPrefix(path, routeSessions+"/")
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"id":%q,"directory":"/work","title":"Stored session"}`, id))
	case r.Method == http.MethodPost:
		// abort, summarize, command, the switches and every reply route answer
		// true.
		writeJSON(w, http.StatusOK, `true`)
	default:
		writeJSON(w, http.StatusNotFound, `{"name":"NotFoundError","data":{"message":"no such route"}}`)
	}
}

func (s *fakeServer) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	stream := &fakeStream{events: make(chan []byte), done: make(chan struct{})}
	s.mu.Lock()
	s.streams[stream] = struct{}{}
	drop := s.dropStreams
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, stream)
		s.mu.Unlock()
		close(stream.done)
	}()
	writeEvent(w, `{"type":"server.connected","properties":{}}`)
	flusher.Flush()
	select {
	case s.streamOpened <- struct{}{}:
	default:
		// No test waits for this many connections, and the handler must not block.
	}
	for {
		select {
		case event := <-stream.events:
			writeEvent(w, string(event))
			flusher.Flush()
		case <-drop:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeEvent(w io.Writer, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// fakeProviders is GET /config/providers for a configuration with two
// providers. `mock/alpha` has reasoning variants, `mock/beta` has none, and
// `other/old` is deprecated.
const fakeProviders = `{
  "providers": [
    {"id":"mock","name":"Mock","models":{
      "beta":{"id":"beta","name":"Beta","limit":{"context":64000},"variants":{}},
      "alpha":{"id":"alpha","name":"Alpha","limit":{"context":128000},
        "variants":{"high":{"reasoningEffort":"high"},"low":{"reasoningEffort":"low"},"default":{}}}
    }},
    {"id":"other","name":"Other","models":{
      "old":{"id":"old","name":"Old","status":"deprecated","limit":{"context":8000}}
    }}
  ],
  "default": {"mock":"alpha","other":"old"}
}`

// fakeAgents is GET /agent as MiMo 0.1.14 lists its agents.
const fakeAgents = `[
  {"name":"build","description":"The default agent.","mode":"primary"},
  {"name":"plan","description":"Plan mode.","mode":"primary"},
  {"name":"general","description":"A general subagent.","mode":"subagent"},
  {"name":"compaction","mode":"primary","hidden":true},
  {"name":"max","description":"Maximum effort.","mode":"all"}
]`

// decodeBody decodes a recorded request body into a generic map.
func decodeBody(t *testing.T, request fakeRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode %s %s body %q: %v", request.Method, request.Path, request.Body, err)
	}
	return body
}
