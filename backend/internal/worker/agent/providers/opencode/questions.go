package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// The OpenCode family asks the reader a question through its `question` tool, and
// that tool blocks until something answers it. The daemon states the question on its
// own event stream and takes the answer over HTTP. Its Agent Client Protocol adapter
// carries neither half: the adapter's event bridge handles four event types
// (session.status, permission.asked, message.part.updated, message.part.delta) and a
// question is not one of them, and the protocol's client-method table holds no
// question method to answer through. What reaches an ACP client is the tool call
// alone, as a notification with no request id, so a reader who answered it would have
// nowhere to send the answer and the turn would block until it was interrupted.
//
// `opencode acp` starts the daemon's HTTP server beside the stdio stream for exactly
// this reason -- the adapter is itself a client of that server -- so LeapMux reads the
// two routes the adapter does not. The daemon chooses that server's port and states
// it nowhere, so discover below reads it back from the process.
//
// This is the one provider in LeapMux that speaks two transports. The stdio stream
// carries the session, and this carries the questions.
const (
	// openCodeQuestionToolEnv turns ON the daemon's own question tool. Kilo has
	// its own spelling of the same flag; see kiloQuestionToolEnv.
	//
	// The tool is GATED, and the gate cannot be opened any other way. The daemon
	// registers it only when its client is one of `app`, `cli` or `desktop`, or when
	// this variable is set -- and `opencode acp` assigns `OPENCODE_CLIENT="acp"` at
	// the top of its own handler, before the configuration that reads it is built. So
	// no value LeapMux passes for that variable reaches the gate. A live session under
	// `.tmp/probe` settles it: with `OPENCODE_CLIENT=cli` and this flag unset, the
	// daemon offers no question tool at all, and the model resorts to running
	// `opencode question ...` through the shell instead.
	//
	// Without the flag the whole bridge below is unreachable: the agent never asks, so
	// no `question.asked` is ever published and nothing is answered. Both providers
	// pin it, so an inherited `=0` cannot turn off the tool the bridge exists to serve.
	openCodeQuestionToolEnv = "OPENCODE_ENABLE_QUESTION_TOOL"

	// The loopback interface only. The daemon defaults to it as well; stating it
	// keeps a configured default from publishing the port to the network.
	openCodeQuestionHost = "127.0.0.1"
	EventRoute           = "/event"
	QuestionRoot         = "/question"
	// A dropped event stream is reconnected. The daemon holds a question while
	// LeapMux is away and the reconnect replays nothing, so every connection lists
	// the questions the daemon still holds. That list closes the gap in BOTH
	// directions: it raises a card for a question asked during the gap, and it
	// retires a card for one that another client answered during the gap.
	openCodeQuestionRetryDelay = time.Second
	// One event line. A question carries its whole option list, so the default
	// scanner limit of 64 KiB is too small for a long one.
	openCodeQuestionMaxEvent = 8 << 20
	openCodeQuestionTimeout  = 30 * time.Second
	// How long discovery waits for the daemon to bind. The server listens before the
	// ACP connection is set up, so it is normally already there; this covers a slow
	// machine rather than a daemon that never binds.
	openCodeDiscoveryTimeout = 20 * time.Second
	// The wait between two searches, doubling from the first to the cap.
	//
	// One search costs a socket read for each candidate -- a `lsof` fork on macOS --
	// and, when the process itself did not answer, one `Ppid` syscall for every
	// process on the host. A fixed 250 ms ran that 80 times over the timeout for a
	// daemon that never binds, and it also made the NORMAL case wait 250 ms for a
	// server that binds in a few. Starting short and backing off answers the common
	// case sooner and costs a quarter of the searches in the worst one.
	openCodeDiscoveryFirstPoll = 50 * time.Millisecond
	openCodeDiscoveryMaxPoll   = time.Second
	// One candidate port gets this long to prove it is the daemon. It is a loopback
	// request to a port that is already listening, so a slow answer means it is
	// something else that happens to hold a socket.
	openCodeDiscoveryProbeTimeout = 2 * time.Second
	// How far below the launched process discovery looks. A POSIX shell EXECs the
	// daemon, so it is the process itself; PowerShell starts it as a child instead.
	// Nothing legitimate sits deeper than that.
	openCodeDiscoveryDepth = 3
)

