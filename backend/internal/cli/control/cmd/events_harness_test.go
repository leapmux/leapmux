package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// lineSink is an io.Writer that hands each line it receives to a channel, so
// a test reads what a running stream printed, line by line, as it prints.
type lineSink struct {
	mu      sync.Mutex
	partial bytes.Buffer
	lines   chan string
}

func newLineSink() *lineSink { return &lineSink{lines: make(chan string, 256)} }

func (w *lineSink) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial.Write(p)
	for {
		line, err := w.partial.ReadString('\n')
		if err != nil {
			// No newline yet: keep the rest for the next write.
			w.partial.Reset()
			w.partial.WriteString(line)
			return len(p), nil
		}
		w.lines <- line[:len(line)-1]
	}
}

// decodeEventLine decodes one {"data": …} line of the stream.
func decodeEventLine(t *testing.T, line string) streamEvent {
	t.Helper()
	var env struct {
		Data streamEvent `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &env), "line: %s", line)
	return env.Data
}

// hubStep is one step of a fake hub session: an event, or the end of the
// session with err.
type hubStep struct {
	event *leapmuxv1.WatchUserEvent
	end   bool
	err   error
}

// fakeHubSession is one subscription that a fakeHub serves.
type fakeHubSession struct {
	steps chan hubStep
}

// send delivers evt on the session.
func (s *fakeHubSession) send(evt *leapmuxv1.WatchUserEvent) { s.steps <- hubStep{event: evt} }

// end ends the session with err, as a dropped connection does.
func (s *fakeHubSession) end(err error) { s.steps <- hubStep{end: true, err: err} }

// fakeHub is a hubSessionFunc that a test drives: each subscription
// announces itself on sessions and then serves the steps that the test
// sends.
type fakeHub struct {
	sessions chan *fakeHubSession
}

func newFakeHub() *fakeHub { return &fakeHub{sessions: make(chan *fakeHubSession, 8)} }

func (h *fakeHub) session(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
	s := &fakeHubSession{steps: make(chan hubStep, 32)}
	h.sessions <- s
	for {
		select {
		case step := <-s.steps:
			if step.end {
				return step.err
			}
			if err := onEvent(step.event); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// recordingWatch is a streamevents.Transport that records each subscription
// and each revision of its interest, and lets a test push frames into the
// newest subscription.
type recordingWatch struct {
	mu sync.Mutex
	// openErrs fails the next opens, one error each.
	openErrs []error
	// requests holds the opening request and each revision of the newest
	// subscription, in order.
	requests []*leapmuxv1.WatchEventsRequest
	onFrame  func(*leapmuxv1.WatchEventsResponse)
	handle   *recordingHandle
	// opened receives the opening request of each subscription, and revised
	// each revision.
	opened  chan *leapmuxv1.WatchEventsRequest
	revised chan *leapmuxv1.WatchEventsRequest
	// cancelled receives one value for each subscription that the feed
	// cancelled.
	cancelled chan struct{}
}

func newRecordingWatch() *recordingWatch {
	return &recordingWatch{
		opened:    make(chan *leapmuxv1.WatchEventsRequest, 16),
		revised:   make(chan *leapmuxv1.WatchEventsRequest, 16),
		cancelled: make(chan struct{}, 16),
	}
}

func (w *recordingWatch) OpenWatchEvents(ctx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (streamevents.Handle, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.openErrs) > 0 {
		err := w.openErrs[0]
		w.openErrs = w.openErrs[1:]
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &recordingHandle{watch: w, cancel: cancel, done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		close(h.done)
	}()
	w.requests = []*leapmuxv1.WatchEventsRequest{req}
	w.onFrame = onFrame
	w.handle = h
	w.opened <- req
	return h, nil
}

// recordingHandle is the handle of one recordingWatch subscription.
type recordingHandle struct {
	watch  *recordingWatch
	cancel context.CancelFunc
	done   chan struct{}
	// endErr is what EndErr reports once the stream ended.
	mu     sync.Mutex
	endErr error
	// cancelledByFeed reports that Cancel ran.
	cancelledByFeed bool
}

func (h *recordingHandle) Update(req *leapmuxv1.WatchEventsRequest) error {
	select {
	case <-h.done:
		return context.Canceled
	default:
	}
	h.watch.mu.Lock()
	h.watch.requests = append(h.watch.requests, req)
	h.watch.mu.Unlock()
	h.watch.revised <- req
	return nil
}

func (h *recordingHandle) Cancel() {
	h.mu.Lock()
	first := !h.cancelledByFeed
	h.cancelledByFeed = true
	h.mu.Unlock()
	h.cancel()
	if first {
		h.watch.cancelled <- struct{}{}
	}
}

func (h *recordingHandle) Done() <-chan struct{} { return h.done }

func (h *recordingHandle) EndErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.endErr
}

// push delivers resp on the newest subscription.
func (w *recordingWatch) push(resp *leapmuxv1.WatchEventsResponse) {
	w.mu.Lock()
	onFrame := w.onFrame
	w.mu.Unlock()
	onFrame(resp)
}

// lastRequest is the interest in force on the newest subscription.
func (w *recordingWatch) lastRequest() *leapmuxv1.WatchEventsRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requests[len(w.requests)-1]
}

// ack acknowledges the interest in force, with the rejections given.
func (w *recordingWatch) ack(rejectedAgents ...*leapmuxv1.WatchRejection) {
	w.push(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
		UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: w.lastRequest().GetUpdateId(), RejectedAgents: rejectedAgents},
	}})
}

// endStream ends the newest subscription with err as its reason, as a
// dropped connection does.
func (w *recordingWatch) endStream(err error) {
	w.mu.Lock()
	h := w.handle
	w.mu.Unlock()
	h.mu.Lock()
	h.endErr = err
	h.mu.Unlock()
	h.cancel()
}

// agentReads answers the readAgents of a feed from the channel that the test
// feeds, and reports each read as it starts.
type agentReads struct {
	started chan []string
	answers chan agentReadAnswer
}

type agentReadAnswer struct {
	levels map[string]*leapmuxv1.AgentInfo
	err    error
}

func newAgentReads() *agentReads {
	return &agentReads{started: make(chan []string, 16), answers: make(chan agentReadAnswer, 16)}
}

func (r *agentReads) read(ctx context.Context, agentIDs []string) (map[string]*leapmuxv1.AgentInfo, error) {
	r.started <- agentIDs
	select {
	case a := <-r.answers:
		return a.levels, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// answer answers the read in flight with one row per agent.
func (r *agentReads) answer(rows ...*leapmuxv1.AgentInfo) {
	levels := map[string]*leapmuxv1.AgentInfo{}
	for _, row := range rows {
		levels[row.GetId()] = row
	}
	r.answers <- agentReadAnswer{levels: levels}
}

// agentLevelRow is the row of an agent with its status and published activity.
func agentLevelRow(id string, status leapmuxv1.AgentStatus, activity leapmuxv1.AgentActivityState) *leapmuxv1.AgentInfo {
	return &leapmuxv1.AgentInfo{Id: id, Status: status, PublishedActivityState: activity}
}

// fakeWorkerSide is what the stream reads of one Worker in a test.
type fakeWorkerSide struct {
	watch *recordingWatch
	reads *agentReads
	// openErr, when set, fails every open of the transport.
	mu      sync.Mutex
	openErr error
}

// streamHarness runs an eventStream against a fake hub and fake Workers,
// with a mock clock, and captures what it prints.
type streamHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	clock  *quartz.Mock
	hub    *fakeHub
	out    *lineSink
	errOut *lineSink
	done   chan error

	mu      sync.Mutex
	workers map[string]*fakeWorkerSide
}

// streamHarnessOptions changes the stream that startStreamHarness runs.
type streamHarnessOptions struct {
	workspace string
	kinds     []string
	limit     int
	heartbeat time.Duration
	// beforeRun runs before the stream starts, to install traps on the
	// clock. A trap that it installs closes before the stream stops, so a
	// call that it holds cannot keep the stream from stopping.
	beforeRun func(clock *quartz.Mock)
}

func startStreamHarness(t *testing.T, opts streamHarnessOptions) *streamHarness {
	t.Helper()
	filter, err := parseKindFilter(opts.kinds)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	h := &streamHarness{
		t:       t,
		ctx:     ctx,
		cancel:  cancel,
		clock:   testutil.NewQuartzMock(t),
		hub:     newFakeHub(),
		out:     newLineSink(),
		errOut:  newLineSink(),
		done:    make(chan error, 1),
		workers: map[string]*fakeWorkerSide{},
	}
	stopped := make(chan struct{})
	// Registered before beforeRun, so it runs after the cleanups of the
	// traps: t.Cleanup runs the last registered first.
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Error("the stream did not stop with its context")
		}
	})
	if opts.beforeRun != nil {
		opts.beforeRun(h.clock)
	}
	stream := newEventStream(eventStreamConfig{
		printer:      &eventPrinter{p: &cli.Printer{Out: h.out, Err: h.errOut, Format: cli.FormatJSON}, filter: filter, limit: opts.limit},
		clock:        h.clock,
		workspace:    opts.workspace,
		heartbeat:    opts.heartbeat,
		hubSession:   h.hub.session,
		workerSource: h.workerSource,
	})
	go func() {
		h.done <- stream.run(ctx)
		close(stopped)
	}()
	return h
}

// trapNewTimer catches each timer that the code arms with tag. It leaves the
// stop of the timer alone: a woken watch stops its timer, and a trapped stop
// would hold it.
func trapNewTimer(t *testing.T, clock *quartz.Mock, tag string) *quartz.Trap {
	t.Helper()
	trap := clock.Trap().NewTimer(tag)
	t.Cleanup(trap.Close)
	return trap
}

// worker returns the fake of the Worker workerID, which the stream reads
// once it watches a tab there.
func (h *streamHarness) worker(workerID string) *fakeWorkerSide {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := h.workers[workerID]
	if w == nil {
		w = &fakeWorkerSide{watch: newRecordingWatch(), reads: newAgentReads()}
		h.workers[workerID] = w
	}
	return w
}

func (h *streamHarness) workerSource(workerID string) workerFeedSource {
	w := h.worker(workerID)
	return workerFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) {
			w.mu.Lock()
			err := w.openErr
			w.mu.Unlock()
			if err != nil {
				return nil, nil, err
			}
			return w.watch, func() {}, nil
		},
		readAgents: w.reads.read,
		clock:      h.clock,
	}
}

// session waits for the next subscription to the hub.
func (h *streamHarness) session() *fakeHubSession {
	h.t.Helper()
	return receive(h.t, h.ctx, h.hub.sessions)
}

// next waits for the next event on stdout.
func (h *streamHarness) next() streamEvent {
	h.t.Helper()
	return decodeEventLine(h.t, receive(h.t, h.ctx, h.out.lines))
}

// nextNotice waits for the next line on stderr.
func (h *streamHarness) nextNotice() string {
	h.t.Helper()
	return receive(h.t, h.ctx, h.errOut.lines)
}

// requireQuiet fails when the stream printed a line that the test did not
// read. The stream prints from its own goroutine, so this checks once after
// the steps that would print it returned.
func (h *streamHarness) requireQuiet() {
	h.t.Helper()
	select {
	case line := <-h.out.lines:
		h.t.Fatalf("unexpected event: %s", line)
	case line := <-h.errOut.lines:
		h.t.Fatalf("unexpected notice: %s", line)
	default:
	}
}

// result waits for the stream to end, and returns its error.
func (h *streamHarness) result() error {
	h.t.Helper()
	return receive(h.t, h.ctx, h.done)
}

// --- CRDT builders ---

// testHLC is a canonical HLC at n milliseconds.
func testHLC(n int64) *leapmuxv1.HLC { return &leapmuxv1.HLC{Physical: n, ClientId: "test"} }

// rootOf is the id of the root tile of a test workspace.
func rootOf(workspaceID string) string { return "root-" + workspaceID }

// tabRecordOn is the record of a live tab on the root tile of workspaceID.
func tabRecordOn(tabType leapmuxv1.TabType, tabID, workspaceID, workerID string) *leapmuxv1.TabRecord {
	return &leapmuxv1.TabRecord{
		TabType:  tabType,
		TabId:    tabID,
		TileId:   &leapmuxv1.LWWString{Value: rootOf(workspaceID), Hlc: testHLC(1)},
		WorkerId: &leapmuxv1.LWWString{Value: workerID, Hlc: testHLC(1)},
	}
}

func agentTabRecord(tabID, workspaceID, workerID string) *leapmuxv1.TabRecord {
	return tabRecordOn(leapmuxv1.TabType_TAB_TYPE_AGENT, tabID, workspaceID, workerID)
}

func terminalTabRecord(tabID, workspaceID, workerID string) *leapmuxv1.TabRecord {
	return tabRecordOn(leapmuxv1.TabType_TAB_TYPE_TERMINAL, tabID, workspaceID, workerID)
}

// snapshotEvent is the initial frame of a hub session: the workspaces, each
// with a root leaf, and the tabs.
func snapshotEvent(workspaceIDs []string, tabs ...*leapmuxv1.TabRecord) *leapmuxv1.WatchUserEvent {
	m := &leapmuxv1.UserMaterialized{
		UserId:     "u-1",
		Nodes:      map[string]*leapmuxv1.NodeRecord{},
		Tabs:       map[string]*leapmuxv1.TabRecord{},
		Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{},
		MaxHlc:     testHLC(1),
	}
	for _, ws := range workspaceIDs {
		m.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: rootOf(ws)}
		m.Nodes[rootOf(ws)] = &leapmuxv1.NodeRecord{
			NodeId: rootOf(ws),
			Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: testHLC(1)},
		}
	}
	for _, tab := range tabs {
		m.Tabs[tab.GetTabId()] = tab
	}
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{Initial: m}}
}

// materializedTab is the frame of a tab that enters the scope.
func materializedTab(tab *leapmuxv1.TabRecord) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{
		EntityMaterialized: &leapmuxv1.EntityMaterialized{AtHlc: testHLC(10), Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: tab}},
	}}
}

// removedTab is the frame of a tab that leaves the scope.
func removedTab(tabType leapmuxv1.TabType, tabID string) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_EntityRemoved{
		EntityRemoved: &leapmuxv1.EntityRemoved{AtHlc: testHLC(10), Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabType: tabType, TabId: tabID}}},
	}}
}

// batchEvent is a batch frame of ops, each committed at the HLC given.
func batchEvent(ops ...*leapmuxv1.CrdtOp) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Batch{Batch: &leapmuxv1.OpBatch{BatchId: "b", Ops: ops}}}
}

func batchEndEvent() *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{AtHlc: testHLC(10)}}}
}

// closeTabOp tombstones a tab, as a close does.
func closeTabOp(tabType leapmuxv1.TabType, tabID string, at int64) *leapmuxv1.CrdtOp {
	return &leapmuxv1.CrdtOp{OpId: "close-" + tabID, CanonicalHlc: testHLC(at), Body: &leapmuxv1.CrdtOp_TombstoneTab{
		TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: &leapmuxv1.TabIdent{TabType: tabType, TabId: tabID}},
	}}
}

// moveTabOp puts a tab on the root tile of workspaceID.
func moveTabOp(tabType leapmuxv1.TabType, tabID, workspaceID string, at int64) *leapmuxv1.CrdtOp {
	return &leapmuxv1.CrdtOp{OpId: "move-" + tabID, CanonicalHlc: testHLC(at), Body: &leapmuxv1.CrdtOp_SetTabRegister{
		SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			Tab:   &leapmuxv1.TabIdent{TabType: tabType, TabId: tabID},
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: rootOf(workspaceID)},
		},
	}}
}

func workspaceCreatedEvent(id, title string) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Created{Created: &leapmuxv1.WorkspaceCreated{WorkspaceId: id, Title: title, RootNodeId: rootOf(id)}}}
}

func workspaceRenamedEvent(id, title string) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Renamed{Renamed: &leapmuxv1.WorkspaceRenamed{WorkspaceId: id, Title: title}}}
}

func workspaceDeletedEvent(id string) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Deleted{Deleted: &leapmuxv1.WorkspaceDeleted{WorkspaceId: id}}}
}

func workersChangedEvent() *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_WorkersChanged{WorkersChanged: &leapmuxv1.WorkersChanged{}}}
}

// --- Worker frames ---

func terminalFrame(terminalID string, event *leapmuxv1.TerminalEvent) *leapmuxv1.WatchEventsResponse {
	event.TerminalId = terminalID
	return &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{TerminalEvent: event}}
}

func bellFrame(terminalID string) *leapmuxv1.WatchEventsResponse {
	return terminalFrame(terminalID, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}}})
}

const (
	statusActive   = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
	statusStarting = leapmuxv1.AgentStatus_AGENT_STATUS_STARTING
)
