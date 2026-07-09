package service

import (
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

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

func (svc *Context) loadControlResponseRequestMetadata(agentID string, plugin agent.Provider, content []byte) controlResponseRequestMetadata {
	reqID := plugin.ControlResponseRequestID(content)
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
	plugin := agent.ProviderFor(dbAgent.AgentProvider)
	requestMeta := svc.loadControlResponseRequestMetadata(agentID, plugin, content)
	resolution := plugin.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  requestMeta.Payload,
		ResponseContent: content,
		ToolName:        requestMeta.ToolName,
	})
	// Backfill on len==0, not just ==nil: a provider that returned an empty-but-non-nil Content would
	// otherwise reach persistControlResponseRow, where json.RawMessage of empty bytes makes
	// json.Marshal fail ("unexpected end of JSON input") and silently drop the user's answer row.
	// Falling back to the raw response bytes keeps the forwarded payload as the persisted response.
	if len(resolution.Content) == 0 {
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
	// handling correct even for a provider that BOTH rewrites Content for the agent AND needs
	// plan-mode handling; resolution.Content is still what gets forwarded to the agent. (For every
	// provider except Cursor createPlan resolution.Content == content, so this is identical to
	// decoding from resolution.Content; for Cursor createPlan it flips hasDecision to true, which is
	// behavior-neutral -- PlanModeControl==None makes the plan-mode effects a no-op, and the single
	// structured row is unaffected by hasDecision.)
	//
	// hasDecision gates the plan-mode handling, and is set ONLY for a recognized
	// allow/deny behavior. The frontend is the sole producer of control responses and provably
	// emits nothing else (buildAllowResponse/buildDenyResponse/decodeControlResponseBehavior in
	// utils/controlResponse.ts), so an empty/unknown behavior is unreachable in practice -- the
	// narrowing (a plan-prompt whose behavior is neither would otherwise forward its raw envelope
	// and skip the plan-mode branch) rests on that frontend contract by design rather than a
	// defensive guard.
	//
	// Normalize the decoded envelope ONCE here -- trim request_id and behavior, collapse the
	// ControlRejectedByUserMessage placeholder to "" -- exactly as agent.DecodeControlBehavior reads
	// the same wire bytes on the other paths. Every downstream reader (behavior(), rejectionMessage(),
	// the plan-mode request-id match) is then a plain field read, so the "trim everywhere to match"
	// hazard becomes mechanically impossible instead of comment-enforced at each site. In particular
	// the hasDecision gate and the applyControlResponsePlanModeMutations request-id match now read the
	// SAME normalized request_id, so a whitespace-padded id can't make one accept while the other skips.
	if err := json.Unmarshal(content, &plan.decision); err == nil {
		plan.decision.Response.RequestID = strings.TrimSpace(plan.decision.Response.RequestID)
		plan.decision.Response.Response.Behavior = strings.TrimSpace(plan.decision.Response.Response.Behavior)
		plan.decision.Response.Response.Message = agent.NormalizeRejectionMessage(plan.decision.Response.Response.Message)
		if plan.decision.Response.RequestID != "" {
			switch plan.decision.Response.Response.Behavior {
			case agent.ControlBehaviorAllow, agent.ControlBehaviorDeny:
				plan.hasDecision = true
			}
		}
	}
	return plan
}

// behavior is the approve/reject behavior the frontend attached to this control response, read
// through one accessor rather than walking plan.decision.Response.Response.Behavior at each site.
// Already trimmed at construction (buildControlResponsePlan), so this is a plain field read.
func (plan controlResponsePlan) behavior() string {
	return plan.decision.Response.Response.Behavior
}

// rejectionMessage is the user's typed rejection reason, or "" when they gave none -- an empty
// message OR the ControlRejectedByUserMessage sentinel (the auto-filled placeholder). Already
// normalized (via agent.NormalizeRejectionMessage) at construction, so this names the deeply-nested
// field for the one caller rather than re-applying the rule.
func (plan controlResponsePlan) rejectionMessage() string {
	return plan.decision.Response.Response.Message
}

