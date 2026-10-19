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
	return a.sendControlAndWaitWithID(ctx, id.Short(), requestBody, timeout)
}

// sendControlAndWaitWithID registers the supplied identity before the request reaches stdin.
func (a *Agent) sendControlAndWaitWithID(ctx context.Context, requestID, requestBody string, timeout time.Duration) (claudeCodeControlResult, error) {
	ch, release, err := a.controlReplies.Register(requestID)
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	defer release()

	msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`, requestID, requestBody)
	if err := a.SendRawInput([]byte(msg), agent.StopContext{}); err != nil {
		release()
		// A closed stdin can precede the process exit report.
		// Wait briefly so the caller receives the exit status and captured stderr.
		timer := a.Clock().NewTimer(claudeControlWriteExitWait, claudeControlWriteExitTimerTag)
		defer timer.Stop(claudeControlWriteExitTimerTag)
		select {
		case <-a.ProcessDone():
			return claudeCodeControlResult{}, a.ProcessExitError()
		case <-timer.C:
			return claudeCodeControlResult{}, err
		}
	}
	agent.TraceStartupPhase(a.AgentID(), "control_stdin_write")

	raw, err := providerkit.AwaitReply(&a.Process, ctx, ch, "claude control", timeout)
	if errors.Is(err, providerkit.ErrAwaitTimeout) {
		return claudeCodeControlResult{}, errControlTimeout
	}
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	response := parseClaudeControlResult(raw)
	if !response.Success {
		return response, errors.New(response.Error)
	}
	return response, nil
}

const claudeControlWriteExitWait = time.Second
const claudeControlWriteExitTimerTag = "claude-control-write-exit"

// errControlTimeout identifies a control reply that did not arrive before its deadline.
// The permission-mode path uses errors.Is to distinguish a deferred acknowledgment from a refusal.
// Claude can defer set_permission_mode until the active turn ends.
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

// sendSetMaxThinkingTokens sends set_max_thinking_tokens, the control request
// that changes the thinking configuration of the RUNNING session. tokens is 0
// to disable thinking, or nil to return the session to the default that
// alwaysThinkingEnabled selects.
//
// The field is always present: nil marshals to an explicit null. Claude Code
// reads an ABSENT max_thinking_tokens as "keep the current configuration", so
// omitting it would turn the "on" request into a no-op.
func (a *Agent) sendSetMaxThinkingTokens(ctx context.Context, tokens *int, timeout time.Duration) error {
	body, err := json.Marshal(map[string]interface{}{
		"subtype":             "set_max_thinking_tokens",
		"max_thinking_tokens": tokens,
	})
	if err != nil {
		return err
	}
	_, err = a.sendControlAndWait(ctx, string(body), timeout)
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
			return resp, nil
		}
		// Registration.PermissionDefaults.NewSession requests auto for each new
		// session. Fall back after any error, including a timeout or a transport
		// error. Such an error does not show whether default mode works. The probe
		// path below also treats a transient failure as unavailable. If default
		// mode fails too, return its error.
		if isAutoModeUnavailableError(err) {
			slog.Warn("requested auto permission mode is unavailable; falling back to default",
				"agent_id", a.AgentID())
		} else {
			slog.Warn("auto permission mode failed (transient); falling back to default",
				"agent_id", a.AgentID(), "error", err)
		}
		a.setAutoModeAvailable(false)
		return a.sendSetPermissionMode(ctx, contracts.ClaudeModeDefault, timeout)
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
	return a.sendSetPermissionMode(ctx, requested, timeout)
}

// permissionModeApplyTimeout limits the wait while the native turn defers its mode reply.
const permissionModeApplyTimeout = 2 * time.Second

func (a *Agent) sendSetPermissionMode(ctx context.Context, mode string, timeout time.Duration) (claudeCodeControlResult, error) {
	a.permissionModeMu.Lock()
	defer a.permissionModeMu.Unlock()
	body, err := json.Marshal(map[string]string{
		"subtype": "set_permission_mode",
		"mode":    mode,
	})
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	change := &permissionModeChange{requestID: id.Short(), requested: mode}
	a.Mu.Lock()
	a.permissionModeChange = change
	a.Mu.Unlock()
	response, err := a.sendControlAndWaitWithID(ctx, change.requestID, string(body), timeout)
	a.Mu.Lock()
	if change.completed {
		response, err = change.response, change.err
	} else if errors.Is(err, errControlTimeout) {
		change.deferred = true
	} else {
		a.completePermissionModeLocked(change, response, err, false)
		response, err = change.response, change.err
	}
	a.Mu.Unlock()
	return response, err
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

// handlePendingControlResponse sends a matching raw reply to its caller.
// A late or unmatched reply returns false and reaches the normal output handler.
func (a *Agent) handlePendingControlResponse(line *providerkit.ParsedLine) bool {
	// Quick check using the pre-parsed Type field.
	if line.Type != claudeMsgTypeControlResponse {
		return false
	}
	var envelope struct {
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(line.Raw, &envelope) != nil || envelope.Response.RequestID == "" {
		return false
	}
	return a.controlReplies.Deliver(envelope.Response.RequestID, line.Raw)
}

// parseClaudeControlResult reads the native reply without changing its fields.
func parseClaudeControlResult(raw json.RawMessage) claudeCodeControlResult {
	var envelope struct {
		Response struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return claudeCodeControlResult{Error: fmt.Sprintf("invalid control response: %v", err)}
	}

	reqID := envelope.Response.RequestID

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
		// Keep the fields that decode before a type mismatch.
		// Log the mismatch so a catalog fallback does not hide a changed native schema.
		if err := json.Unmarshal(envelope.Response.Response, &innerResponse); err != nil {
			slog.Warn("failed to parse control response inner fields",
				"request_id", reqID, "error", err)
		}
	}

	return claudeCodeControlResult{
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
}
