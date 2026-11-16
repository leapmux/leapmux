package kimi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

const (
	kimiPendingAgentBytes          = 16 << 20
	kimiPendingSessionBytes        = 64 << 20
	kimiPendingFrameOverhead       = 256
	kimiTranscriptRecoveryTimerTag = "kimi.transcript.recovery"
)

// takePendingNativeFrames transfers retained frames and releases their byte accounting.
// The caller holds dispatchMu.
func (a *Agent) takePendingNativeFrames(run *kimiRun) []kimiFrame {
	frames := run.pendingNative
	a.pendingNativeBytes -= run.pendingNativeBytes
	run.pendingNative, run.pendingNativeBytes = nil, 0
	return frames
}

func (a *Agent) queueNativeTranscriptFrame(run *kimiRun, frame kimiFrame) {
	bytes := int64(frame.wireSize) + kimiPendingFrameOverhead
	if frame.wireSize == 0 {
		bytes += int64(len(frame.Payload) + len(frame.Raw))
	}
	if len(run.pendingNative) >= kimiMaxPendingEvents || bytes > kimiPendingAgentBytes-run.pendingNativeBytes || bytes > kimiPendingSessionBytes-a.pendingNativeBytes {
		a.takePendingNativeFrames(run)
		run.pendingNativeRecovery = true
		run.nativeRecoveryRevision++
		return
	}
	run.pendingNative = append(run.pendingNative, frame)
	run.pendingNativeBytes += bytes
	a.pendingNativeBytes += bytes
}

func (a *Agent) requireNativeTranscriptRecovery(run *kimiRun) {
	a.takePendingNativeFrames(run)
	run.pendingNativeRecovery = true
	run.nativeRecoveryRevision++
	a.startNativeTranscriptRecovery(run)
}

// startNativeTranscriptRecovery retains output ownership while one worker reads history.
// The caller holds dispatchMu. Unlinked children wait for their parent's reference.
func (a *Agent) startNativeTranscriptRecovery(run *kimiRun) {
	if run.agentID != kimiMainAgentID {
		_, linked := a.children.get(run.agentID)
		if !linked {
			return
		}
	}
	if a.api == nil {
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	publishActivity := !run.turnActive
	run.turnActive = true
	if run.agentID == kimiMainAgentID {
		publishActivity = !a.turnActive || a.turnSteerable
		a.turnActive, a.turnSteerable = true, false
	}
	a.Mu.Unlock()
	if publishActivity {
		if run.agentID == kimiMainAgentID {
			a.PublishTurnActive()
		} else {
			a.publishChildTurn(run, true)
		}
	}
	key := fmt.Sprintf("%q/%q/%p", sessionID, run.agentID, run)
	a.transcriptRecoveryWork.Run(key, func() { a.recoverNativeTranscript(sessionID, run) })
}

func (a *Agent) nativeTranscriptRecoveryCurrent(sessionID string, run *kimiRun) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.Context().Err() == nil && !a.StoppedLocked() && a.sessionID == sessionID && a.runs[run.agentID] == run
}

func (a *Agent) recoverNativeTranscript(sessionID string, run *kimiRun) {
	backoff := time.Second
	for {
		a.dispatchMu.Lock()
		current := a.nativeTranscriptRecoveryCurrent(sessionID, run) && run.pendingNativeRecovery
		revision := run.nativeRecoveryRevision
		a.finishDispatch()
		if !current {
			return
		}
		ctx, cancel := a.requestContext()
		snapshot, err := a.readNativeTranscript(ctx, sessionID, run.agentID)
		cancel()
		if err != nil {
			slog.Warn("recover Kimi transcript", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "error", err)
			timer := a.clock.NewTimer(backoff, kimiTranscriptRecoveryTimerTag)
			select {
			case <-a.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"agent_id": run.agentID, "seq": snapshot.Seq,
			"snapshot": map[string]any{"items": snapshot.Items, "interactions": snapshot.Interactions, "attachments": snapshot.Attachments, "prompts": snapshot.Prompts, "hasMoreOlder": false},
		})
		if err != nil {
			slog.Error("encode recovered Kimi transcript", "agent_id", a.AgentID(), "error", err)
			return
		}
		a.dispatchMu.Lock()
		if !a.nativeTranscriptRecoveryCurrent(sessionID, run) {
			a.finishDispatch()
			return
		}
		if revision != run.nativeRecoveryRevision {
			a.finishDispatch()
			continue
		}
		pending := a.takePendingNativeFrames(run)
		run.pendingNativeRecovery = false
		a.handleTranscriptReset(kimiFrame{Type: contracts.KimiEventTranscriptReset, SessionID: sessionID, Payload: payload})
		for _, frame := range pending {
			var position struct {
				Seq *int64 `json:"seq"`
			}
			if json.Unmarshal(frame.Payload, &position) != nil || position.Seq == nil || *position.Seq <= *snapshot.Seq {
				continue
			}
			if frame.Type == contracts.KimiEventTranscriptReset {
				a.handleTranscriptReset(frame)
			} else {
				a.handleTranscriptOperations(frame)
			}
		}
		repeat := run.pendingNativeRecovery
		a.finishDispatch()
		if !repeat {
			return
		}
	}
}
