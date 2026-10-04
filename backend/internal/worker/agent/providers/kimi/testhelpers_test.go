package kimi

import (
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
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The fake kap-server the tests drive.
//
// It serves the REST routes and the event WebSocket the provider calls, with the
// envelope, the bearer check and the subscribe/ack exchange the real server
// uses. It keeps the settings of each session it created, so a profile write and
// the status read after it agree, as they do on the real server. A test replaces
// any reply by its route, and pushes events down the socket.
//
// The same fake serves the tests in this process (httptest) and the helper
// process a fake `kimi` binary starts (start_test.go).

// fakeKapToken is the bearer token every fake server issues.
const fakeKapToken = "test-token_0123456789"

// fakeKapRequest is one REST request the fake received.
type fakeKapRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// route returns the request's "METHOD path" key.
func (r fakeKapRequest) route() string { return r.Method + " " + r.Path }

// fakeKapReply is one scripted reply.
type fakeKapReply struct {
	// HTTPStatus is the reply's status. Zero is 200.
	HTTPStatus int
	Code       int
	Msg        string
	Data       any
	// Drop, when set, closes the connection with no reply: a transport failure
	// after the request left, whose outcome the client cannot know.
	Drop bool
}

// fakeKapSession is the state of one session the fake created or stores.
type fakeKapSession struct {
	Model      string
	Thinking   string
	Permission string
	PlanMode   bool
	SwarmMode  bool
	// Busy is the status's `busy`: some agent of the session runs, or a
	// background task does. It decides the status of a prompt the session takes.
	Busy bool
	// Goal is the GET .../goal reply, or nil for a session with no goal.
	Goal any
	// InFlightTurn is the id of the main agent's running turn, which GET
	// .../snapshot states, or nil when the main agent runs none.
	InFlightTurn *int64
	// PendingApprovals and PendingQuestions are the snapshot's unresolved
	// interactions, in the shape toWireApproval and toWireQuestion give them:
	// the event payload with no `type`, `agentId` or `sessionId`.
	PendingApprovals []map[string]any
	PendingQuestions []map[string]any
	// Subagents is the snapshot's subagent roster, in the shape of
	// snapshotSubagentSchema.
	Subagents []map[string]any
	// Tasks is the GET .../tasks reply, in the shape of taskSchema.
	Tasks []map[string]any
}

// fakeKapEpoch is the epoch of every session's event journal in the fake.
const fakeKapEpoch = "ep_1"

// fakeKapSubscribe is one subscribe frame the fake received.
type fakeKapSubscribe struct {
	IDs     []string              `json:"session_ids"`
	Cursors map[string]kimiCursor `json:"cursors"`
}

type fakeKap struct {
	mu        sync.Mutex
	requests  []fakeKapRequest
	overrides map[string][]fakeKapReply
	sessions  map[string]*fakeKapSession
	// nextSession and nextPrompt number the sessions and the prompts the fake
	// creates, each from 1.
	nextSession int
	nextPrompt  int
	// models and config are the GET /models items and the GET /config data.
	models []map[string]any
	config map[string]any
	// features is the GET /meta feature list.
	features []map[string]string
	version  string

	conns []*fakeKapConn
	// socketDials counts every event-socket request, refused ones included.
	socketDials int
	// refuseSockets counts the next event-socket requests that the fake refuses,
	// as a server that restarts does.
	refuseSockets int
	subscribes    []fakeKapSubscribe
	unsubscribes  [][]string
	pongs         []string
	// ackFor answers one subscribe. nil accepts every session the fake knows and
	// lists every other one under not_found.
	ackFor func(sub fakeKapSubscribe) (code int, ack kimiAck)
	// onShutdown runs when POST /shutdown arrives.
	onShutdown func()
}

type fakeKapConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *fakeKapConn) write(ctx context.Context, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func newFakeKapState() *fakeKap {
	return &fakeKap{
		overrides: make(map[string][]fakeKapReply),
		sessions:  make(map[string]*fakeKapSession),
		version:   "2.0.2",
		features:  []map[string]string{{"name": kimiFeatureGoal, "state": "active"}},
		models: []map[string]any{
			{
				"model": "kimi-k2", "display_name": "Kimi K2", "max_context_size": 262144,
				"capabilities": []string{"thinking", "image_in"}, "support_efforts": []string{"low", "medium", "high"},
			},
			{"model": "kimi-text", "display_name": "Kimi Text", "max_context_size": 131072, "capabilities": []string{}},
		},
		config: map[string]any{"default_model": "kimi-k2", "thinking": map[string]any{"enabled": true, "effort": "medium"}},
	}
}

// newFakeKap starts a fake server in this process.
func newFakeKap(t *testing.T) (*fakeKap, *httptest.Server) {
	t.Helper()
	fake := newFakeKapState()
	server := httptest.NewServer(fake)
	t.Cleanup(func() {
		fake.closeConnections()
		server.Close()
	})
	return fake, server
}

// reply scripts the replies of one route, in order. The last one repeats.
func (f *fakeKap) reply(route string, replies ...fakeKapReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides[route] = replies
}

// setAckFor replaces the subscribe answer. See fakeKap.ackFor.
func (f *fakeKap) setAckFor(ackFor func(sub fakeKapSubscribe) (code int, ack kimiAck)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackFor = ackFor
}

// setFeatures replaces the GET /meta feature list.
func (f *fakeKap) setFeatures(features ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.features = nil
	for _, name := range features {
		f.features = append(f.features, map[string]string{"name": name, "state": "active"})
	}
}

// store adds a stored session the fake knows but did not create.
func (f *fakeKap) store(id string, session fakeKapSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = &session
}

// session returns a copy of one session's state.
func (f *fakeKap) session(id string) (fakeKapSession, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessions[id]
	if session == nil {
		return fakeKapSession{}, false
	}
	return *session, true
}

// setBusy sets whether a session runs a turn, which decides the status of a
// prompt it takes.
func (f *fakeKap) setBusy(id string, busy bool) {
	f.update(id, func(session *fakeKapSession) { session.Busy = busy })
}

// update changes the state of one session the fake knows, as the server's own
// work does while no socket watches it.
func (f *fakeKap) update(id string, change func(*fakeKapSession)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if session := f.sessions[id]; session != nil {
		change(session)
	}
}

// requestsTo returns every request of one route, in order.
func (f *fakeKap) requestsTo(route string) []fakeKapRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeKapRequest
	for _, request := range f.requests {
		if request.route() == route {
			out = append(out, request)
		}
	}
	return out
}