// openCodeQuestionControlID keys one question's control request.
//
// The daemon's own question id is unique per daemon and already carries its `que_`
// prefix, so it needs no digest to separate it from another session's. The LeapMux
// prefix keeps it apart from the "jsonrpc:"-keyed rows of the stdio stream, which
// the same agent registers.
func openCodeQuestionControlID(questionID string) string {
	return "opencode-question:" + questionID
}

// ACPArgs builds the daemon's arguments with its own server enabled.
//
// The port is the daemon's to choose. `--port 0` makes it bind one and never state
// which, so openCodeQuestions.discover reads it back from the process itself. The
// alternative -- reserving a port here and passing it -- releases that port before the
// daemon binds it, and a process that takes it in between kills the whole session
// over a server that only the questions need.
//
// A daemon whose `acp` command does not accept these flags refuses to start and
// states which flag it rejected. That is the intended failure: a silent fall back
// would leave every question unanswerable with nothing to say why.
func ACPArgs() []string {
	return []string{"acp", "--hostname", openCodeQuestionHost, "--port", "0"}
}

// discover finds the daemon's own HTTP server, which it never states.
//
// Two steps, and the second is what makes the first safe. The operating system is
// asked which ports the daemon listens on, and each candidate is then asked for its
// question list. A port that belongs to something else answers something else, so it
// is skipped rather than answered to.
//
// The process to inspect is not always the one LeapMux launched. A POSIX shell
// `exec`s the daemon, which replaces the shell, so the launched pid IS the daemon.
// PowerShell has no exec and starts the daemon as a child, so Windows needs the
// descendants as well -- and openCodeDescendants walks them for exactly that reason.
//
// gopsutil reads the sockets differently on each platform: `lsof` on macOS, /proc on
// Linux, and the IP helper API on Windows. All three answer for a process this worker
// owns, which is the only process asked about here.
func (q *openCodeQuestions) discover(ctx context.Context, pid int32) (string, error) {
	deadline := time.Now().Add(openCodeDiscoveryTimeout)
	wait := openCodeDiscoveryFirstPoll
	for {
		// The deadline caps ONE search as well as the loop. A search probes every
		// loopback port of every candidate process, and each probe waits up to
		// openCodeDiscoveryProbeTimeout, so a host with many listening sockets made a
		// single search run for minutes -- the check below runs only after that search
		// returns, so the loop overshot the timeout by the whole excess.
		iterCtx, cancel := context.WithDeadline(ctx, deadline)
		base, found := q.searchOnce(iterCtx, pid)
		cancel()
		if found {
			return base, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no OpenCode question server below process %d within %s", pid, openCodeDiscoveryTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
		if wait *= 2; wait > openCodeDiscoveryMaxPoll {
			wait = openCodeDiscoveryMaxPoll
		}
	}
}

// searchOnce asks the launched process, and then everything below it, for the server.
//
// The launched process answers on every platform whose shell can `exec`, because the
// exec replaced the shell with the daemon. Asking it FIRST is what keeps the
// process-table read -- one `Ppid` syscall for every process on the host -- out of
// the search that succeeds. Windows is the platform that needs the descendants, and
// it is the platform that still pays for them.
func (q *openCodeQuestions) searchOnce(ctx context.Context, pid int32) (string, bool) {
	if base, found := q.probePorts(ctx, openCodePidPorts(ctx, pid)); found {
		return base, true
	}
	for _, child := range openCodeDescendants(ctx, pid) {
		if child == pid {
			continue
		}
		if base, found := q.probePorts(ctx, openCodePidPorts(ctx, child)); found {
			return base, true
		}
	}
	return "", false
}

// probePorts returns the first candidate that answers the question list.
func (q *openCodeQuestions) probePorts(ctx context.Context, ports []int) (string, bool) {
	for _, port := range ports {
		base := "http://" + net.JoinHostPort(openCodeQuestionHost, strconv.Itoa(port))
		if q.servesQuestions(ctx, base) {
			return base, true
		}
	}
	return "", false
}

// servesQuestions reports whether ONE candidate address is the daemon's server.
//
// The question list is the cheapest route that identifies it: it needs no session and
// it changes nothing. The test is a heuristic -- another service could answer this path
// with a JSON array -- so it rests on the walk that produced the candidate, which
// offers only sockets held by the process this worker launched.
func (q *openCodeQuestions) servesQuestions(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, openCodeDiscoveryProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+QuestionRoot, nil)
	if err != nil {
		return false
	}
	response, err := q.client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var questions []json.RawMessage
	return json.NewDecoder(io.LimitReader(response.Body, openCodeQuestionMaxEvent)).Decode(&questions) == nil
}

