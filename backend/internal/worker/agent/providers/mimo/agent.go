package mimo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// mainActorID is the actor id of the session's main agent. Every other actor
// id is a subagent's.
const mainActorID = "main"

// Agent manages one `mimo serve` process and the one session it drives.
//
// The process speaks no protocol on stdio. Its stdout carries the listen line
// and log lines, which Process.ReadLines drains; the session runs over the REST
// routes in rpc.go and the event stream that connection.go reads.
//
// One goroutine reads the event stream and calls every handler in order, so
// two events never race. Mu guards the state below it, because the worker's
// own calls (SendInput, UpdateSettings, a control answer) run on other
// goroutines. No handler holds Mu across a sink call or an HTTP request.
type Agent struct {
	providerkit.Process

	sink       agent.ProviderServices
	rpc        mimoRPC
	workingDir string

	// streamCancel ends the event stream. Stop calls it first, so the stream does
	// not reconnect to a server that is going away.
	streamCancel context.CancelFunc
	// streamDone closes when the stream goroutine returns.
	streamDone chan struct{}
	// connected closes at the first server.connected. A session that the start
	// path creates after it cannot lose an event to a stream that was not yet open.
	connected     chan struct{}
	connectedOnce sync.Once
	// clock supplies every timer of the agent and the time that it records.
	// Production uses the real clock, and a test injects a mock. Each timer
	// carries one of the tags below, so a test can catch it.
	clock quartz.Clock
	// dispatchMu serializes the event handlers with the two other paths that
	// change the turn: the reconcile a timer runs after an abort, and the
	// session switch of ClearContext. Each handler reads and writes state in
	// several steps under a.Mu, and a second writer between two of them would
	// see a half-applied turn.
	dispatchMu sync.Mutex

	// --- guarded by Mu ---

	sessionID string
	catalog   mimoCatalog
	// model is the `<provider>/<model>` every prompt states. Empty until the
	// catalog names one, and then the server picks.
	model string
	// effort is the variant every prompt states, or agent.EffortAuto for none.
	effort string
	// mode is the primary agent every prompt runs on.
	mode string
	// permissionPolicy is the policy that the server's two switches carry.
	permissionPolicy string

	turnActive bool
	// interruptRequested marks a turn that an abort ended, so its end reads as
	// interrupted rather than failed.
	interruptRequested bool
	// turnFailure holds the session.error that failed the running turn. The turn
	// end persists it as the divider, which states the reason.
	turnFailure []byte
	// unattributed holds a session.error that no message has claimed yet. A
	// subagent runs in the agent's own session, so its failure arrives as the
	// session's, and only the failed message that follows says whose it is.
	unattributed *mimoFailure
	// lastTurnFailed marks that the last turn ended with a failure and that no
	// input followed. MiMo repeats a failure's error after the turn ends, and
	// this is how that repeat is told apart from a new failure.
	lastTurnFailed bool

	messages map[string]*mimoMessageRecord
	parts    map[string]*mimoTextPart
	tools    map[string]*mimoToolCall
	// nextToolOrder orders the tool calls that a turn end closes.
	nextToolOrder uint64
	// compactions records the compaction parts already persisted, keyed by part
	// id, with the phase each one reached.
	compactions map[string]string
	usage       mimoUsage

	actors      map[string]*mimoActor
	spawnActors map[string]string
	workflows   map[string]*mimoWorkflow

	controls map[string]*mimoControl

	goal mimoGoalState
	// compactionAck is non-nil while CompactContext waits for the compaction to
	// start.
	compactionAck chan struct{}

	// buffers holds each actor's streamed text until its part ends. The map is
	// guarded by Mu; each buffer has its own lock.
	buffers map[string]*providerkit.GenerationBuffer
	// spawnPrompts holds a spawn's prompt, keyed by its tool call, until the
	// child transcript exists.
	spawnPrompts providerkit.PendingPrompts
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ChildSteerer     = (*Agent)(nil)
	_ agent.GoalWriter       = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

// The tags of the agent's timers. A test traps a timer by its tag.
const (
	// mimoStreamConnectTimerTag limits the start's wait for the first connection
	// of the event stream.
	mimoStreamConnectTimerTag = "mimo-stream-connect"
	// mimoStreamReconnectTimerTag is the wait between two connections of the
	// event stream.
	mimoStreamReconnectTimerTag = "mimo-stream-reconnect"
	// mimoStreamStopTimerTag limits Stop's wait for the stream goroutine.
	mimoStreamStopTimerTag = "mimo-stream-stop"
	// mimoAbortGraceTimerTag is the grace after an abort, before the worker asks
	// the server whether the session is still busy.
	mimoAbortGraceTimerTag = "mimo-abort-grace"
	// mimoCompactionStartTimerTag limits CompactContext's wait for the
	// compaction to start.
	mimoCompactionStartTimerTag = "mimo-compaction-start"
	// mimoGoalConfirmTimerTag limits a goal set's wait for MiMo to confirm the
	// goal.
	mimoGoalConfirmTimerTag = "mimo-goal-confirm"
)

// newAgentState returns an agent with its maps made and no process. Start and
// the tests build every agent through it, and then assign the process.
func newAgentState(sink agent.ProviderServices, rpc mimoRPC, workingDir string, clock quartz.Clock) *Agent {
	return &Agent{
		sink:        sink,
		rpc:         rpc,
		workingDir:  workingDir,
		connected:   make(chan struct{}),
		streamDone:  make(chan struct{}),
		clock:       clock,
		messages:    map[string]*mimoMessageRecord{},
		parts:       map[string]*mimoTextPart{},
		tools:       map[string]*mimoToolCall{},
		compactions: map[string]string{},
		actors:      map[string]*mimoActor{},
		spawnActors: map[string]string{},
		workflows:   map[string]*mimoWorkflow{},
		controls:    map[string]*mimoControl{},
		buffers:     map[string]*providerkit.GenerationBuffer{},
		mode:        mimoStaticModes[0].Id,
	}
}

// SendInput delivers a user message to the main agent. It returns once the
// server accepted the prompt, and never waits for the turn.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, false)
}

