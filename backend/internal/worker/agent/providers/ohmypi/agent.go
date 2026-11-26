package ohmypi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// promptAckTimeout limits the wait for omp to acknowledge a prompt.
//
// omp acknowledges after admission in current releases. A prompt can wait
// behind a slow command or its own preflight. Past the deadline omp can still
// accept the prompt, so delivery is uncertain.
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
	steers providerkit.SteerLedger
	providerkit.Process
	providerkit.Correlator[string]

	sink       agent.ProviderServices
	workingDir string
	directory  *agentdir.Dir

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
	// outputMu serializes handleFrame with idle-interrupt cleanup and
	// finishOutput. Acquire it before Process.Mu and never hold it across
	// sessionMu or settleOpenDialogs.
	outputMu sync.Mutex
	// approvalWork keeps permission writes off the stdout reader.
	approvalWork providerkit.CoalescingWork

	// The fields below are guarded by Process.Mu.

	// model is the selected primary model in the form `<provider>/<id>`.
	model string
	// fallbackModel identifies the temporary model that must not replace the primary.
	fallbackModel string
	// effectiveModel is the model that the latest native state reports.
	effectiveModel string
	// thinkingLevel is the reader's thinking level, stored as the agent's effort.
	// agent.EffortAuto means "omp's configured default": the worker sent none.
	thinkingLevel string
	// effectiveThinking is the level omp runs now, as its get_state response and
	// its thinking_level_changed frames state it. omp clamps a requested level to
	// what the model offers and reports only a level that MOVED, so this is the
	// one record of the level a set_thinking_level settled on.
	effectiveThinking string
	// approvalMode is the effective tool policy. The Worker can apply Yolo live.
	// Other changes restart the process with the requested native policy.
	approvalMode    string
	outputStyle     string
	fastMode        bool
	availableModels []*agent.ModelInfo

	sessionID   string
	sessionFile string

	// turn is the turn state that SetTurnState publishes. See output.go.
	turn turnState

	replyPrompts     map[string]*ompReplyPrompt
	replyAwaiting    *ompReplyPrompt
	replyOutput      *ompReplyPrompt
	steerBridge      ompSteerBridge
	steerBridgeReady bool
	identifiedSteers map[string]agent.SteerResolution

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
	// openDialogs holds each dialog that omp still waits on.
	// Interrupt answers each unclaimed dialog that omp's abort leaves open.
	// See control.go.
	openDialogs map[string]*pendingDialog

	// startupSnapshot is true until Start returns. A goal omp reports while the
	// session opens restates a goal the resumed session already had.
	startupSnapshot atomic.Bool

	// stateRefresh coalesces the get_state reads that model_changed asks for.
	stateRefresh stateRefresher
	// compactionCommand owns the outstanding compact receipt, even after its native end.
	// Process.Mu protects it.
	compactionCommand *compactionPass

	// dialogDeadlines withdraws a dialog whose deadline passed. See control.go.
	dialogDeadlines providerkit.ControlDeadlines
}

// Compile-time checks of the optional interfaces this agent implements.
// Manager.SupportsSteering answers false, with no build error, for a provider
// that stops satisfying InputSteerer, so the assertion makes that regression a
// compile error. The same holds for compaction.
var (
	_ agent.Agent             = (*Agent)(nil)
	_ agent.InputSteerer      = (*Agent)(nil)
	_ agent.SteerEchoReporter = (*Agent)(nil)
	_ agent.SteerSurvivor     = (*Agent)(nil)
	_ agent.ContextCompactor  = (*Agent)(nil)
	_ agent.GoalWriter        = (*Agent)(nil)
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
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendInput(nil, content, attachments, nil)
}

// SendInputForSession starts a turn with one user message, when the session it
// states is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendInput(&sessionID, content, attachments, nil)
}

// SupportsSteering always reports true. omp accepts a steering message during any
// run, so the capability needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn. omp checks for steering between
// tool calls, and it moves a long `bash` call to the background to take it.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	_, err := a.sendInput(nil, request.Content, request.Attachments, &request)
	return agent.NativeInputReceipt{}, err
}