// routes returns the route of every request, in order.
func (f *fakeKap) routes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		out = append(out, request.route())
	}
	return out
}

func (f *fakeKap) subscribeFrames() []fakeKapSubscribe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeKapSubscribe(nil), f.subscribes...)
}

func (f *fakeKap) unsubscribeFrames() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.unsubscribes...)
}

func (f *fakeKap) pongNonces() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pongs...)
}

func (f *fakeKap) connectionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// refuseNextSockets makes the fake refuse the next n event-socket requests.
func (f *fakeKap) refuseNextSockets(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuseSockets = n
}

// socketDialCount returns how many event-socket requests the fake received.
func (f *fakeKap) socketDialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.socketDials
}

// push writes one frame to the newest event socket.
func (f *fakeKap) push(t *testing.T, frame any) {
	t.Helper()
	f.mu.Lock()
	var conn *fakeKapConn
	if len(f.conns) > 0 {
		conn = f.conns[len(f.conns)-1]
	}
	f.mu.Unlock()
	require.NotNil(t, conn, "no event socket is open")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, conn.write(ctx, frame))
}

// closeConnections closes every event socket, as a server restart or a network
// failure does.
func (f *fakeKap) closeConnections() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.conn.CloseNow()
	}
}

func (f *fakeKap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeKapToken {
		writeFakeKapEnvelope(w, http.StatusUnauthorized, 40100, "unauthorized", nil)
		return
	}
	if r.URL.Path == kimiRouteWS {
		f.serveWebSocket(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	request := fakeKapRequest{Method: r.Method, Path: r.URL.Path}
	if len(body) > 0 {
		request.Body = append(json.RawMessage(nil), body...)
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	var scripted *fakeKapReply
	if queue := f.overrides[request.route()]; len(queue) > 0 {
		reply := queue[0]
		if len(queue) > 1 {
			f.overrides[request.route()] = queue[1:]
		}
		scripted = &reply
	}
	f.mu.Unlock()

	if scripted != nil {
		if scripted.Drop {
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		status := scripted.HTTPStatus
		if status == 0 {
			status = http.StatusOK
		}
		writeFakeKapEnvelope(w, status, scripted.Code, scripted.Msg, scripted.Data)
		return
	}
	data := f.defaultReply(request)
	writeFakeKapEnvelope(w, http.StatusOK, kimiCodeOK, "success", data)
}

// defaultReply answers a request no test scripted, as the real server does.
func (f *fakeKap) defaultReply(request fakeKapRequest) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case request.route() == "GET "+kimiRouteMeta:
		return map[string]any{"server_version": f.version, "features": f.features}
	case request.route() == "GET "+kimiRouteModels:
		return map[string]any{"items": f.models}
	case request.route() == "GET "+kimiRouteConfig:
		return f.config
	case request.route() == "POST "+kimiRouteSessions:
		f.nextSession++
		id := fmt.Sprintf("session_%d", f.nextSession)
		f.sessions[id] = &fakeKapSession{Permission: "manual"}
		return map[string]any{"id": id}
	case request.route() == "POST "+kimiRouteShutdown:
		if f.onShutdown != nil {
			go f.onShutdown()
		}
		return map[string]any{}
	}
	sessionID, rest, ok := strings.Cut(strings.TrimPrefix(request.Path, kimiRouteSessions+"/"), "/")
	if !strings.HasPrefix(request.Path, kimiRouteSessions+"/") {
		return map[string]any{}
	}
	if !ok {
		sessionID, _, _ = strings.Cut(sessionID, ":")
	}
	session := f.sessions[sessionID]
	if session == nil {
		return map[string]any{}
	}
	switch {
	case request.Method == http.MethodGet && rest == "status":
		return map[string]any{
			"busy": session.Busy, "model": session.Model, "thinking_level": session.Thinking,
			"permission": session.Permission, "plan_mode": session.PlanMode, "swarm_mode": session.SwarmMode,
			"context_tokens": 1000, "max_context_tokens": 262144,
		}
	case request.Method == http.MethodGet && rest == "goal":
		return session.Goal
	case request.Method == http.MethodGet && rest == "snapshot":
		var inFlight any
		if session.InFlightTurn != nil {
			inFlight = map[string]any{"turn_id": *session.InFlightTurn, "assistant_text": "", "thinking_text": "", "running_tools": []any{}}
		}
		approvals, questions, subagents := session.PendingApprovals, session.PendingQuestions, session.Subagents
		if approvals == nil {
			approvals = []map[string]any{}
		}
		if questions == nil {
			questions = []map[string]any{}
		}
		if subagents == nil {
			subagents = []map[string]any{}
		}
		return map[string]any{
			"as_of_seq": 0, "epoch": fakeKapEpoch, "session": map[string]any{"id": sessionID},
			"messages":       map[string]any{"items": []any{}, "has_more": false},
			"in_flight_turn": inFlight, "pending_approvals": approvals, "pending_questions": questions,
			"subagents": subagents,
		}
	case request.Method == http.MethodGet && rest == "tasks":
		tasks := session.Tasks
		if tasks == nil {
			tasks = []map[string]any{}
		}
		return map[string]any{"items": tasks}
	case request.Method == http.MethodPost && rest == "profile":
		var body struct {
			Config map[string]any `json:"agent_config"`
		}
		_ = json.Unmarshal(request.Body, &body)
		if model, ok := body.Config[kimiConfigModel].(string); ok {
			session.Model = model
		}
		if thinking, ok := body.Config[kimiConfigThinking].(string); ok {
			session.Thinking = thinking
		}
		if permission, ok := body.Config[kimiConfigPermissionMode].(string); ok {
			session.Permission = permission
		}
		if plan, ok := body.Config[kimiConfigPlanMode].(bool); ok {
			session.PlanMode = plan
		}
		if swarm, ok := body.Config[kimiConfigSwarmMode].(bool); ok {
			session.SwarmMode = swarm
		}
		return map[string]any{}
	case request.Method == http.MethodPost && rest == "prompts":
		f.nextPrompt++
		status := "running"
		if session.Busy {
			status = kimiPromptQueued
		}
		return map[string]any{"prompt_id": fmt.Sprintf("prompt_%d", f.nextPrompt), "status": status}
	}
	return map[string]any{}
}

func writeFakeKapEnvelope(w http.ResponseWriter, status, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data, "request_id": "req"})
}