// isPlanPrompt reports whether this answer is a plan-mode PROMPT decision (the Codex plan-mode
// prompt), which handleControlResponsePromptPlan handles entirely server-side and NEVER forwards to
// the agent. Derived from the immutable resolution + hasDecision -- a method like its sibling
// predicates (behavior / withholdsForward / exitPlanClearingContext) so the winner-work dispatch and
// the forward gate can never be constructed to disagree on it.
func (plan controlResponsePlan) isPlanPrompt() bool {
	return plan.resolution.PlanModeControl == agent.PlanModeControlPrompt && plan.hasDecision
}

// applyWinningControlResponse runs the once-only work the idempotency-claim WINNER performs for a
// control answer: delete the pending control request (broadcasting its cancel to every window and
// marking the self-displayed span), then either handle a plan-mode prompt entirely server-side or
// persist the structured answer row and apply the plan-mode side effects. SendControlResponse calls
// it for the claim winner only -- a duplicate must not re-delete, re-persist, or re-restart.
//
// Deleting the request here (for the winner alone) is also what keeps the winner's earlier read
// while-present: a loser that deleted could tear the request out from under a concurrent winner's
// not-yet-run read. Claude can emit a follow-up control_request right after it reads a denial; the
// winner's delete + cancel clears the stale one from every window. The row is persisted here BEFORE
// the caller forwards, so the user's answer precedes any async plan-execution rows.
func (svc *Context) applyWinningControlResponse(agentID string, dbAgent db.Agent, plan controlResponsePlan) {
	svc.deleteControlRequest(agentID, dbAgent.AgentProvider, plan.requestMeta, plan.resolution.SelfDisplayed)
	if plan.isPlanPrompt() {
		svc.handleControlResponsePromptPlan(agentID, dbAgent, plan)
	} else {
		svc.persistControlResponseAnswerRow(agentID, dbAgent.AgentProvider, plan)
		svc.applyControlResponsePlanModeMutations(agentID, dbAgent, plan)
	}
}

