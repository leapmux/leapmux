package zcode

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
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
	// zcodeDeliveryStartNow is the inbox admission that starts a separate turn.
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
	queueInputID string
	attemptID    string
	order        uint64
	// inputID is the input id that the acknowledgement stated. ZCode uses the
	// command id, and a turn that runs the input states it.
	inputID string
	// pendingInputID is the id of the queued input, from turn.steerQueued: the
	// queue item a withdrawal deletes and the key of a discard update.
	pendingInputID string
	// awaitingTurnStart records admission to a separate turn. The previous
	// turn's cleanup leaves this input to its native start event.
	awaitingTurnStart bool
}

// SupportsSteering reports the current app-server's command-inbox capability.
func (a *Agent) SupportsSteering() bool { return true }

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
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	content, attachments := request.Content, request.Attachments
	var blocks []string
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return agent.NativeInputReceipt{}, fmt.Errorf("%w: a ZCode steer carries text only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
		blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
	}
	text := zcodeJoinPrompt(content, blocks)
	if len(text) > zcodeSteerInputMaxBytes {
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: the input is %d bytes, and ZCode steers at most %d, so it goes in the next turn",
			agent.ErrSteeringUnsupported, len(text), zcodeSteerInputMaxBytes)
	}

	a.steerMu.Lock()
	defer a.steerMu.Unlock()

	a.Mu.Lock()
	switch {
	case a.StoppedLocked():
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, fmt.Errorf("agent is stopped")
	case !a.turnActive:
		// A compaction alone is not a turn to steer: its summary query is not
		// the user's conversation, and the queued input takes the next turn.
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	sessionID := a.sessionID
	commandID := zcodeSteerCommandPrefix + id.Short()
	// The steer is registered before the command, because its events can
	// arrive before the acknowledgement. Steered control replies carry an empty
	// AttemptID and are accepted immediately without steer resolution tracking.
	var steer *zcodeSteer
	if request.AttemptID != "" {
		a.nextSteerOrder++
		steer = &zcodeSteer{queueInputID: request.InputID, attemptID: request.AttemptID, order: a.nextSteerOrder, inputID: commandID}
		if a.steers == nil {
			a.steers = map[string]*zcodeSteer{}
		}
		a.steers[commandID] = steer
	}
	a.Mu.Unlock()

	ack, err := a.sendZCodeCommand(zcodeCommandEnvelope{
		CommandID: commandID,
		SessionID: sessionID,
		Type:      zcodeCommandSendText,
		Payload:   zcodeSendTextPayload{Text: text, RequestedDelivery: zcodeDeliveryGuide},
	})
	if err != nil {
		if steer != nil {
			a.forgetZCodeSteer(commandID)
		}
		return agent.NativeInputReceipt{}, classifyZCodeDeliveryError(MethodCommand, err)
	}
	if ack.Status != zcodeCommandAccepted || ack.Result == nil || ack.Result.Type != zcodeCommandResultInputAccepted {
		if steer != nil {
			a.forgetZCodeSteer(commandID)
		}
		return agent.NativeInputReceipt{}, fmt.Errorf("ZCode did not accept the steer: %s", zcodeCommandRefusal(ack))
	}
	if steer == nil {
		return agent.NativeInputReceipt{}, nil
	}
	var echoedReceipt agent.NativeInputReceipt
	a.Mu.Lock()
	// The native start or drain may have already resolved this steer.
	if a.steers[commandID] == steer {
		if ack.Result.InputID != "" {
			steer.inputID = ack.Result.InputID
		}
		if turnID := a.seenInputTurns.inputs[steer.inputID]; turnID != "" {
			delete(a.steers, commandID)
			echoedReceipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: steer.inputID, TurnID: turnID}
		} else {
			steer.awaitingTurnStart = steer.awaitingTurnStart || ack.Result.Delivery == zcodeDeliveryStartNow
		}
	}
	a.Mu.Unlock()
	if echoedReceipt.TurnID != "" {
		a.reportZCodeSteer(steer, agent.SteerOutcomeEchoed, echoedReceipt)
	}
	return agent.NativeInputReceipt{}, nil
}