// SteerRowAtEcho places a steer at the native user echo or a confirmed local command result.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd keeps a late accepted steer for the next native run.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

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
// The Worker queues a tracked prompt if a native background run starts first.
// Its fresh admission proves which run owns replies and controls. Slash
// commands can expand into native work and use the native follow-up path.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steerReq *agent.SteerRequest) (agent.NativeInputReceipt, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	var receipt agent.NativeInputReceipt
	steer := steerReq != nil
	a.Mu.Lock()
	sessionID := a.sessionHandleLocked()
	if err := providerkit.CheckInputSession(expected, sessionID); err != nil {
		a.Mu.Unlock()
		return receipt, err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return receipt, fmt.Errorf("agent is stopped")
	}
	state := a.activityStateLocked()
	bridgeReady := a.steerBridgeReady
	a.Mu.Unlock()
	if steer && !state.Active {
		return receipt, agent.ErrNoActiveTurn
	}
	if state.Active && (!steer || !state.Steerable) {
		return receipt, &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: state.Steerable}
	}

	isPrompt := sessionID != "" && !strings.HasPrefix(strings.TrimSpace(content), "/")
	trackReply := !steer && isPrompt
	identifySteer := steer && isPrompt && bridgeReady && steerReq.InputID != "" && steerReq.AttemptID != ""
	payload := promptPayload(content, attachments)
	if identifySteer {
		return a.sendIdentifiedSteerLocked(sessionID, *steerReq, payload)
	}
	if steer {
		return a.sendLegacySteerLocked(payload)
	}
	return a.sendPromptLocked(sessionID, trackReply, payload)
}

type ompAckStatus uint8

const (
	ompAckReceived ompAckStatus = iota
	ompAckTimedOut
	ompAckProcessExited
)

func (a *Agent) awaitPromptAck(pending *pendingPrompt) (promptAck, ompAckStatus) {
	timer := a.Clock().NewTimer(promptAckTimeout, ompPromptAckTimerTag)
	defer timer.Stop(ompPromptAckTimerTag)
	select {
	case result := <-pending.ack:
		return result, ompAckReceived
	case <-timer.C:
		return promptAck{}, ompAckTimedOut
	case <-a.ProcessDone():
		return promptAck{}, ompAckProcessExited
	}
}

func (a *Agent) sendIdentifiedSteerLocked(sessionID string, req agent.SteerRequest, payload map[string]any) (agent.NativeInputReceipt, error) {
	payload["streamingBehavior"] = streamingBehaviorSteer
	resolution := a.prepareOMPSteer(sessionID, req, payload)
	receipt := resolution.Receipt
	observe := func(raw json.RawMessage) { a.observeOMPSteerAdmission(receipt.InputID, raw) }
	pending, err := a.beginPrompt(payload, receipt.InputID, observe)
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	a.registerOMPSteer(resolution)
	a.beginSteer(pending.id)
	release := func() {
		a.dropSteer(pending.id)
		a.takeOMPSteer(pending.id)
	}
	if err := pending.write(); err != nil {
		release()
		return agent.NativeInputReceipt{}, err
	}
	result, status := a.awaitPromptAck(pending)
	switch status {
	case ompAckTimedOut:
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: omp did not acknowledge the prompt within %s", agent.ErrDeliveryUncertain, promptAckTimeout)
	case ompAckProcessExited:
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: %w", agent.ErrDeliveryUncertain, a.ProcessExitError())
	default:
		if result.err != nil {
			var refusal *commandError
			if errors.As(result.err, &refusal) {
				release()
				return agent.NativeInputReceipt{}, fmt.Errorf("deliver the prompt to omp: %w", result.err)
			}
			return agent.NativeInputReceipt{}, fmt.Errorf("%w: deliver the prompt to omp: %w", agent.ErrDeliveryUncertain, result.err)
		}
		if !promptInvokesAgent(result.data) {
			// Local commands such as /fast can change settings without a model-change event.
			a.stateRefresh.schedule(a)
			release()
		} else {
			a.acceptSteer(pending.id)
		}
		return receipt, nil
	}
}