// openCodePidPorts reads every loopback port ONE process listens on. It answers an
// empty list for anything it cannot read, because a process that is still starting has
// no socket yet and the caller polls.
func openCodePidPorts(ctx context.Context, pid int32) []int {
	connections, err := gopsnet.ConnectionsPidWithContext(ctx, "tcp", pid)
	if err != nil {
		return nil
	}
	seen := make(map[int]struct{})
	var ports []int
	for _, connection := range connections {
		if connection.Status != "LISTEN" || connection.Laddr.Port == 0 {
			continue
		}
		// The daemon was told to bind loopback, so a socket on any other
		// interface belongs to something else this process also runs.
		if ip := net.ParseIP(connection.Laddr.IP); ip == nil || !ip.IsLoopback() {
			continue
		}
		port := int(connection.Laddr.Port)
		if _, repeated := seen[port]; repeated {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return ports
}

// openCodeDescendants returns the process and the descendants below it, breadth
// first, to openCodeDiscoveryDepth.
//
// ONE read of the process table, indexed by parent. process.Children() re-runs a full
// scan for each node, which costs depth x (full scan) for the same answer -- the same
// reason the terminal package's own walk reads the table once.
func openCodeDescendants(ctx context.Context, pid int32) []int32 {
	found := []int32{pid}
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return found
	}
	byParent := make(map[int32][]int32, len(processes))
	for _, candidate := range processes {
		parent, err := candidate.PpidWithContext(ctx)
		if err != nil {
			// A process that exits between the listing and this read never aborts
			// the walk, and neither does one this user may not read.
			continue
		}
		byParent[parent] = append(byParent[parent], candidate.Pid)
	}
	// The visited set keeps a stale parent pointer at a recycled pid from closing a
	// loop, which would otherwise hang the discovery goroutine for the session.
	visited := map[int32]struct{}{pid: {}}
	generation := []int32{pid}
	for depth := 0; depth < openCodeDiscoveryDepth && len(generation) > 0; depth++ {
		var next []int32
		for _, parent := range generation {
			for _, child := range byParent[parent] {
				if _, repeated := visited[child]; repeated {
					continue
				}
				visited[child] = struct{}{}
				found = append(found, child)
				next = append(next, child)
			}
		}
		generation = next
	}
	return found
}

// openCodeQuestions bridges the daemon's question routes to LeapMux's control requests.
//
// The zero value is inert: a bridge that was never configured takes no frame and
// publishes nothing, so a test that builds a bare agent needs no server.
type openCodeQuestions struct {
	baseURL string
	client  *http.Client
	sink    agent.ControlServices
	agentID string

	// mu guards baseURL and pending. pending maps the LeapMux request id to the
	// daemon's own question id: the reader answers with the first, and the daemon
	// needs the second. baseURL is written once, by the discovery goroutine.
	mu      sync.Mutex
	pending map[string]string
}

// Configure records the sink. It runs before the process starts, so it opens no
// connection and knows no port yet.
func (q *openCodeQuestions) Configure(sink agent.ControlServices) {
	q.sink = sink
	// No client timeout: the event stream stays open for the life of the session.
	// The per-answer requests below carry their own deadline through the context.
	q.client = &http.Client{}
}

// base is where the daemon's server was found, or "" before discovery finishes.
//
// It is guarded because discovery runs on its own goroutine while the reader may
// already answer something else on the ACP stream.
func (q *openCodeQuestions) base() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.baseURL
}

