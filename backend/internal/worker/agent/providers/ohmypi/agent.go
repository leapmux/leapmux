package ohmypi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// promptAckTimeout limits the wait for omp to acknowledge a prompt.
//
// omp acknowledges a prompt at once, before its preflight, but its command queue
// runs one ordinary command at a time: a prompt that arrives while a slow command
// runs waits for it. Past this limit the delivery is uncertain rather than failed,
// because omp can still run the prompt.
const promptAckTimeout = 10 * time.Second

// abortWaitOnStop limits the wait for the abort that Stop sends before it closes
// stdin. omp answers an abort only after the run it stops has ended, so the wait
// is best effort.
const abortWaitOnStop = 2 * time.Second

// The tags of the agent's timers. A test traps a timer by its tag.
const (
	// ompPromptAckTimerTag limits the wait for omp to acknowledge a prompt.
	ompPromptAckTimerTag = "omp-prompt-ack"
	// ompReadyTimerTag limits the start's wait for omp's ready frame.
	ompReadyTimerTag = "omp-ready"
)

// Agent manages one `omp --mode rpc-ui` process.
//
// omp's wire format is JSONL but not JSON-RPC 2.0: a command carries an opaque
// string `id`, and the response echoes it on a flat {type:"response"} frame. The
// agent shares only the pending-map mechanics, through Correlator[string].
type Agent struct {
	providerkit.Process
	providerkit.Correlator[string]

	sink       agent.ProviderServices
	workingDir string

	// nextReqID mints the command ids.
	nextReqID atomic.Int64

	// ready receives the ready frame, once. See awaitReady.
	ready     chan readyFrame
	readyOnce sync.Once
	// chunks reassembles rpc_chunk frames. Only the read loop touches it, so it
	// needs no lock.
	chunks chunkAssembler

	// sessionMu serializes the operations that address or replace the session:
	// input, context compaction and ClearContext. It is never held while a
	// command waits for a turn to end.
	sessionMu sync.Mutex

	// The fields below are guarded by Process.Mu.

	// model is the running model as `<provider>/<id>`, the form omp's `--model`
	// flag takes and the form the model option group lists.
	model string
	// thinkingLevel is the reader's thinking level, stored as the agent's effort.
	// agent.EffortAuto means "omp's configured default": the worker sent none.
	thinkingLevel string
	// effectiveThinking is the level omp runs now, as its get_state response and
	// its thinking_level_changed frames state it. omp clamps a requested level to
	// what the model offers and reports only a level that MOVED, so this is the
	// one record of the level a set_thinking_level settled on.
	effectiveThinking string
	// approvalMode is the tool approval mode the process launched with. omp
	// changes it at launch only, so it never moves while the process runs.
	approvalMode    string
	availableModels []*agent.ModelInfo

	sessionID   string
	sessionFile string

	// turn is the turn state that SetTurnState publishes. See output.go.
	turn turnState

	usage usageState

	// root is the conversation of the session itself. Each subagent has its own.
	root *conversation
	// subagents indexes the live subagents by omp's agent id.
	subagents map[string]*subagentState
	// spawns keeps the tasks of each `task` call, keyed by the call id, until the
	// lifecycle frame of each of its subagents arrives. See rememberSpawns.
	spawns map[string]*taskSpawn
	// shells maps a background shell job to its registry row key.
	shells map[string]string
	// asks holds the question bridge's state; see ask.go.
	asks askBridge

	// startupSnapshot is true until Start returns. A goal omp reports while the
	// session opens restates a goal the resumed session already had.
	startupSnapshot atomic.Bool

	// stateRefresh coalesces the get_state reads that model_changed asks for.
	stateRefresh stateRefresher

	// dialogDeadlines withdraws a dialog whose deadline passed. See control.go.
	dialogDeadlines providerkit.ControlDeadlines
}

// Compile-time checks of the optional interfaces this agent implements.
// Manager.SupportsSteering answers false, with no build error, for a provider
// that stops satisfying InputSteerer, so the assertion makes that regression a
// compile error. The same holds for compaction.
var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
	_ agent.GoalCapable      = (*Agent)(nil)
)

// rootConversation returns the conversation of the session itself, and creates it
// on first use. The caller holds a.Mu.
func (a *Agent) rootConversationLocked() *conversation {
	if a.root == nil {
		a.root = newConversation(a.sink, "")
	}
	return a.root
}

// rootConversation returns the conversation of the session itself.
func (a *Agent) rootConversation() *conversation {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.rootConversationLocked()
}

// SendInput starts a turn with one user message.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, false)
}

// SendInputForSession starts a turn with one user message, when the session it
// states is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, false)
}

// SupportsSteering always reports true. omp accepts a steering message during any
// run, so the capability needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn. omp checks for steering between
// tool calls, and it moves a long `bash` call to the background to take it.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, true)
}

