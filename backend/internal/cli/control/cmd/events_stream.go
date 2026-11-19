package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// errStreamDone ends the stream without a failure: --limit printed its last
// event, or the workspace that --workspace selects was deleted.
var errStreamDone = errors.New("the event stream is done")

// eventHeartbeatTimerTag identifies the heartbeat ticker to a test clock.
const eventHeartbeatTimerTag = "events-heartbeat"

// eventStreamConfig is what one run of `leapmux control events` watches and
// prints.
type eventStreamConfig struct {
	printer *eventPrinter
	clock   quartz.Clock
	// workspace is the workspace that --workspace selects, or "" for every
	// workspace.
	workspace string
	// heartbeat is the interval of stream.heartbeat, or 0 for none.
	heartbeat time.Duration
	// hubSession runs one subscription to the hub's user events.
	hubSession hubSessionFunc
	// workerSource returns what the notify watch of a Worker reads.
	workerSource func(workerID string) workerFeedSource
}

// eventStream merges the hub's user events with the notify watch of each
// Worker that hosts a tab in scope, and prints each event.
//
// One goroutine owns every field: run reads the input of each source from a
// channel, so no lock guards the state. The hub's frames keep the CRDT state
// of the scope, and its projection gives the tabs. A tab that joins the scope
// joins the interest of its Worker's watch, which starts when the Worker
// hosts its first tab in scope and stops when it hosts none.
type eventStream struct {
	eventStreamConfig

	// ctx is the context of the run. Every feed derives its own from it.
	ctx context.Context
	// wg counts the goroutines of the run: the hub source and every feed.
	wg *sync.WaitGroup
	// feedOut carries the output of every feed.
	feedOut chan feedOutput

	// state is the CRDT state of the scope, nil until the first snapshot.
	state *leapmuxv1.UserCrdtState
	// tabs is the projection of state: each live tab of the scope.
	tabs map[string]scopedTab
	// workspaces holds each workspace of the scope that the stream knows.
	// Only the lifecycle frames and a snapshot change it, so a workspace
	// that a batch creates is announced once, by its frame with the title.
	workspaces map[string]bool
	hub        sourceHealth
	feeds      map[string]*feedState
	agents     map[string]*agentTrack
	terminals  map[string]*terminalTrack
	// rejected holds the reason that a Worker gave for each tab that it
	// refused to watch, so a repeated refusal prints once.
	rejected map[string]leapmuxv1.WatchRejectionReason
	// ready reports that stream.ready printed.
	ready bool
}

// scopedTab is one tab of the scope, as the projection places it.
type scopedTab struct {
	ID          string
	Type        leapmuxv1.TabType
	WorkspaceID string
	WorkerID    string
}

// sourceHealth is whether a source delivers.
type sourceHealth struct {
	// live reports that the source delivers now.
	live bool
	// everLive reports that the source delivered once.
	everLive bool
	// down reports that stream.unavailable printed for the current outage,
	// and since is when the source stopped delivering.
	down  bool
	since time.Time
}

// feedState is the notify watch of one Worker.
type feedState struct {
	feed   *workerFeed
	cancel context.CancelFunc
	// interest is the agents and terminals of the scope on the Worker.
	interest watchInterest
	health   sourceHealth
	// started is when the watch started.
	started time.Time
	// settled reports that the watch delivered once or printed
	// stream.unavailable, so stream.ready need not wait for it.
	settled bool
	// refused reports that the Worker refused the watch for good.
	refused bool
}

// agentTrack is what the stream last printed, or read, for one agent.
type agentTrack struct {
	// activity and status are unspecified while the stream knows neither.
	activity leapmuxv1.AgentActivityState
	status   leapmuxv1.AgentStatus
	// requests holds the handle of each request that agent.request printed
	// and that did not end.
	requests map[string]bool
	// todos and tasks are the keys of the last to-do list and the last
	// background tasks, so an unchanged snapshot prints nothing.
	todos, tasks string
	// announce marks an agent whose tab joined the scope after stream.ready:
	// the stream prints its first state, which no reader of the state saw.
	announce bool
}

// terminalTrack is what the stream last printed for one terminal.
type terminalTrack struct {
	status leapmuxv1.TerminalStatus
	title  string
	// titled reports that title holds a title that the stream printed.
	titled bool
}

func newEventStream(cfg eventStreamConfig) *eventStream {
	return &eventStream{
		eventStreamConfig: cfg,
		tabs:              map[string]scopedTab{},
		workspaces:        map[string]bool{},
		feeds:             map[string]*feedState{},
		agents:            map[string]*agentTrack{},
		terminals:         map[string]*terminalTrack{},
		rejected:          map[string]leapmuxv1.WatchRejectionReason{},
	}
}

// run prints events until ctx ends, --limit printed its last event, or the
// workspace that --workspace selects is deleted, which all return nil. It
// returns the failure that ended the hub's stream for good. It stops every
// goroutine that it started before it returns.
func (s *eventStream) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	s.ctx, s.wg = ctx, &wg
	s.feedOut = make(chan feedOutput, 64)
	hubIn := make(chan hubInput)
	wg.Go(func() { runHubSource(ctx, s.hubSession, s.clock, hubIn) })

	var beat <-chan time.Time
	if s.heartbeat > 0 {
		ticker := s.clock.NewTicker(s.heartbeat, eventHeartbeatTimerTag)
		defer ticker.Stop(eventHeartbeatTimerTag)
		beat = ticker.C
	}
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case in := <-hubIn:
			err = s.onHub(in)
		case out := <-s.feedOut:
			err = s.onFeed(out)
		case <-beat:
			err = s.emit(streamEvent{Kind: kindStreamHeartbeat, Summary: s.heartbeatSummary(), Scope: s.scopeCounts()})
		}
		if errors.Is(err, errStreamDone) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// now is the time of the stream's clock.
