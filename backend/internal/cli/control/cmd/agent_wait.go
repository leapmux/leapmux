package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// waitUntil is the state that `agent wait --until` waits for: a token of
// AgentActivityState, as `agent list` prints it, or "settled".
type waitUntil string

// waitUntilSettled is "idle" or "waiting_for_user": the agent works no more,
// because it finished or because it waits for an answer. The Worker calls
// the change from working to either of them the settle.
const waitUntilSettled waitUntil = "settled"

// waitUntilTokens lists every value of --until, the default first.
func waitUntilTokens() []string {
	return append([]string{string(waitUntilSettled)}, enumTokens(leapmuxv1.AgentActivityState(0))...)
}

// parseWaitUntil reads the value of --until.
func parseWaitUntil(token string) (waitUntil, error) {
	if slices.Contains(waitUntilTokens(), token) {
		return waitUntil(token), nil
	}
	return "", cli.Usagef("unknown --until %q", token).
		WithHint("accepted values: %s", strings.Join(waitUntilTokens(), ", "))
}

// needsEmptyQueue reports whether the condition also needs an input queue
// with nothing that the agent will read by itself. An agent that holds such
// a message starts a turn soon, so it is neither idle nor settled yet: a
// starting agent reports idle while its first message waits in the queue.
func (u waitUntil) needsEmptyQueue() bool {
	return u == waitUntilSettled || enumToken(leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE) == string(u)
}

// holds reports whether an agent in state meets the condition.
func (u waitUntil) holds(state leapmuxv1.AgentActivityState) bool {
	if state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED {
		return false
	}
	if u == waitUntilSettled {
		return state != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	}
	return enumToken(state) == string(u)
}

// agentStateClosed is the state of an agent that closed. agentStateUnknown is
// the state of an agent that the wait did not observe yet.
const (
	agentStateClosed  = "closed"
	agentStateUnknown = "unknown"
)

// agentWaitRow is one agent of the result of `agent wait`.
type agentWaitRow struct {
	ID string `json:"id" table:"AGENT,short"`
	// State is the last state that the wait observed: a token of
	// AgentActivityState, "closed", or "unknown".
	State string `json:"state" table:"STATE"`
	// Reached reports whether the agent was in the state that --until gives
	// at a moment after the wait began.
	Reached bool `json:"reached" table:"REACHED"`
}

// agentWaitResult is the result of `agent wait`.
type agentWaitResult struct {
	Agents []agentWaitRow `json:"agents"`
}

// agentWaitOutcome is how a wait ended.
type agentWaitOutcome int

const (
	// agentWaitHeld is a wait whose condition held.
	agentWaitHeld agentWaitOutcome = iota + 1
	// agentWaitTimedOut is a wait that its timeout ended.
	agentWaitTimedOut
	// agentWaitImpossible is a wait whose condition can no longer hold,
	// because agents closed before they reached the state.
	agentWaitImpossible
)

// agentWaitSpec states one wait.
type agentWaitSpec struct {
	agentIDs []string
	until    waitUntil
	// anyAgent ends the wait when one agent reaches the state.
	anyAgent bool
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
}

// queuedInputFunc reports whether the input queue of an agent holds a
// message that the agent will read by itself.
type queuedInputFunc func(ctx context.Context, agentID string) (bool, error)

// awaitAgents reads events until the condition of spec holds, the timeout
// passes, or the condition can no longer hold. An agent reaches the state
// when an event reports it at any moment after the wait began, so the order
// of a level and a change does not matter here. For a condition that needs
// an empty queue, queued asks the queue at that moment, and a message that
// the agent will read keeps the wait going: its turn sends a new change. The
// error is the cause of an activityFailed, a failed read, or the end of ctx.
func awaitAgents(ctx context.Context, clock quartz.Clock, events <-chan activityEvent, queued queuedInputFunc, spec agentWaitSpec) (agentWaitResult, agentWaitOutcome, error) {
	rows := make([]agentWaitRow, len(spec.agentIDs))
	index := make(map[string]int, len(spec.agentIDs))
	for i, id := range spec.agentIDs {
		rows[i] = agentWaitRow{ID: id, State: agentStateUnknown}
		index[id] = i
	}
	result := func() agentWaitResult { return agentWaitResult{Agents: slices.Clone(rows)} }
	var deadline <-chan time.Time
	if spec.timeout > 0 {
		timer := clock.NewTimer(spec.timeout, agentWaitTimerTag)
		defer timer.Stop(agentWaitTimerTag)
		deadline = timer.C
	}
	for {
		if outcome, done := agentWaitVerdict(rows, spec.anyAgent); done {
			return result(), outcome, nil
		}
		select {
		case <-ctx.Done():
			return result(), 0, context.Cause(ctx)
		case <-deadline:
			return result(), agentWaitTimedOut, nil
		case ev := <-events:
			i, watched := index[ev.agentID]
			if !watched && ev.kind != activityFailed {
				continue
			}
			switch ev.kind {
			case activityFailed:
				return result(), 0, ev.err
			case activityLevel, activityChange:
				if rows[i].State == agentStateClosed {
					continue
				}
				rows[i].State = agentStateToken(ev.state)
				if rows[i].Reached || !spec.until.holds(ev.state) {
					continue
				}
				if spec.until.needsEmptyQueue() {
					busy, err := queued(ctx, ev.agentID)
					if err != nil {
						return result(), 0, err
					}
					if busy {
						continue
					}
				}
				rows[i].Reached = true
			case activityClosed:
				rows[i].State = agentStateClosed
			case activityTurnEnd, activityStatusChange:
				// A state wait reads the published state alone.
			}
		}
	}
}

