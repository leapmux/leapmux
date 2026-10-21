package letta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type lettaPendingPermission struct {
	scope             runtimeScope
	payload           []byte
	bypassable        bool
	ready             bool
	published         bool
	publicationFailed bool
	answering         bool
	automaticFailed   bool
	publicationDone   chan struct{}
	publicationOnce   sync.Once
}

var errPermissionNotPending = errors.New("the Letta permission request no longer waits for an answer")

type lettaPermissionDelivery struct {
	scope  runtimeScope
	waiter *runtimeWaiter
}

func (a *Agent) registerPermission(requestID string, payload []byte, bypassable bool) {
	a.Mu.Lock()
	if a.StoppedLocked() || a.permissions[requestID] != nil {
		a.Mu.Unlock()
		return
	}
	if a.permissions == nil {
		a.permissions = make(map[string]*lettaPendingPermission)
	}
	scope := runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
	bypass := bypassable && a.settings.permissionMode == contracts.LettaModeUnrestricted && scope.AgentID != "" && scope.ConversationID != ""
	pending := &lettaPendingPermission{scope: scope, payload: payload, bypassable: bypassable, ready: bypass, publicationDone: make(chan struct{})}
	a.permissions[requestID] = pending
	a.Mu.Unlock()
	if bypass {
		a.approvalWork.Run("permissions", a.approvePendingPermissions)
		return
	}
	a.publishPermission(requestID, pending)
}

func (a *Agent) publishPermission(requestID string, pending *lettaPendingPermission) {
	a.Mu.Lock()
	if a.permissions[requestID] != pending || a.StoppedLocked() {
		if a.permissions[requestID] == pending {
			delete(a.permissions, requestID)
		}
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	err := a.sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: pending.scope.ConversationID, RequestID: requestID, Payload: pending.payload})
	a.Mu.Lock()
	pending.ready, pending.published, pending.publicationFailed = true, err == nil, err != nil
	current := a.permissions[requestID] == pending && !a.StoppedLocked()
	if !current && a.permissions[requestID] == pending {
		delete(a.permissions, requestID)
	}
	bypass := a.settings.permissionMode == contracts.LettaModeUnrestricted && pending.bypassable && !pending.automaticFailed
	pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	a.Mu.Unlock()
	if err != nil {
		slog.Warn("letta could not publish a permission", "request_id", requestID, "error", err)
	}
	if !current {
		if err == nil {
			a.sink.CancelControlRequest(requestID)
		}
	} else if err != nil || bypass {
		a.approvalWork.Run("permissions", a.approvePendingPermissions)
	}
}

func (p *lettaPendingPermission) matches(scope runtimeScope) bool {
	return scope.AgentID != "" && scope.ConversationID != "" && p.scope.ConversationID == scope.ConversationID &&
		(p.scope.AgentID == "" || p.scope.AgentID == scope.AgentID)
}

func (a *Agent) approvePendingPermissions() {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	scope := runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
	claimed := make(map[string]*lettaPendingPermission)
	toPublish := make(map[string]*lettaPendingPermission)
	for id, pending := range a.permissions {
		if !pending.ready || pending.answering || !pending.matches(scope) {
			continue
		}
		if pending.publicationFailed || a.settings.permissionMode == contracts.LettaModeUnrestricted && pending.bypassable && !pending.automaticFailed {
			pending.answering = true
			claimed[id] = pending
		} else if !pending.published {
			pending.ready = false
			toPublish[id] = pending
		}
	}
	a.Mu.Unlock()
	for id, pending := range toPublish {
		a.publishPermission(id, pending)
	}
	for id, pending := range claimed {
		payload := approvalResponsePayload{Kind: contracts.LettaReplyKindApprovalResponse, RequestID: id}
		if pending.publicationFailed {
			payload.Error = providerkit.ControlPublicationFailure
		} else {
			payload.Decision = json.RawMessage(`{"behavior":"allow","message":""}`)
		}
		raw, err := json.Marshal(payload)
		if err == nil {
			err = a.sendPermissionPayload(scope, raw)
		}
		a.finishPermissionAnswer(id, pending, err, true)
		if err != nil {
			slog.Warn("letta could not answer a pending permission", "request_id", id, "error", err)
		}
	}
}

