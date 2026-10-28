package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

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

	daemonMu        sync.Mutex
	daemonMessages  map[string]*openCodeMessageState
	daemonParts     map[string]string
	lastAssistant   string
	retryResolution providerkit.RetryResolution
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
func (b *FamilyBase) SendRawInput(raw []byte, stop agent.StopContext) error {
	if handled, err := b.Questions.answer(b.Context(), raw); handled {
		return err
	}
	return b.Base.SendRawInput(raw, stop)
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
