package service

import (
	"context"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// AgentResumer schedules background agent resumes. It runs one initial sweep
// after reconciliation and accepts later unarchive transitions.
//
// It exists because several agent CLIs can list the agent sessions that run on
// the same machine and send messages to them. A worker restart kills every
// agent process, and nothing started one again until a human typed into that
// tab -- so after a restart an agent could neither see nor reach its siblings,
// and no amount of waiting fixed it. Respawning the processes is what puts them
// back in their provider's machine-local view of each other.
//
// It is a type rather than a bare method because the scheduler owns goroutines
// and a stop. Tests can drive both without bootstrap.
type AgentResumer struct {
	svc *Service

	// mu guards the lifecycle and scheduled-job fields below. Start and Stop take it for their WHOLE
	// decision, not merely to read a field.
	//
	// The two race in production. Shutdown stops the resumer before the
	// reconciler, so the reconciler goroutine can still report a converged pass
	// and call Start. Deciding under one lock is what makes "Stop wins" true
	// rather than likely: a Start that latched just after Stop read a nil done
	// channel launches a sweep nobody waits for, and it reads a database the
	// caller is about to close.
	mu sync.Mutex
	// started latches the sweep. Start is called from every converged reconciler
	// pass -- which repeats hourly -- and only the first may sweep.
	started bool
	// stopped records that Stop ran, so a later Start refuses and a second Stop
	// does not close an already-closed channel.
	stopped bool
	// done closes when the sweep goroutine returns. Nil until the sweep starts.
	done chan struct{}
	// stop closes on Stop and is what makes the launcher give up on the
	// candidates that it did not reach yet.
	stop chan struct{}
	// scheduled contains reusable unarchive jobs. The wait counter increments
	// before each goroutine starts, so Stop can drain every accepted job.
	scheduled   map[string]struct{}
	scheduledWG sync.WaitGroup
	resumeSlots chan struct{}
}

// NewAgentResumer returns the service's shared resume scheduler.
func (svc *Service) NewAgentResumer() *AgentResumer {
	svc.resumeSchedulerMu.Lock()
	defer svc.resumeSchedulerMu.Unlock()
	if svc.agentResumer == nil {
		// max(1): a zero capacity makes resumeSlots unbuffered, and no goroutine
		// ever receives from it, so every scheduled resume would block until Stop.
		// The manager caps the value at one already; this keeps the deadlock
		// impossible rather than merely absent today.
		limit := max(1, svc.Agents.StartupConcurrency())
		svc.agentResumer = &AgentResumer{
			svc:         svc,
			stop:        make(chan struct{}),
			scheduled:   make(map[string]struct{}),
			resumeSlots: make(chan struct{}, limit),
		}
	}
	return svc.agentResumer
}

// Schedule queues eligible agent rows for a reusable background resume pass.
// It deduplicates one agent while a prior resume remains in flight.
func (r *AgentResumer) Schedule(ctx context.Context, agentIDs []string) {
	for _, agentID := range agentIDs {
		if agentID == "" || r.stopping() {
			continue
		}
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			return
		}
		if _, exists := r.scheduled[agentID]; exists {
			r.mu.Unlock()
			continue
		}
		r.scheduled[agentID] = struct{}{}
		r.scheduledWG.Add(1)
		r.mu.Unlock()

		go func() {
			defer func() {
				r.mu.Lock()
				delete(r.scheduled, agentID)
				r.mu.Unlock()
				r.scheduledWG.Done()
			}()
			select {
			case r.resumeSlots <- struct{}{}:
				defer func() { <-r.resumeSlots }()
			case <-r.stop:
				return
			case <-ctx.Done():
				return
			}
			r.resumeOne(ctx, agentID)
		}()
	}
}