// validatePermissionAnswer checks native decision fields without converting unknown numbers.
func validatePermissionAnswer(data []byte) (string, error) {
	var reply struct {
		RequestID string          `json:"request_id"`
		Decision  json.RawMessage `json:"decision"`
		Error     json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.RequestID == "" {
		return "", errors.New("the Letta permission answer has no valid request ID")
	}
	if len(reply.Error) > 0 {
		if _, ok := providerkit.RawString(reply.Error); !ok {
			return "", errors.New("the Letta permission error is not text")
		}
		return reply.RequestID, nil
	}
	var decision struct {
		Behavior     string          `json:"behavior"`
		Message      json.RawMessage `json:"message"`
		UpdatedInput json.RawMessage `json:"updated_input"`
		Suggestions  json.RawMessage `json:"selected_permission_suggestion_ids"`
	}
	if json.Unmarshal(reply.Decision, &decision) != nil || decision.Behavior != "allow" && decision.Behavior != "deny" {
		return "", errors.New("the Letta permission answer states neither allow nor deny")
	}
	if len(decision.Message) > 0 || decision.Behavior == "deny" {
		if _, ok := providerkit.RawString(decision.Message); !ok {
			return "", errors.New("the Letta permission answer has no valid message")
		}
	}
	if decision.Behavior == "allow" {
		input := bytes.TrimSpace(decision.UpdatedInput)
		if providerkit.RawPresent(input) && input[0] != '{' && input[0] != '[' {
			return "", errors.New("the Letta updated input is not an object or an array")
		}
		if len(decision.Suggestions) > 0 {
			var suggestions []*string
			if json.Unmarshal(decision.Suggestions, &suggestions) != nil || suggestions == nil {
				return "", errors.New("the Letta permission suggestions are not a list of strings")
			}
			for _, suggestion := range suggestions {
				if suggestion == nil {
					return "", errors.New("the Letta permission suggestion is not text")
				}
			}
		}
	}
	return reply.RequestID, nil
}

func (a *Agent) answerPermission(data []byte, scope runtimeScope) error {
	id, err := validatePermissionAnswer(data)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	pending := a.permissions[id]
	unpublished := pending != nil && pending.ready && !pending.published
	a.Mu.Unlock()
	if pending == nil || unpublished {
		return errPermissionNotPending
	}
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-pending.publicationDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.Mu.Lock()
	if a.permissions[id] != pending || pending.answering || !pending.published {
		a.Mu.Unlock()
		return errPermissionNotPending
	}
	if !pending.matches(scope) || scope.AgentID != a.agentID || scope.ConversationID != a.conversationID {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	pending.answering = true
	a.Mu.Unlock()
	err = a.sendPermissionPayload(scope, data)
	a.finishPermissionAnswer(id, pending, err, false)
	return err
}

func (a *Agent) sendPermissionPayload(scope runtimeScope, raw []byte) error {
	command := newLettaCommand("input", a.nextRequestID())
	command.Runtime = &scope
	command.Payload = json.RawMessage(raw)
	delivery := &lettaPermissionDelivery{scope: scope, waiter: newRuntimeWaiter()}
	a.Mu.Lock()
	if a.permissionDeliveries == nil {
		a.permissionDeliveries = make(map[string]*lettaPermissionDelivery)
	}
	a.permissionDeliveries[command.RequestID] = delivery
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		if a.permissionDeliveries[command.RequestID] == delivery {
			delete(a.permissionDeliveries, command.RequestID)
		}
		a.Mu.Unlock()
	}()
	err := a.sendCommand(command)
	if err == nil {
		err = delivery.waiter.wait(a.Context(), a.Clock(), a.ProcessDone(), lettaSendWait)
	}
	if errors.Is(err, errPermissionNotPending) {
		return err
	}
	if err != nil && !errors.Is(err, errAgentStopped) {
		return fmt.Errorf("%w: Letta did not confirm the permission write: %w", agent.ErrDeliveryUncertain, err)
	}
	return err
}

func (a *Agent) handlePermissionReceipt(raw []byte, requestID string) {
	a.Mu.Lock()
	delivery := a.permissionDeliveries[requestID]
	a.Mu.Unlock()
	if delivery == nil {
		return
	}
	var receipt struct {
		Runtime  runtimeScope `json:"runtime"`
		Accepted *bool        `json:"accepted"`
		Error    string       `json:"error"`
	}
	var err error
	switch {
	case json.Unmarshal(raw, &receipt) != nil || receipt.Accepted == nil:
		err = fmt.Errorf("%w: the Letta permission receipt is invalid", agent.ErrDeliveryUncertain)
	case receipt.Runtime != delivery.scope:
		err = fmt.Errorf("%w: the Letta permission receipt belongs to another runtime", agent.ErrDeliveryUncertain)
	case !*receipt.Accepted:
		err = fmt.Errorf("%w: %s", errPermissionNotPending, receipt.Error)
	}
	delivery.waiter.settle(err)
}

func (a *Agent) finishPermissionAnswer(id string, pending *lettaPendingPermission, err error, automatic bool) {
	a.Mu.Lock()
	current := a.permissions[id] == pending
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	gone := errors.Is(err, errPermissionNotPending) || a.StoppedLocked() || errors.Is(err, errAgentStopped)
	retire := err == nil || uncertain || pending.publicationFailed || gone
	published := pending.published
	if current {
		if retire {
			delete(a.permissions, id)
			pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		} else {
			pending.answering = false
			pending.automaticFailed = pending.automaticFailed || automatic
			if !published {
				pending.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if !current {
		return
	}
	if retire {
		if published && (automatic || uncertain || gone) {
			a.sink.CancelControlRequest(id)
		}
	} else if !published {
		a.publishPermission(id, pending)
	}
}

func (a *Agent) withdrawPermissions() {
	a.Mu.Lock()
	var published []string
	for id, pending := range a.permissions {
		if pending.published {
			published = append(published, id)
		}
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	}
	clear(a.permissions)
	a.Mu.Unlock()
	for _, id := range published {
		a.sink.CancelControlRequest(id)
	}
}
