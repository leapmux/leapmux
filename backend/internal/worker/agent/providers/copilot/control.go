package copilot

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type copilotControlSpec struct {
	kind          string
	requestEvent  string
	completeEvent string
	method        string
	responseField string
}

var copilotControlSpecs = []copilotControlSpec{
	{
		kind: "permission", requestEvent: contracts.CopilotEventPermissionRequested,
		completeEvent: contracts.CopilotEventPermissionCompleted,
		method:        "permissions.handlePendingPermissionRequest", responseField: "result",
	},
	{
		kind: "question", requestEvent: contracts.CopilotEventUserInputRequested,
		completeEvent: contracts.CopilotEventUserInputCompleted,
		method:        "ui.handlePendingUserInput", responseField: "response",
	},
	{
		kind: "plan", requestEvent: contracts.CopilotEventExitPlanModeRequested,
		completeEvent: contracts.CopilotEventExitPlanModeCompleted,
		method:        "ui.handlePendingExitPlanMode", responseField: "response",
	},
	{
		kind: "elicitation", requestEvent: contracts.CopilotEventElicitationRequested,
		completeEvent: contracts.CopilotEventElicitationCompleted,
		method:        "ui.handlePendingElicitation", responseField: "result",
	},
	{
		kind: "auto_mode_switch", requestEvent: contracts.CopilotEventAutoModeSwitchRequested,
		completeEvent: contracts.CopilotEventAutoModeSwitchCompleted,
		method:        "ui.handlePendingAutoModeSwitch", responseField: "response",
	},
	{
		kind: "sampling", requestEvent: contracts.CopilotEventSamplingRequested,
		completeEvent: contracts.CopilotEventSamplingCompleted,
		method:        "ui.handlePendingSampling", responseField: "response",
	},
}

func copilotControlSpecForRequest(eventType string) *copilotControlSpec {
	for index := range copilotControlSpecs {
		if copilotControlSpecs[index].requestEvent == eventType {
			return &copilotControlSpecs[index]
		}
	}
	return nil
}

type copilotPendingControl struct {
	order           uint64
	spec            copilotControlSpec
	sink            agent.ProviderServices
	nativeAgentID   string
	sessionID       string
	nativeRequestID string
	// instanceID identifies this publication of the request
	// (agent.ControlRequest.InstanceID). An announcement with other data under
	// the same request ID replaces the pending control, and the replacement
	// gets a new instance.
	instanceID      string
	payload         []byte
	data            json.RawMessage
	ready           chan struct{}
	publicationErr  error
	responding      bool
	published       bool
	automaticFailed bool
}

// copilotEventInterests are the events that reach LeapMux only through an explicit
// subscription. The runtime answers a question and a plan decision through its own
// callbacks otherwise, and those callbacks omit the native request ID and the
// tool-call ID that the event carries. See CP-004.
var copilotEventInterests = []string{
	contracts.CopilotEventUserInputRequested,
	contracts.CopilotEventExitPlanModeRequested,
}

// registerNativeControlEvents subscribes the current session to the control events.
//
// It records every returned handle, because the runtime keeps a subscription until
// its own handle is released. A failure releases what it already took, so a refused
// registration leaves no subscription behind. See CP-009.
func (a *Agent) registerNativeControlEvents() error {
	handles := make([]string, 0, len(copilotEventInterests))
	for _, eventType := range copilotEventInterests {
		raw, err := a.requestNativeSession("eventLog.registerInterest", map[string]any{"eventType": eventType})
		if err == nil {
			var response struct {
				Handle string `json:"handle"`
			}
			if json.Unmarshal(raw, &response) != nil || response.Handle == "" {
				err = fmt.Errorf("the Copilot control subscription has no handle")
			} else {
				handles = append(handles, response.Handle)
				continue
			}
		}
		a.releaseNativeInterests(handles)
		return fmt.Errorf("register Copilot control events: %w", err)
	}
	a.interestMu.Lock()
	a.interests = append(a.interests, handles...)
	a.interestMu.Unlock()
	return nil
}