// SendInputForSession is SendInput for the session the caller states.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, false)
}

// SupportsSteering always reports true. A prompt that reaches a running turn
// joins it: the main loop reads the new message at its next step.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, true)
}

// sendInput sends one prompt to the main agent.
//
// A plain send refuses while a turn runs, with ErrAgentBusy, so the queue holds
// the message for the next turn. A steer requires a running turn. The server
// takes both through the same route: a prompt that reaches a running loop
// joins it, and one that reaches an idle session starts a turn.
//
// A steer whose turn ended between the check and the request is still
// delivered: it starts a new turn, and the server holds its text. Reporting
// ErrNoActiveTurn there would make the queue send the same text a second time.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	parts, err := buildPromptParts(content, attachments)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID, turnActive := a.sessionID, a.turnActive
	request := a.promptRequestLocked(parts, "")
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no MiMo session")
	}
	if !steer && turnActive {
		return agent.ErrAgentBusy
	}
	if steer && !turnActive {
		return agent.ErrNoActiveTurn
	}
	if err := a.rpc.promptAsync(a.Context(), sessionID, request); err != nil {
		return classifyDeliveryError("prompt", err)
	}
	a.Mu.Lock()
	a.lastTurnFailed = false
	a.Mu.Unlock()
	return nil
}

// promptRequestLocked builds a prompt that states the current agent, model and
// variant. actorID addresses a subagent; empty addresses the main agent, which
// is the only one that the mode applies to. The caller holds a.Mu.
func (a *Agent) promptRequestLocked(parts []mimoPromptPart, actorID string) mimoPromptRequest {
	request := mimoPromptRequest{Parts: parts, AgentID: actorID}
	if actorID == "" {
		request.Agent = a.mode
	}
	if ref, ok := splitModelID(a.model); ok {
		request.Model = &ref
		request.Variant = a.catalog.resolveEffort(a.model, a.effort)
	}
	return request
}

// errPromptRateLimited states MiMo's limit on its prompt route. See
// classifyDeliveryError.
var errPromptRateLimited = errors.New("MiMo accepts at most 20 messages a minute, steers and subagent messages included; " +
	"send this message again when the minute ends")

// classifyDeliveryError separates a refusal from an unknown outcome.
//
// A status reply is the server's own answer, and a refused dial never sent the
// request, so both are definite failures. Any other transport error can land
// after the server read the request, so the delivery is uncertain, and the
// queue must not send the same text again as if it never arrived.
//
// A 429 is MiMo's rate limit on prompt_async: at most 20 requests in each fixed
// 60-second window, for the whole server (server/rate-limit.ts, and the route
// in server/routes/instance/session.ts). A prompt, a steer and a message to a
// subagent all use that route. The refusal states the limit, and the queue
// records a failure that the user can send again. A retry inside the provider
// is not possible:
//
//   - HTTPStatusError keeps no header, so the Retry-After value is unknown here.
//   - Retry-After runs to the end of MiMo's window, up to a minute. A dispatch
//     or a steer that waits that long holds the agent's input queue.
//
// A busy refusal is wrong too. It waits for a turn end, and an idle agent
// publishes one at once, so the queue would send the message again, into the
// same limit, for the rest of the window.
func classifyDeliveryError(operation string, err error) error {
	var status *providerkit.HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("MiMo refused the %s: %w: %w", operation, errPromptRateLimited, err)
		}
		return fmt.Errorf("MiMo refused the %s: %w", operation, err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return fmt.Errorf("MiMo refused the %s: %w", operation, err)
	}
	return fmt.Errorf("%w: MiMo did not confirm the %s: %w", agent.ErrDeliveryUncertain, operation, err)
}

// PublishTurnActive republishes the turn flag. Every site that writes
// turnActive calls it after the write, with a.Mu released.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.turnActive
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}

// HandleOutput receives one event, for a test that feeds the stream by hand.
// Production reads the stream in connection.go.
func (a *Agent) HandleOutput(content []byte) {
	a.dispatchEvent(content)
}

// SendRawInput executes a control answer that ResolveControlResponse built. It
// refuses every other input, because the server takes no raw frame.
func (a *Agent) SendRawInput(data []byte) error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	return a.executeControlReply(data)
}
