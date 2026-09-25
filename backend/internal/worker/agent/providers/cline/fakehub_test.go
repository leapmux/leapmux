package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The fake Cline hub the tests drive.
//
// It serves the WebSocket and the shutdown route of a Cline hub daemon, with the
// frame kinds, the token subprotocol, the per-session subscription and the
// durable replay the real daemon has. It answers each command from a default
// handler that behaves as the daemon does, and a test replaces the handler of
// any command. A test pushes events down the socket with emit.
//
// The same fake serves the tests in this process (httptest) and the helper
// process that a fake `cline` program starts (start_unix_test.go).

// fakeHubToken is the token every fake hub takes.
const fakeHubToken = "fake-hub-token"

// fakeSessionSeq numbers the sessions that every fake hub of the test binary
// creates.
var fakeSessionSeq atomic.Int64

// fakeCommand is one command the fake received.
type fakeCommand struct {
	Command   string          `json:"command"`
	RequestID string          `json:"requestId"`
	ClientID  string          `json:"clientId"`
	SessionID string          `json:"sessionId"`
	Payload   json.RawMessage `json:"payload"`
}

// field decodes one field of the payload into out.
func (c fakeCommand) field(key string, out any) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &payload) != nil {
		return false
	}
	raw, ok := payload[key]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// str returns one string field of the payload.
func (c fakeCommand) str(key string) string {
	var value string
	c.field(key, &value)
	return value
}

// fakeReply is the answer to one command. A nil Payload answers `{}`. Hold
// sends no reply now: the test replies later with hub.reply.
type fakeReply struct {
	Payload any
	Code    string
	Message string
	Hold    bool
}

// fakeSubscribe is one stream.subscribe frame the fake received.
type fakeSubscribe struct {
	SessionID     string
	SinceSequence *int64
}

// fakeEvent is one event of the fake's durable log.
type fakeEvent struct {
	sequence  int64
	sessionID string
	envelope  json.RawMessage
}

// fakeHub is the fake daemon.
type fakeHub struct {
	mu       sync.Mutex
	commands []fakeCommand
	// arrived signals each command, for a test that waits for one.
	arrived  chan fakeCommand
	handlers map[string]func(fakeCommand) fakeReply
	// conn is the current connection, and subscriptions its subscribed
	// sessions.
	conn          *fakeConn
	subscriptions map[string]bool
	subscribes    []fakeSubscribe
	unsubscribes  []string
	log           []fakeEvent
	sequence      int64
	// messages and compaction are the stored conversation and compaction state
	// of each session.
	messages   map[string]json.RawMessage
	compaction map[string]json.RawMessage
	// records are the session rows that session.get answers with, where a test
	// states one. A stored session with no row reads as a completed one.
	records map[string]map[string]any
	// sessions is the session.list reply.
	sessions  []map[string]any
	shutdowns int
	// onShutdown runs when POST /shutdown arrives.
	onShutdown func()
	// badShutdownTokens counts the shutdown requests with a wrong token.
	badShutdownTokens int
	// trustsLocalOrigin opens a connection with no token when its Origin is
	// local, as Cline 3.0.64 does when it listens on a host that it counts as
	// local. tokenlessUpgrades counts those connections.
	trustsLocalOrigin bool
	tokenlessUpgrades int
	// statusPID and statusHubID are what GET /status states. A zero value states
	// the process that serves the fake, which in the helper process of the
	// start tests is the daemon itself, and fakeHubID.
	statusPID   int
	statusHubID string
	// routes lists the authenticated HTTP requests in the order they arrived.
	routes []string
	// refuseUpgrades refuses every WebSocket upgrade, as a daemon that stopped
	// serving does, so a test holds a client's reconnect back.
	refuseUpgrades bool
}

// fakeHubID is the hub id of every fake hub.
const fakeHubID = "hub_fake"

// fakeConn is one WebSocket connection, whose writes the fake serializes.
type fakeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *fakeConn) write(frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.writeRaw(data)
}

