package copilot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// copilotChildAdmission tracks one LeapMux message sent to a background child
// through session.tasks.sendMessage until Copilot echoes that client message.
type copilotChildAdmission struct {
	receipt       agent.NativeInputReceipt
	prompt        string
	isSteer       bool
	steer         *copilotQueuedSteer
	acked         bool
	earlyOverflow bool
	unproved      bool
	startSeq      uint64
	ackSeq        uint64
}

// copilotChildReplyTurn tracks the active native interaction of a background
// child once one or more LeapMux inputs are echoed into it.
type copilotChildReplyTurn struct {
	interactionID    string
	originatingInput string
	iteration        string
	iterationEnded   bool
	ordinal          int64
	inputs           []agent.NativeReplyBoundary
	items            []agent.NativeReplyItem
	toolCallIDs      map[string]bool
	competingPrompts map[string]struct{}
	unproved         bool
}

type copilotChildReplyFields struct {
	MessageID            string `json:"messageId"`
	OriginatingMessageID string `json:"originatingMessageId"`
	InteractionID        string `json:"interactionId"`
	TurnID               string `json:"turnId"`
	Content              string `json:"content"`
	Source               string `json:"source"`
	Delivery             string `json:"delivery"`
	ToolCallID           string `json:"toolCallId"`
	PermissionRequest    struct {
		ToolCallID string `json:"toolCallId"`
	} `json:"permissionRequest"`
}

func (child *copilotNativeChild) nextSeq() uint64 {
	child.nextAdmissionSeq++
	return child.nextAdmissionSeq
}

func (a *Agent) awaitsChildAcknowledgment(agentID string) bool {
	child := a.children[agentID]
	if child == nil {
		return false
	}
	for _, admission := range child.admissions {
		if !admission.acked && !admission.earlyOverflow {
			return true
		}
	}
	return false
}

func (a *Agent) awaitsAnyChildAcknowledgment() bool {
	for _, child := range a.children {
		for _, admission := range child.admissions {
			if !admission.acked && !admission.earlyOverflow {
				return true
			}
		}
	}
	return false
}

func (a *Agent) beginChildAdmission(childKey, content string, isSteer bool, steer *copilotQueuedSteer) (*copilotNativeChild, *copilotChildAdmission, error) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	child := a.children[childKey]
	if child == nil {
		if isSteer {
			return nil, nil, agent.ErrNoActiveTurn
		}
		return nil, nil, errCopilotSubagentEnded
	}
	if !child.background {
		return nil, nil, errCopilotForegroundSubagent
	}
	if isSteer && child.idle {
		return nil, nil, agent.ErrNoActiveTurn
	}
	if !isSteer && !child.idle {
		return nil, nil, agent.ErrAgentBusy
	}
	sessionID := a.currentNativeSessionID()
	if sessionID == "" {
		return nil, nil, fmt.Errorf("the Copilot session is unavailable")
	}
	admission := &copilotChildAdmission{
		receipt:  agent.NativeInputReceipt{SessionID: sessionID, InputID: id.Generate()},
		prompt:   content,
		isSteer:  isSteer,
		steer:    steer,
		startSeq: child.nextSeq(),
	}
	child.admissions = append(child.admissions, admission)
	return child, admission, nil
}

