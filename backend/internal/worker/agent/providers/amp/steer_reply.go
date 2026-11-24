package amp

import (
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// observeSteeredInput uses the native export to identify a user echo. A
// stdout user message carries content alone, so identical inputs can echo
// the same bytes for different submissions.
func (a *Agent) observeSteeredInput(threadID string) {
	a.mu.Lock()
	reply := a.turn.reply
	pending := a.pendingTrackedSteerLocked(threadID)
	a.mu.Unlock()
	if !pending {
		return
	}
	ctx, cancel := a.exportContext()
	defer cancel()
	exported, err := a.readInputExport(ctx, threadID)
	if err != nil {
		return
	}

	a.mu.Lock()
	if a.stopped || threadID != a.threadID || a.turn.reply != reply {
		a.mu.Unlock()
		return
	}
	associateSteers(a.nativeSteers, exported)
	awaiting := make(map[string]*ampSteerSubmission)
	for _, candidate := range a.nativeSteers {
		if candidate.threadID == threadID && candidate.resolution.Receipt.InputID != "" &&
			!candidate.echoed && !candidate.uncertain && candidate.nativeID != "" {
			awaiting[candidate.nativeID] = candidate
		}
	}
	if len(awaiting) == 0 {
		a.mu.Unlock()
		return
	}
	var echoes []*ampSteerSubmission
	if reply == nil {
		// The native actor can read a steer in its own turn after the prior
		// turn has settled. Begin the capture at its first proved input.
		for _, message := range exported.Messages {
			if first := awaiting[message.ID]; first != nil {
				reply = &ampInputReply{receipt: first.resolution.Receipt, startsWithSteer: true}
				echoes = append(echoes, first)
				break
			}
		}
	}
	if reply == nil || reply.receipt.SessionID != threadID {
		a.mu.Unlock()
		return
	}
	history := exported.replyHistory(reply.receipt.InputID)
	if history.conflict || !reply.matchesHistoryPrefix(history) {
		a.mu.Unlock()
		return
	}
	for _, boundary := range history.steers {
		if candidate := awaiting[boundary.inputID]; candidate != nil && boundary.afterFrame == len(reply.frames) {
			echoes = append(echoes, candidate)
		}
	}
	if len(echoes) == 0 {
		a.mu.Unlock()
		return
	}
	started := !a.turn.active
	if started {
		a.turn = turnState{active: true, startedAt: a.now()}
	}
	a.turn.reply = reply
	a.mu.Unlock()
	if started {
		a.PublishTurnActive()
	}
	for _, submission := range echoes {
		if a.resolveSteerEcho(submission) && submission.nativeID != reply.receipt.InputID {
			a.mu.Lock()
			if a.turn.reply == reply {
				reply.steers = append(reply.steers, submission.resolution.Receipt)
			}
			a.mu.Unlock()
		}
	}
}

func (a *Agent) pendingTrackedSteerLocked(threadID string) bool {
	return threadID != "" && threadID == a.threadID && slices.ContainsFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.threadID == threadID && submission.resolution.Receipt.InputID != "" && !submission.echoed && !submission.uncertain
	})
}

func (a *Agent) pruneTrackedSteersLocked() {
	a.nativeSteers = slices.DeleteFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.finished && submission.resolution.Receipt.InputID != ""
	})
}

// Once the agent stops, its recovery reader can no longer finish an
// accepted steer. Retire those receipts so an exact wait can finish.
func (a *Agent) retireStoppedTrackedSteers() {
	a.mu.Lock()
	var receipts []agent.NativeInputReceipt
	for _, submission := range a.nativeSteers {
		receipt := submission.resolution.Receipt
		if receipt.InputID != "" && submission.echoed && !submission.finished {
			submission.finished = true
			receipts = append(receipts, receipt)
		}
	}
	a.pruneTrackedSteersLocked()
	a.mu.Unlock()
	for _, receipt := range receipts {
		a.retireInputReply(receipt)
	}
}

