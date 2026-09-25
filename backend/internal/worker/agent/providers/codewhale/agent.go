package codewhale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

// Agent runs one `codewhale app-server --http` process and drives one thread
// in it.
//
// The runtime API is REST plus a server-sent event stream, so this type does
// not embed JSONRPCProcess: it embeds the shared Process for the process
// lifecycle alone, and it talks to the runtime through an HTTPEndpoint.
//
// Every agent has its OWN process and its own task store, and each store holds
// one thread. The runtime takes an exclusive lock on its store, so two agents
// cannot share one, and a store that holds exactly one thread is what lets a
// resume of any thread take its store without taking another tab's thread with
// it. sessions.go states where the stores live.
type Agent struct {
	providerkit.Process

	sink       agent.ProviderServices
	workingDir string
	clock      quartz.Clock

	// endpoint and runtime are set once, before the agent is visible to any
	// other goroutine, and never change.
	endpoint *providerkit.HTTPEndpoint
	runtime  codewhaleRuntimeInfo
	store    codewhaleStore
	// stopProcess sends SIGTERM to the process group at once. The runtime does
	// not read its stdin, so the stdin close that Process.Stop starts with ends
	// nothing, and the grace that follows it would delay every Stop by its full
	// length.
	stopProcess context.CancelFunc

	// streamCancel ends the event stream, and streamDone closes when the stream
	// goroutine returns.
	streamCancel context.CancelFunc
	streamDone   chan struct{}

	// dispatchMu serializes event dispatch. The stream goroutine is the normal
	// caller; a test feeds events through HandleOutput. Take it only around one
	// event, never around an HTTP request: a handler that needs a request runs it
	// on its own goroutine.
	dispatchMu sync.Mutex

	// generationBuffer holds the streamed text of an item until the item's final
	// event, so a process that dies mid-message still leaves its text behind.
	generationBuffer providerkit.GenerationBuffer

	// children watches the subagents this thread started. See subagent.go.
	children *codewhaleChildren

	// --- guarded by Mu ---

	threadID string
	// turnID is the running turn, or "" when none runs. It is the ONE source of
	// the turn flag: PublishTurnActive reads it, SendInput refuses on it, and
	// Interrupt addresses it.
	turnID string
	// finishedTurns remembers the turns that already ended, so a start that
	// arrives after its own end -- the POST reply that raced the event stream --
	// cannot open a turn that nothing will close.
	finishedTurns turnSet
	// lastSeq is the highest event sequence number dispatched. The runtime
	// numbers events across ALL threads, so gaps are normal; a reconnect asks for
	// the events after it.
	lastSeq uint64
	// settings is what the thread record last reported, plus the effort LeapMux
	// sends with each turn. See settings.go.
	settings codewhaleSettings
	// catalog is the thread provider's model catalog as the runtime stated it,
	// and models is the same catalog converted for the option groups.
	catalog []providerModel
	models  []*agent.ModelInfo
	// tools holds every tool call of the thread that has not closed, keyed by its
	// span id. See output.go.
	tools codewhaleToolCalls
	// controls holds the control requests that LeapMux published and that
	// nothing resolved yet. See control.go.
	controls map[string]codewhalePendingControl
	// usage is the latest context reading. See usage.go.
	usage codewhaleUsage
	// shells holds the background shell jobs that run. See subagent.go.
	shells codewhaleShells
	// steers holds the steers that LeapMux sent and whose fate is not settled.
	// See steer.go.
	steers codewhaleSteers
}

var _ agent.Agent = (*Agent)(nil)

// turnSetCapacity limits how many finished turns an agent remembers. A start
// that arrives after its own end is at most a few turns late, so a short memory
// is enough, and it keeps the set from growing with the session.
const turnSetCapacity = 32

// turnSet is a small ring of finished turn ids.
type turnSet struct {
	ids  []string
	next int
}

func (s *turnSet) add(id string) {
	if id == "" || s.contains(id) {
		return
	}
	if len(s.ids) < turnSetCapacity {
		s.ids = append(s.ids, id)
		return
	}
	s.ids[s.next] = id
	s.next = (s.next + 1) % turnSetCapacity
}