// Start runs the sweep once, in the background. Later calls do nothing, and so
// does a call that arrives after Stop.
//
// It REFUSES, without latching, when the worker has no owner yet or when the
// shutdown started. Not latching is the point: the caller fires this from every
// converged reconciler pass, so a refusal here is retried on the next one rather
// than losing the sweep for the life of the process. An owner is a hard
// requirement, not a nicety -- a resumed agent needs a control socket, and
// remintAgentControlIPC cannot mint one for an unnamed user. bootstrap triggers
// a fresh reconciler pass the moment the hub delivers that owner, so the retry
// arrives in seconds rather than at the next hourly tick.
func (r *AgentResumer) Start(ctx context.Context) {
	if r.svc.RegisteredBy().IsZero() {
		slog.Debug("agent resume: no worker owner yet; the sweep waits for the hub to deliver one")
		return
	}
	if r.stopping() {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// stopped is the second half of the stopping() check above, and the half
	// that cannot be lost to a race: a Stop that lands between that check and
	// this lock still refuses this sweep.
	if r.started || r.stopped {
		return
	}
	r.started = true
	done := make(chan struct{})
	r.done = done
	go func() {
		defer close(done)
		r.sweep(ctx)
	}()
}

// Stop tells the scheduler to accept no more candidates. It waits for the
// initial sweep and all reusable jobs. A later Start schedules nothing.
//
// It DOES wait for the resumes the launcher already handed off, because the
// sweep joins its own group on every exit. Two facts make that the correct
// design rather than an expensive one. errgroup.Go blocks the launcher once the
// fan-out limit is full, so a Stop could never return promptly anyway while a
// handshake was in flight. And Service.Shutdown waits for exactly the same
// startups a few lines later through AgentStartup.WaitForInFlight, so joining
// here costs no additional time -- it only moves the wait earlier, to the point
// where the sweep can still be observed as running.
//
// Not joining is what made WaitForInFlight unsafe. The launcher hands a
// candidate to errgroup.Go, the runtime does not schedule it yet, and it is
// therefore short of AgentStartup.begin. The drain then saw a zero count and
// returned, and the straggler read a database the caller was about to close.
func (r *AgentResumer) Stop() {
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		close(r.stop)
	}
	r.mu.Unlock()
	r.waitForSweep()
	r.scheduledWG.Wait()
}

// waitForSweep blocks until the sweep goroutine returns, or returns at once
// when no sweep ever started. Reading r.done demands the lock, and both Stop
// and the test helper need exactly this, so the field layout is known in one
// place.
func (r *AgentResumer) waitForSweep() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

// StoppedForTest reports whether Stop ran. It lives in the production file, like
// Manager.StartupConcurrencyForTest, because the caller that needs it is the
// bootstrap wiring test in another package: both background loops share ONE
// stopper field, and that is exactly the shape in which the second one comes to
// be dropped without a sound.
func (r *AgentResumer) StoppedForTest() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// stopping reports whether the sweep must launch nothing further. It answers
// one question through one predicate: Stop closed r.stop, or the service began
// its shutdown. Every check site calls this, so a new one cannot pick the
// weaker of the two signals and keep launching after Stop returned.
func (r *AgentResumer) stopping() bool {
	select {
	case <-r.stop:
		return true
	default:
	}
	return r.svc.shuttingDown.Load()
}

