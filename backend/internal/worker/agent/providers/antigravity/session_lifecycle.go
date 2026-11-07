package antigravity

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, false)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, false)
}

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, true)
}

func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return fmt.Errorf("agent has no Antigravity session")
	}
	return a.sendToState(root, content, attachments, steer, true)
}

func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChild(childKey, content, attachments, false)
}

func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChild(childKey, content, attachments, true)
}

func (a *Agent) sendChild(childKey, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.Mu.Lock()
	if a.sessionSwitching {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	state := a.cascades[childKey]
	if state != nil && state.closed && !steer {
		done, ctx := state.streamDone, a.streamCtx
		a.Mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return agent.ErrChildRouteNotReady
			}
		}
		a.Mu.Lock()
		reopen, reopenErr := a.reopenChildLocked(state, done)
		a.Mu.Unlock()
		if reopenErr != nil {
			return reopenErr
		}
		if reopen {
			// Recounting here can classify new native peer work as history.
			// Startup already established the replay boundary, so reuse it and the acknowledged steps.
			a.startCascadeWorker(state, a.runCascadeStream)
		}
	} else {
		a.Mu.Unlock()
	}

	state, err := a.childState(childKey)
	if err != nil {
		return err
	}
	return a.sendToState(state, content, attachments, steer, false)
}

func (a *Agent) reopenChildLocked(state *cascadeState, completed chan struct{}) (bool, error) {
	if !a.cascadeCurrentLocked(state) || a.sessionSwitching || state.streamDone != completed {
		return false, agent.ErrChildRouteNotReady
	}
	reopen := state.closed
	if reopen {
		state.closed = false
		state.connectErr = nil
		state.connected = make(chan struct{})
		state.connectedOnce = sync.Once{}
	}
	return reopen, nil
}

func (a *Agent) sendToState(state *cascadeState, content string, attachments []*leapmuxv1.Attachment, steer, allowSlash bool) error {
	command := ""
	a.Mu.Lock()
	if allowSlash {
		if name, rest, matched := a.slashCommand(content); matched {
			command, content = name, rest
		}
	}
	a.Mu.Unlock()
	return a.sendToStateCommand(state, content, attachments, steer, command)
}

