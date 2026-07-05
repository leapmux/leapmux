package service

import (
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func controlResponseRequestID(content []byte) string {
	if requestID, _, _, ok := agent.DecodeControlBehavior(content); ok && requestID != "" {
		return requestID
	}
	if _, requestID, ok := agent.ExtractJSONRPCID(content); ok {
		return requestID
	}
	return ""
}

// controlResponsePlanModePayload is the decoded shape of a plan-mode control response: the
// approve/reject envelope plus the optional permission-mode switch and context-clear the frontend
// attaches. It EMBEDS agent.ControlBehaviorEnvelope (the single Go home for the
// {response:{request_id, response:{behavior, message}}} shape) rather than re-declaring it, so the
// envelope fields (promoted as plan.decision.Response.*) can't drift from DecodeControlBehavior's
// reading of the same wire bytes. Shared by the two plan-mode handlers (the Codex prompt-response
// path and the common control-response path).
type controlResponsePlanModePayload struct {
	PermissionMode string `json:"permissionMode"`
	ClearContext   bool   `json:"clearContext"`
	agent.ControlBehaviorEnvelope
}

type controlResponseRequestMetadata struct {
	RequestID string
	ToolName  string
	ToolUseID string
	Payload   json.RawMessage
	Loaded    bool
}

type controlResponsePlan struct {
	requestMeta controlResponseRequestMetadata
	// resolution is the provider-owned interpretation of the response verbatim, held as ONE field
	// rather than copied out field-by-field, so a new ControlResponseResolution field can never be
	// silently dropped by a forgotten copy line -- the plan references the provider's resolution as
	// the single source of truth for content/display/self-display/plan-mode.
	resolution  agent.ControlResponseResolution
	decision    controlResponsePlanModePayload
	hasDecision bool
}

func (svc *Context) loadControlResponseRequestMetadata(agentID string, content []byte) controlResponseRequestMetadata {
	reqID := controlResponseRequestID(content)
	if reqID == "" {
		return controlResponseRequestMetadata{}
	}
	meta := controlResponseRequestMetadata{RequestID: reqID}
	cr, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{
		AgentID:   agentID,
		RequestID: reqID,
	})
	if err != nil {
		return meta
	}
	meta.Payload = json.RawMessage(cr.Payload)

	var crBody struct {
		Request struct {
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"request"`
	}
	if err := json.Unmarshal(cr.Payload, &crBody); err != nil {
		slog.Warn("control request payload unmarshal failed", "agent_id", agentID, "error", err)
		return meta
	}
	meta.ToolName = crBody.Request.ToolName
	meta.ToolUseID = crBody.Request.ToolUseID
	meta.Loaded = true
	return meta
}

func (svc *Context) buildControlResponsePlan(agentID string, dbAgent db.Agent, content []byte) controlResponsePlan {
	requestMeta := svc.loadControlResponseRequestMetadata(agentID, content)
	resolution := agent.ProviderFor(dbAgent.AgentProvider).ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       requestMeta.RequestID,
		RequestPayload:  requestMeta.Payload,
		ResponseContent: content,
		ToolName:        requestMeta.ToolName,
		ToolUseID:       requestMeta.ToolUseID,
	})
	if resolution.Content == nil {
		resolution.Content = content
	}

	plan := controlResponsePlan{
		requestMeta: requestMeta,
		resolution:  resolution,
	}
	// plan.decision is the frontend's approve/reject + permissionMode/clearContext envelope, decoded
	// from the ORIGINAL `content` -- the raw frontend control response, which ALWAYS carries that
	// envelope -- NOT from resolution.Content, which a provider may have rewritten for the agent
	// (e.g. Cursor createPlan's transformCursorControlResponse replaces it with an ACP outcome that
	// carries no request_id). Reading the frontend-only shape from the frontend bytes keeps plan-mode
	// handling and the fallback row correct even for a provider that BOTH rewrites Content for the
	// agent AND needs plan-mode handling; resolution.Content is still what gets forwarded to the
	// agent. (For every provider except Cursor createPlan resolution.Content == content, so this is
	// identical to decoding from resolution.Content; for Cursor createPlan it flips hasDecision to
	// true, which is behavior-neutral -- PlanModeControl==None makes the plan-mode effects a no-op
	// and the answer stays controlAnswerSynthetic, so no fallback row is added.)
	//
	// hasDecision gates the plan-mode / fallback-row handling, and is set ONLY for a recognized
	// allow/deny behavior. The frontend is the sole producer of control responses and provably
	// emits nothing else (buildAllowResponse/buildDenyResponse/decodeControlResponseBehavior in
	// utils/controlResponse.ts), so an empty/unknown behavior is unreachable in practice -- the
	// narrowing (a plan-prompt whose behavior is neither would otherwise forward its raw envelope
	// and skip the fallback row) rests on that frontend contract by design rather than a defensive
	// guard.
	if err := json.Unmarshal(content, &plan.decision); err == nil && plan.decision.Response.RequestID != "" {
		switch plan.behavior() {
		case agent.ControlBehaviorAllow, agent.ControlBehaviorDeny:
			plan.hasDecision = true
		}
	}
	return plan
}

// behavior is the approve/reject behavior the frontend attached to this control response, read
// through one accessor rather than walking plan.decision.Response.Response.Behavior at each site.
// Trimmed to match agent.DecodeControlBehavior's behavior read: buildControlResponsePlan gates
// hasDecision on this matching agent.ControlBehaviorAllow/Deny, so surrounding whitespace must not
// silently drop it to "no decision" here while the trimming decoder accepts it on the other paths.
func (plan controlResponsePlan) behavior() string {
	return strings.TrimSpace(plan.decision.Response.Response.Behavior)
}

// rejectionMessage is the user's typed rejection reason, or "" when they gave none -- an empty
// message OR the ControlRejectedByUserMessage sentinel (the auto-filled placeholder). Delegates to
// agent.NormalizeRejectionMessage so this side of the wire and DecodeControlBehavior apply one
// shared deny-feedback rule that can't drift.
func (plan controlResponsePlan) rejectionMessage() string {
	return agent.NormalizeRejectionMessage(plan.decision.Response.Response.Message)
}

// answerRow is which persisted row carries the single CONTROL_RESPONSE rail mark, derived from the
// immutable resolution rather than stored -- a method like its sibling derivations (behavior /
// rejectionMessage / exitPlanClearingContext) so the plan can never be constructed with an
// answerRow that disagrees with its own resolution.
func (plan controlResponsePlan) answerRow() controlAnswerRow {
	return classifyControlAnswerRow(plan.resolution.SelfDisplayed, plan.resolution.DisplayText)
}

func (svc *Context) deleteControlRequest(agentID string, provider leapmuxv1.AgentProvider, requestMeta controlResponseRequestMetadata, selfDisplayed bool) {
	if requestMeta.RequestID == "" {
		return
	}
	sink := svc.Output.NewSink(agentID, provider)
	if requestMeta.ToolUseID != "" && selfDisplayed {
		sink.SetSpanType(requestMeta.ToolUseID, requestMeta.ToolName)
	}
	sink.DeleteControlRequest(requestMeta.RequestID)
	sink.BroadcastControlCancel(requestMeta.RequestID)
}

func (svc *Context) persistControlResponseAnswerRows(agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	if plan.needsFallbackDisplayRow() {
		svc.persistControlResponseDisplayRow(agentID, provider, plan)
	}

	// The synthetic {content} answer row belongs ONLY to a provider-resolved answer
	// (controlAnswerSynthetic). A self-displayed answer already lives in the provider's own
	// transcript, and a fallback answer is carried by the {controlResponse} row above -- persisting
	// resolution.DisplayText for either would DUPLICATE the answer row. Today the only
	// self-displaying provider (Claude) returns no DisplayText, so persistSyntheticUserMessage
	// early-returns on empty and this is a no-op for it -- but gating on the classifier makes "exactly
	// one answer row" STRUCTURAL (matching classifyControlAnswerRow's no-double-MARK guarantee) rather
	// than resting on that emptiness, so a future provider that BOTH self-displays AND returns display
	// text can't double-render the answer. controlAnswerSynthetic always carries non-empty display
	// text (the classifier's own rule), so the mark is unconditionally CONTROL_RESPONSE here.
	if plan.answerRow() == controlAnswerSynthetic {
		svc.persistSyntheticUserMessage(agentID, provider, plan.resolution.DisplayText, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE)
	}
}

// exitPlanClearingContext reports the one compound condition the fallback-row rule and the
// plan-mode-effects skipSend both hinge on: an APPROVED ExitPlanMode that ALSO clears context.
// When it holds, initiatePlanExecution is about to stop the agent, so the self-displayed
// transcript tool_result never materializes -- which is why the fallback display row must
// carry the mark instead. Naming the triple once keeps needsFallbackDisplayRow and
// applyControlResponsePlanModeEffects from silently drifting on it.
func (plan controlResponsePlan) exitPlanClearingContext() bool {
	return plan.behavior() == agent.ControlBehaviorAllow &&
		plan.resolution.PlanModeControl == agent.PlanModeControlExit &&
		plan.decision.ClearContext
}

func (plan controlResponsePlan) needsFallbackDisplayRow() bool {
	if !plan.requestMeta.Loaded || !plan.hasDecision {
		return false
	}
	if plan.answerRow() == controlAnswerFallback {
		return true
	}
	// A self-displayed answer normally owns the mark on its own transcript row -- unless a
	// context-clearing plan exit is about to wipe that row, where the fallback row carries it.
	return plan.answerRow() == controlAnswerSelfDisplayed && plan.exitPlanClearingContext()
}

func (svc *Context) handleControlResponsePromptPlan(agentID string, dbAgent db.Agent, plan controlResponsePlan) {
	if !plan.requestMeta.Loaded || !plan.hasDecision {
		return
	}

	crPayload := plan.decision
	// The provider owns which options a plan approval settles (the values stay in the plugin);
	// the service just applies them. Base settles unconditionally; Bypass rides a mode switch.
	approvalOptions := agent.ProviderFor(dbAgent.AgentProvider).PlanApprovalOptions()
	switch plan.behavior() {
	case agent.ControlBehaviorAllow:
		svc.persistControlResponseDisplayRow(agentID, dbAgent.AgentProvider, plan)

		// Settle the provider's base plan-approval options (applied live, notify on first set).
		if len(approvalOptions.Base) > 0 {
			dbAgent = svc.applyOptionChanges(dbAgent, approvalOptions.Base, applyOptionsSpec{live: true, notifyFirstSet: true})
		}

		if crPayload.PermissionMode != "" {
			dbAgent = svc.setAgentPermissionModeWithAgent(dbAgent, crPayload.PermissionMode)
			// Grant the provider's bypass options for the approved mode (applied live, notify on
			// first set) -- e.g. Codex's full network access + no sandbox.
			if len(approvalOptions.Bypass) > 0 {
				svc.applyOptionChanges(dbAgent, approvalOptions.Bypass, applyOptionsSpec{live: true, notifyFirstSet: true})
			}
		}

		if crPayload.ClearContext {
			go svc.initiatePlanExecution(agentID, resolveTargetMode(crPayload.PermissionMode, agent.PermissionModeDefault))
		} else {
			// An auto-injected prompt, not the user's own words: no rail dot.
			svc.sendSyntheticUserMessage(agentID, "Implement the plan.", leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)
		}
	case agent.ControlBehaviorDeny:
		if msg := plan.rejectionMessage(); msg != "" {
			// The user's typed rejection reason IS their answer to the plan-mode control
			// request, so mark it CONTROL_RESPONSE for a rail dot -- consistent with every
			// other deny-with-feedback path (ExitPlanMode, permission decisions).
			svc.sendSyntheticUserMessage(agentID, msg, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE)
		} else {
			svc.persistControlResponseDisplayRow(agentID, dbAgent.AgentProvider, plan)
		}
	}
}

// controlAnswerRow identifies which persisted row carries the single CONTROL_RESPONSE rail
// mark for a user's answer to a control request. The three homes are mutually exclusive, so
// naming them here lets the two persist sites agree on exactly one -- never both (a double
// dot), never neither (a real answer with no dot).
type controlAnswerRow int

const (
	// controlAnswerSelfDisplayed: the provider re-emits the answer in its OWN transcript
	// (Claude's AskUserQuestion / ExitPlanMode tool_result), which the Claude user-envelope
	// classifier marks at ingestion -- so neither synthesized row is marked.
	controlAnswerSelfDisplayed controlAnswerRow = iota
	// controlAnswerSynthetic: a provider-resolved answer row (Codex/OpenCode/Pi/Cursor) that
	// SendControlResponse writes via persistSyntheticUserMessage from displayText -- that row
	// carries the mark.
	controlAnswerSynthetic
	// controlAnswerFallback: no provider display text and not self-displayed (a Claude
	// permission decision, or a non-Claude approval with no feedback) -- the synthetic
	// {controlResponse} fallback row in persistControlResponseAnswerRows carries the mark.
	controlAnswerFallback
)

// classifyControlAnswerRow is the SINGLE home for the "which row is the control answer"
// decision, derived from the provider-resolved self-display flag and display text. Both
// persist sites consult it -- the synthetic answer row and the fallback display row -- so
// exactly one row draws the rail's jump dot. Because self-display is checked FIRST, a
// provider that BOTH self-displays AND returns display text can never double-mark: its
// ingested transcript row owns the mark and the synthesized rows stay unmarked.
func classifyControlAnswerRow(selfDisplayed bool, displayText string) controlAnswerRow {
	if selfDisplayed {
		return controlAnswerSelfDisplayed
	}
	if strings.TrimSpace(displayText) != "" {
		return controlAnswerSynthetic
	}
	return controlAnswerFallback
}

// applyControlResponsePlanModeEffects detects plan mode changes from control responses.
// When the frontend approves/rejects an EnterPlanMode or ExitPlanMode control request,
// this updates the permission mode and initiates plan execution as needed. Returns true
// when the caller should skip sending the response to the agent (clearContext path).
func (svc *Context) applyControlResponsePlanModeEffects(agentID string, dbAgent db.Agent, plan controlResponsePlan) bool {
	if !plan.requestMeta.Loaded || !plan.hasDecision {
		return false
	}

	crPayload := plan.decision
	// Trim to match: plan.requestMeta.RequestID came through DecodeControlBehavior (which trims),
	// so read the response's own request id the same way -- otherwise a whitespace-padded id would
	// fail this equality and silently skip the plan-mode transition even though hasDecision (which
	// also trims) accepted it. Keeps the "trim everywhere" rule the rest of this file enforces.
	reqID := strings.TrimSpace(crPayload.Response.RequestID)
	if reqID == "" || reqID != plan.requestMeta.RequestID {
		return false
	}

	// Detect plan mode changes from control responses (agent-initiated).
	skipSend := false
	if plan.behavior() == agent.ControlBehaviorAllow {
		switch plan.resolution.PlanModeControl {
		case agent.PlanModeControlEnter:
			svc.setAgentPermissionModeWithAgent(dbAgent, agent.PermissionModePlan)
		case agent.PlanModeControlExit:
			// Determine target permission mode from control_response (default AcceptEdits here,
			// vs Default on the plan-prompt path -- resolveTargetMode owns that fallback).
			targetMode := resolveTargetMode(crPayload.PermissionMode, agent.PermissionModeAcceptEdits)
			svc.setAgentPermissionModeWithAgent(dbAgent, targetMode)

			// Remove the planModeToolUse entry so detectPlanModeFromToolResult
			// does not override the mode we just set.
			if plan.requestMeta.ToolUseID != "" {
				svc.Output.planModeToolUse.Delete(plan.requestMeta.ToolUseID)
			}

			// Inside the Behavior==Allow branch and the PlanModeControlExit case, this
			// IS plan.exitPlanClearingContext() -- the same predicate needsFallbackDisplayRow
			// keys the mark-carrying fallback row off. Call it (rather than re-inlining the
			// ClearContext read) so the two provably stay in lockstep, as its doc promises.
			if plan.exitPlanClearingContext() {
				// When clearing context, don't send the approval to the
				// agent — we're about to stop it anyway. This avoids
				// the race where the agent acts on the approval before
				// initiatePlanExecution kills it.
				go svc.initiatePlanExecution(agentID, targetMode)
				skipSend = true
			}
			// When !clearContext, the agent continues in current context.
		}
	}

	return skipSend
}

// resolveTargetMode picks the plan-execution target permission mode: the mode the frontend
// attached to the control response, or `defaultMode` when it attached none. The two plan-mode
// entry points differ only in that default (PermissionModeDefault on the plan-prompt path,
// PermissionModeAcceptEdits on the ExitPlanMode path).
func resolveTargetMode(permissionMode, defaultMode string) string {
	if permissionMode != "" {
		return permissionMode
	}
	return defaultMode
}

// persistControlResponseDisplayRow writes the synthetic {controlResponse} row from the plan's
// approve/reject behavior and the NORMALIZED comment the frontend attached, reading both off the
// plan so the deep decision chain lives here once rather than at each call site. The comment runs
// through plan.rejectionMessage() (not the raw decision message) so the auto-filled
// ControlRejectedByUserMessage sentinel collapses to "" -- otherwise a bare deny would render the
// placeholder "Rejected by user." as if it were typed feedback ("Sent feedback: ...") in both the
// transcript row (controlResponseRenderer) and the rail dot preview (defaultMarkPreview), instead
// of the intended "Rejected". An approved row ignores comment, so normalizing it is safe there too.
func (svc *Context) persistControlResponseDisplayRow(agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	action := "approved"
	if plan.behavior() == agent.ControlBehaviorDeny {
		action = "rejected"
	}
	displayContent := map[string]interface{}{
		"isSynthetic": true,
		"controlResponse": map[string]string{
			"action":  action,
			"comment": plan.rejectionMessage(),
		},
	}
	displayJSON, err := json.Marshal(displayContent)
	if err != nil {
		slog.Warn("marshal control response notification", "agent_id", agentID, "error", err)
		return
	}
	if err := svc.Output.persistAndBroadcast(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, displayJSON, agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE}, nil); err != nil {
		slog.Warn("failed to persist control response notification", "agent_id", agentID, "error", err)
	}
}