func (a *Agent) sendLegacySteerLocked(payload map[string]any) (agent.NativeInputReceipt, error) {
	payload["streamingBehavior"] = streamingBehaviorSteer
	pending, err := a.beginPrompt(payload, "", nil)
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	ticket := a.steers.Add(payload["message"].(string))
	// The request ID is known before the write, so an early native result can resolve this steer.
	a.steers.SetID(ticket, pending.id)
	a.beginSteer(pending.id)
	release := func() {
		a.dropSteer(pending.id)
		a.steers.Forget(ticket)
		a.steers.Settle(a.sink)
	}
	if err := pending.write(); err != nil {
		release()
		return agent.NativeInputReceipt{}, err
	}
	result, status := a.awaitPromptAck(pending)
	switch status {
	case ompAckTimedOut:
		release()
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: omp did not acknowledge the prompt within %s", agent.ErrDeliveryUncertain, promptAckTimeout)
	case ompAckProcessExited:
		release()
		return agent.NativeInputReceipt{}, a.ProcessExitError()
	default:
		if result.err != nil {
			release()
			return agent.NativeInputReceipt{}, fmt.Errorf("deliver the prompt to omp: %w", result.err)
		}
		if !promptInvokesAgent(result.data) {
			// Local commands such as /fast can change settings without a model-change event.
			a.stateRefresh.schedule(a)
			// A local slash command. omp answered it itself, and no run takes it.
			// omp runs a built-in command before it reads the streaming behavior,
			// so a steer can be one too.
			a.dropSteer(pending.id)
			a.steers.Echo(ticket)
			a.steers.Settle(a.sink)
		} else {
			a.acceptSteer(pending.id)
		}
		return agent.NativeInputReceipt{}, nil
	}
}

func (a *Agent) sendPromptLocked(sessionID string, trackReply bool, payload map[string]any) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	var observe func(json.RawMessage)
	if trackReply {
		receipt = newOMPReceipt(sessionID)
		observe = func(raw json.RawMessage) { a.observeReplyAdmission(receipt, raw) }
	} else {
		payload["streamingBehavior"] = streamingBehaviorFollowUp
	}
	pending, err := a.beginPrompt(payload, receipt.InputID, observe)
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	a.armTurn(pending.id)
	if trackReply {
		a.registerReplyPrompt(receipt)
	}
	refuse := func() {
		if trackReply {
			a.takeReplyPrompt(receipt.InputID)
		}
		a.disarmTurn(pending.id)
	}
	if err := pending.write(); err != nil {
		refuse()
		return agent.NativeInputReceipt{}, err
	}
	result, status := a.awaitPromptAck(pending)
	switch status {
	case ompAckTimedOut:
		refuse()
		return agent.NativeInputReceipt{}, fmt.Errorf("%w: omp did not acknowledge the prompt within %s", agent.ErrDeliveryUncertain, promptAckTimeout)
	case ompAckProcessExited:
		refuse()
		return agent.NativeInputReceipt{}, a.ProcessExitError()
	default:
		if result.err != nil {
			if trackReply && isOMPBusyRefusal(result.err) {
				a.takeReplyPrompt(receipt.InputID)
				a.Mu.Lock()
				if a.turn.armedBy == pending.id {
					a.turn.armedBy = ""
				}
				a.Mu.Unlock()
				a.PublishTurnActive()
				return agent.NativeInputReceipt{}, agent.ErrAgentBusy
			}
			refuse()
			return agent.NativeInputReceipt{}, fmt.Errorf("deliver the prompt to omp: %w", result.err)
		}
		if !promptInvokesAgent(result.data) {
			// Local commands such as /fast can change settings without a model-change event.
			a.stateRefresh.schedule(a)
			a.disarmTurn(pending.id)
		}
		return receipt, nil
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
func (a *Agent) beginPrompt(payload map[string]any, id string, observe func(json.RawMessage)) (*pendingPrompt, error) {
	if id == "" {
		id = "leapmux-" + fmt.Sprint(a.nextReqID.Add(1))
	}
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

	ch, release, err := a.RegisterObserved(id, observe)
	if err != nil {
		return nil, fmt.Errorf("register omp prompt: %w", err)
	}
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
	if !providerkit.RawPresent(data) {
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
		case contracts.AttachmentMediaKindText:
			if message.Len() > 0 {
				message.WriteString("\n\n")
			}
			message.WriteString(providerkit.BuildInlineTextAttachmentBlock(attachment))
		case contracts.AttachmentMediaKindImage:
			images = append(images, map[string]any{
				"type":     contentBlockImage,
				"data":     base64.StdEncoding.EncodeToString(attachment.Data),
				"mimeType": attachment.MIMEType,
			})
		case contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary, contracts.AttachmentMediaKindAudio:
			// ValidateAttachment refused these before the queue accepted them.
		}
	}
	payload := map[string]any{"message": message.String()}
	if len(images) > 0 {
		payload["images"] = images
	}
	return payload
}

