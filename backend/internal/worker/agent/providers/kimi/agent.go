package kimi

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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one `kimi web` process and the one session LeapMux drives in
// it.
//
// The process is the kap-server, which speaks no protocol on its stdio: stdout
// carries the ready line and log lines, and the conversation runs over REST and
// one WebSocket (connection.go). So Agent embeds Process for the lifecycle
// alone -- start, stop, exit, stderr -- and routes everything else through the
// server's HTTP API.
type Agent struct {
	providerkit.Process

	sink       agent.ProviderServices
	workingDir string
	// clock supplies the time the agent records -- a goal's first sight and a
	// session's attach -- and the event stream's timers. Production uses the real
	// clock, and a test injects a mock.
	clock quartz.Clock

	api    *kimiClient
	stream *kimiStream
	// endpoint is the server's HTTP endpoint. Stop closes its idle connections.
	endpoint *providerkit.HTTPEndpoint
	// features lists the engine features GET /meta reported as active. Read once
	// at startup and never mutated, so it needs no lock.
	features map[string]bool

	// dispatchMu serializes event dispatch. The stream's dispatcher goroutine and
	// HandleOutput both reach handleFrame, and one event's handling reads and
	// writes several fields in sequence -- a tool call opened by one goroutine and
	// closed by the other before it opened would leave its card running for good.
	dispatchMu sync.Mutex
	// sessionMu serializes the operations that change which session the agent
	// drives or how it is configured: ClearContext, UpdateSettings, and the goal
	// writes. Never held while a.Mu is held.
	sessionMu sync.Mutex

	// --- guarded by Mu ---

	sessionID string
	// attachedAt is when the agent began to drive the current session. The
	// server restores a session's old tasks, so a task that started before this
	// time is the session's history, and a resync opens no row for it.
	attachedAt time.Time
	// turnActive is true while the MAIN agent runs a turn, whoever started it.
	turnActive bool
	// turnSteerable is true while the running main turn takes a steer: a
	// tracked prompt started it (kimiOriginTakesSteer).
	turnSteerable bool
	// settings is the live configuration of the main agent. See settings.go.
	settings kimiSettings
	// catalog is the model list GET /models reported, and the configured
	// default model.
	catalog kimiCatalog
	// contextUsage is the broadcast-shaped context readout, kept so a turn end
	// carries the last one.
	contextUsage map[string]any
	// lastTurnError identifies the failure the last main turn ended with. The
	// server repeats it as an `error` event right after the turn end, and the
	// divider already states it.
	lastTurnError string
	// runs holds the output state of each agent of the session: the main agent
	// and every subagent that reported an event. See output.go.
	runs map[string]*kimiRun
	// controls holds every approval and question LeapMux published and nothing
	// resolved yet. See control.go.
	controls map[string]*kimiPendingControl
	// tasks holds the background tasks the session reported. See tasks.go.
	tasks map[string]*kimiTask
	// goal is the last goal the session reported. See goal.go.
	goal kimiGoalState

	// children maps a subagent to its child transcript. See subagent.go.
	children kimiChildIndex

	// descendantGroups is the Stop hook that lists the process groups the
	// server's tools run in. A test replaces it.
	descendantGroups func(rootPID int) []int
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
	_ agent.ChildSteerer     = (*Agent)(nil)
	_ agent.ChildInterrupter = (*Agent)(nil)
	_ agent.GoalWriter       = (*Agent)(nil)
)

// kimiSendWait limits how long SendInput waits for the server to accept a
// prompt. The server replies once the prompt launched or queued, which takes a
// few milliseconds; the limit stops a stalled server from holding the caller.
const kimiSendWait = 30 * time.Second

// SendInput delivers a user message to the main agent.
//
// It returns once the server accepted the prompt -- the POST replies when the
// turn launched -- and never waits for the turn. A running turn refuses the
// input with ErrAgentBusy, so the LeapMux input queue holds it; Kimi Code would
// queue it itself, but then LeapMux would show the message as the start of a
// turn that has not started.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments)
}

// SendInputForSession validates the session before it sends.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments)
}

func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return errors.New("agent is stopped")
	}
	sessionID, busy, steerable, model := a.sessionID, a.turnActive, a.turnSteerable, a.settings.model
	a.Mu.Unlock()
	if sessionID == "" {
		return errors.New("agent has no Kimi Code session")
	}
	if busy {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: steerable}
	}
	parts, err := buildKimiContent(content, attachments, a.modelTakesImages(model))
	if err != nil {
		return err
	}
	// The turn flag moves on the turn.started event and never here. The server
	// replies once the turn launched, and a short turn can END before the reply
	// arrives: a flag raised from the reply would then latch a turn that is over,
	// with no event left to clear it. A second message that slips in before
	// turn.started waits in the server's own queue and runs next, which delivers
	// it all the same.
	_, err = a.submitPrompt(sessionID, kimiMainAgentID, parts)
	return err
}

// kimiPromptReply is the data of POST /sessions/{id}/prompts.
type kimiPromptReply struct {
	PromptID string `json:"prompt_id"`
	Status   string `json:"status"`
}

// kimiPromptQueued is the `status` of a prompt the server queued behind a
// running turn. A prompt that launched a turn states `running`.
const kimiPromptQueued = "queued"

