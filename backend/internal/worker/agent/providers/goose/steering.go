package goose

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.SteerEchoReporter = (*Agent)(nil)

// SteerRowAtEcho keeps a Goose steer in AWAITING_ECHO until Goose emits the
// steered `user_message_chunk` with `_meta.goose.steer == true` when
// SteerOperation drains it into the active run.
func (*Agent) SteerRowAtEcho() bool { return true }

type gooseSteerReply struct {
	RunID     string `json:"runId"`
	MessageID string `json:"messageId"`
}

const (
	gooseSteerWriteLabel      = "record Goose steer outcome"
	gooseEarlySteerWriteLabel = "record early Goose steer outcome"
)

func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	content, attachments := request.Content, request.Attachments
	method, active, sessionID, runID := a.SteerTarget()
	if method == "" {
		return agent.NativeInputReceipt{}, agent.ErrSteeringUnsupported
	}
	if !active || runID == "" {
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"expectedRunId": runID,
		"prompt":        acp.BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return agent.NativeInputReceipt{}, fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if request.InputID == "" || request.AttemptID == "" {
		if _, err := a.SendRequest(method, params, a.APITimeout()); err != nil {
			return agent.NativeInputReceipt{}, mapGooseSteerError(method, err)
		}
		if !a.SteerRunActive(runID) {
			return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
		}
		return agent.NativeInputReceipt{}, nil
	}

	dispatch, err := a.steers.Begin(sessionID, agent.SteerResolution{
		InputID:   request.InputID,
		AttemptID: request.AttemptID,
	})
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	defer a.steers.Cancel(&a.Base, gooseSteerWriteLabel, dispatch)

	var replyErr error
	_, err = a.SendRequestObserved(method, params, a.APITimeout(), func(raw json.RawMessage, err error) {
		if err == nil {
			replyErr = a.acceptGooseSteerReply(dispatch, raw)
		}
	})
	if err != nil {
		return agent.NativeInputReceipt{}, mapGooseSteerError(method, err)
	}
	if replyErr != nil {
		return agent.NativeInputReceipt{}, replyErr
	}
	if !a.SteerRunActive(runID) {
		if a.steers.DiscardPending(dispatch) {
			return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
		}
		return agent.NativeInputReceipt{}, nil
	}
	return agent.NativeInputReceipt{}, nil
}

func mapGooseSteerError(method string, err error) error {
	if providerkit.HasJSONRPCErrorCode(err, -32601) {
		return agent.ErrSteeringUnsupported
	}
	if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
		return agent.ErrNoActiveTurn
	}
	return providerkit.ClassifyJSONRPCDeliveryError(method, err)
}

func (a *Agent) acceptGooseSteerReply(dispatch *acp.SteerEchoDispatch, raw json.RawMessage) error {
	var reply gooseSteerReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("%w: read the Goose steer reply: %w", agent.ErrDeliveryUncertain, err)
	}
	return a.steers.Accept(&a.Base, "Goose", gooseSteerWriteLabel, dispatch, reply.MessageID)
}

// observeGooseSteerChunk resolves a pending Goose steer when SteerOperation
// emits the steered UserMessageChunk with `_meta.goose.steer == true`.
func (a *Agent) observeGooseSteerChunk(metadata map[string]json.RawMessage, update json.RawMessage) bool {
	var gooseMeta struct {
		MessageID string `json:"messageId"`
		Steer     bool   `json:"steer"`
	}
	if json.Unmarshal(metadata[gooseSteerNamespace], &gooseMeta) != nil || !gooseMeta.Steer {
		return false
	}
	messageID := gooseMeta.MessageID
	if messageID == "" {
		var frame struct {
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(update, &frame) == nil {
			messageID = frame.MessageID
		}
	}
	if messageID == "" {
		return true
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.steers.Resolve(&a.Base, gooseSteerWriteLabel, gooseEarlySteerWriteLabel, messageID, agent.SteerOutcomeEchoed)
	return true
}

func (a *Agent) dropPendingGooseSteers(outcome agent.SteerOutcome) {
	a.steers.DropPending(&a.Base, gooseSteerWriteLabel, outcome)
}