// Begin finds the daemon's question server and then reads its event stream.
//
// It returns at once. Discovery and the stream both run on one goroutine until ctx
// ends. Discovery that fails leaves the questions unavailable and the session whole:
// everything else travels on the ACP stream, so the reader loses the one surface the
// stream never carried anyway.
func (q *openCodeQuestions) Begin(ctx context.Context, agentID string, pid int) {
	if q.sink == nil {
		return
	}
	q.agentID = agentID
	go func() {
		baseURL, err := q.discover(ctx, int32(pid))
		if err != nil {
			slog.Warn("No OpenCode question server; questions stay unanswerable",
				"agent_id", agentID, "pid", pid, "error", err)
			return
		}
		q.mu.Lock()
		q.baseURL = baseURL
		q.mu.Unlock()
		q.run(ctx)
	}()
}

// beginAt reads a server whose address is already known. Discovery is what finds that
// address in production; a test states it directly.
func (q *openCodeQuestions) beginAt(ctx context.Context, agentID, baseURL string) {
	if q.sink == nil {
		return
	}
	q.agentID = agentID
	q.mu.Lock()
	q.baseURL = baseURL
	q.mu.Unlock()
	go q.run(ctx)
}

func (q *openCodeQuestions) run(ctx context.Context) {
	for {
		if err := q.consume(ctx); err != nil && ctx.Err() == nil {
			slog.Debug("OpenCode question stream ended", "agent_id", q.agentID, "error", err)
		}
		select {
		case <-ctx.Done():
			// The daemon dies with the process, so a question it still holds needs no
			// rejection. The reader's card outlives both, and only this retires it.
			q.cancelAll()
			return
		case <-time.After(openCodeQuestionRetryDelay):
		}
	}
}

// consume reads ONE connection to the event stream until it ends.
func (q *openCodeQuestions) consume(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, q.base()+EventRoute, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := q.client.Do(request)
	if err != nil {
		return err
	}
	// Closed WITHOUT draining. Draining a response to let the connection return to
	// the pool is right for a short body and wrong for this one: the event stream
	// stays open for the whole session, so a read that followed a reader which
	// stopped early would block here until the daemon exits.
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the OpenCode event stream answered %s", response.Status)
	}
	// A question that the daemon raised or settled while LeapMux was away raises no
	// event on this connection, so the pending list is what closes the gap -- on the
	// first connection as well as on every reconnect.
	q.publishPending(ctx)
	return q.readEvents(ctx, response.Body)
}

// readEvents reads server-sent events and hands each one's data to handleEvent.
// providerkit.ReadSSE states the event-stream rules, including the discard of
// an event that the stream ends in the middle of: the reconnect that follows
// restates the pending list, so no question is lost with it.
func (q *openCodeQuestions) readEvents(ctx context.Context, body io.Reader) error {
	return providerkit.ReadSSE(body, openCodeQuestionMaxEvent, func(event providerkit.SSEEvent) {
		q.handleEvent(ctx, event.Data)
	})
}

func (q *openCodeQuestions) handleEvent(ctx context.Context, data []byte) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	switch event.Type {
	case contracts.OpenCodeEventQuestionAsked:
		q.publish(ctx, event.Properties)
	case contracts.OpenCodeEventQuestionReplied, contracts.OpenCodeEventQuestionRejected:
		// Answered somewhere else -- another client of the same daemon, or LeapMux
		// itself. Either way the card is stale now.
		q.withdraw(event.Properties)
	}
}

