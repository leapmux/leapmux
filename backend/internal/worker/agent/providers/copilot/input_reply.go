package copilot

import (
	"bytes"
	"cmp"
	"encoding/json"
	"log/slog"
	"slices"

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
	steerEcho          bool
	fields             copilotReplyFields
	control            agent.NativeControlInputs
}

type copilotReplyCapture struct {
	order         uint64
	receipt       agent.NativeInputReceipt
	steer         *copilotQueuedSteer
	early         []copilotReplyFrame
	earlyOverflow bool
	// parent is the most recent event proved to belong to the submitted input.
	parent, interaction, iteration string
	originatingInput               string
	iterationEnded                 bool
	items                          []agent.NativeReplyItem
}

type copilotQueuedSteer struct {
	prompt     string
	resolution agent.SteerResolution
	echoed     bool
}

type copilotDeferredSteerEvent struct {
	raw   []byte
	event copilotEvent
}

const maxCopilotEarlyReplyFrames = 4096
const maxCopilotDeferredSteerBytes = 8 << 20
const copilotAutopilotStopped = "session.autopilot_stopped"
const copilotReplyInputOrdinal int64 = 1

// The request returns after Copilot queues the message. The echo, output, and
// ending event can all reach the reader before that response tells us its ID.
func (a *Agent) beginNativeReplyCapture(session string) *copilotReplyCapture {
	return a.registerNativeReplyCapture(&copilotReplyCapture{receipt: agent.NativeInputReceipt{SessionID: session}})
}

func (a *Agent) beginNativeSteerCapture(session string, request agent.SteerRequest) *copilotReplyCapture {
	return a.registerNativeReplyCapture(&copilotReplyCapture{
		receipt: agent.NativeInputReceipt{SessionID: session},
		steer: &copilotQueuedSteer{prompt: request.Content, resolution: agent.SteerResolution{
			InputID: request.InputID, AttemptID: request.AttemptID,
		}},
	})
}

func (a *Agent) registerNativeReplyCapture(capture *copilotReplyCapture) *copilotReplyCapture {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if a.replyCaptures == nil {
		a.replyCaptures = make(map[*copilotReplyCapture]struct{})
	}
	a.nextReplyCaptureOrder++
	capture.order = a.nextReplyCaptureOrder
	a.replyCaptures[capture] = struct{}{}
	return capture
}

func (a *Agent) orderedReplyCaptures() []*copilotReplyCapture {
	if len(a.replyCaptures) == 0 {
		return nil
	}
	ordered := make([]*copilotReplyCapture, 0, len(a.replyCaptures))
	for capture := range a.replyCaptures {
		ordered = append(ordered, capture)
	}
	slices.SortFunc(ordered, func(left, right *copilotReplyCapture) int {
		return cmp.Compare(left.order, right.order)
	})
	return ordered
}

func (a *Agent) acceptNativeReplyCapture(capture *copilotReplyCapture, messageID string) agent.NativeInputReceipt {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	defer a.flushDeferredSteerEvents()
	capture.receipt.InputID = messageID
	capture.receipt.TurnID = messageID
	if capture.steer == nil {
		capture.originatingInput = messageID
	}
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
		a.bindNativeReplyControl(frame, []*copilotReplyCapture{capture})
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
	a.flushDeferredSteerEvents()
}

func (a *Agent) awaitsSteerAcknowledgment() bool {
	for capture := range a.replyCaptures {
		if capture.steer != nil && capture.receipt.InputID == "" && !capture.earlyOverflow {
			return true
		}
	}
	return false
}

// deferSteerEvent holds writes from the first possible early echo. The reader
// keeps accepting RPC replies, so a delayed acknowledgment cannot block itself.
func (a *Agent) deferSteerEvent(raw []byte, event copilotEvent) bool {
	if len(a.deferredSteerEvents) == 0 {
		if event.Type != contracts.CopilotEventUserMessage || event.Ephemeral || event.ID == "" {
			return false
		}
		if event.AgentID == "" && !a.awaitsSteerAcknowledgment() {
			return false
		}
		if event.AgentID != "" && !a.awaitsChildAcknowledgment(event.AgentID) {
			return false
		}
	}
	size := len(raw) + len(event.Data)
	if len(a.deferredSteerEvents) >= maxCopilotEarlyReplyFrames || size > maxCopilotDeferredSteerBytes-a.deferredSteerBytes {
		a.finishDeferredSteerEvents()
		return false
	}
	a.deferredSteerEvents = append(a.deferredSteerEvents, copilotDeferredSteerEvent{raw: bytes.Clone(raw), event: event})
	a.deferredSteerBytes += size
	return true
}