func (s *eventStream) now() time.Time { return s.clock.Now() }

// emit stamps ev with the time and prints it.
func (s *eventStream) emit(ev streamEvent) error {
	ev.Time = s.now().UTC()
	return s.printer.print(ev)
}

// emitAll prints each event in order, and stops at the first failure.
func (s *eventStream) emitAll(events []streamEvent) error {
	for _, ev := range events {
		if err := s.emit(ev); err != nil {
			return err
		}
	}
	return nil
}

// --- The hub ---

func (s *eventStream) onHub(in hubInput) error {
	switch {
	case in.fatal != nil:
		return in.fatal
	case in.down:
		return s.hubDown(in.err)
	default:
		return s.onHubEvent(in.event)
	}
}

// hubDown reports that the hub's stream ended. The source reconnects by
// itself; one stream.unavailable states the outage.
func (s *eventStream) hubDown(err error) error {
	s.hub.live = false
	if s.hub.down {
		return nil
	}
	s.hub.down, s.hub.since = true, s.now()
	if err == nil {
		err = errHubStreamEnded
	}
	return s.emit(unavailableEvent(streamEvent{Source: "hub"}, err, true))
}

// errHubStreamEnded is the reason of a hub stream that ended without one.
var errHubStreamEnded = cli.Newf(cli.CodeUnavailable, "the hub ended the stream")

func (s *eventStream) onHubEvent(evt *leapmuxv1.WatchUserEvent) error {
	switch e := evt.GetEvent().(type) {
	case *leapmuxv1.WatchUserEvent_Initial:
		return s.onSnapshot(e.Initial)
	case *leapmuxv1.WatchUserEvent_Delta:
		// The stream sends no resume cursor, so the hub sends no delta. A
		// delta still states the frames since a point that the stream holds,
		// so it applies as a live sequence would.
		if err := s.hubLiveAgain(); err != nil {
			return err
		}
		for _, frame := range e.Delta.GetFrames() {
			if err := s.onHubEvent(frame); err != nil {
				return err
			}
		}
		return nil
	case *leapmuxv1.WatchUserEvent_Batch:
		if s.state != nil {
			for _, op := range e.Batch.GetOps() {
				crdt.Apply(s.state, op)
			}
		}
		return nil
	case *leapmuxv1.WatchUserEvent_EntityMaterialized:
		if s.state != nil {
			putEntity(s.state, e.EntityMaterialized)
		}
		return nil
	case *leapmuxv1.WatchUserEvent_EntityRemoved:
		if s.state != nil {
			removeEntity(s.state, e.EntityRemoved)
		}
		return nil
	case *leapmuxv1.WatchUserEvent_BatchEnd:
		return s.reproject()
	case *leapmuxv1.WatchUserEvent_Created:
		return s.workspaceCreated(e.Created)
	case *leapmuxv1.WatchUserEvent_Renamed:
		return s.workspaceRenamed(e.Renamed)
	case *leapmuxv1.WatchUserEvent_Deleted:
		return s.workspaceDeleted(e.Deleted.GetWorkspaceId(), false)
	case *leapmuxv1.WatchUserEvent_WorkersChanged:
		// A Worker registered, connected, disconnected or deregistered. A
		// Worker that the stream cannot reach may be online now.
		for _, fs := range s.feeds {
			if fs.health.down && !fs.refused {
				fs.feed.wakeUp()
			}
		}
		return nil
	case *leapmuxv1.WatchUserEvent_Presence:
		return nil
	default:
		return nil
	}
}

// hubLiveAgain marks the hub live, and prints stream.gap when it was down.
func (s *eventStream) hubLiveAgain() error {
	wasDown := s.hub.down
	since := s.hub.since
	s.hub = sourceHealth{live: true, everLive: true}
	if !wasDown {
		return nil
	}
	return s.emit(gapEvent(streamEvent{Source: "hub"}, s.now(), since, lostKinds(sourceHub, false), nil))
}

// onSnapshot takes the state of the scope. The first snapshot is the start
// of the stream and prints nothing. A later one follows a reconnect, so the
// stream prints what changed during the gap.
func (s *eventStream) onSnapshot(m *leapmuxv1.UserMaterialized) error {
	first := s.state == nil
	s.state = crdtStateOf(m)
	snapshotWorkspaces := map[string]bool{}
	for id := range m.GetWorkspaces() {
		if s.inScope(id) {
			snapshotWorkspaces[id] = true
		}
	}
	if first {
		if s.workspace != "" && !snapshotWorkspaces[s.workspace] {
			return cli.Newf(cli.CodeNotFound, "no such workspace: %s", s.workspace)
		}
		s.hub = sourceHealth{live: true, everLive: true}
		s.workspaces = snapshotWorkspaces
		s.tabs = s.project()
		s.syncFeeds()
		return s.maybeReady()
	}
	if err := s.hubLiveAgain(); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(snapshotWorkspaces)) {
		if s.workspaces[id] {
			continue
		}
		s.workspaces[id] = true
		if err := s.emit(streamEvent{Kind: kindWorkspaceCreated, WorkspaceID: id, Summary: "created", Reconciled: true}); err != nil {
			return err
		}
	}
	if err := s.applyProjection(true); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(s.workspaces)) {
		if snapshotWorkspaces[id] {
			continue
		}
		if err := s.workspaceDeleted(id, true); err != nil {
			return err
		}
	}
	return nil
}

