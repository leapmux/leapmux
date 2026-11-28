package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/tunnel"
)

// waitUntil is the state that `agent wait --until` waits for: a token of
// AgentActivityState, as `agent list` prints it, or "settled".
type waitUntil string

type waitConditions []waitUntil

// match reports whether a state matches and whether it still needs a queue check.
func (conditions waitConditions) match(state string) (bool, bool) {
	guardedMatch := false
	for _, condition := range conditions {
		if condition.holds(state) {
			if !condition.needsEmptyQueue(state) {
				return true, false
			}
			guardedMatch = true
		}
	}
	return guardedMatch, guardedMatch
}

func (conditions waitConditions) description() string {
	if len(conditions) == 1 {
		return "the state " + string(conditions[0])
	}
	tokens := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		tokens = append(tokens, string(condition))
	}
	return "any of the states " + strings.Join(tokens, ", ")
}

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

// needsEmptyQueue reports whether an agent in state also needs an input queue
// with nothing that the agent will read by itself. An idle agent that holds
// such a message starts a turn soon, so it is neither idle nor settled yet: a
// starting agent reports idle while its first message waits in the queue. An
// agent in waiting_for_user is blocked on a prompt and cannot drain its queue
// until the user answers, so settled completes without waiting on queued input.
func (u waitUntil) needsEmptyQueue(state string) bool {
	idleState := enumToken(leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE)
	if state != idleState {
		return false
	}
	return u == waitUntilSettled || string(u) == idleState
}

// holds reports whether an agent in state meets the condition.
func (u waitUntil) holds(state string) bool {
	if state == agentStateUnknown || state == agentStateClosed {
		return false
	}
	if u == waitUntilSettled {
		return state != enumToken(leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING)
	}
	return state == string(u)
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
	State string `json:"state" table:"STATE,state"`
	// Reached reports whether the agent reached a requested state during the wait.
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
	until    waitConditions
	// anyAgent ends the wait when one agent reaches the state.
	anyAgent bool
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
	// onActivity, when non-nil, runs for each activityLevel or activityChange
	// observed for a watched, non-closed agent.
	onActivity func(activityEvent)
}

// queueWaitProbe reports whether a due input or, when requireInactive is set,
// an active turn prevents the wait from completing.
type queueWaitProbe func(ctx context.Context, agentID string, requireInactive bool) (bool, error)

var errAgentWaitExpired = errors.New("the agent wait deadline passed")

// agentWaitHooks holds test seams for runAgentWait. Production leaves it zero.
var agentWaitHooks struct {
	clock      quartz.Clock
	onActivity func(activityEvent)
}

// isContextCancelError reports whether err is a cancellation or deadline error
// caused by ctx ending.
func isContextCancelError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Cause(ctx)) ||
		connect.CodeOf(err) == connect.CodeCanceled
}

// agentWaitContext cancels an event wait and any reads it starts. The cause
// distinguishes this deadline from cancellation of the parent command.
func agentWaitContext(ctx context.Context, clock quartz.Clock, timeout time.Duration, timerTag string) (context.Context, func()) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := clock.AfterFunc(timeout, func() { cancel(errAgentWaitExpired) }, timerTag)
	return ctx, func() {
		timer.Stop()
		cancel(nil)
	}
}

// awaitAgents reads events until the condition holds, the timeout passes, or
// the condition can no longer hold. Once an agent reaches the condition, a
// later event cannot undo it. An idle report must also survive a queue probe,
// which checks for due inputs and an active turn. A queue revision can allow
// the wait to finish even when published activity stays the same.
func awaitAgents(ctx context.Context, clock quartz.Clock, events <-chan activityEvent, queueBlocksWait queueWaitProbe, spec agentWaitSpec) (agentWaitResult, agentWaitOutcome, error) {
	ctx, stop := agentWaitContext(ctx, clock, spec.timeout, agentWaitTimerTag)
	defer stop()
	rows := make([]agentWaitRow, len(spec.agentIDs))
	index := make(map[string]int, len(spec.agentIDs))
	for i, id := range spec.agentIDs {
		rows[i] = agentWaitRow{ID: id, State: agentStateUnknown}
		index[id] = i
	}
	result := func() agentWaitResult { return agentWaitResult{Agents: slices.Clone(rows)} }
	idleState := enumToken(leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE)
wait:
	for {
		if outcome, done := agentWaitVerdict(rows, spec.anyAgent); done {
			return result(), outcome, nil
		}
		if ctx.Err() != nil {
			break wait
		}
		select {
		case <-ctx.Done():
			break wait
		case ev := <-events:
			if ctx.Err() != nil {
				break wait
			}
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
				if spec.onActivity != nil {
					spec.onActivity(ev)
				}
			case activityInputChanged:
				// The published state stays the same. Recheck its queue below.
			case activityClosed:
				rows[i].State = agentStateClosed
				continue
			case activityTurnEnd, activityStatusChange:
				continue
			}
			if rows[i].Reached {
				continue
			}
			matched, checkQueue := spec.until.match(rows[i].State)
			if !matched || (ev.kind == activityInputChanged && !checkQueue) {
				continue
			}
			if checkQueue {
				busy, err := queueBlocksWait(ctx, ev.agentID, rows[i].State == idleState)
				if err != nil {
					if isContextCancelError(ctx, err) {
						break wait
					}
					return result(), 0, err
				}
				if busy {
					continue
				}
			}
			rows[i].Reached = true
		}
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, errAgentWaitExpired) {
		return result(), agentWaitTimedOut, nil
	}
	return result(), 0, cause
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

