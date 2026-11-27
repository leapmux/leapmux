package cmd

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/semanticevents"
)

func filterSelectsFamily(f kindFilter, family string) bool {
	if f.selected == nil {
		return true
	}
	for _, k := range eventKinds {
		if kindFamily(k.name) == family && f.selected[k.name] {
			return true
		}
	}
	return false
}

func filterSelectsPrivate(f kindFilter) bool {
	if f.selected == nil {
		return true
	}
	for _, kind := range privateLostKinds() {
		if f.selected[kind] {
			return true
		}
	}
	return false
}

// syncFeeds gives each Worker that hosts a tab of the scope the watch and
// private-event subscriptions that the selected kinds need, and stops feeds
// for any Worker that no longer needs them.
func (s *eventStream) syncFeeds() {
	var filter kindFilter
	if s.printer != nil {
		filter = s.printer.filter
	}
	wantAgents := filterSelectsFamily(filter, "agent")
	wantTerminals := filterSelectsFamily(filter, "terminal")
	wantPrivate := s.privateSource != nil && filterSelectsPrivate(filter)

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
			if wantAgents {
				interest.agents = append(interest.agents, tab.ID)
			}
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			if wantTerminals {
				interest.terminals = append(interest.terminals, tab.ID)
			}
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			// File and image tabs produce no WatchEvents traffic, but their
			// Worker stays in want so private events (tab.renamed, tab.status,
			// notifications) still stream when selected.
		}
	}
	for workerID, fs := range s.feeds {
		interest := want[workerID]
		needWatch := interest != nil && !interest.empty()
		needPrivate := interest != nil && wantPrivate
		if !needWatch && !needPrivate {
			s.stopWatchFeed(fs)
			s.stopPrivateFeed(fs)
			delete(s.feeds, workerID)
		}
	}
	for _, workerID := range slices.Sorted(maps.Keys(want)) {
		interest := want[workerID]
		needWatch := !interest.empty()
		needPrivate := wantPrivate
		if !needWatch && !needPrivate {
			continue
		}
		slices.Sort(interest.agents)
		slices.Sort(interest.terminals)
		fs := s.feeds[workerID]
		if fs == nil {
			fs = &feedState{workerID: workerID, settled: true}
			s.feeds[workerID] = fs
		}
		switch {
		case !needWatch && fs.feed != nil:
			s.stopWatchFeed(fs)
		case needWatch && fs.feed == nil:
			s.startWatchFeed(fs, *interest)
		case needWatch && !fs.interest.equal(*interest):
			fs.interest = *interest
			if !fs.refused {
				fs.feed.setInterest(*interest)
			}
		}
		if needPrivate && fs.private == nil {
			s.startPrivateFeed(fs)
		} else if !needPrivate && fs.private != nil {
			s.stopPrivateFeed(fs)
		}
	}
}

// startWatchFeed starts the WatchEvents subscription for one Worker.
func (s *eventStream) startWatchFeed(fs *feedState, interest watchInterest) {
	ctx, cancel := context.WithCancel(s.ctx)
	feed := newWorkerFeed(fs.workerID, s.workerSource(fs.workerID), s.feedOut)
	feed.noReconnect = s.noReconnect
	feed.setInterest(interest)
	fs.feed = feed
	fs.watchCancel = cancel
	fs.interest = interest
	fs.health = sourceHealth{}
	fs.started = s.now()
	// A watch that starts after stream.ready cannot hold it back.
	fs.settled = s.ready
	fs.refused = false
	feed.start(ctx, s.wg)
}

func (s *eventStream) stopWatchFeed(fs *feedState) {
	if fs.watchCancel != nil {
		fs.watchCancel()
	}
	fs.feed = nil
	fs.watchCancel = nil
	fs.interest = watchInterest{}
	fs.health = sourceHealth{}
	fs.settled = true
	fs.refused = false
}

// workerTabIDs lists the sorted tab IDs in scope hosted by workerID.
func (s *eventStream) workerTabIDs(workerID string) []string {
	var ids []string
	for id, tab := range s.tabs {
		if tab.WorkerID == workerID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
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
		return s.feedDown(fs, out.err, out.retrying)
	case feedRefused:
		return s.feedRefused(fs, out.err)
	case feedRejected:
		return s.onRejection(fs, out.rejection, out.rejectedTerminal)
	}
	return nil
}

// workerEvent is a stream event about the Worker of fs.
func workerEvent(fs *feedState) streamEvent {
	workerID := fs.workerID
	if workerID == "" && fs.feed != nil {
		workerID = fs.feed.workerID
	}
	return streamEvent{Source: "worker", WorkerID: workerID}
}