// writeRaw sends data as one text frame, as it is. A test sends a frame that no
// daemon would send with it.
func (c *fakeConn) writeRaw(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func newFakeHubState() *fakeHub {
	return &fakeHub{
		arrived:       make(chan fakeCommand, 1024),
		handlers:      make(map[string]func(fakeCommand) fakeReply),
		subscriptions: make(map[string]bool),
		messages:      make(map[string]json.RawMessage),
		compaction:    make(map[string]json.RawMessage),
	}
}

// handle replaces the handler of one command.
func (f *fakeHub) handle(command string, handler func(fakeCommand) fakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[command] = handler
}

// storeRecord states the row of one session: its status, and the process that
// holds it, where pid is not 0.
func (f *fakeHub) storeRecord(sessionID, status string, pid int) {
	record := map[string]any{"sessionId": sessionID, "status": status}
	if pid != 0 {
		record["metadata"] = map[string]any{"pid": pid}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = make(map[string]map[string]any)
	}
	f.records[sessionID] = record
}

// store sets the stored conversation of a session.
func (f *fakeHub) store(sessionID string, messages any) {
	data, err := json.Marshal(messages)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[sessionID] = data
}

// storeCompaction sets the compaction state of a session.
func (f *fakeHub) storeCompaction(sessionID string, state any) {
	data, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compaction[sessionID] = data
}

// setSessions sets the session.list reply.
func (f *fakeHub) setSessions(sessions ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = sessions
}

// ServeHTTP serves the WebSocket, the status route and the shutdown route.
func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case hubPathname:
		f.serveWebSocket(w, r)
	case "/status":
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeHubToken {
			http.Error(w, "token", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.routes = append(f.routes, r.URL.Path)
		pid, hubID := f.statusPID, f.statusHubID
		f.mu.Unlock()
		if pid == 0 {
			pid = os.Getpid()
		}
		if hubID == "" {
			hubID = fakeHubID
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"hubId": hubID, "pid": pid})
	case "/shutdown":
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		if r.Header.Get("Authorization") != "Bearer "+fakeHubToken {
			f.badShutdownTokens++
			f.mu.Unlock()
			http.Error(w, "token", http.StatusUnauthorized)
			return
		}
		f.shutdowns++
		f.routes = append(f.routes, r.URL.Path)
		onShutdown := f.onShutdown
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		if onShutdown != nil {
			go onShutdown()
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHub) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	want := hubAuthSubprotocolPrefix + fakeHubToken
	offered := false
	for _, protocol := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.TrimSpace(protocol) == want {
			offered = true
		}
	}
	f.mu.Lock()
	refused := f.refuseUpgrades
	f.mu.Unlock()
	if refused {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !offered {
		f.mu.Lock()
		trusts := f.trustsLocalOrigin
		f.mu.Unlock()
		if trusts && isLocalOrigin(r.Header.Get("Origin")) {
			f.serveTokenless(w, r)
			return
		}
		http.Error(w, "token", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{want}})
	if err != nil {
		return
	}
	ws.SetReadLimit(hubReadLimit)
	conn := &fakeConn{conn: ws}
	f.mu.Lock()
	f.conn = conn
	f.subscriptions = make(map[string]bool)
	f.mu.Unlock()
	defer func() { _ = ws.CloseNow() }()
	for {
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var frame struct {
			Kind          string          `json:"kind"`
			Envelope      json.RawMessage `json:"envelope"`
			SessionID     string          `json:"sessionId"`
			SinceSequence *int64          `json:"sinceSequence"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch frame.Kind {
		case frameCommand:
			var command fakeCommand
			if json.Unmarshal(frame.Envelope, &command) != nil {
				continue
			}
			f.command(conn, command)
		case frameStreamSubscribe:
			f.subscribe(conn, frame.SessionID, frame.SinceSequence)
		case frameStreamUnsubscribe:
			f.mu.Lock()
			delete(f.subscriptions, frame.SessionID)
			f.unsubscribes = append(f.unsubscribes, frame.SessionID)
			f.mu.Unlock()
		}
	}
}

// subscribe registers a subscription and replays the log after since.
func (f *fakeHub) subscribe(conn *fakeConn, sessionID string, since *int64) {
	f.mu.Lock()
	f.subscriptions[sessionID] = true
	f.subscribes = append(f.subscribes, fakeSubscribe{SessionID: sessionID, SinceSequence: since})
	var replay []fakeEvent
	if since != nil {
		for _, event := range f.log {
			if event.sessionID == sessionID && event.sequence > *since {
				replay = append(replay, event)
			}
		}
	}
	f.mu.Unlock()
	for _, event := range replay {
		_ = conn.write(map[string]any{"kind": frameEvent, "envelope": event.envelope})
	}
}

// command records one command and answers it.
func (f *fakeHub) command(conn *fakeConn, command fakeCommand) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	handler := f.handlers[command.Command]
	f.mu.Unlock()
	select {
	case f.arrived <- command:
	default:
	}
	var reply fakeReply
	if handler != nil {
		reply = handler(command)
	} else {
		reply = f.defaultReply(command)
	}
	if reply.Hold {
		return
	}
	f.replyOn(conn, command.RequestID, reply)
}

// defaultReply answers a command as the daemon does.
func (f *fakeHub) defaultReply(command fakeCommand) fakeReply {
	switch command.Command {
	case commandClientRegister:
		return fakeReply{Payload: map[string]any{"clientId": command.ClientID}}
	case commandSessionCreate:
		var config struct {
			SessionID string `json:"sessionId"`
		}
		command.field("sessionConfig", &config)
		id := config.SessionID
		if id == "" {
			// Unique across every fake of the test binary: the worker's claim of
			// a session is process-wide.
			id = "sess-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
		}
		return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": id}}}
	case commandSessionSendInput:
		if command.str("delivery") == deliverySteer {
			return fakeReply{Payload: map[string]any{"queued": true}}
		}
		f.emit(command.str("sessionId"), eventRunStarted, map[string]any{"requestId": command.RequestID, "clientId": command.ClientID})
		// The daemon answers a turn when the turn ends; the test ends it.
		return fakeReply{Hold: true}
	case commandSessionMessages:
		f.mu.Lock()
		messages, ok := f.messages[command.str("sessionId")]
		f.mu.Unlock()
		if !ok {
			return fakeReply{Code: "session_not_found", Message: "Unknown session: " + command.str("sessionId")}
		}
		return fakeReply{Payload: map[string]any{"sessionId": command.str("sessionId"), "messages": messages}}
	case commandSessionGet:
		sessionID := command.str("sessionId")
		f.mu.Lock()
		record, recorded := f.records[sessionID]
		_, stored := f.messages[sessionID]
		f.mu.Unlock()
		if recorded {
			return fakeReply{Payload: map[string]any{"session": record}}
		}
		if stored {
			return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": sessionID, "status": "completed"}}}
		}
		return fakeReply{Code: "session_not_found", Message: "Unknown session: " + sessionID}
	case commandSessionCompactionGet:
		f.mu.Lock()
		state := f.compaction[command.str("sessionId")]
		f.mu.Unlock()
		payload := map[string]any{"sessionId": command.str("sessionId")}
		if state != nil {
			payload["state"] = state
		}
		return fakeReply{Payload: payload}
	case commandSessionList:
		f.mu.Lock()
		sessions := append([]map[string]any(nil), f.sessions...)
		f.mu.Unlock()
		if sessions == nil {
			sessions = []map[string]any{}
		}
		return fakeReply{Payload: map[string]any{"sessions": sessions}}
	case commandRunAbort:
		return fakeReply{Payload: map[string]any{"applied": true}}
	default:
		return fakeReply{Payload: map[string]any{}}
	}
}

// replyOn writes the reply of one command.
func (f *fakeHub) replyOn(conn *fakeConn, requestID string, reply fakeReply) {
	envelope := map[string]any{"version": hubProtocolVersion, "requestId": requestID, "ok": reply.Code == ""}
	if reply.Code != "" {
		envelope["error"] = map[string]any{"code": reply.Code, "message": reply.Message}
	} else {
		payload := reply.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		envelope["payload"] = payload
	}
	_ = conn.write(map[string]any{"kind": frameReply, "envelope": envelope})
}

// reply answers a command that the fake held.
func (f *fakeHub) reply(requestID string, reply fakeReply) {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		f.replyOn(conn, requestID, reply)
	}
}

// emit appends an event to the log and sends it on the connection when its
// session is subscribed.
func (f *fakeHub) emit(sessionID, event string, payload any) int64 {
	return f.emitAt(sessionID, event, payload, time.Now().UnixMilli())
}

// emitAt is emit with the event's timestamp stated.
func (f *fakeHub) emitAt(sessionID, event string, payload any, timestamp int64) int64 {
	f.mu.Lock()
	f.sequence++
	sequence := f.sequence
	envelope, err := json.Marshal(map[string]any{
		"version":   hubProtocolVersion,
		"event":     event,
		"eventId":   fmt.Sprintf("hevt_%d", sequence),
		"sessionId": sessionID,
		"timestamp": timestamp,
		"sequence":  sequence,
		"payload":   payload,
	})
	if err != nil {
		f.mu.Unlock()
		panic(err)
	}
	f.log = append(f.log, fakeEvent{sequence: sequence, sessionID: sessionID, envelope: envelope})
	conn, subscribed := f.conn, f.subscriptions[sessionID]
	f.mu.Unlock()
	if conn != nil && subscribed {
		_ = conn.write(map[string]any{"kind": frameEvent, "envelope": json.RawMessage(envelope)})
	}
	return sequence
}

// setRefuseUpgrades refuses or admits every later WebSocket upgrade.
func (f *fakeHub) setRefuseUpgrades(refuse bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuseUpgrades = refuse
}

// sendRaw sends data as one frame on the current connection, as it is.
func (f *fakeHub) sendRaw(data []byte) error {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("the fake hub holds no connection")
	}
	return conn.writeRaw(data)
}

// drop closes the current connection, as a daemon restart or a network fault
// would.
func (f *fakeHub) drop() {
	f.mu.Lock()
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	if conn != nil {
		_ = conn.conn.CloseNow()
	}
}

// waitCommand returns the next command of the given name that arrives, and
// fails when none arrives within the deadline.
func (f *fakeHub) waitCommand(name string) (fakeCommand, bool) {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case command := <-f.arrived:
			if command.Command == name {
				return command, true
			}
		case <-deadline.C:
			return fakeCommand{}, false
		}
	}
}

// commandsNamed returns every command of one name the fake received, in order.
func (f *fakeHub) commandsNamed(name string) []fakeCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCommand
	for _, command := range f.commands {
		if command.Command == name {
			out = append(out, command)
		}
	}
	return out
}

// subscribeFrames returns every subscribe frame the fake received.
func (f *fakeHub) subscribeFrames() []fakeSubscribe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSubscribe(nil), f.subscribes...)
}

// unsubscribeFrames returns the session of every unsubscribe frame the fake
// received.
func (f *fakeHub) unsubscribeFrames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unsubscribes...)
}

// serveTokenless opens a connection with no token and holds it until the
// client closes it. The connection takes no command: the client that opens it is
// the start's check, which closes it at once.
func (f *fakeHub) serveTokenless(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.mu.Lock()
	f.tokenlessUpgrades++
	f.mu.Unlock()
	_, _, _ = ws.Read(r.Context())
	_ = ws.CloseNow()
}

// isLocalOrigin is Cline's isLocalHubOrigin: an Origin whose host is one of the
// host names that Cline counts as local.
func isLocalOrigin(origin string) bool {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" {
		return false
	}
	return slices.Contains(clinesLocalHostNames, strings.ToLower(parsed.Hostname()))
}

// shutdownCount returns how many authenticated shutdown requests arrived.
func (f *fakeHub) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

// closeConnections closes every connection, for the test's cleanup.
func (f *fakeHub) closeConnections() {
	f.drop()
}

// newFakeHubServer starts a fake hub in this process.
func newFakeHubServer(t interface {
	Helper()
	Cleanup(func())
}) (*fakeHub, *httptest.Server) {
	t.Helper()
	hub := newFakeHubState()
	server := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.closeConnections()
		server.Close()
	})
	return hub, server
}

// requestRoutes returns the authenticated HTTP requests in the order they
// arrived.
func (f *fakeHub) requestRoutes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.routes...)
}

// fakeRecord is the discovery record of a fake hub at url.
func fakeRecord(url string) discoveryRecord {
	return discoveryRecord{
		HubID:                    fakeHubID,
		ProtocolVersion:          hubProtocolVersion,
		MinClientProtocolVersion: hubProtocolVersion,
		MaxClientProtocolVersion: hubProtocolVersion,
		CoreVersion:              "0.0.85",
		AuthToken:                fakeHubToken,
		URL:                      strings.Replace(url, "http://", "ws://", 1) + hubPathname,
	}
}
