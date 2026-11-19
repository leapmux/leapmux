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

// Agent manages one kimi web process and one native session.
// The kap-server writes readiness and log lines to stdout. Conversation events use REST and one WebSocket, as connection.go describes.
// The embedded Process owns process lifetime and stderr. The native API supplies the other operations.
type Agent struct {
	providerkit.Process

	sink       agent.ProviderServices
	workingDir string
	// clock supplies recorded timestamps and event-stream timers. Tests can inject a mock.
	clock quartz.Clock

	api    *kimiClient
	stream *kimiStream
	// endpoint is the server's HTTP endpoint. Stop closes its idle connections.
	endpoint *providerkit.HTTPEndpoint
	media    *kimiMediaService
	// features lists the engine features GET /meta reported as active. Read once
	// at startup and never mutated, so it needs no lock.
	features map[string]bool

	// dispatchMu serializes handleFrame calls from the event stream and HandleOutput.
	// Without it, a tool result could precede its opener and leave the card active indefinitely.
	dispatchMu kimiPublicationMutex
	// Queued provider writes share one readiness owner across dispatch and session mutations.
	publicationOnce  sync.Once
	publicationOwner *agent.OutputOwner
	// The launch stamp is immutable. dispatchMu protects the warning fingerprints.
	serverLaunchedAt      time.Time
	storedConfigWarnings  map[kimiConfigWarningKey]struct{}
	pendingConfigWarnings []kimiFrame
	pendingSession        *kimiSessionAdmission
	storedSessionWarnings map[string]struct{}
	// sessionMu serializes session replacement and configuration changes.
	// ClearContext and UpdateSettings use it. Goal writes use it also.
	// A caller must not acquire it while it holds Mu.
	sessionMu              kimiPublicationMutex
	approvalWork           providerkit.CoalescingWork
	taskRecoveryWork       providerkit.CoalescingWork
	taskReportWork         providerkit.CoalescingWork
	goalMetadataWork       providerkit.CoalescingWork
	transcriptRecoveryWork providerkit.CoalescingWork
	// dispatchMu protects bytes retained for native transcript recovery.
	pendingNativeBytes int64
	// dispatchMu protects the applied native restoration fence.
	invocationRecovery *kimiInvocationRecovery
	controlReplay      *kimiControlReplay
	// dispatchMu protects locally submitted prompt identities and child admission.
	promptSubmissions map[string]*kimiPromptSubmission
	// usageWork reads the plan usage of the Kimi Code account. See
	// rate_limits.go.
	usageWork providerkit.CoalescingWork

	// --- guarded by Mu ---

	sessionID string
	// attachedAt marks when the worker began driving this session. Native
	// recovery uses it to omit historical notices and finished turns.
	attachedAt time.Time
	// turnActive is true while the MAIN agent runs a turn, whoever started it.
	turnActive bool
	// turnSteerable is true while the running main turn takes a steer: a
	// tracked prompt started it.
	turnSteerable bool
	// settings is the live configuration of the main agent. See settings.go.
	settings kimiSettings
	// catalog is the model list GET /models reported, and the configured
	// default model.
	catalog kimiCatalog
	// contextUsage is the broadcast-shaped context readout, kept so a turn end
	// carries the last one.
	contextUsage map[string]any
	// runs holds the output state of each agent of the session: the main agent
	// and every subagent that reported an event. See output.go.
	runs map[string]*kimiRun
	// controls holds every approval and question LeapMux published and nothing
	// resolved yet. See control.go.
	controls map[string]*kimiPendingControl
	// tasks holds the background tasks the session reported. See tasks.go.
	tasks map[kimiTaskAddress]*kimiTask
	// goal is the last goal the session reported. See goal.go.
	goal         kimiGoalState
	goalPushSeen bool
	// trustRestartRequired is true once the reader trusted the workspace: the
	// session that runs loads the project's configuration only in a new process.
	// See workspace_trust.go.
	trustRestartRequired bool

	// children maps a subagent to its child transcript. See subagent.go.
	children kimiChildIndex

	// workspaceTrust holds the workspace-trust question that LeapMux asks for
	// Kimi Code. See workspace_trust.go.
	workspaceTrust providerkit.WorkspaceTrust
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
	_ agent.ChildSteerer     = (*Agent)(nil)
	_ agent.GoalWriter       = (*Agent)(nil)
)

// kimiSendWait limits how long a stalled server can hold SendInput.
// The reply can state that the prompt started, entered the queue, or was blocked.
const kimiSendWait = 30 * time.Second

