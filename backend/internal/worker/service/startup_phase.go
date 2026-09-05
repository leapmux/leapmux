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
	// closeDisposition reports what a close that landed on this startup
	// decided about the worktree, and whether such a close happened at all.
	// failStartup needs both: see rollbackGitModeAfterStartup. Read through
	// closeRaced rather than called directly.
	closeDisposition func() (closeWorktreeDisposition, bool)
	// archiveStopped reports whether an archive cancelled this startup, and
	// phase0Complete whether its git-mode mutation is already linked to the
	// tab. Two hooks rather than one pair, because the two facts are unrelated
	// and only failStartup reads the second.
	archiveStopped func() bool
	phase0Complete func() bool
}

// closeRaced reports the decision a close recorded against this startup.
//
// A nil hook means the caller wired no registry -- only a hand-built test
// callback set does -- and reports "no close raced". That is the conservative
// answer: it leaves the failing startup owning its own rollback, so a partial
// mutation is never left behind by a callback set that simply forgot the hook.
func (cb startupCallbacks) closeRaced() (closeWorktreeDisposition, bool) {
	if cb.closeDisposition == nil {
		return keepWorktreeOnClose, false
	}
	return cb.closeDisposition()
}

// archived reports whether an archive cancelled this startup. A nil hook means
// a hand-built test callback set wired none, and reports "no archive raced".
func (cb startupCallbacks) archived() bool {
	if cb.archiveStopped == nil {
		return false
	}
	return cb.archiveStopped()
}

// pastPhase0 reports whether phase 0 finished and linked its worktree.
func (cb startupCallbacks) pastPhase0() bool {
	if cb.phase0Complete == nil {
		return false
	}
	return cb.phase0Complete()
}

// runStartupPhase0 broadcasts the per-mode label (if any) and executes
// the git-mode mutation. Returns the result (with rollback metadata
// populated iff a mutation partially succeeded before failing) and any
// error.
func (svc *Service) runStartupPhase0(ctx context.Context, plan gitModePlan, cb startupCallbacks) (gitModeResult, error) {
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
//
// "Failure" here includes a startup that was CANCELLED by a close, which is
// the common shape rather than the rare one: closeTabCommon calls stopProcess
// (and so cancelAndClear) before closeDB, so the cancellation usually surfaces
// as an error out of phase 0 or startAgent well before closed_at is readable.
// Those arrive here, not on the close-detected branch, which is why the
// rollback has to consult the disposition on this path too.
func (svc *Service) failStartup(gm gitModeResult, cause error, cb startupCallbacks) {
	_, closeRaced := cb.closeRaced()
	if cb.archived() && !closeRaced {
		if gm.Rollback.HasPartialMutation() && !cb.pastPhase0() {
			svc.rollbackGitMode(gm)
		}
		return
	}
	if gm.Rollback.HasPartialMutation() {
		if label := rollbackLabelFromRollback(gm.Rollback); label != "" {
			cb.setMessage(label)
			cb.broadcastStarting(label)
		}
		disposition, raced := cb.closeRaced()
		svc.rollbackGitModeAfterStartup(gm, disposition, raced)
	}
	errMsg := cause.Error()
	cb.persistError(errMsg)
	cb.broadcastFailed(errMsg)
	cb.registryFail(errMsg)
}

// linkWorktreeAfterPhase0 performs the phase-0 worktree link for a tab whose
// git-mode mutation just succeeded, honouring any close that has already landed
// on this startup. Shared by runAgentStartup / runTerminalStartup so the
// skip-vs-link decision cannot drift between them.
//
// closedInDB comes from the caller's post-phase-0 re-read of the tab row (the
// query differs per tab type: getAgentByID vs GetTerminalForReady). `raced`
// comes from the registry and closes a window that read cannot see:
// closeTabCommon runs stopProcess -- and so cancelAndClear, which records the
// disposition -- BEFORE closeDB writes closed_at, so a close landing in that gap
// reads as not-closed here. Linking on that read would write a worktree_tabs row
// after the close's own unregisterTab had already deleted the tab's links,
// stranding a worktree whose ref-count never reaches zero.
func (svc *Service) linkWorktreeAfterPhase0(reg *startupCore, h *startupEntry, worktreeID string, tabType leapmuxv1.TabType, tabID string, closedInDB bool) {
	disposition, raced := reg.dispositionOf(h)
	svc.registerTabForWorktreeAfterClose(worktreeID, tabType, tabID, closedInDB || raced, disposition)
	reg.markPhase0Complete(h)
}

// finishStartupAfterClose is the tail both startup goroutines run when their
// post-spawn re-read shows the tab was closed while they were spawning: retire
// the registry entry, then apply the close's own worktree decision.
//
// The disposition is re-read here rather than reused from phase 0 because the
// close this branch detected almost always lands DURING phase 2, i.e. after
// that read. Reusing the earlier value would report the zero value (keep) for
// every such close and silently skip a removal the user confirmed.
func (svc *Service) finishStartupAfterClose(reg *startupCore, h *startupEntry, tabID string, gm gitModeResult) {
	// Identity-guarded: this tail also runs for a close that landed AFTER the
	// goroutine's own succeed, so the id can already belong to a newer startup.
	reg.succeed(tabID, h)
	disposition, _ := reg.dispositionOf(h)
	svc.rollbackGitModeAfterClose(gm, disposition)
}

// agentStartupCallbacks wires the agent-specific registry, broadcast
// and persistence hooks into the shared startupCallbacks shape.
// `gitStatus` is forwarded only to the STARTUP_FAILED broadcast; the
// STARTING broadcasts always carry nil (no git status is available at
// label-emission time).
func (svc *Service) agentStartupCallbacks(dbAgent *db.Agent, gitStatus *leapmuxv1.GitRepoStatus, h *startupEntry) startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { svc.AgentStartup.setMessage(dbAgent.ID, label) },
		broadcastStarting: func(label string) { svc.broadcastAgentStarting(dbAgent, label, nil) },
		persistError:      func(errMsg string) { svc.persistAgentStartupError(dbAgent.ID, errMsg) },
		broadcastFailed:   func(errMsg string) { svc.broadcastAgentFailed(dbAgent, errMsg, gitStatus) },
		registryFail:      func(errMsg string) { svc.AgentStartup.fail(h, errMsg) },
		closeDisposition: func() (closeWorktreeDisposition, bool) {
			return svc.AgentStartup.dispositionOf(h)
		},
		archiveStopped: func() bool { return svc.AgentStartup.archiveStopped(h) },
		phase0Complete: func() bool { return svc.AgentStartup.phase0Complete(h) },
	}
}

// terminalStartupCallbacks wires the terminal-specific registry,
// broadcast and persistence hooks into the shared startupCallbacks
// shape.
func (svc *Service) terminalStartupCallbacks(terminalID string, h *startupEntry) startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { svc.TerminalStartup.setMessage(terminalID, label) },
		broadcastStarting: func(label string) { svc.broadcastTerminalStarting(terminalID, label, nil) },
		persistError:      func(errMsg string) { svc.persistTerminalStartupError(terminalID, errMsg) },
		broadcastFailed:   func(errMsg string) { svc.broadcastTerminalFailed(terminalID, errMsg) },
		registryFail:      func(errMsg string) { svc.TerminalStartup.fail(h, errMsg) },
		closeDisposition: func() (closeWorktreeDisposition, bool) {
			return svc.TerminalStartup.dispositionOf(h)
		},
		archiveStopped: func() bool { return svc.TerminalStartup.archiveStopped(h) },
		phase0Complete: func() bool { return svc.TerminalStartup.phase0Complete(h) },
	}
}
