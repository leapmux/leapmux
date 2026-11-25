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
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var _ agent.ChildSteerer = (*Agent)(nil)

type cascadeInputDelivery uint8

const (
	deliverPrompt cascadeInputDelivery = iota
	deliverSteer
)

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendInput(nil, content, attachments, deliverPrompt)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendInput(&sessionID, content, attachments, deliverPrompt)
}

func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return a.sendInput(nil, request.Content, request.Attachments, deliverSteer)
}

func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, delivery cascadeInputDelivery) (agent.NativeInputReceipt, error) {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, err
	}
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return agent.NativeInputReceipt{}, fmt.Errorf("agent has no Antigravity session")
	}
	return a.sendToState(root, content, attachments, delivery, true)
}

func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	receipt, err := a.sendChild(childKey, input.Content, input.Attachments, deliverPrompt)
	return agent.ChildInputResult{StartsTurn: true, NativeReceipt: receipt}, err
}

func (a *Agent) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return a.sendChild(childKey, request.Content, request.Attachments, deliverSteer)
}

func (*Agent) ChildSteerPolicy() agent.SteerPolicy { return agent.SteerPolicy{} }

func (a *Agent) sendChild(childKey, content string, attachments []*leapmuxv1.Attachment, delivery cascadeInputDelivery) (agent.NativeInputReceipt, error) {
	a.Mu.Lock()
	if a.sessionSwitching {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	state := a.cascades[childKey]
	if state != nil && state.closed && delivery != deliverSteer {
		done, ctx := state.streamDone, a.streamCtx
		a.Mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return agent.NativeInputReceipt{}, agent.ErrChildRouteNotReady
			}
		}
		a.Mu.Lock()
		reopen, reopenErr := a.reopenChildLocked(state, done)
		a.Mu.Unlock()
		if reopenErr != nil {
			return agent.NativeInputReceipt{}, reopenErr
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
		return agent.NativeInputReceipt{}, err
	}
	return a.sendToState(state, content, attachments, delivery, false)
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

func (a *Agent) sendToState(state *cascadeState, content string, attachments []*leapmuxv1.Attachment, delivery cascadeInputDelivery, allowSlash bool) (agent.NativeInputReceipt, error) {
	command := ""
	a.Mu.Lock()
	if allowSlash {
		if name, rest, matched := a.slashCommand(content); matched {
			command, content = name, rest
		}
	}
	a.Mu.Unlock()
	return a.sendToStateCommand(state, content, attachments, delivery, command)
}

func (a *Agent) sendToStateCommand(state *cascadeState, content string, attachments []*leapmuxv1.Attachment, delivery cascadeInputDelivery, command string) (agent.NativeInputReceipt, error) {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, fmt.Errorf("agent is stopped")
	}
	if a.sessionSwitching {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	if state == nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrInputSessionChanged
	}
	if a.cascades[state.id] != state || state.generation != a.generation || state.parent == nil && a.root != state {
		a.Mu.Unlock()
		if state.parent != nil {
			return agent.NativeInputReceipt{}, agent.ErrChildRouteNotReady
		}
		return agent.NativeInputReceipt{}, agent.ErrInputSessionChanged
	}
	if state.closed || a.streamCtx.Err() != nil {
		child := state.parent != nil
		a.Mu.Unlock()
		if child {
			return agent.NativeInputReceipt{}, agent.ErrChildRouteNotReady
		}
		return agent.NativeInputReceipt{}, fmt.Errorf("the Antigravity event stream is closed")
	}
	if state.turnPhase == cascadeTurnFinalizing || delivery != deliverSteer && (state.turnPhase != cascadeTurnIdle || state.sendInFlight) {
		root := state.parent == nil
		a.Mu.Unlock()
		if root {
			a.PublishTurnActive()
		}
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	if delivery == deliverSteer && state.turnPhase != cascadeTurnActive {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	items, media, err := a.buildMessageItems(content, attachments, command)
	if err != nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, err
	}
	config, err := a.cascadeConfigLocked()
	if err != nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, err
	}
	request := map[string]any{"cascadeId": state.id, "items": items, "media": media, "cascadeConfig": config, "tags": []string{antigravityQueueInputTag}}
	receipt := agent.NativeInputReceipt{}
	if command == "" {
		receipt = agent.NativeInputReceipt{SessionID: state.id, InputID: id.Generate()}
		if state.inputReplies == nil {
			state.inputReplies = make(map[string]*inputReply)
		}
		state.inputReplies[receipt.InputID] = &inputReply{receipt: receipt, steered: delivery == deliverSteer}
		request["clientMessageId"] = receipt.InputID
	}
	state.sendInFlight = true
	seq := a.NextTurnSeq()
	state.inputRequestSeq = seq
	outgoing := a.turnStateLocked(state)
	a.inputWG.Add(1)
	a.Mu.Unlock()
	defer a.inputWG.Done()
	providerkit.PublishTurnStateTo(state.sink, outgoing, seq)
	err = a.rpc.sendUserMessage(a.Context(), request)
	if err == nil {
		return receipt, nil
	}
	a.Mu.Lock()
	delete(state.inputReplies, receipt.InputID)
	current := a.cascadeCurrentLocked(state) && state.inputRequestSeq == seq
	var errorState agent.TurnState
	var errorSeq uint64
	if current {
		if state.turnPhase == cascadeTurnIdle {
			state.sendInFlight = false
		}
		errorState = a.turnStateLocked(state)
		errorSeq = a.NextTurnSeq()
	}
	a.Mu.Unlock()
	if current {
		providerkit.PublishTurnStateTo(state.sink, errorState, errorSeq)
	}
	if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
		return agent.NativeInputReceipt{}, providerkit.ResumeFailedError(state.id, err)
	}
	return agent.NativeInputReceipt{}, err
}

