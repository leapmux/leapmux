package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type openCodeControlOwner struct {
	nativeSessionID string
	sessionID       string
	sink            agent.ProviderServices
	// current reads captured session authority only. Callers do not hold the owner mutex when entering this registry.
	current func() bool
}

type openCodePendingControl struct {
	owner              openCodeControlOwner
	requestID          string
	nativeID           string
	instanceID         string
	kind               string
	payload            []byte
	order              uint64
	publishing         bool
	published          bool
	publicationFailed  bool
	answering          bool
	uncertain          bool
	attempt            uint64
	automaticAttempted bool
}

type openCodeControls struct {
	mu              sync.Mutex
	ctx             context.Context
	rpc             openCodeRPC
	pending         map[string]*openCodePendingControl
	nextOrder       uint64
	autoApprove     bool
	closed          bool
	work            providerkit.CoalescingWork
	requestRecovery func()
}

func newOpenCodeControls(ctx context.Context, rpc openCodeRPC, requestRecovery func()) (*openCodeControls, error) {
	if ctx == nil || rpc.endpoint == nil || requestRecovery == nil {
		return nil, errors.New("OpenCode controls require context, transport, and recovery notification")
	}
	return &openCodeControls{ctx: ctx, rpc: rpc, pending: make(map[string]*openCodePendingControl), requestRecovery: requestRecovery}, nil
}

func openCodeControlKey(kind, sessionID, nativeID string) string {
	return "opencode:" + kind + ":" + sessionID + ":" + nativeID
}

// ControlRequestKey reads the key that register gives a stored native request
// (openCodeControlKey): its kind, the session that owns it, and its id. A
// permission states its session and its id itself, and a form states them in
// its `form`. register keys a request by the session of its owner, and
// publishPermission and publishForm refuse a request of another session, so
// the session that the request states is the owner's.
func (opencodeProvider) ControlRequestKey(payload []byte, _ string) (string, bool) {
	var request struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &request) != nil {
		return "", false
	}
	var kind string
	var native struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
	}
	switch request.Type {
	case contracts.OpenCodeNativeEventPermissionAsked:
		kind = "permission"
		if json.Unmarshal(request.Data, &native) != nil {
			return "", false
		}
	case contracts.OpenCodeNativeEventFormCreated:
		kind = "form"
		var data struct {
			Form json.RawMessage `json:"form"`
		}
		if json.Unmarshal(request.Data, &data) != nil || json.Unmarshal(data.Form, &native) != nil {
			return "", false
		}
	default:
		return "", false
	}
	if native.ID == "" || native.SessionID == "" {
		return "", false
	}
	return openCodeControlKey(kind, native.SessionID, native.ID), true
}

func (r *openCodeControls) publishPermission(owner openCodeControlOwner, raw json.RawMessage) error {
	permission, err := readOpenCodePermission(raw)
	if err != nil {
		return err
	}
	if permission.SessionID != owner.nativeSessionID {
		return errors.New("the OpenCode permission belongs to another owner")
	}
	header := map[string]any{"tool_name": permission.Action}
	if permission.Source != nil && permission.Source.MessageID != "" {
		header["tool_use_id"] = openCodeToolSpanID(permission.Source.MessageID, permission.Source.ID)
	}
	return r.register(owner, permission.ID, "permission", contracts.OpenCodeNativeEventPermissionAsked, raw, header)
}

func (r *openCodeControls) publishForm(owner openCodeControlOwner, raw json.RawMessage) error {
	form, err := readOpenCodeForm(raw)
	if err != nil {
		return err
	}
	if form.SessionID != owner.nativeSessionID {
		return errors.New("the OpenCode form belongs to another owner")
	}
	var metadata struct {
		Kind string `json:"kind"`
		Tool *struct {
			ID        string `json:"id"`
			MessageID string `json:"messageID"`
		} `json:"tool"`
	}
	if providerkit.RawPresent(form.Metadata) {
		if err := json.Unmarshal(form.Metadata, &metadata); err != nil {
			return err
		}
	}
	if form.SessionID == "global" && metadata.Kind != "mcp-elicitation" {
		return errors.New("the global OpenCode owner is only valid for an MCP elicitation")
	}
	toolName := metadata.Kind
	if toolName == "" {
		toolName = "form"
	}
	header := map[string]any{"tool_name": toolName}
	if metadata.Tool != nil && metadata.Tool.MessageID != "" {
		header["tool_use_id"] = openCodeToolSpanID(metadata.Tool.MessageID, metadata.Tool.ID)
	}
	data, err := json.Marshal(map[string]json.RawMessage{"form": raw})
	if err != nil {
		return err
	}
	return r.register(owner, form.ID, "form", contracts.OpenCodeNativeEventFormCreated, data, header)
}