func (a *Agent) sendToStateCommand(state *cascadeState, content string, attachments []*leapmuxv1.Attachment, steer bool, command string) error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if a.sessionSwitching {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	if state == nil {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	if a.cascades[state.id] != state || state.generation != a.generation || state.parent == nil && a.root != state {
		a.Mu.Unlock()
		if state.parent != nil {
			return agent.ErrChildRouteNotReady
		}
		return agent.ErrInputSessionChanged
	}
	if state.closed || a.streamCtx.Err() != nil {
		child := state.parent != nil
		a.Mu.Unlock()
		if child {
			return agent.ErrChildRouteNotReady
		}
		return fmt.Errorf("the Antigravity event stream is closed")
	}
	if state.turnPhase == cascadeTurnFinalizing || !steer && (state.turnPhase != cascadeTurnIdle || state.sendInFlight) {
		root := state.parent == nil
		a.Mu.Unlock()
		if root {
			a.PublishTurnActive()
		}
		return agent.ErrAgentBusy
	}
	if steer && state.turnPhase != cascadeTurnActive {
		a.Mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	items, media, err := a.buildMessageItems(content, attachments, command)
	if err != nil {
		a.Mu.Unlock()
		return err
	}
	config, err := a.cascadeConfigLocked()
	if err != nil {
		a.Mu.Unlock()
		return err
	}
	state.sendInFlight = true
	seq := a.NextTurnSeq()
	outgoing := a.turnStateLocked(state)
	a.inputWG.Add(1)
	a.Mu.Unlock()
	defer a.inputWG.Done()
	providerkit.PublishTurnStateTo(state.sink, outgoing, seq)
	request := map[string]any{"cascadeId": state.id, "items": items, "media": media, "cascadeConfig": config, "tags": []string{antigravityQueueInputTag}}
	err = a.rpc.sendUserMessage(a.Context(), request)
	a.Mu.Lock()
	if err != nil && state.turnPhase == cascadeTurnIdle {
		state.sendInFlight = false
	}
	errorState := a.turnStateLocked(state)
	errorSeq := a.NextTurnSeq()
	a.Mu.Unlock()
	if err != nil {
		providerkit.PublishTurnStateTo(state.sink, errorState, errorSeq)
		if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			return providerkit.ResumeFailedError(state.id, err)
		}
		return err
	}
	return nil
}

func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	root := a.root
	if root == nil || a.sessionSwitching {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.Mu.Unlock()
	return a.sendToStateCommand(root, "", nil, false, "compact")
}

func (a *Agent) ClearContext() (string, error) {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return "", fmt.Errorf("agent is stopped")
	}
	if a.sessionSwitching {
		a.Mu.Unlock()
		return "", agent.ErrAgentBusy
	}
	a.sessionSwitching = true
	oldRoot := a.root
	admissionGeneration := a.generation
	a.Mu.Unlock()
	newID, err := a.openACPSession(a.Context(), "")
	if err != nil {
		a.Mu.Lock()
		if a.root == oldRoot && a.generation == admissionGeneration && !a.StoppedLocked() {
			a.sessionSwitching = false
		}
		a.Mu.Unlock()
		return "", err
	}
	installed := false
	defer func() {
		if !installed {
			if closeErr := a.closeACPSession(a.Context(), newID); closeErr != nil {
				slog.Debug("antigravity close an unused replacement session", "error", closeErr)
			}
		}
	}()
	a.inputWG.Wait()
	a.Mu.Lock()
	if a.root != oldRoot || a.generation != admissionGeneration || a.StoppedLocked() {
		a.Mu.Unlock()
		return "", agent.ErrInputSessionChanged
	}
	a.generation++
	retiredGeneration := a.generation
	cancel, group := a.streamCancel, a.streamWG
	states := a.cascadeSnapshotLocked()
	activeIDs := a.nativeCancellationIDsLocked(states)
	a.Mu.Unlock()
	for _, cascadeID := range activeIDs {
		if err := a.rpc.cancel(a.Context(), cascadeID); err != nil {
			slog.Warn("antigravity cancel during clear", "agent_id", a.AgentID(), "cascade_id", cascadeID, "error", err)
		}
	}
	if cancel != nil {
		cancel()
	}
	a.awaitStreams(group, antigravityClearTimerTag)
	a.finishCascades(states, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	if oldRoot != nil {
		if err := a.closeACPSession(a.Context(), oldRoot.id); err != nil {
			slog.Warn("antigravity close the cleared session", "agent_id", a.AgentID(), "error", err)
		}
	}
	streamCtx, streamCancel := context.WithCancel(a.Context())
	a.Mu.Lock()
	if a.root != oldRoot || a.generation != retiredGeneration || a.StoppedLocked() {
		a.Mu.Unlock()
		streamCancel()
		return "", agent.ErrInputSessionChanged
	}
	a.streamCtx, a.streamCancel, a.streamWG = streamCtx, streamCancel, &sync.WaitGroup{}
	root := a.installRoot(newID, false)
	installed = true
	a.sessionSwitching = false
	a.Mu.Unlock()
	a.startCascadeStream(root)
	a.sink.ResetSpans()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.UpdateSessionID(newID)
	a.sink.AdoptNativeSession(newID)
	a.PublishTurnActive()
	return newID, nil
}

func (a *Agent) nativeCancellationIDsLocked(states []*cascadeState) []string {
	refused := map[string]struct{}{}
	for _, state := range states {
		for id, record := range state.childKeys {
			if record.refused {
				refused[id] = struct{}{}
			}
		}
	}
	depths := map[string]int{}
	for _, state := range states {
		depth := 0
		for parent := state.parent; parent != nil; parent = parent.parent {
			depth++
		}
		if _, excluded := refused[state.id]; !excluded && state.id != "" {
			depths[state.id] = depth
		}
		// A cached idle status can precede a native wake, so cleanup includes every owned child.
		for id, record := range state.childKeys {
			if !record.refused && id != "" {
				depths[id] = max(depths[id], depth+1)
			}
		}
	}
	if a.root != nil && a.root.id != "" {
		depths[a.root.id] = 0
	}
	ids := make([]string, 0, len(depths))
	for id := range depths {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(left, right string) int {
		if order := cmp.Compare(depths[right], depths[left]); order != 0 {
			return order
		}
		return cmp.Compare(left, right)
	})
	return ids
}

func (a *Agent) finishCascades(states []*cascadeState, completion agent.MessageCompletion, status bgtask.Status) {
	var root *cascadeState
	for _, state := range states {
		a.retireAllInteractions(state)
		a.closeBackgroundTasks(state, status)
		if state.parent != nil {
			if err := a.endTurn(state, completion); err != nil {
				slog.Error("antigravity finish child output", "agent_id", a.AgentID(), "error", err)
			}
			a.closeChildRow(state, status)
			a.Mu.Lock()
			seq := a.NextTurnSeq()
			a.Mu.Unlock()
			providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
		} else {
			root = state
		}
	}
	if root != nil {
		if err := a.endTurn(root, completion); err != nil {
			slog.Error("antigravity finish root output", "agent_id", a.AgentID(), "error", err)
		}
	}
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	root := a.root
	if root == nil || !a.cascadeCurrentLocked(root) {
		a.Mu.Unlock()
		return nil
	}
	root.interrupted = true
	a.Mu.Unlock()
	// An idle foreground can still own native background tasks. Their cancellation
	// must reach the runtime even when the local turn already ended.
	if err := a.rpc.cancel(a.Context(), root.id); err != nil {
		return fmt.Errorf("cancel the Antigravity turn: %w", err)
	}
	return nil
}

func (a *Agent) awaitStreams(group *sync.WaitGroup, tag string) {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	timer := a.clock.NewTimer(5*time.Second, tag)
	defer timer.Stop(tag)
	select {
	case <-done:
	case <-timer.C:
		slog.Warn("antigravity streams did not stop", "agent_id", a.AgentID())
	}
}
