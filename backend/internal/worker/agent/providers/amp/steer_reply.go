package amp

import (
	"log/slog"
	"slices"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// enqueueSteerWork runs fn on a per-agent serialized FIFO worker off outputMu
// so steer echo observations and steered turn-end checks preserve event order
// without stalling stdout reading or Interrupt.
func (a *Agent) enqueueSteerWork(fn func()) bool {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return false
	}
	a.steerWorkWG.Add(1)
	a.mu.Unlock()

	a.steerWorkMu.Lock()
	a.steerWorkQueue = append(a.steerWorkQueue, fn)
	start := !a.steerWorkRunning
	if start {
		a.steerWorkRunning = true
	}
	a.steerWorkMu.Unlock()

	if start {
		go a.drainSteerWork()
	}
	return true
}

func (a *Agent) drainSteerWork() {
	for {
		a.steerWorkMu.Lock()
		if len(a.steerWorkQueue) == 0 {
			a.steerWorkRunning = false
			a.steerWorkMu.Unlock()
			return
		}
		fn := a.steerWorkQueue[0]
		a.steerWorkQueue[0] = nil
		a.steerWorkQueue = a.steerWorkQueue[1:]
		a.steerWorkMu.Unlock()

		fn()
		a.steerWorkWG.Done()
	}
}

func (a *Agent) hasPendingSteerWork() bool {
	a.steerWorkMu.Lock()
	defer a.steerWorkMu.Unlock()
	return a.steerWorkRunning || len(a.steerWorkQueue) > 0
}

func (a *Agent) waitSteerWork() {
	a.steerWorkWG.Wait()
}

// observeSteeredInputAsync schedules native export reconciliation for a user
// echo off outputMu while reserving a transcript position so any resolved steer
// row stays ordered ahead of subsequent assistant rows.
func (a *Agent) observeSteeredInputAsync(proc *ampProcess, threadID string) {
	a.mu.Lock()
	reply := a.turn.reply
	pending := a.pendingTrackedSteerLocked(threadID)
	frameCount := 0
	if reply != nil {
		frameCount = len(reply.frames)
	}
	a.mu.Unlock()
	if !pending {
		return
	}
	a.bumpExportGen()

	var opsMu sync.Mutex
	var ops []func() error
	var ready bool
	publish, reserveErr := agent.ReserveTranscriptSequence(a.sink, "resolve Amp steer echo", func(agent.TranscriptSequence) error {
		opsMu.Lock()
		if !ready {
			opsMu.Unlock()
			return agent.ErrOutputInFlight
		}
		pendingOps := ops
		opsMu.Unlock()
		for len(pendingOps) > 0 {
			if err := pendingOps[0](); err != nil {
				return err
			}
			pendingOps[0] = nil
			pendingOps = pendingOps[1:]
			opsMu.Lock()
			ops = pendingOps
			opsMu.Unlock()
		}
		return nil
	})
	if reserveErr != nil {
		if !agent.OutputRetained(reserveErr) {
			slog.Warn("reserve Amp steer echo position", "agent_id", a.agentID, "error", reserveErr)
		}
		return
	}

	var publishOnce sync.Once
	flushReserved := func() error {
		opsMu.Lock()
		ready = true
		opsMu.Unlock()
		var err error
		publishOnce.Do(func() {
			err = publish()
		})
		return err
	}
	runReserved := func(resolutions []agent.SteerResolution) bool {
		captured := make([]func() error, 0, len(resolutions))
		for _, resolution := range resolutions {
			captured = append(captured, a.sink.CaptureSteerResolution(resolution))
		}
		opsMu.Lock()
		ops = captured
		opsMu.Unlock()
		if err := flushReserved(); !agent.OutputRetained(err) {
			slog.Warn("retain the Amp steer echo", "agent_id", a.agentID, "error", err)
			return false
		}
		return true
	}

	if !a.enqueueSteerWork(func() {
		defer func() { _ = flushReserved() }()
		a.observeSteeredInputWork(proc, threadID, reply, frameCount, runReserved)
	}) {
		_ = flushReserved()
	}
}