// runAgentWait waits for the agents to reach a requested state.
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
	byWorker, agentIDs := groupAgentsByWorker(targets)
	spec.agentIDs = agentIDs

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	events := make(chan activityEvent, 16)
	workerOf := map[string]string{}
	for _, group := range byWorker {
		stopFeed := startActivityFeed(ctx, workerActivitySource(s.c, group.workerID), group.agentIDs, events)
		defer stopFeed()
		for _, id := range group.agentIDs {
			workerOf[id] = group.workerID
		}
	}
	spinMsg := fmt.Sprintf("waiting for %s (%s)", agentPhrase(agentIDs), spec.until.description())
	if len(agentIDs) == 1 {
		spinMsg = fmt.Sprintf("waiting for agent %s (%s)", cli.ShortID(agentIDs[0]), spec.until.description())
	}
	spin := cli.StartSpinner(inv.Printer, spinMsg)
	defer spin.Stop()
	extraOnActivity := agentWaitHooks.onActivity
	spec.onActivity = func(ev activityEvent) {
		state := agentStateToken(ev.state)
		if len(agentIDs) == 1 {
			spin.Update(fmt.Sprintf("waiting for agent %s (%s; now %s)", cli.ShortID(agentIDs[0]), spec.until.description(), state))
		} else {
			spin.Update(fmt.Sprintf("waiting for %s (%s; %s -> %s)", agentPhrase(agentIDs), spec.until.description(), cli.ShortID(ev.agentID), state))
		}
		if extraOnActivity != nil {
			extraOnActivity(ev)
		}
	}
	queuePool := newWorkerQueuePool(s.c)
	defer queuePool.close()
	queues := &queueWatch{
		read: func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			return queuePool.readQueue(ctx, workerOf[agentID], agentID)
		},
		notice: func(format string, args ...any) {
			spin.Stop()
			_ = inv.Printer.Notice(format, args...)
		},
		now: time.Now,
	}
	waitClock := agentWaitHooks.clock
	if waitClock == nil {
		waitClock = quartz.NewReal()
	}
	result, outcome, err := awaitAgents(ctx, waitClock, events, queues.blocksWait, spec)
	spin.Stop()
	if err != nil {
		return nil, err
	}
	if outcome == agentWaitHeld {
		return result, nil
	}
	return nil, agentWaitEnded(inv.Printer, result, outcome, spec)
}

// workerQueuePool keeps one open E2EE channel per Worker for repeated
// ListAgentInputQueue probes during `agent wait`. Local IPC clients reuse the
// socket directly. On a channel transport failure, the cached channel is closed
// and reopened once for a retry.
type workerQueuePool struct {
	c        control.Peer
	mu       sync.Mutex
	channels map[string]*tunnel.Channel
	closed   bool
}

func newWorkerQueuePool(c control.Peer) *workerQueuePool {
	return &workerQueuePool{
		c:        c,
		channels: map[string]*tunnel.Channel{},
	}
}