// Interrupt stops the running turn with omp's `abort` command, then answers each
// dialog that the abort leaves open (see settleOpenDialogs).
//
// It returns once the command is written. omp answers an abort only after the run
// it stops has ended -- after the agent_end that the abort causes -- so a wait
// here would hold the caller for the whole teardown of the run. A refusal is
// logged.
//
// When no turn runs, it sends no abort, so a script can call it without checking
// first. It still answers each dialog that omp waits on, because no abort settles
// one.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	active := a.activityStateLocked().Active
	if active {
		// See turnState.interruptRequested.
		a.turn.interruptRequested = true
	}
	a.Mu.Unlock()
	if !active {
		a.settleOpenDialogs()
		a.outputMu.Lock()
		a.Mu.Lock()
		stillIdle := !a.StoppedLocked() && !a.activityStateLocked().Active
		a.Mu.Unlock()
		if stillIdle {
			root := a.rootConversation()
			a.persistIncompleteTools(root, agent.MessageCompletionInterrupted)
			a.ResetCumulativeOutput()
			providerkit.ResetSpansAndProgress(a.sink)
		}
		a.outputMu.Unlock()
		return nil
	}
	return a.sendCommandDetached(CommandAbort, nil, func(_ json.RawMessage, err error) {
		if err != nil && !a.IsStopped() {
			slog.Warn("omp abort failed", "agent_id", a.AgentID(), "error", err)
		}
		// The read loop handles each frame before the response that follows it,
		// so each dialog that the abort settled is withdrawn by now. A refused
		// abort settled nothing, and its dialogs still need an answer.
		a.settleOpenDialogs()
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
	active := a.activityStateLocked().Active
	a.asks.clearLocked()
	a.Mu.Unlock()
	if !stopped && active && a.IntentionalStopRequested() {
		// Best effort. A failure falls through to the teardown below.
		_, _ = a.sendCommand(CommandAbort, nil, abortWaitOnStop)
	}
	a.Process.Stop()
	a.finishOutput(a.ProcessExitCompletion())
}

// Wait blocks until the process exits, then keeps the unfinished output of an
// exit that nothing asked for.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stateRefresh.stop()
	a.dialogDeadlines.StopAll()
	a.finishOutput(a.ProcessExitCompletion())
	return errors.Join(err, a.directory.Close())
}

// finishOutput persists what the process left unfinished when it stopped: the
// model output it streamed, the tool calls it never ended, and the subagents it
// never closed. It also clears the turn.
func (a *Agent) finishOutput(completion agent.MessageCompletion) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	root := a.rootConversation()
	a.flushGeneration(root, completion)
	a.persistIncompleteTools(root, completion)
	a.finishOpenCompactions()
	a.closeSubagents(subagentStatusForCompletion(completion))
	a.closeAllShells()
	a.retireOpenReplies(agent.SteerOutcomeUncertain)
	a.Mu.Lock()
	a.turn.clear()
	a.Mu.Unlock()
	providerkit.ResetSpansAndProgress(a.sink)
	a.PublishTurnActive()
}

