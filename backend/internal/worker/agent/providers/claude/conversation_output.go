package claude

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type claudeConversationFrame struct {
	msgType         string
	envelope        *messageEnvelope
	content         agent.MessageContent
	destination     *claudeTranscriptDestination
	spans           agent.SpanServices
	prepared        *agent.CapturedTranscriptOperation
	report          *agent.CapturedTranscriptOperation
	retryResolution *agent.CapturedTranscriptOperation
	admission       *claudeOutputDecision
	span            agent.SpanInfo
	result          *claudeReceivedResult
	inputEvidence   *claudeInferredTurnEvidence
}

func (destination *claudeTranscriptDestination) captureFrame(content []byte, msgType string, env *messageEnvelope) *claudeConversationFrame {
	key := claudeRevocableKey(msgType, env)
	captured := destination.capture(agent.MessageContent{
		IdempotencyKey: key, WriteReceipt: agent.NewTranscriptWriteReceipt(),
	}, agent.SpanInfo{})
	// Claude's reader owns these bytes before it admits the frame.
	captured.Original = content
	frame := &claudeConversationFrame{
		msgType: msgType, envelope: env, content: captured, destination: destination,
		spans: agent.TranscriptFrameSpans(captured, destination.sink),
	}
	if key != "" {
		frame.admission = &claudeOutputDecision{}
	}
	return frame
}

func (frame *claudeConversationFrame) live() bool {
	return agent.TranscriptFrameIsCurrent(frame.content)
}

func (a *Agent) admitClaudeFrame(sequence agent.TranscriptSequence, frame *claudeConversationFrame, retractions *claudeFrameRetractions) (bool, error) {
	accepted, err := frame.content.WriteReceipt.AdmitMessage(frame.destination.sink, frame.content)
	if err != nil {
		return false, err
	}
	a.settleClaudeInferredTurn(frame, accepted)
	if !accepted {
		a.resolveClaudeRetractionOwner(frame, false)
		return false, nil
	}
	return true, retractions.apply(sequence)
}

func (a *Agent) completeClaudeNativeFrame(frame *claudeConversationFrame, err error) error {
	if err == nil {
		_, stored := frame.content.WriteReceipt.StoredMessageSequence()
		a.resolveClaudeRetractionOwner(frame, stored)
	}
	return err
}

func (frame *claudeConversationFrame) prepare(content agent.MessageContent, span agent.SpanInfo, notification bool) error {
	captured, err := agent.CaptureTranscriptFrame(frame.destination.sink, content, span)
	if err != nil {
		return err
	}
	var operation agent.CapturedTranscriptOperation
	switch {
	case frame.msgType == claudeMsgTypeResult:
		operation = captured.TurnEndOperation()
	case frame.msgType == claudeMsgTypeUser:
		operation = captured.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER)
	case notification:
		operation = captured.NotificationOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	default:
		operation = captured.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	}
	frame.prepared = &operation
	frame.span = span
	return nil
}

func (frame *claudeConversationFrame) persist(sequence agent.TranscriptSequence) (bool, error) {
	if _, err := sequence.Persist(*frame.prepared); err != nil {
		return false, err
	}
	if frame.content.IdempotencyKey != "" {
		if _, stored := frame.content.WriteReceipt.StoredMessageSequence(); !stored {
			return false, nil
		}
	}
	if frame.span.SpanType != "" {
		frame.spans.SetSpanType(frame.span.SpanID, frame.span.SpanType)
	}
	return true, nil
}

func (a *Agent) submitClaudeConversationFrame(frame *claudeConversationFrame, label string, run func(agent.TranscriptSequence) error) {
	senderSettled := a.outputOrder.captureSender(frame)
	drain, err := agent.ReserveTranscriptSequence(frame.destination.sink, label, func(sequence agent.TranscriptSequence) error {
		err := run(sequence)
		if err == nil && senderSettled != nil {
			senderSettled()
		}
		return err
	})
	if err == nil {
		err = drain()
	}
	a.reportClaudeSequenceError(label, err)
	if !agent.OutputRetained(err) {
		a.resolveClaudeRetractionOwner(frame, false)
		if senderSettled != nil {
			senderSettled()
		}
	}
}

func (a *Agent) reportClaudeSequenceError(label string, err error) {
	if !agent.OutputRetained(err) {
		slog.Error("write Claude transcript", "agent_id", a.AgentID(), "operation", label, "error", err)
	}
}

