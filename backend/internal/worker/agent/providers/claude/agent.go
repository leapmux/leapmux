package claude

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

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
	contextUsage    *contextUsageSnapshot
	lastAgentStatus string
	turnActive      bool
	inputMu         sync.Mutex
	turnEndRevision uint64
	// sessionStateReported records that this CLI publishes
	// `system`/`session_state_changed`. Once it does, the output heuristic
	// stands down for the life of the process: the CLI states its own turn
	// state, so nothing else needs to be read as evidence of one, and no frame
	// the vendor adds later can start a turn that nothing ends.
	sessionStateReported bool

	// awaitingResult covers the input write and its reply. An unsuccessful write clears it unless native output proves a turn started.
	// This prevents an older idle notification from releasing a queued prompt before the CLI reads it.
	awaitingResult bool
	// interruptRequested records that the user stopped the RUNNING turn. The
	// `result` that ends that turn spends the note, and disarmTurn drops a note that
	// no `result` spent. Guarded by a.Mu. See noteInterruptRequested.
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

	pendingControlMu        sync.Mutex
	pendingControl          map[string]chan<- claudeCodeControlResult
	confirmedPermissionMode string
	// deferredPermissionModeReqID is the request_id of the LATEST set_permission_mode toggle
	// whose ack the CLI deferred (it holds the response until the active turn ends). Guarded by
	// a.Mu, alongside confirmedPermissionMode. claudeCodeHandleControlResponse folds back ONLY
	// the deferred ack whose request_id matches this, so a stale/duplicate ack -- or an earlier
	// toggle's ack arriving after a newer toggle superseded it -- can't clobber the confirmed
	// mode. Tracking only the latest (a later toggle overwrites it) means a superseded toggle's
	// ack is ignored, leaving the mode the user last asked for. Empty when nothing is pending.
	deferredPermissionModeReqID string
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

	// tasks indexes everything this agent knows about a Claude task. It carries
	// its own mutex, so the eight maps no longer contend on Process.Mu --
	// the process-lifecycle lock every provider embeds, which guards `stopped`,
	// the model, and the effort. A value, not a pointer: Agent is
	// built at many sites that do not go through Start, and a nil
	// receiver would be reachable from every one of them.
	tasks claudeTaskIndex
}

func (a *Agent) Stop() {
	a.Process.Stop()
	a.sink.ReportProgress(agent.ResetProgress())
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

// Interrupt aborts the current turn by sending the Claude Code
// interrupt control_request. This matches the wire format the
// frontend's buildInterruptRequest produced before the dedicated RPC,
// and the receiving claudeProvider.IsInterrupt detects:
//
//	{"type":"control_request","request_id":"...",
//	 "request":{"subtype":"interrupt"}}
//
// The request is best-effort: if the agent has already exited or is
// mid-stop the call returns the underlying error so the caller can
// surface it, but a no-active-turn agent won't fail — Claude Code
// silently ack's interrupts received outside a turn.
func (a *Agent) Interrupt() error {
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	// Noted BEFORE the request goes out, the way every other provider notes its
	// own stop. The reader goroutine hands the acknowledgement to the wait below
	// and reads the NEXT line at once, and that line is the `result` of the turn
	// the interrupt just aborted -- so a note taken after the wait races the
	// takeInterruptRequest that spends it, and loses whenever the reader wins.
	// The turn then reads as the failure its subtype claims.
	//
	// A failed send leaves the note standing, because the failure that reaches
	// here is a control TIMEOUT far more often than a lost write, and the CLI
	// that answered late still aborted the turn. The note costs nothing if it is
	// wrong: disarmTurn drops one that no `result` spent.
	a.noteInterruptRequested()
	// Use the agent's own context so a process exit unblocks the
	// wait. APITimeout caps how long we hold the caller; the control
	// protocol itself is fast (single round-trip).
	_, err := a.sendControlAndWait(a.Context(), `{"subtype":"interrupt"}`, a.APITimeout())
	return err
}

// noteInterruptRequested records that the USER stopped the running turn, so the
// `result` that ends it carries LeapMux's own completion.
//
// The command-line interface reports an interrupted turn as
// `subtype: error_during_execution` with `is_error: true`, which is the same shape it
// uses for a genuine failure, and its `errors` array carries its own diagnostics. The
// subtype therefore cannot tell the two apart. LeapMux can: it asked for the stop.
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

// takeInterruptRequest reports whether the turn that is ending was interrupted, and
// clears the note. One `result` ends one turn, so the note is spent there.
func (a *Agent) takeInterruptRequest() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	interrupted := a.interruptRequested
	a.interruptRequested = false
	return interrupted
}

// SendInput writes a user message to the agent's stdin.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, "")
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
	a.Mu.Lock()
	active := a.turnActive
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}