// submitPrompt posts one prompt to one agent of the session.
func (a *Agent) submitPrompt(sessionID, agentID string, parts []map[string]any) (kimiPromptReply, error) {
	if err := kimiCheckID("session", sessionID); err != nil {
		return kimiPromptReply{}, err
	}
	body := map[string]any{"content": parts}
	if agentID != kimiMainAgentID {
		body["agent_id"] = agentID
	}
	ctx, cancel := context.WithTimeout(a.Context(), kimiSendWait)
	defer cancel()
	var reply kimiPromptReply
	if err := a.api.post(ctx, kimiSessionPath(sessionID, "/prompts"), body, &reply); err != nil {
		return kimiPromptReply{}, classifyKimiDeliveryError(err)
	}
	return reply, nil
}

// classifyKimiDeliveryError marks a failure whose delivery the server may have
// taken anyway. A refusal the server stated is a clear failure. A transport
// failure after the request left -- a timeout, a reset -- is uncertain, and the
// queue must not send the message a second time.
func classifyKimiDeliveryError(err error) error {
	if _, stated := kimiErrorCode(err); stated {
		return err
	}
	var statusErr *providerkit.HTTPStatusError
	if errors.As(err, &statusErr) {
		return err
	}
	return fmt.Errorf("%w: Kimi Code did not confirm the prompt: %v", agent.ErrDeliveryUncertain, err)
}

// SupportsSteering is always true: the server queues a prompt that arrives
// during a turn, and `prompts:steer` moves it into the running turn at its next
// step. A turn that no tracked prompt started takes no steer, and the turn
// state says so (kimiOriginTakesSteer).
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running main turn.
//
// It posts the message, which the server queues behind the turn, and then
// steers the queued prompt into it. A turn that ended in between launched the
// message as a turn of its own, which delivered it as well.
//
// A turn that no tracked prompt started refuses the steer, and the posted
// message would wait in the server's queue behind it. SteerInput posts nothing
// for such a turn and reports it busy, so the input queue holds the message
// until the turn ends.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	sessionID, active, steerable, stopped, model := a.sessionID, a.turnActive, a.turnSteerable, a.StoppedLocked(), a.settings.model
	a.Mu.Unlock()
	if stopped {
		return errors.New("agent is stopped")
	}
	if !active {
		return agent.ErrNoActiveTurn
	}
	if !steerable {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: false}
	}
	parts, err := buildKimiContent(content, attachments, a.modelTakesImages(model))
	if err != nil {
		return err
	}
	reply, err := a.submitPrompt(sessionID, kimiMainAgentID, parts)
	if err != nil {
		return err
	}
	if reply.Status != kimiPromptQueued {
		// The turn ended before the prompt arrived, so the prompt started the next
		// turn. The text reached the agent either way, and turn.started moves the
		// flag for the new turn.
		return nil
	}
	if err := kimiCheckID("prompt", reply.PromptID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.Context(), kimiSendWait)
	defer cancel()
	err = a.api.post(ctx, kimiItemPath(sessionID, "prompts", reply.PromptID, ":steer"), nil, nil)
	if err == nil {
		return nil
	}
	if code, stated := kimiErrorCode(err); stated && code == kimiCodePromptNotPending {
		// The server refuses the steer in two cases, and in each one it delivers
		// the prompt with no second send:
		//
		//   - The prompt left the queue: the turn ended between the two requests,
		//     and the prompt launched as the next turn.
		//   - The running turn is not one a tracked prompt started: a turn that
		//     waited in the server's queue ahead of the prompt -- a task
		//     notification, a cron fire -- started when the steerable turn ended.
		//     The prompt stays queued and runs as the turn after that one.
		return nil
	}
	// The prompt still waits in the server's queue and would run after the turn.
	// Withdraw it, so a caller that retries the steer cannot deliver the text
	// twice. A withdrawal that fails leaves the delivery uncertain, and the
	// queue must not send it again.
	abortErr := a.api.post(ctx, kimiItemPath(sessionID, "prompts", reply.PromptID, kimiActionAbort), nil, nil)
	if abortErr != nil {
		return fmt.Errorf("%w: the steer failed (%v) and the queued prompt could not be withdrawn: %v",
			agent.ErrDeliveryUncertain, err, abortErr)
	}
	return fmt.Errorf("steer the queued prompt into the running turn: %w", err)
}

// kimiCodePromptNotPending (PROMPT_NOT_FOUND) refuses a steer. The server states
// it for a prompt that is no longer queued -- it launched, or it finished --
// and for a running turn that no tracked prompt started.
const kimiCodePromptNotPending = 40402

// PublishTurnActive republishes the main turn flag, and whether the running
// turn takes a steer (kimiOriginTakesSteer).
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive, Steerable: a.turnActive && a.turnSteerable}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, seq)
}

// SendRawInput delivers a raw frame: a control response, or the raw abort
// frame. Kimi Code reads nothing on stdin, so there is no frame to forward
// verbatim.
func (a *Agent) SendRawInput(data []byte) error {
	if a.IsStopped() {
		return errors.New("agent is stopped")
	}
	if isKimiRawAbort(data) {
		return a.Interrupt()
	}
	return a.deliverControlResponse(data)
}

// isKimiRawAbort reports whether data is the raw abort frame.
func isKimiRawAbort(data []byte) bool {
	var frame struct {
		Action string `json:"action"`
	}
	return json.Unmarshal(data, &frame) == nil && frame.Action == kimiRawAbortAction
}

// HandleOutput dispatches one WebSocket frame, as the stream's dispatcher does.
// Tests feed frames through it.
func (a *Agent) HandleOutput(content []byte) {
	var frame kimiFrame
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("kimi frame is not JSON", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.handleFrame(frame)
}

// requestContext limits one REST request to the configured API timeout and to
// the process's own lifetime.
func (a *Agent) requestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.Context(), a.APITimeout())
}

// modelTakesImages reports whether the model accepts image input.
func (a *Agent) modelTakesImages(model string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.catalog.takesImages(model)
}