// processControlResponse runs the control-response orchestration for one SendControlResponse call:
// build the plan, claim the answer for idempotency, run the once-only WINNER work, and report the
// bytes (if any) the RPC handler should forward to the agent. It is dispatcher-free -- the transport
// (unmarshal, access check, sender replies) stays in the handler -- so the winner/duplicate/forward
// decision is unit-testable without a channel sender. Returns forward=false (nil bytes) for a
// duplicate (deduped no-op), a plan-mode prompt (handled entirely server-side), or a context-clearing
// plan approval (withheld so it can't race the restart it kicked off).
func (svc *Context) processControlResponse(agentID string, dbAgent db.Agent, content []byte, claimToken string) (forwardBytes []byte, forward bool) {
	// Build the plan (which reads the pending control request and decodes the request id ONCE),
	// then CLAIM the answer for idempotency. The claim is the concurrency serialization point:
	// handlers run concurrently (DispatchAsync, no per-agent lock), and an atomic INSERT on
	// (agent_id, request_id, claim_token) picks exactly ONE winner. A duplicate -- an RPC retry, or a
	// second window answering the same request instance before it received the cancel broadcast, both
	// echoing the SAME claim_token -- loses. The claim is a DURABLE row (see claimControlResponseAnswer),
	// so a duplicate straddling even a worker-PROCESS restart is deduped, and a REUSED request_id is
	// deduped per instance by its distinct claim_token. An unattributable answer (no request id) is
	// nobody's duplicate, so it needs no claim.
	//
	// Claiming AFTER buildControlResponsePlan is safe and lets the request id be decoded once: only
	// the winner deletes the request (in applyWinningControlResponse below), and only AFTER its own
	// claim, which is after its own read -- so the winner always reads the FULL render context (and
	// the true SelfDisplayed flag) while the request is present, and a loser (which does nothing) may
	// read it gone. The claim must precede the persist/delete, though: claiming only after a
	// successful persist would let two answers both persist before either claims -- the double-row
	// this prevents.
	plan := svc.buildControlResponsePlan(agentID, dbAgent, content)

	firstAnswer := true
	if plan.requestMeta.RequestID != "" {
		firstAnswer = svc.Output.claimControlResponseAnswer(agentID, plan.requestMeta.RequestID, claimToken)
	}

	// A duplicate does NOTHING: the winner already deleted the request, persisted the answer row,
	// applied the plan-mode side effects, AND forwarded its own response. A duplicate must not
	// re-do any of it -- and in particular must NOT re-forward. Because a duplicate read the request
	// gone, its resolution.Content diverges from the winner's: a Codex plan-mode prompt approval
	// would forward an envelope the winner (via applyWinningControlResponse) handles server-side and
	// never sends, and a Cursor create_plan answer whose ACP transform needs the now-deleted request
	// would forward the untransformed envelope. Re-forwarding either injects a stray/malformed frame
	// onto the agent's stdin, so only the winner forwards.
	if !firstAnswer {
		return nil, false
	}

	// The once-only winner work: delete the request, then persist the answer row and apply the
	// plan-mode side effects (or handle a plan-mode prompt entirely server-side). It runs BEFORE the
	// forward so the user's answer row precedes any async plan-execution rows.
	svc.applyWinningControlResponse(agentID, dbAgent, plan)

	// Forward the winner's response to the agent unless it withholds its forward. A plan-prompt is
	// handled server-side and never forwarded (plan.isPlanPrompt()); a context-clearing plan
	// approval must not race the restart applyWinningControlResponse just kicked off
	// (withholdsForward keys off the response's clearContext).
	if plan.isPlanPrompt() || plan.withholdsForward() {
		return nil, false
	}
	return plan.resolution.Content, true
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

// persistControlResponseAnswerRow persists the single synthetic control-response row that carries
// the user's answer -- and the rail's CONTROL_RESPONSE mark -- for every provider whose answer is
// not already shown in its own transcript. needsStructuredRow decides whether THIS answer needs the
// row, so a self-displayed answer (Claude's echoed tool_result) draws its dot on the ingested
// provider row instead of here. Duplicate answers for the same request are gated out one layer up
// (SendControlResponse's idempotency claim) before reaching here, so the row is never double-marked.
func (svc *Context) persistControlResponseAnswerRow(agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	if plan.needsStructuredRow() {
		svc.persistControlResponseRow(agentID, provider, plan)
	}
}

// approvedClearContext reports an APPROVED decision that clears context: behavior()==Allow with the
// frontend's clearContext flag set. clearContext is attached ONLY by the two restart-causing plan
// approvals (ExitPlanMode, the Codex plan-mode prompt), so this is the shared core of the two
// predicates below -- withholdsForward (the request-independent forward gate) narrows it with
// hasDecision, exitPlanClearingContext (the loaded-answer mark rule) narrows it with
// PlanModeControl==Exit. Named once so the two can't drift on what "approved + clears context" means.
func (plan controlResponsePlan) approvedClearContext() bool {
	return plan.behavior() == agent.ControlBehaviorAllow && plan.decision.ClearContext
}

// exitPlanClearingContext reports the one compound condition the structured-row rule hinges on: an
// APPROVED ExitPlanMode that ALSO clears context. When it holds, initiatePlanExecution is about to
// stop the agent, so the self-displayed transcript tool_result never materializes -- which is why the
// synthetic structured row must carry the mark instead. It is the LOADED-answer equivalent of
// withholdsForward (which is request-independent); naming the triple once keeps needsStructuredRow and
// applyControlResponsePlanModeMutations from silently drifting on it.
func (plan controlResponsePlan) exitPlanClearingContext() bool {
	return plan.approvedClearContext() && plan.resolution.PlanModeControl == agent.PlanModeControlExit
}

// needsStructuredRow reports whether SendControlResponse should synthesize the structured
// control-response row for this answer. A resolvable request id is required (an unattributable
// response draws no row, as before).
//
// A genuinely self-displayed answer (Claude's echoed tool_result carries the mark) gets NO structured
// row, EXCEPT when a context-clearing plan exit is about to wipe that echoed row, where the
// structured row carries the mark instead.
//
// Otherwise the answer gets the row -- even when the pending request was already deleted or the
// decision was unrecognized, so the user's answer is never lost (#258). The "answered twice" concern
// (a request-gone duplicate whose first answer's Claude tool_result already carries the mark) is
// handled ONE layer up by SendControlResponse's per-request idempotency claim, which lets only the
// first answer reach this row -- so needsStructuredRow no longer needs a provider-capability guard.
func (plan controlResponsePlan) needsStructuredRow() bool {
	if plan.requestMeta.RequestID == "" {
		return false
	}
	if plan.resolution.SelfDisplayed {
		return plan.exitPlanClearingContext()
	}
	return true
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
		svc.persistControlResponseRow(agentID, dbAgent.AgentProvider, plan)

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
			svc.persistControlResponseRow(agentID, dbAgent.AgentProvider, plan)
		}
	}
}

