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
// The daemon speaks no protocol on its stdio: the conversation runs over one
// WebSocket (rpc.go). So Agent embeds the daemon's Process for the lifecycle
// alone -- start, stop, exit, stderr -- and routes everything else through the
// hub client.
type Agent struct {
	*providerkit.Process

	sink agent.ProviderServices
	opts agent.Options
	// dir is the agent's private directory: the discovery record, the private
	// task database and the attached files live in it.
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

	// ctx lives as long as the agent: Stop and the daemon's exit cancel it. The
	// hub client, every command and every goroutine of the agent end with it.
	ctx    context.Context
	cancel context.CancelFunc
	// background tracks the goroutines that outlive the event that started
	// them: a child transcript read from Cline's store, a mode change after a
	// turn, an automatic answer, and an abort that waited for its run. Stop and
	// Wait wait for them.
	background sync.WaitGroup

	// dispatchMu serializes event dispatch. The hub's dispatcher goroutine and
	// HandleOutput both reach handleEvent, and one event's handling reads and
	// writes the output state in sequence.
	dispatchMu sync.Mutex
	// sessionMu serializes the operations that change which session the agent
	// drives or how Cline builds it: the first open, ClearContext, a rebuild for
	// a new mode, the settings writes, and the release of the claims at the
	// teardown. A holder of Mu never takes it.
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
// Manager.SupportsSteering answers false, with no build error, for a provider
// that stops satisfying InputSteerer, so the assertion makes that regression a
// compile error.
var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.InputSteerer = (*Agent)(nil)
)

// turnState is the turn the agent owes the user a reply for. Guarded by Mu.
type turnState struct {
	active bool
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

// hasNoRun reports whether no run end can end the turn: output that arrived
// with no turn armed it (ensureTurn), and no run of it started. A turn that a
// LeapMux message started waits for the run that the message starts, and a
// settling turn ends with its mode change.
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
// A plain message during a turn would join Cline's own queue and run as the
// next turn, out of LeapMux's sight, so the agent refuses it while a turn runs:
// the LeapMux input queue owns queueing. The turn is armed BEFORE the send, so
// the queue holds the next message behind this one from the moment it leaves.
//
// It returns once the daemon started the turn: the daemon answers the command
// only when the turn ends, and publishes run.started with the command's
// request id at once.
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
	a.armTurn()
	if err := a.deliver(sessionID, input.payload(sessionID, mode, "")); err != nil {
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			a.disarmTurn()
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

// watchTurnReply waits for the reply of one session.send_input. A refusal that
// arrives before run.started fails the delivery. A reply that arrives after the
// turn's end event changes nothing. A failure after run.started with no end
// event -- the run's promise rejected inside the daemon -- ends the
// turn as an error, which is what the daemon's own run.failed states in every
// other case.
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
		// The delivery failed, and the sender disarms the turn.
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

// SupportsSteering reports true: the hub takes a steer during any turn that
// LeapMux started, and joins it to the turn at its next step.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn. The daemon queues it with the
// `steer` delivery, answers at once, and submits it as the next user message
// of the same run when the current model step ends.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	sessionID, active, steerable, stopped, mode := a.sessionID, a.turn.active, a.turn.steerable, a.StoppedLocked(), a.settings.sessionMode()
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
	return nil
}

// armTurn marks a new turn active and publishes it.
func (a *Agent) armTurn() {
	now := a.clock.Now()
	a.Mu.Lock()
	a.turn = turnState{active: true, steerable: true, startedAt: now}
	a.Mu.Unlock()
	a.sink.ReportProgress(agent.ResetModelProgress())
	a.PublishTurnActive()
}

// disarmTurn releases a turn whose message never reached Cline.
func (a *Agent) disarmTurn() {
	a.Mu.Lock()
	a.turn = turnState{}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// ensureTurn arms a turn that Cline started by itself: a team run that ended
// resumes the lead with no message from the user. Such a turn takes no steer.
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
