package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
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
	NonBlocking    bool
}

type controlResponsePlan struct {
	requestMeta controlResponseRequestMetadata
	// Keep the complete provider resolution as one value.
	resolution agent.ControlResponseResolution
	decision   agent.ControlBehaviorEnvelope
	settings   *leapmuxv1.PlanApprovalSettings
	// settingsJSON is the ONE encoding of settings, and every consumer writes
	// these bytes. The claim row stores them and the transcript row repeats
	// them, so one approval has one stored form: a later change to the marshal
	// options cannot reshape one row and leave the other one alone.
	//
	// It is nil while settings is nil. With settings set, the plan carries it
	// only after encodePlanApprovalSettings runs, or after the answer row
	// supplied the stored bytes -- and controlResponseMessageContent refuses a
	// plan that reached it in neither state.
	settingsJSON []byte
	hasDecision  bool
	// controlDecision is the neutral decision that the answer delivered
	// (deliveredControlDecision), as the answer row stores it, or nil for an
	// answer that the Worker recorded without one. decision and hasDecision
	// above read the provider's own allow-or-deny envelope instead, which a
	// provider's answer can lack.
	controlDecision *leapmuxv1.ControlDecision
}

// encodePlanApprovalSettings fills settingsJSON from settings. Call it once,
// before the first consumer reads the bytes. A plan rebuilt from a stored answer
// row does not call it: that row already holds the encoding.
func (plan *controlResponsePlan) encodePlanApprovalSettings() error {
	if plan.settings == nil {
		return nil
	}
	encoded, err := protojson.MarshalOptions{EmitDefaultValues: true}.Marshal(plan.settings)
	if err != nil {
		return fmt.Errorf("encode plan approval settings: %w", err)
	}
	plan.settingsJSON = encoded
	return nil
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
	return storedControlRequestMetadata(request), nil
}

// storedControlRequestRefusal refuses a stored request whose payload gives no
// key, or another key than the one that stores it (agent.StoredControlRequestAgrees).
// The Worker wrote that key from the payload, so the row is corrupt, or the
// provider derived the key wrongly, and no answer to it is safe to send. The
// refusal leaves the request pending: the agent still waits, so the user's way
// out is a stop.
//
// It is the one check of that agreement, and no provider repeats it. The
// Worker runs it on each read of a request row that leads to an answer: in
// answerControl before a provider reads the prompt or builds the answer, and
// in processControlAnswer before a provider resolves the answer that the
// Worker claims. It never runs on a row that the Worker only shows, or on the
// stored payload of an answer that the Worker already delivered.
func storedControlRequestRefusal(plugin agent.Provider, agentID string, meta controlResponseRequestMetadata) error {
	if agent.StoredControlRequestAgrees(plugin, meta.RequestID, meta.Payload, meta.AgentSessionID) {
		return nil
	}
	slog.Warn("the stored control request disagrees with its key", "agent_id", agentID, "request_id", meta.RequestID)
	return errors.New(agent.RefusalUnreadableRequest)
}

// storedControlRequestMetadata is the metadata of one stored request row.
func storedControlRequestMetadata(request db.ControlRequest) controlResponseRequestMetadata {
	return completeControlRequestMetadata(controlResponseRequestMetadata{
		RequestID:      request.RequestID,
		Payload:        request.Payload,
		ClaimToken:     request.ClaimToken,
		AgentSessionID: request.AgentSessionID,
		SourceSeq:      request.SourceSeq,
		NonBlocking:    request.NonBlocking,
		Exists:         true,
	})
}

