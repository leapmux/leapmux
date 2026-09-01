package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// remintControlIPC retires the previous spawn's LEAPMUX_CONTROL_* token and
// mints a fresh one, for a relaunch of a tab whose row already exists. It
// returns the env vars the new process must carry and whether a cleanup is now
// registered under tabID.
//
// **The old token is retired FIRST.** controlipc.DefaultSocketPath is a pure
// function of (worker id, kind, tab id), so the relaunch's socket path is the
// one the previous spawn is still listening on -- and locallisten refuses a
// path whose socket answers a dial ("... is already in use"). Minting first
// therefore fails EVERY relaunch inside one worker process, degradably: the
// factory error is not ErrMissingIdentity, so the tab starts with no
// LEAPMUX_CONTROL_* at all and `leapmux control` inside it reports "socket not
// configured". A stale socket file left by a DEAD worker process is not this
// case -- locallisten unlinks one whose dial is refused.
//
// Retiring first opens the window a concurrent close can land in, which is why
// cleanupRegistry.replace does both halves at once: it claims tabID as it
// retires, so a close that arrives during the mint leaves a mark and register
// then retires the fresh resource immediately instead of storing a cleanup
// nobody would ever run. abandonClaim clears the claim when the factory hands
// back no cleanup, so a degraded mint cannot strand one and make the NEXT close
// a silent no-op.
func (svc *Service) remintControlIPC(
	reg *cleanupRegistry,
	kind, tabID, phase string,
	call func() ([]string, func(), error),
) (envs []string, owned bool, err error) {
	if svc.ControlIPC == nil {
		return nil, false, nil
	}
	reg.replace(tabID)

	var newCleanup func()
	envs, err = svc.spawnControlIPC(kind, tabID, phase, func(_ string, fn func()) {
		newCleanup = fn
	}, call)
	// Ownership is what was REGISTERED, not what the env vars imply: the two
	// come from different values, and a factory result with one and not the
	// other would leave a cleanup nobody retires.
	if newCleanup != nil {
		reg.register(tabID, newCleanup)
		owned = true
	} else {
		reg.abandonClaim(tabID)
	}
	if err != nil {
		return nil, owned, err
	}
	return envs, owned, nil
}

// remintAgentControlIPC is remintControlIPC for an agent relaunch. It returns
// the env vars the new process must carry, or nil when remote control is
// disabled or the factory failed degradably.
//
// Every relaunch needs this, because the open path's env vars are a LOCAL in
// the OpenAgent handler and nothing keeps a copy. Without it the four relaunch
// paths -- resume, /clear, a settings change the CLI only honors on a fresh
// launch, and a plan execution -- all produced a process with no
// LEAPMUX_CONTROL_* at all, so `leapmux control` inside that agent reported
// "socket not configured" and every cross-agent call from it failed. That is
// the whole capability the boot-time resume sweep exists to restore.
//
// The user is the worker's registrant. Every agent handler is registered
// ownerOnly, so the caller id the open path passes IS this value; the relaunch
// paths have no caller at all, and this is the only authority available to
// them. A zero owner therefore fails the relaunch rather than starting the
// agent as nobody -- see spawnControlIPC, whose ErrMissingIdentity rule this
// inherits.
//
// No deferred retire, unlike the terminal restart: an agent whose relaunch
// fails keeps its tab open and retries on the next message, and that retry
// re-mints through here, which retires this token. The registered cleanup is
// the tab's CURRENT socket either way.
func (svc *Service) remintAgentControlIPC(agentID, workingDir string, provider leapmuxv1.AgentProvider, phase string) ([]string, error) {
	owner := svc.RegisteredBy()
	envs, _, err := svc.remintControlIPC(&svc.agentCleanups, "agent", agentID, phase, func() ([]string, func(), error) {
		return svc.ControlIPC.AgentSpawning(AgentSpawnInfo{
			UserID:        owner,
			WorkerID:      svc.WorkerID,
			TabID:         agentID,
			WorkingDir:    workingDir,
			AgentProvider: agentlabels.CLIAlias(provider),
		})
	})
	return envs, err
}