func (f *fakeKap) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.socketDials++
	refuse := f.refuseSockets > 0
	if refuse {
		f.refuseSockets--
	}
	f.mu.Unlock()
	if refuse {
		writeFakeKapEnvelope(w, http.StatusServiceUnavailable, 50300, "the server restarts", nil)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	wrapped := &fakeKapConn{conn: conn}
	f.mu.Lock()
	f.conns = append(f.conns, wrapped)
	f.mu.Unlock()
	ctx := r.Context()
	_ = wrapped.write(ctx, map[string]any{"type": kimiFrameServerHello, "payload": map[string]any{"heartbeat_ms": 10000}})
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch frame.Type {
		case kimiFramePong:
			var pong struct {
				Nonce string `json:"nonce"`
			}
			_ = json.Unmarshal(frame.Payload, &pong)
			f.mu.Lock()
			f.pongs = append(f.pongs, pong.Nonce)
			f.mu.Unlock()
		case kimiFrameSubscribe:
			var sub fakeKapSubscribe
			_ = json.Unmarshal(frame.Payload, &sub)
			f.mu.Lock()
			f.subscribes = append(f.subscribes, sub)
			ackFor := f.ackFor
			f.mu.Unlock()
			code, ack := f.defaultAck(sub)
			if ackFor != nil {
				code, ack = ackFor(sub)
			}
			reply := map[string]any{"type": kimiFrameAck, "id": frame.ID, "payload": ack}
			if code != kimiCodeOK {
				reply["code"] = code
				reply["msg"] = "refused"
			}
			_ = wrapped.write(ctx, reply)
		case kimiFrameUnsubscribe:
			var unsub struct {
				IDs []string `json:"session_ids"`
			}
			_ = json.Unmarshal(frame.Payload, &unsub)
			f.mu.Lock()
			f.unsubscribes = append(f.unsubscribes, unsub.IDs)
			f.mu.Unlock()
		}
	}
}

