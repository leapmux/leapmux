package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type RootTranscriptStart struct {
	SessionID      string
	NativeModeID   string
	AgentInitiated bool
	Preceding      RootTranscript
	InputReceipt   agent.NativeInputReceipt
}

type RootTranscriptEnd uint8

const (
	RootTranscriptPromptEnded RootTranscriptEnd = iota
	RootTranscriptWorkEnded
	RootTranscriptStopped
	RootTranscriptSessionReplaced
	RootTranscriptProcessExited
	RootTranscriptPromptNotSent
)

type RootTranscriptCompletion struct {
	Reason     RootTranscriptEnd
	Completion agent.MessageCompletion
	Discard    bool
}

type RootTranscriptCapture interface {
	Writer() agent.TranscriptServices
	Complete()
}

type RootTranscript interface {
	// Prepare captures authority and the native baseline after ACP releases its state locks.
	Prepare() error
	// PrepareInput receives independent copies of the final dispatched ACP blocks.
	PrepareInput([]json.RawMessage) error
	// Reserve only allocates a local position. The caller holds turnMu.
	// It performs no capture, preparation, I/O, output flush or ACP lock acquisition.
	Reserve() RootTranscriptCapture
	// Finish seals this logical turn and admits asynchronous recovery without waiting for I/O.
	Finish(RootTranscriptCompletion) error
}

type PendingToolRequest struct {
	Content  agent.MessageContent
	Revision int64
}
type PendingToolRequestWriter interface {
	ReadPendingToolRequest(*agent.TranscriptWriteReceipt) (PendingToolRequest, bool, error)
	EnrichPendingToolRequest(*agent.TranscriptWriteReceipt, int64, []byte, []byte) (bool, error)
}

type acpTranscriptTarget struct {
	sessionID      string
	transcript     RootTranscript
	inputReceipt   agent.NativeInputReceipt
	agentInitiated bool
	// turnMu protects receiptStarted. The receipt itself is immutable.
	receiptStarted bool
	// turnMu protects sharingReply. Admissions copy it before leaving turnMu.
	sharingReply      bool
	mu                sync.Mutex
	admitted          int
	sealed, finishing bool
	completion        RootTranscriptCompletion
	done              chan struct{}
	// mu protects the steer receipts whose binding was published in output order.
	steerReceipts []agent.NativeInputReceipt
}

// The caller holds turnMu and has released Mu. The factory allocates only.
func (b *Base) openRootTranscriptLocked(sessionID string, agentInitiated bool, preceding *acpTranscriptTarget, receipt agent.NativeInputReceipt) *acpTranscriptTarget {
	var transcript RootTranscript
	if b.hooks.OpenRootTranscript != nil {
		var previous RootTranscript
		if preceding != nil {
			previous = preceding.transcript
		}
		b.Mu.Lock()
		nativeMode := b.nativeModeID
		b.Mu.Unlock()
		transcript = b.hooks.OpenRootTranscript(RootTranscriptStart{
			SessionID: sessionID, NativeModeID: nativeMode, AgentInitiated: agentInitiated, Preceding: previous, InputReceipt: receipt,
		}, b.sink)
	}
	if transcript == nil && receipt.TurnID == "" {
		return nil
	}
	return &acpTranscriptTarget{
		sessionID: sessionID, transcript: transcript, inputReceipt: receipt,
		agentInitiated: agentInitiated, done: make(chan struct{}),
	}
}

func (target *acpTranscriptTarget) prepare() error {
	if target == nil || target.transcript == nil {
		return nil
	}
	return target.transcript.Prepare()
}

type acpTranscriptAdmission struct {
	target  *acpTranscriptTarget
	capture RootTranscriptCapture
	writer  agent.TranscriptServices
	receipt agent.NativeInputReceipt
	shared  bool
	once    sync.Once
}

