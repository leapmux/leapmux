package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// errStreamDone ends the stream without a failure: --limit printed its last
// event, or the selected tab or workspace left the scope.
var errStreamDone = errors.New("the event stream is done")

// eventHeartbeatTimerTag identifies the heartbeat ticker to a test clock.
const (
	eventHeartbeatTimerTag = "events-heartbeat"
	eventTimeoutTimerTag   = "events-timeout"
)

// eventStreamConfig is what one run of `leapmux events` watches and
// prints.
type eventStreamConfig struct {
	printer *eventPrinter
	clock   quartz.Clock
	// workspace is the workspace that --workspace selects, or "" for every
	// workspace.
	workspace string
	// tabID selects a single tab within the workspace scope when set.
	tabID string
	// heartbeat is the interval of stream.heartbeat, or 0 for none.
	heartbeat time.Duration
	// timeout stops the stream cleanly after this long, or 0 for no limit.
	timeout time.Duration
	// snapshot exits cleanly right after stream.ready prints.
	snapshot bool
	// noReconnect stops instead of reconnecting when the Hub disconnects.
	noReconnect bool
	// hubSession runs one subscription to the hub's user events.
	hubSession hubSessionFunc
	// workerSource returns what the notify watch of a Worker reads.
	workerSource func(workerID string) workerFeedSource
	// privateSource optionally returns a WatchWorkerPrivateEvents source for
	// each watched Worker (tab.renamed, tab.status, notification.*).
	privateSource func(workerID string) workerPrivateSource
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
	// privateOut carries WatchWorkerPrivateEvents frames from watched Workers.
	privateOut chan workerPrivateOutput

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
	s.privateOut = make(chan workerPrivateOutput, 64)
	hubIn := make(chan hubInput)
	wg.Go(func() { runHubSourcePolicy(ctx, s.hubSession, s.clock, !s.noReconnect, hubIn) })

	var beat <-chan time.Time
	if s.heartbeat > 0 {
		ticker := s.clock.NewTicker(s.heartbeat, eventHeartbeatTimerTag)
		defer ticker.Stop(eventHeartbeatTimerTag)
		beat = ticker.C
	}
	var deadline <-chan time.Time
	if s.timeout > 0 {
		timer := s.clock.NewTimer(s.timeout, eventTimeoutTimerTag)
		defer timer.Stop(eventTimeoutTimerTag)
		deadline = timer.C
	}
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			return nil
		case in := <-hubIn:
			err = s.onHub(in)
		case out := <-s.feedOut:
			err = s.onFeed(out)
		case priv := <-s.privateOut:
			err = s.onPrivateEvent(priv)
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
	if s.tabID != "" && ev.TabID != s.tabID && kindFamily(ev.Kind) != "stream" {
		return nil
	}
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
		if s.tabID != "" && len(s.tabs) == 0 {
			return cli.Newf(cli.CodeNotFound, "no such tab in the event scope: %s", s.tabID)
		}
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
	leftScope := s.tabID != "" && len(s.tabs) != 0 && len(next) == 0
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
	if leftScope {
		return errStreamDone
	}
	return s.maybeReady()
}

// project lists the live tabs of the state that are in scope.
func (s *eventStream) project() map[string]scopedTab {
	out := map[string]scopedTab{}
	for _, t := range crdt.Project(s.state).OwnedTabs {
		if !s.inScope(t.WorkspaceID) || (s.tabID != "" && t.TabID != s.tabID) {
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

// workspaceDeleted reports the deletion and ends the stream if it removes
// the selected workspace or the tab that the stream follows.
func (s *eventStream) workspaceDeleted(id string, reconciled bool) error {
	if !s.workspaces[id] {
		return nil
	}
	delete(s.workspaces, id)
	if err := s.emit(streamEvent{Kind: kindWorkspaceDeleted, WorkspaceID: id, Summary: "deleted", Reconciled: reconciled}); err != nil {
		return err
	}
	if tab, selected := s.tabs[s.tabID]; selected && tab.WorkspaceID == id {
		ev := tabStreamEvent(kindTabClosed, tab)
		ev.Summary = "its workspace was deleted"
		ev.Reconciled = reconciled
		if err := s.emit(ev); err != nil {
			return err
		}
		return errStreamDone
	}
	if id == s.workspace {
		return errStreamDone
	}
	return nil
}

// --- The Workers ---

// syncFeeds gives each Worker that hosts a tab of the scope a watch of
// exactly those tabs, and stops the watch of a Worker that hosts none.
