package droid

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/google/uuid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one `droid exec --input-format stream-jsonrpc` process and the
// one session LeapMux drives in it.
//
// The process speaks Factory's stream-jsonrpc protocol over stdio: every
// message is one NDJSON line carrying Factory's version stamps. The agent
// embeds Process for the lifecycle and routes conversation through
// droid.add_user_message, with notifications dispatched in output.go.
type Agent struct {
	providerkit.Process

	sink            agent.ProviderServices
	workingDir      string
	homeDir         string
	clock           quartz.Clock
	launchOpts      agent.Options
	launch          droidLaunch
	cleanupSettings func()
	cleanupOnce     sync.Once

	// dispatchMu serializes event dispatch. The reader goroutine and
	// HandleOutput both reach handleFrame.
	dispatchMu sync.Mutex

	// --- guarded by Mu ---

	sessionID  string
	turnActive bool
	compaction *droidCompaction
	settings   droidSettings
	catalog    droidCatalog
	// controls holds every permission and question LeapMux published and
	// nothing resolved yet.
	controls     map[string]*droidPendingControl
	approvalWork providerkit.CoalescingWork
	// childTurns tracks each child session's turn flag. The key is the
	// childSessionId of child_session_available, which is also the registry
	// row key.
	childTurns map[string]bool
	// startupReply settles the native initialize or load request before Start
	// exposes this agent to a caller that can send a prompt.
	startupReply chan error

	// settingsApplyMu serializes native updates so one readback cannot confirm
	// another caller's choice. rpcMu guards the request inboxes and context read.
	settingsApplyMu       sync.Mutex
	rpcMu                 sync.Mutex
	pendingReplies        map[string]chan droidEnvelope
	pendingSettings       map[string]chan droidSettingsUpdated
	contextRefreshPending bool
	contextRefreshQueued  bool

	// dispatchMu guards these maps and every droidOutputState.
	childSpawns        map[string]droidChildSpawn
	childAgents        map[string]string
	childAnnouncements map[string]droidChildAnnouncementID
	outputStates       map[string]*droidOutputState
	mcpFailures        map[string]string
	retryResolution    providerkit.RetryResolution
	steers             providerkit.SteerLedger
	outputQueue        agent.OutputQueue
	tailMu             sync.Mutex
	childTails         map[string]*droidChildTail
	tailClosing        bool
	// beforeChildTailStateRead controls the replay lock order in tests only.
	beforeChildTailStateRead func()
	// beforeChildTailStart controls Stop during a deferred start in tests only.
	beforeChildTailStart func()
	// beforeChildTailRun controls the map-publish/start gap in tests only.
	beforeChildTailRun func()
	childConnMu        sync.Mutex
	childConns         map[string]*droidChildConnection
	childClosing       bool
	childStopOnce      sync.Once

	sendMu  sync.Mutex
	stopped bool
}

// droidLaunch keeps the resolved program and its shutdown duration together.
type droidLaunch struct {
	spec          launch.Spec
	shutdownGrace time.Duration
}

var (
	_ agent.Agent             = (*Agent)(nil)
	_ agent.InputSteerer      = (*Agent)(nil)
	_ agent.ContextCompactor  = (*Agent)(nil)
	_ agent.SteerEchoReporter = (*Agent)(nil)
	_ agent.SteerSurvivor     = (*Agent)(nil)
)

// errAgentStopped reports that the agent's process already ended.
var errAgentStopped = errors.New("the Factory Droid process has stopped")

// droidSettings is the live configuration of the session.
type droidSettings struct {
	model           string
	reasoningEffort string
	interactionMode string
	autonomyLevel   string
	permissionMode  string
}

// droidCatalog is the model list the session reported.
type droidCatalog struct {
	models []droidModel
}

// droidModel is one entry of the session's availableModels.
type droidModel struct {
	id          string
	displayName string
	efforts     []string
	supportsPDF bool
}

// droidTool is one open tool call.
type droidTool struct {
	// pendingRow becomes nil after the output queue takes the immutable snapshot.
	pendingRow []byte
	order      uint64
	id         string
	name       string
	spanID     string
	input      json.RawMessage
}

type droidModelScopeState uint8

const (
	droidModelScopeStreaming droidModelScopeState = iota
	droidModelScopeCompleted
)

// droidModelMessage tracks uncommitted scopes and an accepted final boundary.
// A finalized message cannot reopen when a late delta or commit arrives.
type droidModelMessage struct {
	scopes    map[string]droidModelScopeState
	finalized bool
}

// droidOutputState holds one native session's transcript assembly.
type droidOutputState struct {
	nextToolOrder  uint64
	modelMessages  map[string]*droidModelMessage
	tools          map[string]*droidTool
	turnToolUse    int
	turnErrorSeen  bool
	compactionLive bool
	generation     providerkit.GenerationBuffer
	outputQueue    *agent.OutputQueue
	ended          bool
}

// droidPendingControl is one published control request awaiting an answer.
type droidPendingControl struct {
	requestID       string
	rpcID           string
	sessionID       string
	root            bool
	payload         []byte
	automaticReply  []byte
	cancelAnswer    any
	ready           bool
	published       bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

// SendInput delivers a user message to the session.
//
// Droid replies when it enqueues the message. This method waits for that reply, not the turn end.
// A running turn refuses ordinary input with ErrAgentBusy, so the LeapMux queue retains it.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, droidInputOrdinary)
}

// SendInputForSession delivers a user message when the session it states is
// still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, droidInputOrdinary)
}

// SupportsSteering reports true: Droid accepts end_of_turn during a running
// turn and puts that user message into the active agent loop.
func (a *Agent) SupportsSteering() bool { return true }