// The caller selects the bytes and reserves their position under turnMu.
func (target *acpTranscriptTarget) reserve() *acpTranscriptAdmission {
	if target == nil {
		return nil
	}
	var receipt agent.NativeInputReceipt
	if target.receiptStarted {
		receipt = target.inputReceipt
	}
	if target.transcript == nil {
		if receipt.TurnID == "" {
			return nil
		}
		return &acpTranscriptAdmission{target: target, receipt: receipt, shared: target.sharingReply}
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.sealed {
		panic("ACP reserved output from a sealed root transcript")
	}
	capture := target.transcript.Reserve()
	if capture == nil || capture.Writer() == nil {
		panic("ACP root transcript returned an invalid capture reservation")
	}
	target.admitted++
	return &acpTranscriptAdmission{target: target, capture: capture, writer: capture.Writer(), receipt: receipt, shared: target.sharingReply}
}

func (admission *acpTranscriptAdmission) transcript(fallback agent.TranscriptServices) agent.TranscriptServices {
	if !admission.captured() {
		return fallback
	}
	return admission.writer
}

func (admission *acpTranscriptAdmission) captured() bool {
	return admission != nil && admission.capture != nil
}

func (admission *acpTranscriptAdmission) tagReply(content agent.MessageContent) agent.MessageContent {
	if admission != nil && admission.receipt.TurnID != "" {
		return tagInputReply(content, admission.receipt, admission.shared)
	}
	return content
}

func (admission *acpTranscriptAdmission) release() {
	if !admission.captured() {
		return
	}
	admission.once.Do(func() {
		admission.capture.Complete()
		target := admission.target
		target.mu.Lock()
		target.admitted--
		finish := target.sealed && target.admitted == 0 && !target.finishing
		if finish {
			target.finishing = true
		}
		target.mu.Unlock()
		if finish {
			target.finish()
		}
	})
}

func (target *acpTranscriptTarget) seal(completion RootTranscriptCompletion) {
	if target == nil || target.transcript == nil {
		return
	}
	target.mu.Lock()
	if target.sealed {
		target.mu.Unlock()
		return
	}
	target.sealed = true
	target.completion = completion
	finish := target.admitted == 0
	if finish {
		target.finishing = true
	}
	target.mu.Unlock()
	if finish {
		target.finish()
	}
}

func (target *acpTranscriptTarget) finish() {
	if err := target.transcript.Finish(target.completion); err != nil && !agent.OutputRetained(err) {
		slog.Error("finish ACP root transcript", "session_id", target.sessionID, "error", err)
	}
	close(target.done)
}

func (target *acpTranscriptTarget) waitForCaptureCompletion() {
	if target != nil && target.transcript != nil {
		<-target.done
	}
}

type acpTranscriptSpanServices struct {
	agent.TranscriptServices
	agent.SpanServices
}
type rootTranscriptCallbacks struct {
	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
}

// TrackRootPromptCallback keeps a custom prompt transport's completion alive
// until it releases the callback. Wait closes admission before joining it.
func (b *Base) TrackRootPromptCallback() (release func(), err error) {
	return b.rootCallbacks.acquire()
}

func (callbacks *rootTranscriptCallbacks) acquire() (func(), error) {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	if callbacks.closing {
		return nil, errors.New("the agent no longer accepts root prompts")
	}
	callbacks.wg.Add(1)
	var once sync.Once
	return func() { once.Do(callbacks.wg.Done) }, nil
}
func (callbacks *rootTranscriptCallbacks) closeAdmission() {
	callbacks.mu.Lock()
	callbacks.closing = true
	callbacks.mu.Unlock()
}
func (callbacks *rootTranscriptCallbacks) wait() { callbacks.wg.Wait() }

func appendRootTarget(targets []*acpTranscriptTarget, target *acpTranscriptTarget) []*acpTranscriptTarget {
	if target == nil {
		return targets
	}
	for _, previous := range targets {
		if previous == target {
			return targets
		}
	}
	return append(targets, target)
}

// Reserve genuinely unowned text before the new physical group. Capture runs after unlock.
func (b *Base) preparePriorTextLocked(turn *acpTurnSnapshot) {
	if b.hooks.OpenRootTranscript != nil {
		b.reserveBufferedTextLocked(turn)
	}
}

// Reserve text without a root transcript capture before later rows can enter the queue.
func (b *Base) reserveBufferedTextLocked(turn *acpTurnSnapshot) {
	if (turn.thoughtText == "" || turn.thoughtAdmission.captured()) && (turn.assistantText == "" || turn.assistantAdmission.captured()) {
		return
	}
	batch, err := agent.ReserveCapturedTranscriptBatch(b.sink)
	if err != nil {
		slog.Error("reserve buffered ACP text", "agent_id", b.AgentID(), "error", err)
		return
	}
	turn.textBatch = batch
}

func (b *Base) finishRootTargets(turn acpTurnSnapshot, reason RootTranscriptEnd, completion agent.MessageCompletion, discard bool) {
	turn.endingAdmission.release()
	for _, target := range turn.rootTargets {
		target.seal(RootTranscriptCompletion{Reason: reason, Completion: completion, Discard: discard})
	}
}
func waitRootTargets(turn acpTurnSnapshot) {
	if turn.answeredPrompt != nil {
		waitRootTargets(*turn.answeredPrompt)
	}
	for _, target := range turn.rootTargets {
		target.waitForCaptureCompletion()
	}
}

func (b *Base) finishRootSnapshot(turn acpTurnSnapshot, textCompletion, toolCompletion agent.MessageCompletion, reason RootTranscriptEnd, ending func(agent.TranscriptServices, int)) int {
	if turn.cleanupCaptured {
		defer b.queueTurnOutputCleanup(turn.cleanup)
	}
	count := turn.completedToolUses + len(turn.incompleteTools)
	if b.IsDiscardingOutput() {
		if turn.textBatch.Discard() {
			if err := agent.OutputQueueFor(b.sink).Flush(); err != nil && !agent.OutputRetained(err) {
				slog.Error("release discarded ACP text", "agent_id", b.AgentID(), "error", err)
			}
		}
		turn.releaseTranscriptAdmissions()
		b.finishRootTargets(turn, reason, textCompletion, true)
		b.queueReplyReconciliation(turn.reconciliation)
		if !turn.hasPromptBoundary {
			b.ResetCumulativeOutput()
			b.sink.ReportProgress(agent.ResetProgress())
		}
		return count
	}
	main := b.main()
	main.persistBufferedText(turn, textCompletion)
	main.persistIncompleteTools(turn.incompleteTools, toolCompletion)
	b.clearCompletedTerminals()
	b.queueReplyReconciliation(turn.reconciliation)
	if ending != nil {
		ending(turn.endingAdmission.transcript(b.sink), count)
	}
	b.finishRootTargets(turn, reason, textCompletion, false)
	return count
}

// The caller holds turnMu and Mu. Capture provider state before any sink call
// can allow another turn or session to replace this snapshot's owner.
func (b *Base) captureRootSnapshotLocked(turn *acpTurnSnapshot, allTurns bool) {
	turn.sessionID = b.sessionID
	turn.interrupted = b.interruptRequested
	turn.cleanupCaptured = true
	if capture := b.hooks.CaptureTurnOutputCleanup; capture != nil && (allTurns || !b.agentTurnQueued) {
		turn.cleanup = capture()
	}
	if capture := b.hooks.CaptureReplyReconciliation; capture != nil && (allTurns || !b.agentTurnQueued) {
		turn.reconciliation = capture()
	}
	b.promptOutputDrained = true
}

func (b *Base) detachRootPrompt(keepTools bool) acpTurnSnapshot {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.takeRootPromptLocked(keepTools)
}

func (b *Base) takeRootPromptLocked(keepTools bool) acpTurnSnapshot {
	if answered := b.takeAnsweredPromptLocked(); answered != nil {
		if !keepTools {
			extra := b.drainOpenToolsOnlyLocked()
			answered.incompleteTools = append(answered.incompleteTools, extra.incompleteTools...)
		}
		return *answered
	}
	return b.snapshotRootPromptLocked(keepTools)
}

func (b *Base) snapshotRootPromptLocked(keepTools bool) acpTurnSnapshot {
	turn, _ := b.drainPromptTurnLocked(keepTools)
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, false)
	b.Mu.Unlock()
	return turn
}