// reproject compares the projection after a batch with the one before it.
func (s *eventStream) reproject() error {
	if s.state == nil {
		return nil
	}
	return s.applyProjection(false)
}

// applyProjection takes the projection of the state, prints how its tabs
// differ from the ones that the stream held, and revises the watch of each
// Worker. A watch that it stops may be the last one that stream.ready waits
// for.
func (s *eventStream) applyProjection(reconciled bool) error {
	next := s.project()
	events := s.tabEvents(s.tabs, next, reconciled)
	for id := range s.tabs {
		if _, kept := next[id]; !kept {
			s.forgetTab(id)
		}
	}
	for id, tab := range next {
		if _, held := s.tabs[id]; !held && s.ready && tab.Type == leapmuxv1.TabType_TAB_TYPE_AGENT {
			s.agentTrack(id).announce = true
		}
	}
	s.tabs = next
	s.syncFeeds()
	if err := s.emitAll(events); err != nil {
		return err
	}
	return s.maybeReady()
}

// project lists the live tabs of the state that are in scope.
func (s *eventStream) project() map[string]scopedTab {
	out := map[string]scopedTab{}
	for _, t := range crdt.Project(s.state).OwnedTabs {
		if !s.inScope(t.WorkspaceID) {
			continue
		}
		out[t.TabID] = scopedTab{ID: t.TabID, Type: t.TabType, WorkspaceID: t.WorkspaceID, WorkerID: t.WorkerID}
	}
	return out
}

// inScope reports whether the stream watches the workspace id.
func (s *eventStream) inScope(workspaceID string) bool {
	return s.workspace == "" || workspaceID == s.workspace
}

