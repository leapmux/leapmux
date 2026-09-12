package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type controlResponseRequestMetadata struct {
	AgentSessionID string
	RequestID      string
	ToolName       string
	ToolUseID      string
	Payload        json.RawMessage
	Loaded         bool
	Exists         bool
	ClaimToken     string
	SourceSeq      int64
}

type controlResponsePlan struct {
	requestMeta controlResponseRequestMetadata
	// Keep the complete provider resolution as one value.
	resolution  agent.ControlResponseResolution
	decision    agent.ControlBehaviorEnvelope
	settings    *leapmuxv1.PlanApprovalSettings
	hasDecision bool
}

func (svc *Service) loadControlResponseRequestMetadata(agentID, requestID string) (controlResponseRequestMetadata, error) {
	meta := controlResponseRequestMetadata{RequestID: requestID}
	if requestID == "" {
		return meta, nil
	}
	request, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return meta, nil
	}
	if err != nil {
		return meta, err
	}
	meta.Payload = request.Payload
	meta.ClaimToken = request.ClaimToken
	meta.AgentSessionID = request.AgentSessionID
	meta.SourceSeq = request.SourceSeq
	meta.Exists = true
	return completeControlRequestMetadata(meta), nil
}

func completeControlRequestMetadata(meta controlResponseRequestMetadata) controlResponseRequestMetadata {
	var body struct {
		Request struct {
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"request"`
	}
	if json.Unmarshal(meta.Payload, &body) == nil {
		meta.ToolName = body.Request.ToolName
		meta.ToolUseID = body.Request.ToolUseID
		meta.Loaded = true
	}
	return meta
}

func resolveControlResponsePlan(plugin agent.Provider, requestMeta controlResponseRequestMetadata, content []byte, settings *leapmuxv1.PlanApprovalSettings) controlResponsePlan {
	resolution := plugin.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       requestMeta.RequestID,
		RequestPayload:  requestMeta.Payload,
		ResponseContent: content,
		ToolName:        requestMeta.ToolName,
		PlanApproval:    settings,
	})
	// Preserve the caller's response when the provider returns no replacement bytes.
	// Withhold controls delivery separately, so an empty replacement cannot suppress forwarding.
	if len(resolution.Content) == 0 {
		resolution.Content = content
	}

	plan := controlResponsePlan{
		requestMeta: requestMeta,
		resolution:  resolution,
		settings:    settings,
	}
	// Normalize the decision once. Only an allow or deny decision enables plan operations.
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

// behavior returns the normalized decision.
func (plan controlResponsePlan) behavior() string {
	return plan.decision.Response.Response.Behavior
}

// rejectionMessage returns the normalized feedback, without the default rejection placeholder.
func (plan controlResponsePlan) rejectionMessage() string {
	return plan.decision.Response.Response.Message
}

// isPlanPrompt identifies a plan approval that LeapMux handles without forwarding a native response.
func (plan controlResponsePlan) isPlanPrompt() bool {
	return plan.resolution.PlanModeControl == agent.PlanModeControlPrompt
}

// processControlResponse retains the chosen answer before delivery and finalizes it after receipt.
// Uncertain delivery keeps its reservation and cannot resend automatically.
func (svc *Service) processControlResponse(dbAgent db.Agent, request *leapmuxv1.SendControlResponseRequest) error {
	agentID, content, claimToken := dbAgent.ID, request.GetContent(), request.GetClaimToken()
	plugin := agent.ProviderFor(dbAgent.AgentProvider)
	requestID := plugin.ControlResponseRequestID(content)
	readAnswer := func() (db.ControlResponseAnswer, error) {
		return svc.Queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
			AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
		})
	}
	if requestID != "" {
		answer, err := readAnswer()
		if err == nil {
			return svc.resumeControlResponseFinalization(answer)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read control response delivery: %w", err)
		}
	}
	meta, err := svc.loadControlResponseRequestMetadata(agentID, requestID)
	if err != nil {
		return fmt.Errorf("read the control request: %w", err)
	}
	plan := resolveControlResponsePlan(plugin, meta, content, request.GetPlanApproval())
	if !plan.requestMeta.Exists {
		return errors.New("the control request is no longer pending")
	}
	if plan.requestMeta.Exists && plan.requestMeta.ClaimToken != claimToken {
		return nil
	}
	if plan.isPlanPrompt() && (!plan.requestMeta.Loaded || !plan.hasDecision) {
		return errors.New("the plan approval has no valid decision")
	}
	if plan.requestMeta.Exists && plan.resolution.Withhold {
		return errors.New("the agent provider could not read this control response")
	}
	if plan.settings != nil && (!plan.hasDecision || plan.behavior() != agent.ControlBehaviorAllow ||
		!plan.isPlanPrompt() && plan.resolution.PlanModeControl != agent.PlanModeControlExit) {
		return errors.New("plan settings require an approval for a matching plan request")
	}
	var settings []byte
	if plan.settings != nil {
		settings, err = protojson.MarshalOptions{EmitDefaultValues: true}.Marshal(plan.settings)
		if err != nil {
			return fmt.Errorf("encode plan approval settings: %w", err)
		}
	}
	firstAnswer := true
	if requestID != "" {
		firstAnswer, err = svc.Output.claimControlResponseAnswer(db.ClaimControlResponseAnswerParams{
			AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
			RequestPayload: plan.requestMeta.Payload, ResponseContent: content,
			ResolvedContent: plan.resolution.Content, Feedback: plan.resolution.Feedback,
			PlanApprovalSettings: settings,
			SourceSeq:            plan.requestMeta.SourceSeq,
			AgentSessionID:       plan.requestMeta.AgentSessionID, AgentProvider: int64(dbAgent.AgentProvider),
			InputID: controlResponseQueueID(agentID, plan),
		})
		if err != nil {
			return fmt.Errorf("reserve the control response: %w", err)
		}
	}
	if !firstAnswer {
		answer, err := readAnswer()
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("the control request is no longer pending")
		}
		if err != nil {
			return fmt.Errorf("read control response delivery: %w", err)
		}
		return svc.resumeControlResponseFinalization(answer)
	}

	svc.broadcastControlResponseState(dbAgent, requestID, claimToken)
	if deliveryErr := svc.executeControlResponse(agentID, dbAgent, plan); deliveryErr != nil {
		var recordErr error
		if requestID != "" {
			if errors.Is(deliveryErr, agent.ErrDeliveryUncertain) {
				_, recordErr = svc.Queries.SetControlResponseDeliveryState(bgCtx(), db.SetControlResponseDeliveryStateParams{
					AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
					State: storedStateUncertain, RequiredState: storedStatePending,
				})
			} else {
				recordErr = svc.Queries.ReleaseUnsentControlResponseAnswer(bgCtx(), db.ReleaseUnsentControlResponseAnswerParams{
					AgentID: agentID, RequestID: requestID, ClaimToken: claimToken, State: storedStatePending,
				})
			}
		}
		return errors.Join(deliveryErr, recordErr)
	}
	if requestID == "" {
		return nil
	}
	_, err = svc.Queries.SetControlResponseDeliveryState(bgCtx(), db.SetControlResponseDeliveryStateParams{
		AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
		State: storedStateDelivered, RequiredState: storedStatePending,
	})
	if err != nil {
		return fmt.Errorf("%w: could not record the delivery receipt: %w", agent.ErrDeliveryUncertain, err)
	}
	answer, err := readAnswer()
	if err != nil {
		return err
	}
	return svc.finalizeControlResponse(answer)
}

// approvedClearContext identifies an approval that requests a fresh context.
func (plan controlResponsePlan) approvedClearContext() bool {
	return plan.behavior() == agent.ControlBehaviorAllow && plan.settings.GetClearContext()
}

// exitPlanClearingContext identifies a plan exit that replaces the provider session.
// LeapMux must retain its own answer row because the replaced process cannot emit an answer echo.
func (plan controlResponsePlan) exitPlanClearingContext() bool {
	return plan.approvedClearContext() && plan.resolution.PlanModeControl == agent.PlanModeControlExit
}

// needsTranscriptRow omits duplicate answer displays.
// Plan feedback already uses a queued user message. A native answer echo also replaces the LeapMux row.
// A fresh-context plan exit still needs the row because the old process cannot emit that echo.
func (plan controlResponsePlan) needsTranscriptRow() bool {
	if plan.isPlanPrompt() && plan.behavior() == agent.ControlBehaviorDeny && plan.rejectionMessage() != "" {
		return false
	}
	if plan.requestMeta.RequestID == "" {
		return false
	}
	if plan.resolution.SelfDisplayed {
		return plan.exitPlanClearingContext()
	}
	return true
}

// applyControlResponsePlanModeMutations records a plan-mode decision for its validated provider session.
// The caller holds the lifecycle lock. Native approval applies its own mode; a context clear prepares the next session's mode.
func (svc *Service) applyControlResponsePlanModeMutations(dbAgent db.Agent, plan controlResponsePlan) error {
	if !plan.requestMeta.Loaded || !plan.hasDecision || plan.behavior() != agent.ControlBehaviorAllow {
		return nil
	}

	crPayload := plan.decision
	// Match the normalized decision to the stored request.
	reqID := crPayload.Response.RequestID
	if reqID == "" || reqID != plan.requestMeta.RequestID {
		return nil
	}
	persistMode := func(mode string) error {
		values := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
		previous := OptionMap{agent.OptionIDPermissionMode: values[agent.OptionIDPermissionMode]}
		values[agent.OptionIDPermissionMode] = mode
		_, err := svc.persistOptionChanges(dbAgent, previous, values, false)
		return err
	}

	// Each provider supplies its own permission-mode values.
	plugin := agent.ProviderFor(dbAgent.AgentProvider)
	switch plan.resolution.PlanModeControl {
	case agent.PlanModeControlEnter:
		if enterMode := plugin.PlanModePermissionMode(agent.PlanModeControlEnter); enterMode != "" {
			return persistMode(enterMode)
		}
	case agent.PlanModeControlExit:
		// The mode the frontend attached wins; otherwise the provider's own exit mode.
		targetMode := resolveTargetMode(plan.settings.GetPermissionMode(), plugin.PlanModePermissionMode(agent.PlanModeControlExit))
		if targetMode != "" {
			if err := persistMode(targetMode); err != nil {
				return err
			}
		}

		// Remove the planModeToolUse entry so detectPlanModeFromToolResult
		// does not override the mode we just set.
		if plan.requestMeta.ToolUseID != "" {
			svc.Output.planModeToolUse.Delete(plan.requestMeta.ToolUseID)
		}

	case agent.PlanModeControlNone, agent.PlanModeControlPrompt:
		// These decisions do not change permission mode through this path.
	}
	return nil
}

// resolveTargetMode uses the requested mode, or the provider default when no mode was supplied.
func resolveTargetMode(permissionMode, defaultMode string) string {
	if permissionMode != "" {
		return permissionMode
	}
	return defaultMode
}

// controlResponseMessageContent preserves the sent bytes and the complete matching request separately.
// Worker request identity belongs to metadata, outside both provider payloads.
func controlResponseMessageContent(plan controlResponsePlan) (agent.MessageContent, error) {
	fields := map[string]any{
		contracts.MessageMetadataFieldControlRequestID:         plan.requestMeta.RequestID,
		contracts.MessageMetadataFieldControlRequestClaimToken: plan.requestMeta.ClaimToken,
	}
	if plan.settings != nil {
		settings, err := protojson.MarshalOptions{EmitDefaultValues: true}.Marshal(plan.settings)
		if err != nil {
			return agent.MessageContent{}, fmt.Errorf("encode plan approval metadata: %w", err)
		}
		fields[contracts.MessageMetadataFieldPlanApprovalSettings] = json.RawMessage(settings)
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		return agent.MessageContent{}, err
	}
	return agent.MessageContent{
		Original:       plan.resolution.Content,
		AgentSessionID: plan.requestMeta.AgentSessionID,
		Supplemental:   plan.requestMeta.Payload,
		Metadata:       metadata,
	}, nil
}
