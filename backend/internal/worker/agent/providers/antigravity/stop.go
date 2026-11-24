package antigravity

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) Stop() { a.stop(nil, true) }

func (a *Agent) stop(expected *cascadeState, intentional bool) {
	a.Mu.Lock()
	if expected != nil && (a.root != expected || expected.generation != a.generation) {
		a.Mu.Unlock()
		return
	}
	a.generation++
	a.sessionSwitching = true
	cancel, group := a.streamCancel, a.streamWG
	states := a.cascadeSnapshotLocked()
	a.Mu.Unlock()
	if intentional {
		a.NoteIntentionalStop()
	}
	if cancel != nil {
		cancel()
	}
	a.awaitStreams(group, antigravityStopTimerTag)
	a.accountRefreshWork.CloseAndWait()
	a.approvalWork.CloseAndWait()
	completion := a.ProcessExitCompletion()
	a.finishCascades(states, completion, agent.IncompleteTaskStatus(completion))
	a.Process.Stop()
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	providerkit.RemoveAgentDir("antigravity", a.AgentID(), a.agentDir)
}

func (a *Agent) ProcessExitCompletion() agent.MessageCompletion {
	a.Mu.Lock()
	failed := a.fatalStreamErr != nil
	a.Mu.Unlock()
	if failed {
		return agent.MessageCompletionError
	}
	return a.Process.ProcessExitCompletion()
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.Mu.Lock()
	a.generation++
	a.sessionSwitching = true
	cancel, group := a.streamCancel, a.streamWG
	states := a.cascadeSnapshotLocked()
	a.Mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.awaitStreams(group, antigravityStopTimerTag)
	a.accountRefreshWork.CloseAndWait()
	a.approvalWork.CloseAndWait()
	a.finishCascades(states, a.ProcessExitCompletion(), agent.IncompleteTaskStatus(a.ProcessExitCompletion()))
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	providerkit.RemoveAgentDir("antigravity", a.AgentID(), a.agentDir)
	return err
}

func (a *Agent) cascadeSnapshotLocked() []*cascadeState {
	states := make([]*cascadeState, 0, len(a.cascades))
	for _, state := range a.cascades {
		states = append(states, state)
	}
	return states
}
