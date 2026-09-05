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

// AgentResumer returns the service's shared resume scheduler, building it on
// the first call. It is an accessor, not a constructor: bootstrap and the
// unarchive RPC must reach the SAME scheduler, because a second one would
// carry its own dedup set and its own stop channel, and nothing would ever
// drain it.
//
// The concurrency read stays lazy on purpose. bootstrap calls
// SetStartupConcurrency after service.New and before it starts the background
// loops, so a resumer built in service.New would size its semaphore from the
// manager's default pool instead of the configured value.
func (svc *Service) AgentResumer() *AgentResumer {
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

// claim reserves one agent id against concurrent resume work, and reports
// whether the caller got it. Both producers -- Schedule and the boot sweep --
// claim through this one set, so an id that one is already working on is never
// started twice.
//
// It refuses every id once Stop latched, so a claim also answers "may I still
// start work?".
//
// wg, when set, is joined UNDER the same lock that reads r.stopped. That
// pairing is the whole reason the caller does not add to the group itself:
// Stop latches r.stopped and then waits on the group, so an Add issued after
// the lock was released could land after that Wait began -- the WaitGroup
// misuse that reuse-after-Wait panics on, and, before it panics, a resume the
// drain does not cover.
func (r *AgentResumer) claim(agentID string, wg *sync.WaitGroup) bool {
	if agentID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	if _, exists := r.scheduled[agentID]; exists {
		return false
	}
	r.scheduled[agentID] = struct{}{}
	if wg != nil {
		wg.Add(1)
	}
	return true
}

func (r *AgentResumer) releaseClaim(agentID string) {
	r.mu.Lock()
	delete(r.scheduled, agentID)
	r.mu.Unlock()
}

// Schedule queues eligible agent rows for a reusable background resume pass.
// It deduplicates one agent while a prior resume remains in flight.
//
// It does NOT block, and it cannot: its callers are the orphan reconciler's
// single goroutine and an RPC handler, and neither may wait for a provider
// handshake. resumeSlots is therefore the cap, and one goroutine per claimed
// id parks on it. The boot sweep wants the opposite -- ordered admission with
// backpressure at the loop -- so it keeps its own errgroup and shares only the
// claim.
func (r *AgentResumer) Schedule(ctx context.Context, agentIDs []string) {
	for _, agentID := range agentIDs {
		if !r.claim(agentID, &r.scheduledWG) {
			continue
		}
		go func() {
			defer func() {
				r.releaseClaim(agentID)
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
// It DOES wait for the resumes it already accepted -- scheduledWG covers every
// job from both producers, and the sweep joins its own batch on every exit.
// Service.Shutdown waits for exactly the same startups a few lines later
// through AgentStartup.WaitForInFlight, so joining here costs no additional
// time; it only moves the wait earlier, to the point where the scheduler can
// still be observed as running.
//
// Not joining is what made WaitForInFlight unsafe. schedule accepts a
// candidate, the runtime does not start its goroutine yet, and it is therefore
// short of AgentStartup.begin. The drain then saw a zero count and returned,
// and the straggler read a database the caller was about to close.
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
			// Claim through the SAME set Schedule uses. The two producers
			// overlap in production: a pass that unarchives a workspace calls
			// Schedule for those agents and then reports convergence, which
			// starts this sweep, whose query selects the rows that pass just
			// marked active. Without one shared claim each of those agents got
			// two goroutines, and the loser parked on the per-agent lifecycle
			// lock for the length of the winner's provider handshake while
			// holding a permit -- halving the effective resume concurrency on
			// exactly the boot that needed it most.
			if !r.claim(id, nil) {
				return nil
			}
			defer r.releaseClaim(id)
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
	// The candidate list is a SNAPSHOT: ListRootAgentIDsForResume ran once, and
	// the sweep reaches a candidate at the back of a throttled queue much
	// later -- minutes, on a machine with many tabs and a concurrency of 1. A
	// CloseAgent in between already ran that tab's whole teardown, so a process
	// started for it now is one nothing will ever stop: it holds a CLI and its
	// memory for the life of the worker, under a tab no client can see. This
	// row was read AFTER the list, which is what makes the check worth having.
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