func (s *turnSet) contains(id string) bool {
	for _, existing := range s.ids {
		if existing == id {
			return true
		}
	}
	return false
}

// PublishTurnActive republishes the turn state from turnID, the single source.
// Call it after every critical section that writes turnID, and never with Mu
// held: the sink broadcasts, and a broadcast can block.
//
// A running turn always accepts steering: the runtime's steer route takes any
// in-progress turn, including one that a goal continuation started.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.turnID != ""
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}

// markTurnStarted records a running turn. It is a no-op for a turn that
// already ended, and for the turn that already runs.
//
// It reports whether the turn flag changed, so the caller publishes only a real
// change.
func (a *Agent) markTurnStarted(turnID string) bool {
	if turnID == "" {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.finishedTurns.contains(turnID) || a.turnID == turnID {
		return false
	}
	a.turnID = turnID
	a.TurnToolUses = 0
	return true
}

// markTurnFinished records the end of a turn and reports whether it was the
// running one.
func (a *Agent) markTurnFinished(turnID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.finishedTurns.add(turnID)
	if turnID == "" || a.turnID != turnID {
		return false
	}
	a.turnID = ""
	return true
}

// SendInput starts a turn with the user's message. It returns once the runtime
// created the turn, never when the turn ends.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

// SendInputForSession starts a turn only when sessionID is still the agent's
// thread.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.threadID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID, turnID := a.threadID, a.turnID
	effort := a.settings.effort
	imageInput := a.currentImageInputLocked()
	a.Mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("the Codewhale agent has no thread")
	}
	// Normal dispatch never joins the running turn. Steering is its own
	// operation, and the runtime refuses a second turn anyway.
	if turnID != "" {
		return fmt.Errorf("%w: %s", agent.ErrAgentBusy, turnID)
	}

	prompt, images, err := buildTurnInput(content, agent.ClassifyAttachments(attachments), imageInput)
	if err != nil {
		return err
	}
	request := startTurnRequest{Prompt: prompt, Images: images}
	if effort != "" && effort != agent.EffortAuto {
		request.ReasoningEffort = effort
	}
	turn, err := a.startTurn(threadID, request)
	if err != nil {
		return classifyTurnStartError(err)
	}
	// The reply states the turn before its first event can, so the flag moves at
	// once and a second message waits for this turn rather than drawing a 409.
	if a.markTurnStarted(turn.ID) {
		a.PublishTurnActive()
	}
	return nil
}

// classifyTurnStartError maps a failed turn start onto the sentinels the input
// queue reads.
//
// A 409 is the runtime's own "Thread already has an active turn": the thread
// runs a turn this agent has not seen yet -- one that a goal continuation or a
// compaction started. That is transient, so the queue holds the message. A
// transport failure is an uncertain delivery, because the runtime may have
// created the turn before the reply was lost.
func classifyTurnStartError(err error) error {
	var status *providerkit.HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == httpStatusConflict {
			return fmt.Errorf("%w: %s", agent.ErrAgentBusy, status.Body)
		}
		return err
	}
	return fmt.Errorf("%w: Codewhale did not confirm the turn start: %v", agent.ErrDeliveryUncertain, err)
}

var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering is always true: the runtime's steer route takes any running
// turn, so the capability needs no discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds the message to the running turn.
//
// The steer route carries a prompt and nothing else, so a text attachment is
// inlined and an image attachment is refused rather than dropped.
//
// A nil return tells the queue that the steer is delivered. The runtime can
// still drop it later, and steer.go hands such a steer back to the queue.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID, turnID := a.threadID, a.turnID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		return agent.ErrNoActiveTurn
	}
	prompt, images, err := buildTurnInput(content, agent.ClassifyAttachments(attachments), imageInputSupported)
	if err != nil {
		return err
	}
	if len(images) > 0 {
		return fmt.Errorf("the Codewhale runtime cannot add an image to a running turn; send it as a new message")
	}
	a.Mu.Lock()
	ticket := a.steers.begin(turnID, prompt, content, attachments)
	a.Mu.Unlock()
	return a.settleSteerReply(ticket, a.steerTurn(threadID, turnID, prompt))
}