func (a *Agent) receiveClaudeConversation(content []byte, msgType string) {
	var env messageEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		slog.Warn("invalid message envelope", "agent_id", a.AgentID(), "error", err)
		return
	}
	routing := decodeClaudeFrameRouting(content)
	if env.ParentToolUseID != "" {
		if !claudeIsSyntheticContext(msgType, &env) && (msgType != claudeMsgTypeUser || !isSimpleUserTextEcho(content)) {
			a.routeClaudeOutput(func() { a.routeSubagentMessage(content, msgType, &env, routing) })
		}
		return
	}
	var result *claudeReceivedResult
	var inputEvidence *claudeInferredTurnEvidence
	if !claudeIsSyntheticContext(msgType, &env) && !routing.isReplay && a.liveClaudeOutput() {
		switch msgType {
		case claudeMsgTypeResult:
			if (env.Origin.Kind != "peer" || !env.Origin.Handback) &&
				(routing.sessionID == "" || routing.sessionID == a.nativeTranscriptSession()) {
				result = a.captureClaudeResultEdge()
			}
		case claudeMsgTypeAssistant, claudeMsgTypeUser:
			inputEvidence = a.reserveClaudeInferredTurn()
		}
	}
	route, err := a.reserveRoutedClaudeRootOutput("process Claude conversation frame", func(func()) claudeRootSequence {
		destination := a.rootTranscript()
		frame := destination.captureFrame(content, msgType, &env)
		if frame.spans == nil {
			a.releaseClaudeInferredTurn(inputEvidence)
			return func(agent.TranscriptSequence) error {
				return errors.New("the Claude frame has no retained span authority")
			}
		}
		frame.result = result
		frame.inputEvidence = inputEvidence
		if routing.sessionID != "" && routing.sessionID != frame.content.AgentSessionID {
			a.releaseClaudeInferredTurn(frame.inputEvidence)
			frame.inputEvidence = nil
		}
		if msgType == claudeMsgTypeResult && !routing.isReplay && frame.live() && a.liveClaudeOutput() &&
			(routing.sessionID == "" || routing.sessionID == frame.content.AgentSessionID) {
			a.endClaudeCompaction()
		}
		keys := routing.claudeEvictionKeys(msgType, frame.content.IdempotencyKey)
		retractions := claudeFrameRetractions{candidates: a.claudeRetractionCandidates(keys, routing.sessionID, destination)}
		if !claudeIsSyntheticContext(msgType, &env) && !claudeIsFailedCompactCommand(&env) {
			a.rememberRevocableFrame(frame, routing.sessionID)
		}
		senderSettled := a.outputOrder.captureSender(frame)
		return func(sequence agent.TranscriptSequence) error {
			err := a.processClaudeRootFrame(sequence, frame, &retractions)
			if err == nil && senderSettled != nil {
				senderSettled()
			}
			return err
		}
	})
	if err != nil {
		a.releaseClaudeInferredTurn(inputEvidence)
		a.reportClaudeSequenceError("process Claude conversation frame", err)
		return
	}
	route()
}

type claudeInferredTurnEvidence struct{ pending uint64 }

func (a *Agent) reserveClaudeInferredTurn() *claudeInferredTurnEvidence {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.sessionStateReported {
		return nil
	}
	if a.inferredTurnEvidence == nil {
		a.inferredTurnEvidence = &claudeInferredTurnEvidence{}
	}
	a.inferredTurnEvidence.pending++
	return a.inferredTurnEvidence
}

func (a *Agent) releaseClaudeInferredTurn(evidence *claudeInferredTurnEvidence) {
	if evidence == nil {
		return
	}
	a.Mu.Lock()
	evidence.pending--
	a.Mu.Unlock()
}

func (a *Agent) settleClaudeInferredTurn(frame *claudeConversationFrame, accepted bool) {
	evidence := frame.inputEvidence
	if evidence == nil {
		return
	}
	current := accepted && frame.live()
	a.Mu.Lock()
	activate := current && a.inferredTurnEvidence == evidence && !a.sessionStateReported &&
		!a.turnActive && a.liveClaudeOutputLocked()
	if activate {
		a.turnActive = true
	}
	evidence.pending--
	a.Mu.Unlock()
	frame.inputEvidence = nil
	if activate {
		a.PublishTurnActive()
	}
}

type claudeReceivedResult struct {
	endRevision   uint64
	inputRevision uint64
	busyRevision  uint64
	interrupted   bool
}

func (a *Agent) captureClaudeResultEdge() *claudeReceivedResult {
	a.Mu.Lock()
	a.turnEndRevision++
	result := &claudeReceivedResult{
		endRevision: a.turnEndRevision, inputRevision: a.turnInputRevision,
		busyRevision: a.nativeBusyRevision, interrupted: a.interruptRequested,
	}
	a.interruptRequested = false
	retired := a.takeControlsLocked(func(record *claudePendingControl) bool { return record.agentID == "" })
	a.Mu.Unlock()
	a.cancelControls(retired)
	return result
}

