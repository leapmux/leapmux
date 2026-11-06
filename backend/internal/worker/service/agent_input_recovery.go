package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// This file owns the provider-neutral recovery that runs BEFORE native input
// delivery: a provider that reports its process unable to accept input
// (agent.InputProcessPreparer) gets that process replaced through the existing
// cold-start path, and the input reaches the replacement, or nothing.
//
// The one behavior this file must NOT grow is provider knowledge. Codex's
// account failures live in the Codex provider; everything here works from the
// interface, the store, and the activity state alone.

// ensureAgentReadyForInput starts an absent process and asks a running one to
// prepare for input. It is the readiness step every root input dispatch passes
// through, including when a process is already alive: a process whose
// authentication state cannot recover must be replaced before it receives
// anything, and only the provider can state that fact.
//
// A provider that does not implement agent.InputProcessPreparer keeps today's
// behavior -- an alive process receives input with no preparation step.
//
// At most one replacement happens per call: the replacement itself is prepared,
// and a preparation that still fails is the answer. A loop here would restart a
// signed-out tab on every queue drain.
func (svc *Service) ensureAgentReadyForInput(agentID string) error {
	if err := svc.ensureAgentRunning(agentID, resumeIfConversation, queuedStart); err != nil {
		return err
	}
	return svc.prepareAgentInputProcess(agentID, true)
}

// prepareAgentInputProcess calls the provider's preparation and replaces its
// process when preparation requires one. allowReplacement limits a dispatch to
// a single replacement: the replacement's own preparation runs with it false.
func (svc *Service) prepareAgentInputProcess(agentID string, allowReplacement bool) error {
	// Resolve the provider under the lifecycle lock, then release that lock
	// before preparation can wait for a native response. A close or a shutdown
	// must stay able to stop the process while an account read is in flight.
	provider, release := svc.Agents.LockProvider(agentID)
	if provider == nil {
		release()
		return notReadyInput(fmt.Errorf("%w: %s", agent.ErrAgentNotFound, agentID))
	}
	preparer, supports := provider.(agent.InputProcessPreparer)
	if !supports {
		release()
		return nil
	}
	target, err := svc.Agents.CaptureStopTarget(agentID)
	release()
	if err != nil {
		return notReadyInput(err)
	}
	err = preparer.PrepareInputProcess()
	unlock := svc.Agents.LockAgent(agentID)
	current := target.IsCurrent()
	unlock()
	if !current {
		return notReadyInput(errors.New("the agent process changed while its input preparation was checked; retry the input"))
	}
	if err == nil {
		return nil
	}
	if !errors.Is(err, agent.ErrInputProcessRestartRequired) {
		return err
	}
	if !allowReplacement {
		return err
	}
	if err := svc.stopStaleInputProcess(agentID, target); err != nil {
		return err
	}
	// The stale process stopped under the lifecycle lock, which has now
	// released; the cold start takes the lock itself.
	if err := svc.ensureAgentRunning(agentID, resumeStoredSession, queuedStart); err != nil {
		// The stale process is gone and the tab has no process. The stored
		// thread ID survives (the launch failed; nothing cleared it), the
		// resume intent persisted below the stop makes the next attempt resume
		// it, and the input stays in the queue for that retry. No loop: say
		// what happened.
		slog.Error("input recovery: the replacement process did not start", "agent_id", agentID, "error", err)
		return err
	}
	slog.Info("input recovery: replaced the agent process", "agent_id", agentID)
	return svc.prepareAgentInputProcess(agentID, false)
}

// stopStaleInputProcess stops the process that reported itself unable to accept
// input, under this agent's lifecycle lock, after rechecks that the manager
// still holds that provider and that nothing blocks the replacement. It also
// persists the resume intent that makes the replacement, and every later
// attempt, resume the same stored native session.
//
// The caller starts the replacement AFTER this returns, once the lifecycle lock
// has released: the cold start takes that lock itself.
func (svc *Service) stopStaleInputProcess(agentID string, target agent.StopTarget) error {
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()

	// Registration identity also works for provider values containing slices,
	// maps, or functions. Agent interface values need not be comparable.
	if !target.IsCurrent() {
		return notReadyInput(errors.New("the agent process changed while its input recovery was checked; retry the input"))
	}
	row, err := svc.readOpenAgentForLaunch(agentID)
	if err != nil {
		return err
	}
	if blocked := svc.inputRecoveryBlocked(agentID); blocked != nil {
		return blocked
	}

	// Persist the resume intent BEFORE the stop. A launch that fails after it
	// leaves no runtime in memory, and the persisted intent is what makes the
	// later ordinary retry, or a Worker restart's resume sweep, select the same
	// stored thread. The write is conditional on the current stored session and
	// an open tab, so a session or tab that changed while this recovery waited
	// affects zero rows and stops nothing.
	resumeSessionID := storedSessionID(row)
	if resumeSessionID == "" {
		return fmt.Errorf("agent %s has no stored native session to recover; clear the context or restart the agent", agentID)
	}
	changed, err := svc.Queries.MarkAgentResumedForInputRecovery(bgCtx(), db.MarkAgentResumedForInputRecoveryParams{
		AgentID: agentID, AgentSessionID: resumeSessionID,
	})
	if err != nil {
		return notReadyStoreFault(fmt.Errorf("record the input recovery resume intent: %w", err))
	}
	if changed == 0 {
		return notReadyInput(errors.New("the agent's stored session changed while its input recovery was checked; retry the input"))
	}

	// Recheck ownership and every blocker immediately before the destructive
	// stop. Work that started or a control that appeared since the first check
	// must survive, and control-answer delivery must stay available: clearing
	// controls to make recovery possible would strand the process they wait on.
	if !target.IsCurrent() {
		return notReadyInput(errors.New("the agent process changed while its input recovery was checked; retry the input"))
	}
	if blocked := svc.inputRecoveryBlocked(agentID); blocked != nil {
		return blocked
	}

	// Stop only this tab's process and wait for its exit callback and cleanup
	// to finish, so the replacement can never race the old process's teardown
	// (see Manager.StopAndWaitAgent). An explicit stop does not pause the
	// queue, and the dispatch that reached here owns the turn, so the input
	// this recovery serves stays deliverable once the replacement registers.
	svc.Agents.StopAndWaitAgent(agentID)
	return nil
}

// inputRecoveryBlocked refuses a process replacement while the affected
// process still owns work or unanswered controls. Display activity alone is
// not admission: an idle main turn does not prove that its subagents and
// background commands are idle, and a pending stop can make display activity
// idle before the provider confirms its turn ended, so the raw turn flag and
// the task count are read independently of the displayed state.
//
// A read failure is an answer, not an empty list: never infer that child
// controls are absent from a query that did not run. Every refusal keeps the
// input in the queue and tells the reader what stands in the way.
func (svc *Service) inputRecoveryBlocked(agentID string) error {
	if svc.Output.TurnActive(agentID) {
		return fmt.Errorf("agent %s still runs a turn; finish or stop it before the agent can recover", agentID)
	}
	if svc.Output.AgentActivitySnapshot(agentID, agentID).ActiveTasks > 0 {
		return fmt.Errorf("agent %s still runs a background task; finish or stop it before the agent can recover", agentID)
	}
	owner, err := svc.Queries.FindAgentInputRecoveryControlOwner(bgCtx(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return notReadyStoreFault(fmt.Errorf("read the pending controls for input recovery: %w", err))
	}
	return fmt.Errorf("agent %s still waits on an unanswered control; answer or stop it before the agent can recover", owner)
}