// tabEvents states how the tabs of next differ from the ones of prev, in the
// order of the tab ids.
//
// A tab that left the projection closed when the state still holds its
// record: a close tombstones the record, and a tab whose tile went away
// keeps it. A tab whose record left the state moved out of the scope, which
// only entity_removed does. A snapshot after a gap holds no record of a
// closed tab, so a reconciled tab that left reads as closed: the snapshot
// does not say whether it closed or moved to a workspace outside the scope.
func (s *eventStream) tabEvents(prev, next map[string]scopedTab, reconciled bool) []streamEvent {
	ids := slices.Sorted(maps.Keys(prev))
	for id := range next {
		if _, held := prev[id]; !held {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out []streamEvent
	for _, id := range ids {
		before, wasIn := prev[id]
		after, isIn := next[id]
		switch {
		case !wasIn:
			ev := tabStreamEvent(kindTabOpened, after)
			ev.Summary, ev.Reconciled = tabTypeName(after.Type)+" tab", reconciled
			out = append(out, ev)
		case !isIn:
			_, recorded := s.state.GetTabs()[id]
			if reconciled || recorded {
				ev := tabStreamEvent(kindTabClosed, before)
				ev.Summary, ev.Reconciled = tabTypeName(before.Type)+" tab", reconciled
				out = append(out, ev)
				continue
			}
			ev := tabStreamEvent(kindTabMoved, before)
			ev.WorkspaceID, ev.FromWorkspaceID = "", before.WorkspaceID
			ev.Summary = tabTypeName(before.Type) + " tab left the watched workspace"
			out = append(out, ev)
		case before.WorkspaceID != after.WorkspaceID:
			ev := tabStreamEvent(kindTabMoved, after)
			ev.FromWorkspaceID, ev.Reconciled = before.WorkspaceID, reconciled
			ev.Summary = fmt.Sprintf("%s tab from workspace %s", tabTypeName(after.Type), cli.ShortID(before.WorkspaceID))
			out = append(out, ev)
		}
	}
	return out
}

// tabStreamEvent is an event of kind about tab.
func tabStreamEvent(kind string, tab scopedTab) streamEvent {
	return streamEvent{
		Kind:        kind,
		WorkspaceID: tab.WorkspaceID,
		TabID:       tab.ID,
		TabType:     tabTypeName(tab.Type),
		WorkerID:    tab.WorkerID,
	}
}

// forgetTab drops what the stream held for a tab that left the scope.
func (s *eventStream) forgetTab(id string) {
	delete(s.agents, id)
	delete(s.terminals, id)
	delete(s.rejected, id)
}

func (s *eventStream) workspaceCreated(c *leapmuxv1.WorkspaceCreated) error {
	id := c.GetWorkspaceId()
	if id == "" || !s.inScope(id) || s.workspaces[id] {
		return nil
	}
	s.workspaces[id] = true
	return s.emit(streamEvent{Kind: kindWorkspaceCreated, WorkspaceID: id, Title: c.GetTitle(), Summary: fmt.Sprintf("%q", c.GetTitle())})
}

func (s *eventStream) workspaceRenamed(r *leapmuxv1.WorkspaceRenamed) error {
	id := r.GetWorkspaceId()
	if id == "" || !s.inScope(id) {
		return nil
	}
	return s.emit(streamEvent{Kind: kindWorkspaceRenamed, WorkspaceID: id, Title: r.GetTitle(), Summary: fmt.Sprintf("to %q", r.GetTitle())})
}

// workspaceDeleted prints workspace.deleted once, and ends the stream when
// the workspace is the one that --workspace selects: nothing can happen in
// it any more.
func (s *eventStream) workspaceDeleted(id string, reconciled bool) error {
	if !s.workspaces[id] {
		return nil
	}
	delete(s.workspaces, id)
	if err := s.emit(streamEvent{Kind: kindWorkspaceDeleted, WorkspaceID: id, Summary: "deleted", Reconciled: reconciled}); err != nil {
		return err
	}
	if id == s.workspace {
		return errStreamDone
	}
	return nil
}

// --- The Workers ---

// syncFeeds gives each Worker that hosts a tab of the scope a watch of
// exactly those tabs, and stops the watch of a Worker that hosts none.
func (s *eventStream) syncFeeds() {
	want := map[string]*watchInterest{}
	for _, tab := range s.tabs {
		if tab.WorkerID == "" {
			continue
		}
		interest := want[tab.WorkerID]
		if interest == nil {
			interest = &watchInterest{}
			want[tab.WorkerID] = interest
		}
		switch tab.Type {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			interest.agents = append(interest.agents, tab.ID)
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			interest.terminals = append(interest.terminals, tab.ID)
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			// A file or an image tab is a viewer: its Worker sends no event
			// about it.
		}
	}
	for workerID, fs := range s.feeds {
		if interest := want[workerID]; interest == nil || interest.empty() {
			fs.cancel()
			delete(s.feeds, workerID)
		}
	}
	for _, workerID := range slices.Sorted(maps.Keys(want)) {
		interest := want[workerID]
		if interest.empty() {
			continue
		}
		slices.Sort(interest.agents)
		slices.Sort(interest.terminals)
		fs := s.feeds[workerID]
		if fs == nil {
			s.startFeed(workerID, *interest)
			continue
		}
		if fs.interest.equal(*interest) {
			continue
		}
		fs.interest = *interest
		if !fs.refused {
			fs.feed.setInterest(*interest)
		}
	}
}

// startFeed starts the watch of one Worker.
func (s *eventStream) startFeed(workerID string, interest watchInterest) {
	ctx, cancel := context.WithCancel(s.ctx)
	feed := newWorkerFeed(workerID, s.workerSource(workerID), s.feedOut)
	feed.setInterest(interest)
	s.feeds[workerID] = &feedState{
		feed:     feed,
		cancel:   cancel,
		interest: interest,
		started:  s.now(),
		// A watch that starts after stream.ready cannot hold it back.
		settled: s.ready,
	}
	feed.start(ctx, s.wg)
}

func (s *eventStream) onFeed(out feedOutput) error {
	fs := s.feeds[out.feed.workerID]
	if fs == nil || fs.feed != out.feed {
		// The output of a watch that the stream stopped.
		return nil
	}
	switch out.kind {
	case feedAgentEvent:
		return s.onAgentEvent(fs, out.agent)
	case feedTerminalEvent:
		return s.onTerminalEvent(fs, out.terminal)
	case feedLevels:
		return s.onLevels(fs, out)
	case feedDown:
		return s.feedDown(fs, out.err)
	case feedRefused:
		return s.feedRefused(fs, out.err)
	case feedRejected:
		return s.onRejection(fs, out.rejection, out.rejectedTerminal)
	}
	return nil
}

// workerEvent is a stream event about the Worker of fs.
func workerEvent(fs *feedState) streamEvent {
	return streamEvent{Source: "worker", WorkerID: fs.feed.workerID}
}

// feedDown reports that a Worker's watch cannot deliver now. The watch
// retries by itself; one stream.unavailable states the outage.
func (s *eventStream) feedDown(fs *feedState, err error) error {
	fs.settled = true
	fs.health.live = false
	if fs.health.down {
		return s.maybeReady()
	}
	// A watch that never delivered lost everything since it started.
	fs.health.down, fs.health.since = true, fs.started
	if fs.health.everLive {
		fs.health.since = s.now()
	}
	if err := s.emit(unavailableEvent(workerEvent(fs), err, true)); err != nil {
		return err
	}
	return s.maybeReady()
}

// feedRefused reports that a Worker refused the watch for good. Its tabs
// stay in the scope, without the events of the Worker.
func (s *eventStream) feedRefused(fs *feedState, err error) error {
	fs.settled, fs.refused = true, true
	fs.health.live = false
	if err := s.emit(unavailableEvent(workerEvent(fs), err, false)); err != nil {
		return err
	}
	return s.maybeReady()
}

// onLevels takes the state of agents that a Worker reported after it
// acknowledged a watch. The first levels of a session mark the watch live,
// and print stream.gap when it was down.
//
// The state of an agent that the stream does not know yet is its baseline,
// which prints only for an agent whose tab joined after stream.ready. After
// a gap, the stream printed nothing of what changed meanwhile, so each state
// that differs from the one that it held prints as a reconciled event.
// Within one session the events are complete, so a level never overrides a
// known state there: a change that arrived during the read follows the
// level, and it states the newer value.
func (s *eventStream) onLevels(fs *feedState, out feedOutput) error {
	if out.first {
		wasDown, since := fs.health.down, fs.health.since
		fs.health = sourceHealth{live: true, everLive: true}
		fs.settled = true
		if wasDown {
			lost := lostKinds(sourceWorker, out.levelsErr != nil)
			if err := s.emit(gapEvent(workerEvent(fs), s.now(), since, lost, fs.interest.tabIDs())); err != nil {
				return err
			}
		}
	}
	if out.levelsErr == nil {
		reconcile := out.first && out.session > 1
		for _, id := range slices.Sorted(maps.Keys(out.levels)) {
			if err := s.agentLevel(id, out.levels[id], reconcile); err != nil {
				return err
			}
		}
	}
	return s.maybeReady()
}

// agentLevel takes the state of one agent that a level read reported.
func (s *eventStream) agentLevel(id string, info *leapmuxv1.AgentInfo, reconcile bool) error {
	tab, ok := s.tabs[id]
	if !ok || tab.Type != leapmuxv1.TabType_TAB_TYPE_AGENT {
		return nil
	}
	track := s.agentTrack(id)
	announce := track.announce
	track.announce = false
	var events []streamEvent
	if status := info.GetStatus(); status != leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED {
		known := track.status != leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED
		if (!known && announce) || (known && reconcile && status != track.status) {
			ev := agentStatusEvent(tab, status, info.GetStartupError())
			ev.Reconciled = known
			events = append(events, ev)
		}
		if !known || reconcile {
			track.status = status
		}
	}
	if state := info.GetPublishedActivityState(); state != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED {
		known := track.activity != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
		if (!known && announce) || (known && reconcile && state != track.activity) {
			ev := agentActivityEvent(tab, state, nil)
			ev.Reconciled = known
			events = append(events, ev)
		}
		if !known || reconcile {
			track.activity = state
		}
	}
	return s.emitAll(events)
}

func (s *eventStream) agentTrack(id string) *agentTrack {
	track := s.agents[id]
	if track == nil {
		track = &agentTrack{requests: map[string]bool{}}
		s.agents[id] = track
	}
	return track
}

func (s *eventStream) terminalTrack(id string) *terminalTrack {
	track := s.terminals[id]
	if track == nil {
		track = &terminalTrack{}
		s.terminals[id] = track
	}
	return track
}

// onAgentEvent prints one event of an agent of the scope.
func (s *eventStream) onAgentEvent(fs *feedState, ae *leapmuxv1.AgentEvent) error {
	tab, ok := s.tabs[ae.GetAgentId()]
	if !ok || tab.Type != leapmuxv1.TabType_TAB_TYPE_AGENT || tab.WorkerID != fs.feed.workerID {
		return nil
	}
	track := s.agentTrack(tab.ID)
	switch e := ae.GetEvent().(type) {
	case *leapmuxv1.AgentEvent_ActivityChanged:
		state := e.ActivityChanged.GetState()
		// The Worker can repeat the state that it sent last, after a
		// restart: a transition is not news.
		if state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED || state == track.activity {
			return nil
		}
		track.activity, track.announce = state, false
		var toolCalls *int32
		if e.ActivityChanged.NumToolUses != nil {
			n := e.ActivityChanged.GetNumToolUses()
			toolCalls = &n
		}
		return s.emit(agentActivityEvent(tab, state, toolCalls))
	case *leapmuxv1.AgentEvent_StatusChange:
		status := e.StatusChange.GetStatus()
		if status == leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED || status == track.status {
			return nil
		}
		track.status, track.announce = status, false
		return s.emit(agentStatusEvent(tab, status, e.StatusChange.GetStartupError()))
	case *leapmuxv1.AgentEvent_TurnEnd:
		ev := tabStreamEvent(kindAgentTurnEnd, tab)
		ev.Summary = "the turn ended"
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_ControlRequest:
		req := e.ControlRequest
		handle := req.GetClaimToken()
		// The Worker announces a request again while it waits, and while an
		// answer to it is in delivery. Only a waiting instance that the
		// stream did not print is new.
		if handle == "" || track.requests[handle] || req.GetResponseState() != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
			return nil
		}
		track.requests[handle] = true
		return s.emit(agentRequestEvent(tab, req))
	case *leapmuxv1.AgentEvent_ControlCancel:
		cancel := e.ControlCancel
		delete(track.requests, cancel.GetClaimToken())
		ev := tabStreamEvent(kindAgentRequestEnded, tab)
		ev.Request, ev.RequestID = cancel.GetClaimToken(), cancel.GetRequestId()
		ev.State = controlResponseStateToken(cancel.GetResponseState())
		ev.Summary = fmt.Sprintf("request %s ended (%s)", cli.ShortID(cancel.GetClaimToken()), tokenWords(ev.State))
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_TodosChanged:
		key, counts, current := todoSummary(e.TodosChanged.GetTodos())
		if key == track.todos {
			return nil
		}
		track.todos = key
		ev := tabStreamEvent(kindAgentTodos, tab)
		ev.Todos = &counts
		ev.Summary = fmt.Sprintf("%d of %d done", counts.Completed, counts.Total)
		if current != "" {
			ev.Summary += "; in progress: " + current
		}
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_BackgroundTasksChanged:
		key, counts := taskSummary(e.BackgroundTasksChanged.GetTasks())
		if key == track.tasks {
			return nil
		}
		track.tasks = key
		ev := tabStreamEvent(kindAgentBackgroundTasks, tab)
		ev.Tasks = &counts
		ev.Summary = fmt.Sprintf("%d active of %d", counts.Active, counts.Total)
		return s.emit(ev)
	default:
		return nil
	}
}

// onTerminalEvent prints one event of a terminal of the scope.
func (s *eventStream) onTerminalEvent(fs *feedState, te *leapmuxv1.TerminalEvent) error {
	tab, ok := s.tabs[te.GetTerminalId()]
	if !ok || tab.Type != leapmuxv1.TabType_TAB_TYPE_TERMINAL || tab.WorkerID != fs.feed.workerID {
		return nil
	}
	track := s.terminalTrack(tab.ID)
	switch e := te.GetEvent().(type) {
	case *leapmuxv1.TerminalEvent_StatusChange:
		status := e.StatusChange.GetStatus()
		if status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED || status == track.status {
			return nil
		}
		track.status = status
		ev := tabStreamEvent(kindTerminalStatus, tab)
		ev.Status, ev.Error = enumToken(status), e.StatusChange.GetStartupError()
		ev.Summary = withError(tokenWords(ev.Status), ev.Error)
		return s.emit(ev)
	case *leapmuxv1.TerminalEvent_Closed:
		track.status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED
		ev := tabStreamEvent(kindTerminalExit, tab)
		if e.Closed.ExitCode == nil {
			// An unset code is an exit that the Worker holds no record of; a 0
			// would claim a clean exit that it never saw.
			ev.Summary = "the shell exited; the Worker holds no record of its exit code"
			return s.emit(ev)
		}
		code := e.Closed.GetExitCode()
		ev.ExitCode = &code
		ev.Summary = fmt.Sprintf("the shell exited with code %d", code)
		return s.emit(ev)
	case *leapmuxv1.TerminalEvent_Bell:
		ev := tabStreamEvent(kindTerminalBell, tab)
		ev.Summary = "bell"
		return s.emit(ev)
	case *leapmuxv1.TerminalEvent_Notification:
		ev := tabStreamEvent(kindTerminalNotification, tab)
		ev.Title, ev.Body = e.Notification.GetTitle(), e.Notification.GetBody()
		ev.Summary = ev.Body
		if ev.Title != "" {
			ev.Summary = ev.Title + ": " + ev.Body
		}
		return s.emit(ev)
	case *leapmuxv1.TerminalEvent_TitleChanged:
		title := e.TitleChanged.GetTitle()
		if track.titled && title == track.title {
			return nil
		}
		track.title, track.titled = title, true
		ev := tabStreamEvent(kindTerminalTitle, tab)
		ev.Title, ev.Summary = title, fmt.Sprintf("%q", title)
		return s.emit(ev)
	case *leapmuxv1.TerminalEvent_Progress:
		report := progressReport{State: enumToken(e.Progress.GetState()), Percent: e.Progress.GetPercent()}
		ev := tabStreamEvent(kindTerminalProgress, tab)
		ev.Progress, ev.Summary = &report, progressSummary(report)
		return s.emit(ev)
	default:
		return nil
	}
}

// onRejection reports a tab that a Worker refused to watch, once for each
// reason. A failed lookup is retried by the subscription, so it prints
// nothing.
func (s *eventStream) onRejection(fs *feedState, rejection *leapmuxv1.WatchRejection, terminal bool) error {
	tab, ok := s.tabs[rejection.GetEntityId()]
	if !ok || tab.WorkerID != fs.feed.workerID {
		return nil
	}
	reason := rejection.GetReason()
	if reason == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED || s.rejected[tab.ID] == reason {
		return nil
	}
	s.rejected[tab.ID] = reason
	noun := "agent"
	if terminal {
		noun = "terminal"
	}
	var err error
	switch reason {
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND:
		err = cli.Newf(cli.CodeNotFound, "the worker holds no %s %s", noun, tab.ID)
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN:
		err = cli.Newf(cli.CodePermissionDenied, "the credential may not watch %s %s", noun, tab.ID)
	default:
		// The protocol reads an unknown reason as durable.
		err = cli.Newf(cli.CodeInternal, "the worker refused to watch %s %s (%s)", noun, tab.ID, reason)
	}
	ev := tabStreamEvent(kindStreamUnavailable, tab)
	ev.Source = "worker"
	return s.emit(unavailableEvent(ev, err, false))
}

// --- Readiness ---

// maybeReady prints stream.ready once the hub delivered its first snapshot
// and each Worker that hosted a tab of the scope then delivered once or
// printed stream.unavailable.
func (s *eventStream) maybeReady() error {
	if s.ready || s.state == nil {
		return nil
	}
	for _, fs := range s.feeds {
		if !fs.settled {
			return nil
		}
	}
	s.ready = true
	scope := s.scopeCounts()
	summary := fmt.Sprintf("watching %s and %s on %s",
		pluralize(scope.Workspaces, "workspace"), pluralize(scope.Tabs, "tab"), pluralize(scope.Workers, "worker"))
	if down := scope.Workers - scope.WorkersLive; down > 0 {
		summary += fmt.Sprintf("; %d of them cannot deliver now", down)
	}
	return s.emit(streamEvent{Kind: kindStreamReady, Summary: summary, Scope: scope})
}

// scopeCounts counts what the stream watches now.
func (s *eventStream) scopeCounts() *scopeCounts {
	counts := &scopeCounts{Workspaces: len(s.workspaces), Tabs: len(s.tabs), Workers: len(s.feeds), HubLive: s.hub.live}
	for _, fs := range s.feeds {
		if fs.health.live {
			counts.WorkersLive++
		}
	}
	return counts
}

func (s *eventStream) heartbeatSummary() string {
	scope := s.scopeCounts()
	hub := "the hub delivers"
	if !scope.HubLive {
		hub = "the hub cannot deliver now"
	}
	return fmt.Sprintf("%s; %d of %s deliver", hub, scope.WorkersLive, pluralize(scope.Workers, "worker"))
}

// --- Event builders ---

func agentActivityEvent(tab scopedTab, state leapmuxv1.AgentActivityState, toolCalls *int32) streamEvent {
	ev := tabStreamEvent(kindAgentActivity, tab)
	ev.State, ev.ToolCalls = enumToken(state), toolCalls
	ev.Summary = tokenWords(ev.State)
	if toolCalls != nil {
		ev.Summary += fmt.Sprintf(" (%s)", pluralize(int(*toolCalls), "tool call"))
	}
	return ev
}

func agentStatusEvent(tab scopedTab, status leapmuxv1.AgentStatus, startupError string) streamEvent {
	ev := tabStreamEvent(kindAgentStatus, tab)
	ev.Status, ev.Error = enumToken(status), startupError
	ev.Summary = withError(tokenWords(ev.Status), startupError)
	return ev
}

// promptDetailLength caps the detail of a request in its summary. The whole
// request is what `agent requests` prints.
const promptDetailLength = 120

func agentRequestEvent(tab scopedTab, req *leapmuxv1.AgentControlRequest) streamEvent {
	ev := tabStreamEvent(kindAgentRequest, tab)
	ev.Request, ev.RequestID = req.GetClaimToken(), req.GetRequestId()
	ev.Prompt = controlPromptKind(req.GetPrompt())
	ev.Blocking = boolPtr(!req.GetNonBlocking())
	ev.Summary = ev.Prompt
	if detail := truncateRunes(oneLine(promptDetail(req.GetPrompt())), promptDetailLength); detail != "" {
		ev.Summary += ": " + detail
	}
	if req.GetNonBlocking() {
		ev.Summary += " (the agent goes on working)"
	}
	return ev
}

// promptDetail is the first text of a request that says what it asks.
func promptDetail(prompt *leapmuxv1.ControlPrompt) string {
	switch k := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		return firstNonEmpty(k.Permission.GetTitle(), k.Permission.GetCommand(), k.Permission.GetText())
	case *leapmuxv1.ControlPrompt_Plan:
		return "approve the plan"
	case *leapmuxv1.ControlPrompt_Question:
		questions := k.Question.GetQuestions()
		if len(questions) == 0 {
			return ""
		}
		text := firstNonEmpty(questions[0].GetQuestion(), questions[0].GetHeader())
		if more := len(questions) - 1; more > 0 {
			text += fmt.Sprintf(" (and %s)", pluralize(more, "more question"))
		}
		return text
	case *leapmuxv1.ControlPrompt_Elicitation:
		return firstNonEmpty(k.Elicitation.GetMessage(), k.Elicitation.GetTitle(), k.Elicitation.GetDescription())
	case *leapmuxv1.ControlPrompt_Dialog:
		return firstNonEmpty(k.Dialog.GetTitle(), k.Dialog.GetMessage())
	case *leapmuxv1.ControlPrompt_Unreadable:
		return fmt.Sprintf("LeapMux cannot read it (%s)", enumToken(k.Unreadable))
	default:
		return ""
	}
}