// publishPending reconciles the reader's question cards with the list the daemon
// holds. The list is AUTHORITATIVE: a question on it gets a card, and a card for a
// question that is not on it is retired.
//
// PublishControlRequest keeps the claim token of a request it already carries, so a
// question that is on the reader's screen is announced again without disturbing it.
//
// Both halves are needed, and the second one was missing. A question answered in
// ANOTHER client while the event stream was down raises its `question.replied` on a
// connection LeapMux no longer holds, so nothing withdrew the card: the reader kept a
// live question card for a question the agent had moved past, and a click on one of
// its options reached a daemon that refuses it.
//
// A list this cannot read retires nothing. Every early return below leaves the cards
// as they are, because a failed read is not a statement that the daemon holds none.
func (q *openCodeQuestions) publishPending(ctx context.Context) {
	// The client carries no timeout of its own, so every request states one. `consume`
	// calls this BEFORE it reads the event stream, so a daemon that accepts the
	// connection and then stalls on this route blocked the whole question bridge for
	// the life of the session, with nothing logged and no retry able to reach it.
	ctx, cancel := context.WithTimeout(ctx, openCodeQuestionTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, q.base()+QuestionRoot, nil)
	if err != nil {
		return
	}
	response, err := q.client.Do(request)
	if err != nil {
		slog.Debug("read the OpenCode questions the daemon holds", "error", err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return
	}
	var questions []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, openCodeQuestionMaxEvent)).Decode(&questions); err != nil {
		slog.Debug("Read the pending OpenCode questions", "agent_id", q.agentID, "error", err)
		return
	}
	held := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		if requestID := q.publish(ctx, question); requestID != "" {
			held[requestID] = struct{}{}
		}
	}
	q.retireUnheld(held)
}

// retireUnheld cancels the card of every question the daemon no longer holds.
//
// The caller passes the request ids of the questions on the daemon's own list. This
// runs on the goroutine that reads the event stream, and that goroutine is the only
// one that publishes, so no question can enter `pending` between the list and this
// sweep. The answer path can still remove one, which leaves nothing to retire.
func (q *openCodeQuestions) retireUnheld(held map[string]struct{}) {
	q.mu.Lock()
	var stale []string
	for requestID := range q.pending {
		if _, still := held[requestID]; still {
			continue
		}
		stale = append(stale, requestID)
	}
	for _, requestID := range stale {
		delete(q.pending, requestID)
	}
	q.mu.Unlock()
	for _, requestID := range stale {
		q.sink.CancelControlRequest(requestID)
	}
}

// publish raises ONE question as a control request.
//
// The stored payload is the event's own shape, `{type, properties}`, which is what
// the browser plugin reads. The event envelope's own id is left out on purpose: the
// shared response reader treats a stored request that carries a top-level `id` as a
// JSON-RPC request and withholds any answer whose id does not match it, and `evt_...`
// addresses the delivery rather than the question. The question's id is in
// `properties.id`, where this keys it from.
//
// It reports the request id it registered, or "" for a frame it could not publish.
// publishPending collects those ids to decide which cards the daemon no longer
// holds, and a frame this could not read must not retire the card it identifies.
func (q *openCodeQuestions) publish(ctx context.Context, properties json.RawMessage) string {
	var question struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(properties, &question); err != nil || question.ID == "" {
		slog.Warn("Read an OpenCode question", "agent_id", q.agentID, "error", err)
		return ""
	}
	payload, err := json.Marshal(struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}{Type: contracts.OpenCodeEventQuestionAsked, Properties: properties})
	if err != nil {
		slog.Error("Build an OpenCode question payload", "agent_id", q.agentID, "error", err)
		return ""
	}
	requestID := openCodeQuestionControlID(question.ID)
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[string]string)
	}
	q.pending[requestID] = question.ID
	q.mu.Unlock()
	if err := q.sink.PublishControlRequest(agent.ControlRequest{
		RequestID:      requestID,
		Payload:        payload,
		AgentSessionID: question.SessionID,
	}); err != nil {
		slog.Error("Publish an OpenCode question", "agent_id", q.agentID, "request_id", requestID, "error", err)
		q.forget(requestID)
		// The daemon blocks on this question. A reader who will never see it must not
		// leave the turn waiting, so the question is rejected here and the agent
		// carries on with the refusal.
		if err := q.reject(ctx, question.ID); err != nil {
			slog.Warn("Reject an OpenCode question LeapMux could not publish",
				"agent_id", q.agentID, "request_id", requestID, "error", err)
		}
		// The record is gone, so the caller must not count this question as held.
		return ""
	}
	return requestID
}

// withdraw retires the card of a question the daemon reports as settled.
func (q *openCodeQuestions) withdraw(properties json.RawMessage) {
	var settled struct {
		RequestID string `json:"requestID"`
	}
	if err := json.Unmarshal(properties, &settled); err != nil || settled.RequestID == "" {
		return
	}
	requestID := openCodeQuestionControlID(settled.RequestID)
	if q.forget(requestID) {
		q.sink.CancelControlRequest(requestID)
	}
}