// agentWaitTimerTag identifies the timeout of a wait to a test clock.
const agentWaitTimerTag = "agent-wait-timeout"

// agentWaitVerdict decides whether the wait over rows ended. done is false
// while the condition can still hold.
func agentWaitVerdict(rows []agentWaitRow, anyAgent bool) (agentWaitOutcome, bool) {
	reached, closedBefore := 0, 0
	for _, row := range rows {
		switch {
		case row.Reached:
			reached++
		case row.State == agentStateClosed:
			closedBefore++
		}
	}
	switch {
	case anyAgent && reached > 0, !anyAgent && reached == len(rows):
		return agentWaitHeld, true
	case anyAgent && closedBefore == len(rows), !anyAgent && closedBefore > 0:
		return agentWaitImpossible, true
	}
	return 0, false
}

// runAgentWait waits until the agents reach the state that --until gives.
// It prints the state of each agent whether the condition held or not, and
// exits 6 when it did not.
func runAgentWait(inv *cli.Invocation) (any, error) {
	spec, err := agentWaitSpecOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	targets, err := resolveWaitTargets(s, inv.Variadic("agent"))
	if err != nil {
		return nil, err
	}
	byWorker, agentIDs := groupWaitTargets(targets)
	spec.agentIDs = agentIDs

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	events := make(chan activityEvent, 16)
	workerOf := map[string]string{}
	for _, group := range byWorker {
		startActivityFeed(ctx, workerActivitySource(s.c, group.workerID), group.agentIDs, events)
		for _, id := range group.agentIDs {
			workerOf[id] = group.workerID
		}
	}
	queues := &queueWatch{
		read: func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			return workerReplyReader(s.c, workerOf[agentID], agentID).readQueue(ctx)
		},
		notice: inv.Printer.Notice,
		now:    time.Now,
	}
	result, outcome, err := awaitAgents(ctx, quartz.NewReal(), events, queues.queuedInput, spec)
	if err != nil {
		return nil, err
	}
	if outcome == agentWaitHeld {
		return result, nil
	}
	return nil, agentWaitEnded(inv.Printer, result, outcome, spec)
}

// queueWatch answers queuedInputFunc from the input queue of each agent, and
// says once on stderr why a paused queue keeps the wait going.
type queueWatch struct {
	read   func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error)
	notice func(format string, args ...any)
	now    func() time.Time
	// noticed is the pause reason that the last notice stated for each agent.
	noticed map[string]leapmuxv1.AgentInputQueuePauseReason
}

func (q *queueWatch) queuedInput(ctx context.Context, agentID string) (bool, error) {
	snapshot, err := q.read(ctx, agentID)
	if err != nil {
		return false, err
	}
	pending := queuedForDispatch(snapshot, q.now())
	pause := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
	if pending {
		pause = snapshot.GetPauseReason()
	}
	if q.noticed == nil {
		q.noticed = map[string]leapmuxv1.AgentInputQueuePauseReason{}
	}
	if pause != q.noticed[agentID] {
		q.noticed[agentID] = pause
		if pause != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
			q.notice("the input queue of agent %s is paused (%s); the wait goes on until the agent reads its messages", agentID, enumToken(pause))
		}
	}
	return pending, nil
}

// queuedForDispatch reports whether the queue holds a message that the agent
// will read without a decision of the user: one that waits, dispatches or
// waits for its echo, and that is due. A failed message waits for the user,
// and a message scheduled for later is not due yet.
//
// A paused queue still counts. A planned restart pauses it too, and that
// pause ends by itself; the public snapshot does not say which pause it is.
func queuedForDispatch(snapshot *leapmuxv1.AgentInputQueueSnapshot, now time.Time) bool {
	for _, item := range snapshot.GetItems() {
		switch item.GetState() {
		case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO:
		default:
			continue
		}
		if item.GetScheduledAt() == "" {
			return true
		}
		// The Worker states a canonical UTC RFC 3339 time. One that does not
		// parse counts as due, so a wait never ends on a message it cannot
		// read the time of.
		at, err := time.Parse(time.RFC3339Nano, item.GetScheduledAt())
		if err != nil || !at.After(now) {
			return true
		}
	}
	return false
}

