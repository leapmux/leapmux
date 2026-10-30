package opencode

import (
	"context"
	"encoding/json"
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

// FamilyBase is what OpenCode and Kilo share beyond the ACP base.
//
// Both daemons run the same two transports: the Agent Client Protocol stream that
// carries the session, and the daemon's own HTTP server that carries the questions
// the ACP adapter drops. openCodeQuestions states why that second transport exists.
type FamilyBase struct {
	acp.Base
	Questions openCodeQuestions
	// MemberEvents receives native session metadata. Set it before the daemon reader starts.
	MemberEvents func(eventType string, properties json.RawMessage)
	// DaemonReady runs once the address of the daemon's HTTP server is known,
	// so ReadDaemon can reach it. Its context ends with the daemon bridge. It
	// runs before the bridge reads events, so it must not wait. Set it before
	// the start. It may be nil.
	DaemonReady func(ctx context.Context)
	// TurnEnded runs after each prompt ends, also after a failure or a stop. Set
	// it before the start. It may be nil.
	TurnEnded func()
	// MemberOptionGroups returns the option groups that one member keeps
	// beside the family's own, each with its current value. The member applies
	// a change of one of them in its own UpdateSettings. Set it before the
	// start. It may be nil.
	MemberOptionGroups func() []*leapmuxv1.AvailableOptionGroup
	// MemberFailure classifies the error of a failed turn by the vocabulary of
	// one member's own gateway, before the family recipe reads it. It returns
	// false for an error that the family recipe decides. Set it before the
	// start. It may be nil.
	MemberFailure func(failure DaemonError) (providerkit.TurnFailure, bool)

	daemonMu        sync.Mutex
	daemonMessages  map[string]*openCodeMessageState
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
	longWait *openCodeLongWait
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
	childSessions map[string]*openCodeChildSession
}

func (b *FamilyBase) configureQuestions(sink agent.ControlServices) {
	b.Questions.Configure(sink)
	b.Questions.observe = b.handleDaemonEvent
	b.Questions.ready = b.DaemonReady
	b.Questions.connected = b.readMissedSessionStatus
}

// ReadDaemon GETs one route of the daemon's HTTP server, such as a route that
// one member of the family serves alone, and returns its body.
func (b *FamilyBase) ReadDaemon(ctx context.Context, route string) ([]byte, error) {
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
func (b *FamilyBase) SendRawInput(raw []byte, stop agent.StopContext) error {
	if handled, err := b.Questions.answer(b.Context(), raw); handled {
		return err
	}
	if (acp.Provider{}).IsInterrupt(string(raw)) {
		b.noteUserStop()
	}
	return b.Base.SendRawInput(raw, stop)
}

// openCodeCompactCommand is the slash command that the family's ACP adapter
// runs as the daemon's compaction (session.summarize). OpenCode 1.19.4 ignores
// the command's arguments, and runs nothing for "summarize".
const openCodeCompactCommand = "compact"

// SendInput starts one prompt. A /compact command starts the daemon's
// compaction, so the prompt carries the compaction's mark.
func (b *FamilyBase) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !startsCompaction(content, attachments) {
		return b.Base.SendInput(content, attachments)
	}
	return b.sendCompactionPrompt(nil, content, attachments)
}

// SendInputForSession is SendInput for the session that the caller validated.
func (b *FamilyBase) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if !startsCompaction(content, attachments) {
		return b.Base.SendInputForSession(sessionID, content, attachments)
	}
	return b.sendCompactionPrompt(&sessionID, content, attachments)
}

// startsCompaction reports whether a prompt of content and attachments is the
// compaction command. A message with an attachment is a conversation turn.
func startsCompaction(content string, attachments []*leapmuxv1.Attachment) bool {
	name, _, ok := providerkit.SlashCommand(content)
	return ok && name == openCodeCompactCommand && len(attachments) == 0
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
func (b *FamilyBase) sendCompactionPrompt(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	if err := providerkit.CheckInputSession(expected, b.CurrentSessionID()); err != nil {
		return err
	}
	return b.SendPreparedPrompt(content, attachments, b.prepareCompactionPrompt(expected))
}

// prepareCompactionPrompt returns the preparation of a compaction prompt for
// the session expected, or for any session when expected is nil. It receives
// the session of the prompt, and refuses another session than expected.
func (b *FamilyBase) prepareCompactionPrompt(expected *string) func(sessionID string) error {
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
func (b *FamilyBase) Interrupt(stop agent.StopContext) error {
	b.noteUserStop()
	return b.Base.Interrupt(stop)
}

// noteUserStop records that the reader stopped the running turn.
func (b *FamilyBase) noteUserStop() {
	b.daemonMu.Lock()
	b.userStopRequested = true
	b.daemonMu.Unlock()
}

// ClearContext records the user's intent before replacing the session. A failed
// replacement must not retain the automatic retry of an earlier long wait.
func (b *FamilyBase) ClearContext() (string, error) {
	b.noteUserStop()
	return b.Base.ClearContext()
}

// SteerInput sends the steer as a second prompt on the running session.
//
// A refusal reaches the READER, not the log alone. The steer is something the reader
// typed and watched for, and a daemon that declines a concurrent prompt -- which the
// protocol does not oblige it to accept -- otherwise swallowed those words with no
// trace anywhere the reader can see.
//
// A STOPPED agent states nothing: the reader ended the turn themselves, so a steer
// that did not land is the outcome they asked for.
func (b *FamilyBase) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	_, active, sessionID, _ := b.SteerTarget()
	if !active {
		return agent.ErrNoActiveTurn
	}
	return b.SendPromptDetached(sessionID, content, attachments, func(_ json.RawMessage, err error) {
		b.ReportSteerFailure(err)
	})
}

// ReportSteerFailure tells the reader when a detached ACP prompt refused their steer.
// Kilo also reads this result after its own detached turn completes.
func (b *FamilyBase) ReportSteerFailure(err error) {
	if err == nil || b.IsStopped() {
		return
	}
	slog.Error("acp steer failed", "agent_id", b.AgentID(), "provider", b.ProviderName(), "error", err)
	b.Sink().PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
	})
}
