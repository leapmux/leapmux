package claude

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages a single Claude Code process.
type Agent struct {
	providerkit.Process // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	sessionID  string
	effort     string
	workingDir string
	homeDir    string
	sink       agent.ProviderServices

	// Claude Code-specific state.
	contextUsage         *contextUsageSnapshot
	compactionLive       bool
	turnActive           bool
	inputMu              sync.Mutex
	turnEndRevision      uint64
	turnInputRevision    uint64
	inputReply           *claudeInputReply
	inferredTurnEvidence *claudeInferredTurnEvidence
	// sessionStateReported records that this CLI publishes
	// `system`/`session_state_changed`. Once it does, the output heuristic
	// stands down for the life of the process: the CLI states its own turn
	// state, so nothing else needs to be read as evidence of one, and no frame
	// the vendor adds later can start a turn that nothing ends.
	sessionStateReported bool
	nativeBusyRevision   uint64

	// awaitedInputRevision identifies the input whose result the CLI still owes.
	// Zero means the agent awaits no result. An older idle cannot release a queued prompt.
	awaitedInputRevision uint64
	// interruptRequested records that the user stopped the running turn.
	// The result takes the note. An idle report clears any unused note.
	// Guarded by a.Mu. See noteInterruptRequested.
	interruptRequested     bool
	thirdPartyFromSettings bool // third-party LLM provider detected from settings at startup
	// hasGoalCommand records whether the running CLI advertises /goal in its
	// init frame's slash_commands. The command shipped in 2.1.139, so this is
	// read from the process rather than assumed -- see observeSlashCommands.
	// Guarded by a.Mu.
	hasGoalCommand bool
	// goalCommandKnown records whether the init frame arrived at all. Absent is
	// not the same answer as false: before the frame the capability is UNKNOWN,
	// and ObserveGoalCommand must not drop a delivered command for a process
	// that has the feature but has not said so yet. Guarded by a.Mu.
	goalCommandKnown bool

	controlReplies          providerkit.Correlator[string]
	heldAPIErrorMu          sync.Mutex
	heldAPIError            *claudeHeldAPIError
	transcripts             claudeTranscriptIndex
	readerFinalReservation  chan struct{}
	finalHoldOnce           sync.Once
	confirmedPermissionMode string
	// The request identity exists before its first write. A deferred request
	// supplies the displayed mode until its matching reply arrives.
	permissionModeChange *permissionModeChange
	permissionModeMu     sync.Mutex
	incomingControls     map[string]*claudePendingControl
	approvalWork         providerkit.CoalescingWork
	// unresolvedSettings records flag axes whose last write had no successful
	// get_settings readback.
	unresolvedSettings map[string]struct{}

	// Settings state from initialize response and runtime updates.
	outputStyle           string
	availableOutputStyles []string
	fastMode              string // "on" / "off"
	alwaysThinking        string // "on" / "off"
	autoModeAvailable     bool

	// availableModels is the model catalog discovered from the initialize
	// response. It is written only during Start's pre-registration
	// startup handshake (convertClaudeModels, then a possible ensureSettledModelListed
	// insert) and never mutated afterward, so reads are safe without a.Mu (callers may
	// already hold it). nil/empty ⇒ fall back to the static claudeCodeAvailableModels
	// catalog.
	availableModels []*agent.ModelInfo
	// Native thinking capabilities stay immutable after startup.
	thinkingOffSupport map[string]bool
	// Mu guards the account-default resolution when the first native readback arrives.
	accountDefaultModel string

	// tasks indexes everything this agent knows about a Claude task. It carries
	// its own mutex, so the eight maps no longer contend on Process.Mu --
	// the process-lifecycle lock every provider embeds, which guards `stopped`,
	// the model, and the effort. A value, not a pointer: Agent is
	// built at many sites that do not go through Start, and a nil
	// receiver would be reachable from every one of them.
	tasks       claudeTaskIndex
	outputOrder claudeOutputOrder
	// workflows draws the agent rows of Workflow runs. A value, not a pointer,
	// for the same reason as tasks.
	workflows claudeWorkflowRuns
}