func (p *workerQueuePool) readQueue(ctx context.Context, workerID, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
	req := &leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID}
	switch c := p.c.(type) {
	case *control.WorkerIPCClient:
		callCtx, cancel := rpcDeadline(ctx)
		defer cancel()
		var resp leapmuxv1.ListAgentInputQueueResponse
		if err := (workerCall{ipc: c, workerID: workerID}).Call(callCtx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, req, &resp); err != nil {
			return nil, err
		}
		return resp.GetSnapshot(), nil
	case *control.HubClient:
		if workerID == "" {
			return nil, &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
		}
		for attempt := range 2 {
			ch, err := p.channelFor(ctx, c, workerID)
			if err != nil {
				return nil, err
			}
			callCtx, cancel := rpcDeadline(ctx)
			var resp leapmuxv1.ListAgentInputQueueResponse
			err = (workerCall{hub: c, workerID: workerID, ch: ch}).Call(callCtx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, req, &resp)
			cancel()
			if err == nil {
				return resp.GetSnapshot(), nil
			}
			var coded *codedRPCError
			if attempt == 0 && ctx.Err() == nil && errors.As(err, &coded) && coded.Code == "rpc_failed" {
				p.dropChannel(workerID, ch)
				continue
			}
			return nil, err
		}
		return nil, context.Canceled
	default:
		return nil, fmt.Errorf("unsupported control peer %T", p.c)
	}
}

func (p *workerQueuePool) channelFor(ctx context.Context, hub *control.HubClient, workerID string) (*tunnel.Channel, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, context.Canceled
	}
	if ch := p.channels[workerID]; ch != nil {
		p.mu.Unlock()
		return ch, nil
	}
	p.mu.Unlock()

	openCtx, cancelOpen := rpcDeadline(ctx)
	ch, err := openHubE2EEChannel(openCtx, context.WithoutCancel(ctx), hub, workerID)
	cancelOpen()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		ch.Close()
		return nil, context.Canceled
	}
	if existing := p.channels[workerID]; existing != nil {
		p.mu.Unlock()
		ch.Close()
		return existing, nil
	}
	p.channels[workerID] = ch
	p.mu.Unlock()
	return ch, nil
}

func (p *workerQueuePool) dropChannel(workerID string, ch *tunnel.Channel) {
	p.mu.Lock()
	if p.channels[workerID] == ch {
		delete(p.channels, workerID)
		p.mu.Unlock()
		ch.Close()
		return
	}
	p.mu.Unlock()
}

func (p *workerQueuePool) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	channels := make([]*tunnel.Channel, 0, len(p.channels))
	for _, ch := range p.channels {
		channels = append(channels, ch)
	}
	clear(p.channels)
	p.mu.Unlock()
	for _, ch := range channels {
		ch.Close()
	}
}

// queueWatch answers queueWaitProbe from the input queue of each agent, and
// says once on stderr why a paused queue keeps the wait going.
type queueWatch struct {
	read   func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error)
	notice func(format string, args ...any)
	now    func() time.Time
	// noticed is the pause reason that the last notice stated for each agent.
	noticed map[string]leapmuxv1.AgentInputQueuePauseReason
}

func (q *queueWatch) blocksWait(ctx context.Context, agentID string, requireInactive bool) (bool, error) {
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
	return pending || (requireInactive && snapshot.GetActiveTurn()), nil
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
	values := inv.StringArray("until")
	until := make(waitConditions, 0, len(values))
	for _, value := range values {
		condition, err := parseWaitUntil(value)
		if err != nil {
			return agentWaitSpec{}, err
		}
		if !slices.Contains(until, condition) {
			until = append(until, condition)
		}
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

// resolveWaitTargets resolves each agent input, in order. "children" expands
// to all direct child subagents of the calling agent. A wait on the calling
// agent is refused: its own turn runs the wait, so the agent cannot settle
// while the wait runs.
func resolveWaitTargets(s *session, inputs []string) ([]tabTarget, error) {
	timed := s.timed()
	defer timed.Close()
	targets, err := timed.resolveVariadicTabs(resolve.KindAgent, inputs, resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if err := refuseSelfWait(target); err != nil {
			return nil, err
		}
	}
	return targets, nil
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
	var ids []string
	for _, row := range result.Agents {
		if row.Reached {
			continue
		}
		if outcome == agentWaitImpossible && row.State != agentStateClosed {
			continue
		}
		ids = append(ids, row.ID)
	}
	if outcome == agentWaitImpossible {
		_ = p.Notice("the wait ended, because %s closed before reaching %s", agentPhrase(ids), spec.until.description())
	} else {
		_ = p.Notice("the wait ended after %s, before %s reached %s", spec.timeout, agentPhrase(ids), spec.until.description())
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}

// agentPhrase is "agent <id>" for one agent, and "agents <id>, <id>" for
// more.
func agentPhrase(ids []string) string {
	if len(ids) == 1 {
		return "agent " + ids[0]
	}
	return fmt.Sprintf("agents %s", strings.Join(ids, ", "))
}
