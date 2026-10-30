package kilo

import (
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

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	gopsnet "github.com/shirou/gopsutil/v4/net"
)

// The stdio stream carries the conversation. This stream supplies questions and native session events.
const (
	// StoredMessageReadTimerTag identifies the deadline for one stored-message read.
	StoredMessageReadTimerTag = "kilo-stored-message-read"
	// LatestAssistantErrorReadTimerTag identifies the deadline for one read of
	// the error of the newest stored message.
	LatestAssistantErrorReadTimerTag = "kilo-latest-assistant-error-read"
	// QuestionsDiscoveryTag identifies the discovery deadline and its poll timers.
	QuestionsDiscoveryTag = "kilo-questions-discovery"
	// QuestionsReconnectTag identifies the wait before the next daemon connection.
	QuestionsReconnectTag = "kilo-questions-reconnect"

	// The loopback interface only. The daemon defaults to it as well; stating it
	// keeps a configured default from publishing the port to the network.
	kiloQuestionHost = "127.0.0.1"
	EventRoute       = "/event"
	QuestionRoot     = "/question"
	// A dropped event stream is reconnected. The daemon holds a question while
	// LeapMux is away and the reconnect replays nothing, so every connection lists
	// the questions the daemon still holds. That list closes the gap in BOTH
	// directions: it raises a card for a question asked during the gap, and it
	// retires a card for one that another client answered during the gap.
	kiloQuestionRetryDelay = time.Second
	// One event line. A question carries its whole option list, so the default
	// scanner limit of 64 KiB is too small for a long one.
	kiloQuestionMaxEvent = 8 << 20
	kiloQuestionTimeout  = 30 * time.Second
	// How long discovery waits for the daemon to bind. The server listens before the
	// ACP connection is set up, so it is normally already there; this covers a slow
	// machine rather than a daemon that never binds.
	kiloDiscoveryTimeout = 20 * time.Second
	// The wait between two searches, doubling from the first to the cap.
	//
	// One search costs a socket read for each candidate -- a `lsof` fork on macOS --
	// and, when the process itself did not answer, one `Ppid` syscall for every
	// process on the host. A fixed 250 ms ran that 80 times over the timeout for a
	// daemon that never binds, and it also made the NORMAL case wait 250 ms for a
	// server that binds in a few. Starting short and backing off answers the common
	// case sooner and costs a quarter of the searches in the worst one.
	kiloDiscoveryFirstPoll = 50 * time.Millisecond
	kiloDiscoveryMaxPoll   = time.Second
	// One candidate port gets this long to prove it is the daemon. It is a loopback
	// request to a port that is already listening, so a slow answer means it is
	// something else that happens to hold a socket.
	kiloDiscoveryProbeTimeout = 2 * time.Second
	// How far below the launched process discovery looks. A POSIX shell EXECs the
	// daemon, so it is the process itself; PowerShell starts it as a child instead.
	// Nothing legitimate sits deeper than that.
	kiloDiscoveryDepth = 3
)