func (a *Agent) forgetZCodeSteer(commandID string) {
	a.Mu.Lock()
	delete(a.steers, commandID)
	a.Mu.Unlock()
}

// takeZCodeSteers gives one caller ownership of each matching dispatch.
func (a *Agent) takeZCodeSteers(match func(commandID string, steer *zcodeSteer) bool) []*zcodeSteer {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var matched []*zcodeSteer
	for commandID, steer := range a.steers {
		if match(commandID, steer) {
			matched = append(matched, steer)
			delete(a.steers, commandID)
		}
	}
	slices.SortFunc(matched, func(left, right *zcodeSteer) int {
		return cmp.Compare(left.order, right.order)
	})
	return matched
}

func (a *Agent) reportZCodeSteer(steer *zcodeSteer, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt) {
	err := agent.RetainSteerResolution(a.sink, agent.SteerResolution{
		InputID: steer.queueInputID, AttemptID: steer.attemptID, Outcome: outcome, Receipt: receipt,
	})
	if !agent.OutputRetained(err) {
		slog.Error("zcode record steer resolution", "agent_id", a.AgentID(), "input_id", steer.queueInputID, "error", err)
	}
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
	TargetTurnID    string   `json:"targetTurnId"`
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
		return a.observeZCodeSteerDrain(event, payload)
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

func (a *Agent) observeZCodeSteerDrain(event zcodeEventEnvelope, payload zcodeSteerEventPayload) bool {
	// The native event gives queued IDs in read order. Use command IDs when
	// the queue ID was never observed.
	positions := make(map[string]int, len(payload.PendingInputIDs)+len(payload.QueryIDs))
	for _, ids := range [][]string{payload.PendingInputIDs, payload.QueryIDs} {
		for _, nativeID := range ids {
			if nativeID != "" && positions[nativeID] == 0 {
				positions[nativeID] = len(positions) + 1
			}
		}
	}
	order := make(map[*zcodeSteer]int)
	steers := a.takeZCodeSteers(func(commandID string, steer *zcodeSteer) bool {
		for _, nativeID := range []string{steer.pendingInputID, steer.inputID, commandID} {
			if pos := positions[nativeID]; pos > 0 {
				order[steer] = pos
				return true
			}
		}
		return false
	})
	if len(steers) == 0 {
		return false
	}
	slices.SortFunc(steers, func(left, right *zcodeSteer) int { return cmp.Compare(order[left], order[right]) })
	owner := a.zcodeSteerReplyOwner(event, payload.TargetTurnID)
	// Retain the lead's buffered text before the first steer row. The reply
	// query starts at the steer row, so this order also identifies its reply.
	if owner != nil && !a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete) {
		owner = nil
	}
	for _, steer := range steers {
		var receipt agent.NativeInputReceipt
		if owner != nil {
			receipt = agent.NativeInputReceipt{SessionID: event.SessionID, InputID: steer.inputID, TurnID: owner.id}
			a.Mu.Lock()
			owner.steerReceipts = append(owner.steerReceipts, receipt)
			a.Mu.Unlock()
		}
		a.reportZCodeSteer(steer, agent.SteerOutcomeEchoed, receipt)
	}
	return true
}