func (r *openCodeControls) register(owner openCodeControlOwner, nativeID, kind, eventType string, data json.RawMessage, header map[string]any) error {
	if owner.sink == nil || owner.current == nil || owner.sessionID == "" {
		return errors.New("the OpenCode control has no publication owner")
	}
	payload, err := json.Marshal(map[string]any{"type": eventType, "data": data, "request": header})
	if err != nil {
		return err
	}
	key := openCodeControlKey(kind, owner.nativeSessionID, nativeID)
	var replaced *openCodePendingControl
	r.mu.Lock()
	if r.closed || !owner.current() {
		r.mu.Unlock()
		return agent.ErrControlRequestNotPending
	}
	if previous := r.pending[key]; previous != nil {
		if previous.owner.sessionID == owner.sessionID {
			retry := previous.publicationFailed && !previous.published && !previous.publishing && !previous.answering
			r.mu.Unlock()
			if retry {
				return r.publish(previous)
			}
			return nil
		}
		if previous.answering {
			r.mu.Unlock()
			return errors.New("the previous OpenCode control owner still has a reply in flight")
		}
		replaced = previous
	}
	r.nextOrder++
	record := &openCodePendingControl{owner: owner, requestID: key, nativeID: nativeID, instanceID: id.Generate(), kind: kind, payload: payload, order: r.nextOrder}
	r.pending[key] = record
	automatic := r.autoApprove && kind == "permission"
	replacedPublished := replaced != nil && replaced.published
	r.mu.Unlock()
	if replacedPublished {
		replaced.owner.sink.CancelControlRequestInstance(replaced.requestID, replaced.instanceID)
	}
	if automatic {
		r.queueApprovals()
		return nil
	}
	return r.publish(record)
}

func (r *openCodeControls) publish(record *openCodePendingControl) error {
	r.mu.Lock()
	if r.closed || r.pending[record.requestID] != record || record.publishing || record.published || !record.owner.current() {
		r.mu.Unlock()
		return nil
	}
	record.publishing = true
	r.mu.Unlock()
	err := record.owner.sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: record.owner.sessionID, RequestID: record.requestID, InstanceID: record.instanceID, Payload: record.payload})
	r.mu.Lock()
	record.publishing = false
	current := r.pending[record.requestID] == record
	if err == nil {
		record.published = true
		record.publicationFailed = false
	} else {
		record.publicationFailed = true
	}
	withdraw := err == nil && !current
	reject := err != nil && current && !record.answering && !record.uncertain
	if reject {
		delete(r.pending, record.requestID)
	}
	automatic := err == nil && current && r.autoApprove && record.kind == "permission"
	r.mu.Unlock()
	if withdraw {
		record.owner.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
	if reject {
		return errors.Join(err, r.rejectNative(record))
	}
	if automatic {
		r.queueApprovals()
	}
	return err
}

func (r *openCodeControls) autoApproveEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.autoApprove
}
func (r *openCodeControls) setAutoApprove(enabled bool) {
	r.mu.Lock()
	previous := r.autoApprove
	r.autoApprove = enabled
	var publish []*openCodePendingControl
	for _, record := range r.pending {
		if record.kind != "permission" {
			continue
		}
		if enabled && !previous {
			record.automaticAttempted = false
		}
		if !enabled && !record.published && !record.publishing && !record.answering {
			publish = append(publish, record)
		}
	}
	r.mu.Unlock()
	for _, record := range publish {
		if err := r.publish(record); err != nil {
			slog.Error("OpenCode could not publish a permission after Auto-approve was disabled", "request_id", record.requestID, "error", err)
		}
	}
	if enabled {
		r.queueApprovals()
	}
}
func (r *openCodeControls) queueApprovals() { r.work.Run("permissions", r.approvePending) }