// sendInput writes one prompt and returns once omp acknowledges it.
//
// A new turn ARMS the turn state before the write, so the input queue holds the
// next message behind this one from the moment it leaves: omp acknowledges a
// prompt before its run starts, and a message dispatched into that window would
// otherwise reach omp as a second prompt. The arm is released when the prompt
// turns out to start no run -- a local slash command, a refusal, an uncertain
// delivery -- and agent_start takes it over when the run starts. See turnState
// in output.go.
//
// A steer is recorded before the write in the same way, and it counts as a
// pending steer only once omp acknowledges it for a run. The same outcomes that
// release an arm drop the steer. See turnState.steers.
//
// A new turn is sent with `streamingBehavior: followUp`, not without one. omp can
// start a run of its own between the busy check below and omp's own handling --
// a background job's result starts one -- and a prompt with no streaming
// behavior FAILS then ("Agent is already processing"). With followUp, omp queues
// the message behind that run instead, which delivers it.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionHandleLocked()); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	active := a.turn.active
	a.Mu.Unlock()
	if steer && !active {
		return agent.ErrNoActiveTurn
	}
	if !steer && active {
		return agent.ErrAgentBusy
	}

	payload := promptPayload(content, attachments)
	if steer {
		payload["streamingBehavior"] = streamingBehaviorSteer
	} else {
		payload["streamingBehavior"] = streamingBehaviorFollowUp
	}

	pending, err := a.beginPrompt(payload)
	if err != nil {
		return err
	}
	// release undoes the record below for a prompt that no run takes.
	var release func()
	if steer {
		a.beginSteer(pending.id)
		release = func() { a.dropSteer(pending.id) }
	} else {
		a.armTurn(pending.id)
		release = func() { a.disarmTurn(pending.id) }
	}
	if err := pending.write(); err != nil {
		release()
		return err
	}

	timer := a.Clock().NewTimer(promptAckTimeout, ompPromptAckTimerTag)
	defer timer.Stop(ompPromptAckTimerTag)
	select {
	case result := <-pending.ack:
		if result.err != nil {
			release()
			return fmt.Errorf("deliver the prompt to omp: %w", result.err)
		}
		switch {
		case !promptInvokesAgent(result.data):
			// A local slash command. omp answered it itself, and no run takes it.
			// omp runs a built-in command before it reads the streaming behavior,
			// so a steer can be one too.
			release()
		case steer:
			a.acceptSteer(pending.id)
		}
		return nil
	case <-timer.C:
		release()
		return fmt.Errorf("%w: omp did not acknowledge the prompt within %s", agent.ErrDeliveryUncertain, promptAckTimeout)
	case <-a.ProcessDone():
		release()
		return a.ProcessExitError()
	}
}

// pendingPrompt is one prompt command that is registered but not yet written, and
// the channel its acknowledgement arrives on.
type pendingPrompt struct {
	id    string
	write func() error
	ack   chan promptAck
}

type promptAck struct {
	data json.RawMessage
	err  error
}

// beginPrompt registers a prompt command and returns it unwritten, so the caller
// can arm the turn under the command's own id before omp can answer it.
func (a *Agent) beginPrompt(payload map[string]any) (*pendingPrompt, error) {
	id := "leapmux-" + fmt.Sprint(a.nextReqID.Add(1))
	envelope := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		envelope[key] = value
	}
	envelope["id"] = id
	envelope["type"] = CommandPrompt
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode omp prompt: %w", err)
	}
	data = append(data, '\n')

	ch, release := a.Register(id)
	ack := make(chan promptAck, 1)
	pending := &pendingPrompt{id: id, ack: ack}
	pending.write = func() error {
		a.Mu.Lock()
		stopped := a.StoppedLocked()
		a.Mu.Unlock()
		if stopped {
			release()
			return fmt.Errorf("agent is stopped")
		}
		if err := a.WriteStdin(data); err != nil {
			release()
			return fmt.Errorf("write omp prompt: %w", err)
		}
		// The acknowledgement has no deadline of its own: the caller's timer
		// bounds the wait, and a late answer still reaches this goroutine, which
		// releases the registration.
		go func() {
			defer release()
			raw, err := a.AwaitResponse(ch, CommandPrompt, 0)
			if err != nil {
				ack <- promptAck{err: err}
				return
			}
			response, err := parseResponse(CommandPrompt, raw)
			ack <- promptAck{data: response, err: err}
		}()
		return nil
	}
	return pending, nil
}

// promptInvokesAgent reports whether an acknowledged prompt starts a run.
//
// omp states `agentInvoked:false` for a slash command it answered itself. A plain
// prompt carries no data at all, and it starts a run.
func promptInvokesAgent(data json.RawMessage) bool {
	if len(data) == 0 || string(data) == "null" {
		return true
	}
	var result struct {
		AgentInvoked *bool `json:"agentInvoked"`
	}
	if json.Unmarshal(data, &result) != nil || result.AgentInvoked == nil {
		return true
	}
	return *result.AgentInvoked
}