// releaseNativeControlEvents releases every subscription of the current session.
// The runtime accepts a repeated release, so a second call is harmless.
func (a *Agent) releaseNativeControlEvents() {
	a.releaseNativeInterests(a.forgetNativeControlEvents())
}

// forgetNativeControlEvents drops the recorded handles and returns them. A caller
// that cannot reach the runtime any more uses it to avoid a release that must fail.
func (a *Agent) forgetNativeControlEvents() []string {
	a.interestMu.Lock()
	defer a.interestMu.Unlock()
	handles := a.interests
	a.interests = nil
	return handles
}

func (a *Agent) releaseNativeInterests(handles []string) {
	for _, handle := range handles {
		if _, err := a.requestNativeSession("eventLog.releaseInterest", map[string]any{"handle": handle}); err != nil {
			slog.Debug("Release Copilot event subscription", "agent_id", a.AgentID(), "handle", handle, "error", err)
		}
	}
}

// ControlRequestKey reads the key that the agent publishes a Copilot request
// under (copilotControlID): the session of the request's event, the kind of
// the event, and the runtime's own requestId. The agent reads an event only in
// the session that the event states (decodeCopilotSessionEvent), and keys its
// request by that session. LeapMux's workspace-trust question states no key,
// so the session that the Worker stores with it gives the key
// (providerkit.WorkspaceTrustRequestKey).
func (copilotProvider) ControlRequestKey(payload []byte, agentSessionID string) (string, bool) {
	if providerkit.IsWorkspaceTrustQuestion(payload) {
		return providerkit.WorkspaceTrustRequestKey(payload, agentSessionID)
	}
	var frame copilotSessionEventFrame
	if json.Unmarshal(payload, &frame) != nil || frame.Method != copilotMethodSessionEvent || frame.Params.SessionID == "" {
		return "", false
	}
	spec := copilotControlSpecForRequest(frame.Params.Event.Type)
	var data struct {
		RequestID string `json:"requestId"`
	}
	if spec == nil || json.Unmarshal(frame.Params.Event.Data, &data) != nil || data.RequestID == "" {
		return "", false
	}
	return copilotControlID(frame.Params.SessionID, spec.kind, data.RequestID), true
}

