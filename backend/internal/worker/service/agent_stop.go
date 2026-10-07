package service

import (
	"errors"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

var errStopTargetChanged = errors.New("the stopped turn no longer owns the process")

type forceStopResult struct {
	done chan struct{}
	err  error
}

type forceStopOwner struct {
	target agent.StopTarget
	scope  *activityScope
}

// forceStopAgentTurn shares the actual result of one process replacement.
// A captured target prevents an old stop from replacing a newer process or turn.
func (svc *Service) forceStopAgentTurn(dbAgent db.Agent, target agent.StopTarget, stop *agentStopRequest) error {
	result := &forceStopResult{done: make(chan struct{})}
	owner := forceStopOwner{target: target, scope: stop.scope}
	actual, inFlight := svc.forceStops.LoadOrStore(owner, result)
	if inFlight {
		other := actual.(*forceStopResult)
		<-other.done
		return other.err
	}
	defer func() {
		close(result.done)
		svc.forceStops.CompareAndDelete(owner, result)
	}()
	current := func() bool { return target.IsCurrent() && stop.IsCurrent() }
	resumeSessionID, err := svc.restartAgentPreservingSession(dbAgent, storedRestartOptions, forcedStopMessages, restartTurnEndPending, current)
	if errors.Is(err, errStopTargetChanged) {
		return nil
	}
	result.err = err
	if err != nil {
		return err
	}
	slog.Info("agent force-stopped by replacement", "agent_id", dbAgent.ID, "resume_session_id", resumeSessionID)
	return nil
}