// ACPArgs builds the daemon's arguments with its own server enabled.
//
// The port is the daemon's to choose. `--port 0` makes it bind one and never state
// which, so kiloQuestions.discover reads it back from the process itself. The
// alternative -- reserving a port here and passing it -- releases that port before the
// daemon binds it, and a process that takes it in between kills the whole session
// over a server that only the questions need.
//
// A daemon whose `acp` command does not accept these flags refuses to start and
// states which flag it rejected. That is the intended failure: a silent fall back
// would leave every question unanswerable with nothing to say why.
func ACPArgs() []string {
	return []string{"acp", "--hostname", kiloQuestionHost, "--port", "0"}
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
// descendants as well -- and kiloDescendants walks them for exactly that reason.
//
// gopsutil reads the sockets differently on each platform: `lsof` on macOS, /proc on
// Linux, and the IP helper API on Windows. All three answer for a process this worker
// owns, which is the only process asked about here.
func (q *kiloQuestions) discover(ctx context.Context, clock quartz.Clock, pid int32) (string, error) {
	return discoverDaemonServer(ctx, clock, pid, q.searchOnce)
}

// discoverDaemonServer keeps the clock policy separate from socket and process reads.
func discoverDaemonServer(ctx context.Context, clock quartz.Clock, pid int32, search func(context.Context, int32) (string, bool)) (string, error) {
	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := clock.AfterFunc(kiloDiscoveryTimeout, cancel, QuestionsDiscoveryTag, "deadline")
	defer deadline.Stop()
	wait := kiloDiscoveryFirstPoll
	for {
		// The injected deadline also cancels an active socket probe.
		base, found := search(searchCtx, pid)
		if found {
			return base, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if searchCtx.Err() != nil {
			return "", fmt.Errorf("no Kilo question server below process %d within %s", pid, kiloDiscoveryTimeout)
		}
		timer := clock.NewTimer(wait, QuestionsDiscoveryTag, "poll")
		select {
		case <-searchCtx.Done():
		case <-timer.C:
		}
		timer.Stop()
		if wait *= 2; wait > kiloDiscoveryMaxPoll {
			wait = kiloDiscoveryMaxPoll
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
func (q *kiloQuestions) searchOnce(ctx context.Context, pid int32) (string, bool) {
	if base, found := q.probePorts(ctx, kiloPidPorts(ctx, pid)); found {
		return base, true
	}
	for _, child := range kiloDescendants(ctx, pid) {
		if child == pid {
			continue
		}
		if base, found := q.probePorts(ctx, kiloPidPorts(ctx, child)); found {
			return base, true
		}
	}
	return "", false
}

// probePorts returns the first candidate that answers the question list.
func (q *kiloQuestions) probePorts(ctx context.Context, ports []int) (string, bool) {
	for _, port := range ports {
		base := "http://" + net.JoinHostPort(kiloQuestionHost, strconv.Itoa(port))
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
func (q *kiloQuestions) servesQuestions(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, kiloDiscoveryProbeTimeout)
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
	return json.NewDecoder(io.LimitReader(response.Body, kiloQuestionMaxEvent)).Decode(&questions) == nil
}

// kiloPidPorts reads every loopback port ONE process listens on. It answers an
// empty list for anything it cannot read, because a process that is still starting has
// no socket yet and the caller polls.
func kiloPidPorts(ctx context.Context, pid int32) []int {
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

// kiloDescendants returns the process and its descendants in breadth-first order.
// The shared table reads and indexes each process once.
// Discovery keeps readable rows when another process's parent data is unavailable.
func kiloDescendants(ctx context.Context, pid int32) []int32 {
	found := []int32{pid}
	table, _ := procutil.SnapshotProcessTable(ctx)
	if table == nil || ctx.Err() != nil {
		return found
	}
	children, _ := table.DescendantPIDs(int(pid), kiloDiscoveryDepth, 0, nil)
	for _, child := range children {
		found = append(found, int32(child))
	}
	return found
}

// kiloQuestions bridges the daemon's question routes to LeapMux's control requests.
//
// The zero value is inert: a bridge that was never configured takes no frame and
// publishes nothing, so a test that builds a bare agent needs no server.
type kiloQuestions struct {
	baseURL string
	client  *http.Client
	sink    agent.ControlServices
	agentID string
	observe func(json.RawMessage)
	// ready runs once the daemon address is known, before the event stream
	// opens, with the context of the bridge. It may be nil.
	ready func(context.Context)
	// connected runs on each connection of the event stream, before its first
	// event, with the context of the bridge. It reads what the daemon stated
	// while no connection was open. It may be nil.
	connected func(context.Context)

	// mu guards baseURL and pending. pending maps the LeapMux request id to the
	// daemon's own question id: the reader answers with the first, and the daemon
	// needs the second. baseURL is written once, by the discovery goroutine.
	mu      sync.Mutex
	pending map[string]string
}

// Configure records the sink. It runs before the process starts, so it opens no
// connection and knows no port yet.
func (q *kiloQuestions) Configure(sink agent.ControlServices) {
	q.sink = sink
	// No client timeout: the event stream stays open for the life of the session.
	// The per-answer requests below carry their own deadline through the context.
	q.client = &http.Client{}
}

// base is where the daemon's server was found, or "" before discovery finishes.
//
// It is guarded because discovery runs on its own goroutine while the reader may
// already answer something else on the ACP stream.
func (q *kiloQuestions) base() string {
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
func (q *kiloQuestions) Begin(ctx context.Context, clock quartz.Clock, agentID string, pid int) {
	if q.sink == nil {
		return
	}
	q.agentID = agentID
	go func() {
		baseURL, err := q.discover(ctx, clock, int32(pid))
		if err != nil {
			slog.Warn("No Kilo question server; questions stay unanswerable",
				"agent_id", agentID, "pid", pid, "error", err)
			return
		}
		q.mu.Lock()
		q.baseURL = baseURL
		q.mu.Unlock()
		q.announceReady(ctx)
		q.run(ctx, clock)
	}()
}

// announceReady tells the owner that the daemon address is known.
func (q *kiloQuestions) announceReady(ctx context.Context) {
	if q.ready != nil {
		q.ready(ctx)
	}
}

// beginAt reads a server whose address is already known. Discovery is what finds that
// address in production; a test states it directly.
func (q *kiloQuestions) beginAt(ctx context.Context, clock quartz.Clock, agentID, baseURL string) {
	if q.sink == nil {
		return
	}
	q.agentID = agentID
	q.mu.Lock()
	q.baseURL = baseURL
	q.mu.Unlock()
	q.announceReady(ctx)
	go q.run(ctx, clock)
}

func (q *kiloQuestions) run(ctx context.Context, clock quartz.Clock) {
	defer q.cancelAll()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := q.consume(ctx); err != nil && ctx.Err() == nil {
			slog.Debug("Kilo question stream ended", "agent_id", q.agentID, "error", err)
		}
		timer := clock.NewTimer(kiloQuestionRetryDelay, QuestionsReconnectTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		timer.Stop()
	}
}

// consume reads ONE connection to the event stream until it ends.
func (q *kiloQuestions) consume(ctx context.Context) error {
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
		return fmt.Errorf("the Kilo event stream answered %s", response.Status)
	}
	// A question that the daemon raised or settled while LeapMux was away raises no
	// event on this connection, so the pending list is what closes the gap -- on the
	// first connection as well as on every reconnect.
	q.publishPending(ctx)
	if q.connected != nil {
		q.connected(ctx)
	}
	return q.readEvents(ctx, response.Body)
}

// BeginQuestionsForTest starts the agent's question bridge under ctx against the
// daemon at baseURL, with sink as its services, without a process to discover it
// from. A test that answers a question through the agent's own SendRawInput
// needs the bridge that Start would have begun.
func (b *Agent) BeginQuestionsForTest(ctx context.Context, sink agent.ControlServices, agentID, baseURL string) {
	b.configureQuestions(sink)
	b.Questions.beginAt(ctx, b.Clock(), agentID, baseURL)
}
