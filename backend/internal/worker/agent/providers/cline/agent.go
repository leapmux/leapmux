package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent is one Cline session, driven through a private hub daemon.
//
// The daemon sends no protocol frames through standard input or output.
// The conversation uses one WebSocket connection (rpc.go).
// Agent embeds Process for these lifecycle operations:
//   - Start the daemon.
//   - Stop the daemon.
//   - Observe its exit.
//   - Read its standard error.
//
// The hub client handles every other operation.
type Agent struct {
	*providerkit.Process

	sink      agent.ProviderServices
	leadRetry providerkit.RetryResolution
	opts      agent.Options
	// dir is the private directory for these files:
	//   - The discovery record.
	//   - The private task database.
	//   - The attached files.
	dir *agentdir.Dir
	// workspaceRoot is the git top level of the working directory, or the
	// directory itself outside a repository, as Cline's CLI states it.
	workspaceRoot string
	// record is the daemon's discovery record.
	record discoveryRecord
	// endpoint is the daemon's loopback HTTP endpoint. Stop sends the shutdown
	// through it.
	endpoint *providerkit.HTTPEndpoint
	hub      *hubClient
	// subscribe scopes the event stream to one session: the hub client's own
	// subscribe. A test replaces it to make a subscription fail.
	subscribe func(ctx context.Context, sessionID string) error
	clientID  string
	// selection is the provider and the model that the user's Cline settings
	// select. The provider does not change for the agent's life.
	selection providerSelection
	clock     quartz.Clock
	// sendWait limits how long a send waits for the daemon to start the turn.
	// A test shortens it.
	sendWait time.Duration

	// ctx lasts for the agent's lifetime. Stop and the daemon's exit cancel it.
	// Cancellation ends the hub client and every command. It ends every agent goroutine also.
	ctx    context.Context
	cancel context.CancelFunc
	// background tracks work that outlives its initial event:
	//   - Read a child transcript from Cline's store.
	//   - Change the mode after a turn.
	//   - Answer a request automatically.
	//   - Abort a run after it starts.
	//
	// Stop and Wait wait for these goroutines.
	background sync.WaitGroup

	// dispatchMu serializes event dispatch. The hub's dispatcher goroutine and
	// HandleOutput both reach handleEvent, and one event's handling reads and
	// writes the output state in sequence.
	dispatchMu sync.Mutex
	// sessionMu serializes changes to the session and its configuration:
	//   - Open the initial session.
	//   - Clear the context.
	//   - Rebuild the session for settings that fixed its runtime.
	//   - Write the settings.
	//   - Release session claims during teardown.
	//
	// A caller that holds Mu never takes sessionMu.
	sessionMu sync.Mutex
	// claims holds each session that the agent claimed and did not release
	// (hostedSessions). Guarded by sessionMu.
	claims map[string]bool
	// sendMu serializes the writes of user input, so two sends cannot both find
	// no turn and both start one.
	sendMu sync.Mutex

	// --- guarded by Mu (from Process) ---

	sessionID string
	settings  clineSettings
	// loadedExtensions are the extensions that the runtime of the current
	// session loaded: the ones of the settings that built it. A move between
	// Act and Auto-approve during a turn applies its approval policy at once,
	// and the runtime keeps these until the turn's end rebuilds it
	// (clineSettings.needsRebuild).
	loadedExtensions []string
	turn             turnState
	// deliveries holds the waiter of each session.send_input that waits for its
	// run.started event, by request id.
	deliveries map[string]chan error
	// controls holds every approval and question that LeapMux published and
	// nothing resolved yet, by request id. See control.go.
	controls map[string]*pendingControl
	// out is the transcript output state. See output.go.
	out outputState
	// team is the agent-team state. See team.go.
	team teamState
	// contextUsage is the last context-usage broadcast.
	contextUsage map[string]any
	// attachSeq numbers the directories of attached files, one for each
	// message.
	attachSeq uint64
	// modeRebuild is a mode that the session takes once the running turn ends.
	// See session_lifecycle.go.
	modeRebuild *pendingModeChange

	stopOnce sync.Once
	// stopped closes when Stop finishes.
	stopped chan struct{}
	// teardownOnce runs the teardown once, whether Stop or Wait reaches it
	// first. See stop.go.
	teardownOnce sync.Once
}

// Compile-time checks of the optional interfaces this agent implements.
// Manager.SupportsSteering returns false when a provider no longer implements InputSteerer.
// This assertion makes that regression a compile error.
var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.InputSteerer = (*Agent)(nil)
)

