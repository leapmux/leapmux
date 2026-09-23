package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// autoModeUnavailableErrorPrefix is the prefix of the error message Claude
// Code returns when set_permission_mode:auto is rejected (regardless of
// reason: admin settings, plan circuit-breaker, or unsupported model).
const autoModeUnavailableErrorPrefix = "Cannot set permission mode to auto"

// claudeCodeControlResult holds the outcome of a pending control request.
type claudeCodeControlResult struct {
	Success               bool
	Mode                  string
	Error                 string
	OutputStyle           string
	AvailableOutputStyles []string
	FastModeState         string // "off", "cooldown", "on", or "" (unavailable)
	// Models / UnavailableModels carry the model catalog from the initialize
	// response (see claudeCodeModelInfo). UnavailableModels are the
	// visible-but-not-selectable entries the CLI reports separately; we drop
	// them during conversion. Note: Claude Code only emits unavailable_models to an
	// allowlisted first-party host entrypoint (currently the VS Code extension), so
	// for LeapMux's "cli" entrypoint this is effectively always empty -- the dynamic
	// catalog drops only `disabled` rows. Parsing it anyway keeps us correct if that
	// host allowlist ever widens.
	Models            []claudeCodeModelInfo
	UnavailableModels []claudeCodeModelInfo
	RawResponse       json.RawMessage
}

// sendControlAndWait sends a control request to the agent and waits for the
// response. The requestBody should be the JSON for the "request" field only
// (e.g. `{"subtype":"initialize"}`). Returns the control result or an error
// on timeout/cancellation/failure.
func (a *Agent) sendControlAndWait(ctx context.Context, requestBody string, timeout time.Duration) (claudeCodeControlResult, error) {
	_, resp, err := a.sendControlAndWaitWithID(ctx, requestBody, timeout)
	return resp, err
}