// forget drops one record and reports whether it was there. The answer path and the
// event path both retire a question, and only the one that took the record acts.
func (q *openCodeQuestions) forget(requestID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, existed := q.pending[requestID]
	delete(q.pending, requestID)
	return existed
}

func (q *openCodeQuestions) cancelAll() {
	q.mu.Lock()
	requestIDs := make([]string, 0, len(q.pending))
	for requestID := range q.pending {
		requestIDs = append(requestIDs, requestID)
	}
	clear(q.pending)
	q.mu.Unlock()
	for _, requestID := range requestIDs {
		q.sink.CancelControlRequest(requestID)
	}
}

// answer sends the reader's decision to the daemon, and reports whether the frame
// was one of its questions.
//
// A frame it does not claim goes to the stdio stream unchanged, which is where every
// other control answer belongs.
func (q *openCodeQuestions) answer(ctx context.Context, raw []byte) (bool, error) {
	if q.client == nil || q.base() == "" {
		return false, nil
	}
	_, requestID, ok := agent.ExtractJSONRPCID(raw)
	if !ok || requestID == "" {
		return false, nil
	}
	q.mu.Lock()
	questionID, pending := q.pending[requestID]
	q.mu.Unlock()
	if !pending {
		return false, nil
	}
	var frame struct {
		Result openCodeAnswerResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return true, fmt.Errorf("decode the OpenCode question answer: %w", err)
	}
	var err error
	switch {
	case frame.Result.Rejected:
		err = q.reject(ctx, questionID)
	case frame.Result.Answers != nil:
		err = q.reply(ctx, questionID, frame.Result.Answers)
	default:
		return true, fmt.Errorf("the OpenCode question answer carries neither answers nor a rejection")
	}
	if err != nil {
		return true, err
	}
	// The daemon states `question.replied` as well, and that arrives on the event
	// stream. Whichever reaches the record first retires it; the other finds nothing.
	q.forget(requestID)
	return true, nil
}

// openCodeAnswerResult is the answer envelope the BROWSER writes and this reads back.
//
// A Go struct tag cannot hold a constant, so the two field names are literals here
// and TestOpenCodeAnswerTagsMatchTheContract pins them to
// contracts/opencode-protocol.json, which the browser reads the same names from. A
// rename on one side alone used to leave `Answers` nil, so `reply` never ran and the
// daemon's question tool blocked for the rest of the session with nothing logged.
type openCodeAnswerResult struct {
	Answers  [][]string `json:"answers"`
	Rejected bool       `json:"rejected"`
}

func (q *openCodeQuestions) reply(ctx context.Context, questionID string, answers [][]string) error {
	return q.post(ctx, questionID, "reply", struct {
		Answers [][]string `json:"answers"`
	}{Answers: answers})
}

func (q *openCodeQuestions) reject(ctx context.Context, questionID string) error {
	return q.post(ctx, questionID, "reject", nil)
}

func (q *openCodeQuestions) post(ctx context.Context, questionID, action string, payload any) error {
	ctx, cancel := context.WithTimeout(ctx, openCodeQuestionTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode the OpenCode question %s: %w", action, err)
		}
		body = bytes.NewReader(encoded)
	}
	url := q.base() + QuestionRoot + "/" + questionID + "/" + action
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("build the OpenCode question %s: %w", action, err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := q.client.Do(request)
	if err != nil {
		return fmt.Errorf("send the OpenCode question %s: %w", action, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		return fmt.Errorf("the OpenCode question %s answered %s: %s", action, response.Status, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<12))
	return nil
}

// BeginQuestionsForTest starts the agent's question bridge under ctx against the
// daemon at baseURL, with sink as its services, without a process to discover it
// from. A test that answers a question through the agent's own SendRawInput
// needs the bridge that Start would have begun.
func (b *FamilyBase) BeginQuestionsForTest(ctx context.Context, sink agent.ControlServices, agentID, baseURL string) {
	b.Questions.Configure(sink)
	b.Questions.beginAt(ctx, agentID, baseURL)
}