// copilotControlID separates native request kinds and sessions without changing the provider payload.
func copilotControlID(sessionID, kind, requestID string) string {
	identity, _ := json.Marshal([]string{sessionID, kind, requestID})
	digest := sha256.Sum256(identity)
	return "copilot-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func (a *Agent) handleNativeControlEvent(raw []byte, event copilotEvent) (bool, string) {
	var spec *copilotControlSpec
	completed := false
	for index := range copilotControlSpecs {
		candidate := &copilotControlSpecs[index]
		if event.Type == candidate.requestEvent || event.Type == candidate.completeEvent {
			spec = candidate
			completed = event.Type == candidate.completeEvent
			break
		}
	}
	if spec == nil {
		return false, ""
	}
	child := a.childForEvent(event)
	sink := a.sinkFor(child)
	nativeAgentID := ""
	if child != nil {
		nativeAgentID = child.nativeAgentID
	}
	a.persistNativeFrameTo(sink, raw, agent.SpanInfo{})
	if a.closing {
		return true, ""
	}
	var data struct {
		RequestID      string `json:"requestId"`
		ResolvedByHook bool   `json:"resolvedByHook"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.RequestID == "" {
		slog.Warn("Read Copilot control request", "event", event.Type, "error", err)
		return true, ""
	}
	sessionID := a.currentNativeSessionID()
	identifier := copilotControlID(sessionID, spec.kind, data.RequestID)
	if completed || data.ResolvedByHook {
		a.removeNativeControl(identifier)
		return true, ""
	}
	a.controlMu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*copilotPendingControl)
	}
	pending := a.controls[identifier]
	fresh := pending == nil || !bytes.Equal(pending.data, event.Data) || pending.nativeAgentID != nativeAgentID
	// An announcement with other data replaces the waiting request. The card of
	// the replaced instance goes, and an answer to it reaches nothing
	// (SendRawInput), because Copilot would apply it to the replacement.
	var replaced *copilotPendingControl
	if fresh {
		if pending != nil && pending.published {
			replaced = pending
		}
		a.nextControlOrder++
		pending = &copilotPendingControl{
			order: a.nextControlOrder,
			spec:  *spec, sink: sink, nativeAgentID: nativeAgentID, sessionID: sessionID, nativeRequestID: data.RequestID, instanceID: id.Generate(),
			payload: append([]byte(nil), raw...), data: append(json.RawMessage(nil), event.Data...),
			ready: make(chan struct{}),
		}
		a.controls[identifier] = pending
	}
	a.controlMu.Unlock()
	if replaced != nil {
		a.controlSink(replaced).CancelControlRequestInstance(identifier, replaced.instanceID)
	}
	a.stateMu.Lock()
	bypass := spec.kind == "permission" && a.options[agent.OptionIDPermissionMode] == contracts.CopilotPermissionModeAllowAll
	a.stateMu.Unlock()
	a.controlMu.Lock()
	bypass = bypass && !pending.automaticFailed
	a.controlMu.Unlock()
	var err error
	if !bypass {
		err = a.publishNativeControl(identifier, pending)
	}
	if fresh {
		a.controlMu.Lock()
		pending.publicationErr = err
		a.controlMu.Unlock()
		close(pending.ready)
	}
	if err != nil {
		slog.Error("Publish Copilot control request", "request_id", identifier, "error", err)
		if fresh {
			a.controlMu.Lock()
			if a.controls[identifier] == pending {
				delete(a.controls, identifier)
			}
			a.controlMu.Unlock()
			go a.abortNativeControlSession(sessionID)
		}
	}
	if err == nil && spec.kind == "permission" {
		a.offReader(copilotApprovePermissions, a.approvePendingPermissions)
	}
	return true, identifier
}

func (a *Agent) controlSink(pending *copilotPendingControl) agent.ProviderServices {
	if pending != nil && pending.sink != nil {
		return pending.sink
	}
	return a.sink
}

// publishNativeControl also retires a card whose native completion raced publication.
// It retires the card of its own instance, so a replacement that arrived meanwhile
// keeps its card.
func (a *Agent) publishNativeControl(identifier string, pending *copilotPendingControl) error {
	sink := a.controlSink(pending)
	err := sink.PublishControlRequest(agent.ControlRequest{RequestID: identifier, InstanceID: pending.instanceID, Payload: pending.payload, AgentSessionID: pending.sessionID})
	if err != nil {
		return err
	}
	a.controlMu.Lock()
	pending.published = true
	current := a.controls[identifier] == pending
	a.controlMu.Unlock()
	if !current {
		sink.CancelControlRequestInstance(identifier, pending.instanceID)
	}
	return nil
}

// removeNativeControl forgets the request that Copilot settled under identifier,
// whichever instance shows it, and withdraws that instance's card.
func (a *Agent) removeNativeControl(identifier string) {
	a.controlMu.Lock()
	pending := a.controls[identifier]
	delete(a.controls, identifier)
	a.controlMu.Unlock()
	a.withdrawNativeControlCard(identifier, pending)
}

// withdrawNativeControl forgets pending while it is still the request under
// identifier, withdraws its card, and reports whether it did. A replacement
// keeps its own card.
func (a *Agent) withdrawNativeControl(identifier string, pending *copilotPendingControl) bool {
	a.controlMu.Lock()
	if a.controls[identifier] != pending {
		a.controlMu.Unlock()
		return false
	}
	delete(a.controls, identifier)
	a.controlMu.Unlock()
	a.withdrawNativeControlCard(identifier, pending)
	return true
}

// withdrawNativeControlCard withdraws the card of one published instance. A nil or
// unpublished instance has no card.
func (a *Agent) withdrawNativeControlCard(identifier string, pending *copilotPendingControl) {
	if pending == nil {
		return
	}
	a.controlMu.Lock()
	published := pending.published
	a.controlMu.Unlock()
	if published {
		a.controlSink(pending).CancelControlRequestInstance(identifier, pending.instanceID)
	}
}

func (a *Agent) clearNativeControlsMatching(match func(*copilotPendingControl) bool) {
	type withdrawn struct {
		order                  uint64
		identifier, instanceID string
		sink                   agent.ProviderServices
	}
	var canceled []withdrawn
	a.controlMu.Lock()
	for identifier, pending := range a.controls {
		if !match(pending) {
			continue
		}
		if pending.published {
			canceled = append(canceled, withdrawn{
				order: pending.order, identifier: identifier, instanceID: pending.instanceID, sink: a.controlSink(pending),
			})
		}
		delete(a.controls, identifier)
	}
	a.controlMu.Unlock()
	slices.SortFunc(canceled, func(left, right withdrawn) int {
		return cmp.Compare(left.order, right.order)
	})
	for _, item := range canceled {
		item.sink.CancelControlRequestInstance(item.identifier, item.instanceID)
	}
}

func (a *Agent) clearNativeChildControls(nativeAgentID string) {
	if nativeAgentID == "" {
		return
	}
	a.clearNativeControlsMatching(func(pending *copilotPendingControl) bool {
		return pending.nativeAgentID == nativeAgentID
	})
}

func (a *Agent) clearNativeControls() {
	a.clearNativeControlsMatching(func(*copilotPendingControl) bool { return true })
	// The folder-trust question belongs to the session too.
	a.workspaceTrust.Retire(a.sink)
}

func (a *Agent) abortNativeControlSession(sessionID string) {
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err == nil {
		_, err = a.SendRequest("session.abort", params, a.APITimeout())
	}
	if err != nil {
		slog.Error("Cancel Copilot after a control request failed", "error", err)
	}
}

// SendRawInput accepts the shared control envelope and forwards the exact native response value.
func (a *Agent) SendRawInput(input agent.RawInput) error {
	raw := input.Data
	var envelope struct {
		Response struct {
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode Copilot control response: %w", err)
	}
	identifier := envelope.Response.RequestID
	if identifier == "" {
		return a.copilotConnection.SendRawInput(input)
	}
	if providerkit.IsWorkspaceTrustRequestID(identifier) {
		// LeapMux asked this question, so no runtime request waits for the answer.
		a.sessionMu.RLock()
		defer a.sessionMu.RUnlock()
		_, err := a.workspaceTrust.Answer(a.sink, copilotWorkspaceTrustHost{a}, input)
		return err
	}
	a.controlMu.Lock()
	pending := a.controls[identifier]
	a.controlMu.Unlock()
	if pending == nil {
		return fmt.Errorf("the Copilot control request is no longer pending")
	}
	// Copilot applies an answer to the request ID, and an announcement with other
	// data replaces the request under it (handleNativeControlEvent). So the answer
	// goes only to the instance that it states.
	if err := agent.ControlInstanceRefusal(pending.instanceID, input.ControlInstanceID); err != nil {
		return err
	}
	// Publication must finish before this answer can hold the session in place.
	select {
	case <-pending.ready:
	case <-a.Context().Done():
		return a.Context().Err()
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.answerNativeControl(identifier, pending, envelope.Response.Response, false)
}

var errBypassInactive = errors.New("the Copilot Bypass preset no longer applies")

// answerNativeControl claims one exact request. The caller holds sessionMu.
func (a *Agent) answerNativeControl(identifier string, pending *copilotPendingControl, response json.RawMessage, automatic bool) error {
	select {
	case <-pending.ready:
	case <-a.Context().Done():
		return a.Context().Err()
	}
	if pending.sessionID != a.currentNativeSessionID() {
		return fmt.Errorf("the Copilot control request belongs to a previous session")
	}
	if len(response) == 0 {
		return fmt.Errorf("the Copilot control response is absent")
	}
	a.stateMu.Lock()
	if automatic && a.options[agent.OptionIDPermissionMode] != contracts.CopilotPermissionModeAllowAll {
		a.stateMu.Unlock()
		return errBypassInactive
	}
	a.controlMu.Lock()
	if pending.publicationErr != nil {
		a.controlMu.Unlock()
		a.stateMu.Unlock()
		return pending.publicationErr
	}
	if current := a.controls[identifier]; current != pending {
		a.controlMu.Unlock()
		a.stateMu.Unlock()
		if current != nil && current.instanceID != pending.instanceID {
			return agent.ErrControlInstanceReplaced
		}
		return fmt.Errorf("the Copilot control request is no longer pending")
	}
	if pending.responding {
		a.controlMu.Unlock()
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot control request is no longer pending")
	}
	pending.responding = true
	a.controlMu.Unlock()
	a.stateMu.Unlock()
	retainClaim := false
	defer func() {
		if !retainClaim {
			a.controlMu.Lock()
			pending.responding = false
			a.controlMu.Unlock()
		}
	}()
	answer, err := a.completeNativeControlAnswer(response)
	if err != nil {
		return err
	}
	values := map[string]any{"requestId": pending.nativeRequestID, pending.spec.responseField: answer}
	if pending.spec.kind == "sampling" {
		values, err = a.resolveSampling(pending, response)
		if err != nil {
			retainClaim = errors.Is(err, agent.ErrDeliveryUncertain)
			return err
		}
		a.controlMu.Lock()
		current := a.controls[identifier]
		a.controlMu.Unlock()
		if current != pending {
			if current != nil && current.instanceID != pending.instanceID {
				return agent.ErrControlInstanceReplaced
			}
			return fmt.Errorf("the Copilot sampling request is no longer pending")
		}
	}
	result, err := a.requestNativeSession(pending.spec.method, values)
	if err != nil {
		err = providerkit.ClassifyJSONRPCDeliveryError("control response", err)
		retainClaim = errors.Is(err, agent.ErrDeliveryUncertain)
		return err
	}
	var receipt struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(result, &receipt) != nil || receipt.Success == nil {
		retainClaim = true
		return fmt.Errorf("%w: Copilot returned no valid control response receipt", agent.ErrDeliveryUncertain)
	}
	if !*receipt.Success {
		return fmt.Errorf("the Copilot runtime did not accept the control response")
	}
	retainClaim = true
	return nil
}

// approvePendingPermissions runs under sessionMu, outside the event reader.
// The existing delivery path checks native receipts and excludes competing answers.
func (a *Agent) approvePendingPermissions() {
	type pendingApproval struct {
		identifier string
		pending    *copilotPendingControl
	}
	a.controlMu.Lock()
	var permissions []pendingApproval
	for identifier, pending := range a.controls {
		if pending.spec.kind == "permission" && !pending.responding && !pending.automaticFailed {
			select {
			case <-pending.ready:
				permissions = append(permissions, pendingApproval{identifier: identifier, pending: pending})
			default:
			}
		}
	}
	a.controlMu.Unlock()
	slices.SortFunc(permissions, func(left, right pendingApproval) int {
		return cmp.Compare(left.pending.order, right.pending.order)
	})
	response, _ := json.Marshal(map[string]string{"kind": contracts.CopilotDecisionApproveOnce})
	for _, entry := range permissions {
		identifier, pending := entry.identifier, entry.pending
		err := a.answerNativeControl(identifier, pending, response, true)
		a.controlMu.Lock()
		current := a.controls[identifier] == pending
		published := pending.published
		retire := current && (err == nil || errors.Is(err, agent.ErrDeliveryUncertain))
		if retire {
			delete(a.controls, identifier)
		} else if current && err != nil && !errors.Is(err, errBypassInactive) {
			pending.automaticFailed = true
		}
		a.controlMu.Unlock()
		if retire && published {
			a.controlSink(pending).CancelControlRequestInstance(identifier, pending.instanceID)
		}
		if err == nil || !current || retire {
			continue
		}
		if !errors.Is(err, errBypassInactive) {
			slog.Warn("Copilot could not approve a pending tool", "request_id", identifier, "error", err)
		}
		// A failed automatic answer needs a manual card. A narrower mode does too.
		if !published {
			if publishErr := a.publishNativeControl(identifier, pending); publishErr != nil {
				slog.Error("Publish Copilot permission after failed approval", "request_id", identifier, "error", publishErr)
				// Only the current request leaves the turn waiting for an answer that no
				// reader can give. A replacement that arrived meanwhile has its own card,
				// so the turn that waits for it goes on.
				if a.withdrawNativeControl(identifier, pending) {
					a.abortNativeControlSession(pending.sessionID)
				}
			}
		}
	}
}

// completeNativeControlAnswer fills the one native field that only the runtime can
// supply: the location key of a project-wide approval.
//
// The pure resolution states the DECISION, and the decision is what LeapMux stores.
// The key is the runtime's own identifier for the working directory, so it is
// resolved at delivery and travels no further. Every other answer passes through
// byte for byte, which is what keeps a valid zero, false or empty value intact.
func (a *Agent) completeNativeControlAnswer(answer json.RawMessage) (json.RawMessage, error) {
	var decision struct {
		Kind        string `json:"kind"`
		LocationKey string `json:"locationKey"`
	}
	if json.Unmarshal(answer, &decision) != nil || decision.Kind != copilotDecisionApproveForLocation || decision.LocationKey != "" {
		return answer, nil
	}
	raw, err := a.requestNativeSession("permissions.locations.resolve", map[string]any{
		"workingDirectory": a.opts.WorkingDir,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve the Copilot permission location: %w", err)
	}
	var location struct {
		LocationKey string `json:"locationKey"`
	}
	if json.Unmarshal(raw, &location) != nil || location.LocationKey == "" {
		return nil, fmt.Errorf("the Copilot runtime reported no permission location for this directory")
	}
	key, err := json.Marshal(location.LocationKey)
	if err != nil {
		return nil, err
	}
	// Set the field without rewriting the bytes around it, so every other value the
	// decision carries reaches the runtime exactly as it was stored.
	completed, err := jsonfield.Set(answer, key, copilotLocationKeyField)
	if err != nil {
		return nil, fmt.Errorf("address the Copilot approval to its location: %w", err)
	}
	return completed, nil
}

// resolveSampling runs an allowed inference before it answers the native request.
// The caller holds sessionMu for reading, so ClearContext waits for the inference.
// Stop or process exit cancels the call. A model response has no fixed timeout.
func (a *Agent) resolveSampling(pending *copilotPendingControl, response json.RawMessage) (map[string]any, error) {
	values := map[string]any{"requestId": pending.nativeRequestID}
	switch providerkit.StringField(response, "decision") {
	case contracts.CopilotSamplingDecisionDeny:
		return values, nil
	case contracts.CopilotSamplingDecisionAllow:
	default:
		return nil, fmt.Errorf("the Copilot sampling decision is invalid")
	}
	var data struct {
		ServerName   string          `json:"serverName"`
		MCPRequestID json.RawMessage `json:"mcpRequestId"`
		Request      json.RawMessage `json:"request"`
	}
	if json.Unmarshal(pending.data, &data) != nil || data.ServerName == "" || !providerkit.RawPresent(data.MCPRequestID) || !providerkit.RawPresent(data.Request) {
		return nil, fmt.Errorf("the Copilot sampling request is incomplete")
	}
	ctx := a.samplingContext()
	raw, err := a.requestSessionContext(ctx, pending.sessionID, "mcp.executeSampling", map[string]any{
		"requestId":    "leapmux-sampling-" + pending.nativeRequestID,
		"serverName":   data.ServerName,
		"mcpRequestId": data.MCPRequestID,
		"request":      data.Request,
	}, 0)
	if err != nil {
		return nil, providerkit.ClassifyJSONRPCDeliveryError("sampling inference", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, providerkit.ClassifyJSONRPCDeliveryError("sampling inference", err)
	}
	var result struct {
		Action string          `json:"action"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: Copilot returned an invalid sampling result", agent.ErrDeliveryUncertain)
	}
	switch result.Action {
	case "success":
		if !providerkit.RawPresent(result.Result) {
			return nil, fmt.Errorf("%w: Copilot returned no sampling result", agent.ErrDeliveryUncertain)
		}
		values["response"] = result.Result
	case "failure", "cancelled":
		// An absent response rejects the native sampling request.
	default:
		return nil, fmt.Errorf("%w: Copilot returned an unknown sampling outcome", agent.ErrDeliveryUncertain)
	}
	return values, nil
}
