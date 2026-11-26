package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) restoreRun(ctx context.Context, run *openCodeRun) error {
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	return a.restoreRunLocked(ctx, run)
}
func (a *Agent) restoreStream(ctx context.Context) error {
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return nil
	}
	if err := a.refreshNativeCatalogLocked(ctx); err != nil {
		return err
	}
	if err := a.restoreRunLocked(ctx, root); err != nil {
		return err
	}
	a.Mu.Lock()
	runs := make([]*openCodeRun, 0, len(a.runs))
	for _, run := range a.runs {
		if run != root {
			runs = append(runs, run)
		}
	}
	a.Mu.Unlock()
	sort.Slice(runs, func(i, j int) bool { return runs[i].id < runs[j].id })
	for _, run := range runs {
		if err := a.restoreRunLocked(ctx, run); err != nil {
			return err
		}
	}
	return a.restoreGlobalFormsLocked(ctx)
}

func (a *Agent) restoreRunLocked(ctx context.Context, run *openCodeRun) error {
	if run.restoring {
		return nil
	}
	run.restoring = true
	defer func() { run.restoring = false }()
	if err := run.ctx.Err(); err != nil {
		return err
	}
	info, err := a.rpc.session(ctx, run.id)
	if err != nil {
		if run.parentID != "" && providerkit.IsHTTPStatus(err, 404) {
			return a.retireRunLocked(run, agent.MessageCompletionInterrupted)
		}
		return err
	}
	if err := run.observeSessionInfo(info); err != nil {
		return err
	}
	run.mu.Lock()
	wasActive := run.active || run.compacting
	run.mu.Unlock()
	var pending *openCodeStoredMessage
	var replyProjection openCodeReplyProjection
	pendingCount := 0
	persistHistorical := func(message openCodeStoredMessage, count int) error {
		if run.ended[message.ID] {
			return nil
		}
		completion := storedOpenCodeCompletion(message.Raw)
		content := agent.WithToolUseCount(agent.MessageContent{Original: message.Raw, AgentSessionID: run.id, IdempotencyKey: "completion:" + message.ID, Completion: completion}, count-run.lastToolCount)
		if err := run.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
			return err
		}
		run.ended[message.ID] = true
		run.lastToolCount = count
		return nil
	}
	err = a.rpc.visitMessages(ctx, run.id, func(message openCodeStoredMessage) error {
		if message.Type == contracts.OpenCodeNativeMessageTypeUser {
			if err := run.confirmProjectedInput(message.ID); err != nil {
				return err
			}
		}
		if message.Type != contracts.OpenCodeNativeMessageTypeIdle {
			replyProjection.observe(message, run.replyReceipt(message))
		}
		if pending != nil && message.Type != contracts.OpenCodeNativeMessageTypeIdle {
			if err := persistHistorical(*pending, pendingCount); err != nil {
				return err
			}
			pending = nil
		}
		switch message.Type {
		case "assistant":
			snapshot, err := readOpenCodeAssistantSnapshot(message)
			if err != nil {
				return err
			}
			if err := recoverOpenCodeAssistantOutput(message.ID, snapshot, run.text, run.tools); err != nil {
				return err
			}
			run.publishNativeUsage(snapshot.Tokens, snapshot.Model)
			return nil
		case contracts.OpenCodeNativeMessageTypeIdle:
			replyProjection.ending = message
			if err := run.publishInputReply(replyProjection); err != nil {
				return err
			}
			replyProjection = openCodeReplyProjection{}
			if pending != nil {
				if err := persistHistorical(*pending, pendingCount); err != nil {
					return err
				}
			}
			copy := message
			pending = &copy
			pendingCount = run.tools.count
			return nil
		case contracts.OpenCodeNativeMessageTypeUser:
			if strings.HasPrefix(message.ID, openCodeInputIDPrefix) {
				return nil
			}
			return run.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: message.Raw, AgentSessionID: run.id, IdempotencyKey: "opencode-user:" + message.ID}, agent.SpanInfo{})
		default:
			return run.persistNativeRecord(agent.MessageContent{Original: message.Raw, AgentSessionID: run.id, IdempotencyKey: "record:" + message.ID})
		}
	})
	if err != nil {
		return err
	}
	active, err := a.rpc.activeSessions(ctx)
	if err != nil {
		return err
	}
	if pending != nil {
		if !active[run.id] && (wasActive || run.childAgentID != "") {
			if err := a.finishRunExecutionLocked(run, pending.ID, pending.Raw, storedOpenCodeCompletion(pending.Raw)); err != nil {
				return err
			}
		} else if err := persistHistorical(*pending, pendingCount); err != nil {
			return err
		}
	}
	if !active[run.id] {
		if err := run.text.finish(agent.MessageCompletionInterrupted); err != nil {
			return err
		}
		if err := run.tools.finish(agent.MessageCompletionInterrupted); err != nil {
			return err
		}
		if err := run.retireSettledPendingInputReplies(); err != nil {
			return err
		}
		if pending == nil && run.childAgentID != "" {
			if err := run.sink.CloseBackgroundTask(run.id, bgtask.StatusEndedWithUnknownOutcome); err != nil {
				return err
			}
		}
	} else if run.childAgentID != "" {
		if err := run.sink.ReviveBackgroundTask(run.id); err != nil {
			return err
		}
	}
	run.mu.Lock()
	compacting := run.compacting && active[run.id]
	run.mu.Unlock()
	run.setNativeActivity(active[run.id], compacting)
	return a.restorePendingControlsLocked(ctx, run)
}
func storedOpenCodeCompletion(raw json.RawMessage) agent.MessageCompletion {
	var body struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return agent.MessageCompletionError
	}
	switch body.Outcome {
	case contracts.OpenCodeNativeOutcomeSucceeded:
		return agent.MessageCompletionComplete
	case contracts.OpenCodeNativeOutcomeInterrupted:
		return agent.MessageCompletionInterrupted
	case contracts.OpenCodeNativeOutcomeFailed:
		return agent.MessageCompletionError
	default:
		return agent.MessageCompletionError
	}
}
func (a *Agent) restorePendingControlsLocked(ctx context.Context, run *openCodeRun) error {
	permissions, err := a.rpc.pendingPermissions(ctx, run.id)
	if err != nil {
		return err
	}
	forms, err := a.rpc.pendingForms(ctx, run.id)
	if err != nil {
		return err
	}
	owner := a.controlOwner(run)
	for _, raw := range permissions {
		if err := a.controls.publishPermission(owner, raw); err != nil {
			return err
		}
	}
	for _, raw := range forms {
		if err := a.controls.publishForm(owner, raw); err != nil {
			return err
		}
	}
	return errors.Join(a.controls.reconcilePending("permission", run.id), a.controls.reconcilePending("form", run.id))
}
func (a *Agent) restoreGlobalFormsLocked(ctx context.Context) error {
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if root == nil {
		return nil
	}
	forms, err := a.rpc.pendingForms(ctx, "global")
	if err != nil {
		return err
	}
	owner := a.controlOwner(root)
	owner.nativeSessionID = "global"
	owner.replyInputs = nil
	for _, raw := range forms {
		form, err := readOpenCodeForm(raw)
		if err != nil {
			return err
		}
		var metadata struct {
			Kind string `json:"kind"`
		}
		if providerkit.RawPresent(form.Metadata) {
			if err := json.Unmarshal(form.Metadata, &metadata); err != nil {
				return err
			}
		}
		if metadata.Kind != "mcp-elicitation" {
			continue
		}
		if err := a.controls.publishForm(owner, raw); err != nil {
			return err
		}
	}
	return a.controls.reconcilePending("form", "global")
}
