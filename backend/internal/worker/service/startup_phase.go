package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// startupCallbacks bundles the per-tab-type hooks that the shared
// startup-phase orchestration (`runStartupPhase0`, `failStartup`) drives.
// Agent and terminal startup differ only in which registry / broadcast
// / persistence functions are wired in; the orchestration around them
// (label broadcast, git-mode rollback, error persistence, fail
// broadcast, registry transition) is identical.
type startupCallbacks struct {
	setMessage        func(label string)
	broadcastStarting func(label string)
	persistError      func(errMsg string)
	broadcastFailed   func(errMsg string)
	registryFail      func(errMsg string)
}

// runStartupPhase0 broadcasts the per-mode label (if any) and executes
// the git-mode mutation. Returns the result (with rollback metadata
// populated iff a mutation partially succeeded before failing) and any
// error.
func (svc *Context) runStartupPhase0(ctx context.Context, plan gitModePlan, cb startupCallbacks) (gitModeResult, error) {
	if label := plan.PhaseLabel(); label != "" {
		cb.setMessage(label)
		cb.broadcastStarting(label)
	}
	return svc.executeGitMode(ctx, plan)
}

// failStartup is the common tail for every failure after the sync
// prologue: optionally show a rollback label, roll back any partial
// git-mode mutation, persist the error, broadcast STARTUP_FAILED, and
// mark the registry failed last so observers see a durable terminal
// state.
func (svc *Context) failStartup(gm gitModeResult, cause error, cb startupCallbacks) {
	if gm.Rollback.HasPartialMutation() {
		if label := rollbackLabelFromRollback(gm.Rollback); label != "" {
			cb.setMessage(label)
			cb.broadcastStarting(label)
		}
		svc.rollbackGitMode(gm)
	}
	errMsg := cause.Error()
	cb.persistError(errMsg)
	cb.broadcastFailed(errMsg)
	cb.registryFail(errMsg)
}

// agentStartupCallbacks wires the agent-specific registry, broadcast
// and persistence hooks into the shared startupCallbacks shape.
// `gitStatus` is forwarded only to the STARTUP_FAILED broadcast; the
// STARTING broadcasts always carry nil (no git status is available at
// label-emission time).
func (svc *Context) agentStartupCallbacks(dbAgent *db.Agent, gitStatus *leapmuxv1.AgentGitStatus) startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { svc.AgentStartup.setMessage(dbAgent.ID, label) },
		broadcastStarting: func(label string) { svc.broadcastAgentStarting(dbAgent, label, nil) },
		persistError:      func(errMsg string) { svc.persistAgentStartupError(dbAgent.ID, errMsg) },
		broadcastFailed:   func(errMsg string) { svc.broadcastAgentFailed(dbAgent, errMsg, gitStatus) },
		registryFail:      func(errMsg string) { svc.AgentStartup.fail(dbAgent.ID, errMsg) },
	}
}

// terminalStartupCallbacks wires the terminal-specific registry,
// broadcast and persistence hooks into the shared startupCallbacks
// shape.
func (svc *Context) terminalStartupCallbacks(terminalID string) startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { svc.TerminalStartup.setMessage(terminalID, label) },
		broadcastStarting: func(label string) { svc.broadcastTerminalStarting(terminalID, label, nil) },
		persistError:      func(errMsg string) { svc.persistTerminalStartupError(terminalID, errMsg) },
		broadcastFailed:   func(errMsg string) { svc.broadcastTerminalFailed(terminalID, errMsg) },
		registryFail:      func(errMsg string) { svc.TerminalStartup.fail(terminalID, errMsg) },
	}
}