// promptPayload builds the `message` and `images` of a prompt.
//
// omp's prompt carries text and images. A text attachment is inlined into the
// message; ValidateAttachment refuses a PDF and a binary file before they reach
// this, because omp has no field for them.
func promptPayload(content string, attachments []*leapmuxv1.Attachment) map[string]any {
	var message strings.Builder
	message.WriteString(content)
	images := make([]map[string]any, 0)
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			if message.Len() > 0 {
				message.WriteString("\n\n")
			}
			message.WriteString(providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
			images = append(images, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(attachment.Data),
				"mimeType": attachment.MIMEType,
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// ValidateAttachment refused these before the queue accepted them.
		}
	}
	payload := map[string]any{"message": message.String()}
	if len(images) > 0 {
		payload["images"] = images
	}
	return payload
}

// Interrupt stops the running turn with omp's `abort` command.
//
// It returns once the command is written. omp answers an abort only after the run
// it stops has ended -- after the agent_end that the abort causes -- so a wait
// here would hold the caller for the whole teardown of the run. A refusal is
// logged.
//
// A no-op when no turn runs, so a script can call it without checking first.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	active := a.turn.active
	if active {
		// See turnState.interruptRequested.
		a.turn.interruptRequested = true
	}
	a.Mu.Unlock()
	if !active {
		return nil
	}
	return a.sendCommandDetached(CommandAbort, nil, func(_ json.RawMessage, err error) {
		if err != nil && !a.IsStopped() {
			slog.Warn("omp abort failed", "agent_id", a.AgentID(), "error", err)
		}
	})
}

// Stop aborts the running turn, then closes the process through Process.Stop.
//
// The abort is sent and waited for BEFORE Process.Stop marks the process stopped
// and closes stdin, because a command written after that is refused. omp cancels
// the running tool and every open dialog on an abort; closing stdin then makes omp
// dispose of the session and exit.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.stateRefresh.stop()
	a.dialogDeadlines.StopAll()
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	active := a.turn.active
	a.asks.clearLocked()
	a.Mu.Unlock()
	if !stopped && active {
		// Best effort. A failure falls through to the teardown below.
		_, _ = a.sendCommand(CommandAbort, nil, abortWaitOnStop)
	}
	a.Process.Stop()
	a.finishOutput(agent.MessageCompletionInterrupted)
}

// Wait blocks until the process exits, then keeps the unfinished output of an
// exit that nothing asked for.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stateRefresh.stop()
	a.dialogDeadlines.StopAll()
	a.finishOutput(a.ProcessExitCompletion())
	return err
}

// finishOutput persists what the process left unfinished when it stopped: the
// model output it streamed, the tool calls it never ended, and the subagents it
// never closed. It also clears the turn.
func (a *Agent) finishOutput(completion agent.MessageCompletion) {
	root := a.rootConversation()
	a.flushGeneration(root, completion)
	a.persistIncompleteTools(root, completion)
	a.closeSubagents(subagentStatusForCompletion(completion))
	a.Mu.Lock()
	a.turn.clear()
	a.Mu.Unlock()
	a.sink.ReportProgress(agent.ResetProgress())
	a.PublishTurnActive()
}

// CompactContext runs omp's `compact` command.
//
// omp compacts outside any run: the command sends no agent_start and no
// agent_end, and its RESPONSE is the result. The input queue records the dispatch
// as a turn, so the turn is armed here and released when the response arrives,
// and a "compacting" notice shows meanwhile. omp's response frame itself then
// reaches the transcript, where the browser reads the compaction it states, or
// the reason omp refused it ("Nothing to compact").
//
// It returns once the command is written: a compaction can take as long as a
// model call.
func (a *Agent) CompactContext() error {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if a.turn.active {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.Mu.Unlock()

	a.armTurn(compactionTurnID)
	wait, err := a.beginCommandFrame(contracts.OhMyPiCommandCompact, nil)
	if err != nil {
		a.disarmTurn(compactionTurnID)
		return err
	}
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeCompacting,
	})
	go func() {
		frame, err := wait(0)
		a.recordCompactionResult(frame, err)
		a.disarmTurn(compactionTurnID)
	}()
	return nil
}

// compactionTurnID arms the turn for a `compact` command. A prompt arms it under
// its command id, which starts "leapmux-", so the two cannot collide.
const compactionTurnID = "compaction"

// recordCompactionResult persists the outcome of a `compact` command: omp's
// response frame, or the transport failure that left no response.
func (a *Agent) recordCompactionResult(frame json.RawMessage, err error) {
	if a.IsStopped() || a.IsDiscardingOutput() {
		return
	}
	if err != nil {
		slog.Warn("omp compact failed", "agent_id", a.AgentID(), "error", err)
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: err.Error(),
		})
		return
	}
	if _, persistErr := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, frame); persistErr != nil {
		slog.Error("omp persist compact response", "agent_id", a.AgentID(), "error", persistErr)
	}
}

// PublishTurnActive republishes the turn state from turn.active, the single
// source. Call it after EVERY critical section that writes that field.
//
// It re-reads rather than taking a value, so a caller cannot publish something the
// field does not say. Never called with a.Mu held: the sink broadcasts, and a
// broadcast can block on a slow transport.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.turn.active
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}
