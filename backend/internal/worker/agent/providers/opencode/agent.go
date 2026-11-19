package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one OpenCode server, one root conversation and its native children.
type Agent struct {
	providerkit.Process
	sink            agent.ProviderServices
	rpc             openCodeRPC
	stream          *openCodeStream
	controls        *openCodeControls
	catalog         openCodeCatalog
	root            *openCodeRun
	runs            map[string]*openCodeRun
	dispatch        sync.Mutex
	settings        sync.Mutex
	finishOnce      sync.Once
	nativeCancel    context.CancelFunc
	streamWatchDone chan struct{}
	streamFailure   error
	finishError     error
	shellMu         sync.Mutex
	shellReaders    map[openCodeShellKey]*openCodeShellReader
	shellsClosed    bool
	shellWorkers    sync.WaitGroup
}

type openCodeRun struct {
	*openCodeSessionRuntime
	cancel          context.CancelFunc
	text            *openCodeTextStreams
	tools           *openCodeToolStreams
	lastSeq         int64
	stepModels      map[string]openCodeModelRef
	childAgentID    string
	spawnSpanID     string
	childLease      *agent.ChildSinkLease
	ended           map[string]bool
	lastToolCount   int
	restoring       bool
	retryResolution providerkit.RetryResolution
}

var (
	_ agent.Agent               = (*Agent)(nil)
	_ agent.InputSteerer        = (*Agent)(nil)
	_ agent.SessionInputSteerer = (*Agent)(nil)
	_ agent.ChildSteerer        = (*Agent)(nil)
	_ agent.ChildInterrupter    = (*Agent)(nil)
	_ agent.ContextCompactor    = (*Agent)(nil)
	_ agent.SteerEchoReporter   = (*Agent)(nil)
	_ agent.SteerSurvivor       = (*Agent)(nil)
)

func (a *Agent) rootRun() (*openCodeRun, error) {
	a.Mu.Lock()
	root := a.root
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return nil, errors.New("OpenCode is stopped")
	}
	if root == nil {
		return nil, agent.ErrInputSessionChanged
	}
	return root, nil
}
func (a *Agent) SupportsSteering() bool   { _, err := a.rootRun(); return err == nil }
func (*Agent) SteerRowAtEcho() bool       { return true }
func (*Agent) SteerSurvivesTurnEnd() bool { return true }
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	return run.SendInput(content, attachments)
}
func (a *Agent) SendInputForSession(id, content string, attachments []*leapmuxv1.Attachment) error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	return run.SendInputForSession(id, content, attachments)
}
func (a *Agent) SteerInput(request agent.SteerRequest) error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	return run.SteerInput(request.Content, request.Attachments)
}
func (a *Agent) SteerInputForSession(id string, request agent.SteerRequest) error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	return run.sendInput(&id, request.Content, request.Attachments, true)
}
func (a *Agent) PublishTurnActive() agent.TurnState {
	run, err := a.rootRun()
	if err != nil {
		return agent.TurnState{}
	}
	return run.PublishTurnActive()
}
func (a *Agent) SendRawInput(input agent.RawInput) error {
	if a.controls == nil {
		return agent.ErrControlRequestNotPending
	}
	return a.controls.answer(input.Data)
}

func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	run, err := a.rootRun()
	if err != nil {
		return nil
	}
	groups := run.OptionGroups()
	automatic := false
	if a.controls != nil {
		automatic = a.controls.autoApproveEnabled()
	}
	return append(groups, providerkit.LiveGroup(AutoApproveGroup, strconv.FormatBool(automatic)))
}
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.settings.Lock()
	defer a.settings.Unlock()
	run, err := a.rootRun()
	if err != nil {
		return agent.RestartRequiredSettings(options)
	}
	current := a.SettingsSnapshot()
	automatic := a.controls.autoApproveEnabled()
	if requested := options[AutoApproveGroup.Id]; requested != "" {
		automatic, err = optionmap.Map{AutoApproveGroup.Id: requested}.Bool(AutoApproveGroup.Id)
		if err != nil {
			return a.SettingsSnapshot()
		}
	}
	changes := make(optionmap.Map)
	for _, key := range []string{agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPrimaryAgent} {
		if requested := options[key]; requested != "" && requested != current.SurfacedOptions[key] {
			changes[key] = requested
		}
	}
	if changes[agent.OptionIDModel] != "" && changes[agent.OptionIDEffort] == "" {
		if effort := options[agent.OptionIDEffort]; effort != "" {
			changes[agent.OptionIDEffort] = effort
		} else if effort := current.SurfacedOptions[agent.OptionIDEffort]; effort != "" {
			changes[agent.OptionIDEffort] = effort
		}
	}
	if len(changes) == 0 && automatic == a.controls.autoApproveEnabled() {
		return current
	}
	if len(changes) > 0 && !run.UpdateSettings(changes).AppliedLive {
		return agent.RestartRequiredSettings(options)
	}
	a.controls.setAutoApprove(automatic)
	result := a.SettingsSnapshot()
	a.sink.PersistSettingsRefresh(result.SurfacedOptions)
	return result
}