// todoSummary counts the items of a to-do list. A deleted item does not
// count. key changes when an item or its status changes, and it is never
// empty, so an empty list differs from the list that the stream does not
// know yet. current is the first item in progress.
func todoSummary(items []*leapmuxv1.TodoItem) (key string, counts todoCounts, current string) {
	var b strings.Builder
	fmt.Fprintf(&b, "%d;", len(items))
	for _, item := range items {
		fmt.Fprintf(&b, "%q:%d;", item.GetContent(), item.GetStatus())
		switch item.GetStatus() {
		case leapmuxv1.TodoStatus_TODO_STATUS_DELETED:
			continue
		case leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED:
			counts.Completed++
		case leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS:
			counts.InProgress++
			if current == "" {
				current = firstNonEmpty(item.GetActiveForm(), item.GetContent())
			}
		case leapmuxv1.TodoStatus_TODO_STATUS_UNSPECIFIED, leapmuxv1.TodoStatus_TODO_STATUS_PENDING, leapmuxv1.TodoStatus_TODO_STATUS_BLOCKED:
		}
		counts.Total++
	}
	return b.String(), counts, current
}

// taskSummary counts the background tasks of an agent. A task is active
// until it states when it ended. key changes when a task or its status
// changes, and not when its activity text does. Like the key of
// todoSummary, it is never empty.
func taskSummary(tasks []*leapmuxv1.BackgroundTaskItem) (key string, counts taskCounts) {
	var b strings.Builder
	fmt.Fprintf(&b, "%d;", len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(&b, "%q:%d;", task.GetId(), task.GetStatus())
		counts.Total++
		if task.GetEndedAt() == "" {
			counts.Active++
		}
	}
	return b.String(), counts
}