// SendInput delivers a user message to the main agent.
//
// The POST reply states whether the prompt started, queued, or was blocked.
// This call never waits for a turn to finish.
// A running turn returns ErrAgentBusy so the LeapMux queue retains the input.
// Native queueing would otherwise show that input before its turn starts.
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
	inputState := a.inputStateLocked(kimiMainAgentID)
	sessionID, busy, steerable, model := a.sessionID, inputState.Active, inputState.Steerable, a.settings.model
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
	// The native turn header moves the turn flag, never this reply. The server
	// replies once the turn launched, and a short turn can END before the reply
	// arrives: a flag raised from the reply would then latch a turn that is over,
	// with no event left to clear it. A second message that slips in before
	// the native turn header waits in the server's queue and runs next, which delivers
	// it all the same.
	reply, err := a.submitPrompt(sessionID, kimiMainAgentID, parts)
	if err == nil && reply.Status == kimiPromptBlocked {
		a.PublishTurnActive()
	}
	return err
}

// submitPrompt posts one prompt to one agent of the session.
func (a *Agent) submitPrompt(sessionID, agentID string, parts []map[string]any) (kimiPromptReply, error) {
	if err := kimiCheckID("session", sessionID); err != nil {
		return kimiPromptReply{}, err
	}
	submission, err := a.reservePromptSubmission(sessionID, agentID)
	if err != nil {
		return kimiPromptReply{}, err
	}
	return a.submitReservedPrompt(submission, parts)
}

func (a *Agent) submitReservedPrompt(submission *kimiPromptSubmission, parts []map[string]any) (kimiPromptReply, error) {
	sessionID, agentID := submission.sessionID, submission.receiver
	body := map[string]any{"content": parts, "prompt_id": submission.id}
	if agentID != kimiMainAgentID {
		body["agent_id"] = agentID
	}
	ctx, cancel := context.WithTimeout(a.Context(), kimiSendWait)
	defer cancel()
	var reply kimiPromptReply
	if err := a.api.post(ctx, kimiSessionPath(sessionID, "/prompts"), body, &reply); err != nil {
		deliveryErr := classifyKimiDeliveryError(err)
		a.finishPromptSubmission(submission, deliveryErr)
		return kimiPromptReply{}, deliveryErr
	}
	if reply.PromptID != submission.id {
		return kimiPromptReply{}, fmt.Errorf("%w: Kimi Code returned a different prompt id", agent.ErrDeliveryUncertain)
	}
	if reply.Status != kimiPromptRunning && reply.Status != kimiPromptQueued && reply.Status != kimiPromptBlocked {
		return kimiPromptReply{}, fmt.Errorf("%w: Kimi Code returned an invalid prompt status %q", agent.ErrDeliveryUncertain, reply.Status)
	}
	if reply.Status == kimiPromptBlocked {
		a.finishPromptSubmission(submission, nil)
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
// state says so.
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
	inputState := a.inputStateLocked(kimiMainAgentID)
	sessionID, active, steerable, stopped, model := a.sessionID, inputState.Active, inputState.Steerable, a.StoppedLocked(), a.settings.model
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
		// turn. The text reached the agent either way, and its native header moves the
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

// PublishTurnActive reports a main turn or manual compaction to the input queue.
// Only a running user turn takes a steer.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := a.inputStateLocked(kimiMainAgentID)
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, seq)
}

// inputStateLocked is the common authority for publication and prompt admission.
// The caller holds Mu; the lookup creates no native run.
func (a *Agent) inputStateLocked(nativeAgentID string) agent.TurnState {
	if a.StoppedLocked() || a.ProcessExitedLocked() {
		return agent.TurnState{}
	}
	run := a.runs[nativeAgentID]
	active, blocked, steerable := false, false, false
	if run != nil {
		active = run.turnActive
		blocked = run.compacting || run.conversationRecovery.Load() != nil
	}
	if nativeAgentID == kimiMainAgentID {
		active, steerable = a.turnActive, a.turnSteerable
	}
	return agent.TurnState{Active: active || blocked, Steerable: active && steerable && !blocked}
}

// SendRawInput delivers a raw frame: a control response, or the raw abort
// frame. Kimi Code reads nothing on stdin, so there is no frame to forward
// verbatim.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	if a.IsStopped() {
		return errors.New("agent is stopped")
	}
	if isKimiRawAbort(data) {
		return a.Interrupt(stop)
	}
	// LeapMux asked the workspace-trust question, so no server request waits for its answer.
	if handled, err := a.workspaceTrust.Answer(a.sink, kimiWorkspaceTrustHost{a}, data); handled {
		return err
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