// sendControlAndWaitWithID is sendControlAndWait that also returns the generated request_id, so
// a caller that needs to correlate a LATE (deferred) control_response with the request it
// belongs to -- the set_permission_mode path, whose ack the CLI holds until an active turn
// ends -- can record that id. Most callers use sendControlAndWait and ignore it.
func (a *Agent) sendControlAndWaitWithID(ctx context.Context, requestBody string, timeout time.Duration) (string, claudeCodeControlResult, error) {
	requestID := id.Short()
	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl(requestID, ch)

	msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`, requestID, requestBody)
	if err := a.SendRawInput([]byte(msg)); err != nil {
		a.unregisterPendingControl(requestID)
		// A write failure almost always means the child closed its stdin —
		// i.e. it exited before we could hand off the request. Wait briefly
		// for the wait goroutine to finalize so callers see "agent process
		// exited with code N" (with captured stderr) instead of a raw
		// "broken pipe" symptom. This also removes a race in
		// TestAgent_EarlyExitDetected where, on fast Linux runners, the
		// subprocess exits before the initialize write reaches the pipe.
		select {
		case <-a.ProcessDone():
			return requestID, claudeCodeControlResult{}, a.ProcessExitError()
		case <-time.After(1 * time.Second):
			return requestID, claudeCodeControlResult{}, err
		}
	}
	agent.TraceStartupPhase(a.AgentID(), "control_stdin_write")

	select {
	case resp := <-ch:
		a.unregisterPendingControl(requestID)
		if !resp.Success {
			return requestID, resp, fmt.Errorf("%s", resp.Error)
		}
		return requestID, resp, nil
	case <-a.ProcessDone():
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, a.ProcessExitError()
	case <-ctx.Done():
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, ctx.Err()
	case <-time.After(timeout):
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, errControlTimeout
	}
}

// errControlTimeout is returned by sendControlAndWait when the agent does not respond to a
// control request within the timeout. It is a sentinel (its message is unchanged) so the
// live permission-mode path can tell a deferred ack -- the CLI holds the set_permission_mode
// response until an active turn ends -- from a genuine failure via errors.Is.
var errControlTimeout = errors.New("timeout waiting for agent to respond")

// sendApplyFlagSettings marshals flagSettings into an apply_flag_settings
// control request and sends it, returning the control error (if any). The
// envelope shape lives here so the startup path (Start) and the live
// path (UpdateSettings) don't each hand-roll the same JSON literal.
func (a *Agent) sendApplyFlagSettings(ctx context.Context, flagSettings map[string]interface{}, timeout time.Duration) error {
	body, _ := json.Marshal(map[string]interface{}{
		"subtype":  "apply_flag_settings",
		"settings": flagSettings,
	})
	_, err := a.sendControlAndWait(ctx, string(body), timeout)
	return err
}

// applyStartupPermissionMode sets the agent's permission mode during startup
// while also detecting whether auto mode is available for this session.
// a.autoModeAvailable is updated as a side effect; the returned result
// reflects the mode actually applied (which may be default if the requested
// auto mode was rejected with autoModeUnavailableErrorPrefix).
//
// When requested == auto a single set_permission_mode call serves both
// purposes; otherwise auto is probed first (leaving the session briefly in
// auto on success) before the requested mode is applied to restore the
// intended state. Transient probe errors are treated as unavailable so the
// UI does not offer a mode the agent cannot enter.
func (a *Agent) applyStartupPermissionMode(ctx context.Context, requested string, timeout time.Duration) (claudeCodeControlResult, error) {
	if requested == contracts.ClaudeModeAuto {
		resp, err := a.sendSetPermissionMode(ctx, contracts.ClaudeModeAuto, timeout)
		if err == nil {
			a.setAutoModeAvailable(true)
			return a.settleStartupPermissionMode(resp, nil)
		}
		// Registration.PermissionDefaults.NewSession requests auto for each new
		// session. Fall back after any error, including a timeout or a transport
		// error. Such an error does not show whether default mode works. The probe
		// path below also treats a transient failure as unavailable. If default
		// mode fails too, settleStartupPermissionMode returns its error.
		if isAutoModeUnavailableError(err) {
			slog.Warn("requested auto permission mode is unavailable; falling back to default",
				"agent_id", a.AgentID())
		} else {
			slog.Warn("auto permission mode failed (transient); falling back to default",
				"agent_id", a.AgentID(), "error", err)
		}
		a.setAutoModeAvailable(false)
		return a.settleStartupPermissionMode(a.sendSetPermissionMode(ctx, contracts.ClaudeModeDefault, timeout))
	}

	if _, err := a.sendSetPermissionMode(ctx, contracts.ClaudeModeAuto, timeout); err != nil {
		if !isAutoModeUnavailableError(err) {
			slog.Warn("auto-mode probe failed (transient); treating as unavailable",
				"agent_id", a.AgentID(), "error", err)
		}
		a.setAutoModeAvailable(false)
	} else {
		a.setAutoModeAvailable(true)
	}
	return a.settleStartupPermissionMode(a.sendSetPermissionMode(ctx, requested, timeout))
}

// settleStartupPermissionMode records an acknowledged startup mode through
// settlePermissionMode and passes a failure through unchanged, so every exit of
// applyStartupPermissionMode settles the axis from one place.
//
// The auto PROBE runs before the requested mode, and a probe that times out records its
// own request id as the deferred ack (see sendSetPermissionMode). The mode that follows
// it supersedes that request, so the id must go with it. A failure needs no settlement:
// the caller tears the process down.
func (a *Agent) settleStartupPermissionMode(resp claudeCodeControlResult, err error) (claudeCodeControlResult, error) {
	if err == nil {
		a.settlePermissionMode(resp.Mode)
	}
	return resp, err
}

// sendSetPermissionMode issues set_permission_mode and falls back to the
// requested mode when the response omits the applied mode field, so callers
// always receive a non-empty resp.Mode on success.
// permissionModeApplyTimeout caps how long the live UpdateSettings path waits for a
// set_permission_mode ack. Kept short so a permission-mode toggle made while a turn is
// streaming (the CLI defers the ack until the turn ends) fails fast and is applied
// optimistically, rather than blocking for APITimeout and then restarting the agent.
const permissionModeApplyTimeout = 2 * time.Second

func (a *Agent) sendSetPermissionMode(ctx context.Context, mode string, timeout time.Duration) (claudeCodeControlResult, error) {
	body, err := json.Marshal(map[string]string{
		"subtype": "set_permission_mode",
		"mode":    mode,
	})
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	requestID, resp, err := a.sendControlAndWaitWithID(ctx, string(body), timeout)
	if errors.Is(err, errControlTimeout) {
		// The CLI deferred this ack until the active turn ends. Remember THIS request's id (as
		// the latest pending toggle) so claudeCodeHandleControlResponse folds back only the ack
		// that belongs to it -- not a stale/duplicate ack, nor an earlier toggle this one
		// supersedes. The turn is still streaming, so the deferred ack cannot arrive before this
		// returns, hence no race with the optimistic write the caller does next.
		a.Mu.Lock()
		a.deferredPermissionModeReqID = requestID
		a.Mu.Unlock()
	}
	if err == nil && resp.Mode == "" {
		resp.Mode = mode
	}
	return resp, err
}

// setAutoModeAvailable stores the auto-mode probe result under a.Mu so
// concurrent readers (e.g. AvailableOptionGroups) observe a consistent value.
func (a *Agent) setAutoModeAvailable(v bool) {
	a.Mu.Lock()
	a.autoModeAvailable = v
	a.Mu.Unlock()
}

// isAutoModeUnavailableError reports whether err is the Claude Code
// control_response rejection for set_permission_mode:auto (admin-disabled,
// blocked in plan mode, or not supported by the model).
func isAutoModeUnavailableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), autoModeUnavailableErrorPrefix)
}

func (a *Agent) registerPendingControl(requestID string, ch chan<- claudeCodeControlResult) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	a.pendingControl[requestID] = ch
}

func (a *Agent) unregisterPendingControl(requestID string) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	delete(a.pendingControl, requestID)
}

// handlePendingControlResponse checks if a parsed line is a control_response
// matching a pending request. If so, it sends the result to the waiting
// channel and returns true (the line should be consumed, not forwarded).
func (a *Agent) handlePendingControlResponse(line *providerkit.ParsedLine) bool {
	// Quick check using the pre-parsed Type field.
	if line.Type != claudeMsgTypeControlResponse {
		return false
	}

	var envelope struct {
		Response struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line.Raw, &envelope); err != nil {
		return false
	}

	reqID := envelope.Response.RequestID
	a.pendingControlMu.Lock()
	ch, ok := a.pendingControl[reqID]
	a.pendingControlMu.Unlock()

	if !ok {
		return false
	}

	// Parse known fields from the inner response object.
	var innerResponse struct {
		Mode                  string                `json:"mode"`
		OutputStyle           string                `json:"output_style"`
		AvailableOutputStyles []string              `json:"available_output_styles"`
		FastModeState         string                `json:"fast_mode_state"`
		Models                []claudeCodeModelInfo `json:"models"`
		UnavailableModels     []claudeCodeModelInfo `json:"unavailable_models"`
	}
	if len(envelope.Response.Response) > 0 {
		// Best-effort: a partial/unknown response shape still yields the fields it
		// does carry. But log a type-mismatch failure (e.g. a schema-drifted models
		// array) so dynamic model discovery silently falling back to the static
		// catalog is diagnosable rather than indistinguishable from an old CLI.
		if err := json.Unmarshal(envelope.Response.Response, &innerResponse); err != nil {
			slog.Warn("failed to parse control response inner fields",
				"agent_id", a.AgentID(), "request_id", reqID, "error", err)
		}
	}

	result := claudeCodeControlResult{
		Success:               envelope.Response.Subtype == "success",
		Mode:                  innerResponse.Mode,
		Error:                 envelope.Response.Error,
		OutputStyle:           innerResponse.OutputStyle,
		AvailableOutputStyles: innerResponse.AvailableOutputStyles,
		FastModeState:         innerResponse.FastModeState,
		Models:                innerResponse.Models,
		UnavailableModels:     innerResponse.UnavailableModels,
		RawResponse:           envelope.Response.Response,
	}
	ch <- result
	return true
}