// sweep resumes every open root agent that ran a process before. See skipReason
// for what that means and for the predicate it deliberately does not use.
func (r *AgentResumer) sweep(ctx context.Context) {
	svc := r.svc
	ids, err := svc.Queries.ListRootAgentIDsForResume(ctx)
	if err != nil {
		slog.Error("agent resume: list open agents", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	limit := svc.Agents.StartupConcurrency()
	slog.Info("agent resume: restoring agent processes after a worker restart",
		"candidates", len(ids), "concurrency", limit)

	var counts resumeCounts
	g := new(errgroup.Group)
	// The manager's own permit pool already caps concurrent startups across
	// every spawn path. Limiting the sweep to the SAME number caps how deep the
	// queue in front of that pool gets: an interactive open then waits behind one
	// generation of startups rather than behind every open tab on the machine.
	g.SetLimit(limit)
	// Join the group on EVERY exit, including the two early returns below.
	//
	// The group never returns an error: one agent that refuses to start must not
	// abandon the rest, so resumeOne records the failure and reports success.
	//
	// An early return that skipped this wait would leave a candidate that
	// errgroup.Go accepted but the runtime did not schedule yet. That candidate
	// is still short of AgentStartup.begin, so Shutdown's WaitForInFlight --
	// which runs immediately after Stop -- sees a zero count, returns, and lets
	// the caller close the database under a goroutine that is about to read it.
	defer func() {
		_ = g.Wait()
		slog.Info("agent resume: sweep finished", counts.logArgs()...)
	}()
	for _, id := range ids {
		// errgroup.Go blocks once the limit is reached, so the loop itself is the
		// backpressure and no candidate is dropped. counts is mutex-guarded, so
		// the summary the deferred wait logs is a snapshot rather than a race.
		if r.stopping() {
			slog.Info("agent resume: stopped before the sweep reached every candidate",
				"remaining", len(ids)-counts.total())
			return
		}
		select {
		case <-ctx.Done():
			slog.Info("agent resume: context cancelled before the sweep reached every candidate",
				"remaining", len(ids)-counts.total())
			return
		default:
		}
		g.Go(func() error {
			counts.record(r.resumeOne(ctx, id))
			return nil
		})
	}
}

// resumeOutcome is what one candidate cost the sweep.
type resumeOutcome int

const (
	outcomeResumed resumeOutcome = iota
	outcomeSkipped
	outcomeFailed
)

// resumeCounts tallies a sweep's outcomes. Guarded by its own mutex because the
// candidates run concurrently, so logArgs reports a consistent triple rather
// than three independently torn reads.
type resumeCounts struct {
	mu      sync.Mutex
	resumed int
	skipped int
	failed  int
}

func (c *resumeCounts) record(o resumeOutcome) {
	c.mu.Lock()
	switch o {
	case outcomeResumed:
		c.resumed++
	case outcomeSkipped:
		c.skipped++
	case outcomeFailed:
		c.failed++
	}
	c.mu.Unlock()
}

func (c *resumeCounts) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumed + c.skipped + c.failed
}

func (c *resumeCounts) logArgs() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []any{"resumed", c.resumed, "skipped", c.skipped, "failed", c.failed}
}

// resumeOne restores a single agent, and reports what that candidate cost.
func (r *AgentResumer) resumeOne(ctx context.Context, agentID string) resumeOutcome {
	svc := r.svc
	if r.stopping() {
		return outcomeSkipped
	}
	dbAgent, err := svc.getAgentByID(ctx, agentID)
	if err != nil {
		slog.Warn("agent resume: fetch agent", "agent_id", agentID, "error", err)
		return outcomeFailed
	}
	if reason := r.skipReason(dbAgent); reason != "" {
		slog.Debug("agent resume: skipping agent", "agent_id", agentID, "reason", string(reason))
		return outcomeSkipped
	}

	// ensureAgentRunning owns the rest: it registers the start with
	// AgentStartup -- which is what lets a CloseAgent cancel a resume that is
	// still in its handshake, and what makes Shutdown's drain wait for it -- and
	// it roots the agent process's own context at bgCtx() rather than at this
	// sweep's, so a worker teardown stops a resumed agent through the same
	// ordered StopAll every other agent goes through.
	//
	// backgroundStart is the whole reason this caller differs from the other
	// three. It is the one spawn nobody is waiting on, so it is the one that
	// draws on the startup permit pool.
	//
	// Resume the session the row points at, rather than letting
	// ensureAgentRunning re-derive one. Its resolveResumeSessionID asks
	// HasUserMessages, which is scoped to the CURRENT session:
	// UpdateAgentSessionID stamps session_start_seq with the message high-water
	// mark, so it answers false for exactly the row skipReason exists to rescue
	// -- an agent whose provider issued a fresh session id mid-conversation.
	// That answer resolves the resume id to "", the CLI comes up in a BLANK
	// session, and its startup handshake reports a new session id that
	// overwrites agent_session_id. The conversation itself survives in the
	// worker database, but the only pointer to the provider's side of it is
	// gone, unprompted, on a path no user asked for.
	//
	// skipReason already established that a process ran for this agent, so the
	// row's session id is the thing to restore. A provider that refuses to
	// resume it fails this one start, which leaves the tab exactly as cold as it
	// is now and retryable on the next message.
	resumeSessionID := dbAgent.AgentSessionID
	if err := svc.ensureAgentRunning(agentID, &resumeSessionID, backgroundStart); err != nil {
		slog.Warn("agent resume: failed to start agent", "agent_id", agentID, "error", err)
		return outcomeFailed
	}
	return outcomeResumed
}

