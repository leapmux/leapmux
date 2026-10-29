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
)

type RootTranscriptStart struct {
	SessionID      string
	NativeModeID   string
	AgentInitiated bool
	Preceding      RootTranscript
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
	sessionID         string
	transcript        RootTranscript
	mu                sync.Mutex
	admitted          int
	sealed, finishing bool
	completion        RootTranscriptCompletion
	done              chan struct{}
}

// The caller holds turnMu and has released Mu. The factory allocates only.
func (b *Base) openRootTranscriptLocked(sessionID string, agentInitiated bool, preceding *acpTranscriptTarget) *acpTranscriptTarget {
	if b.hooks.OpenRootTranscript == nil {
		return nil
	}
	var previous RootTranscript
	if preceding != nil {
		previous = preceding.transcript
	}
	b.Mu.Lock()
	nativeMode := b.nativeModeID
	b.Mu.Unlock()
	transcript := b.hooks.OpenRootTranscript(RootTranscriptStart{SessionID: sessionID, NativeModeID: nativeMode, AgentInitiated: agentInitiated, Preceding: previous}, b.sink)
	if transcript == nil {
		return nil
	}
	return &acpTranscriptTarget{sessionID: sessionID, transcript: transcript, done: make(chan struct{})}
}

func (target *acpTranscriptTarget) prepare() error {
	if target == nil {
		return nil
	}
	return target.transcript.Prepare()
}

type acpTranscriptAdmission struct {
	target  *acpTranscriptTarget
	capture RootTranscriptCapture
	writer  agent.TranscriptServices
	once    sync.Once
}

// The caller selects the bytes and reserves their position under turnMu.
func (target *acpTranscriptTarget) reserve() *acpTranscriptAdmission {
	if target == nil {
		return nil
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
	return &acpTranscriptAdmission{target: target, capture: capture, writer: capture.Writer()}
}

func (admission *acpTranscriptAdmission) transcript(fallback agent.TranscriptServices) agent.TranscriptServices {
	if admission == nil {
		return fallback
	}
	return admission.writer
}

func (admission *acpTranscriptAdmission) release() {
	if admission == nil {
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
	if target == nil {
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
	if target != nil {
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
	if b.hooks.OpenRootTranscript == nil {
		return
	}
	if (turn.thoughtText == "" || turn.thoughtAdmission != nil) && (turn.assistantText == "" || turn.assistantAdmission != nil) {
		return
	}
	batch, err := agent.ReserveCapturedTranscriptBatch(b.sink)
	if err != nil {
		panic(fmt.Sprintf("reserve preceding ACP transcript: %v", err))
	}
	turn.priorText = batch
}

func (b *Base) finishRootTargets(turn acpTurnSnapshot, reason RootTranscriptEnd, completion agent.MessageCompletion, discard bool) {
	turn.endingAdmission.release()
	for _, target := range turn.rootTargets {
		target.seal(RootTranscriptCompletion{Reason: reason, Completion: completion, Discard: discard})
	}
}
func waitRootTargets(turn acpTurnSnapshot) {
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
		turn.releaseTranscriptAdmissions()
		b.finishRootTargets(turn, reason, textCompletion, true)
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
	b.promptOutputDrained = true
}

func (b *Base) detachRootPrompt() acpTurnSnapshot {
	b.turnMu.Lock()
	turn, _ := b.drainPromptTurnLocked()
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, false)
	b.Mu.Unlock()
	b.turnMu.Unlock()
	return turn
}

func (b *Base) detachAllRootOutput() acpTurnSnapshot {
	b.turnMu.Lock()
	turn := b.drainTurnLocked()
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, true)
	b.Mu.Unlock()
	b.turnMu.Unlock()
	return turn
}

func (b *Base) replaceRootSession(updateProtocol func()) acpTurnSnapshot {
	b.turnMu.Lock()
	turn := b.drainTurnLocked()
	b.Mu.Lock()
	b.captureRootSnapshotLocked(&turn, true)
	b.Mu.Unlock()
	updateProtocol()
	b.turnMu.Unlock()
	return turn
}

func (b *Base) resetCompletedRootSpans(sessionID string) {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	b.Mu.Lock()
	reset := b.sessionID == sessionID && !b.agentTurnQueued && !b.agentTurnActive
	b.Mu.Unlock()
	if reset && b.rootTarget == nil {
		b.sink.ResetSpans()
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

func (b *Base) sendOwnedPromptDetachedLocked(sessionID string, target *acpTranscriptTarget, content string, attachments []*leapmuxv1.Attachment, releaseOperation func()) error {
	return b.sendPromptDetachedLocked(sessionID, content, attachments, target, func(response json.RawMessage, err error) {
		defer releaseOperation()
		b.finishOwnedPromptRequest(sessionID, target, response, err)
	})
}

func (b *Base) finishUnsentPrompt(sessionID string) {
	b.sessionUpdates.mu.Lock()
	turn := b.detachRootPrompt()
	b.sessionUpdates.mu.Unlock()
	b.finishRootSnapshot(turn, agent.MessageCompletionError, agent.MessageCompletionError, RootTranscriptPromptNotSent, nil)
	// This request never ran. Do not ask the provider for another continuation
	// of the same failed write. A queued native turn still keeps its ownership.
	if b.handoffQueuedAgentTurn(sessionID) {
		return
	}
	b.clearActivePromptForSession(sessionID)
}