// autoApprovalReady requires the registry mutex.
func (record *openCodePendingControl) autoApprovalReady() bool {
	return record.kind == "permission" && !record.publishing && !record.answering && !record.uncertain && !record.automaticAttempted
}

func (r *openCodeControls) approvePending() {
	r.mu.Lock()
	if r.closed || !r.autoApprove {
		r.mu.Unlock()
		return
	}
	var candidates []*openCodePendingControl
	for _, record := range r.pending {
		if record.autoApprovalReady() {
			candidates = append(candidates, record)
		}
	}
	r.mu.Unlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].order < candidates[j].order })
	for _, selected := range candidates {
		r.mu.Lock()
		if r.closed || !r.autoApprove {
			r.mu.Unlock()
			return
		}
		if r.pending[selected.requestID] != selected || !selected.autoApprovalReady() || !selected.owner.current() {
			r.mu.Unlock()
			continue
		}
		selected.answering = true
		selected.automaticAttempted = true
		selected.attempt++
		r.mu.Unlock()
		err := r.send(selected, openCodeControlAnswer{RequestID: selected.requestID, Decision: contracts.OpenCodeNativePermissionDecisionOnce})
		if err != nil {
			r.mu.Lock()
			current := r.pending[selected.requestID] == selected
			r.mu.Unlock()
			slog.Warn("OpenCode could not confirm automatic approval", "request_id", selected.requestID, "error", err)
			if current {
				if publishErr := r.publish(selected); publishErr != nil {
					slog.Error("OpenCode could not publish the pending permission", "request_id", selected.requestID, "error", publishErr)
				}
			}
		}
	}
}

func (r *openCodeControls) answer(raw []byte) error {
	var header struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	r.mu.Lock()
	record := r.pending[header.RequestID]
	r.mu.Unlock()
	if record == nil {
		return agent.ErrControlRequestNotPending
	}
	answer, err := readOpenCodeControlAnswer(raw, record.requestID, record.kind)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed || r.pending[record.requestID] != record || !record.owner.current() {
		r.mu.Unlock()
		return agent.ErrControlRequestNotPending
	}
	if record.uncertain {
		r.mu.Unlock()
		return agent.ErrDeliveryUncertain
	}
	if record.answering {
		r.mu.Unlock()
		return errors.New("the OpenCode control reply is already in flight")
	}
	record.answering = true
	record.attempt++
	r.mu.Unlock()
	return r.send(record, answer)
}

// reconcilePending repairs existing records from a complete native snapshot.
// A reply attempt or settlement after the read starts takes precedence. The
// snapshot never recreates a record that a newer observation removed.
func (r *openCodeControls) reconcilePending(kind, sessionID string) error {
	type observation struct {
		record  *openCodePendingControl
		attempt uint64
	}
	r.mu.Lock()
	observations := make([]observation, 0, len(r.pending))
	for _, record := range r.pending {
		if record.kind == kind && record.owner.nativeSessionID == sessionID && !record.answering {
			observations = append(observations, observation{record: record, attempt: record.attempt})
		}
	}
	r.mu.Unlock()
	var snapshot []json.RawMessage
	var err error
	switch kind {
	case "permission":
		snapshot, err = r.rpc.pendingPermissions(r.ctx, sessionID)
	case "form":
		snapshot, err = r.rpc.pendingForms(r.ctx, sessionID)
	default:
		return fmt.Errorf("unknown OpenCode control kind %q", kind)
	}
	if err != nil {
		return err
	}
	pending := make(map[string]bool, len(snapshot))
	for _, raw := range snapshot {
		if kind == "permission" {
			value, err := readOpenCodePermission(raw)
			if err != nil {
				return err
			}
			pending[value.ID] = true
		} else {
			value, err := readOpenCodeForm(raw)
			if err != nil {
				return err
			}
			pending[value.ID] = true
		}
	}
	r.mu.Lock()
	var retired, replacements []*openCodePendingControl
	for _, observation := range observations {
		record := observation.record
		if r.closed || r.pending[record.requestID] != record || record.attempt != observation.attempt || record.answering || !record.owner.current() {
			continue
		}
		if pending[record.nativeID] {
			if record.uncertain {
				replacement := &openCodePendingControl{
					owner: record.owner, requestID: record.requestID, nativeID: record.nativeID,
					instanceID: id.Generate(), kind: record.kind, payload: record.payload,
					order: record.order, automaticAttempted: record.automaticAttempted,
				}
				r.pending[record.requestID] = replacement
				replacements = append(replacements, replacement)
				if record.published {
					retired = append(retired, record)
				}
			}
			continue
		}
		delete(r.pending, record.requestID)
		if record.published {
			retired = append(retired, record)
		}
	}
	r.mu.Unlock()
	cancelOpenCodeControlInstances(retired)
	var publicationErr error
	for _, record := range replacements {
		publicationErr = errors.Join(publicationErr, r.publish(record))
	}
	return publicationErr
}