// Interrupt stops the running turn. It is a no-op when no turn runs.
//
// The runtime settles the turn itself: the pending approvals and questions end
// as cancelled, the running tools fail, and the turn completes as interrupted.
// Each of those arrives as an event, so this method changes no state.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID, turnID := a.threadID, a.turnID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		return nil
	}
	if err := a.interruptTurn(threadID, turnID, a.APITimeout()); err != nil {
		// The turn ended before the request arrived. Nothing is left to stop.
		if providerkit.IsHTTPStatus(err, httpStatusNotFound) || providerkit.IsHTTPStatus(err, httpStatusConflict) {
			return nil
		}
		return err
	}
	return nil
}

var _ agent.ContextCompactor = (*Agent)(nil)

// CompactContext runs the runtime's own compaction. The runtime runs it as a
// turn, so it waits for a running turn to end.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID, turnID := a.threadID, a.turnID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" {
		return fmt.Errorf("the Codewhale agent has no thread")
	}
	if turnID != "" {
		return fmt.Errorf("%w: %s", agent.ErrAgentBusy, turnID)
	}
	turn, err := a.compactThread(threadID)
	if err != nil {
		return classifyTurnStartError(err)
	}
	if a.markTurnStarted(turn.ID) {
		a.PublishTurnActive()
	}
	return nil
}

// ClearContext reports that Codewhale clears its context by a restart.
//
// A second thread in the same process is possible, but it breaks the rule that
// one store holds one thread: the old thread would stay locked inside this
// agent's store, and a resume of it in another tab would fail on the store lock
// for as long as this agent runs. A restart with no resume handle starts a
// fresh store instead, and it costs one runtime start.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// Stop ends the running turn, then tears the process down.
//
// The interrupt is best effort and has a time limit. It runs BEFORE the
// process stops, so the thread record states an interrupted turn rather than
// one that a restart must recover.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID, turnID := a.threadID, a.turnID
	a.Mu.Unlock()
	if !stopped && threadID != "" && turnID != "" && a.endpoint != nil {
		_ = a.interruptTurn(threadID, turnID, codewhaleStopInterruptTimeout)
	}
	a.children.stopAll()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	if a.stopProcess != nil {
		a.stopProcess()
	}
	a.Process.Stop()
	a.waitForStream()
	a.finishAfterExit(agent.MessageCompletionInterrupted)
	if a.endpoint != nil {
		a.endpoint.Close()
	}
}

// codewhaleStopInterruptTimeout limits the interrupt that Stop sends first.
const codewhaleStopInterruptTimeout = 2 * time.Second

// Wait retains the unfinished output of a process that exited.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.children.stopAll()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.waitForStream()
	a.finishAfterExit(a.ProcessExitCompletion())
	return err
}

// waitForStream waits for the event stream goroutine to return, so no event
// is dispatched after the tear-down below runs.
func (a *Agent) waitForStream() {
	if a.streamDone == nil {
		return
	}
	<-a.streamDone
}

// finishAfterExit settles what the process left open: the streamed text, the
// running tool calls, the pending control requests and the turn flag.
func (a *Agent) finishAfterExit(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.flushGeneration(completion)
	a.persistIncompleteTools(completion)
	a.withdrawAllControls()
	a.closeOpenChildren(completion)
	a.closeOpenShells()
	a.settleSteersAfterExit()
	a.Mu.Lock()
	cleared := a.turnID != ""
	a.finishedTurns.add(a.turnID)
	a.turnID = ""
	a.Mu.Unlock()
	if cleared {
		a.PublishTurnActive()
	}
	a.sink.ReportProgress(agent.ResetProgress())
}

// DiscardOutput drops the output of a process that is about to restart. The
// event stream reads the flag before it dispatches.
func (a *Agent) DiscardOutput() {
	a.Process.DiscardOutput()
}

// currentThreadID reads the thread under the lock.
func (a *Agent) currentThreadID() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.threadID
}

// shortText cuts a string to at most limit bytes for a log line. The cut never
// splits a rune, so the line stays valid UTF-8.
func shortText(text string, limit int) string {
	return validate.TruncateToBytes(strings.TrimSpace(text), limit)
}