func (a *Agent) flushDeferredSteerEvents() {
	if len(a.deferredSteerEvents) == 0 || a.awaitsSteerAcknowledgment() || a.awaitsAnyChildAcknowledgment() {
		return
	}
	pending := a.deferredSteerEvents
	a.deferredSteerEvents = nil
	a.deferredSteerBytes = 0
	for _, event := range pending {
		a.applyNativeOutput(event.raw, event.event)
	}
}

// finishDeferredSteerEvents releases the transcript when the reader cannot
// retain more or the connection closes. An unresolved steer loses its proof.
func (a *Agent) finishDeferredSteerEvents() {
	for capture := range a.replyCaptures {
		if capture.steer != nil && capture.receipt.InputID == "" {
			capture.earlyOverflow = true
		}
	}
	for _, child := range a.children {
		for _, admission := range child.admissions {
			if !admission.acked {
				admission.earlyOverflow = true
			}
		}
	}
	a.flushDeferredSteerEvents()
}

func reportCopilotSteer(sink agent.ProviderServices, steer *copilotQueuedSteer, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt, msg string, attrs ...any) error {
	resolution := steer.resolution
	resolution.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		resolution.Receipt = receipt
	}
	err := agent.RetainSteerResolution(sink, resolution)
	if !agent.OutputRetained(err) {
		slog.Warn(msg, append(attrs, "error", err)...)
	}
	return err
}

func (a *Agent) reportNativeSteer(capture *copilotReplyCapture, outcome agent.SteerOutcome) {
	_ = reportCopilotSteer(a.sink, capture.steer, outcome, capture.receipt, "Retain Copilot steer outcome")
}

type copilotSteerWithdrawal struct {
	capture           *copilotReplyCapture
	messageID, prompt string
}

func (a *Agent) pendingNativeSteerWithdrawals() []copilotSteerWithdrawal {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	var pending []copilotSteerWithdrawal
	for _, capture := range a.orderedReplyCaptures() {
		if capture.steer != nil && !capture.steer.echoed && capture.receipt.InputID != "" {
			pending = append(pending, copilotSteerWithdrawal{
				capture: capture, messageID: capture.receipt.InputID, prompt: capture.steer.prompt,
			})
		}
	}
	return pending
}

func (a *Agent) dropUnreadNativeSteer(capture *copilotReplyCapture) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if _, active := a.replyCaptures[capture]; active && !capture.steer.echoed {
		delete(a.replyCaptures, capture)
		a.reportNativeSteer(capture, agent.SteerOutcomeDropped)
	}
}

// dropUnreadNativeSteers reports pending inputs of a disposed session.
// The caller holds outputMu.
func (a *Agent) dropUnreadNativeSteers() {
	for _, capture := range a.orderedReplyCaptures() {
		if capture.steer != nil && !capture.steer.echoed {
			delete(a.replyCaptures, capture)
			a.reportNativeSteer(capture, agent.SteerOutcomeDropped)
		}
	}
}

func copilotReplyMessageKey(eventID string) string {
	return "copilot-reply:" + eventID
}

func (frame copilotReplyFrame) identifiesInput(messageID string) bool {
	return frame.root && !frame.ephemeral && frame.kind == contracts.CopilotEventUserMessage &&
		frame.id != "" && frame.fields.MessageID == messageID && messageID != "" && frame.fields.InteractionID != ""
}

// observeNativeReply runs after the event's transcript row or control card
// is published. outputMu also serializes it against RPC acknowledgements.
func (a *Agent) observeNativeReply(raw []byte, event copilotEvent, controlKey string) {
	if event.AgentID != "" {
		a.observeChildReply(raw, event, controlKey)
	}
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
			if copilotControlSpecForRequest(frame.kind) != nil {
				if control, owner := a.nativeReplyControl(raw, controlKey); owner == "" {
					frame.control = control
				}
			}
		}
	}
	captures := a.orderedReplyCaptures()
	if frame.root && frame.kind == contracts.CopilotEventUserMessage {
		for _, capture := range captures {
			if capture.steer != nil && !capture.steer.echoed && frame.identifiesInput(capture.receipt.InputID) {
				frame.steerEcho = true
				break
			}
		}
	}
	if frame.control.RequestID != "" {
		var acknowledged []*copilotReplyCapture
		for _, capture := range captures {
			if capture.receipt.InputID != "" {
				acknowledged = append(acknowledged, capture)
			}
		}
		a.bindNativeReplyControl(frame, acknowledged)
	}
	for _, capture := range captures {
		if capture.receipt.InputID != "" {
			a.advanceNativeReplyCapture(capture, frame)
		} else if len(capture.early) == maxCopilotEarlyReplyFrames {
			capture.earlyOverflow = true
		} else if !capture.earlyOverflow {
			capture.early = append(capture.early, frame)
		}
	}
}