func (a *Agent) Stop() {
	a.Process.Stop()
	a.finishClaudeProcessExit()
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.finishClaudeProcessExit()
	return err
}

func (a *Agent) finishClaudeProcessExit() {
	a.joinFinalHoldReservation()
	a.retireClaudeInputReply()
	a.withdrawControls()
	a.sink.ReportProgress(agent.ResetProgress())
}

func (a *Agent) liveClaudeOutput() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.liveClaudeOutputLocked()
}

func (a *Agent) liveClaudeOutputLocked() bool {
	if a.StoppedLocked() || a.ProcessExitedLocked() {
		return false
	}
	select {
	case <-a.ProcessDone():
		return false
	default:
		return true
	}
}

// Interrupt aborts the current turn by sending the Claude Code
// interrupt control_request. This matches the wire format the
// frontend's buildInterruptRequest produced before the dedicated RPC,
// and the receiving claudeProvider.IsInterrupt detects:
//
//	{"type":"control_request","request_id":"...",
//	 "request":{"subtype":"interrupt"}}
//
// The call returns the underlying error when the process exited or the stop prevents delivery.
// Claude Code acknowledges an interrupt outside a turn without an error.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	// Noted BEFORE the request goes out, the way every other provider notes its
	// own stop. The reader goroutine hands the acknowledgement to the wait below
	// and reads the NEXT line at once, and that line is the `result` of the turn
	// the interrupt just aborted -- so a note taken after the wait races the
	// native result handler that spends it, and loses whenever the reader wins.
	// The turn then reads as the failure its subtype claims.
	//
	// A failed send leaves the note standing, because the failure that reaches
	// here is a control TIMEOUT far more often than a lost write, and the CLI
	// that answered late still aborted the turn. The note costs nothing if it is
	// wrong: an idle report drops an unused note.
	a.noteInterruptRequested()
	// Use the agent's own context so a process exit unblocks the
	// wait. APITimeout caps how long we hold the caller; the control
	// protocol itself is fast (single round-trip).
	_, err := a.sendControlAndWait(a.Context(), `{"subtype":"interrupt"}`, a.APITimeout())
	return err
}

// noteInterruptRequested records that the USER stopped the running turn, so the
// `result` that ends it can carry LeapMux's own completion.
//
// The command-line interface reports an interrupted turn as
// `subtype: error_during_execution` with `is_error: true`, which is the same shape it
// uses for a genuine failure, and its `errors` array carries its own diagnostics. The
// subtype therefore cannot tell the two apart. LeapMux can: it asked for the stop.
//
// The note does not mark the turn alone. The CLI can end the turn before it reads
// the stop, and that turn keeps its own outcome. The `result` marks the turn only when
// it also states the abort: see messageEnvelope.statesAbortedTurn.
//
// The note is taken only while a turn is running. Claude acknowledges an interrupt
// sent outside a turn and sends no `result` for it, so a note taken there would wait
// and then mislabel the NEXT turn's outcome.
func (a *Agent) noteInterruptRequested() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.turnActive {
		a.interruptRequested = true
	}
}

// SendInput writes a user message to the agent's stdin.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, "")
}

var _ agent.InputReceiptSender = (*Agent)(nil)

func (a *Agent) SendInputWithReceipt(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.writeClaudeInput(nil, content, attachments, "", uuid.NewString())
}

// PublishTurnActive republishes the Worker-visible turn state from turnActive,
// the single source. Call it after EVERY critical section that writes that
// field.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and a missing call is the only way the two can drift.
// Never called with a.Mu held: the sink broadcasts, and a broadcast can block on
// a slow transport.
//
// seq comes from the SAME critical section that reads the flag. Two goroutines
// reach the sink unordered -- the reader that ends a turn, and the drain that a
// refusal answers -- so without it the older value can land second and latch a
// turn that is over.
func (a *Agent) PublishTurnActive() agent.TurnState {
	return a.publishTurnActive(a.sink)
}

func (a *Agent) publishTurnActive(sink agent.TurnServices) agent.TurnState {
	a.Mu.Lock()
	active := a.turnActive
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(sink, active, seq)
}