// armTurn records a turn that the CLI runs and this Worker did not start. The
// output handler calls it for root output that only a live turn produces.
//
// It publishes on the rising edge alone. Every assistant message of one turn
// reaches it, and the publish reconciles the Worker's input queue, so a
// streaming turn would otherwise repeat that work once per message block.
func (a *Agent) armTurn() {
	a.Mu.Lock()
	already := a.turnActive
	a.turnActive = true
	a.Mu.Unlock()
	if already {
		return
	}
	a.PublishTurnActive()
}

// noteSessionState applies one session_state_changed frame, and records that
// this CLI publishes them at all.
//
// That record is what retires the output heuristic. The frame states the turn
// state that armTurnFromOutput can only infer, so a build that sends it needs no
// inference. Only the signals this file specifies can then start a turn.
// A message that the vendor adds later stays inert.
func (a *Agent) noteSessionState(state string) {
	a.Mu.Lock()
	a.sessionStateReported = true
	a.Mu.Unlock()
	if state == claudeSessionStateIdle {
		a.noteSessionIdle()
		return
	}
	a.armTurn()
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
// It REFUSES an idle that arrives while a message this Worker sent has no
// `result` yet. Claude Code emits idle when its run loop stops, before it reads
// the next message, so such a frame describes the state before that message --
// and the turn it would clear is the one about to start. The Worker's input
// queue follows this flag, so the clear would dispatch the next message INTO
// the running turn, which is the failure the flag exists to prevent.
//
// Every other idle clears the turn, and that is the point of reading the frame
// at all. A turn armed from output has only `result` to end it, so one armed by
// a frame that no turn produced would otherwise hold the queue until the process
// exits.
func (a *Agent) noteSessionIdle() {
	a.Mu.Lock()
	stale := a.awaitingResult
	if !stale {
		a.turnActive = false
	}
	a.Mu.Unlock()
	if stale {
		return
	}
	a.PublishTurnActive()
}

// disarmTurn records the end of the turn and publishes it, the way armTurn
// records the start. ProviderServices.SetTurnState requires the mutation and the
// publish at ONE site, and the falling edge kept them twenty lines apart in the
// middle of the output handler.
//
// The caller decides WHEN. The publish releases the Worker's input queue, so
// the next message dispatches on it and must find the finished turn's spans
// already reset.
func (a *Agent) disarmTurn() {
	a.Mu.Lock()
	a.turnEndRevision++
	a.turnActive = false
	// The `result` this ends is the answer to whatever the Worker sent, so a
	// later idle is no longer stale.
	a.awaitingResult = false
	// The note belongs to the turn that just ended. A turn end that ran without one
	// (a process exit, a refused dispatch) must not leave it for the next turn.
	a.interruptRequested = false
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering always reports true. The Claude Code stream-json protocol
// accepts a priority:"next" user message during any turn, so the capability
// needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
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
	// Serialize input writes without blocking state updates or Stop on the process mutex.
	defer a.PublishTurnActive()
	a.inputMu.Lock()
	defer a.inputMu.Unlock()
	msg := UserInputMessage{
		Type:     MessageTypeUser,
		Priority: priority,
		Message: UserInputContent{
			Role: "user",
		},
	}

	if len(attachments) == 0 {
		// Plain text uses the protocol's string content.
		msg.Message.Content = content
	} else {
		// Multimodal — build a content block array.
		blocks := buildClaudeContentBlocks(content, agent.ClassifyAttachments(attachments))
		msg.Message.Content = blocks
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}

	data = append(data, '\n')
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if priority == "" && a.turnActive {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	endRevision := a.turnEndRevision
	if priority == "" {
		a.awaitingResult = true
	}
	a.Mu.Unlock()
	writeErr := a.WriteStdin(data)
	a.Mu.Lock()
	if priority == "" && a.turnEndRevision == endRevision {
		if writeErr == nil && !a.StoppedLocked() {
			a.turnActive = true
		} else if !a.turnActive {
			a.awaitingResult = false
		}
	}
	a.Mu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("write stdin: %w", writeErr)
	}
	return nil
}

// buildClaudeContentBlocks converts text + classified attachments into Claude
// Code's content block format: text blocks, image blocks (base64), and document
// blocks (PDF).
func buildClaudeContentBlocks(content string, classified []agent.ClassifiedAttachment) []interface{} {
	var blocks []interface{}
	if content != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	for _, attachment := range classified {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, map[string]interface{}{
				"type": "text",
				"text": providerkit.BuildInlineTextAttachmentBlock(attachment),
			})
		case agent.AttachmentKindPDF:
			blocks = append(blocks, map[string]interface{}{
				"type": "document",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.MIMEType,
					"data":       base64.StdEncoding.EncodeToString(attachment.Data),
				},
			})
		default:
			blocks = append(blocks, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.MIMEType,
					"data":       base64.StdEncoding.EncodeToString(attachment.Data),
				},
			})
		}
	}
	return blocks
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, "")
}