func (a *Agent) resolveSteerEcho(submission *ampSteerSubmission) bool {
	a.mu.Lock()
	echoed := submission.echoed
	a.mu.Unlock()
	if echoed {
		return true
	}
	if submission.ticket != 0 {
		a.steers.Echo(submission.ticket)
		a.steers.Settle(a.sink)
	} else {
		resolution := submission.resolution
		resolution.Outcome = agent.SteerOutcomeEchoed
		if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
			slog.Warn("retain the Amp steer echo", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	a.mu.Lock()
	submission.echoed = true
	a.mu.Unlock()
	return true
}

// nativeSteeredTurnEnd distinguishes an assistant's stream end from the
// durable end of an execution. Amp can print end_turn before it reads a
// queued steer in that same execution.
func (a *Agent) nativeSteeredTurnEnd(threadID string, reply *ampInputReply) bool {
	if reply == nil {
		return true
	}
	a.mu.Lock()
	pending := a.pendingTrackedSteerLocked(threadID)
	a.mu.Unlock()
	if !pending && len(reply.steers) == 0 && !reply.startsWithSteer {
		return true
	}
	ctx, cancel := a.exportContext()
	defer cancel()
	history, err := a.readReplyHistory(ctx, reply.receipt)
	if err != nil || history.conflict || !reply.matchesHistoryPrefix(history) {
		return true
	}
	if history.ended && len(history.assistant) == len(reply.frames) {
		a.mu.Lock()
		if a.turn.reply == reply {
			reply.settledHistory = &history
		}
		a.mu.Unlock()
		return true
	}
	// A durable steer after the last observed assistant frame links the
	// following stream output to this execution.
	for _, boundary := range history.steers {
		if boundary.afterFrame >= len(reply.frames) {
			return false
		}
	}
	return len(history.assistant) == len(reply.frames)
}

type recoveredSteer struct {
	submission *ampSteerSubmission
	afterFrame int
}

// recoverTrackedSteers runs after the reader exits. Native history chooses
// both the steer order and the reply shared by consecutive inputs.
func (a *Agent) recoverTrackedSteers(exported *ampThreadExport, submissions []*ampSteerSubmission) bool {
	tracked := make(map[string]*ampSteerSubmission)
	a.mu.Lock()
	for _, submission := range submissions {
		if submission.threadID == exported.ID && submission.resolution.Receipt.InputID != "" &&
			!submission.finished && !submission.uncertain && submission.nativeID != "" {
			tracked[submission.nativeID] = submission
		}
	}
	a.mu.Unlock()
	for _, message := range exported.Messages {
		first := tracked[message.ID]
		if first == nil {
			continue
		}
		history := exported.replyHistory(message.ID)
		steers := []recoveredSteer{{submission: first}}
		for _, boundary := range history.steers {
			if submission := tracked[boundary.inputID]; submission != nil {
				steers = append(steers, recoveredSteer{submission: submission, afterFrame: boundary.afterFrame})
			}
		}
		if !a.recoverSteeredReply(history, steers) {
			return false
		}
		for _, steer := range steers {
			delete(tracked, steer.submission.nativeID)
		}
	}
	return true
}

func (a *Agent) recoverSteeredReply(history ampReplyHistory, steers []recoveredSteer) bool {
	if a.discarding() || a.IsStopped() {
		return false
	}
	a.ensureTurn()
	reply := &ampInputReply{receipt: steers[0].submission.resolution.Receipt, startsWithSteer: true}
	for i, steer := range steers {
		if !a.resolveSteerEcho(steer.submission) {
			return false
		}
		if i > 0 {
			reply.steers = append(reply.steers, steer.submission.resolution.Receipt)
		}
	}
	if !history.ended && !history.conflict {
		return false
	}

	// Only import after checking every retained live frame. A gap before
	// live output would put the recovered text in the wrong order.
	a.mu.Lock()
	for _, steer := range steers {
		live := steer.submission.liveFrames
		if steer.submission.liveUnproved || len(live) > 0 && steer.afterFrame != len(reply.frames) {
			reply.unproved = true
		}
		reply.frames = append(reply.frames, live...)
	}
	a.mu.Unlock()
	if history.ended && reply.matchesHistoryPrefix(history) {
		for _, message := range history.assistant[len(reply.frames):] {
			if message.State.Type != "complete" {
				return false
			}
			blocks, err := visibleExportBlocks(message.Content)
			if err != nil {
				return false
			}
			digest, err := ampReplyFrameDigest(blocks)
			if err != nil || !a.persistExportMessage(reply.receipt.SessionID, message, blocks) {
				return false
			}
			frame := ampReplyFrame{digest: digest}
			for i, block := range blocks {
				if block.Type == contracts.AmpBlockTypeText {
					frame.textKeys = append(frame.textKeys, exportMessageKey(reply.receipt.SessionID, message.ID, i))
				}
			}
			reply.frames = append(reply.frames, frame)
		}
	}
	if !a.retainReplyProjection(reply, history) {
		a.retireReplyInputs(reply)
	}
	a.mu.Lock()
	for _, steer := range steers {
		steer.submission.finished = true
	}
	a.pruneTrackedSteersLocked()
	a.mu.Unlock()
	return true
}