// resumeSkipReason states why the sweep passed an agent over, or "" to resume
// it.
//
// The reason is a value rather than a bool so the sweep can log it. A count
// alone ("skipped 3") tells an operator nothing about whether the worker made
// the right call, and "why did my agent not come back" is the only question
// anybody asks of this feature.
type resumeSkipReason string

const (
	resumeSkipAlreadyRunning resumeSkipReason = "already running"
	resumeSkipSubagent       resumeSkipReason = "subagent transcript, which owns no process"
	resumeSkipClosed         resumeSkipReason = "tab closed since the sweep listed it"
	resumeSkipStartupFailed  resumeSkipReason = "previous startup failed"
	resumeSkipArchived       resumeSkipReason = "workspace archived"
	resumeSkipNeverStarted   resumeSkipReason = "never started and not created from a session"
)

// skipReason reports why this row identifies an agent NOT worth respawning, or
// "" to resume it.
//
// "Worth respawning" is "a process ran for this agent, or it was created from
// an existing session": a non-empty agent_session_id, which every provider
// writes from its startup handshake, or Resumed != 0. A tab that was opened and
// never started has neither, and spawning it would start a blank CLI that costs
// memory and answers nobody.
//
// It deliberately does NOT ask HasUserMessages, which reads as the same
// question and is not. That query is scoped to the CURRENT session --
// UpdateAgentSessionID stamps session_start_seq with the message high-water
// mark, so once a provider issues a fresh session id mid-conversation (Claude
// Code does, on its first turn) it answers false for an agent that the user
// still talks to in the same conversation. Every such agent then stays cold.
// resumeOne passes the row's own session id for the same reason.
//
// This is derived on every sweep rather than recorded in a column, so there is
// nothing to keep in step with the truth. A "was running" flag would be a second
// source of truth for the same fact, and a hard kill is exactly the case that
// leaves it stale.
func (r *AgentResumer) skipReason(dbAgent db.Agent) resumeSkipReason {
	// Already running: a message that arrived during the sweep beat us to it.
	if r.svc.Agents.HasAgent(dbAgent.ID) {
		return resumeSkipAlreadyRunning
	}
	// A virtual child agent is a subagent transcript: it has no process of its
	// own, and ensureAgentRunning refuses one. The boot sweep never lists a
	// child (its query selects roots), but Schedule takes the tab ids the Hub
	// fanned out, and a subagent transcript IS a tab in the workspace layout.
	// Without this the unarchive of a workspace holding one logs a failed start
	// per subagent tab and spends a startup permit on each.
	if dbAgent.ParentAgentID.Valid {
		return resumeSkipSubagent
	}
	// The candidate list is a SNAPSHOT: ListRootAgentIDsForResume ran once, and a
	// candidate at the back of a throttled queue is reached much later -- minutes,
	// on a machine with many tabs and a concurrency of 1. A CloseAgent in between
	// already ran that tab's whole teardown, so a process started for it now is
	// one nothing will ever stop: it holds a CLI and its memory for the life of
	// the worker, under a tab no client can see. This row was read AFTER the
	// list, which is what makes the check worth having.
	//
	// ensureAgentRunning refuses a closed row as well, under the per-agent
	// lifecycle lock, which is what closes the window between this read and that
	// refusal. The check stays here so the sweep can report the skip as a skip
	// rather than as a failed start.
	if dbAgent.ClosedAt.Valid {
		return resumeSkipClosed
	}
	// The same permanent-failure gate SendAgentMessage applies. Respawning an
	// agent whose last startup failed would burn a slot on a CLI that is going to
	// fail again, and the row already tells the user to open a new agent.
	if dbAgent.StartupError != "" {
		return resumeSkipStartupFailed
	}
	if dbAgent.WorkspaceArchived != 0 {
		return resumeSkipArchived
	}
	if dbAgent.AgentSessionID == "" && dbAgent.Resumed == 0 {
		return resumeSkipNeverStarted
	}
	return ""
}
