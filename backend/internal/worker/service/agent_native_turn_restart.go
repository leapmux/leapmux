package service

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// scheduleNativeTurnRestart keeps the turn marker until a replacement process
// takes the queued input. A duplicate end report joins the first replacement.
func (svc *Service) scheduleNativeTurnRestart(agentID string) bool {
	if svc.Agents == nil || !svc.Agents.NativeTurnRestartRequired(agentID) {
		return false
	}
	svc.nativeTurnRestartMu.Lock()
	defer svc.nativeTurnRestartMu.Unlock()
	if svc.shuttingDown.Load() {
		return false
	}
	if _, running := svc.nativeTurnRestarts[agentID]; running {
		return true
	}
	if svc.nativeTurnRestarts == nil {
		svc.nativeTurnRestarts = make(map[string]struct{})
	}
	svc.nativeTurnRestarts[agentID] = struct{}{}
	svc.nativeTurnRestartWG.Add(1)
	go svc.restartAfterNativeTurn(agentID)
	return true
}

func (svc *Service) restartAfterNativeTurn(agentID string) {
	defer func() {
		svc.nativeTurnRestartMu.Lock()
		delete(svc.nativeTurnRestarts, agentID)
		svc.nativeTurnRestartMu.Unlock()
		svc.nativeTurnRestartWG.Done()
	}()

	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		// A missing row may mean the tab closed during the turn-end report. A
		// database fault leaves the queue held instead of sending to the old CLI.
		slog.Warn("read agent for native mode restart failed", "agent_id", agentID, "error", err)
		if _, pauseErr := svc.InputQueue.Pause(bgCtx(), agentID,
			leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT); pauseErr != nil {
			slog.Warn("pause input after native mode restart read failed", "agent_id", agentID, "error", pauseErr)
			return
		}
		if _, endErr := svc.InputQueue.TurnEnded(bgCtx(), agentID); endErr != nil {
			slog.Warn("end turn after native mode restart read failed", "agent_id", agentID, "error", endErr)
		}
		return
	}
	resumeSessionID, err := svc.restartAgentPreservingSession(dbAgent, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	if err != nil {
		return
	}
	slog.Info("agent restarted with native mode", "agent_id", agentID, "resume_session_id", resumeSessionID)
}

var nativeTurnRestartMessages = restartMessages{
	pauseFailedLog:      "failed to pause input for a native plan mode restart",
	finishFailedLog:     "failed to finish the queue pause after a native plan mode restart",
	restartFailedLog:    "failed to restart the native plan mode process",
	pauseFailedNotice:   "Failed to apply the native plan mode because the input queue could not pause: ",
	restartFailedNotice: "Failed to restart the agent with the native plan mode: ",
}