func (r *openCodeControls) send(record *openCodePendingControl, answer openCodeControlAnswer) error {
	var err error
	if record.kind == "permission" {
		err = r.rpc.replyPermission(r.ctx, record.owner.nativeSessionID, record.nativeID, answer.Decision, answer.Message)
	} else if answer.Cancel {
		err = r.rpc.cancelForm(r.ctx, record.owner.nativeSessionID, record.nativeID)
	} else {
		values := make(map[string]any, len(answer.Answer))
		for key, value := range answer.Answer {
			values[key] = value
		}
		err = r.rpc.replyForm(r.ctx, record.owner.nativeSessionID, record.nativeID, values)
	}
	settledElsewhere := providerkit.IsHTTPStatus(err, http.StatusNotFound) || providerkit.IsHTTPStatus(err, http.StatusConflict)
	uncertain := openCodeDeliveryUncertain(err)
	r.mu.Lock()
	current := r.pending[record.requestID] == record
	record.answering = false
	record.uncertain = uncertain
	remove := current && (err == nil || settledElsewhere || record.publicationFailed && !uncertain)
	if remove {
		delete(r.pending, record.requestID)
	}
	withdraw := remove && record.published
	reject := remove && err != nil && !settledElsewhere && record.publicationFailed
	r.mu.Unlock()
	if withdraw {
		record.owner.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
	if reject {
		err = errors.Join(err, r.rejectNative(record))
	}
	if uncertain {
		r.requestRecovery()
		return fmt.Errorf("%w: OpenCode did not confirm the control reply: %w", agent.ErrDeliveryUncertain, err)
	}
	return err
}

func (r *openCodeControls) rejectNative(record *openCodePendingControl) error {
	if record.kind == "permission" {
		return r.rpc.replyPermission(r.ctx, record.owner.nativeSessionID, record.nativeID, contracts.OpenCodeNativePermissionDecisionReject, providerkit.ControlPublicationFailure)
	}
	return r.rpc.cancelForm(r.ctx, record.owner.nativeSessionID, record.nativeID)
}

func (r *openCodeControls) retire(kind, sessionID, nativeID string) {
	key := openCodeControlKey(kind, sessionID, nativeID)
	r.mu.Lock()
	record := r.pending[key]
	delete(r.pending, key)
	published := record != nil && record.published
	r.mu.Unlock()
	if published {
		record.owner.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
}

// retireOwner withdraws cards from one publication session. Native forms that
// remain pending can later be published under a replacement broker session.
func (r *openCodeControls) retireOwner(sessionID string) {
	r.mu.Lock()
	retired := r.removePendingLocked(func(record *openCodePendingControl) bool { return record.owner.sessionID == sessionID })
	r.mu.Unlock()
	cancelOpenCodeControlInstances(retired)
}

// removePendingLocked captures publication state while the registry mutex is held.
func (r *openCodeControls) removePendingLocked(matches func(*openCodePendingControl) bool) []*openCodePendingControl {
	var retired []*openCodePendingControl
	for key, record := range r.pending {
		if !matches(record) {
			continue
		}
		if record.published {
			retired = append(retired, record)
		}
		delete(r.pending, key)
	}
	return retired
}

func cancelOpenCodeControlInstances(records []*openCodePendingControl) {
	for _, record := range records {
		record.owner.sink.CancelControlRequestInstance(record.requestID, record.instanceID)
	}
}

func (r *openCodeControls) close() {
	r.mu.Lock()
	r.closed = true
	retired := r.removePendingLocked(func(*openCodePendingControl) bool { return true })
	r.pending = make(map[string]*openCodePendingControl)
	r.mu.Unlock()
	r.work.CloseAndWait()
	cancelOpenCodeControlInstances(retired)
}
