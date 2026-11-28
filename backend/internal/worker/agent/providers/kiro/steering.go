package kiro

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	kiroSteerWriteLabel      = "record Kiro steer outcome"
	kiroEarlySteerWriteLabel = "record early Kiro steer outcome"
)

// kiroSteerReply is Kiro's answer to a steer.
type kiroSteerReply struct {
	Queued    *bool  `json:"queued"`
	MessageID string `json:"messageId"`
	Dropped   string `json:"dropped"`
}

// SteerInput sends text into the running turn. Kiro queues it and injects it
// before its next model call. If that turn ended, the Worker keeps the input
// for the next prompt. Kiro accepts text alone through this route.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	if err := providerkit.TextOnlySteer("Kiro", request.Attachments); err != nil {
		return agent.NativeInputReceipt{}, err
	}
	err := a.WithSessionID(func(sessionID string) error {
		if !a.PromptActive() {
			return agent.ErrNoActiveTurn
		}
		if request.InputID == "" {
			return errors.New("a Kiro steer requires an input ID")
		}
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "message": request.Content})
		if err != nil {
			return fmt.Errorf("marshal the Kiro steer: %w", err)
		}
		// The Worker writes a control reply at dispatch. That reply has no
		// echo reservation to resolve when Kiro repeats the text.
		if request.AttemptID == "" {
			raw, err := a.SendRequest(kiroSteerMethod, params, a.APITimeout())
			if err != nil {
				return providerkit.ClassifyJSONRPCDeliveryError(kiroSteerMethod, err)
			}
			_, err = readKiroSteerReply(raw)
			return err
		}
		dispatch, err := a.steers.Begin(sessionID, agent.SteerResolution{
			InputID: request.InputID, AttemptID: request.AttemptID,
		})
		if err != nil {
			return err
		}
		defer a.steers.Cancel(&a.Base, kiroSteerWriteLabel, dispatch)

		var replyErr error
		_, err = a.SendRequestObserved(kiroSteerMethod, params, a.APITimeout(), func(raw json.RawMessage, err error) {
			if err == nil {
				replyErr = a.acceptKiroSteerReply(dispatch, raw)
			}
		})
		if err != nil {
			return providerkit.ClassifyJSONRPCDeliveryError(kiroSteerMethod, err)
		}
		return replyErr
	})
	return agent.NativeInputReceipt{}, err
}

func readKiroSteerReply(raw json.RawMessage) (kiroSteerReply, error) {
	var reply kiroSteerReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return reply, fmt.Errorf("%w: read the Kiro steer reply: %w", agent.ErrDeliveryUncertain, err)
	}
	if reply.Queued == nil {
		return reply, fmt.Errorf("%w: the Kiro steer reply omitted its queue decision", agent.ErrDeliveryUncertain)
	}
	if !*reply.Queued {
		return reply, fmt.Errorf("%w: Kiro dropped the steer (%s)", agent.ErrNoActiveTurn, reply.Dropped)
	}
	return reply, nil
}

func (a *Agent) acceptKiroSteerReply(dispatch *acp.SteerEchoDispatch, raw json.RawMessage) error {
	reply, err := readKiroSteerReply(raw)
	if err != nil {
		return err
	}
	return a.steers.Accept(&a.Base, "Kiro", kiroSteerWriteLabel, dispatch, reply.MessageID)
}

// handleSteeringInjected runs on the reader at Kiro's model input boundary.
// Reserve the row there even if the RPC reply has yet to give us the ID.
func (a *Agent) handleSteeringInjected(messageID string) {
	if messageID == "" {
		return
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.steers.Resolve(&a.Base, kiroSteerWriteLabel, kiroEarlySteerWriteLabel, messageID, agent.SteerOutcomeEchoed)
}

func (a *Agent) handleSteeringCleared(raw json.RawMessage) {
	var cleared []string
	if json.Unmarshal(raw, &cleared) != nil {
		return
	}
	for _, id := range cleared {
		a.steers.Resolve(&a.Base, kiroSteerWriteLabel, kiroEarlySteerWriteLabel, id, agent.SteerOutcomeDropped)
	}
}

func (a *Agent) dropPendingSteers() {
	a.steers.DropPending(&a.Base, kiroSteerWriteLabel, agent.SteerOutcomeDropped)
}
