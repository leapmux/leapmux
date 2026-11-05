package antigravity

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
		state.closed = false
		state.connectErr = nil
		state.connected = make(chan struct{})
		state.connectedOnce = sync.Once{}
		state.stored = true
		a.Mu.Unlock()
		go a.startStoredChild(state)
	} else {
		a.Mu.Unlock()
	}
	state, err := a.childState(childKey)
	if err != nil {
		return err
	}
	return a.sendToState(state, content, attachments, steer, false)
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
	if state.closed {
		a.Mu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	if !steer && (state.turnActive || state.sendInFlight) {
		root := state.parent == nil
		a.Mu.Unlock()
		if root {
			a.PublishTurnActive()
		}
		return agent.ErrAgentBusy
	}
	if steer && !state.turnActive {
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
	isChild := state.parent != nil
	a.inputWG.Add(1)
	a.Mu.Unlock()
	if isChild {
		providerkit.PublishSteerableTurnActiveTo(state.sink, true, seq)
	} else {
		providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: true, Steerable: state.turnActive}, seq)
	}
	request := map[string]any{"cascadeId": state.id, "items": items, "media": media, "cascadeConfig": config}
	err = a.rpc.sendUserMessage(a.Context(), request)
	a.inputWG.Done()
	a.Mu.Lock()
	if err != nil && !state.turnActive {
		state.sendInFlight = false
	}
	childActive := state.turnActive
	errorSeq := a.NextTurnSeq()
	a.Mu.Unlock()
	if err != nil {
		if state.parent == nil {
			a.PublishTurnActive()
		} else {
			providerkit.PublishSteerableTurnActiveTo(state.sink, childActive, errorSeq)
		}
		if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			return providerkit.ResumeFailedError(state.id, err)
		}
		if steer {
			return fmt.Errorf("%w: Antigravity refused the steer: %v", agent.ErrAgentBusy, err)
		}
		return providerkit.ClassifyHTTPDeliveryError("send the Antigravity message", err)
	}
	if isChild {
		providerkit.LogRegistryRefusal("antigravity", "revive", a.sink.ReviveBackgroundTask(state.id))
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
	a.Mu.Unlock()
	newID, err := a.openACPSession(a.Context(), "")
	if err != nil {
		a.Mu.Lock()
		a.sessionSwitching = false
		a.Mu.Unlock()
		return "", err
	}
	a.inputWG.Wait()
	a.Mu.Lock()
	var states []*cascadeState
	for _, state := range a.cascades {
		states = append(states, state)
	}
	a.Mu.Unlock()
	for _, state := range states {
		if state.turnActive {
			if err := a.rpc.cancel(a.Context(), state.id); err != nil {
				slog.Warn("antigravity cancel during clear", "agent_id", a.AgentID(), "cascade_id", state.id, "error", err)
			}
		}
	}
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.awaitStreams(antigravityClearTimerTag)
	a.Mu.Lock()
	a.generation++
	a.Mu.Unlock()
	a.finishCascades(states, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	if oldRoot != nil {
		if err := a.closeACPSession(a.Context(), oldRoot.id); err != nil {
			slog.Warn("antigravity close the cleared session", "agent_id", a.AgentID(), "error", err)
		}
	}
	streamCtx, streamCancel := context.WithCancel(a.Context())
	a.streamCtx, a.streamCancel = streamCtx, streamCancel
	a.Mu.Lock()
	root := a.installRoot(newID, false)
	a.sessionSwitching = false
	a.planPromptRequestID = ""
	a.Mu.Unlock()
	a.startCascadeStream(root)
	a.sink.ResetSpans()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.UpdateSessionID(newID)
	a.sink.AdoptNativeSession(newID)
	a.PublishTurnActive()
	return newID, nil
}

func (a *Agent) finishCascades(states []*cascadeState, completion agent.MessageCompletion, status bgtask.Status) {
	var root *cascadeState
	for _, state := range states {
		a.flushOpenSteps(state, completion)
		a.retireAllInteractions(state)
		a.closeBackgroundTasks(state, status)
		if state.parent != nil {
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
		a.endTurn(root, completion)
	}
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	root := a.root
	if root == nil || !root.turnActive {
		a.Mu.Unlock()
		return nil
	}
	root.interrupted = true
	a.Mu.Unlock()
	if err := a.rpc.cancel(a.Context(), root.id); err != nil {
		return fmt.Errorf("cancel the Antigravity turn: %w", err)
	}
	return nil
}

func (a *Agent) awaitStreams(tag string) {
	done := make(chan struct{})
	go func() {
		a.streamWG.Wait()
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
