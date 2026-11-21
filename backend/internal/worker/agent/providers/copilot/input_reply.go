package copilot

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type copilotReplyFields struct {
	MessageID            string `json:"messageId"`
	OriginatingMessageID string `json:"originatingMessageId"`
	TurnID               string `json:"turnId"`
	InteractionID        string `json:"interactionId"`
	RequestID            string `json:"requestId"`
	Mode                 string `json:"mode"`
	Reason               string `json:"reason"`
	Aborted              bool   `json:"aborted"`
}

type copilotReplyFrame struct {
	id, parentID, kind string
	root, ephemeral    bool
	fields             copilotReplyFields
	control            agent.NativeControlInputs
}

type copilotReplyCapture struct {
	receipt       agent.NativeInputReceipt
	early         []copilotReplyFrame
	earlyOverflow bool
	// parent is the most recent event proved to belong to the submitted input.
	parent, interaction, iteration string
	iterationEnded                 bool
	items                          []agent.NativeReplyItem
}

const maxCopilotEarlyReplyFrames = 4096
const copilotAutopilotStopped = "session.autopilot_stopped"
const copilotReplyInputOrdinal int64 = 1

// The request returns after Copilot queues the message. The echo, output, and
// ending event can all reach the reader before that response tells us its ID.
func (a *Agent) beginNativeReplyCapture(session string) *copilotReplyCapture {
	capture := &copilotReplyCapture{receipt: agent.NativeInputReceipt{SessionID: session}}
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.replyCaptures == nil {
		a.replyCaptures = make(map[*copilotReplyCapture]struct{})
	}
	a.replyCaptures[capture] = struct{}{}
	return capture
}

func (a *Agent) acceptNativeReplyCapture(capture *copilotReplyCapture, messageID string) agent.NativeInputReceipt {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	capture.receipt.InputID = messageID
	capture.receipt.TurnID = messageID
	if capture.earlyOverflow {
		a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return capture.receipt
	}
	frames := capture.early
	capture.early = nil
	for _, frame := range frames {
		if _, active := a.replyCaptures[capture]; !active {
			break
		}
		a.advanceNativeReplyCapture(capture, frame)
	}
	// The process can exit after the reader hands off the acknowledgment.
	// Keep any completion already buffered, then retire a capture that the
	// closed reader can no longer finish.
	if _, active := a.replyCaptures[capture]; a.closing && active {
		a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	return capture.receipt
}

func (a *Agent) discardNativeReplyCapture(capture *copilotReplyCapture) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	delete(a.replyCaptures, capture)
}

func copilotReplyMessageKey(eventID string) string {
	return "copilot-reply:" + eventID
}

// observeNativeReply runs after the event's transcript row or control card
// is published. outputMu also serializes it against RPC acknowledgements.
func (a *Agent) observeNativeReply(raw []byte, event copilotEvent) {
	if len(a.replyCaptures) == 0 {
		return
	}
	frame := copilotReplyFrame{
		id: event.ID, parentID: event.ParentID, kind: event.Type,
		root: event.AgentID == "", ephemeral: event.Ephemeral,
	}
	if frame.root {
		switch frame.kind {
		case contracts.CopilotEventUserMessage, contracts.CopilotEventAssistantMessage,
			contracts.CopilotEventAssistantTurnStart, contracts.CopilotEventAssistantTurnEnd,
			contracts.CopilotEventSessionIdle, copilotAutopilotStopped,
			contracts.CopilotEventSessionError:
			_ = json.Unmarshal(event.Data, &frame.fields)
		default:
			for _, spec := range copilotControlSpecs {
				if spec.requestEvent == frame.kind {
					frame.control = a.nativeReplyControl(raw)
					break
				}
			}
		}
	}
	for capture := range a.replyCaptures {
		if capture.receipt.InputID != "" {
			a.advanceNativeReplyCapture(capture, frame)
		} else if len(capture.early) == maxCopilotEarlyReplyFrames {
			capture.earlyOverflow = true
		} else {
			capture.early = append(capture.early, frame)
		}
	}
}