func (a *Agent) processClaudeRootFrame(sequence agent.TranscriptSequence, frame *claudeConversationFrame, retractions *claudeFrameRetractions) error {
	accepted, err := a.admitClaudeFrame(sequence, frame, retractions)
	if err != nil || !accepted {
		return err
	}
	msgType, env := frame.msgType, frame.envelope
	syntheticContext := claudeIsSyntheticContext(msgType, env)
	if !syntheticContext {
		if err := a.resolveHeldAPIError(sequence, frame); err != nil {
			return err
		}
	}
	if msgType == claudeMsgTypeAssistant && env.IsAPIErrorMessage {
		a.holdAPIError(frame)
		a.resolveClaudeRetractionOwner(frame, true)
		return nil
	}
	if syntheticContext || msgType == claudeMsgTypeAssistant && claudeIsFailedCompactCommand(env) ||
		msgType == claudeMsgTypeUser && isSimpleUserTextEcho(frame.content.Original) {
		a.resolveClaudeRetractionOwner(frame, false)
		return nil
	}
	return a.completeClaudeNativeFrame(frame, a.persistClaudeRootFrame(sequence, frame))
}

type claudeFrameRetractions struct {
	candidates []claudeRetractionCandidate
	targets    []claudeRetractionTarget
}

func (retractions *claudeFrameRetractions) apply(sequence agent.TranscriptSequence) error {
	if len(retractions.candidates) > 0 {
		targets, ready := selectClaudeRetractionTargets(retractions.candidates)
		if !ready {
			return agent.ErrOutputInFlight
		}
		retractions.candidates = nil
		retractions.targets = targets
	}
	return applyClaudeRetractions(sequence, &retractions.targets)
}

func applyClaudeRetractions(sequence agent.TranscriptSequence, targets *[]claudeRetractionTarget) error {
	for len(*targets) > 0 {
		if err := (*targets)[0].apply(sequence); err != nil {
			return err
		}
		(*targets)[0] = claudeRetractionTarget{}
		*targets = (*targets)[1:]
	}
	return nil
}