// feedDown reports that a Worker's watch cannot deliver now. When retrying is
// true, the watch retries by itself and one stream.unavailable states the
// outage. When retrying is false (for example under --reconnect=false), the
// watch stopped and stream.unavailable reports that it will not try again.
func (s *eventStream) feedDown(fs *feedState, err error, retrying bool) error {
	fs.settled = true
	fs.health.live = false
	if !retrying {
		fs.refused = true
	}
	if fs.health.down && retrying {
		return s.maybeReady()
	}
	// A watch that never delivered lost everything since it started.
	if !fs.health.down {
		fs.health.down, fs.health.since = true, fs.started
		if fs.health.everLive {
			fs.health.since = s.now()
		}
	}
	if err := s.emit(unavailableEvent(workerEvent(fs), err, retrying)); err != nil {
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
	announceStatus := track.announceStatus
	announceActivity := track.announceActivity
	track.announceStatus = false
	track.announceActivity = false
	var events []streamEvent
	if status := info.GetStatus(); status != leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED {
		known := track.status != leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED
		if (!known && announceStatus) || (known && reconcile && status != track.status) {
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
		if (!known && announceActivity) || (known && reconcile && state != track.activity) {
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
		track.activity, track.announceActivity = state, false
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
		track.status, track.announceStatus = status, false
		return s.emit(agentStatusEvent(tab, status, e.StatusChange.GetStartupError()))
	case *leapmuxv1.AgentEvent_TurnEnd:
		return s.emit(agentTurnEndEvent(tab))
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
		ev := agentRequestEndedEvent(tab, cancel.GetRequestId(), cancel.GetClaimToken(), cancel.GetResponseState())
		ev.Request = cancel.GetClaimToken()
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_TodosChanged:
		items := e.TodosChanged.GetTodos()
		key := todoEventKey(items)
		if key == track.todos {
			return nil
		}
		track.todos = key
		return s.emit(agentTodoEvent(tab, semanticevents.SummarizeTodos(items)))
	case *leapmuxv1.AgentEvent_BackgroundTasksChanged:
		items := e.BackgroundTasksChanged.GetTasks()
		key := backgroundTaskEventKey(items)
		if key == track.tasks {
			return nil
		}
		track.tasks = key
		return s.emit(agentBackgroundTaskEvent(tab, semanticevents.SummarizeBackgroundTasks(items)))
	case *leapmuxv1.AgentEvent_GoalChanged:
		g := e.GoalChanged.GetGoal()
		ev := tabStreamEvent(kindAgentGoal, tab)
		obj := ""
		status := leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED
		if g != nil {
			obj = strings.TrimSpace(g.GetObjective())
			status = g.GetStatus()
		}
		active := obj != "" && status == leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE
		paused := obj != "" && status == leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_PAUSED
		ev.GoalActive = &active
		ev.GoalPaused = &paused
		switch {
		case obj == "":
			ev.Summary = "goal cleared"
		case status == leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_PAUSED:
			ev.Summary = fmt.Sprintf("goal paused: %s", oneLine(obj))
		case status == leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE:
			ev.Summary = fmt.Sprintf("goal active: %s", oneLine(obj))
		default:
			ev.Summary = fmt.Sprintf("goal %s: %s", enumToken(status), oneLine(obj))
		}
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_InputQueueChanged:
		snap := e.InputQueueChanged.GetSnapshot()
		rev := snap.GetRevision()
		ev := tabStreamEvent(kindAgentQueue, tab)
		ev.QueueRevision = &rev
		ev.Summary = fmt.Sprintf("queue revision %d (%d items)", rev, len(snap.GetItems()))
		return s.emit(ev)
	case *leapmuxv1.AgentEvent_InputQueueRevision:
		rev := e.InputQueueRevision
		ev := tabStreamEvent(kindAgentQueue, tab)
		ev.QueueRevision = &rev
		ev.Summary = fmt.Sprintf("queue revision %d", rev)
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
	fact := semanticevents.TerminalFact(te)
	track := s.terminalTrack(tab.ID)
	switch e := fact.GetEvent().(type) {
	case *leapmuxv1.SemanticTabFact_TerminalStatus:
		status := e.TerminalStatus.GetStatus()
		if status == track.status {
			return nil
		}
		track.status = status
	case *leapmuxv1.SemanticTabFact_TerminalExit:
		track.status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED
	case *leapmuxv1.SemanticTabFact_TerminalTitle:
		title := e.TerminalTitle.GetTitle()
		if track.titled && title == track.title {
			return nil
		}
		track.title, track.titled = title, true
	}
	ev, ok := terminalFactEvent(fact, tab)
	if !ok {
		return nil
	}
	return s.emit(ev)
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
	noun := "agent"
	if terminal {
		noun = "terminal"
	}
	disposition, err := classifyWatchRejection(noun, tab.ID, reason)
	if disposition == watchRejectionRetry || s.rejected[tab.ID] == reason {
		return nil
	}
	s.rejected[tab.ID] = reason
	if disposition == watchRejectionNotFound {
		err = cli.Newf(cli.CodeNotFound, "the worker holds no %s %s", noun, tab.ID)
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
	if err := s.emit(streamEvent{Kind: kindStreamReady, Summary: summary, Scope: scope}); err != nil {
		return err
	}
	if s.snapshot {
		return errStreamDone
	}
	return nil
}

// scopeCounts counts what the stream watches now.
func (s *eventStream) scopeCounts() *scopeCounts {
	counts := &scopeCounts{Workspaces: len(s.workspaces), Tabs: len(s.tabs), Workers: len(s.feeds), HubLive: s.hub.live}
	if s.tabID != "" {
		counts.Workspaces = len(s.tabs)
	}
	for _, fs := range s.feeds {
		if fs.isLive() {
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

func agentRequestEvent(tab scopedTab, req *leapmuxv1.AgentControlRequest) streamEvent {
	ev := agentRequestEventFields(tab, controlPromptKind(req.GetPrompt()), !req.GetNonBlocking(), semanticevents.PromptDetail(req.GetPrompt()))
	ev.Request, ev.RequestID = req.GetClaimToken(), req.GetRequestId()
	return ev
}

func todoEventKey(items []*leapmuxv1.TodoItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d;", len(items))
	for _, item := range items {
		fmt.Fprintf(&b, "%q:%d;", item.GetContent(), item.GetStatus())
	}
	return b.String()
}

func backgroundTaskEventKey(tasks []*leapmuxv1.BackgroundTaskItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d;", len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(&b, "%q:%d;", task.GetId(), task.GetStatus())
	}
	return b.String()
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
