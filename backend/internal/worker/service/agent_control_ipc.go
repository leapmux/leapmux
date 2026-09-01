package service

import (
	"context"

	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// tabKind is the kind of tab a control-IPC token belongs to. It carries both
// halves of what a mint needs -- the wire token the factory stamps into the
// socket path, and the cleanup registry the tab's teardown reads -- so a caller
// cannot hand one kind's name to the other kind's registry.
type tabKind string

const (
	tabKindAgent    tabKind = "agent"
	tabKindTerminal tabKind = "terminal"
)

// cleanupsFor answers which registry holds this kind's per-tab cleanups.
func (svc *Service) cleanupsFor(kind tabKind) *cleanupRegistry {
	if kind == tabKindTerminal {
		return &svc.terminalCleanups
	}
	return &svc.agentCleanups
}

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
// That order had a price, which HoldDelegation now pays off. The retire runs the
// previous spawn's cleanup, which releases the per-user delegation; for a user
// whose only live spawn is this one the reference count reached zero, so the
// release revoked the hub token through a blocking call and the next hub-bound
// call from the tab minted a fresh one. Holding one reference across the swap
// keeps the count off zero, so a relaunch costs no hub round trip -- and a mint
// that FAILS still lands at zero and revokes, which is correct, because no spawn
// is left to use the token.
//
// Retiring first opens the window a concurrent close can land in, which is why
// cleanupRegistry.replace does both halves at once: it claims tabID as it
// retires, so a close that arrives during the mint leaves a mark and register
// then retires the fresh resource immediately instead of storing a cleanup
// nobody would ever run. abandonClaim clears the claim when the factory hands
// back no cleanup, so a degraded mint cannot strand one and make the NEXT close
// a silent no-op.
func (svc *Service) remintControlIPC(
	kind tabKind, tabID, phase string, owner userid.UserID,
	call func() ([]string, func(), error),
) (envs []string, owned bool, err error) {
	if svc.ControlIPC == nil {
		return nil, false, nil
	}
	reg := svc.cleanupsFor(kind)
	// Hold the owner's delegation across the whole swap. The retire below
	// releases the previous spawn's reference, and for a user whose only live
	// spawn is this one that reaches zero, which revokes the hub token through a
	// blocking call and makes the next call from the tab mint a fresh one. One
	// reference held here keeps the count off zero, so the retire costs nothing
	// and a failed mint still lands at zero and revokes, as it should.
	defer svc.ControlIPC.HoldDelegation(owner)()
	reg.replace(tabID)

	// The registry sees this mint through the same seam the open paths use, so
	// `owned` means the same thing on both: the registry STORED the cleanup.
	// register declines to store -- and retires the resource on the spot --
	// when it finds the mark a real close left.
	//
	// registered tracks whether register ran at all, which `owned` cannot say on
	// its own. A factory that hands back no cleanup never reaches it, and the
	// claim replace took must then be abandoned: leaving it makes the NEXT close
	// a silent no-op that marks the tab instead of retiring anything.
	registered := false
	envs, owned, err = svc.spawnControlIPC(string(kind), tabID, phase, func(id string, fn func()) bool {
		registered = true
		return reg.register(id, fn)
	}, call)
	if !registered {
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
// It takes the LAUNCH OPTIONS rather than the agent id, the working directory
// and the provider as three parameters, so the socket it mints is scoped to
// exactly the process that is about to carry it. Three loose primitives let a
// caller pair one agent's id with another row's working directory, and every
// caller builds these options through baseAgentOptions anyway.
//
// Every relaunch needs this, because the open path's env vars are a LOCAL in
// the OpenAgent handler and nothing keeps a copy. Without it the relaunch paths
// -- resume, /clear, a settings change the CLI only honors on a fresh launch, a
// plan execution, and the startup-window relaunch -- all produced a process
// with no LEAPMUX_CONTROL_* at all, so `leapmux control` inside that agent
// reported "socket not configured" and every cross-agent call from it failed.
// That is the whole capability the boot-time resume sweep exists to restore.
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
//
// One consequence of retiring first is worth stating, because the order that
// forces it is not negotiable. applySettingsViaRestart is the one caller that
// still has a LIVE process when it calls this -- restartAgentLocked stops it
// afterwards -- so an ErrMissingIdentity here leaves that process running with
// a socket that no longer answers. The caller reports the failure and keeps the
// agent on its current settings; the next relaunch mints again.
func (svc *Service) remintAgentControlIPC(opts agent.Options, phase string) ([]string, error) {
	owner := svc.RegisteredBy()
	envs, _, err := svc.remintControlIPC(tabKindAgent, opts.AgentID, phase, owner, func() ([]string, func(), error) {
		return svc.ControlIPC.AgentSpawning(AgentSpawnInfo{
			UserID:        owner,
			WorkerID:      svc.WorkerID,
			TabID:         opts.AgentID,
			WorkingDir:    opts.WorkingDir,
			AgentProvider: agentlabels.CLIAlias(opts.AgentProvider),
		})
	})
	return envs, err
}

// agentLauncher is the shape of the ways a relaunch reaches a process:
// startAgent for a tab with no live process, restartAgentLocked for one that
// must be stopped first, and startBackgroundAgent for a start nobody waits on.
type agentLauncher func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error)

// startPriority states whether a user is waiting on a cold start. It decides
// one thing: whether the spawn draws on the manager's startup permit pool.
//
// It is a type rather than a bare bool so the four ensureAgentRunning call
// sites read as what they are. Three of them answer a request the user is
// blocked on; the fourth is the boot-time resume sweep, which nobody is waiting
// on and which would otherwise start every restorable tab at once.
type startPriority int

const (
	// interactiveStart is a spawn a user is waiting on. It takes no permit and
	// never queues: the send path calls the cold start INLINE, and the client
	// gives that RPC about fifteen seconds, so a wait would fail a send whose
	// message row is already durable.
	interactiveStart startPriority = iota
	// backgroundStart is a spawn nobody is waiting on. It draws on the permit
	// pool, so the boot sweep cannot run more handshakes at once than the
	// machine was configured for.
	backgroundStart
)

// launcher answers which of the service's two starters this priority uses.
func (p startPriority) launcher(svc *Service) agentLauncher {
	if p == backgroundStart {
		return svc.startBackgroundAgent
	}
	return svc.startAgent
}

// mintAndLaunch mints the relaunch's control socket, hands the env vars to the
// launch options, and starts the process. It returns the provider's confirmed
// settings, or the first error of the two steps.
//
// Every relaunch path goes through it, so the three-step shape -- mint, thread
// ExtraEnv, launch only when the mint succeeded -- is written once. Four call
// sites each re-deriving it is how one of them comes to skip a step: the
// startup-window relaunch did exactly that and inherited the previous spawn's
// socket.
//
// A failed mint returns before the launch, so the caller's failure branch
// handles both errors identically. That matters most for the paths that stopped
// the old process first: a relaunch that started a process with no remote
// control would report success and leave the user with an agent whose
// `leapmux control` fails for a reason nothing names.
func (svc *Service) mintAndLaunch(
	ctx context.Context,
	phase string,
	opts agent.Options,
	sink agent.OutputSink,
	launch agentLauncher,
) (map[string]string, error) {
	confirmed, _, err := svc.mintAndLaunchReportingStep(ctx, phase, opts, sink, launch)
	return confirmed, err
}

// mintAndLaunchReportingStep is mintAndLaunch for a caller that must tell the
// two failures apart, and it reports whether the LAUNCH ran.
//
// The distinction is not cosmetic: a mint that fails returns before the launch,
// so a restarting caller never stopped its old process and that process is still
// serving the tab. A launch that fails ran restartAgentLocked's stop first, so
// the tab has NO process. A caller that treats the two alike either reports a
// healthy agent that is gone, or tears down settings for an agent that is fine.
func (svc *Service) mintAndLaunchReportingStep(
	ctx context.Context,
	phase string,
	opts agent.Options,
	sink agent.OutputSink,
	launch agentLauncher,
) (confirmed map[string]string, launched bool, err error) {
	remoteEnvs, err := svc.remintAgentControlIPC(opts, phase)
	if err != nil {
		return nil, false, err
	}
	opts.ExtraEnv = remoteEnvs
	confirmed, err = launch(ctx, opts, sink)
	return confirmed, true, err
}