// completeControlRequestMetadata reads the tool of the stored request with
// the readers of package agent, which the provider tests read through too.
// Each field reads alone, so a sibling of the wrong type hides nothing. Only
// a payload that is no JSON object, such as the empty payload of a gone
// request, leaves the request unread.
func completeControlRequestMetadata(meta controlResponseRequestMetadata) controlResponseRequestMetadata {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(meta.Payload, &envelope) != nil {
		return meta
	}
	meta.ToolName = agent.ControlToolName(meta.Payload)
	meta.ToolUseID = agent.ControlToolUseID(meta.Payload)
	meta.Loaded = true
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
			case contracts.ControlAnswerBehaviorAllow, contracts.ControlAnswerBehaviorDeny:
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

// workerAnswer is one answer that the Worker delivers for one request
// instance:
//
//   - The content that the provider's AnswerControlRequest built
//   - The plan settings that the answer applies
//   - The input that the answer queues after itself, for a runtime whose
//     answer cannot carry the user's feedback
//   - The neutral decision that the content delivers (deliveredControlDecision)
//
// No client sends answer content: AnswerControl builds it from a decision.
// The answer row stores the decision, so the transcript row states it even
// when a Worker restart interrupts the recording (controlResponsePlanFromAnswer).
type workerAnswer struct {
	claimToken   string
	content      []byte
	planApproval *leapmuxv1.PlanApprovalSettings
	followUp     agent.ControlFollowUp
	decision     *leapmuxv1.ControlDecision
}

// encodeControlDecision is the form in which the answer row stores decision:
// the protobuf binary form, or nil for no decision, which the row stores as
// empty bytes.
func encodeControlDecision(decision *leapmuxv1.ControlDecision) ([]byte, error) {
	if decision.GetKind() == nil {
		return nil, nil
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(decision)
	if err != nil {
		return nil, fmt.Errorf("encode the control decision: %w", err)
	}
	return encoded, nil
}

// processControlAnswer retains the chosen answer before delivery and
// finalizes it after receipt. Uncertain delivery keeps its reservation and
// cannot resend automatically. The answer's follow-up joins the
// resolution's, so it is stored with the answer and queued when the answer
// is finalized, as a follow-up of the resolution is.
func (svc *Service) processControlAnswer(dbAgent db.Agent, chosen workerAnswer) error {
	agentID, content, claimToken, followUp := dbAgent.ID, chosen.content, chosen.claimToken, chosen.followUp
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
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
	// An agent restart, a context clear, or a provider-side cancel deletes the
	// request row. LeapMux then refuses the answer instead of keeping it.
	// StoreControlResponseAnswer repeats the same condition in SQL (WHERE EXISTS),
	// and the two do not duplicate each other: this guard is the fast path that
	// gives the caller a clear error, and the SQL predicate closes the race between
	// the read above and the INSERT below.
	if !meta.Exists {
		return agent.ErrControlRequestNotPending
	}
	// A SILENT refusal, and the only one here: every other gate below returns an
	// error. The claim token identifies the request INSTANCE, so a mismatch means
	// the card the reader answered from belongs to an instance the worker already
	// retired and reissued. There is no error to report, because nothing went
	// wrong: controlResponseState reports that instance as CANCELED, and the
	// browser retires its stale card on that state and keeps the live one. Do not
	// "fix" this into an error -- that would put a failure toast on a card the
	// reader cannot see any more.
	//
	// It must stay BELOW the Exists gate. A missing row leaves ClaimToken empty,
	// which would take this silent path instead of the error that says the request
	// is gone.
	if meta.answersARetiredInstance(claimToken) {
		return nil
	}
	// The row that the claim below stores must agree with its key. AnswerControl
	// checked the row that it read, but a provider can republish under one claim
	// token, so this read can hold other bytes. The check stays below the two
	// gates above: a gone row and a retired instance keep their own outcomes.
	if err := storedControlRequestRefusal(plugin, agentID, meta); err != nil {
		return err
	}
	plan := resolveControlResponsePlan(plugin, meta, content, chosen.planApproval)
	if followUp != (agent.ControlFollowUp{}) {
		if plan.resolution.FollowUp != (agent.ControlFollowUp{}) {
			return errors.New("the answer and its resolution both state a follow-up")
		}
		plan.resolution.FollowUp = followUp
	}
	if plan.isPlanPrompt() && (!plan.requestMeta.Loaded || !plan.hasDecision) {
		return errors.New("the plan approval has no valid decision")
	}
	if plan.resolution.Withhold {
		return plan.resolution.Refusal()
	}
	// The follow-up invariant: a queueable kind with its text, or nothing at all.
	// A QUESTION_ANSWER follow-up answers a question that LeapMux owns, so no
	// provider request waits for it.
	if !plan.resolution.FollowUp.Valid() {
		return errors.New("the resolution states a follow-up that is neither empty nor a queueable kind with its text")
	}
	if plan.resolution.FollowUp.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER && !plan.resolution.LeapMuxOwned {
		return errors.New("a question-answer follow-up requires a question that LeapMux owns")
	}
	if followUp := plan.resolution.FollowUp; followUp.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
		if err := inputqueue.ValidateContent(followUp.Kind, followUp.Text, nil); err != nil {
			return fmt.Errorf("validate the response input: %w", err)
		}
	}
	if plan.settings != nil && (!plan.hasDecision || plan.behavior() != contracts.ControlAnswerBehaviorAllow ||
		!plan.isPlanPrompt() && plan.resolution.PlanModeControl != agent.PlanModeControlExit) {
		return errors.New("plan settings require an approval for a matching plan request")
	}
	// Encode ONCE, after the gates that can refuse the response. The claim row
	// below and the transcript row that finalizeControlResponse writes both read
	// these bytes, and a second marshal would give them a different byte string.
	if err := plan.encodePlanApprovalSettings(); err != nil {
		return err
	}
	storedDecision, err := encodeControlDecision(chosen.decision)
	if err != nil {
		return err
	}
	deliveryID, err := svc.Output.claimControlResponseAnswer(db.StoreControlResponseAnswerParams{
		AgentID: agentID, RequestID: requestID, ClaimToken: claimToken,
		RequestPayload: plan.requestMeta.Payload, ResponseContent: content,
		ResolvedContent: plan.resolution.Content, FollowUp: plan.resolution.FollowUp.Text,
		FollowUpKind: plan.resolution.FollowUp.Kind, NonBlocking: plan.requestMeta.NonBlocking,
		PlanApprovalSettings: plan.settingsJSON,
		SourceSeq:            plan.requestMeta.SourceSeq,
		AgentSessionID:       plan.requestMeta.AgentSessionID,
		InputID:              controlResponseQueueID(agentID, plan),
		Decision:             storedDecision,
	})
	if err != nil {
		return fmt.Errorf("reserve the control response: %w", err)
	}
	if deliveryID == "" {
		answer, err := readAnswer()
		if errors.Is(err, sql.ErrNoRows) {
			return agent.ErrControlRequestNotPending
		}
		if err != nil {
			return fmt.Errorf("read control response delivery: %w", err)
		}
		return svc.resumeControlResponseFinalization(answer)
	}

	svc.broadcastControlResponseState(dbAgent, requestID, claimToken)
	if err := svc.deliverControlOperation(deliveryID, func() error {
		return svc.executeControlResponse(agentID, dbAgent, plan)
	}); err != nil {
		return err
	}
	answer, err := readAnswer()
	if err != nil {
		return err
	}
	return svc.resumeControlResponseFinalization(answer)
}

// approvedClearContext identifies an approval that requests a fresh context.
func (plan controlResponsePlan) approvedClearContext() bool {
	return plan.behavior() == contracts.ControlAnswerBehaviorAllow && plan.settings.GetClearContext()
}

// exitPlanClearingContext identifies a plan exit that replaces the provider session.
// LeapMux retains its own answer row because it rejects events from the retired session.
// A provider can retain that session in the same process until its native close completes.
func (plan controlResponsePlan) exitPlanClearingContext() bool {
	return plan.approvedClearContext() && plan.resolution.PlanModeControl == agent.PlanModeControlExit
}

// needsTranscriptRow omits duplicate answer displays.
// Plan feedback already uses a queued user message. A native answer echo also replaces the LeapMux row.
// A fresh-context plan exit still needs the row because LeapMux rejects the retired session's echo.
//
// The decision ignores whether the control request still exists, and that is
// deliberate (#258). An agent restart deletes the request row, and the delivered
// answer that ListControlResponsesAwaitingRecording recovers still draws its row.
// An answer that ARRIVES after the request row disappeared never reaches this
// point, because processControlAnswer refuses it.
func (plan controlResponsePlan) needsTranscriptRow() bool {
	// A question answer's queued input writes the row that shows it, as plan
	// feedback does.
	if plan.resolution.FollowUp.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
		return false
	}
	if plan.isPlanPrompt() && plan.behavior() == contracts.ControlAnswerBehaviorDeny && plan.rejectionMessage() != "" {
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
	if !plan.requestMeta.Loaded || !plan.hasDecision || plan.behavior() != contracts.ControlAnswerBehaviorAllow {
		return nil
	}

	crPayload := plan.decision
	// Match the normalized decision to the stored request.
	reqID := crPayload.Response.RequestID
	if reqID == "" || reqID != plan.requestMeta.RequestID {
		return nil
	}
	persistMode := func(mode string) error {
		values := loadOptions(svc.Agents.Registry(), dbAgent.Options, dbAgent.AgentProvider)
		previous := OptionMap{agent.OptionIDPermissionMode: values[agent.OptionIDPermissionMode]}
		values[agent.OptionIDPermissionMode] = mode
		_, err := svc.persistOptionChanges(dbAgent, previous, values, false)
		return err
	}

	// Each provider supplies its own permission-mode values.
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
	switch plan.resolution.PlanModeControl {
	case agent.PlanModeControlEnter:
		if enterMode := plugin.PlanModePermissionMode(agent.PlanModeControlEnter); enterMode != "" {
			return persistMode(enterMode)
		}
	case agent.PlanModeControlExit:
		// The mode that the plan decision states wins; otherwise the provider's own exit mode.
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

// controlFeedback is the feedback of decision: the reason of a refused
// permission, plan or question, or "" for a decision of another kind.
func controlFeedback(decision *leapmuxv1.ControlDecision) string {
	switch kind := decision.GetKind().(type) {
	case *leapmuxv1.ControlDecision_Permission:
		return kind.Permission.GetFeedback()
	case *leapmuxv1.ControlDecision_Plan:
		return kind.Plan.GetFeedback()
	case *leapmuxv1.ControlDecision_Question:
		return kind.Question.GetFeedback()
	default:
		return ""
	}
}

// withoutControlFeedback is a copy of decision whose feedback is empty. It
// leaves decision itself alone.
func withoutControlFeedback(decision *leapmuxv1.ControlDecision) *leapmuxv1.ControlDecision {
	bare := proto.CloneOf(decision)
	switch kind := bare.GetKind().(type) {
	case *leapmuxv1.ControlDecision_Permission:
		kind.Permission.Feedback = ""
	case *leapmuxv1.ControlDecision_Plan:
		kind.Plan.Feedback = ""
	case *leapmuxv1.ControlDecision_Question:
		kind.Question.Feedback = ""
	case *leapmuxv1.ControlDecision_Elicitation, *leapmuxv1.ControlDecision_Dialog:
		// Neither carries feedback.
	}
	return bare
}

// deliveredControlDecision is the decision that answer delivers to the
// runtime: the decision of input, without feedback that reaches the runtime
// neither in the answer nor as a message after it. The answer row stores it,
// so the transcript row never states a reason that the runtime did not
// receive. A runtime that takes no reason with some answer, such as
// OpenCode's dismissed question, gets none from its provider.
//
// The test needs no knowledge of the provider. The provider builds the answer
// a second time without the feedback, and the feedback reached the runtime
// unless the runtime receives the same thing either way: the same resolved
// bytes, the same follow-up and the same plan settings. A second answer that
// fails keeps the feedback, which errs toward stating it.
func deliveredControlDecision(plugin agent.Provider, meta controlResponseRequestMetadata, input agent.ControlAnswerInput, answer agent.ControlAnswer) *leapmuxv1.ControlDecision {
	decision := input.Decision
	if controlFeedback(decision) == "" {
		return decision
	}
	bare := withoutControlFeedback(decision)
	input.Decision = bare
	without, err := plugin.AnswerControlRequest(input)
	if err != nil || !sameControlDelivery(plugin, meta, answer, without) {
		return decision
	}
	return bare
}

// sameControlDelivery reports whether the runtime receives the same thing for
// the answers a and b to the request of meta.
func sameControlDelivery(plugin agent.Provider, meta controlResponseRequestMetadata, a, b agent.ControlAnswer) bool {
	if a.FollowUp != b.FollowUp || !proto.Equal(a.PlanApproval, b.PlanApproval) {
		return false
	}
	resolvedA := resolveControlResponsePlan(plugin, meta, a.Content, a.PlanApproval).resolution
	resolvedB := resolveControlResponsePlan(plugin, meta, b.Content, b.PlanApproval).resolution
	return resolvedA.Withhold == resolvedB.Withhold && resolvedA.FollowUp == resolvedB.FollowUp &&
		bytes.Equal(resolvedA.Content, resolvedB.Content)
}

// transcriptControlDecision is the decision that the answer's transcript row
// states: the delivered decision, without the feedback that a follow-up input
// delivers. A runtime whose answer cannot carry the feedback receives it as
// the user's next message (agent.FeedbackFollowUp), and the transcript draws
// that message as a row of its own, so the answer's row would show the same
// text a second time. The answer row keeps the whole decision.
func (plan controlResponsePlan) transcriptControlDecision() *leapmuxv1.ControlDecision {
	decision := plan.controlDecision
	if decision == nil || plan.resolution.FollowUp.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK {
		return decision
	}
	return withoutControlFeedback(decision)
}

// controlResponseMessageContent preserves the sent bytes and the complete matching request separately.
// Worker request identity belongs to metadata, outside both provider payloads, and
// so do the prompt that the request showed and the decision that answered it:
// the row's display reads the answer's words from the two, so the browser
// reads no provider's request or answer format. A nil prompt states none, and
// an answer that the Worker recorded without a decision states none.
func controlResponseMessageContent(plan controlResponsePlan, prompt *leapmuxv1.ControlPrompt) (agent.MessageContent, error) {
	fields := map[string]any{
		contracts.MessageMetadataFieldControlRequestID:         plan.requestMeta.RequestID,
		contracts.MessageMetadataFieldControlRequestClaimToken: plan.requestMeta.ClaimToken,
	}
	if prompt != nil {
		encoded, err := protojson.Marshal(prompt)
		if err != nil {
			return agent.MessageContent{}, fmt.Errorf("encode the control prompt: %w", err)
		}
		fields[contracts.MessageMetadataFieldControlPrompt] = json.RawMessage(encoded)
	}
	if decision := plan.transcriptControlDecision(); decision != nil {
		encoded, err := protojson.Marshal(decision)
		if err != nil {
			return agent.MessageContent{}, fmt.Errorf("encode the control decision: %w", err)
		}
		fields[contracts.MessageMetadataFieldControlDecision] = json.RawMessage(encoded)
	}
	if plan.settings != nil && len(plan.settingsJSON) == 0 {
		// The plan carries settings that nothing encoded, so this row would drop
		// them in silence. Refuse instead: the caller rebuilds the plan from the
		// answer row, which always carries the stored encoding.
		return agent.MessageContent{}, errors.New("the plan approval settings have no stored encoding")
	}
	if len(plan.settingsJSON) > 0 {
		// The SAME bytes the claim row stored, not a second encoding of the
		// message they decode to.
		fields[contracts.MessageMetadataFieldPlanApprovalSettings] = json.RawMessage(plan.settingsJSON)
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