// armTurn records a turn that the CLI runs and this Worker did not start. The
// output handler calls it for root output that only a live turn produces.
//
// It publishes on the rising edge alone. Every assistant message of one turn
// reaches it, and the publish reconciles the Worker's input queue, so a
// streaming turn would otherwise repeat that work once per message block.
func (a *Agent) armTurn() {
	a.Mu.Lock()
	if !a.liveClaudeOutputLocked() {
		a.Mu.Unlock()
		return
	}
	already := a.turnActive
	a.turnActive = true
	a.Mu.Unlock()
	if already {
		return
	}
	a.PublishTurnActive()
}

// publishesSessionState reports whether this CLI stated its own turn state at
// least once.
func (a *Agent) publishesSessionState() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.sessionStateReported
}

// noteSessionIdle applies the CLI's own idle report.
//
// An idle is stale after a later native busy state or while a sent input awaits its result.
// Claude can emit idle before it reads that input. Accepting that idle would release the queue into the new turn.
// Every other idle clears the turn and any unused stop note.
func (a *Agent) noteSessionIdle(busyRevision uint64, sink agent.TurnServices) {
	a.Mu.Lock()
	if a.awaitedInputRevision != 0 || a.nativeBusyRevision != busyRevision {
		a.Mu.Unlock()
		return
	}
	retired := a.disarmTurnLocked()
	a.Mu.Unlock()
	a.cancelControls(retired)
	a.publishTurnActive(sink)
}

// disarmTurnLocked keeps the idle check and the turn change under the same lock.
func (a *Agent) disarmTurnLocked() map[string]string {
	a.turnActive = false
	return a.recordClaudeResultEdgeLocked()
}