// withholdsForward reports whether this answer's response must NOT be forwarded to the agent: an
// APPROVED context-clearing plan approval (ExitPlanMode, or the Codex plan-mode prompt), which is
// about to restart the agent via initiatePlanExecution -- forwarding it would race the restart. Only
// the claim WINNER ever reaches this predicate: a duplicate returns at SendControlResponse's
// !firstAnswer gate before the forward decision. It is read from the RESPONSE envelope
// (content-derived, request-independent), so a request-gone winner -- a genuine orphan whose pending
// request was already cleared (a teardown, or never stored), which therefore can't resolve
// PlanModeControlExit -- still withholds rather than forwarding a stale restart-approval onto a
// subprocess that is already being torn down. clearContext is set ONLY by the two restart-causing plan
// approvals, never an ordinary permission or EnterPlanMode, so it is a reliable proxy for "this answer
// restarts the agent."
//
// On a LOADED ExitPlanMode answer this equals exitPlanClearingContext() -- the predicate
// needsStructuredRow keys its mark-carrying row off -- because such an answer resolves
// PlanModeControl==Exit; the two are named separately only so the mark rule can require
// PlanModeControl==Exit while the forward rule stays request-independent for the request-gone case.
// The one loaded answer where they DIVERGE is the Codex plan-mode PROMPT: it also carries
// clearContext+Allow (so withholdsForward is true) but resolves PlanModeControl==Prompt, not Exit (so
// exitPlanClearingContext is false). That divergence is harmless: the handler catches a plan prompt on
// its isPlanPrompt gate and runs it server-side (handleControlResponsePromptPlan) BEFORE the forward,
// so withholdsForward is never consulted for it.
func (plan controlResponsePlan) withholdsForward() bool {
	return plan.hasDecision && plan.approvedClearContext()
}

// applyControlResponsePlanModeMutations applies the once-only plan-mode side effects of an
// EnterPlanMode / ExitPlanMode approval: the permission-mode switch, the planModeToolUse cleanup, and
// (for a context-clearing exit) kicking off initiatePlanExecution. The caller runs it for the WINNER
// only -- a duplicate must not re-run the switch or re-restart the agent -- so unlike withholdsForward
// (a pure content-derived predicate, correct for any answer but reached only by the winner), these
// mutations are side effects the caller gates externally to the winner, not internally.
// A request-gone or decision-less answer touches nothing (the mutations need the loaded request).
func (svc *Context) applyControlResponsePlanModeMutations(agentID string, dbAgent db.Agent, plan controlResponsePlan) {
	if !plan.requestMeta.Loaded || !plan.hasDecision || plan.behavior() != agent.ControlBehaviorAllow {
		return
	}

	crPayload := plan.decision
	// crPayload.Response.RequestID is already trimmed at construction (buildControlResponsePlan),
	// matching plan.requestMeta.RequestID (which came through DecodeControlBehavior's trimming read),
	// so this equality can never be a trimmed-vs-untrimmed mismatch -- and it agrees with the
	// hasDecision gate, which reads the same normalized id.
	reqID := crPayload.Response.RequestID
	if reqID == "" || reqID != plan.requestMeta.RequestID {
		return
	}

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

		// When clearing context, kick off the restart -- once. The forward itself is withheld for
		// every answer by withholdsForward (== exitPlanClearingContext on this loaded path), so we only
		// start the restart here; we don't re-decide the withhold.
		if plan.exitPlanClearingContext() {
			go svc.initiatePlanExecution(agentID, targetMode)
		}
	}
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