// agentWaitSpecOf reads the flags of `agent wait`.
func agentWaitSpecOf(inv *cli.Invocation) (agentWaitSpec, error) {
	until, err := parseWaitUntil(stringFlag(inv, "until"))
	if err != nil {
		return agentWaitSpec{}, err
	}
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return agentWaitSpec{}, err
	}
	return agentWaitSpec{until: until, anyAgent: boolFlag(inv, "any"), timeout: timeout}, nil
}

// waitTimeoutFlag reads --timeout, which must not be negative.
func waitTimeoutFlag(inv *cli.Invocation) (time.Duration, error) {
	timeout, err := inv.Command.Flags().GetDuration("timeout")
	if err != nil {
		return 0, cli.Wrap(cli.CodeUsage, err, "read --timeout")
	}
	if timeout < 0 {
		return 0, cli.Usagef("--timeout must not be negative")
	}
	return timeout, nil
}

// waitTargets are the agents of one Worker that a wait watches.
type waitTargets struct {
	workerID string
	agentIDs []string
}

// resolveWaitTargets resolves each agent input, in order. A wait on the
// calling agent is refused: its own turn runs the wait, so the agent cannot
// settle while the wait runs.
func resolveWaitTargets(s *session, inputs []string) ([]tabTarget, error) {
	bounded := s.bounded()
	defer bounded.Close()
	targets := make([]tabTarget, 0, len(inputs))
	for _, input := range inputs {
		got, err := bounded.resolveTab(resolve.KindAgent, input, resolve.Need{WorkerID: true})
		if err != nil {
			return nil, err
		}
		if err := refuseSelfWait(got); err != nil {
			return nil, err
		}
		targets = append(targets, got)
	}
	return targets, nil
}

// groupWaitTargets groups the agents by Worker, in the order of the first
// agent of each, and lists the agents in the order of the arguments. An
// agent that two arguments give counts once, at its first place.
func groupWaitTargets(targets []tabTarget) (groups []waitTargets, agentIDs []string) {
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		agentIDs = append(agentIDs, t.ID)
		i := slices.IndexFunc(groups, func(g waitTargets) bool { return g.workerID == t.WorkerID })
		if i < 0 {
			groups = append(groups, waitTargets{workerID: t.WorkerID})
			i = len(groups) - 1
		}
		groups[i].agentIDs = append(groups[i].agentIDs, t.ID)
	}
	return groups, agentIDs
}

// refuseSelfWait refuses a wait on the calling agent.
func refuseSelfWait(t tabTarget) error {
	if !t.Self && t.ID != callingTabID() {
		return nil
	}
	return cli.Usagef("agent %s is the calling agent, which cannot settle while it waits", t.ID).
		WithHint("wait from another tab or a terminal, or for another agent")
}

// agentWaitEnded reports a wait whose condition did not hold: it prints the
// result, says why on stderr, and returns exit status 6. The result prints
// in both cases, so a script reads the state of each agent either way.
func agentWaitEnded(p *cli.Printer, result agentWaitResult, outcome agentWaitOutcome, spec agentWaitSpec) error {
	if err := p.Result(result); err != nil {
		return err
	}
	if outcome == agentWaitImpossible {
		p.Notice("the wait ended, because %s closed before reaching the state %s", agentsClosed(result), spec.until)
	} else {
		p.Notice("the wait ended after %s, before %s reached the state %s", spec.timeout, agentsNotReached(result), spec.until)
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}

// agentsNotReached lists the agents of result that did not reach the state.
func agentsNotReached(result agentWaitResult) string {
	var ids []string
	for _, row := range result.Agents {
		if !row.Reached {
			ids = append(ids, row.ID)
		}
	}
	return agentPhrase(ids)
}

// agentsClosed lists the agents of result that closed before they reached
// the state.
func agentsClosed(result agentWaitResult) string {
	var ids []string
	for _, row := range result.Agents {
		if !row.Reached && row.State == agentStateClosed {
			ids = append(ids, row.ID)
		}
	}
	return agentPhrase(ids)
}

// agentPhrase is "agent <id>" for one agent, and "agents <id>, <id>" for
// more.
func agentPhrase(ids []string) string {
	if len(ids) == 1 {
		return "agent " + ids[0]
	}
	return fmt.Sprintf("agents %s", strings.Join(ids, ", "))
}
