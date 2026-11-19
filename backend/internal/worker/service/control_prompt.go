package service

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

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
	rows, err := svc.Queries.ListControlRequestsByAgentID(bgCtx(), dbAgent.ID)
	if err != nil {
		return nil, err
	}
	plugin := svc.Agents.Registry().Plugin(dbAgent.AgentProvider)
	response := &leapmuxv1.ListControlRequestsResponse{}
	for _, row := range rows {
		request, err := buildAgentControlRequest(svc.Queries, plugin, dbAgent.ID, dbAgent.AgentProvider, agent.ControlRequest{
			RequestID: row.RequestID, Payload: row.Payload, SourceSeq: row.SourceSeq, AgentSessionID: row.AgentSessionID, NonBlocking: row.NonBlocking,
		}, row.ClaimToken)
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

// answerControl answers one request instance with a neutral decision. The
// provider builds its own answer from the decision, and the Worker then
// claims, delivers and records it (deliverControlAnswer). No client sends
// answer content of its own.
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
		return nil, errors.New("the control request ID is missing")
	}
	row, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{AgentID: dbAgent.ID, RequestID: r.GetRequestId()})
	if err != nil || row.ClaimToken != r.GetClaimToken() {
		state, stateErr := controlResponseState(svc.Queries, dbAgent.ID, r.GetRequestId(), r.GetClaimToken())
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
	request := agent.ControlRequest{
		RequestID: row.RequestID, Payload: row.Payload, SourceSeq: row.SourceSeq, AgentSessionID: row.AgentSessionID, NonBlocking: row.NonBlocking,
	}
	prompt := controlRequestPrompt(svc.Queries, plugin, dbAgent.ID, request)
	if prompt.GetUnreadable() != leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		// The provider does not recognize the request (agent.UnrecognizedControlPrompt).
		return nil, unanswerableControlRequest()
	}
	input := agent.ControlAnswerInput{
		Request:   controlRequestInput(svc.Queries, plugin, dbAgent.ID, request),
		RequestID: row.RequestID,
		Prompt:    prompt,
		Decision:  r.GetDecision(),
	}
	answer, err := plugin.AnswerControlRequest(input)
	if err != nil {
		return nil, err
	}
	var followUp agent.ControlFollowUp
	if answer.FollowUp != "" {
		followUp = agent.ControlFollowUp{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: answer.FollowUp}
	}
	state, errText, err := svc.deliverControlAnswer(dbAgent, row.RequestID, workerAnswer{
		claimToken:   r.GetClaimToken(),
		content:      answer.Content,
		planApproval: answer.PlanApproval,
		followUp:     followUp,
		decision:     deliveredControlDecision(plugin, meta, input, answer),
	})
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.AnswerControlResponse{State: state, Error: errText}, nil
}
