package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode steering, through the command inbox.
//
// `session/send` refuses an input while a prompt runs, but the command inbox
// (`v4/command`, see rpc.go) takes a `sendText` with the `guide` delivery: the
// runtime queues the text for the running turn and injects it at the next
// boundary, after a tool result or after the model's answer, where the turn
// continues. The model reads it as the user's own message ("The user sent a
// new message while you were working"). Probed live against ZCode 3.16.3.
//
// The events of a steer repeat its command id. turn.steerQueued states it as
// `inputId`, with the id of the queued input (`pendingInputId`), and
// turn.steerDrained states it in `queryIds` once the model read the text. A
// steer-discard update addresses the queued input: `promoted` moved it into a
// turn of its own, and any other reason removed it unread.
//
// ZCode keeps a queued steer across the end of its turn: an interrupt drains it
// into the conversation, where no model request reads it, and a failed turn
// leaves it queued. So LeapMux takes each unresolved steer back
// (`deleteQueueItem`) before an interrupt and after a turn end, and the queue
// then runs it as the next turn.

var (
	_ agent.InputSteerer      = (*Agent)(nil)
	_ agent.SteerEchoReporter = (*Agent)(nil)
	_ agent.SteerSurvivor     = (*Agent)(nil)
)

const (
	// zcodeDeliveryGuide asks the runtime to inject the input into the running turn.
	zcodeDeliveryGuide = "guide"
	// zcodeDeliveryStartNow is the delivery the inbox admitted when no turn
	// ran: the input started a turn of its own.
	zcodeDeliveryStartNow = "startNow"
	// zcodeCommandResultInputAccepted is the result of an admitted input.
	zcodeCommandResultInputAccepted = "inputAccepted"
	// zcodeSteerInputMaxBytes is the runtime's own steer limit
	// (`input_too_large` above 2e5, read from the bundle).
	zcodeSteerInputMaxBytes = 200000
	// The prefixes of the command ids that LeapMux mints.
	zcodeSteerCommandPrefix    = "leapmux-steer-"
	zcodeWithdrawCommandPrefix = "leapmux-withdraw-"
)

// zcodeSendTextPayload is the payload of a sendText command.
type zcodeSendTextPayload struct {
	Text              string `json:"text"`
	RequestedDelivery string `json:"requestedDelivery"`
}

// zcodeDeleteQueueItemPayload is the payload of a deleteQueueItem command.
type zcodeDeleteQueueItemPayload struct {
	QueueItemID string `json:"queueItemId"`
}

// zcodeSteer is one steer that ZCode accepted and has not resolved.
type zcodeSteer struct {
	ticket providerkit.SteerTicket
	// inputID is the input id that the acknowledgement stated. ZCode uses the
	// command id, and a turn that runs the input states it.
	inputID string
	// pendingInputID is the id of the queued input, from turn.steerQueued: the
	// queue item a withdrawal deletes and the key of a discard update.
	pendingInputID string
}

// SupportsSteering reports whether the app-server takes a steer. It is true
// until the app-server refuses the command inbox as an unknown method.
func (a *Agent) SupportsSteering() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return !a.steeringUnsupported
}

// SteerRowAtEcho keeps the queue's row until ZCode proves the delivery: the
// app-server never repeats a steered input into its own stream, so the row
// lands when the model reads the steer.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd keeps a steer pending across a turn end. ZCode keeps it
// queued there, and the agent resolves it: an echo when a turn of its own runs
// it, or a drop once a withdrawal takes it back.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

