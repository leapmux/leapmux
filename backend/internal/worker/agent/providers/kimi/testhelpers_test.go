package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The fake kap-server the tests drive.
//
// It serves the REST routes and the event WebSocket the provider calls, with the
// envelope, the bearer check and the subscribe/ack exchange the real server
// uses. It keeps each session's settings, so profile writes and pushed state agree.
// A test replaces
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
	DataFor    func(fakeKapRequest) any
	// BeforeReply runs after the fake records the request and before it writes the reply.
	BeforeReply func()
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
	// Goal supplies the native subscription snapshot, or nil for no goal.
	Goal any
	// ReplayFrames are complete native frames sent when this session is subscribed.
	ReplayFrames []any
}

// fakeKapSubscribe is one subscribe frame the fake received.
type fakeKapSubscribe struct {
	SessionID  string            `json:"session_id"`
	Transcript map[string]string `json:"transcript"`
	IDs        []string          `json:"-"`
	Since      map[string]int64  `json:"transcript_since"`
	WireType   string            `json:"-"`
	Payload    json.RawMessage   `json:"-"`
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
	// usage is the GET /oauth/usage data. By default it is the answer of a
	// server with no Kimi Code account.
	usage any
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
	// beforeReplay pauses an accepted subscription before native state and replay.
	beforeReplay   func(fakeKapSubscribe)
	taskCommands   []fakeKimiTaskCommand
	taskCommandFor func(fakeKimiTaskCommand) (int, string)
	taskStateFor   func(fakeKimiTaskCommand) any
	taskOutputsFor func(fakeKimiTaskCommand) []any
	// onShutdown runs when POST /shutdown arrives.
	onShutdown func()
}

type fakeKimiTaskCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id"`
	TailBytes *int   `json:"tail_bytes,omitempty"`
}

type fakeKapConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
	// The fake's mutex protects subscriptions with its session state.
	subscriptions map[string]bool
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
		version:   "2.4.0",
		features:  []map[string]string{{"name": kimiFeatureGoal, "state": "active"}},
		models: []map[string]any{
			{
				"model": "kimi-k2", "display_name": "Kimi K2", "max_context_size": 262144,
				"capabilities": []string{"thinking", "image_in"}, "support_efforts": []string{"low", "medium", "high"},
			},
			{"model": "kimi-text", "display_name": "Kimi Text", "max_context_size": 131072, "capabilities": []string{}},
		},
		config: map[string]any{"default_model": "kimi-k2", "thinking": map[string]any{"enabled": true, "effort": "medium"}},
		usage:  map[string]any{"kind": "error", "message": `No token for "managed:kimi-code". Run /login to authenticate.`},
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

