package kimi

import (
	"fmt"
	"log/slog"
)

// Native subscriptions default to main. An exact adopted child therefore
// needs its own agent_ids request before its public conversation can arrive.
func (a *Agent) subscribeChildConversation(run *kimiRun) {
	if a.stream == nil || run.agentID == kimiMainAgentID {
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	current := !a.StoppedLocked() && !a.ProcessExitedLocked() && a.runs[run.agentID] == run
	a.Mu.Unlock()
	if !current {
		return
	}
	if run.conversation != nil && run.conversation.snapshotReady {
		return
	}
	if run.conversationRecovery.Load() == nil {
		a.awaitConversationSnapshot(run)
	}
	stream := a.stream
	key := fmt.Sprintf("conversation-subscription/%q/%q/%p", sessionID, run.agentID, run)
	a.taskRecoveryWork.Run(key, func() {
		a.Mu.Lock()
		current := a.sessionID == sessionID && a.runs[run.agentID] == run && !a.StoppedLocked()
		a.Mu.Unlock()
		if !current {
			return
		}
		ctx, cancel := a.requestContext()
		defer cancel()
		if err := stream.subscribeAgents(ctx, sessionID, []string{run.agentID}); err != nil {
			slog.Warn("subscribe to the Kimi child conversation", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "error", err)
		}
	})
}