func (a *Agent) nativeReplyControl(raw []byte) agent.NativeControlInputs {
	key, ok := (copilotProvider{}).ControlRequestKey(raw, a.currentNativeSessionID())
	if !ok {
		return agent.NativeControlInputs{}
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if pending := a.controls[key]; pending != nil && pending.published {
		return agent.NativeControlInputs{SessionID: pending.sessionID, RequestID: key, InstanceID: pending.instanceID}
	}
	return agent.NativeControlInputs{}
}

func (a *Agent) advanceNativeReplyCapture(capture *copilotReplyCapture, frame copilotReplyFrame) {
	fields := frame.fields
	if capture.parent == "" {
		if frame.root && frame.kind == contracts.CopilotEventUserMessage && fields.MessageID == capture.receipt.InputID &&
			frame.id != "" && fields.InteractionID != "" && !frame.ephemeral {
			capture.parent = frame.id
			capture.interaction = fields.InteractionID
		}
		return
	}
	if frame.id == capture.parent {
		return
	}
	linked := frame.parentID == capture.parent
	if linked && !frame.ephemeral && frame.id != "" {
		capture.parent = frame.id
	}
	if !frame.root {
		return
	}
	if linked && frame.control.RequestID != "" && capture.iteration != "" && !capture.iterationEnded {
		frame.control.InputIDs = []string{capture.receipt.InputID}
		if err := a.sink.SetNativeControlInputs(frame.control); err != nil && !agent.OutputRetained(err) {
			slog.Error("Bind Copilot control to native input", "error", err)
		}
	}
	switch frame.kind {
	case contracts.CopilotEventUserMessage:
		a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	case contracts.CopilotEventAssistantTurnStart:
		if fields.InteractionID != capture.interaction || fields.TurnID == "" || frame.id == "" {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
			return
		}
		capture.iteration, capture.iterationEnded = fields.TurnID, false
		capture.parent = frame.id
	case contracts.CopilotEventAssistantMessage:
		if fields.OriginatingMessageID != capture.receipt.InputID || fields.TurnID == "" ||
			fields.TurnID != capture.iteration || capture.iterationEnded || frame.id == "" {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
			return
		}
		capture.items = append(capture.items, agent.NativeReplyItem{
			Ordinal: copilotReplyInputOrdinal + int64(len(capture.items)) + 1, MessageKey: copilotReplyMessageKey(frame.id),
		})
		capture.parent = frame.id
	case contracts.CopilotEventAssistantTurnEnd:
		// The provider can keep telemetry in its log without sending it to
		// this listener. Match the native iteration here, then follow its
		// persisted descendants to attribute the session-wide idle event.
		if capture.iteration != "" && fields.TurnID == capture.iteration && frame.id != "" {
			capture.iterationEnded = true
			capture.parent = frame.id
		}
	case contracts.CopilotEventSessionIdle:
		if !linked {
			return
		}
		if fields.Aborted {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED)
		} else if fields.Mode != contracts.CopilotModeAutopilot {
			outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
			if capture.iterationEnded {
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
			}
			a.endNativeReplyCapture(capture, outcome)
		}
	case contracts.CopilotEventSessionError:
		if fields.OriginatingMessageID == capture.receipt.InputID {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
		}
	case copilotAutopilotStopped:
		if fields.RequestID == capture.receipt.InputID {
			outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
			switch fields.Reason {
			case "error":
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
			case "aborted":
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
			}
			a.endNativeReplyCapture(capture, outcome)
		}
	}
}

func (a *Agent) endNativeReplyCapture(capture *copilotReplyCapture, outcome leapmuxv1.AgentInputTurnOutcome) {
	delete(a.replyCaptures, capture)
	receipt := capture.receipt
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
			SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome,
			Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: copilotReplyInputOrdinal}},
			Items:  capture.items,
		})
		if err == nil || agent.OutputRetained(err) {
			return
		}
		slog.Error("Persist exact Copilot reply", "error", err)
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); err != nil && !agent.OutputRetained(err) {
		slog.Error("Record Copilot reply outcome", "error", err)
	}
}

// discardNativeReplyCaptures ends unfinished waits for the retiring session.
// The caller holds outputMu.
func (a *Agent) discardNativeReplyCaptures() {
	for capture := range a.replyCaptures {
		if capture.receipt.InputID != "" {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
	}
}