// setUsage replaces the GET /oauth/usage data.
func (f *fakeKap) setUsage(usage any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usage = usage
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

// update changes a stored session and publishes its native state to subscribers.
func (f *fakeKap) update(id string, change func(*fakeKapSession)) {
	f.mu.Lock()
	if session := f.sessions[id]; session != nil {
		change(session)
	}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.publishSessionState(ctx, id)
}

func (f *fakeKap) sessionStateFrames(id string) []any {
	session, exists := f.session(id)
	if !exists {
		return nil
	}
	status := "idle"
	if session.Busy {
		status = "running"
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	return []any{
		map[string]any{
			"type": contracts.KimiEventAgent, "session_id": id, "agent_id": kimiMainAgentID, "event_created_at": at,
			"status": status, "model": session.Model, "thinking": session.Thinking, "permission_mode": session.Permission,
			"plan_mode": session.PlanMode, "swarm_mode": session.SwarmMode, "tower_mode": false,
		},
		map[string]any{
			"type": contracts.KimiEventUsage, "session_id": id, "agent_id": kimiMainAgentID, "event_created_at": at,
			"context_tokens": 1000, "max_context_tokens": 262144,
		},
	}
}

func (f *fakeKap) publishSessionState(ctx context.Context, id string) {
	f.mu.Lock()
	var targets []*fakeKapConn
	for _, conn := range f.conns {
		if conn.subscriptions[id] {
			targets = append(targets, conn)
		}
	}
	f.mu.Unlock()
	for _, frame := range f.sessionStateFrames(id) {
		for _, conn := range targets {
			_ = conn.write(ctx, frame)
		}
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
		if scripted.BeforeReply != nil {
			scripted.BeforeReply()
		}
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
		data := scripted.Data
		if scripted.DataFor != nil {
			data = scripted.DataFor(request)
		}
		writeFakeKapEnvelope(w, status, scripted.Code, scripted.Msg, data)
		return
	}
	if request.Method == http.MethodPost {
		if strings.Contains(request.Path, "/approvals/") || strings.Contains(request.Path, "/questions/") {
			writeFakeKapEnvelope(w, http.StatusNotFound, 40400, "the control route does not exist", nil)
			return
		}
		if strings.Contains(request.Path, "/interactions/") {
			var reply struct {
				Kind      string          `json:"kind"`
				Decision  string          `json:"decision"`
				Answers   json.RawMessage `json:"answers"`
				Dismissed bool            `json:"dismissed"`
			}
			valid := json.Unmarshal(request.Body, &reply) == nil
			switch reply.Kind {
			case contracts.KimiInteractionKindApproval:
				valid = valid && (reply.Decision == contracts.KimiDecisionApproved || reply.Decision == contracts.KimiDecisionRejected || reply.Decision == contracts.KimiDecisionCancelled)
			case contracts.KimiInteractionKindQuestion:
				valid = valid && (reply.Dismissed != (len(reply.Answers) != 0))
			default:
				valid = false
			}
			if !valid {
				writeFakeKapEnvelope(w, http.StatusBadRequest, 40001, "the interaction body is invalid", nil)
				return
			}
		}
	}
	if request.Method == http.MethodGet && (strings.HasSuffix(request.Path, "/status") || strings.HasSuffix(request.Path, "/snapshot") || strings.HasSuffix(request.Path, "/tasks")) {
		writeFakeKapEnvelope(w, http.StatusNotFound, 40400, "the session route does not exist", nil)
		return
	}
	data := f.defaultReply(request)
	if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/profile") {
		id := strings.TrimSuffix(strings.TrimPrefix(request.Path, kimiRouteSessions+"/"), "/profile")
		f.publishSessionState(r.Context(), id)
	}
	writeFakeKapEnvelope(w, http.StatusOK, kimiCodeOK, "success", data)
}

// defaultReply supplies the native routes that a test does not script explicitly.
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
	case request.route() == "GET "+kimiRouteManagedUsage:
		return f.usage
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
	case request.Method == http.MethodGet && rest == "goal":
		return session.Goal
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
		var body struct {
			PromptID string `json:"prompt_id"`
		}
		_ = json.Unmarshal(request.Body, &body)
		if body.PromptID == "" {
			body.PromptID = fmt.Sprintf("prompt_%d", f.nextPrompt)
		}
		return map[string]any{"prompt_id": body.PromptID, "status": status}
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
	wrapped := &fakeKapConn{conn: conn, subscriptions: make(map[string]bool)}
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
		case "task.stop", "task.watch", "task.unwatch":
			var command fakeKimiTaskCommand
			if json.Unmarshal(data, &command) != nil {
				continue
			}
			f.mu.Lock()
			f.taskCommands = append(f.taskCommands, command)
			answer := f.taskCommandFor
			stateFor := f.taskStateFor
			outputsFor := f.taskOutputsFor
			f.mu.Unlock()
			code, msg := 0, "success"
			if answer != nil {
				code, msg = answer(command)
			}
			response := map[string]any{"type": "response", "request_id": command.RequestID, "code": code, "msg": msg}
			if stateFor != nil && command.Type == kimiFrameTaskWatch {
				response["task"] = stateFor(command)
			}
			_ = wrapped.write(ctx, response)
			if command.Type == kimiFrameTaskWatch && code == 0 && outputsFor != nil {
				for _, output := range outputsFor(command) {
					_ = wrapped.write(ctx, output)
				}
			}
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
			sub.IDs = []string{sub.SessionID}
			sub.WireType = frame.Type
			sub.Payload = append(json.RawMessage(nil), frame.Payload...)
			f.mu.Lock()
			f.subscribes = append(f.subscribes, sub)
			ackFor := f.ackFor
			beforeReplay := f.beforeReplay
			f.mu.Unlock()
			code, ack := f.defaultAck(sub)
			if ackFor != nil {
				code, ack = ackFor(sub)
			}
			if code == kimiCodeOK && slices.Contains(ack.Accepted, sub.SessionID) {
				f.mu.Lock()
				first := !wrapped.subscriptions[sub.SessionID]
				wrapped.subscriptions[sub.SessionID] = true
				f.mu.Unlock()
				if first && ackFor == nil {
					if beforeReplay != nil {
						beforeReplay(sub)
					}
					for _, state := range f.sessionStateFrames(sub.SessionID) {
						_ = wrapped.write(ctx, state)
					}
					if goal, enabled := f.initialGoalFrame(sub.SessionID); enabled {
						_ = wrapped.write(ctx, goal)
					}
					if session, exists := f.session(sub.SessionID); exists {
						for _, replay := range session.ReplayFrames {
							_ = wrapped.write(ctx, replay)
						}
					}
				}
			}
			reply := map[string]any{"type": kimiFrameAck, "id": frame.ID, "payload": ack}
			if code != kimiCodeOK {
				reply["code"] = code
				reply["msg"] = "refused"
			}
			_ = wrapped.write(ctx, reply)
		case kimiFrameUnsubscribe:
			var unsub struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(frame.Payload, &unsub)
			f.mu.Lock()
			f.unsubscribes = append(f.unsubscribes, []string{unsub.SessionID})
			delete(wrapped.subscriptions, unsub.SessionID)
			f.mu.Unlock()
		}
	}
}