// detachIdleRootTools drains open root tools during an idle Interrupt without
// mutating promptOutputDrained or touching any active rootTarget.
func (b *Base) detachIdleRootTools() acpTurnSnapshot {
	b.sessionUpdates.mu.Lock()
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.promptActive || b.agentTurnActive || b.agentTurnQueued {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		b.sessionUpdates.mu.Unlock()
		return acpTurnSnapshot{}
	}
	sessionID := b.sessionID
	b.Mu.Unlock()
	turn := b.drainOpenToolsOnlyLocked()
	turn.sessionID = sessionID
	turn.interrupted = true
	b.turnMu.Unlock()
	b.sessionUpdates.mu.Unlock()
	return turn
}

func (b *Base) detachAllRootOutput() acpTurnSnapshot {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.drainAllRootOutputLocked()
}

func (b *Base) replaceRootSession(updateProtocol func()) acpTurnSnapshot {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	turn := b.drainAllRootOutputLocked()
	b.closedSpans.reset()
	updateProtocol()
	return turn
}

func (b *Base) drainAllRootOutputLocked() acpTurnSnapshot {
	turn := b.drainTurnLocked(false)
	turn.answeredPrompt = b.takeAnsweredPromptLocked()
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, true)
	b.Mu.Unlock()
	return turn
}