func (a *Agent) bindNativeReplyControl(frame copilotReplyFrame, captures []*copilotReplyCapture) {
	if !frame.root || frame.control.RequestID == "" {
		return
	}
	for _, capture := range captures {
		if capture.parent != "" && frame.parentID == capture.parent && capture.iteration != "" && !capture.iterationEnded {
			frame.control.InputIDs = append(frame.control.InputIDs, capture.receipt.InputID)
		}
	}
	if len(frame.control.InputIDs) == 0 {
		return
	}
	slices.Sort(frame.control.InputIDs)
	if err := a.sink.SetNativeControlInputs(frame.control); !agent.OutputRetained(err) {
		slog.Error("Bind Copilot control to native input", "error", err)
	}
}

func (a *Agent) nativeReplyControl(raw []byte, controlKey string) (agent.NativeControlInputs, string) {
	key := controlKey
	if key == "" {
		var ok bool
		key, ok = (copilotProvider{}).ControlRequestKey(raw, a.currentNativeSessionID())
		if !ok {
			return agent.NativeControlInputs{}, ""
		}
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if pending := a.controls[key]; pending != nil && pending.published {
		return agent.NativeControlInputs{SessionID: pending.sessionID, RequestID: key, InstanceID: pending.instanceID}, pending.nativeAgentID
	}
	return agent.NativeControlInputs{}, ""
}

func (a *Agent) advanceNativeReplyCapture(capture *copilotReplyCapture, frame copilotReplyFrame) {
	fields := frame.fields
	if capture.parent == "" {
		if frame.identifiesInput(capture.receipt.InputID) {
			capture.parent = frame.id
			capture.interaction = fields.InteractionID
			if capture.steer != nil {
				capture.steer.echoed = true
				a.reportNativeSteer(capture, agent.SteerOutcomeEchoed)
			}
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
	switch frame.kind {
	case contracts.CopilotEventUserMessage:
		if linked && frame.steerEcho && fields.InteractionID != capture.interaction {
			capture.interaction = fields.InteractionID
			capture.iteration, capture.iterationEnded = "", false
			return
		}
		a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	case contracts.CopilotEventAssistantTurnStart:
		if fields.InteractionID != capture.interaction || fields.TurnID == "" || frame.id == "" {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
			return
		}
		capture.iteration, capture.iterationEnded = fields.TurnID, false
		capture.parent = frame.id
	case contracts.CopilotEventAssistantMessage:
		// An immediate steer starts a new interaction, while Copilot still
		// attributes its assistant messages to the input that started the run.
		if capture.originatingInput == "" && fields.InteractionID == capture.interaction {
			capture.originatingInput = fields.OriginatingMessageID
		}
		if capture.originatingInput == "" || fields.OriginatingMessageID != capture.originatingInput ||
			(fields.InteractionID != "" && fields.InteractionID != capture.interaction) ||
			(capture.steer != nil && fields.InteractionID == "") || fields.TurnID == "" ||
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
		if fields.OriginatingMessageID != "" && (fields.OriginatingMessageID == capture.receipt.InputID ||
			linked && fields.OriginatingMessageID == capture.originatingInput) {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
		}
	case copilotAutopilotStopped:
		if fields.RequestID != "" && (fields.RequestID == capture.receipt.InputID ||
			linked && capture.iterationEnded && fields.RequestID == capture.originatingInput) {
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
	if capture.steer != nil && !capture.steer.echoed {
		a.reportNativeSteer(capture, agent.SteerOutcomeUncertain)
		return
	}
	receipt := capture.receipt
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
			SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome,
			Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: copilotReplyInputOrdinal}},
			Items:  capture.items,
		})
		if agent.OutputRetained(err) {
			return
		}
		slog.Error("Persist exact Copilot reply", "error", err)
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("Record Copilot reply outcome", "error", err)
	}
}

// discardNativeReplyCaptures ends unfinished waits for the retiring session.
// The caller holds outputMu.
func (a *Agent) discardNativeReplyCaptures() {
	for _, capture := range a.orderedReplyCaptures() {
		if capture.receipt.InputID != "" {
			a.endNativeReplyCapture(capture, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
	}
}