// turnState is the turn the agent owes the user a reply for. Guarded by Mu.
type turnState struct {
	active bool
	// sequence identifies this turn across delayed steer replies.
	sequence      uint64
	steersPending int
	steerReports  uint64
	// requestID is the session.send_input that started the turn, or "" for a
	// turn that Cline started by itself.
	requestID string
	// steerable is true while the turn takes a steer: a turn that LeapMux
	// started. A turn that Cline started by itself -- a team run that resumed
	// the lead -- takes the next message as a turn of its own.
	steerable bool
	// interruptRequested records that the user stopped the turn, so its end
	// reads as an interruption and not as an error. On a settling turn it
	// stops the plan continuation that would follow the turn (applyModeChange).
	interruptRequested bool
	// runStarted records that the turn's run exists: Cline published its
	// run.started, or the session's status turned `running`.
	runStarted bool
	// abortOnStart records an interrupt that came before the run started.
	// Cline finds no run to abort then, and still answers that it applied the
	// abort, so the worker aborts the run when it starts (markRunStarted).
	abortOnStart bool
	startedAt    time.Time
	// toolUses counts the tool calls of the turn that ended, which the turn-end
	// row carries.
	toolUses int
	// actModeApproved records that the model called switch_to_act_mode in this
	// turn and the worker answered it, so the session rebuilds in Act mode when
	// the turn ends.
	actModeApproved bool
	// settling marks the turn that holds the input queue while a mode change
	// applies: one that waited for the previous turn (afterTurn), or one
	// between two turns (UpdateSettings). No run of Cline's belongs to it, so
	// no end event of a run ends it: the rebuild's own detach can publish one.
	settling bool
}

// hasNoRun identifies a turn that unsolicited output started before any native run.
// A turn that a LeapMux message started waits for its native run.
// A settling turn ends with its mode change.
func (t turnState) hasNoRun() bool {
	return t.active && !t.steerable && !t.settling && !t.runStarted
}

// defaultSendWait limits how long SendInput waits for the daemon to start the
// turn. The daemon publishes run.started as soon as it reads the command, so
// the limit only stops a stalled daemon from holding the caller.
const defaultSendWait = 30 * time.Second

// errAgentStopped refuses work for an agent that Stop ended.
var errAgentStopped = errors.New("agent is stopped")

// errNoSession refuses input before a session exists.
var errNoSession = errors.New("agent has no Cline session")

// SendInput starts a turn with one user message.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments)
}

// SendInputForSession starts a turn with one user message, when the session it
// states is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments)
}

// sendInput starts a turn.
//
// A plain message during a turn would enter Cline's queue without LeapMux's knowledge.
// The agent refuses that message because LeapMux owns the input queue.
// The worker starts the turn before the send, so the queue holds the next message.
//
// The send returns when the daemon publishes run.started with the command's request ID.
// The daemon sends the command reply only when the turn ends.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return errAgentStopped
	}
	sessionID, busy, steerable, mode := a.sessionID, a.turn.active, a.turn.steerable, a.settings.sessionMode()
	a.Mu.Unlock()
	if sessionID == "" {
		return errNoSession
	}
	if busy {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: steerable}
	}
	input, err := a.buildInput(content, attachments)
	if err != nil {
		return err
	}
	a.beginInputTurn()
	if err := a.deliver(sessionID, input.payload(sessionID, mode, "")); err != nil {
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			a.releaseInputTurn()
		}
		return err
	}
	return nil
}

// deliver sends one session.send_input and waits for its run.started. The turn
// request's reply arrives when the turn ends; a refusal before that fails the
// delivery.
func (a *Agent) deliver(sessionID string, payload map[string]any) error {
	ctx, cancel := context.WithTimeout(a.ctx, a.sendWait)
	defer cancel()
	started := make(chan error, 1)
	var prepared string
	requestID, replies, err := a.hub.send(ctx, commandSessionSendInput, sessionID, payload, func(requestID string) {
		prepared = requestID
		a.Mu.Lock()
		if a.turn.active && a.turn.requestID == "" {
			a.turn.requestID = requestID
		}
		if a.deliveries == nil {
			a.deliveries = make(map[string]chan error)
		}
		a.deliveries[requestID] = started
		a.Mu.Unlock()
	})
	if err != nil {
		if prepared != "" {
			a.Mu.Lock()
			delete(a.deliveries, prepared)
			a.Mu.Unlock()
		}
		return fmt.Errorf("deliver the message to Cline: %w", err)
	}
	go a.watchTurnReply(sessionID, requestID, replies)

	select {
	case err := <-started:
		return err
	case <-ctx.Done():
		a.Mu.Lock()
		delete(a.deliveries, requestID)
		a.Mu.Unlock()
		return fmt.Errorf("%w: Cline did not confirm the message within %s", agent.ErrDeliveryUncertain, a.sendWait)
	}
}

