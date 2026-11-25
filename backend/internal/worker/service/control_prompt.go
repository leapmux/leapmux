package service

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var errInvalidControlArgument = errors.New("invalid control argument")

// controlArgumentError wraps a caller-validation refusal while keeping its
// exact message text for tests and user-facing errors.
type controlArgumentError struct {
	msg string
}

func (e controlArgumentError) Error() string { return e.msg }
func (e controlArgumentError) Unwrap() error { return errInvalidControlArgument }

func invalidControlArgument(msg string) error {
	return controlArgumentError{msg: msg}
}

func sendControlError(sender channel.ResponseWriter, err error) {
	if errors.Is(err, errInvalidControlArgument) || errors.Is(err, agent.ErrInvalidControlDecision) {
		sendInvalidArgument(sender, err.Error())
		return
	}
	sendInternalError(sender, err.Error())
}

// controlRequestPrompt reads what one control request asks, through its
// provider, with the rows that the prompt can need (agent.ReadControlPrompt).
// The loaders read a row only when the provider asks for it, so a payload
// that is not a JSON object costs no read.
func controlRequestPrompt(queries *db.Queries, plugin agent.Provider, agentID string, request agent.ControlRequest) *leapmuxv1.ControlPrompt {
	return agent.ReadControlPrompt(plugin, controlRequestInput(queries, plugin, agentID, request))
}

// controlRequestInput is the provider input of one stored control request:
// its payload, and loaders for the two rows that a prompt can need. Each
// loader reads the row when the provider asks for it, so a prompt that needs
// neither costs no read.
func controlRequestInput(queries *db.Queries, plugin agent.Provider, agentID string, request agent.ControlRequest) agent.ControlRequestInput {
	input := agent.NewControlRequestInput(request.Payload)
	input.AgentSessionID = request.AgentSessionID
	if queries == nil {
		return input
	}
	input.Source = func() (agent.MessageContent, bool) {
		if request.SourceSeq <= 0 {
			return agent.MessageContent{}, false
		}
		row, err := queries.GetMessageByAgentIDAndSeq(bgCtx(), db.GetMessageByAgentIDAndSeqParams{AgentID: agentID, Seq: request.SourceSeq})
		return resolvedControlRow(plugin, agentID, row, err, "control request source", "seq", strconv.FormatInt(request.SourceSeq, 10))
	}
	input.SpanRequest = func(spanID string) (agent.MessageContent, bool) {
		if spanID == "" {
			return agent.MessageContent{}, false
		}
		row, err := queries.GetAgentMessageBySpanIDAndSource(bgCtx(), db.GetAgentMessageBySpanIDAndSourceParams{
			AgentID: agentID, AgentSessionID: request.AgentSessionID, SpanID: spanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		})
		return resolvedControlRow(plugin, agentID, row, err, "control request tool call", "span_id", spanID)
	}
	return input
}

// resolvedControlRow decodes one row that a prompt reads and resolves it
// through the provider (agent.ResolveMessageContent). A row that cannot be
// read reads as absent: the prompt then states less, and the warning says
// why. key and value identify the row in the warning.
func resolvedControlRow(plugin agent.Provider, agentID string, row db.Message, err error, what, key, value string) (agent.MessageContent, bool) {
	if errors.Is(err, sql.ErrNoRows) {
		return agent.MessageContent{}, false
	}
	if err != nil {
		slog.Warn("read the "+what, "agent_id", agentID, key, value, "error", err)
		return agent.MessageContent{}, false
	}
	content, err := decodeStoredRow(row, what)
	if err != nil {
		slog.Warn("decode the "+what, "agent_id", agentID, key, value, "error", err)
		return agent.MessageContent{}, false
	}
	return agent.MessageContent{
		Original:       agent.ResolveMessageContent(plugin, content),
		Supplemental:   content.Supplemental,
		AgentSessionID: content.AgentSessionID,
	}, true
}

// listControlRequests lists the requests that the agent waits on, each with
// its prompt and its response state.
func (svc *Service) listControlRequests(dbAgent db.Agent) (*leapmuxv1.ListControlRequestsResponse, error) {
	rows, err := svc.Output.ListControlRequests(bgCtx(), dbAgent.ID)
	if err != nil {
		return nil, err
	}
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
	response := &leapmuxv1.ListControlRequestsResponse{}
	for _, row := range rows {
		request, err := buildAgentControlRequest(svc.Output, plugin, dbAgent.ID, dbAgent.AgentProvider, row.controlRequest(), row.ClaimToken)
		if err != nil {
			return nil, err
		}
		response.Requests = append(response.Requests, request)
	}
	return response, nil
}

// unanswerableControlRequest refuses a decision about a request whose prompt
// states a fault (leapmuxv1.ControlPayloadFault): no decision answers it.
func unanswerableControlRequest() error {
	return agent.InvalidControlDecision("LeapMux cannot read this request, so it takes no answer; stop the agent instead")
}