func (f *fakeKap) defaultAck(sub fakeKapSubscribe) (int, kimiAck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ack := kimiAck{Cursors: map[string]kimiCursor{}}
	for _, id := range sub.IDs {
		if f.sessions[id] == nil {
			ack.NotFound = append(ack.NotFound, id)
			continue
		}
		ack.Accepted = append(ack.Accepted, id)
		// The real server states the cursor of every session it accepts, which is
		// what a reconnect replays from.
		ack.Cursors[id] = kimiCursor{Epoch: fakeKapEpoch}
	}
	return kimiCodeOK, ack
}

// --- the agent under test ---

// kimiTestRig is one agent connected to a fake server.
type kimiTestRig struct {
	agent *Agent
	fake  *fakeKap
	sink  *agenttest.ControlSink
}

// newKimiTestRig connects an agent with no process behind it to a new fake
// server and opens a session with opts.
//
// No process is started: the tests drive the server's REST routes and events,
// which is everything the provider does after the ready line. start_test.go
// covers the launch itself.
func newKimiTestRig(t *testing.T, opts agent.Options) *kimiTestRig {
	t.Helper()
	fake, server := newFakeKap(t)
	return connectKimiTestRig(t, fake, server.URL, opts)
}

func connectKimiTestRig(t *testing.T, fake *fakeKap, url string, opts agent.Options) *kimiTestRig {
	t.Helper()
	a, sink, opts := newConnectedKimiAgent(t, url, opts)
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	return &kimiTestRig{agent: a, fake: fake, sink: sink}
}

// newConnectedKimiAgent connects an agent to the server at url and opens no
// session. It returns the options with their test defaults filled in.
func newConnectedKimiAgent(t *testing.T, url string, opts agent.Options) (*Agent, *agenttest.ControlSink, agent.Options) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if opts.AgentID == "" {
		opts.AgentID = "test-agent"
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = "/work/project"
	}
	if opts.APITimeout == 0 {
		opts.APITimeout = 30 * time.Second
	}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: opts.AgentID, ProviderName: "kimi", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(io.Discard), APITimeout: opts.APITimeout,
		}),
		sink:       agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		workingDir: opts.WorkingDir,
		clock:      quartz.NewReal(),
	}
	require.NoError(t, a.connect(ctx, url, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(a.closeStream)
	return a, sink, opts
}

// sessionID returns the agent's current session.
func (r *kimiTestRig) sessionID() string {
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	return r.agent.sessionID
}

// feed dispatches one event of the current session through HandleOutput, as the
// stream's dispatcher would.
func (r *kimiTestRig) feed(t *testing.T, payload map[string]any) {
	t.Helper()
	r.agent.HandleOutput(kimiEventFrame(t, r.sessionID(), payload))
}

// kimiEventFrame renders one event frame the way the server sends it: the
// payload's own `type` repeated on the frame.
func kimiEventFrame(t *testing.T, sessionID string, payload map[string]any) []byte {
	t.Helper()
	if _, ok := payload["agentId"]; !ok {
		payload["agentId"] = kimiMainAgentID
	}
	frame := map[string]any{"type": payload["type"], "session_id": sessionID, "seq": 1, "payload": payload}
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	return data
}

// newOfflineKimiAgent builds an agent with no server at all, for a handler that
// sends no request.
func newOfflineKimiAgent(t *testing.T, sink agent.ServiceFacets) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "kimi", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(io.Discard),
		}),
		sink:       agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		workingDir: "/work/project",
		clock:      quartz.NewReal(),
		sessionID:  "session_1",
		settings:   kimiSettings{model: "kimi-k2", effort: agent.EffortAuto, permission: "manual"},
	}
}

// waitFor polls cond until it holds. The deadline is generous and never
// asserted: a condition that never holds fails the test, however long that took.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 30*time.Second, 2*time.Millisecond, msg)
}

// options builds an option map.
func options(pairs ...string) optionmap.Map {
	out := optionmap.Map{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}