// watchTurnReply waits for the reply to one session.send_input.
// A refusal before run.started fails the delivery. A reply after the turn-end event changes nothing.
// A failed promise in the daemon can produce a failed reply after run.started without an end event.
// That reply ends the turn as an error, as a native run.failed event does.
func (a *Agent) watchTurnReply(sessionID, requestID string, replies <-chan hubReply) {
	ctx := a.ctx
	var reply hubReply
	var ok bool
	select {
	case reply, ok = <-replies:
	case <-ctx.Done():
		return
	}
	if ok && reply.OK {
		a.settleDelivery(requestID, nil)
		return
	}
	// A connection that failed after the command left says nothing about
	// whether the daemon read it.
	failure := fmt.Errorf("%w: %v", agent.ErrDeliveryUncertain, errHubConnectionLost)
	if ok {
		refused := &HubCommandError{Command: commandSessionSendInput}
		if reply.Error != nil {
			refused.Code, refused.Message = reply.Error.Code, reply.Error.Message
		}
		failure = refused
	}
	if a.settleDelivery(requestID, failure) {
		// The delivery failed, and the sender releases the turn.
		return
	}
	if !ok {
		// A lost connection says nothing about the turn: the reconnect replays
		// the events it missed, and the run's end event ends the turn.
		return
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	current := a.sessionID == sessionID && a.turn.active && a.turn.requestID == requestID
	a.Mu.Unlock()
	if current {
		a.endTurn(agent.MessageCompletionError, runEndRow(sessionID, runReasonError, failure.Error()))
	}
}

// settleDelivery hands the outcome of one delivery to the sender that waits for
// it, and reports whether one waited.
func (a *Agent) settleDelivery(requestID string, err error) bool {
	a.Mu.Lock()
	started := a.deliveries[requestID]
	delete(a.deliveries, requestID)
	a.Mu.Unlock()
	if started == nil {
		return false
	}
	started <- err
	return true
}

// SupportsSteering accepts a steer during any turn that LeapMux started.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn.
// Cline stops the running model request as soon as it accepts the steer.
// The cut answer keeps an interrupted completion. The next model request receives the steer.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	sessionID, active, steerable, stopped, mode := a.sessionID, a.turn.active, a.turn.steerable, a.StoppedLocked(), a.settings.sessionMode()
	sequence, reports := a.turn.sequence, a.turn.steerReports
	a.Mu.Unlock()
	if stopped {
		return errAgentStopped
	}
	if !active {
		return agent.ErrNoActiveTurn
	}
	if !steerable {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: false}
	}
	input, err := a.buildInput(content, attachments)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, a.sendWait)
	defer cancel()
	if _, err := a.hub.command(ctx, commandSessionSendInput, sessionID, input.payload(sessionID, mode, deliverySteer)); err != nil {
		if _, refused := hubErrorCode(err); refused {
			return err
		}
		return fmt.Errorf("%w: Cline did not confirm the steer: %v", agent.ErrDeliveryUncertain, err)
	}
	a.Mu.Lock()
	// Native reports take precedence, including a submission that arrives before this reply.
	if a.sessionID == sessionID && a.turn.active && a.turn.sequence == sequence && a.turn.steerReports == reports {
		a.turn.steersPending++
	}
	a.Mu.Unlock()
	return nil
}

// beginInputTurn marks a new turn active and publishes it.
func (a *Agent) beginInputTurn() {
	now := a.clock.Now()
	a.Mu.Lock()
	a.turn = turnState{active: true, steerable: true, startedAt: now, sequence: a.NextTurnSeq()}
	a.Mu.Unlock()
	a.sink.ReportProgress(agent.ResetModelProgress())
	a.PublishTurnActive()
}

// releaseInputTurn releases a turn whose message never reached Cline.
func (a *Agent) releaseInputTurn() {
	a.Mu.Lock()
	a.turn = turnState{}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// ensureTurn starts a turn that Cline initiated without a user message.
// Such a turn takes no steer. A completed team run can resume the lead this way.
func (a *Agent) ensureTurn() {
	now := a.clock.Now()
	a.Mu.Lock()
	if a.turn.active {
		a.Mu.Unlock()
		return
	}
	a.turn = turnState{active: true, startedAt: now}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// PublishTurnActive republishes the turn flag, and whether the running turn
// takes a steer.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turn.active, Steerable: a.turn.active && a.turn.steerable}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, seq)
}

// HandleOutput dispatches one hub event envelope, as the hub's dispatcher does.
// Tests feed events through it.
func (a *Agent) HandleOutput(content []byte) {
	var event hubEvent
	if err := json.Unmarshal(content, &event); err != nil || event.Event == "" {
		slog.Warn("cline event cannot be read", "agent_id", a.AgentID(), "error", err)
		return
	}
	event.Raw = append(json.RawMessage(nil), content...)
	a.handleEvent(event)
}

// currentSession returns the session the agent drives.
func (a *Agent) currentSession() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.sessionID
}

// requestContext limits one hub command to the configured API timeout and to
// the process's own lifetime.
func (a *Agent) requestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, a.APITimeout())
}
