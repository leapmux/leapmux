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
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/semanticevents"
)

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
	feed.noReconnect = s.noReconnect
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
	s.startPrivateFeed(ctx, workerID)
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