func (a *Agent) observeSteeredInputWork(
	proc *ampProcess,
	threadID string,
	reply *ampInputReply,
	frameCount int,
	runReserved func([]agent.SteerResolution) bool,
) {
	a.mu.Lock()
	pending := a.pendingTrackedSteerLocked(threadID)
	a.mu.Unlock()
	if !pending {
		return
	}
	ctx, cancel := a.exportContextForProc(proc)
	defer cancel()
	exported, err := a.readInputExport(ctx, threadID)
	if err != nil {
		return
	}

	a.mu.Lock()
	if a.stopped || threadID != a.threadID || (reply != nil && a.turn.reply != reply) {
		a.mu.Unlock()
		return
	}
	associateSteers(a.nativeSteers, exported)
	awaiting := make(map[string]*ampSteerSubmission)
	for _, candidate := range a.nativeSteers {
		if candidate.threadID == threadID && candidate.resolution.Receipt.InputID != "" &&
			candidate.state == ampSteerPending && candidate.nativeID != "" {
			awaiting[candidate.nativeID] = candidate
		}
	}
	if len(awaiting) == 0 {
		a.mu.Unlock()
		return
	}
	var firstSteer *ampSteerSubmission
	createdReply := false
	candidateReply := reply
	if candidateReply == nil {
		// If a prior async echo already installed turn.reply, reuse it.
		candidateReply = a.turn.reply
	}
	if candidateReply == nil {
		// The native actor can read a steer in its own turn after the prior
		// turn has settled. Begin the capture at its first proved input.
		for _, message := range exported.Messages {
			if first := awaiting[message.ID]; first != nil {
				candidateReply = &ampInputReply{receipt: first.resolution.Receipt, startsWithSteer: true}
				firstSteer = first
				createdReply = true
				break
			}
		}
	}
	if candidateReply == nil || candidateReply.receipt.SessionID != threadID {
		a.mu.Unlock()
		return
	}
	prefixCount := min(frameCount, len(candidateReply.frames))
	snapshot := ampInputReply{
		receipt:  candidateReply.receipt,
		frames:   slices.Clone(candidateReply.frames[:prefixCount]),
		unproved: candidateReply.unproved,
	}
	a.mu.Unlock()

	history := exported.replyHistory(snapshot.receipt.InputID)
	if history.conflict || !snapshot.matchesHistoryPrefix(history) {
		return
	}

	a.mu.Lock()
	if a.stopped || threadID != a.threadID || (!createdReply && a.turn.reply != candidateReply) ||
		(createdReply && a.turn.reply != nil) {
		a.mu.Unlock()
		return
	}
	var echoes []*ampSteerSubmission
	if firstSteer != nil && firstSteer.state == ampSteerPending {
		echoes = append(echoes, firstSteer)
	}
	for _, boundary := range history.steers {
		if candidate := awaiting[boundary.inputID]; candidate != nil && candidate.state == ampSteerPending && boundary.afterFrame == frameCount {
			echoes = append(echoes, candidate)
		}
	}
	startedTurn := false
	if createdReply && len(echoes) > 0 && !a.turn.active {
		a.turn = turnState{active: true, startedAt: a.now()}
		startedTurn = true
	}
	a.mu.Unlock()
	if len(echoes) == 0 {
		return
	}
	if startedTurn {
		// Publish the active turn before the echo can release the queue.
		a.PublishTurnActive()
	}

	resolutions := make([]agent.SteerResolution, 0, len(echoes))
	for _, submission := range echoes {
		res := submission.resolution
		res.Outcome = agent.SteerOutcomeEchoed
		resolutions = append(resolutions, res)
	}
	if !runReserved(resolutions) {
		if startedTurn {
			a.mu.Lock()
			if a.turn.active && a.turn.reply == nil && a.turn.untrackedFrames == 0 {
				a.turn = turnState{}
				a.mu.Unlock()
				a.PublishTurnActive()
			} else {
				a.mu.Unlock()
			}
		}
		return
	}

	a.mu.Lock()
	for _, submission := range echoes {
		if submission.state == ampSteerPending {
			submission.state = ampSteerEchoed
		}
	}
	if createdReply && !a.stopped && threadID == a.threadID && a.turn.reply == nil {
		if a.turn.untrackedFrames > 0 {
			candidateReply.unproved = true
		}
		a.turn.reply = candidateReply
	}
	if a.turn.reply == candidateReply {
		for _, submission := range echoes {
			if submission.nativeID != candidateReply.receipt.InputID {
				candidateReply.steers = append(candidateReply.steers, submission.resolution.Receipt)
			}
		}
	}
	a.mu.Unlock()
}

