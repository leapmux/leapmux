package service

import (
	"context"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// AgentResumer respawns, once per worker process, the agent subprocesses a
// previous worker process left behind.
//
// It exists because several agent CLIs can list the agent sessions running on
// the same machine and send messages to them. A worker restart kills every
// agent process, and nothing started one again until a human typed into that
// tab -- so after a restart an agent could neither see nor reach its siblings,
// and no amount of waiting fixed it. Respawning the processes is what puts them
// back in their provider's machine-local view of each other.
//
// It is a type rather than a bare method for the same reason OrphanReconciler
// is: the sweep owns a goroutine and a stop, and both have to be drivable from
// a test without standing up bootstrap.
type AgentResumer struct {
	svc *Service

	// startOnce latches the sweep. Start is called from every converged
	// reconciler pass -- which repeats hourly -- and only the first may sweep.
	startOnce sync.Once
	// done closes when the sweep goroutine returns. Nil until the sweep starts,
	// which is why Stop reads it under mu.
	done chan struct{}
	// stop closes on Stop and is what makes the launcher give up on the
	// candidates it has not reached yet. stopOnce guards the close: Shutdown
	// runs it through the background-loop stopper while a test's cleanup may
	// run it again, and a second bare close panics.
	stop     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
}

// NewAgentResumer builds the boot-time resume sweep for this service.
func (svc *Service) NewAgentResumer() *AgentResumer {
	return &AgentResumer{svc: svc, stop: make(chan struct{})}
}

// Start runs the sweep once, in the background. Later calls do nothing.
//
// It REFUSES, without latching, when the worker has no owner yet or is shutting
// down. Not latching is the point: the caller fires this from every converged
// reconciler pass, so a refusal here is retried on the next one rather than
// losing the sweep for the life of the process. An owner is a hard requirement,
// not a nicety -- a resumed agent needs a control socket, and
// remintAgentControlIPC cannot mint one for an unnamed user.
func (r *AgentResumer) Start(ctx context.Context) {
	if r.svc.RegisteredBy().IsZero() {
		slog.Debug("agent resume: no worker owner yet; waiting for the hub to deliver one")
		return
	}
	if r.svc.shuttingDown.Load() {
		return
	}
	r.startOnce.Do(func() {
		done := make(chan struct{})
		r.mu.Lock()
		r.done = done
		r.mu.Unlock()
		go func() {
			defer close(done)
			r.sweep(ctx)
		}()
	})
}

// Stop tells the launcher to reach no further candidates and waits for it.
//
// It does NOT wait for a resume that is already handshaking. Those are
// registered with svc.AgentStartup, so Service.Shutdown's own
// AgentStartup.WaitForInFlight drains them -- the same drain an interactive
// startup goes through. Waiting for them here as well would park Shutdown
// behind a second copy of that wait, before the goodbye broadcasts that
// Shutdown deliberately sends first.
func (r *AgentResumer) Stop() {
	r.stopOnce.Do(func() { close(r.stop) })
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

// sweep resumes every open root agent the user has actually used.
func (r *AgentResumer) sweep(ctx context.Context) {
	svc := r.svc
	ids, err := svc.Queries.ListAllOpenRootAgentIDs(ctx)
	if err != nil {
		slog.Error("agent resume: list open agents", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	limit := agent.ResolveStartupConcurrency(svc.AgentStartupConcurrency)
	slog.Info("agent resume: restoring agent processes after a worker restart",
		"candidates", len(ids), "concurrency", limit)

	var counts resumeCounts
	g := new(errgroup.Group)
	// The manager's own permit pool already caps concurrent startups across
	// every spawn path. Limiting the sweep to the SAME number caps how deep the
	// queue in front of that pool gets: an interactive open then waits behind one
	// generation of startups rather than behind every open tab on the machine.
	g.SetLimit(limit)
	for _, id := range ids {
		// Both early returns leave the resumes already in flight running, and
		// deliberately do not g.Wait() for them: Shutdown calls Stop BEFORE its
		// goodbye broadcasts, and a handshake can park for the whole
		// agent_startup_timeout. Those startups are registered with
		// svc.AgentStartup, so Shutdown's own WaitForInFlight is what joins them,
		// after the broadcasts are away. counts is mutex-guarded, so the summary
		// logged here is a snapshot rather than a race.
		select {
		case <-r.stop:
			slog.Info("agent resume: stopped before the sweep finished", counts.logArgs()...)
			return
		case <-ctx.Done():
			return
		default:
		}
		// errgroup.Go blocks once the limit is reached, so the loop itself is the
		// backpressure and no candidate is dropped.
		g.Go(func() error {
			r.resumeOne(ctx, id, &counts)
			return nil
		})
	}
	// The group never returns an error: one agent that refuses to start must not
	// abandon the rest, so resumeOne records the failure and reports success.
	_ = g.Wait()
	slog.Info("agent resume: sweep complete", counts.logArgs()...)
}

// resumeCounts tallies a sweep's outcomes. Guarded by its own mutex because the
// candidates run concurrently.
type resumeCounts struct {
	mu      sync.Mutex
	resumed int
	skipped int
	failed  int
}

func (c *resumeCounts) add(resumed, skipped, failed int) {
	c.mu.Lock()
	c.resumed += resumed
	c.skipped += skipped
	c.failed += failed
	c.mu.Unlock()
}

func (c *resumeCounts) logArgs() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []any{"resumed", c.resumed, "skipped", c.skipped, "failed", c.failed}
}

// resumeOne restores a single agent, or records why it did not.
func (r *AgentResumer) resumeOne(ctx context.Context, agentID string, counts *resumeCounts) {
	svc := r.svc
	if svc.shuttingDown.Load() {
		counts.add(0, 1, 0)
		return
	}
	dbAgent, err := svc.getAgentByID(ctx, agentID)
	if err != nil {
		slog.Warn("agent resume: fetch agent", "agent_id", agentID, "error", err)
		counts.add(0, 0, 1)
		return
	}
	if reason := r.skipReason(dbAgent); reason != "" {
		slog.Debug("agent resume: skipping agent", "agent_id", agentID, "reason", string(reason))
		counts.add(0, 1, 0)
		return
	}

	// Register the resume with the SAME startup registry an interactive open
	// uses. Three things follow from that and none of them needs its own
	// machinery here: the tab reports STARTING to a client that connects mid
	// sweep, a CloseAgent cancels this resume through cancelAndClear, and
	// Service.Shutdown's AgentStartup.WaitForInFlight drains it.
	// This context is the resumed PROCESS's lifetime, not just its startup's:
	// the provider builds its exec.CommandContext from it. So it must NOT be
	// cancelled on the success path -- a `defer cancel()` here SIGTERMs the agent
	// a moment after it comes up (exit status 143), which reads as a crash with
	// nothing naming the cause. runAgentStartup has the same shape and for the
	// same reason leaves the cancel to a close.
	//
	// Handing it to AgentStartup.begin is what keeps it reachable: three things
	// follow and none needs its own machinery here. The tab reports STARTING to a
	// client that connects mid sweep; a CloseAgent cancels this resume through
	// cancelAndClear, which is where the cancel above is finally called; and
	// Service.Shutdown's AgentStartup.WaitForInFlight drains it.
	startupCtx, cancel := context.WithCancel(ctx)
	svc.AgentStartup.begin(agentID, cancel)
	defer svc.AgentStartup.finish()
	// succeed() on BOTH outcomes, because it only means "drop the override and
	// derive the status from the manager again". fail() would be wrong here: it
	// reports STARTUP_FAILED, which makes the agent permanently unusable until
	// the user opens a new one, and ensureAgentRunning deliberately keeps a
	// failed auto-start retryable on the next message.
	defer svc.AgentStartup.succeed(agentID)

	if err := svc.ensureAgentRunning(startupCtx, agentID, nil); err != nil {
		// No process to keep alive, so release the context here rather than
		// stranding it until the worker exits.
		cancel()
		slog.Warn("agent resume: failed to start agent", "agent_id", agentID, "error", err)
		counts.add(0, 0, 1)
		return
	}
	counts.add(1, 0, 0)
}

// resumeSkipReason names why an agent was passed over, or "" to resume it.
//
// The reason is a value rather than a bool so the sweep can log it. A count
// alone ("skipped 3") tells an operator nothing about whether the worker made
// the right call, and "why did my agent not come back" is the only question
// anybody asks of this feature.
type resumeSkipReason string

const (
	resumeSkipAlreadyRunning resumeSkipReason = "already running"
	resumeSkipStartupFailed  resumeSkipReason = "previous startup failed"
	resumeSkipNeverStarted   resumeSkipReason = "never started and not created from a session"
)

// skipReason reports why this row names an agent NOT worth respawning, or "" to
// resume it.
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
// Code does, on its first turn) it answers false for an agent the user has been
// talking to all along. Every such agent would have been left cold.
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
	// The same permanent-failure gate SendAgentMessage applies. Respawning an
	// agent whose last startup failed would burn a slot on a CLI that is going to
	// fail again, and the row already tells the user to open a new agent.
	if dbAgent.StartupError != "" {
		return resumeSkipStartupFailed
	}
	if dbAgent.AgentSessionID == "" && dbAgent.Resumed == 0 {
		return resumeSkipNeverStarted
	}
	return ""
}
