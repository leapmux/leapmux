package kilo

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.SteerPreparer = (*Agent)(nil)

// Agent owns Kilo protocol state and its ACP connection.
type Agent struct {
	acp.Base
	steers       kiloSteerState
	usageRefresh chan struct{}
	// Guarded by Base.Mu.
	swarm           bool
	swarmConfigOff  bool
	Questions       kiloQuestions
	daemonMu        sync.Mutex
	daemonMessages  map[string]*kiloMessageState
	daemonParts     map[string]string
	lastAssistant   string
	retryResolution providerkit.RetryResolution
	// promptStartedAt, heldSessionError, longWait, userStopRequested, and
	// compactionPrompt belong to one prompt: resetDaemonGeneration clears them
	// when the next prompt starts. A Kilo steer joins the running prompt, so it
	// keeps them. Guarded by daemonMu.
	//
	// promptStartedAt is the agent clock's instant of the prompt's start.
	promptStartedAt time.Time
	// heldSessionError is the newest session.error of the current session.
	heldSessionError *heldSessionError
	// longWait is the retry status that made LeapMux end the turn.
	longWait *kiloLongWait
	// userStopRequested records a stop that the reader asked for.
	userStopRequested bool
	// compactionPrompt states that the prompt is the /compact command, which
	// the daemon runs as its compaction (sendCompactionPrompt).
	compactionPrompt bool
	// autoApprove is the current value of the Auto-approve option. Guarded by
	// the base's settings lock, Base.Mu, which an automatic approval also holds.
	autoApprove bool
	childStopMu sync.Mutex
	// stoppedChildren holds the task calls whose subagent the user stopped, until
	// the close of that subagent's row reads the stop. Guarded by childStopMu.
	stoppedChildren map[string]string
	childSessionsMu sync.Mutex
	// childSessions holds the session of each running subagent, by the task
	// call that started it. Guarded by childSessionsMu.
	childSessions map[string]*kiloChildSession
}

func (b *Agent) configureQuestions(sink agent.ControlServices) {
	b.Questions.Configure(sink)
	b.Questions.observe = b.handleDaemonEvent
	b.Questions.ready = func(ctx context.Context) {
		b.requestUsageRefresh()
		go b.readSwarmConfig(ctx)
	}
	b.Questions.connected = b.readMissedSessionStatus
}

// ReadDaemon returns the response body of a native HTTP route.
func (b *Agent) ReadDaemon(ctx context.Context, route string) ([]byte, error) {
	raw, _, err := b.Questions.readRoute(ctx, route)
	return raw, err
}

// SendRawInput answers a question through the daemon's HTTP server, and sends every
// other control answer to the ACP stream unchanged.
//
// The two answers cannot share one path. An ACP control request is a JSON-RPC request
// that LeapMux answers by id on the stream it arrived on; a question never reached
// that stream, so it has no id there to answer and the daemon reads its answer from
// a route instead.
//
// A raw session/cancel is the reader's stop, as Interrupt is: the worker
// service pauses the queue for it, so it records the stop for the verdict too.
func (b *Agent) SendRawInput(raw []byte, stop agent.StopContext) error {
	if handled, err := b.Questions.answer(b.Context(), raw); handled {
		return err
	}
	if (acp.Provider{}).IsInterrupt(string(raw)) {
		b.noteUserStop()
	}
	return b.Base.SendRawInput(raw, stop)
}

// kiloCompactCommand is the slash command that Kilo's ACP adapter
// runs as the daemon's compaction (session.summarize). Kilo 7.8.18 ignores
// the command's arguments, and runs nothing for "summarize".
const kiloCompactCommand = "compact"

// SendInput starts one prompt. A /compact command starts the daemon's
// compaction, so the prompt carries the compaction's mark.
func (b *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !startsCompaction(content, attachments) {
		return b.Base.SendInput(content, attachments)
	}
	return b.sendCompactionPrompt(nil, content, attachments)
}

// SendInputForSession is SendInput for the session that the caller validated.
func (b *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if !startsCompaction(content, attachments) {
		return b.Base.SendInputForSession(sessionID, content, attachments)
	}
	return b.sendCompactionPrompt(&sessionID, content, attachments)
}

// startsCompaction reports whether a prompt of content and attachments is the
// compaction command. A message with an attachment is a conversation turn.
func startsCompaction(content string, attachments []*leapmuxv1.Attachment) bool {
	name, _, ok := providerkit.SlashCommand(content)
	return ok && name == kiloCompactCommand && len(attachments) == 0
}

// sendCompactionPrompt sends the compaction command as a prompt that carries
// the compaction's mark. expected is the session that the input was written
// for, or nil for the current session.
//
// The prompt's own preparation sets the mark. It runs after the base cleared
// the state of the prompt before (resetDaemonGeneration), and before the
// native write. So no other prompt can take the mark, and no daemon event of
// the compaction arrives before it. The base offers that preparation for the
// current session only, so the preparation checks expected against the
// prompt's session itself. The check before the send refuses another session
// before the base claims the turn, as the base's own check does. A failed
// native write leaves the mark on the prompt that never ran, and the start of
// the next prompt clears it.
func (b *Agent) sendCompactionPrompt(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	if err := providerkit.CheckInputSession(expected, b.CurrentSessionID()); err != nil {
		return err
	}
	return b.SendPreparedPrompt(content, attachments, b.prepareCompactionPrompt(expected))
}

// prepareCompactionPrompt returns the preparation of a compaction prompt for
// the session expected, or for any session when expected is nil. It receives
// the session of the prompt, and refuses another session than expected.
func (b *Agent) prepareCompactionPrompt(expected *string) func(sessionID string) error {
	return func(sessionID string) error {
		if err := providerkit.CheckInputSession(expected, sessionID); err != nil {
			return err
		}
		b.daemonMu.Lock()
		b.compactionPrompt = true
		b.daemonMu.Unlock()
		return nil
	}
}

// Interrupt stops the running turn for the reader. It records the stop first,
// so the turn's verdict schedules no retry, also after a long wait
// (TurnEndVerdict). LeapMux's own stop for a long wait calls Base.Interrupt
// and records nothing.
func (b *Agent) Interrupt(stop agent.StopContext) error {
	b.noteUserStop()
	return b.Base.Interrupt(stop)
}

// noteUserStop records that the reader stopped the running turn.
func (b *Agent) noteUserStop() {
	b.daemonMu.Lock()
	b.userStopRequested = true
	b.daemonMu.Unlock()
}

// ClearContext records the user's intent before replacing the session. A failed
// replacement must not retain the automatic retry of an earlier long wait.
func (b *Agent) ClearContext() (string, error) {
	b.noteUserStop()
	return b.Base.ClearContext()
}

// ReportSteerFailure tells the reader when a detached ACP prompt refused their steer.
// Kilo also reads this result after its own detached turn completes.
func (b *Agent) ReportSteerFailure(err error) {
	if err == nil || b.IsStopped() {
		return
	}
	slog.Error("acp steer failed", "agent_id", b.AgentID(), "provider", b.ProviderName(), "error", err)
	b.Sink().PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
	})
}

func newAgent(swarm bool) *Agent { return &Agent{usageRefresh: make(chan struct{}, 1), swarm: swarm} }

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