func (a *Agent) recordClaudeResultEdgeLocked() map[string]string {
	a.turnEndRevision++
	// A stop note cannot carry into a later result or turn.
	a.interruptRequested = false
	return a.takeControlsLocked(func(record *claudePendingControl) bool { return record.agentID == "" })
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering always reports true. The Claude Code stream-json protocol
// accepts a priority:"next" user message during any turn, so the capability
// needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput writes the input as a priority:"next" user message. Claude Code
// folds such a message into the running turn as a queued command, and that fold
// keeps only the text blocks and the image blocks (the queued_command
// attachment of 2.1.289). A PDF would lose its document block there. SteerInput
// therefore refuses an input that carries a file other than a text file or an
// image as unsupported, and the worker sends that input as the next turn, where
// the CLI reads every block. The refusal depends on the input alone, so it
// comes before the turn check.
func (a *Agent) SteerInput(request agent.SteerRequest) error {
	content, attachments := request.Content, request.Attachments
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText && attachment.Kind != contracts.AttachmentMediaKindImage {
			return fmt.Errorf("%w: a steered turn of Claude Code keeps text and images only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
	}
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	return a.sendInput(content, attachments, "next")
}

func (a *Agent) sendInput(content string, attachments []*leapmuxv1.Attachment, priority string) error {
	return a.sendInputForSession(nil, content, attachments, priority)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, priority string) error {
	_, err := a.writeClaudeInput(expected, content, attachments, priority, "")
	return err
}

func (a *Agent) writeClaudeInput(expected *string, content string, attachments []*leapmuxv1.Attachment, priority, inputID string) (agent.NativeInputReceipt, error) {
	// Serialize input writes without blocking state updates or Stop on the process mutex.
	defer a.PublishTurnActive()
	a.inputMu.Lock()
	defer a.inputMu.Unlock()
	msg := UserInputMessage{
		Type:     MessageTypeUser,
		UUID:     inputID,
		Priority: priority,
		Message: UserInputContent{
			Role: "user",
		},
	}

	if classified := agent.ClassifyAttachments(attachments); len(classified) == 0 {
		// Plain text uses the protocol's string content. A list that holds only
		// nil entries carries no attachment, so it is plain text too.
		msg.Message.Content = content
	} else {
		msg.Message.Content = buildClaudeContentBlocks(content, classified)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return agent.NativeInputReceipt{}, fmt.Errorf("marshal input: %w", err)
	}

	data = append(data, '\n')
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, fmt.Errorf("agent is stopped")
	}
	inferredTurnPending := !a.sessionStateReported && a.inferredTurnEvidence != nil && a.inferredTurnEvidence.pending > 0
	if priority == "" && (a.turnActive || inferredTurnPending) {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	endRevision := a.turnEndRevision
	a.turnInputRevision++
	inputRevision := a.turnInputRevision
	var reply *claudeInputReply
	var retired *claudeInputReply
	if priority == "" {
		a.awaitedInputRevision = inputRevision
		retired = a.inputReply
		a.inputReply = nil
		if inputID != "" && a.sessionID != "" {
			reply = &claudeInputReply{receipt: agent.NativeInputReceipt{SessionID: a.sessionID, InputID: inputID}}
			a.inputReply = reply
		}
	}
	a.Mu.Unlock()
	a.retireUnprovedClaudeReply(retired)
	writeErr := a.WriteStdin(data)
	a.Mu.Lock()
	var receipt agent.NativeInputReceipt
	if reply != nil {
		if writeErr == nil || reply.acknowledged {
			receipt = reply.receipt
		} else if a.inputReply == reply {
			a.inputReply = nil
		}
	}
	delivered := writeErr == nil || receipt.InputID != ""
	if priority == "next" && delivered && !a.StoppedLocked() && a.turnEndRevision == endRevision {
		// Do not wait again for a result that arrived during the write.
		a.awaitedInputRevision = inputRevision
	}
	if priority == "" && a.turnEndRevision == endRevision {
		if delivered && !a.StoppedLocked() {
			a.turnActive = true
		} else if !a.turnActive {
			a.awaitedInputRevision = 0
		}
	}
	a.Mu.Unlock()
	if !delivered {
		return agent.NativeInputReceipt{}, fmt.Errorf("write stdin: %w", writeErr)
	}
	return receipt, nil
}

// buildClaudeContentBlocks converts the prompt text and the classified
// attachments into Claude Code's content block array.
//
// Every attachment comes first, in the order that the user attached it, and the
// prompt text comes last. Claude Code reads the prompt of an array message from
// its LAST block, and only when that block is text (processUserInputBase). The
// prompt controls this per-prompt work:
//   - The slash-command dispatch.
//   - The @-mention and skill-mention expansion.
//   - The "ultrathink" keyword.
//
// A prompt before the attachments makes the CLI do that work incorrectly. The
// model still receives every block. The failure depends on the last block:
//   - An image or a document gives no prompt, so the CLI skips the work.
//   - A text attachment makes the CLI do the work on the attached file instead
//     of on the prompt.
//
// Claude Code's own producers use the same order. Its handler for inbound file
// attachments puts the images before the text, and it adds @-path references to
// the last text block.
//
// An empty prompt adds no text block. The CLI sends the blocks to the Messages
// API, and that API refuses an empty text block.
func buildClaudeContentBlocks(content string, classified []agent.ClassifiedAttachment) []interface{} {
	blocks := make([]interface{}, 0, len(classified)+1)
	for _, attachment := range classified {
		blocks = append(blocks, claudeAttachmentBlock(attachment))
	}
	if content != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	return blocks
}

// claudeAttachmentBlock converts one attachment into its content block: a text
// block for a text file, a document block for a PDF, and an image block for an
// image. ValidateAttachment refuses a binary file before it reaches here.
func claudeAttachmentBlock(attachment agent.ClassifiedAttachment) map[string]interface{} {
	switch attachment.Kind {
	case contracts.AttachmentMediaKindText:
		return map[string]interface{}{
			"type": "text",
			"text": providerkit.BuildInlineTextAttachmentBlock(attachment),
		}
	case contracts.AttachmentMediaKindPdf:
		return claudeBase64Block("document", attachment)
	default:
		return claudeBase64Block("image", attachment)
	}
}

// claudeBase64Block carries the bytes of one attachment in a block of
// blockType, with a base64 source.
func claudeBase64Block(blockType string, attachment agent.ClassifiedAttachment) map[string]interface{} {
	return map[string]interface{}{
		"type": blockType,
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": attachment.MIMEType,
			"data":       base64.StdEncoding.EncodeToString(attachment.Data),
		},
	}
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, "")
}
