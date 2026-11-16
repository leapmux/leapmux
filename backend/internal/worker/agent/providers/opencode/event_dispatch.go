package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) acceptNativeEvent(ctx context.Context, raw json.RawMessage) error {
	event, err := parseOpenCodeEvent(raw)
	if err != nil {
		return err
	}
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	if event.sessionID == "global" {
		return a.dispatchGlobalControlLocked(event)
	}
	if event.sessionID == "" {
		switch event.Type {
		case "agent.updated", "model.updated", "provider.updated", "config.updated":
			return a.refreshNativeCatalogLocked(ctx)
		}
		return nil
	}
	a.Mu.Lock()
	run := a.runs[event.sessionID]
	a.Mu.Unlock()
	if run == nil {
		if event.Type == "session.deleted" {
			return nil
		}
		run, err = a.ensureOwnedRunLocked(ctx, event.sessionID)
		if errors.Is(err, errOpenCodeUnowned) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if event.Durable != nil && event.Durable.Seq <= run.lastSeq {
		return nil
	}
	if err := a.dispatchRunEventLocked(ctx, run, event); err != nil {
		return err
	}
	if event.Durable != nil {
		run.lastSeq = event.Durable.Seq
	}
	return nil
}

func (a *Agent) controlOwner(run *openCodeRun) openCodeControlOwner {
	return openCodeControlOwner{nativeSessionID: run.id, sessionID: run.id, sink: run.sink, current: func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.StoppedLocked() && run.ctx.Err() == nil && a.runs[run.id] == run
	}}
}
func (a *Agent) dispatchGlobalControlLocked(event openCodeEvent) error {
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return nil
	}
	switch event.Type {
	case contracts.OpenCodeNativeEventFormCreated:
		var data struct {
			Form json.RawMessage `json:"form"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		owner := a.controlOwner(root)
		owner.nativeSessionID = "global"
		return a.controls.publishForm(owner, data.Form)
	case "form.replied", "form.cancelled":
		var data struct {
			ID string `json:"id"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		if data.ID == "" {
			return errors.New("the native OpenCode form settlement has no ID")
		}
		a.controls.retire("form", "global", data.ID)
	}
	return nil
}
func (a *Agent) dispatchRunEventLocked(ctx context.Context, run *openCodeRun, event openCodeEvent) error {
	if event.Type == contracts.OpenCodeNativeEventStepEnded || event.Type == contracts.OpenCodeNativeEventStepFirstToken {
		var data struct {
			MessageID string `json:"assistantMessageID"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		if data.MessageID == "" {
			return errors.New("the OpenCode step output has no assistant message ID")
		}
	}
	if event.Type == contracts.OpenCodeNativeEventStepFirstToken || event.Type == contracts.OpenCodeNativeEventStepEnded || event.Type == contracts.OpenCodeNativeEventExecutionSucceeded {
		run.retryResolution.Resolve(run.sink)
	}
	if accepted, err := run.text.observe(event); accepted {
		return err
	}
	if accepted, err := run.tools.observe(event); accepted {
		return err
	}
	if accepted, err := run.observeInbox(event); accepted {
		return err
	}
	switch event.Type {
	case contracts.OpenCodeNativeEventRetryScheduled:
		if err := run.persistNativeRecord(agent.MessageContent{Original: event.Raw, AgentSessionID: run.id, IdempotencyKey: "event:" + event.ID}); err != nil {
			return err
		}
		run.retryResolution.NoteRetry()
		return nil
	case contracts.OpenCodeNativeEventStepStarted:
		var data struct {
			MessageID string            `json:"assistantMessageID"`
			Model     *openCodeModelRef `json:"model"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		if data.MessageID != "" && data.Model != nil {
			if run.stepModels == nil {
				run.stepModels = make(map[string]openCodeModelRef)
			}
			run.stepModels[data.MessageID] = *data.Model
		}
	case contracts.OpenCodeNativeEventStepEnded, contracts.OpenCodeNativeEventStepFailed:
		run.publishNativeStepUsage(event.Data)
	case contracts.OpenCodeNativeEventUsageUpdated:
		run.publishNativeTotalCost(event.Data)
	case "session.execution.started":
		run.mu.Lock()
		compacting := run.compacting
		run.mu.Unlock()
		run.setNativeActivity(true, compacting)
		if run.childAgentID != "" {
			return run.sink.UpdateBackgroundTaskStatus(run.id, bgtask.StatusRunning, "")
		}
		return nil
	case contracts.OpenCodeNativeEventExecutionSucceeded:
		return a.finishRunExecutionLocked(run, nativeMessageIDForEvent(event.ID), event.Raw, agent.MessageCompletionComplete)
	case contracts.OpenCodeNativeEventExecutionFailed:
		return a.finishRunExecutionLocked(run, nativeMessageIDForEvent(event.ID), event.Raw, agent.MessageCompletionError)
	case contracts.OpenCodeNativeEventExecutionInterrupted:
		var data struct {
			Reason string `json:"reason"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		// Native execution interruptions default to shutdown, including permission rejection.
		// Ignore that reason only while this adapter is ending its process.
		if data.Reason == "shutdown" && (a.IntentionalStopRequested() || a.Context().Err() != nil) {
			return nil
		}
		return a.finishRunExecutionLocked(run, nativeMessageIDForEvent(event.ID), event.Raw, agent.MessageCompletionInterrupted)
	case "session.agent.selected", "session.model.selected", "session.moved", "session.updated":
		info, err := a.rpc.session(ctx, run.id)
		if err != nil {
			return err
		}
		if err := run.observeSessionInfo(info); err != nil {
			return err
		}
		if run.parentID == "" {
			a.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
		}
		return nil
	case contracts.OpenCodeNativeEventPermissionAsked:
		return a.controls.publishPermission(a.controlOwner(run), event.Data)
	case "permission.replied":
		var data struct {
			ID string `json:"requestID"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		if data.ID == "" {
			return errors.New("the native OpenCode permission settlement has no ID")
		}
		a.controls.retire("permission", run.id, data.ID)
		return nil
	case contracts.OpenCodeNativeEventFormCreated:
		var data struct {
			Form json.RawMessage `json:"form"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		return a.controls.publishForm(a.controlOwner(run), data.Form)
	case "form.replied", "form.cancelled":
		var data struct {
			ID string `json:"id"`
		}
		if err := event.decodeData(&data); err != nil {
			return err
		}
		if data.ID == "" {
			return errors.New("the native OpenCode form settlement has no ID")
		}
		a.controls.retire("form", run.id, data.ID)
		return nil
	case contracts.OpenCodeNativeEventCompactionStarted:
		run.setNativeActivity(true, true)
	case contracts.OpenCodeNativeEventCompactionEnded, contracts.OpenCodeNativeEventCompactionFailed:
		run.mu.Lock()
		active := run.active
		run.mu.Unlock()
		run.setNativeActivity(active, false)
	case "session.deleted":
		return a.retireRunLocked(run, agent.MessageCompletionInterrupted)
	}
	return run.persistNativeRecord(agent.MessageContent{Original: event.Raw, AgentSessionID: run.id, IdempotencyKey: "event:" + event.ID})
}
func nativeMessageIDForEvent(eventID string) string {
	if strings.HasPrefix(eventID, "evt_") {
		return "msg_" + strings.TrimPrefix(eventID, "evt_")
	}
	return eventID
}
func (a *Agent) finishRunExecutionLocked(run *openCodeRun, id string, raw []byte, completion agent.MessageCompletion) error {
	if run.ended[id] {
		if err := run.sink.OutputQueue().Flush(); err != nil {
			return err
		}
		return run.closeNativeBackgroundTask(completion)
	}
	if err := run.text.finish(completion); err != nil {
		return err
	}
	if err := run.tools.finish(completion); err != nil {
		return err
	}
	count := run.tools.count - run.lastToolCount
	content := agent.WithToolUseCount(agent.MessageContent{Original: raw, AgentSessionID: run.id, IdempotencyKey: "completion:" + id, Completion: completion}, count)
	if err := run.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		return err
	}
	run.ended[id] = true
	run.retryResolution.Reset()
	run.stepModels = nil
	run.lastToolCount = run.tools.count
	run.setNativeActivity(false, false)
	return run.closeNativeBackgroundTask(completion)
}

func (run *openCodeRun) closeNativeBackgroundTask(completion agent.MessageCompletion) error {
	if run.childAgentID == "" {
		return nil
	}
	status := bgtask.StatusSucceeded
	switch completion {
	case agent.MessageCompletionComplete, agent.MessageCompletionFinished:
	case agent.MessageCompletionError:
		status = bgtask.StatusFailed
	case agent.MessageCompletionInterrupted:
		status = bgtask.StatusInterrupted
	}
	return run.sink.CloseBackgroundTask(run.id, status)
}
func (a *Agent) retireRunLocked(run *openCodeRun, completion agent.MessageCompletion) error {
	a.Mu.Lock()
	var children []*openCodeRun
	for _, child := range a.runs {
		if child.parentID == run.id {
			children = append(children, child)
		}
	}
	delete(a.runs, run.id)
	a.Mu.Unlock()
	var result error
	for _, child := range children {
		result = errors.Join(result, a.retireRunLocked(child, completion))
	}
	run.cancel()
	a.controls.retireOwner(run.id)
	result = errors.Join(result, run.text.finish(completion), run.tools.finish(completion))
	run.finishPendingSteers()
	if run.childLease != nil && run.childLease.Cleanup != nil {
		run.childLease.Cleanup()
	}
	return result
}
func (a *Agent) refreshNativeCatalogLocked(ctx context.Context) error {
	catalog, err := loadOpenCodeCatalog(ctx, a.rpc)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	a.catalog = catalog
	runs := make([]*openCodeRun, 0, len(a.runs))
	for _, run := range a.runs {
		runs = append(runs, run)
	}
	a.Mu.Unlock()
	for _, run := range runs {
		run.mu.Lock()
		run.catalog = catalog
		run.settingsVersion++
		run.mu.Unlock()
	}
	a.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	return nil
}
func (a *Agent) finishNativeOutputs(completion agent.MessageCompletion) error {
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return nil
	}
	if err := a.retireRunLocked(root, completion); err != nil {
		return fmt.Errorf("finish native OpenCode output: %w", err)
	}
	return nil
}