func (a *Agent) acceptChildAdmission(child *copilotNativeChild, admission *copilotChildAdmission) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	defer a.flushDeferredSteerEvents()
	index := slices.Index(child.admissions, admission)
	if index < 0 {
		// The child ended while tasks.sendMessage was in flight.
		a.retireChildAdmission(child, admission, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	if admission.earlyOverflow || a.closing {
		child.admissions = slices.Delete(child.admissions, index, index+1)
		a.retireChildAdmission(child, admission, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	admission.acked = true
	admission.ackSeq = child.nextSeq()
}

func (a *Agent) discardChildAdmission(child *copilotNativeChild, admission *copilotChildAdmission, uncertain bool) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	defer a.flushDeferredSteerEvents()
	if index := slices.Index(child.admissions, admission); index >= 0 {
		child.admissions = slices.Delete(child.admissions, index, index+1)
	}
	if uncertain {
		if child.uncertainPrompts == nil {
			child.uncertainPrompts = make(map[string]int)
		}
		child.uncertainPrompts[admission.prompt]++
	}
}

// observeChildReply tracks child turns, echoes, replies, and control bindings.
// The caller holds outputMu.
func (a *Agent) observeChildReply(raw []byte, event copilotEvent) {
	child := a.children[event.AgentID]
	if child == nil {
		return
	}
	var fields copilotChildReplyFields
	switch event.Type {
	case contracts.CopilotEventAssistantTurnStart:
		if json.Unmarshal(event.Data, &fields) != nil {
			return
		}
		child.liveInteraction = fields.InteractionID
		child.liveIteration = fields.TurnID
		child.liveIterationEnded = false
		if turn := child.replyTurn; turn != nil {
			if fields.InteractionID != turn.interactionID || fields.TurnID == "" || event.ID == "" {
				a.retireChildReplyTurn(child, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
				return
			}
			turn.iteration = fields.TurnID
			turn.iterationEnded = false
		}
	case contracts.CopilotEventUserMessage:
		if event.Ephemeral || event.ID == "" || json.Unmarshal(event.Data, &fields) != nil {
			return
		}
		a.observeChildUserMessage(child, fields)
	case contracts.CopilotEventToolStarted:
		if turn := child.replyTurn; turn != nil && turn.iteration != "" && !turn.iterationEnded {
			if json.Unmarshal(event.Data, &fields) == nil && fields.ToolCallID != "" &&
				(fields.TurnID == "" || fields.TurnID == turn.iteration) {
				if turn.toolCallIDs == nil {
					turn.toolCallIDs = make(map[string]bool)
				}
				turn.toolCallIDs[fields.ToolCallID] = true
			}
		}
	case contracts.CopilotEventAssistantMessage:
		if turn := child.replyTurn; turn != nil {
			if json.Unmarshal(event.Data, &fields) != nil {
				turn.unproved = true
				return
			}
			a.observeChildAssistantMessage(child, turn, event.ID, fields)
		}
	case contracts.CopilotEventAssistantTurnEnd:
		if json.Unmarshal(event.Data, &fields) != nil {
			return
		}
		if child.liveIteration != "" && fields.TurnID == child.liveIteration {
			child.liveIterationEnded = true
		}
		if turn := child.replyTurn; turn != nil && turn.iteration != "" && fields.TurnID == turn.iteration && event.ID != "" {
			turn.iterationEnded = true
		}
	default:
		for _, spec := range copilotControlSpecs {
			if spec.requestEvent == event.Type {
				_ = json.Unmarshal(event.Data, &fields)
				a.bindChildReplyControl(child, raw, event.Type, fields)
				break
			}
		}
	}
}

func (a *Agent) observeChildUserMessage(child *copilotNativeChild, fields copilotChildReplyFields) {
	// Inter-agent messages (task spawn and parent write_agent) always set source
	// to "agent-<sender>". Client sends pass fromAgentId:"" so source is empty.
	if fields.Source != "" {
		if turn := child.replyTurn; turn != nil && fields.InteractionID != "" && fields.InteractionID != turn.interactionID {
			a.retireChildReplyTurn(child, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
		return
	}
	if count := child.uncertainPrompts[fields.Content]; count > 0 {
		if count == 1 {
			delete(child.uncertainPrompts, fields.Content)
		} else {
			child.uncertainPrompts[fields.Content] = count - 1
		}
		hasAcked := false
		kept := child.admissions[:0]
		for _, admission := range child.admissions {
			if admission.acked && admission.prompt == fields.Content {
				hasAcked = true
				a.retireChildAdmission(child, admission, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
				continue
			}
			kept = append(kept, admission)
		}
		child.admissions = kept
		if hasAcked && child.replyTurn != nil {
			child.replyTurn.unproved = true
		}
		return
	}
	matchedIndex := -1
	for index, admission := range child.admissions {
		if admission.acked && admission.prompt == fields.Content {
			matchedIndex = index
			break
		}
	}
	if matchedIndex < 0 {
		if child.replyTurn != nil {
			child.replyTurn.unproved = true
		}
		return
	}
	matched := child.admissions[matchedIndex]
	overlapped := false
	for index, other := range child.admissions {
		if index != matchedIndex && other.acked && other.prompt == fields.Content && other.startSeq < matched.ackSeq {
			overlapped = true
			break
		}
	}
	child.admissions = slices.Delete(child.admissions, matchedIndex, matchedIndex+1)
	if matched.isSteer && matched.steer == nil {
		if child.replyTurn != nil && fields.InteractionID != "" && fields.InteractionID != child.replyTurn.interactionID {
			a.retireChildReplyTurn(child, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
		return
	}
	if matched.unproved {
		a.retireChildAdmission(child, matched, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	if fields.InteractionID == "" || fields.MessageID == "" {
		a.retireChildAdmission(child, matched, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	if child.replyTurn != nil && child.replyTurn.interactionID != fields.InteractionID {
		a.retireChildReplyTurn(child, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	turn := child.replyTurn
	if turn == nil {
		turn = &copilotChildReplyTurn{
			interactionID: fields.InteractionID,
			ordinal:       copilotReplyInputOrdinal,
		}
		if fields.Delivery == "idle" {
			turn.originatingInput = fields.MessageID
		} else if fields.TurnID != "" && child.liveInteraction == fields.InteractionID &&
			child.liveIteration == fields.TurnID && !child.liveIterationEnded {
			// Steering echoes arrive after assistant.turn_start for the receiving iteration.
			turn.iteration = fields.TurnID
		}
		child.replyTurn = turn
	} else if fields.Delivery == "idle" {
		turn.unproved = true
	}
	receipt := matched.receipt
	receipt.TurnID = turn.interactionID
	var err error
	if matched.steer != nil {
		err = a.reportChildSteer(child, matched.steer, agent.SteerOutcomeEchoed, receipt)
	} else {
		err = child.sink.BindNativeInputTurn(receipt, turn.interactionID)
		if !agent.OutputRetained(err) {
			slog.Error("Bind Copilot child input to native turn", "agent_id", a.AgentID(), "child_agent_id", child.workerAgentID, "error", err)
		}
	}
	if !agent.OutputRetained(err) {
		a.retireChildInput(child, receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	turn.inputs = append(turn.inputs, agent.NativeReplyBoundary{
		InputID: receipt.InputID, AfterOrdinal: turn.ordinal,
	})
	if overlapped {
		if turn.competingPrompts == nil {
			turn.competingPrompts = make(map[string]struct{})
		}
		turn.competingPrompts[fields.Content] = struct{}{}
	}
}

func (a *Agent) observeChildAssistantMessage(child *copilotNativeChild, turn *copilotChildReplyTurn, eventID string, fields copilotChildReplyFields) {
	if len(turn.competingPrompts) > 0 {
		for _, pending := range child.admissions {
			if _, competing := turn.competingPrompts[pending.prompt]; competing && pending.acked {
				turn.unproved = true
				pending.unproved = true
			}
		}
		clear(turn.competingPrompts)
	}
	if turn.originatingInput == "" && fields.InteractionID == turn.interactionID {
		turn.originatingInput = fields.OriginatingMessageID
	}
	if turn.originatingInput == "" || fields.OriginatingMessageID != turn.originatingInput ||
		(fields.InteractionID != "" && fields.InteractionID != turn.interactionID) ||
		fields.TurnID == "" || fields.TurnID != turn.iteration || turn.iterationEnded || eventID == "" {
		turn.unproved = true
		return
	}
	turn.ordinal++
	turn.items = append(turn.items, agent.NativeReplyItem{
		Ordinal: turn.ordinal, MessageKey: copilotReplyMessageKey(eventID),
	})
}

func (a *Agent) bindChildReplyControl(child *copilotNativeChild, raw []byte, eventType string, fields copilotChildReplyFields) {
	turn := child.replyTurn
	if turn == nil || turn.unproved || turn.iteration == "" || turn.iterationEnded || len(turn.inputs) == 0 {
		return
	}
	if eventType == contracts.CopilotEventPermissionRequested {
		toolCallID := fields.PermissionRequest.ToolCallID
		if toolCallID != "" && !turn.toolCallIDs[toolCallID] {
			return
		}
	}
	control, owner := a.nativeReplyControl(raw)
	if control.RequestID == "" || owner != child.nativeAgentID {
		return
	}
	control.InputIDs = make([]string, 0, len(turn.inputs))
	for _, input := range turn.inputs {
		control.InputIDs = append(control.InputIDs, input.InputID)
	}
	slices.Sort(control.InputIDs)
	if err := child.sink.SetNativeControlInputs(control); !agent.OutputRetained(err) {
		slog.Error("Bind Copilot child control to native input", "agent_id", a.AgentID(), "child_agent_id", child.workerAgentID, "error", err)
	}
}

func (a *Agent) completeChildReply(child *copilotNativeChild) {
	turn := child.replyTurn
	if turn == nil {
		return
	}
	child.replyTurn = nil
	if len(turn.inputs) == 0 {
		return
	}
	if turn.unproved || !turn.iterationEnded {
		a.retireChildTurnInputs(child, turn, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	err := child.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: a.currentNativeSessionID(),
		TurnID:    turn.interactionID,
		Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:    turn.inputs,
		Items:     turn.items,
	})
	if err == nil || agent.OutputRetained(err) {
		return
	}
	slog.Error("Persist exact Copilot child reply", "agent_id", a.AgentID(), "child_agent_id", child.workerAgentID, "error", err)
	a.retireChildTurnInputs(child, turn, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
}

func (a *Agent) retireChildReplyTurn(child *copilotNativeChild, outcome leapmuxv1.AgentInputTurnOutcome) {
	turn := child.replyTurn
	if turn == nil {
		return
	}
	child.replyTurn = nil
	a.retireChildTurnInputs(child, turn, outcome)
}

func (a *Agent) retireChildReply(child *copilotNativeChild, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.retireChildReplyTurn(child, outcome)
	a.retireChildAdmissions(child, outcome)
}

func (a *Agent) retireChildAdmissions(child *copilotNativeChild, outcome leapmuxv1.AgentInputTurnOutcome) {
	pending := child.admissions
	child.admissions = nil
	for _, admission := range pending {
		a.retireChildAdmission(child, admission, outcome)
	}
}

func (a *Agent) retireChildAdmission(child *copilotNativeChild, admission *copilotChildAdmission, outcome leapmuxv1.AgentInputTurnOutcome) {
	if admission.isSteer {
		if admission.steer != nil {
			_ = a.reportChildSteer(child, admission.steer, agent.SteerOutcomeUncertain, agent.NativeInputReceipt{})
		}
		return
	}
	if admission.acked {
		a.retireChildInput(child, admission.receipt, outcome)
	}
}

func (a *Agent) retireChildTurnInputs(child *copilotNativeChild, turn *copilotChildReplyTurn, outcome leapmuxv1.AgentInputTurnOutcome) {
	sessionID := a.currentNativeSessionID()
	for _, input := range turn.inputs {
		a.retireChildInput(child, agent.NativeInputReceipt{
			SessionID: sessionID, InputID: input.InputID, TurnID: turn.interactionID,
		}, outcome)
	}
}

func (a *Agent) retireChildInput(child *copilotNativeChild, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if receipt.SessionID == "" || receipt.InputID == "" {
		return
	}
	if err := child.sink.RetireNativeInputTurn(receipt, outcome); err != nil && !agent.OutputRetained(err) {
		slog.Error("Record Copilot child reply outcome", "agent_id", a.AgentID(), "child_agent_id", child.workerAgentID, "error", err)
	}
}

func (a *Agent) reportChildSteer(child *copilotNativeChild, steer *copilotQueuedSteer, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt) error {
	resolution := steer.resolution
	resolution.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		resolution.Receipt = receipt
	}
	err := agent.RetainSteerResolution(child.sink, resolution)
	if !agent.OutputRetained(err) {
		slog.Warn("Retain Copilot child steer outcome", "agent_id", a.AgentID(), "child_agent_id", child.workerAgentID, "error", err)
	}
	return err
}
