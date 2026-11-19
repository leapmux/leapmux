package opencode

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var errOpenCodeUnowned = errors.New("the OpenCode session is outside the owned conversation")

type openCodeLeasedServices struct {
	agent.ProviderServices
	capture func(agent.MessageContent, agent.SpanInfo) agent.MessageContent
}

func (s *openCodeLeasedServices) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	return s.capture(content, span)
}

func (a *Agent) ensureOwnedRun(ctx context.Context, key string) (*openCodeRun, error) {
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	return a.ensureOwnedRunLocked(ctx, key)
}

func (a *Agent) ensureOwnedRunLocked(ctx context.Context, key string) (*openCodeRun, error) {
	if err := openCodeRouteID(key); err != nil {
		return nil, err
	}
	a.Mu.Lock()
	root, known, stopped := a.root, a.runs[key], a.StoppedLocked()
	a.Mu.Unlock()
	if stopped || ctx.Err() != nil {
		return nil, agent.ErrInputSessionChanged
	}
	if root == nil {
		return nil, errOpenCodeUnowned
	}
	if known != nil {
		return known, nil
	}
	seen := make(map[string]bool)
	var chain []openCodeSessionInfo
	cursor := key
	for {
		if seen[cursor] {
			return nil, errors.New("the OpenCode session parent chain contains a cycle")
		}
		seen[cursor] = true
		a.Mu.Lock()
		parent := a.runs[cursor]
		a.Mu.Unlock()
		if parent != nil {
			break
		}
		info, err := a.rpc.session(ctx, cursor)
		if err != nil {
			if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
				return nil, errOpenCodeUnowned
			}
			return nil, err
		}
		if info.ParentID == "" {
			return nil, errOpenCodeUnowned
		}
		chain = append(chain, info)
		cursor = info.ParentID
	}
	for i := len(chain) - 1; i >= 0; i-- {
		info := chain[i]
		a.Mu.Lock()
		parent := a.runs[info.ParentID]
		currentRoot := a.root
		a.Mu.Unlock()
		if parent == nil || currentRoot != root {
			return nil, agent.ErrInputSessionChanged
		}
		span, err := a.findNativeSpawn(ctx, parent.id, info.ID)
		if err != nil {
			return nil, err
		}
		if _, err = a.installChildLocked(info, parent, span); err != nil {
			return nil, err
		}
	}
	a.Mu.Lock()
	run := a.runs[key]
	a.Mu.Unlock()
	if run == nil {
		return nil, errOpenCodeUnowned
	}
	return run, nil
}

func (a *Agent) installChildLocked(info openCodeSessionInfo, parent *openCodeRun, spanID string) (*openCodeRun, error) {
	if info.ParentID != parent.id {
		return nil, errOpenCodeUnowned
	}
	a.Mu.Lock()
	existing, root, stopped := a.runs[info.ID], a.root, a.StoppedLocked()
	a.Mu.Unlock()
	if stopped || parent.ctx.Err() != nil {
		return nil, agent.ErrInputSessionChanged
	}
	if existing != nil {
		if existing.parentID != parent.id {
			return nil, errOpenCodeUnowned
		}
		return existing, nil
	}
	childID, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  spanID, ProviderChildKey: info.ID, AgentSessionID: info.ID, Title: info.Title})
	if err != nil {
		return nil, err
	}
	lease := parent.sink.AcquireChildSink(childID)
	if lease.Sink == nil || lease.CaptureMessage == nil || lease.Activate == nil || lease.Cleanup == nil {
		return nil, errors.New("OpenCode could not acquire complete child transcript services")
	}
	services := agent.NewOrderedProviderServices(agent.OutputQueueFor(lease.Sink), &openCodeLeasedServices{ProviderServices: lease.Sink, capture: lease.CaptureMessage})
	run, err := a.newRun(info, services)
	if err != nil {
		lease.Cleanup()
		return nil, err
	}
	run.childAgentID, run.spawnSpanID, run.childLease = childID, spanID, &lease
	a.Mu.Lock()
	if a.StoppedLocked() || a.root != root || a.runs[parent.id] != parent {
		a.Mu.Unlock()
		run.cancel()
		lease.Cleanup()
		return nil, agent.ErrInputSessionChanged
	}
	a.runs[info.ID] = run
	a.Mu.Unlock()
	run.sink.AdoptNativeSession(info.ID)
	lease.Activate()
	return run, nil
}

func (a *Agent) findNativeSpawn(ctx context.Context, parentID, childID string) (string, error) {
	span := ""
	err := a.rpc.visitMessages(ctx, parentID, func(message openCodeStoredMessage) error {
		if message.Type != "assistant" || span != "" {
			return nil
		}
		var body struct {
			Content []struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				State struct {
					Input struct {
						SessionID string `json:"sessionID"`
					} `json:"input"`
					Metadata struct {
						SessionID string `json:"sessionID"`
					} `json:"metadata"`
				} `json:"state"`
			} `json:"content"`
		}
		if err := json.Unmarshal(message.Raw, &body); err != nil {
			return err
		}
		for _, part := range body.Content {
			if part.Type == contracts.OpenCodeNativePartTypeTool && part.Name == contracts.OpenCodeNativeToolSubagent && part.State.Input.SessionID == "" && part.State.Metadata.SessionID == childID {
				span = openCodeToolSpanID(message.ID, part.ID)
				break
			}
		}
		return nil
	})
	return span, err
}

func (a *Agent) observeNativeChildMetadataLocked(parent *openCodeRun, tool *openCodeToolState, raw json.RawMessage) error {
	if tool.name != contracts.OpenCodeNativeToolSubagent {
		return nil
	}
	var metadata struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	if metadata.SessionID == "" {
		return nil
	}
	info, err := a.rpc.session(parent.ctx, metadata.SessionID)
	if err != nil {
		return err
	}
	if info.ParentID != parent.id {
		return fmt.Errorf("%w: native child parent differs", errOpenCodeUnowned)
	}
	run, err := a.installChildLocked(info, parent, tool.spanID)
	if err != nil {
		return err
	}
	return a.restoreRunLocked(parent.ctx, run)
}
