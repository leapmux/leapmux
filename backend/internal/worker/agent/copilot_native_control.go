package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
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
}

type copilotPendingControl struct {
	spec            copilotControlSpec
	sessionID       string
	nativeRequestID string
	payload         []byte
	data            json.RawMessage
	ready           chan struct{}
	publicationErr  error
	responding      bool
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
func (a *copilotAgent) registerNativeControlEvents() error {
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
func (a *copilotAgent) releaseNativeControlEvents() {
	a.releaseNativeInterests(a.forgetNativeControlEvents())
}

// forgetNativeControlEvents drops the recorded handles and returns them. A caller
// that cannot reach the runtime any more uses it to avoid a release that must fail.
func (a *copilotAgent) forgetNativeControlEvents() []string {
	a.interestMu.Lock()
	defer a.interestMu.Unlock()
	handles := a.interests
	a.interests = nil
	return handles
}

func (a *copilotAgent) releaseNativeInterests(handles []string) {
	for _, handle := range handles {
		if _, err := a.requestNativeSession("eventLog.releaseInterest", map[string]any{"handle": handle}); err != nil {
			slog.Debug("Release Copilot event subscription", "agent_id", a.agentID, "handle", handle, "error", err)
		}
	}
}

// copilotControlID separates native request kinds and sessions without changing the provider payload.
func copilotControlID(sessionID, kind, requestID string) string {
	identity, _ := json.Marshal([]string{sessionID, kind, requestID})
	digest := sha256.Sum256(identity)
	return "copilot-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func (a *copilotAgent) handleNativeControlEvent(raw []byte, event copilotEvent) bool {
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
		return false
	}
	a.persistNativeFrame(raw, SpanInfo{})
	if a.closing {
		return true
	}
	var data struct {
		RequestID      string `json:"requestId"`
		ResolvedByHook bool   `json:"resolvedByHook"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.RequestID == "" {
		slog.Warn("Read Copilot control request", "event", event.Type, "error", err)
		return true
	}
	sessionID := a.currentNativeSessionID()
	identifier := copilotControlID(sessionID, spec.kind, data.RequestID)
	if completed || data.ResolvedByHook {
		a.removeNativeControl(identifier)
		return true
	}
	a.controlMu.Lock()
	if a.controls == nil {
		a.controls = make(map[string]*copilotPendingControl)
	}
	pending := a.controls[identifier]
	fresh := pending == nil || !bytes.Equal(pending.data, event.Data)
	if fresh {
		pending = &copilotPendingControl{
			spec: *spec, sessionID: sessionID, nativeRequestID: data.RequestID,
			payload: append([]byte(nil), raw...), data: append(json.RawMessage(nil), event.Data...),
			ready: make(chan struct{}),
		}
		a.controls[identifier] = pending
	}
	a.controlMu.Unlock()
	err := a.sink.PublishControlRequest(ControlRequest{RequestID: identifier, Payload: pending.payload, AgentSessionID: sessionID})
	if fresh {
		pending.publicationErr = err
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
	return true
}

func (a *copilotAgent) removeNativeControl(identifier string) {
	a.controlMu.Lock()
	_, exists := a.controls[identifier]
	delete(a.controls, identifier)
	a.controlMu.Unlock()
	if exists {
		a.sink.CancelControlRequest(identifier)
	}
}

func (a *copilotAgent) clearNativeControls() {
	a.controlMu.Lock()
	identifiers := make([]string, 0, len(a.controls))
	for identifier := range a.controls {
		identifiers = append(identifiers, identifier)
	}
	clear(a.controls)
	a.controlMu.Unlock()
	for _, identifier := range identifiers {
		a.sink.CancelControlRequest(identifier)
	}
}

func (a *copilotAgent) abortNativeControlSession(sessionID string) {
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err == nil {
		_, err = a.sendRequest("session.abort", params, a.APITimeout())
	}
	if err != nil {
		slog.Error("Cancel Copilot after a control request failed", "error", err)
	}
}

// SendRawInput accepts the shared control envelope and forwards the exact native response value.
func (a *copilotAgent) SendRawInput(raw []byte) error {
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
		return a.copilotConnection.SendRawInput(raw)
	}
	a.controlMu.Lock()
	pending := a.controls[identifier]
	a.controlMu.Unlock()
	if pending == nil {
		return fmt.Errorf("the Copilot control request is no longer pending")
	}
	select {
	case <-pending.ready:
	case <-a.ctx.Done():
		return a.ctx.Err()
	}
	if pending.publicationErr != nil {
		return pending.publicationErr
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if pending.sessionID != a.currentNativeSessionID() {
		return fmt.Errorf("the Copilot control request belongs to a previous session")
	}
	if len(envelope.Response.Response) == 0 {
		return fmt.Errorf("the Copilot control response is absent")
	}
	a.controlMu.Lock()
	if a.controls[identifier] != pending || pending.responding {
		a.controlMu.Unlock()
		return fmt.Errorf("the Copilot control request is no longer pending")
	}
	pending.responding = true
	a.controlMu.Unlock()
	accepted := false
	defer func() {
		if !accepted {
			a.controlMu.Lock()
			pending.responding = false
			a.controlMu.Unlock()
		}
	}()
	answer, err := a.completeNativeControlAnswer(envelope.Response.Response)
	if err != nil {
		return err
	}
	result, err := a.requestNativeSession(pending.spec.method, map[string]any{
		"requestId":                pending.nativeRequestID,
		pending.spec.responseField: answer,
	})
	if err != nil {
		return classifyJSONRPCDeliveryError("control response", err)
	}
	var receipt struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(result, &receipt) != nil || receipt.Success == nil {
		return fmt.Errorf("%w: Copilot returned no valid control response receipt", ErrDeliveryUncertain)
	}
	if !*receipt.Success {
		return fmt.Errorf("the Copilot runtime did not accept the control response")
	}
	accepted = true
	return nil
}

// completeNativeControlAnswer fills the one native field that only the runtime can
// supply: the location key of a project-wide approval.
//
// The pure resolution states the DECISION, and the decision is what LeapMux stores.
// The key is the runtime's own identifier for the working directory, so it is
// resolved at delivery and travels no further. Every other answer passes through
// byte for byte, which is what keeps a valid zero, false or empty value intact.
func (a *copilotAgent) completeNativeControlAnswer(answer json.RawMessage) (json.RawMessage, error) {
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