func (a *Agent) receiveClaudeFallback(content []byte) {
	routing := decodeClaudeFrameRouting(content)
	// Apply the model observation in native source order. The notice can wait for storage.
	// An absent scope means a session fallback. A local fallback changes only its own response.
	if routing.scope != claudeModelFallbackScopeLocal {
		a.adoptClaudeModelFallback(content)
	}
	var fields struct {
		UUID            string `json:"uuid"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		ToolUseID       string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(content, &fields); err != nil {
		slog.Warn("invalid Claude fallback", "agent_id", a.AgentID(), "error", err)
		return
	}
	env := messageEnvelope{UUID: fields.UUID, ParentToolUseID: fields.ParentToolUseID, ToolUseID: fields.ToolUseID}
	a.routeClaudeRootSequence("process Claude fallback", func(wakeRoot func()) claudeRootSequence {
		destination := a.rootTranscript()
		frame := destination.captureFrame(content, claudeMsgTypeSystem, &env)
		if frame.spans == nil {
			return func(agent.TranscriptSequence) error {
				return errors.New("the Claude fallback has no retained span authority")
			}
		}
		var candidates []claudeRetractionCandidate
		if env.ParentToolUseID == "" {
			keys := routing.claudeEvictionKeys(claudeMsgTypeSystem, claudeTranscriptKey(env.UUID))
			candidates = a.claudeRetractionCandidates(keys, routing.sessionID, nil)
		}
		return a.prepareClaudeFallback(frame, candidates, wakeRoot)
	})
}

type claudeFallbackQueueWrite struct {
	targets []claudeRetractionTarget
	drain   func() error
	err     error
}

func (a *Agent) prepareClaudeFallback(frame *claudeConversationFrame, candidates []claudeRetractionCandidate, wakeRoot func()) claudeRootSequence {
	const label = "process Claude fallback"
	root := frame.destination.sink
	rootQueue := agent.OutputQueueFor(root)
	rootWrite := &claudeFallbackQueueWrite{}
	writes := map[*agent.OutputQueue]*claudeFallbackQueueWrite{rootQueue: rootWrite}
	var orderedWrites []*claudeFallbackQueueWrite
	var ready atomic.Bool
	var pendingChildren atomic.Int64
	var reservationErr error
	reserve := func(queue *agent.OutputQueue, writer agent.TranscriptServices) {
		if _, exists := writes[queue]; exists {
			return
		}
		write := &claudeFallbackQueueWrite{}
		writes[queue] = write
		drain, err := agent.ReserveTranscriptSequence(writer, label, func(sequence agent.TranscriptSequence) error {
			if !ready.Load() {
				return agent.ErrOutputInFlight
			}
			if len(write.targets) == 0 {
				return nil
			}
			if err := applyClaudeRetractions(sequence, &write.targets); err != nil {
				return err
			}
			// Every targeted deletion committed before this callback returned.
			if pendingChildren.Add(-1) == 0 {
				wakeRoot()
			}
			return nil
		})
		if err != nil {
			write.err = err
			return
		}
		write.drain = drain
		orderedWrites = append(orderedWrites, write)
	}
	decisions := make(map[*claudeOutputDecision]struct{})
	for _, candidate := range candidates {
		for owner := range candidate.owners.all {
			queue := agent.OutputQueueFor(owner.destination.sink)
			if queue != rootQueue {
				reserve(queue, owner.destination.sink)
			}
			if owner.admission != nil {
				decisions[owner.admission] = struct{}{}
			}
		}
	}
	afterClaudeOutputDecisions(decisions, func() {
		targets, _ := selectClaudeRetractionTargets(candidates)
		for _, target := range targets {
			queue := agent.OutputQueueFor(target.destination.sink)
			write := writes[queue]
			write.targets = append(write.targets, target)
			if write.err != nil && reservationErr == nil {
				reservationErr = write.err
			}
		}
		var count int64
		for _, write := range orderedWrites {
			if len(write.targets) != 0 {
				count++
			}
		}
		pendingChildren.Store(count)
		ready.Store(true)
		for _, write := range orderedWrites {
			a.reportClaudeSequenceError(label, write.drain())
		}
		if count == 0 {
			wakeRoot()
		}
	})
	return func(sequence agent.TranscriptSequence) error {
		if !ready.Load() || pendingChildren.Load() != 0 {
			return agent.ErrOutputInFlight
		}
		if reservationErr != nil {
			return reservationErr
		}
		if err := applyClaudeRetractions(sequence, &rootWrite.targets); err != nil {
			return err
		}
		return persistClaudeFallbackNotice(sequence, frame)
	}
}

func persistClaudeFallbackNotice(sequence agent.TranscriptSequence, frame *claudeConversationFrame) error {
	if frame.prepared == nil {
		env := frame.envelope
		parentSpanID := env.ParentToolUseID
		if parentSpanID == "" {
			parentSpanID = env.ToolUseID
		}
		span := claudeSpanInfoFor(frame.spans, frame.msgType, env, parentSpanID)
		if err := frame.prepare(frame.content, span, claudeIsConsolidatable(frame.content.Original)); err != nil {
			return err
		}
	}
	_, err := sequence.Persist(*frame.prepared)
	return err
}

func (a *Agent) receiveClaudeSessionState(state string) {
	a.Mu.Lock()
	a.sessionStateReported = true
	busy := state != claudeSessionStateIdle
	if busy {
		a.nativeBusyRevision++
	}
	busyRevision := a.nativeBusyRevision
	a.Mu.Unlock()
	if busy {
		a.armTurn()
		return
	}
	a.routeClaudeRootSequence("apply Claude session state", func(func()) claudeRootSequence {
		destination := a.rootTranscript()
		captured := destination.capture(agent.MessageContent{}, agent.SpanInfo{})
		return func(agent.TranscriptSequence) error {
			if agent.TranscriptFrameIsCurrent(captured) {
				a.noteSessionIdle(busyRevision, agent.DirectProviderServices(destination.sink))
			}
			return nil
		}
	})
}

type claudeHeldAPIError struct {
	frame *claudeConversationFrame
	text  string
	key   string
}

func (a *Agent) holdAPIError(frame *claudeConversationFrame) {
	var texts []string
	for _, block := range frame.envelope.ContentBlocks() {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	a.heldAPIErrorMu.Lock()
	a.heldAPIError = &claudeHeldAPIError{frame: frame, text: strings.Join(texts, "\n"), key: frame.content.IdempotencyKey}
	a.heldAPIErrorMu.Unlock()
}

// resolveHeldAPIError settles the hold at its resolver's position or the
// reader's final position. Failed storage leaves the same prepared hold in place.
func (a *Agent) resolveHeldAPIError(sequence agent.TranscriptSequence, resolver *claudeConversationFrame) error {
	a.heldAPIErrorMu.Lock()
	held := a.heldAPIError
	a.heldAPIErrorMu.Unlock()
	if held == nil {
		return nil
	}
	frame := held.frame
	if frame.prepared == nil {
		retracted, err := frame.destination.sink.IsMessageRetracted(frame.content, held.key)
		if err != nil {
			return err
		}
		coalesced := resolver != nil && resolver.destination == frame.destination && resolver.msgType == claudeMsgTypeResult &&
			resolver.envelope.IsError && resolver.envelope.Result == held.text
		if retracted || coalesced {
			a.clearHeldAPIError(held)
			return nil
		}
	}
	if err := a.persistClaudeRootFrame(sequence, frame); err != nil {
		return err
	}
	a.clearHeldAPIError(held)
	return nil
}

func (a *Agent) clearHeldAPIError(held *claudeHeldAPIError) {
	a.heldAPIErrorMu.Lock()
	if a.heldAPIError == held {
		a.heldAPIError = nil
	}
	a.heldAPIErrorMu.Unlock()
}

func (a *Agent) persistClaudeRetryResolution(sequence agent.TranscriptSequence, frame *claudeConversationFrame) error {
	if frame.retryResolution == nil {
		if !frame.destination.apiRetry.TakePending() {
			return nil
		}
		raw, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
		if err != nil {
			return err
		}
		content := agent.MessageContent{
			Original: raw, AgentSessionID: frame.content.AgentSessionID,
			Publication: frame.content.Publication, WriteReceipt: agent.NewTranscriptWriteReceipt(),
		}
		captured, err := agent.CaptureTranscriptFrame(frame.destination.sink, content, agent.SpanInfo{})
		if err != nil {
			return err
		}
		resolution := captured.WorkerNotificationOperation()
		frame.retryResolution = &resolution
	}
	_, err := sequence.Persist(*frame.retryResolution)
	return err
}

func (a *Agent) persistClaudeRootFrame(sequence agent.TranscriptSequence, frame *claudeConversationFrame) error {
	env, msgType, raw := frame.envelope, frame.msgType, frame.content.Original
	if frame.prepared == nil {
		content := frame.content
		if msgType == claudeMsgTypeAssistant && !env.IsAPIErrorMessage {
			if err := a.persistClaudeRetryResolution(sequence, frame); err != nil {
				return err
			}
		}
		if msgType == claudeMsgTypeResult && frame.live() {
			if a.persistClaudePeerHandbackResult(env) {
				return nil
			}
			content.Metadata = a.MessageWithToolUses(raw).Metadata
			if frame.result != nil && frame.result.interrupted && env.statesAbortedTurn() {
				content.Completion = agent.MessageCompletionInterrupted
			}
		}
		span := claudeSpanInfoFor(frame.spans, msgType, env, env.ToolUseID)
		if err := frame.prepare(content, span, false); err != nil {
			return err
		}
	}
	stored, err := frame.persist(sequence)
	if err != nil || !stored {
		return err
	}

	live := frame.live() && a.liveClaudeOutput()
	if live && (msgType == claudeMsgTypeAssistant || msgType == claudeMsgTypeResult) {
		a.extractAndBroadcastUsage(env, msgType)
	}
	switch msgType {
	case claudeMsgTypeAssistant:
		a.processAssistantBlocks(env, frame.spans, live)
	case claudeMsgTypeUser:
		if live {
			a.settleToolControls(env.ContentBlocks())
			a.persistClaudeHandbackResult(env)
			a.detectPlanModeFromToolResult(env)
		}
		claudeCloseToolResultSpans(frame.spans, env)
	case claudeMsgTypeResult:
		frame.spans.ResetSpans()
		frame.destination.apiRetry.Reset()
		if live && frame.result != nil {
			a.finishClaudeRootResult(frame.result, env, raw)
		}
	}
	return nil
}

func (a *Agent) finishClaudeRootResult(result *claudeReceivedResult, env *messageEnvelope, raw []byte) {
	a.Mu.Lock()
	a.TurnToolUses = 0
	if a.awaitedInputRevision <= result.inputRevision {
		a.awaitedInputRevision = 0
	}
	current := a.turnEndRevision == result.endRevision && a.awaitedInputRevision == 0 &&
		a.nativeBusyRevision == result.busyRevision
	publishesState := a.sessionStateReported
	if current && !publishesState {
		a.turnActive = false
	}
	a.Mu.Unlock()
	// A child can outlive this root result, so clear only the root's restart intents.
	a.tasks.clearClaudeRestarts("")
	if current {
		direct := agent.DirectProviderServices(a.sink)
		providerkit.ScheduleOrCancelServerErrorAutoRetry(direct, env.IsError && isRetryableClaudeResultError(env.Result), raw)
		if !publishesState {
			a.publishTurnActive(direct)
		}
	}
}