// CompactContext requests manual compaction and keeps input behind its pending receipt.
// Native lifecycle events supply the outcome. A response supplies a fallback
// when this command has no observed native end.
func (a *Agent) CompactContext() error {
	request, err := a.startCompactionCommand()
	if request == nil {
		return err
	}
	a.flushCompactionState(request.conversation)
	a.PublishTurnActive()
	if err != nil {
		return err
	}
	go func() {
		frame, err := request.wait(0)
		a.recordCompactionResult(request.pass, frame, err)
	}()
	return nil
}

type compactionCommandRequest struct {
	conversation *conversation
	pass         *compactionPass
	wait         func(time.Duration) (json.RawMessage, error)
}

// startCompactionCommand serializes the native write with session changes.
// The caller publishes the admitted state after this method releases sessionMu.
func (a *Agent) startCompactionCommand() (*compactionCommandRequest, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	root := a.rootConversation()
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return nil, fmt.Errorf("agent is stopped")
	}
	if a.activityStateLocked().Active {
		a.Mu.Unlock()
		return nil, agent.ErrAgentBusy
	}
	pass := &compactionPass{trigger: "manual", stage: compactionAwaitingNativeStart}
	root.compaction, a.compactionCommand = pass, pass
	a.queueCompactionStateLocked(root)
	a.Mu.Unlock()
	request := &compactionCommandRequest{conversation: root, pass: pass}
	wait, err := a.beginCommandFrame(contracts.OhMyPiCommandCompact, nil)
	if err == nil {
		request.wait = wait
		return request, nil
	}
	a.Mu.Lock()
	if a.compactionCommand == pass {
		a.compactionCommand = nil
	}
	if root.compaction == pass && pass.stage == compactionAwaitingNativeStart {
		root.compaction = nil
		pass.stage = compactionCommandFinished
		a.queueCompactionStateLocked(root)
	}
	a.Mu.Unlock()
	return request, err
}

// recordCompactionResult releases only this command's reservation. An already
// observed native end takes precedence over the receipt, including a lost receipt.
func (a *Agent) recordCompactionResult(pass *compactionPass, frame json.RawMessage, err error) {
	root := a.rootConversation()
	a.Mu.Lock()
	if a.StoppedLocked() || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	commandOwned, current := a.compactionCommand == pass, root.compaction == pass
	if !commandOwned && !current {
		a.Mu.Unlock()
		return
	}
	nativeEnded := pass.stage == compactionNativeEnded
	if commandOwned {
		a.compactionCommand = nil
	}
	if current {
		root.compaction = nil
		a.queueCompactionStateLocked(root)
	}
	pass.stage = compactionCommandFinished
	a.Mu.Unlock()
	if !nativeEnded {
		if err != nil {
			slog.Warn("omp compact failed", "agent_id", a.AgentID(), "error", err)
			providerkit.PersistCompactionEnded(root.sink, providerkit.CompactionEndFailed, err.Error())
		} else if _, persistErr := root.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: frame}); persistErr != nil {
			slog.Error("omp persist compact response", "agent_id", a.AgentID(), "error", persistErr)
		}
	}
	if current {
		a.flushCompactionState(root)
	}
	a.PublishTurnActive()
}

// activityStateLocked combines prompt activity with maintenance and its receipt.
// A compaction can run without an agent_start and does not accept steering.
func (a *Agent) activityStateLocked() agent.TurnState {
	compacting := a.compactionCommand != nil || a.root != nil && a.root.compaction != nil
	return agent.TurnState{Active: a.turn.active || compacting, Steerable: a.turn.active && !compacting}
}

// PublishTurnActive reads the current prompt and maintenance state before publication.
// Call it without Process.Mu because the sink can wait for a slow transport.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := a.activityStateLocked()
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, seq)
}