func progressSummary(report progressReport) string {
	switch report.State {
	case "":
		return "cleared"
	case enumToken(leapmuxv1.TerminalProgress_STATE_INDETERMINATE):
		return "in progress"
	case enumToken(leapmuxv1.TerminalProgress_STATE_NORMAL):
		return fmt.Sprintf("%d%%", report.Percent)
	default:
		return fmt.Sprintf("%s at %d%%", report.State, report.Percent)
	}
}

// unavailableEvent completes ev as a stream.unavailable for err.
func unavailableEvent(ev streamEvent, err error, retrying bool) streamEvent {
	e := cli.FromError(inBandError(err))
	ev.Kind, ev.Code, ev.Message, ev.Retrying = kindStreamUnavailable, string(e.Code), e.Message, boolPtr(retrying)
	if retrying {
		ev.Summary = e.Message + "; retrying"
	} else {
		ev.Summary = e.Message + "; the stream does not try again"
	}
	return ev
}

// gapEvent completes ev as the stream.gap of a source that stopped
// delivering at since and delivers again at until.
func gapEvent(ev streamEvent, until, since time.Time, lost, tabIDs []string) streamEvent {
	ev.Kind = kindStreamGap
	sinceUTC := since.UTC()
	ev.Since = &sinceUTC
	ev.Lost, ev.TabIDs = lost, tabIDs
	ev.Summary = fmt.Sprintf("delivers again after a gap of %s", until.Sub(since).Round(time.Millisecond))
	if len(lost) > 0 {
		ev.Summary += "; these events may be lost: " + strings.Join(lost, ", ")
	}
	return ev
}

