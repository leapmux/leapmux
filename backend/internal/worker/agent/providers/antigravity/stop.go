package antigravity

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.Mu.Lock()
	var states []*cascadeState
	for _, state := range a.cascades {
		states = append(states, state)
	}
	a.Mu.Unlock()
	a.finishCascades(states, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	a.approvalWork.CloseAndWait()
	a.Process.Stop()
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	a.awaitStreams(antigravityStopTimerTag)
	providerkit.RemoveAgentDir("antigravity", a.AgentID(), a.agentDir)
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.awaitStreams(antigravityStopTimerTag)
	a.Mu.Lock()
	var states []*cascadeState
	for _, state := range a.cascades {
		states = append(states, state)
	}
	a.Mu.Unlock()
	a.finishCascades(states, a.ProcessExitCompletion(), agent.IncompleteTaskStatus(a.ProcessExitCompletion()))
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	providerkit.RemoveAgentDir("antigravity", a.AgentID(), a.agentDir)
	return err
}
