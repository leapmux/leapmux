package codewhale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start runs one Codewhale runtime for the agent and opens its thread.
//
// A fresh agent gets a fresh store and a new thread. A resume finds the store
// that holds the thread, ends a runtime that a dead worker left on it, and
// resumes the thread there. A resume that fails is fatal: see
// providerkit.ResumeFailedError.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return startWith(ctx, opts, sink, quartz.NewReal())
}

func startWith(ctx context.Context, opts agent.Options, sink agent.ProviderServices, clock quartz.Clock) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	root := codewhaleStoresRoot(agent.StoredSessionQuery{HomeDir: opts.HomeDir})

	var store codewhaleStore
	fresh := opts.ResumeSessionID == ""
	if fresh {
		if store, err = newCodewhaleStore(root); err != nil {
			return nil, err
		}
	} else {
		threadID, err := (codewhaleProvider{}).ResolveResumeHandle(opts.ResumeSessionID, opts.HomeDir)
		if err != nil {
			return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, fmt.Errorf("the stored Codewhale thread id is not valid: %w", err))
		}
		if store, err = findCodewhaleStore(root, threadID); err != nil {
			return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
		if err := reclaimStore(ctx, store, clock); err != nil {
			return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
	}

	token, err := providerkit.NewServerSecret()
	if err != nil {
		return nil, err
	}
	launched, err := launchRuntime(ctx, sink, runtimeLaunch{opts: opts, spec: spec, store: store, token: token, clock: clock})
	if err != nil {
		discardFreshStore(fresh, store)
		return nil, err
	}
	a := launched.agent
	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	if a.runtime, err = a.readRuntimeInfo(); err != nil {
		cleanup()
		discardFreshStore(fresh, store)
		return nil, a.FormatStartupError("runtime info", err)
	}
	slog.Debug("codewhale runtime ready", "agent_id", a.AgentID(), "version", a.runtime.CodewhaleVersion, "store", store.dir)

	thread, err := a.openThread(opts)
	if err != nil {
		cleanup()
		if fresh {
			discardFreshStore(fresh, store)
			return nil, a.FormatStartupError("thread open", err)
		}
		return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
	}
	detail, err := a.readThread(thread.ID)
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError("thread read", err)
	}
	a.Mu.Lock()
	a.threadID = thread.ID
	a.applyThreadRecordLocked(detail.Thread)
	if fresh && agent.UsesAccountDefaultModel(opts.Model()) {
		// The runtime chose the model, so the thread's model is the provider's
		// default.
		a.settings.defaultModel = a.settings.model
	}
	a.lastSeq = detail.LatestSeq
	a.settings.effort = opts.Effort()
	for _, turn := range detail.Turns {
		// A thread the runtime still runs a turn on -- one a dead worker left, which
		// the runtime recovered -- is busy until that turn ends.
		if turn.Status == turnStatusInProgress || turn.Status == turnStatusQueued {
			a.turnID = turn.ID
		}
	}
	a.Mu.Unlock()

	// Everything below reads the runtime, and no failure in it fails the start.
	a.runtime.hasContextRoute = a.probeContextRoute(thread.ID)
	a.refreshModelCatalog()
	if !fresh {
		a.syncGoalSnapshot(thread.ID)
	}

	a.startEventStream(a.Context())
	a.sink.UpdateSessionID(thread.ID)
	a.sink.BroadcastStatusActive(thread.ID)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.PublishTurnActive()
	a.refreshContextUsage()
	return a, nil
}

// newAgent builds an agent that has no process yet.
func newAgent(opts agent.Options, sink agent.ProviderServices, clock quartz.Clock) *Agent {
	return &Agent{
		sink:       agent.NewModelProgressResetSink(sink),
		workingDir: opts.WorkingDir,
		clock:      clock,
	}
}

// openThread creates the agent's thread, or resumes the stored one, with the
// settings the launch asked for.
func (a *Agent) openThread(opts agent.Options) (threadRecord, error) {
	mode := opts.Get(contracts.CodewhaleOptionMode)
	posture := opts.PermissionMode()
	if !postureIsKnown(posture) {
		posture = ""
	}
	if opts.ResumeSessionID == "" {
		request := createThreadRequest{
			Workspace:         opts.WorkingDir,
			Mode:              mode,
			PermissionPosture: posture,
			AllowShell:        true,
		}
		if model := opts.Model(); !agent.UsesAccountDefaultModel(model) {
			request.Model = model
		}
		return a.createThread(request)
	}
	thread, err := a.resumeThread(opts.ResumeSessionID)
	if err != nil {
		return threadRecord{}, err
	}
	// A resumed thread keeps the settings it last ran with. The launch states the
	// settings LeapMux stored for the agent, which are the reader's latest
	// choice, so they are applied over the thread's own.
	var update updateThreadRequest
	patched := false
	if model := opts.Model(); !agent.UsesAccountDefaultModel(model) && model != thread.Model {
		update.Model = &model
		patched = true
	}
	if mode != "" && mode != thread.Mode {
		update.Mode = &mode
		patched = true
	}
	if posture != "" && posture != thread.PermissionPosture {
		update.PermissionPosture = &posture
		patched = true
	}
	if !patched {
		return thread, nil
	}
	updated, err := a.updateThread(thread.ID, update)
	if err != nil {
		// The thread runs on its own settings, which the snapshot reports, and the
		// reader can change them again.
		slog.Warn("codewhale apply launch settings to a resumed thread", "agent_id", a.AgentID(), "error", err)
		return thread, nil
	}
	return updated, nil
}

// probeContextRoute reports whether the runtime serves the thread's context
// route, which exists from 0.10.0. The runtime's capability map does not state
// route additions, so the route itself answers.
func (a *Agent) probeContextRoute(threadID string) bool {
	raw, err := a.readThreadContext(threadID)
	if err != nil {
		return false
	}
	a.applyContextReport(raw)
	return true
}

// syncGoalSnapshot reports the goal a resumed thread already holds. It RESTATES
// the goal, so it writes no transcript row.
func (a *Agent) syncGoalSnapshot(threadID string) {
	var goal codewhaleGoal
	err := a.call(http.MethodGet, threadPath(threadID, threadRouteGoal), nil, nil, &goal)
	switch {
	case err == nil && goal.Objective != "":
		a.sink.UpsertGoal(agent.GoalUpdate{
			NativeID:        goal.GoalID,
			Objective:       goal.Objective,
			Status:          codewhaleGoalStatus(goal.Status),
			StatusDetail:    goal.Status,
			CreatedAt:       codewhaleGoalTime(goal.CreatedAt),
			TokensUsed:      goal.TokensUsed,
			TokenBudget:     goal.TokenBudget,
			TimeUsedSeconds: goal.TimeUsedSeconds,
			Iterations:      goal.ContinuationCount,
			Snapshot:        true,
		})
	case providerkit.IsHTTPStatus(err, httpStatusNotFound):
		a.sink.ClearGoal(true)
	case err != nil:
		slog.Debug("codewhale read the goal of a resumed thread", "agent_id", a.AgentID(), "error", err)
	}
}

// discardFreshStore removes a store this start made and never used. A store
// that holds a thread is never removed here: a resume may need it.
func discardFreshStore(fresh bool, store codewhaleStore) {
	if !fresh || store.dir == "" {
		return
	}
	if err := os.RemoveAll(store.dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Debug("codewhale remove an unused store", "store", store.dir, "error", err)
	}
}