func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	root := a.root
	if root == nil || a.sessionSwitching {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.Mu.Unlock()
	_, err := a.sendToStateCommand(root, "", nil, deliverPrompt, antigravityCompactCommand)
	return err
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
	oldStreamCtx := a.streamCtx
	admissionGeneration := a.generation
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		if a.root == oldRoot && a.generation == admissionGeneration && !a.StoppedLocked() {
			a.sessionSwitching = false
		}
		a.Mu.Unlock()
	}()
	newID, err := a.rpc.startCascade(a.Context(), a.workingDir)
	if err != nil {
		return "", err
	}
	streamCtx, streamCancel := context.WithCancel(a.Context())
	var prepared *preparedStateStream
	installed := false
	defer func() {
		if !installed {
			streamCancel()
			if prepared != nil {
				_ = prepared.body.Close()
			}
			if stopErr := a.rpc.forceStopCascadeTree(a.Context(), newID); stopErr != nil {
				slog.Debug("antigravity stop the unused replacement cascade", "error", stopErr)
			}
		}
	}()
	prepared, err = a.awaitReplacementStream(streamCtx, oldStreamCtx, newID)
	if err != nil {
		return "", fmt.Errorf("open the replacement Antigravity stream: %w", err)
	}
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
	if cancel != nil {
		cancel()
	}
	for _, cascadeID := range activeIDs {
		if err := a.rpc.cancel(a.Context(), cascadeID); err != nil {
			slog.Warn("antigravity cancel during clear", "agent_id", a.AgentID(), "cascade_id", cascadeID, "error", err)
		}
	}
	a.awaitStreams(group, antigravityClearTimerTag)
	a.finishCascades(states, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	if oldRoot != nil {
		if err := a.rpc.forceStopCascadeTree(a.Context(), oldRoot.id); err != nil {
			slog.Warn("antigravity stop the cleared cascade tree", "agent_id", a.AgentID(), "error", err)
		}
	}
	a.Mu.Lock()
	if a.root != oldRoot || a.generation != retiredGeneration || a.StoppedLocked() {
		a.Mu.Unlock()
		return "", agent.ErrInputSessionChanged
	}
	a.streamCtx, a.streamCancel, a.streamWG = streamCtx, streamCancel, &sync.WaitGroup{}
	root := a.installRoot(newID, false)
	installed = true
	a.Mu.Unlock()
	a.sink.ResetSpans()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.UpdateSessionID(newID)
	a.sink.AdoptNativeSession(newID)
	providerkit.EndCompaction(a.sink)
	if !a.startCascadeWorker(root, func(ctx context.Context, state *cascadeState) {
		a.runCascadeStreamFrom(ctx, state, prepared)
	}) {
		_ = prepared.body.Close()
		return "", agent.ErrInputSessionChanged
	}
	a.Mu.Lock()
	current := a.cascadeCurrentLocked(root)
	if current {
		a.sessionSwitching = false
	}
	a.Mu.Unlock()
	if !current {
		return "", agent.ErrInputSessionChanged
	}
	a.PublishTurnActive()
	return newID, nil
}