func (b *Base) resetCompletedRootSpans(sessionID string, resetProgress bool) {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	b.Mu.Lock()
	reset := b.sessionID == sessionID && !b.agentTurnQueued && !b.agentTurnActive
	b.Mu.Unlock()
	if reset && b.rootTarget == nil {
		if resetProgress {
			providerkit.ResetSpansAndProgress(b.sink)
		} else {
			b.sink.ResetSpans()
		}
	}
}

func (b *Base) persistRootFailure(writer agent.TranscriptServices, err error) {
	raw, encodeErr := json.Marshal(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldError: fmt.Sprintf("prompt failed: %v", err)})
	if encodeErr != nil {
		slog.Error("encode ACP prompt failure", "error", encodeErr)
		return
	}
	if _, writeErr := writer.PersistWorkerNotification(agent.MessageContent{Original: raw}); writeErr != nil && !agent.OutputRetained(writeErr) {
		slog.Error("persist ACP prompt failure", "error", writeErr)
	}
}

type acpRootPromptRequest struct {
	sessionID          string
	target             *acpTranscriptTarget
	answered           *acpTurnSnapshot
	beforeDispatch     acpTurnSnapshot
	nativeReplyManaged bool
}

func (b *Base) takeAnsweredPromptLocked() *acpTurnSnapshot {
	request := b.rootPromptRequest
	b.rootPromptRequest = nil
	if request == nil {
		return nil
	}
	return request.answered
}

func (b *Base) ownsRootPromptEndLocked(sessionID string, target *acpTranscriptTarget, request *acpRootPromptRequest) bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.sessionID != sessionID || b.rootPromptRequest != request {
		return false
	}
	return (request != nil && request.answered != nil) || (b.promptTarget == target && !b.promptOutputDrained)
}

