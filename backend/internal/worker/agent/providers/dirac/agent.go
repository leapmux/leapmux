package dirac

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// diracSteerMethod is the extension notification that carries steering text
// into a running turn. Dirac advertises it as `_meta["dev.dirac/whisper"]` on
// the initialize response, and answers with `dev.dirac/steering_status`.
const diracSteerMethod = "dev.dirac/whisper"

// Agent manages one Dirac ACP process.
type Agent struct {
	acp.Base
	root       string
	clock      quartz.Clock
	childMu    sync.Mutex
	childState map[string]*diracChildState
	childDone  map[string]struct{}
	childStop  chan struct{}
	archiveWG  sync.WaitGroup
	// claimedMu guards claimedCards, the tool calls whose cards the notice
	// observer consumed (see observeDiracNotice).
	claimedMu    sync.Mutex
	claimedCards map[string]struct{}
	// retryMu guards the cards of a failed model request that the
	// turn-failure verdict reads (see observeDiracFailure): the failed-request
	// card of the running prompt, and the sibling card that no failed-request
	// card has taken yet. It also guards compactionPrompt.
	retryMu        sync.Mutex
	failedRequest  *diracFailedRequest
	pendingSibling diracSiblingCard
	// compactionPrompt states that the running prompt runs Dirac's condense
	// action (sendCompactionPrompt). The prompt's end takes it, and the start
	// of the next prompt clears it.
	compactionPrompt bool
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers through `dev.dirac/whisper`. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying
// InputSteerer, so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// SteerInput sends text into the running turn as a `dev.dirac/whisper`
// notification. Dirac queues it and answers `dev.dirac/steering_status` with
// `queued` or `sent`. A steer that reaches an idle session is refused, because
// the whisper carries no turn to land in and Dirac buffers only the whispers
// that arrive while a session has a pending prompt.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if content == "" {
		return nil
	}
	// The whisper carries text alone, so the queue keeps an attachment-bearing
	// input for the next turn.
	if err := providerkit.TextOnlySteer("Dirac", attachments); err != nil {
		return err
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	err := a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"text":      content,
		})
		if err != nil {
			return fmt.Errorf("marshal the Dirac whisper: %w", err)
		}
		return a.SendNotification(diracSteerMethod, params)
	})
	// A write failure is a delivery failure, not a missing turn: the turn ran
	// while the whisper left.
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError(diracSteerMethod, err)
	}
	// The turn can end while the whisper writes; a whisper into an ended turn
	// reaches nothing.
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return nil
}

// The commands that Dirac 0.5.23 runs as its condense action, with or without
// arguments (BuiltinCommandExecutor.ts). Dirac then goes on to a model request
// in the same prompt, with the arguments as its text.
const (
	diracCompactCommand = "compact"
	diracSmolCommand    = "smol"
)

// SendInput starts one prompt. A /smol or /compact command runs Dirac's
// condense action, so the prompt carries the compaction's mark.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !diracStartsCompaction(content, attachments) {
		return a.Base.SendInput(content, attachments)
	}
	return a.sendCompactionPrompt(nil, content)
}

// SendInputForSession is SendInput for the session that the caller validated.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if !diracStartsCompaction(content, attachments) {
		return a.Base.SendInputForSession(sessionID, content, attachments)
	}
	return a.sendCompactionPrompt(&sessionID, content)
}

// diracStartsCompaction reports whether a prompt of content and attachments
// opens with a command that Dirac runs as its condense action. A message with
// an attachment is a conversation turn.
func diracStartsCompaction(content string, attachments []*leapmuxv1.Attachment) bool {
	name, _, ok := providerkit.SlashCommand(content)
	return ok && len(attachments) == 0 && (name == diracCompactCommand || name == diracSmolCommand)
}

// sendCompactionPrompt sends content as a prompt that carries the compaction's
// mark. expected is the session that the input was written for, or nil for the
// current session. The base offers the prompt's preparation for the current
// session only, so the preparation checks expected against the prompt's
// session itself. The check before the send refuses another session before the
// base claims the turn, as the base's own check does. A failed native write
// leaves the mark on the prompt that never ran, and the start of the next
// prompt clears it.
func (a *Agent) sendCompactionPrompt(expected *string, content string) error {
	if err := providerkit.CheckInputSession(expected, a.CurrentSessionID()); err != nil {
		return err
	}
	return a.SendPreparedPrompt(content, nil, a.prepareCompactionPrompt(expected))
}

// prepareCompactionPrompt returns the preparation of a compaction prompt for
// the session expected, or for any session when expected is nil. It receives
// the session of the prompt, refuses another session than expected, and
// otherwise marks the prompt as a compaction.
func (a *Agent) prepareCompactionPrompt(expected *string) func(sessionID string) error {
	return func(sessionID string) error {
		if err := providerkit.CheckInputSession(expected, sessionID); err != nil {
			return err
		}
		a.retryMu.Lock()
		a.compactionPrompt = true
		a.retryMu.Unlock()
		return nil
	}
}