// SteerInput sends the input into the running turn.
//
// A steer carries text only: the inbox takes uploaded attachment references,
// not the inline data that session/send carries. Text attachments inline into
// the text, as a send inlines them, and anything else goes in the next turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	var blocks []string
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != agent.AttachmentKindText {
			return fmt.Errorf("%w: a ZCode steer carries text only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
		blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
	}
	text := zcodeJoinPrompt(content, blocks)
	if len(text) > zcodeSteerInputMaxBytes {
		return fmt.Errorf("%w: the input is %d bytes, and ZCode steers at most %d, so it goes in the next turn",
			agent.ErrSteeringUnsupported, len(text), zcodeSteerInputMaxBytes)
	}

	a.steerMu.Lock()
	defer a.steerMu.Unlock()

	a.Mu.Lock()
	switch {
	case a.StoppedLocked():
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	case a.steeringUnsupported:
		a.Mu.Unlock()
		return fmt.Errorf("%w: the ZCode app-server has no command inbox", agent.ErrSteeringUnsupported)
	case !a.turnActive:
		// A compaction alone is not a turn to steer: its summary query is not
		// the user's conversation, and the queued input takes the next turn.
		a.Mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	sessionID := a.sessionID
	commandID := zcodeSteerCommandPrefix + id.Short()
	// The steer is registered before the command, because its events can
	// arrive before the acknowledgement.
	steer := &zcodeSteer{ticket: a.steerLedger.Add(commandID), inputID: commandID}
	if a.steers == nil {
		a.steers = map[string]*zcodeSteer{}
	}
	a.steers[commandID] = steer
	a.Mu.Unlock()

	ack, err := a.sendZCodeCommand(zcodeCommandEnvelope{
		CommandID: commandID,
		SessionID: sessionID,
		Type:      zcodeCommandSendText,
		Payload:   zcodeSendTextPayload{Text: text, RequestedDelivery: zcodeDeliveryGuide},
	})
	if err != nil {
		a.forgetZCodeSteer(commandID)
		if zcodeIsMethodNotFound(err) {
			a.markZCodeSteeringUnsupported()
			return fmt.Errorf("%w: %v", agent.ErrSteeringUnsupported, err)
		}
		return classifyZCodeDeliveryError(MethodCommand, err)
	}
	if ack.Status != zcodeCommandAccepted || ack.Result == nil || ack.Result.Type != zcodeCommandResultInputAccepted {
		a.forgetZCodeSteer(commandID)
		return fmt.Errorf("ZCode did not accept the steer: %s", zcodeCommandRefusal(ack))
	}
	if ack.Result.InputID != "" {
		a.Mu.Lock()
		steer.inputID = ack.Result.InputID
		a.Mu.Unlock()
	}
	if ack.Result.Delivery == zcodeDeliveryStartNow {
		// The turn ended before ZCode admitted the input, so the input started a
		// turn of its own, whose prompt the model reads.
		a.resolveZCodeSteers(func(candidate string, _ *zcodeSteer) bool { return candidate == commandID }, agent.SteerOutcomeEchoed)
	}
	return nil
}

// forgetZCodeSteer removes a steer whose command failed. The queue already
// knows the failure from SteerInput's error.
func (a *Agent) forgetZCodeSteer(commandID string) {
	a.Mu.Lock()
	steer, ok := a.steers[commandID]
	delete(a.steers, commandID)
	a.Mu.Unlock()
	if ok {
		a.steerLedger.Forget(steer.ticket)
		a.steerLedger.Settle(a.sink)
	}
}

// markZCodeSteeringUnsupported records that the app-server has no command
// inbox, and republishes the two states that read it: the turn state, whose
// steerable flag the queue follows, and the agent status, whose
// SupportsSteering the browser's Steer control follows.
func (a *Agent) markZCodeSteeringUnsupported() {
	a.Mu.Lock()
	known := a.steeringUnsupported
	a.steeringUnsupported = true
	sessionID := a.sessionID
	a.Mu.Unlock()
	if known {
		return
	}
	a.PublishTurnActive()
	if sessionID != "" {
		a.sink.BroadcastStatusActive(sessionID)
	}
}

// resolveZCodeSteers resolves every unresolved steer that match selects with
// outcome, settles the ledger, and reports whether one matched.
func (a *Agent) resolveZCodeSteers(match func(commandID string, steer *zcodeSteer) bool, outcome agent.SteerOutcome) bool {
	a.Mu.Lock()
	var tickets []providerkit.SteerTicket
	for commandID, steer := range a.steers {
		if match(commandID, steer) {
			tickets = append(tickets, steer.ticket)
			delete(a.steers, commandID)
		}
	}
	a.Mu.Unlock()
	if len(tickets) == 0 {
		return false
	}
	for _, ticket := range tickets {
		switch outcome {
		case agent.SteerOutcomeEchoed:
			a.steerLedger.Echo(ticket)
		case agent.SteerOutcomeDropped:
			a.steerLedger.Drop(ticket)
		default:
			a.steerLedger.Uncertain(ticket)
		}
	}
	a.steerLedger.Settle(a.sink)
	return true
}

// zcodeSteerEventPayload holds the fields of the three steer events that
// identify a steer.
type zcodeSteerEventPayload struct {
	// InputID is the command id of the input that turn.steerQueued queued.
	InputID string `json:"inputId"`
	// PendingInputID is the queued input of turn.steerQueued and
	// turn.steerDeliveryChanged.
	PendingInputID string `json:"pendingInputId"`
	// QueryIDs and PendingInputIDs are the inputs that turn.steerDrained
	// injected into the turn.
	QueryIDs        []string `json:"queryIds"`
	PendingInputIDs []string `json:"pendingInputIds"`
	// Intent states the command of a turn.steerDeliveryChanged input.
	Intent struct {
		SourceCommandID string `json:"sourceCommandId"`
	} `json:"intent"`
}

// observeZCodeSteerEvent applies one steer event, and reports whether it
// belongs to a steer of LeapMux's. Such an event writes no row: the queue's row
// or requeue is its visible outcome. A steer event of the runtime's own (a
// background task notification or a subagent message) keeps its notification.
//
//   - turn.steerQueued records the queued input of the steer.
//   - turn.steerDrained states the model read the steer, which echoes it.
//   - turn.steerDeliveryChanged moved the steer to the ordinary queue. It stays
//     pending: a turn of its own echoes it, or a withdrawal takes it back.
func (a *Agent) observeZCodeSteerEvent(event zcodeEventEnvelope) bool {
	var payload zcodeSteerEventPayload
	if json.Unmarshal(event.Payload, &payload) != nil {
		return false
	}
	switch event.Type {
	case contracts.ZCodeEventTurnSteerQueued:
		a.Mu.Lock()
		defer a.Mu.Unlock()
		steer, ours := a.steers[payload.InputID]
		if ours && payload.PendingInputID != "" {
			steer.pendingInputID = payload.PendingInputID
		}
		return ours
	case contracts.ZCodeEventTurnSteerDrained:
		commands := zcodeStringSet(payload.QueryIDs)
		inputs := zcodeStringSet(payload.PendingInputIDs)
		return a.resolveZCodeSteers(func(commandID string, steer *zcodeSteer) bool {
			return commands[commandID] || commands[steer.inputID] || (steer.pendingInputID != "" && inputs[steer.pendingInputID])
		}, agent.SteerOutcomeEchoed)
	case contracts.ZCodeEventTurnSteerDeliveryChanged:
		a.Mu.Lock()
		defer a.Mu.Unlock()
		for commandID, steer := range a.steers {
			if commandID == payload.Intent.SourceCommandID || (steer.pendingInputID != "" && steer.pendingInputID == payload.PendingInputID) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// observeZCodeSteerTurnStart echoes the steer that a turn runs as its prompt:
// one that ZCode moved to the ordinary queue and then ran by itself.
func (a *Agent) observeZCodeSteerTurnStart(inputID string) {
	if inputID == "" {
		return
	}
	a.resolveZCodeSteers(func(commandID string, steer *zcodeSteer) bool {
		return commandID == inputID || steer.inputID == inputID
	}, agent.SteerOutcomeEchoed)
}

// observeZCodeSteerDiscard settles the steers that ZCode removed from its
// queue, and reports whether the update belongs to LeapMux's own steers.
//
// `promoted` is not a loss: ZCode moved the input into a turn of its own, so
// the model reads it there, and the steer counts as echoed. Any other reason
// removed the input unread, and the queue runs it again as the next turn. A
// withdrawal that already settled its steer still owns the update.
func (a *Agent) observeZCodeSteerDiscard(payload zcodeSessionUpdatedPayload) bool {
	var pendingIDs []string
	if json.Unmarshal(payload.PendingInputIDs, &pendingIDs) != nil || len(pendingIDs) == 0 {
		return false
	}
	discarded := zcodeStringSet(pendingIDs)
	a.Mu.Lock()
	withdrawn := false
	for _, pendingID := range pendingIDs {
		if _, ok := a.withdrawnSteerInputs[pendingID]; ok {
			delete(a.withdrawnSteerInputs, pendingID)
			withdrawn = true
		}
	}
	a.Mu.Unlock()
	outcome := agent.SteerOutcomeDropped
	if reason, _ := providerkit.RawString(payload.Reason); reason == zcodeSteerDiscardReasonPromoted {
		outcome = agent.SteerOutcomeEchoed
	}
	resolved := a.resolveZCodeSteers(func(_ string, steer *zcodeSteer) bool {
		return steer.pendingInputID != "" && discarded[steer.pendingInputID]
	}, outcome)
	return resolved || withdrawn
}

// zcodeStringSet is the set of values.
func zcodeStringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// hasPendingZCodeSteers reports whether a steer awaits its outcome.
func (a *Agent) hasPendingZCodeSteers() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return len(a.steers) > 0
}

// withdrawZCodeSteersLocked takes every unresolved steer back from ZCode's
// queue. A deleted steer is dropped, so the queue runs it as the next turn.
// Any other answer leaves its delivery unknown: an input that the model read
// was echoed by its drain event, which precedes the answer on the wire, so the
// remaining case is an input that ZCode removed without a word. A steer whose
// queued input ZCode has not stated cannot be addressed, and is uncertain too.
// The caller holds steerMu.
func (a *Agent) withdrawZCodeSteersLocked() {
	a.Mu.Lock()
	sessionID := a.sessionID
	pending := make(map[string]string, len(a.steers))
	for commandID, steer := range a.steers {
		pending[commandID] = steer.pendingInputID
	}
	a.Mu.Unlock()
	for commandID, pendingInputID := range pending {
		outcome := agent.SteerOutcomeUncertain
		if pendingInputID != "" && a.deleteZCodeQueuedInput(sessionID, pendingInputID) {
			outcome = agent.SteerOutcomeDropped
		}
		a.resolveZCodeSteers(func(candidate string, _ *zcodeSteer) bool { return candidate == commandID }, outcome)
	}
}

// deleteZCodeQueuedInput deletes one queued input and reports whether ZCode
// deleted it. The input is marked withdrawn before the command, because ZCode
// can report the discard before it acknowledges the command.
func (a *Agent) deleteZCodeQueuedInput(sessionID, pendingInputID string) bool {
	a.Mu.Lock()
	if a.withdrawnSteerInputs == nil {
		a.withdrawnSteerInputs = map[string]struct{}{}
	}
	a.withdrawnSteerInputs[pendingInputID] = struct{}{}
	a.Mu.Unlock()
	ack, err := a.sendZCodeCompareAndSetCommand(zcodeCommandEnvelope{
		CommandID: zcodeWithdrawCommandPrefix + id.Short(),
		SessionID: sessionID,
		Type:      zcodeCommandDeleteQueueItem,
		Payload:   zcodeDeleteQueueItemPayload{QueueItemID: pendingInputID},
	})
	if err == nil && ack.Status == zcodeCommandAccepted {
		return true
	}
	a.Mu.Lock()
	delete(a.withdrawnSteerInputs, pendingInputID)
	a.Mu.Unlock()
	if err != nil {
		slog.Warn("zcode could not withdraw a queued steer", "agent_id", a.AgentID(), "pending_input_id", pendingInputID, "error", err)
	} else {
		slog.Warn("zcode did not withdraw a queued steer", "agent_id", a.AgentID(), "pending_input_id", pendingInputID, "reason", zcodeCommandRefusal(ack))
	}
	return false
}

// withdrawZCodeSteersAfterTurnEnd takes back the steers that a turn left
// queued, unless another turn started in the meantime. It runs on its own
// goroutine: the turn end is dispatched on the read loop, which must keep
// reading to deliver the withdrawal's acknowledgement.
func (a *Agent) withdrawZCodeSteersAfterTurnEnd() {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.Mu.Lock()
	active := a.turnActive || a.compactionSessionID != ""
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if active || stopped {
		return
	}
	a.withdrawZCodeSteersLocked()
}

// dropZCodeSteersOfReplacedSession drops the steers of a session that a new
// one replaced: no turn of the old session runs again, so the queue runs them
// in the new one.
func (a *Agent) dropZCodeSteersOfReplacedSession() {
	a.Mu.Lock()
	a.withdrawnSteerInputs = nil
	a.Mu.Unlock()
	a.resolveZCodeSteers(func(string, *zcodeSteer) bool { return true }, agent.SteerOutcomeDropped)
}

// resetZCodeSteers forgets every steer when the process ends. The ledger is
// reset rather than settled: the process exit owns the queue's recovery of
// inputs still awaiting their echo.
func (a *Agent) resetZCodeSteers() {
	a.Mu.Lock()
	a.steers = nil
	a.withdrawnSteerInputs = nil
	a.Mu.Unlock()
	a.steerLedger.Reset()
}