func (b *Base) observeRootPromptResponse(request *acpRootPromptRequest) {
	// This runs on the reader without sessionMu. Session replacement keeps the
	// output and session identity together under turnMu while the reader can
	// still deliver the response that replacement is waiting for.
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	if b.sessionUpdates.buffering {
		b.sessionUpdates.pending = append(b.sessionUpdates.pending, bufferedSessionUpdate{sessionID: request.sessionID, promptResponse: request})
		return
	}
	b.captureRootPromptResponse(request)
}

// The caller holds sessionUpdates.mu. The reader can accept the next text
// only after the old progress scope completes.
func (b *Base) captureRootPromptResponse(request *acpRootPromptRequest) {
	b.turnMu.Lock()
	if request.answered != nil || !b.ownsRootPromptEndLocked(request.sessionID, request.target, request) {
		b.turnMu.Unlock()
		return
	}
	b.Mu.Lock()
	stopped := b.StoppedLocked() || b.interruptRequested
	b.Mu.Unlock()
	turn := b.snapshotRootPromptLocked(!stopped)
	b.reserveBufferedTextLocked(&turn)
	turn.textProgressCompleted = true
	request.answered = &turn
	b.turnMu.Unlock()
	b.completeBufferedTextProgress(turn)
}

func (b *Base) completeBufferedTextProgress(turn acpTurnSnapshot) {
	main := b.main()
	if turn.thoughtText != "" {
		main.completeTextProgress(agent.AssembledMessageKindReasoning)
	}
	if turn.assistantText != "" {
		main.completeTextProgress(agent.AssembledMessageKindText)
	}
}

func (b *Base) sendOwnedPromptDetachedLocked(sessionID string, target *acpTranscriptTarget, content string, attachments []*leapmuxv1.Attachment, releaseOperation func()) error {
	params, err := b.prepareDetachedPromptLocked(sessionID, content, attachments, target)
	if err != nil {
		return err
	}
	var nativeSender func(func(json.RawMessage, error)) error
	if b.hooks.RootPromptTransport != nil && target != nil && target.inputReceipt.InputID != "" {
		nativeSender, err = b.hooks.RootPromptTransport(target.inputReceipt, params)
		if err != nil {
			return err
		}
	}
	request := &acpRootPromptRequest{sessionID: sessionID, target: target, nativeReplyManaged: nativeSender != nil}
	b.turnMu.Lock()
	b.rootPromptRequest = request
	b.turnMu.Unlock()
	handle := func(response json.RawMessage, err error) {
		defer releaseOperation()
		b.finishOwnedPromptRequest(sessionID, target, request, response, err)
	}
	if nativeSender != nil {
		return nativeSender(handle)
	}
	observation := providerkit.DetachedRequestObservation{
		Response: func(json.RawMessage) { b.observeRootPromptResponse(request) },
	}
	if target != nil && target.inputReceipt.TurnID != "" {
		observation.BeforeWrite = func() { b.startInputReply(request) }
	}
	err = b.SendDetachedRequestObserved(MethodSessionPrompt, params, observation, handle)
	if observation.BeforeWrite != nil {
		b.turnMu.Lock()
		preceding := request.beforeDispatch
		request.beforeDispatch = acpTurnSnapshot{}
		b.turnMu.Unlock()
		b.persistUnownedText(preceding)
	}
	return err
}

func (b *Base) finishUnsentPrompt(sessionID string) {
	b.sessionUpdates.mu.Lock()
	turn := b.detachRootPrompt(true)
	b.sessionUpdates.mu.Unlock()
	b.finishRootSnapshot(turn, agent.MessageCompletionError, agent.MessageCompletionError, RootTranscriptPromptNotSent, nil)
	// This request never ran. Do not ask the provider for another continuation
	// of the same failed write. A queued native turn still keeps its ownership.
	if end, queued := b.takeQueuedAgentTurn(sessionID); queued {
		if end != nil {
			b.endAgentTurn(*end)
		}
		return
	}
	b.clearActivePromptForSession(sessionID)
}