// SteerRowAtEcho keeps the queue item until Droid commits its user message.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd retains accepted input until Droid echoes or explicitly discards it.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

// SteerInput adds a message to the running turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, droidInputSteer)
}

type droidInputMode uint8

const (
	droidInputOrdinary droidInputMode = iota
	droidInputSteer
	// A child queue writes at acceptance. Child sinks do not report steer echoes.
	droidInputChildSteer
)

// sendInput writes one droid.add_user_message request.
//
// A plain message during a turn would be queued by Droid itself and become the
// next turn out of LeapMux's sight, so the agent refuses a new message while a
// turn runs: the LeapMux input queue owns queueing. A steering message uses
// the same native placement while the turn is active.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, mode droidInputMode) error {
	steer := mode != droidInputOrdinary
	message, err := buildUserMessage(content, attachments)
	if err != nil {
		return err
	}

	// Serialize model-setting changes with attachment validation and native admission.
	a.settingsApplyMu.Lock()
	defer a.settingsApplyMu.Unlock()
	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.stopped {
		a.Mu.Unlock()
		return errAgentStopped
	}
	active := a.turnActive
	compacting := a.compaction != nil
	sessionID := a.sessionID
	var modelErr error
	if len(message.Files) > 0 {
		modelErr = a.validatePDFModelLocked()
	}
	a.Mu.Unlock()

	if compacting {
		return agent.ErrAgentBusy
	}
	if steer {
		if !active {
			return agent.ErrNoActiveTurn
		}
	} else if active {
		return agent.ErrAgentBusy
	}
	if modelErr != nil {
		return modelErr
	}

	params := addUserMessageParams{
		SessionID:      sessionID,
		MessageID:      uuid.NewString(),
		Text:           message.Text,
		Images:         message.Images,
		Files:          message.Files,
		QueuePlacement: droidQueueEndOfTurn,
	}
	if !steer {
		a.startTurn()
	}
	id := a.nextRequestID()
	var ticket providerkit.SteerTicket
	if mode == droidInputSteer {
		a.dispatchMu.Lock()
		ticket = a.steers.Add(params.MessageID)
		a.steers.SetID(ticket, id)
		a.dispatchMu.Unlock()
	}
	if err := a.requestInput(id, params); err != nil {
		if ticket != 0 {
			a.dispatchMu.Lock()
			pending := false
			for _, entry := range a.steers.Pending() {
				pending = pending || entry == ticket
			}
			if pending {
				a.steers.Forget(ticket)
				target, _ := a.outputTargetFor(sessionID)
				a.steers.Settle(target.sink)
			}
			a.dispatchMu.Unlock()
			if !pending {
				// An echo or a discard already resolved this input, even if its report still waits for storage.
				return nil
			}
		}
		if !steer {
			a.endTurn()
		}
		return err
	}
	return nil
}

// startTurn marks a turn active and publishes the flag. A repeat of the current
// state publishes nothing, so a frame that is not a turn signal moves nothing.
func (a *Agent) startTurn() {
	a.Mu.Lock()
	if a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = true
	seq := a.NextTurnSeq()
	steerable := a.compaction == nil
	a.Mu.Unlock()
	a.orderedSink().SetTurnState(agent.TurnState{Active: true, Steerable: steerable}, seq)
}

// endTurn marks no turn active and publishes the flag. A repeat of the
// current state publishes nothing.
func (a *Agent) endTurn() {
	a.Mu.Lock()
	if !a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = false
	seq := a.NextTurnSeq()
	compacting := a.compaction != nil
	retired := a.takeControlsLocked(true)
	a.Mu.Unlock()
	a.cancelControls(retired)
	a.orderedSink().SetTurnState(agent.TurnState{Active: compacting}, seq)
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive || a.compaction != nil, Steerable: a.turnActive && a.compaction == nil}
	a.Mu.Unlock()
	a.orderedSink().SetTurnState(state, a.nextTurnSeq())
	return state
}

func (a *Agent) nextTurnSeq() uint64 {
	return a.NextTurnSeq()
}

// Interrupt aborts the agent's current turn with droid.interrupt_session.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	sessionID := a.sessionID
	active := a.turnActive
	a.Mu.Unlock()
	if !active || sessionID == "" {
		return nil
	}
	if err := a.request(droidMethodInterruptSession, interruptParams{SessionID: sessionID}); err != nil {
		return err
	}
	return nil
}

// ClearContext requests a worker restart because Droid exposes no in-process context clear.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// Stop ends the process.
func (a *Agent) Stop() {
	a.Mu.Lock()
	a.stopped = true
	retired := a.takeControlsLocked(false)
	a.Mu.Unlock()
	a.cancelControls(retired)
	a.markChildTailsClosing()
	a.Process.Stop()
	a.stopChildConnections()
	a.stopChildTails()
	a.finishOutput(agent.MessageCompletionInterrupted)
	a.cleanupRuntimeSettings()
}

// Wait returns the native process result after its child processes and
// temporary settings are closed.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.Mu.Lock()
	a.stopped = true
	retired := a.takeControlsLocked(false)
	a.Mu.Unlock()
	a.cancelControls(retired)
	a.stopChildConnections()
	a.stopChildTails()
	a.finishOutput(a.ProcessExitCompletion())
	a.cleanupRuntimeSettings()
	return err
}

func (a *Agent) cleanupRuntimeSettings() {
	a.cleanupOnce.Do(func() {
		if a.cleanupSettings != nil {
			a.cleanupSettings()
		}
	})
}