func (f *fakeKap) initialGoalFrame(sessionID string) (any, bool) {
	f.mu.Lock()
	enabled := slices.ContainsFunc(f.features, func(feature map[string]string) bool {
		return feature["name"] == kimiFeatureGoal && feature["state"] == "active"
	})
	f.mu.Unlock()
	session, exists := f.session(sessionID)
	if !enabled || !exists {
		return nil, false
	}
	frame := map[string]any{"type": contracts.KimiEventGoal, "session_id": sessionID, "agent_id": kimiMainAgentID}
	if session.Goal != nil {
		goal, valid := session.Goal.(map[string]any)
		if !valid {
			panic("the fake Kimi goal must be a map")
		}
		native := map[string]any{"objective": goal["objective"], "status": goal["status"], "turns_used": 0, "tokens_used": 0, "wall_clock_ms": 0}
		for source, target := range map[string]string{"turnsUsed": "turns_used", "tokensUsed": "tokens_used", "wallClockMs": "wall_clock_ms"} {
			if value, present := goal[source]; present {
				native[target] = value
			}
		}
		frame["goal"] = native
	}
	return frame, true
}

func (f *fakeKap) defaultAck(sub fakeKapSubscribe) (int, kimiAck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ack := kimiAck{}
	for _, id := range sub.IDs {
		if f.sessions[id] == nil {
			ack.NotFound = append(ack.NotFound, id)
			continue
		}
		ack.Accepted = append(ack.Accepted, id)
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
	fake.mu.Lock()
	waitForGoal := fake.ackFor == nil && a.features[kimiFeatureGoal]
	fake.mu.Unlock()
	if waitForGoal {
		waitFor(t, func() bool {
			a.Mu.Lock()
			defer a.Mu.Unlock()
			return a.goalPushSeen && (len(sink.Goals()) > 0 || len(sink.GoalClearSnapshots()) > 0)
		}, "the initial native goal state is applied")
	}
	return &kimiTestRig{agent: a, fake: fake, sink: sink}
}

// newConnectedKimiAgent connects an agent to the server at url and opens no
// session. It returns the options with their test defaults filled in.
func newConnectedKimiAgent(t *testing.T, url string, opts agent.Options) (*Agent, *agenttest.ControlSink, agent.Options) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a, opts := newUnconnectedKimiAgent(t, opts, agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
	require.NoError(t, a.connect(a.Context(), url, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(a.closeStream)
	return a, sink, opts
}

// newUnconnectedKimiAgent installs services before the stream can use them.
func newUnconnectedKimiAgent(t *testing.T, opts agent.Options, services agent.ProviderServices) (*Agent, agent.Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
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
		sink:       services,
		workingDir: opts.WorkingDir,
		clock:      quartz.NewReal(),
	}
	return a, opts
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

// kimiEventFrame builds flat native interactions and retained diagnostic envelopes.
// Diagnostic envelopes repeat the payload type on the outer frame.
func kimiEventFrame(t *testing.T, sessionID string, payload map[string]any) []byte {
	t.Helper()
	if payload["type"] == contracts.KimiFrameInteraction {
		payload["session_id"] = sessionID
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		return data
	}

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
	a := &Agent{
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
	t.Cleanup(func() {
		cancel()
		a.closeStream()
	})
	return a
}

func adoptNativeKimiChild(t *testing.T, a *Agent, nativeID string) string {
	t.Helper()
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	raw, err := json.Marshal(map[string]any{"type": contracts.KimiEventTask, "session_id": sessionID,
		"agent_id": kimiMainAgentID, "task_id": "task-" + nativeID, "kind": "subagent", "status": "running", "detached": false, "child_agent_id": nativeID})
	require.NoError(t, err)
	a.HandleOutput(raw)
	child, linked := a.children.get(nativeID)
	require.True(t, linked)
	return child.childAgentID
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