func (a *Agent) awaitReplacementStream(ctx, oldCtx context.Context, sessionID string) (*preparedStateStream, error) {
	type result struct {
		stream *preparedStateStream
		err    error
	}
	ready := make(chan result)
	go func() {
		stream, err := a.rpc.prepareStateStream(ctx, sessionID)
		select {
		case ready <- result{stream: stream, err: err}:
		case <-ctx.Done():
			if stream != nil {
				_ = stream.body.Close()
			}
		}
	}()
	timer := a.clock.NewTimer(a.rpc.timeout, antigravityClearStreamTimerTag)
	defer timer.Stop(antigravityClearStreamTimerTag)
	select {
	case prepared := <-ready:
		return prepared.stream, prepared.err
	case <-oldCtx.Done():
		return nil, agent.ErrInputSessionChanged
	case <-a.ProcessDone():
		return nil, agent.ErrInputSessionChanged
	case <-timer.C:
		return nil, context.DeadlineExceeded
	}
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

func (a *Agent) finishCascadeOutput(state *cascadeState, completion agent.MessageCompletion) error {
	a.Mu.Lock()
	active := a.cascadeInstalledLocked(state) && state.turnPhase == cascadeTurnActive
	a.Mu.Unlock()
	if active {
		return a.endTurnWithTeardown(state, completion, true)
	}
	if !a.IsDiscardingOutput() {
		if err := a.flushOpenSteps(state, completion, true); err != nil {
			return err
		}
	}
	providerkit.ResetSpansAndProgress(state.sink)
	return nil
}

func (a *Agent) finishCascades(states []*cascadeState, completion agent.MessageCompletion, status bgtask.Status) {
	var root *cascadeState
	for _, state := range states {
		a.retireAllInteractions(state)
		a.closeBackgroundTasks(state, status)
		if state.parent != nil {
			if err := a.finishCascadeOutput(state, completion); err != nil {
				slog.Error("antigravity finish child output", "agent_id", a.AgentID(), "error", err)
			}
			a.finishInputReplies(state, false, completion)
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
		if err := a.finishCascadeOutput(root, completion); err != nil {
			slog.Error("antigravity finish root output", "agent_id", a.AgentID(), "error", err)
		}
		a.finishInputReplies(root, false, completion)
	}
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	return a.interruptCascade(root, stop, "Antigravity turn")
}

func (a *Agent) interruptCascade(state *cascadeState, stop agent.StopContext, cancelLabel string) error {
	a.Mu.Lock()
	if state == nil || !a.cascadeCurrentLocked(state) || (state.parent != nil && state.closed) {
		a.Mu.Unlock()
		return nil
	}
	active := state.turnPhase != cascadeTurnIdle || state.sendInFlight
	var interruptGen uint64
	if active {
		state.interruptGen++
		interruptGen = state.interruptGen
		state.interrupted = true
	}
	a.Mu.Unlock()
	// An idle cascade can still own native background tasks. Their cancellation
	// must reach the runtime even when the local turn already ended.
	if err := a.rpc.cancel(a.Context(), state.id); err != nil {
		if active {
			a.Mu.Lock()
			if state.interruptGen == interruptGen {
				state.interrupted = false
			}
			a.Mu.Unlock()
		}
		return fmt.Errorf("cancel the %s: %w", cancelLabel, err)
	}
	if !active {
		state.updateMu.Lock()
		defer state.updateMu.Unlock()
		a.Mu.Lock()
		stillIdle := a.cascadeCurrentLocked(state) && !state.closed && state.turnPhase == cascadeTurnIdle && !state.sendInFlight
		a.Mu.Unlock()
		if !stillIdle {
			return nil
		}
		if !a.IsDiscardingOutput() {
			if err := a.flushOpenSteps(state, agent.MessageCompletionInterrupted, true); err != nil {
				slog.Error("antigravity flush idle interrupted steps", "agent_id", a.AgentID(), "error", err)
			}
		}
		a.Mu.Lock()
		stillIdle = a.cascadeCurrentLocked(state) && !state.closed && state.turnPhase == cascadeTurnIdle && !state.sendInFlight
		hasBackground := len(state.background) > 0
		a.Mu.Unlock()
		if !stillIdle {
			return nil
		}
		if hasBackground {
			state.sink.ReportProgress(agent.ResetModelProgress())
			stop.PreserveOpenSpans()
		} else {
			providerkit.ResetSpansAndProgress(state.sink)
		}
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