// --- Small helpers ---

func withError(text, err string) string {
	if err == "" {
		return text
	}
	return text + ": " + err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncateRunes cuts text to limit characters, the last of them an
// ellipsis.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// crdtStateOf makes a CRDT state of a snapshot, so the batches that follow
// apply to it as the hub applies them. The state takes the maps of m, which
// the stream does not read again.
func crdtStateOf(m *leapmuxv1.UserMaterialized) *leapmuxv1.UserCrdtState {
	state := crdt.NewState(m.GetUserId())
	maps.Copy(state.Nodes, m.GetNodes())
	maps.Copy(state.Tabs, m.GetTabs())
	maps.Copy(state.FloatingWindows, m.GetFloatingWindows())
	maps.Copy(state.Workspaces, m.GetWorkspaces())
	if m.GetMaxHlc() != nil {
		state.MaxHlc = m.GetMaxHlc()
	}
	state.CurrentEpoch = m.GetCurrentEpoch()
	return state
}

// putEntity replaces the record of an entity that entered the scope.
func putEntity(state *leapmuxv1.UserCrdtState, em *leapmuxv1.EntityMaterialized) {
	switch e := em.GetEntity().(type) {
	case *leapmuxv1.EntityMaterialized_Tab:
		if id := e.Tab.GetTabId(); id != "" {
			state.Tabs[id] = e.Tab
		}
	case *leapmuxv1.EntityMaterialized_Node:
		if id := e.Node.GetNodeId(); id != "" {
			state.Nodes[id] = e.Node
		}
	case *leapmuxv1.EntityMaterialized_FloatingWindow:
		if id := e.FloatingWindow.GetWindowId(); id != "" {
			state.FloatingWindows[id] = e.FloatingWindow
		}
	}
}

// removeEntity drops the record of an entity that left the scope.
func removeEntity(state *leapmuxv1.UserCrdtState, er *leapmuxv1.EntityRemoved) {
	switch e := er.GetEntity().(type) {
	case *leapmuxv1.EntityRemoved_Tab:
		delete(state.Tabs, e.Tab.GetTabId())
	case *leapmuxv1.EntityRemoved_NodeId:
		delete(state.Nodes, e.NodeId)
	case *leapmuxv1.EntityRemoved_WindowId:
		delete(state.FloatingWindows, e.WindowId)
	}
}

// --- The printer ---

// eventPrinter prints the events that --kind selects on stdout, until
// --limit printed its last one.
type eventPrinter struct {
	p      *cli.Printer
	filter kindFilter
	// limit ends the stream after this many events on stdout. Zero prints
	// without a limit.
	limit   int
	printed int
}

// print prints ev on stdout when --kind selects it. A stream event that
// --kind does not select prints its text line on stderr instead, so a
// filter never hides a gap from the person who watches. A heartbeat prints
// only with --heartbeat, which selects it, and --limit does not count it.
// print returns errStreamDone after the last event that --limit allows.
func (e *eventPrinter) print(ev streamEvent) error {
	if ev.Kind == kindStreamHeartbeat {
		return e.p.Event(ev)
	}
	if !e.filter.selects(ev.Kind) {
		if kindFamily(ev.Kind) == kindFamily(kindStreamReady) {
			e.p.Notice("%s", ev.TextLine())
		}
		return nil
	}
	if err := e.p.Event(ev); err != nil {
		return err
	}
	e.printed++
	if e.limit > 0 && e.printed >= e.limit {
		return errStreamDone
	}
	return nil
}