// verifySteeredTurnEndAsync checks off outputMu whether an end_turn message
// truly ended a steered execution or whether a pending steer continues it.
func (a *Agent) verifySteeredTurnEndAsync(proc *ampProcess, threadID string, reply *ampInputReply, frameCount int) {
	a.enqueueSteerWork(func() {
		a.mu.Lock()
		if a.stopped || !a.turn.active || a.turn.reply != reply || (reply != nil && len(reply.frames) != frameCount) {
			a.mu.Unlock()
			return
		}
		pending := a.pendingTrackedSteerLocked(threadID)
		a.mu.Unlock()

		if pending && reply != nil {
			ctx, cancel := a.exportContextForProc(proc)
			history, err := a.readReplyHistory(ctx, reply.receipt)
			cancel()
			if err == nil && !history.conflict && reply.matchesHistoryPrefixLen(history, frameCount) {
				if history.ended && len(history.assistant) == frameCount {
					a.mu.Lock()
					if a.turn.reply == reply {
						reply.settledHistory = &history
					}
					a.mu.Unlock()
				} else {
					for _, boundary := range history.steers {
						if boundary.afterFrame >= frameCount {
							return
						}
					}
					if len(history.assistant) != frameCount {
						return
					}
				}
			}
		}

		a.outputMu.Lock()
		defer a.outputMu.Unlock()
		a.mu.Lock()
		stillCurrent := !a.stopped && a.turn.active && a.turn.reply == reply && (reply == nil || len(reply.frames) == frameCount)
		if stillCurrent {
			a.finishEchoedSteersLocked(threadID)
		}
		a.mu.Unlock()
		if stillCurrent {
			a.finishTurn(agent.MessageCompletionComplete, nil)
		}
	})
}

func (a *Agent) pendingTrackedSteerLocked(threadID string) bool {
	return threadID != "" && threadID == a.threadID && slices.ContainsFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.threadID == threadID && submission.resolution.Receipt.InputID != "" && submission.state == ampSteerPending
	})
}

func (a *Agent) pruneTrackedSteersLocked() {
	a.nativeSteers = slices.DeleteFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.state == ampSteerFinished && submission.resolution.Receipt.InputID != ""
	})
}

func (a *Agent) finishReplySteersLocked(reply *ampInputReply, completion agent.MessageCompletion) {
	if reply == nil {
		return
	}
	owned := make(map[string]bool, len(reply.steers)+1)
	if completion == agent.MessageCompletionComplete {
		if reply.startsWithSteer {
			owned[reply.receipt.InputID] = true
		}
		for _, receipt := range reply.steers {
			owned[receipt.InputID] = true
		}
	} else if !reply.startsWithSteer {
		for _, receipt := range reply.steers {
			owned[receipt.InputID] = true
		}
	}
	if len(owned) == 0 {
		return
	}
	for _, submission := range a.nativeSteers {
		if submission.state == ampSteerEchoed && owned[submission.nativeID] {
			submission.markFinished()
		}
	}
	a.pruneTrackedSteersLocked()
}

// Once the agent stops, its recovery reader can no longer finish an
// accepted steer. Retire those receipts so an exact wait can finish.
func (a *Agent) retireStoppedTrackedSteers() {
	a.mu.Lock()
	var receipts []agent.NativeInputReceipt
	for _, submission := range a.nativeSteers {
		receipt := submission.resolution.Receipt
		if receipt.InputID != "" && submission.state == ampSteerEchoed {
			submission.markFinished()
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
	alreadyEchoed := submission.state == ampSteerEchoed || submission.state == ampSteerFinished
	a.mu.Unlock()
	if alreadyEchoed {
		return true
	}
	resolution := submission.resolution
	resolution.Outcome = agent.SteerOutcomeEchoed
	if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
		slog.Warn("retain the Amp steer echo", "agent_id", a.agentID, "error", err)
		return false
	}
	a.mu.Lock()
	if submission.state == ampSteerPending {
		submission.state = ampSteerEchoed
	}
	a.mu.Unlock()
	return true
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
			(submission.state == ampSteerPending || submission.state == ampSteerEchoed) && submission.nativeID != "" {
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
		steer.submission.markFinished()
	}
	a.pruneTrackedSteersLocked()
	a.mu.Unlock()
	return true
}
