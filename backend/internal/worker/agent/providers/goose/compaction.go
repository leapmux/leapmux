package goose

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// gooseCompactCommand is the prompt that compacts the conversation. Goose runs
// it as a turn of its own, which the queue expects of a compaction.
const gooseCompactCommand = "/compact"

// The two other texts that Goose 1.56.0 runs as its compaction when the
// trimmed message equals one of them (ops_slash_command.rs).
const (
	gooseCompactPhrase  = "Please compact this conversation"
	gooseSummarizeAlias = "/summarize"
)

// gooseCompactionMark marks the prompt that runs Goose's compaction. Its
// address identifies that prompt, so a cleanup for one prompt never clears the
// mark of a later prompt. It is not empty, because two pointers to distinct
// zero-size values can be equal.
type gooseCompactionMark struct {
	// sessionID is the session of the compaction prompt.
	sessionID string
}

// Agent compacts through Goose's own /compact turn.
var _ agent.ContextCompactor = (*Agent)(nil)

// CompactContext runs Goose's `/compact` turn (sendCompactPrompt).
func (a *Agent) CompactContext() error {
	return a.sendCompactPrompt(nil, gooseCompactCommand)
}

// SendInput starts one prompt. Goose runs a typed compaction trigger as the
// turn of CompactContext, so such a prompt starts the same compaction turn.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !gooseStartsCompaction(content, attachments) {
		return a.Base.SendInput(content, attachments)
	}
	return a.sendCompactPrompt(nil, content)
}

// SendInputForSession is SendInput for the session that the caller validated.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if !gooseStartsCompaction(content, attachments) {
		return a.Base.SendInputForSession(sessionID, content, attachments)
	}
	return a.sendCompactPrompt(&sessionID, content)
}

// gooseStartsCompaction reports whether Goose runs a prompt of content and
// attachments as its compaction. Goose 1.56.0 reads the trimmed text
// (ops_slash_command.rs and ops_compaction.rs): /compact and its two other
// triggers, and /compact with arguments, which it ignores. Goose ends the
// command name at the first literal space, so a tab or a newline after
// /compact makes another command. A message with an attachment is a
// conversation turn.
func gooseStartsCompaction(content string, attachments []*leapmuxv1.Attachment) bool {
	if len(attachments) > 0 {
		return false
	}
	trimmed := strings.TrimSpace(content)
	switch trimmed {
	case gooseCompactCommand, gooseCompactPhrase, gooseSummarizeAlias:
		return true
	}
	name, _, ok := providerkit.SlashCommand(trimmed)
	return ok && name == strings.TrimPrefix(gooseCompactCommand, "/") && strings.HasPrefix(trimmed, gooseCompactCommand+" ")
}

// sendCompactPrompt sends content as a prompt that runs Goose's compaction
// turn. expected is the session that the input was written for, or nil for the
// current session. Goose states no compaction frame of its own, so the worker
// publishes the live compaction while the turn runs, and the turn's end stores
// the boundary row the transcript keeps (decision 21: live start, one stored
// end).
//
// The base offers the prompt's preparation for the current session only, so
// the preparation checks expected against the prompt's session itself. The
// check before the send refuses another session before the base claims the
// turn, as the base's own check does.
func (a *Agent) sendCompactPrompt(expected *string, content string) error {
	if err := providerkit.CheckInputSession(expected, a.CurrentSessionID()); err != nil {
		return err
	}
	var mark *gooseCompactionMark
	prepare := a.prepareCompactPrompt(expected)
	err := a.SendPreparedPrompt(content, nil, func(sessionID string) error {
		var err error
		mark, err = prepare(sessionID)
		return err
	})
	if err == nil || mark == nil {
		return err
	}
	// The send failed, so no turn will end the compaction this request began.
	// A later prompt's start already ended it when the mark is no longer this
	// prompt's (prepareGoosePrompt).
	a.Mu.Lock()
	own := a.compaction == mark
	if own {
		a.compaction = nil
	}
	a.Mu.Unlock()
	if own {
		providerkit.EndCompaction(a.Sink())
	}
	return err
}

// prepareCompactPrompt returns the preparation of a compaction prompt for the
// session expected, or for any session when expected is nil. It receives the
// session of the prompt and refuses another session than expected. Otherwise
// it marks the prompt, publishes the live compaction, and returns the mark.
func (a *Agent) prepareCompactPrompt(expected *string) func(sessionID string) (*gooseCompactionMark, error) {
	return func(sessionID string) (*gooseCompactionMark, error) {
		if err := providerkit.CheckInputSession(expected, sessionID); err != nil {
			return nil, err
		}
		mark := &gooseCompactionMark{sessionID: sessionID}
		a.Mu.Lock()
		a.compaction = mark
		a.Mu.Unlock()
		providerkit.PublishCompaction(a.Sink(), "manual")
		return mark, nil
	}
}

// prepareGoosePrompt forgets what the prompt before left (Hooks.PromptPrepared):
// its last agent message, and the mark of a compaction prompt whose end never
// ran, as after a session change. That compaction's live state ends here, so
// the mark reaches no later prompt.
func (a *Agent) prepareGoosePrompt() {
	a.clearGooseLastMessage()
	a.Mu.Lock()
	stale := a.compaction != nil
	a.compaction = nil
	a.Mu.Unlock()
	if stale {
		providerkit.EndCompaction(a.Sink())
	}
}

// finishGoosePrompt ends what the turn held open: the goal-end rule, the
// turn-failure verdict, and the compaction whose end Goose never frames.
//
// A compaction turn ends on CompactionEnd, whatever its outcome. A failed
// compaction leaves the task where the last turn left it, so no retry may
// resume it, whatever the classifier reads in the failure.
func (a *Agent) finishGoosePrompt(err error, stopped bool) acp.PromptEnding {
	// Both consumers read one snapshot, taken and cleared under one lock.
	last := a.takeGooseLastMessage()
	a.finishGooseGoalPrompt(err, stopped, last.text)

	a.Mu.Lock()
	compact := a.compaction != nil
	a.compaction = nil
	a.Mu.Unlock()
	if !compact {
		verdict := gooseTurnFailure(err, stopped, last, a.Clock().Now())
		return acp.PromptEnding{Retry: &verdict}
	}
	if err == nil && !stopped {
		providerkit.PersistCompactionEnded(a.Sink(), providerkit.CompactionEndCompleted, "")
	}
	providerkit.EndCompaction(a.Sink())
	return acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: providerkit.CompactionEnd()}}
}