// answerControl answers one request instance. The provider builds the
// answer for a decision and verifies native permission choices against
// the offered options. The Worker then claims, delivers and records it.
//
// A request that is gone, or an instance that the Worker retired, answers
// with its state and no error, as RecordControlResponse does for it. A failed
// read also answers with the state, which reads the row again and states its
// own failure. A stored request whose payload disagrees with its key
// (storedControlRequestRefusal) answers with its state and the refusal, as
// the claim path refuses an answer. So each error that this returns is the
// caller's: a missing request ID, a decision about a request whose prompt
// states a fault, or a decision that does not answer the prompt.
func (svc *Service) answerControl(dbAgent db.Agent, r *leapmuxv1.AnswerControlRequest) (*leapmuxv1.AnswerControlResponse, error) {
	if r.GetRequestId() == "" {
		return nil, invalidControlArgument("the control request ID is missing")
	}
	if r.GetClaimToken() == "" {
		return nil, invalidControlArgument("the control request claim token is missing")
	}
	native := r.GetNativeResponse()
	if (r.GetDecision() == nil) == (native == nil) {
		return nil, invalidControlArgument("supply exactly one control decision or native response")
	}
	if native != nil && len(native.GetContent()) == 0 {
		return nil, invalidControlArgument("the native response is empty")
	}
	finish, err := svc.admitAgentMutation(bgCtx(), dbAgent)
	if errors.Is(err, errAgentPublicationClosed) {
		state, readErr := closedControlResponseState(bgCtx(), svc.Queries, dbAgent.ID, r.GetRequestId(), r.GetClaimToken())
		if readErr != nil {
			return nil, readErr
		}
		return &leapmuxv1.AnswerControlResponse{State: state}, nil
	}
	if err != nil {
		return nil, err
	}
	defer finish()
	if existing, answerErr := svc.Queries.GetControlResponseAnswer(bgCtx(), db.GetControlResponseAnswerParams{
		AgentID: dbAgent.ID, RequestID: r.GetRequestId(), ClaimToken: r.GetClaimToken(),
	}); answerErr == nil {
		opErr := svc.resumeControlResponseFinalization(bgCtx(), existing)
		state, errText := svc.reportControlResponseState(dbAgent, r.GetRequestId(), r.GetClaimToken(), opErr)
		return &leapmuxv1.AnswerControlResponse{State: state, Error: errText}, nil
	} else if !errors.Is(answerErr, sql.ErrNoRows) {
		return nil, answerErr
	}
	row, found, err := svc.Output.LookupControlRequest(bgCtx(), dbAgent.ID, r.GetRequestId())
	if err != nil {
		state, stateErr := controlResponseState(bgCtx(), svc.Output, dbAgent.ID, r.GetRequestId(), r.GetClaimToken())
		return &leapmuxv1.AnswerControlResponse{State: state, Error: errors.Join(err, stateErr).Error()}, nil
	}
	if !found || row.ClaimToken != r.GetClaimToken() {
		state, stateErr := controlResponseState(bgCtx(), svc.Output, dbAgent.ID, r.GetRequestId(), r.GetClaimToken())
		response := &leapmuxv1.AnswerControlResponse{State: state}
		if stateErr != nil {
			response.Error = stateErr.Error()
		}
		return response, nil
	}
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
	// A payload that is no JSON object states its fault, which is the refusal.
	// Any other payload must agree with its key before a provider reads it.
	if _, unreadable := agent.UnreadableControlPrompt(row.Payload); unreadable {
		return nil, unanswerableControlRequest()
	}
	meta := storedControlRequestMetadata(row)
	if refusal := storedControlRequestRefusal(plugin, dbAgent.ID, meta); refusal != nil {
		state, errText := svc.reportControlResponseState(dbAgent, row.RequestID, row.ClaimToken, refusal)
		return &leapmuxv1.AnswerControlResponse{State: state, Error: errText}, nil
	}
	reqInput := controlRequestInput(svc.Queries, plugin, dbAgent.ID, row.controlRequest())
	prompt := agent.ReadControlPrompt(plugin, reqInput)
	if prompt.GetUnreadable() != leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		// The provider does not recognize the request (agent.UnrecognizedControlPrompt).
		return nil, unanswerableControlRequest()
	}
	input := agent.ControlAnswerInput{
		Request:   reqInput,
		RequestID: row.RequestID,
		Prompt:    prompt,
		Decision:  r.GetDecision(),
	}
	chosen := workerAnswer{claimToken: r.GetClaimToken(), observedRequest: &meta}
	var answer agent.ControlAnswer
	if native != nil {
		answer, input.Decision, err = matchNativePermissionAnswer(plugin, input, native.GetContent())
		chosen.nativeContent = native.GetContent()
	} else {
		answer, err = plugin.AnswerControlRequest(input)
	}
	if err != nil {
		return nil, err
	}
	if answer.FollowUp != "" {
		chosen.followUp = agent.FeedbackFollowUp(answer.FollowUp)
	}
	chosen.content = answer.Content
	chosen.planApproval = answer.PlanApproval
	chosen.decision = deliveredControlDecision(plugin, meta, input, answer)
	state, errText, err := svc.deliverControlAnswer(dbAgent, row.RequestID, chosen)
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.AnswerControlResponse{State: state, Error: errText}, nil
}