// A child report can wake its previous parent after session replacement.
// Clear Context restarts this stdio server without a daemon handoff.
// Shutdown stops live execution but preserves native history and recovery claims.
func (*Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }
func (a *Agent) CompactContext() error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	return run.compactNativeContext()
}
func (a *Agent) Interrupt(_ agent.StopContext) error {
	run, err := a.rootRun()
	if err != nil {
		return err
	}
	interrupted, err := a.rpc.interrupt(a.Context(), run.id)
	if err != nil {
		return err
	}
	if !interrupted {
		return a.restoreRun(a.Context(), run)
	}
	return nil
}
func (a *Agent) SendChildInput(key string, input agent.ChildInput) (agent.ChildInputResult, error) {
	run, err := a.ensureOwnedRun(a.Context(), key)
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	if run.parentID == "" {
		return agent.ChildInputResult{StartsTurn: true}, errors.New("the native child key identifies the root conversation")
	}
	return agent.ChildInputResult{StartsTurn: true}, run.SendInput(input.Content, input.Attachments)
}
func (a *Agent) SteerChildInput(key, content string, attachments []*leapmuxv1.Attachment) error {
	run, err := a.ensureOwnedRun(a.Context(), key)
	if err != nil {
		return err
	}
	if run.parentID == "" {
		return errors.New("the native child key identifies the root conversation")
	}
	return run.SteerInput(content, attachments)
}
func (a *Agent) ActiveChildTurnState(key string) agent.TurnState {
	a.Mu.Lock()
	run := a.runs[key]
	a.Mu.Unlock()
	if run == nil || run.parentID == "" {
		return agent.TurnState{}
	}
	return run.PublishTurnActive()
}
func (a *Agent) InterruptChild(key string, _ agent.StopContext) error {
	run, err := a.ensureOwnedRun(a.Context(), key)
	if err != nil {
		return err
	}
	if run.parentID == "" {
		return errors.New("the native child key identifies the root conversation")
	}
	interrupted, err := a.rpc.interrupt(a.Context(), run.id)
	if err != nil {
		return err
	}
	if !interrupted {
		return a.restoreRun(a.Context(), run)
	}
	return nil
}
func (a *Agent) HandleOutput(raw []byte) {
	if err := a.acceptNativeEvent(a.Context(), raw); err != nil {
		slog.Error("OpenCode could not process an event", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) newRun(info openCodeSessionInfo, sink agent.ProviderServices) (*openCodeRun, error) {
	ctx, cancel := context.WithCancel(a.Context())
	nativeID := info.ID
	a.Mu.Lock()
	catalog := a.catalog
	a.Mu.Unlock()
	run := &openCodeRun{cancel: cancel, lastSeq: -1, ended: make(map[string]bool)}
	runtime, err := newOpenCodeSessionRuntime(ctx, a.rpc, info, catalog, openCodeSessionServices{
		sink: sink,
		claimTurnSequence: func() uint64 {
			a.Mu.Lock()
			defer a.Mu.Unlock()
			if a.StoppedLocked() || ctx.Err() != nil || a.runs[nativeID] != run {
				return 0
			}
			return a.NextTurnSeq()
		},
		requestRecovery: a.requestRecovery,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	run.openCodeSessionRuntime = runtime
	run.text = newOpenCodeTextStreams(info.ID, runtime.sink)
	run.tools = newOpenCodeToolStreams(info.ID, runtime.sink)
	run.tools.metadata = func(tool *openCodeToolState, raw json.RawMessage) error {
		if err := a.observeNativeChildMetadataLocked(run, tool, raw); err != nil {
			return err
		}
		return a.observeNativeShellMetadataLocked(run, tool, raw)
	}
	run.tools.closed = func(tool *openCodeToolState) { a.stopNativeShellReader(run, tool) }
	return run, nil
}

func (a *Agent) watchStream() {
	defer close(a.streamWatchDone)
	select {
	case <-a.ProcessDone():
	case <-a.stream.done:
		if !a.IntentionalStopRequested() && a.Context().Err() == nil {
			failure := a.stream.connectionError("the OpenCode event stream ended")
			a.Mu.Lock()
			a.streamFailure = failure
			a.Mu.Unlock()
			a.nativeCancel()
		}
	}
}
func (a *Agent) finishNative() error {
	a.finishOnce.Do(func() {
		if a.stream != nil {
			a.stream.stop()
			a.stream.wait()
		}
		if a.streamWatchDone != nil {
			<-a.streamWatchDone
		}
		a.closeNativeShellReaders()
		if a.controls != nil {
			a.controls.close()
		}
		a.dispatch.Lock()
		a.finishError = a.finishNativeOutputs(a.ProcessExitCompletion())
		a.dispatch.Unlock()
		if a.rpc.endpoint != nil {
			a.rpc.endpoint.Close()
		}
	})
	return a.finishError
}
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Process.Stop()
	if err := a.finishNative(); err != nil {
		slog.Error("OpenCode could not finish its transcript", "agent_id", a.AgentID(), "error", err)
	}
}
func (a *Agent) Wait() error {
	processErr := a.Process.Wait()
	finishErr := a.finishNative()
	a.Mu.Lock()
	streamErr := a.streamFailure
	a.Mu.Unlock()
	processErr = errors.Join(streamErr, processErr)
	return errors.Join(processErr, finishErr)
}

func (a *Agent) requestRecovery() {
	a.Mu.Lock()
	stream, stopped := a.stream, a.StoppedLocked()
	a.Mu.Unlock()
	if stream != nil && !stopped {
		stream.requestRecovery()
	}
}