func (a *Agent) zcodeSteerReplyOwner(event zcodeEventEnvelope, targetTurnID string) *zcodeTurnState {
	if targetTurnID == "" || event.SessionID == "" || event.TurnID != "" && event.TurnID != targetTurnID || event.archive || event.replayUncertain {
		return nil
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	owner := a.turns[targetTurnID]
	if owner == nil || owner.inputSessionID != event.SessionID || !owner.inputConfirmed || owner.replyUnproved ||
		owner.steerReplyUnproved || owner.canonicalRequired || owner.settled || owner.pendingNative != nil {
		return nil
	}
	return owner
}

// observeZCodeSteerTurnStart echoes the steer that a turn runs as its prompt:
// one that ZCode moved to the ordinary queue and then ran by itself.
func (a *Agent) observeZCodeSteerTurnStart(inputID string, receipt agent.NativeInputReceipt) {
	if inputID == "" {
		return
	}
	for _, steer := range a.takeZCodeSteers(func(commandID string, steer *zcodeSteer) bool {
		return commandID == inputID || steer.inputID == inputID
	}) {
		a.reportZCodeSteer(steer, agent.SteerOutcomeEchoed, receipt)
	}
}

// observeZCodeSteerDiscard settles the steers that ZCode removed from its
// queue, and reports whether the update belongs to LeapMux's own steers.
//
// `promoted` moves the input into a turn of its own. The native start
// identifies that turn and determines the steer row's position. Any other reason
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
	if reason, _ := providerkit.RawString(payload.Reason); reason == zcodeSteerDiscardReasonPromoted {
		for _, steer := range a.steers {
			if discarded[steer.pendingInputID] {
				steer.awaitingTurnStart = true
				withdrawn = true
			}
		}
		a.Mu.Unlock()
		return withdrawn
	}
	a.Mu.Unlock()
	steers := a.takeZCodeSteers(func(_ string, steer *zcodeSteer) bool {
		return steer.pendingInputID != "" && discarded[steer.pendingInputID]
	})
	for _, steer := range steers {
		a.reportZCodeSteer(steer, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})
	}
	return len(steers) > 0 || withdrawn
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

// withdrawZCodeSteersLocked takes unresolved queued steers back from ZCode's
// queue. A deleted steer is dropped, so the queue runs it as the next turn.
// Any other answer leaves its delivery unknown: an input that the model read
// was echoed by its drain event, which precedes the answer on the wire, so the
// remaining case is an input that ZCode removed without a word. A steer whose
// queued input ZCode has not stated cannot be addressed, and is uncertain too.
// The caller holds steerMu.
func (a *Agent) withdrawZCodeSteersLocked(includeAwaitingStart bool) {
	type pendingSteer struct {
		commandID      string
		pendingInputID string
		order          uint64
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	var pending []pendingSteer
	for commandID, steer := range a.steers {
		if includeAwaitingStart || !steer.awaitingTurnStart {
			pending = append(pending, pendingSteer{
				commandID:      commandID,
				pendingInputID: steer.pendingInputID,
				order:          steer.order,
			})
		}
	}
	a.Mu.Unlock()
	slices.SortFunc(pending, func(left, right pendingSteer) int {
		return cmp.Compare(left.order, right.order)
	})
	for _, entry := range pending {
		outcome := agent.SteerOutcomeUncertain
		if entry.pendingInputID != "" && a.deleteZCodeQueuedInput(sessionID, entry.pendingInputID) {
			outcome = agent.SteerOutcomeDropped
		}
		a.Mu.Lock()
		steer := a.steers[entry.commandID]
		// A native promotion can arrive during the withdrawal RPC.
		if !includeAwaitingStart && steer != nil && steer.awaitingTurnStart {
			steer = nil
		}
		if steer != nil {
			delete(a.steers, entry.commandID)
		}
		a.Mu.Unlock()
		if steer != nil {
			a.reportZCodeSteer(steer, outcome, agent.NativeInputReceipt{})
		}
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
	a.withdrawZCodeSteersLocked(false)
}

// dropZCodeSteersOfReplacedSession drops the steers of a session that a new
// one replaced: no turn of the old session runs again, so the queue runs them
// in the new one.
func (a *Agent) dropZCodeSteersOfReplacedSession() {
	a.Mu.Lock()
	a.withdrawnSteerInputs = nil
	a.Mu.Unlock()
	for _, steer := range a.takeZCodeSteers(func(string, *zcodeSteer) bool { return true }) {
		a.reportZCodeSteer(steer, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})
	}
}

// resetZCodeSteers forgets every steer when the process ends. The process
// exit owns the queue's recovery of inputs still awaiting their echo.
func (a *Agent) resetZCodeSteers() {
	a.Mu.Lock()
	a.steers = nil
	a.withdrawnSteerInputs = nil
	a.Mu.Unlock()
}