// persistedControlResponse is the structured control-response payload persisted inside the
// synthetic transcript row (issue #258). It keeps the provider-native response as sent to the
// agent plus the minimal provider-pruned request context, so the frontend can render human-facing
// labels AFTER the pending control request is deleted -- the backend no longer duplicates the
// frontend's label text.
type persistedControlResponse struct {
	// Provider is the AgentProvider enum name minus its AGENT_PROVIDER_ prefix (CODEX, OPENCODE,
	// CLAUDE_CODE, ...), matching how protobuf-es names the TS enum members. It durably records
	// which provider produced this answer; the frontend resolves the rendering plugin from the
	// message's own agentProvider, NOT this field, so it is an audit/debug token rather than a
	// dispatch key.
	Provider string `json:"provider"`
	// RequestID is the id of the control request this answers, omitted only when unknown.
	RequestID string `json:"requestId,omitempty"`
	// Request is the provider-pruned minimal render context (agent.ControlResponseResolution's
	// RequestContext), omitted when the stored request was unavailable or unrecognized.
	Request json.RawMessage `json:"request,omitempty"`
	// Response is the provider-native response payload forwarded to the agent (resolution.Content,
	// e.g. Cursor's transformed outcome), the source of truth the frontend renders the answer from.
	Response json.RawMessage `json:"response"`
}

// syntheticControlResponseRow is the transcript-row content envelope wrapping a
// persistedControlResponse. isSynthetic (NOT the message source) is what makes the frontend
// classify it as a control_response: the row is USER-sourced (a control answer is the user's own
// response), so without this flag it would render as a plain user-send content bubble.
type syntheticControlResponseRow struct {
	IsSynthetic     bool                     `json:"isSynthetic"`
	ControlResponse persistedControlResponse `json:"controlResponse"`
}

// controlResponseProviderName renders an AgentProvider as the bare token the persisted control
// response records -- the proto enum name with its AGENT_PROVIDER_ prefix stripped, matching how
// protobuf-es names the TS enum members (AGENT_PROVIDER_CODEX -> "CODEX"). It is recorded for
// audit/debug; the frontend dispatches on the message's agentProvider, not this token.
func controlResponseProviderName(p leapmuxv1.AgentProvider) string {
	return strings.TrimPrefix(p.String(), "AGENT_PROVIDER_")
}

// persistControlResponseRow writes the durable synthetic control-response transcript row: the
// provider-native response payload forwarded to the agent, plus the provider-pruned request
// context the frontend renders human-facing labels from (issue #258). It is sourced from USER --
// a control answer is the user's own response to the agent, so like a typed message it counts
// toward HasUserMessages/resolveResumeSessionID (answering alone can make a session resumable) and
// its bubble exposes data-role="user". This matches how every non-Claude provider-resolved answer
// was persisted on the pre-#258 path (a USER {content} row); the isSynthetic flag -- not the source
// -- is what routes it to the control_response renderer. It carries the CONTROL_RESPONSE rail mark.
// The frontend owns all label text; the backend no longer duplicates it.
func (svc *Context) persistControlResponseRow(agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	// Coalesce empty-OR-invalid bytes to nil so the Response field marshals as `null` rather than
	// making the WHOLE row marshal fail (which would silently drop the user's answer row): a
	// json.RawMessage marshals only when it holds valid JSON -- empty-but-non-nil bytes error with
	// "unexpected end of JSON input", and non-empty non-JSON bytes error with "invalid character
	// ...". buildControlResponsePlan already backfills empty Content with the raw response, so this is
	// the marshal-boundary backstop that keeps "a resolvable answer always persists a row" true
	// regardless of the resolution's Content -- empty or malformed. json.Valid also treats nil/empty
	// as invalid, so this one check subsumes the empty case.
	response := json.RawMessage(plan.resolution.Content)
	if !json.Valid(response) {
		response = nil
	}
	row := syntheticControlResponseRow{
		IsSynthetic: true,
		ControlResponse: persistedControlResponse{
			Provider:  controlResponseProviderName(provider),
			RequestID: plan.requestMeta.RequestID,
			Request:   plan.resolution.RequestContext,
			Response:  response,
		},
	}
	rowJSON, err := json.Marshal(row)
	if err != nil {
		slog.Warn("marshal control response row", "agent_id", agentID, "error", err)
		return
	}
	if err := svc.Output.persistAndBroadcast(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rowJSON, agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE}, nil); err != nil {
		slog.Warn("failed to persist control response row", "agent_id", agentID, "error", err)
	}
}
