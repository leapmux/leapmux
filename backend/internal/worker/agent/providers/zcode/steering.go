package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode steering, through the session mailbox.
//
// The app-server protocol itself offers no steering input: `session/send` is
// strict-schema and answers -32010 while a prompt runs (see steering_test.go's
// verification note). The runtime inside the same bundle DOES steer
// (runtime.steerTurn, a pending-input queue drained at the next tool boundary),
// and one path reaches it from outside the process: with
// ZCODE_MESSAGE_ENABLED=1 the runtime registers a builtin hook,
// builtin.sessionMailbox.drain, on UserPromptSubmit, PostToolUse and Stop. A
// drain reads `$ZCODE_MAILBOX_ROOT/<sessionId>/unread/*.json` -- a strict
// envelope {version:1, messageId, fromSessionId, toSessionId, content,
// createdAt}, at most 20 files per drain, renamed unread -> read as it goes --
// and the PostToolUse drain feeds each message to steerTurn(delivery:"guide"),
// which injects it into the RUNNING turn at the tool boundary. The Stop drain
// answers `continue: true`, so a message that arrives while the model streams
// its final answer continues the turn with the message instead of losing it.
// Probed live against ZCode 3.16.2 (CLI 0.17.0): turn.steerQueued, then
// turn.steerDrained with injectedMessageIds, and every follow-up model request
// of the turn carried the message.
//
// The runtime wraps the content as a message from ANOTHER session
// (`<session-message source="mailbox" ...>` with a "For reference only" line),
// so the envelope's content states the user-steer framing itself -- the wording
// mirrors the runtime's own user_steer reminder.
//
// Everything here is best-effort against a filesystem the runtime also touches:
// the envelope is written under a temporary name (the drain only reads names
// ending in .json) and renamed into place, so the runtime never reads a partial
// file, and an envelope still unread at a turn end is withdrawn before the next
// prompt could fold it in as reference material.

// Agent steers, through the mailbox. Manager.SupportsSteering answers false,
// with no build error, for a provider that stops satisfying InputSteerer, so
// this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerEchoReporter = (*Agent)(nil)

// zcodeMailboxSteer is the bookkeeping of one envelope LeapMux mailed.
type zcodeMailboxSteer struct {
	// file is the envelope's name under the session's unread directory.
	file string
	// ticket identifies the steer in the ledger, whose resolution reports the
	// delivery to the queue.
	ticket providerkit.SteerTicket
	// pendingInputID is the runtime's id for the queued input, learned from
	// turn.steerQueued and the one key a discard event addresses.
	pendingInputID string
}

const (
	// zcodeMailboxEnabledEnv and zcodeMailboxRootEnv are the launch variables
	// that turn the runtime's mailbox drain on and point it at a root LeapMux
	// owns. Set at Start, after FinalizeAgentEnv, so they win over an inherited
	// or ExtraEnv value: the writer below and the runtime must agree on one
	// root, and a user-supplied value would desynchronize the two.
	zcodeMailboxEnabledEnv = "ZCODE_MESSAGE_ENABLED"
	zcodeMailboxRootEnv    = "ZCODE_MAILBOX_ROOT"

	// zcodeMailboxDrainHookSource names the builtin hook in its session.updated
	// hook-run events. A successful run of it is the live proof that this
	// app-server build drains the mailbox, which is what SupportsSteering
	// reports -- a build without the feature ignores the env silently.
	zcodeMailboxDrainHookSource = "builtin.sessionMailbox.drain"

	// zcodeHookRunOutcomeSuccess is the one hook-run outcome word the shared
	// contract does not generate (it carries only the words that produce a
	// row). Stated here for the discovery read above.
	zcodeHookRunOutcomeSuccess = "success"

	// zcodeMailboxMessageIDPrefix marks the envelopes LeapMux itself wrote, in
	// the message_id attribute of the wrapped input the steer events carry.
	zcodeMailboxMessageIDPrefix = "leapmux-"

	// zcodeSteerInputMaxBytes is the runtime's own steer admission bound
	// (`input_too_large` above 2e5 bytes, read from the bundle). It judges the
	// WRAPPED input, so the check below builds the wrapper the runtime will
	// build rather than measuring the content alone.
	zcodeSteerInputMaxBytes = 200000

	// zcodeMailboxEnvelopeVersion is the one envelope version the drain parses.
	zcodeMailboxEnvelopeVersion = 1
)

// zcodeMailboxSessionIDRE is the session-id shape the drain's directory
// resolver accepts. A session id outside it makes EVERY drain throw inside the
// runtime -- a failing hook on every prompt -- so SteerInput refuses rather
// than write an envelope no drain can ever take.
var zcodeMailboxSessionIDRE = regexp.MustCompile(`^sess_[A-Za-z0-9._-]+$`)

// zcodeMailboxEnvelope is the drain's file shape. Every field is a string
// except the version, and the version must be the NUMBER 1.
type zcodeMailboxEnvelope struct {
	Version       int    `json:"version"`
	MessageID     string `json:"messageId"`
	FromSessionID string `json:"fromSessionId"`
	ToSessionID   string `json:"toSessionId"`
	Content       string `json:"content"`
	CreatedAt     string `json:"createdAt"`
}

// zcodeMailboxSteerContent frames the user's text the way the runtime frames
// its own user steers, because the wrapper the drain adds presents the content
// as reference material from another session. The words are the runtime's own
// user_steer reminder's words, so the model reads a steer, not a citation.
func zcodeMailboxSteerContent(content string) string {
	return strings.Join([]string{
		"This session-message is a steering message from the user of this session, relayed by LeapMux.",
		"It is a direct user instruction, not reference material: the \"For reference only\" line above does not apply to it.",
		"",
		"The user sent a new message while you were working:",
		"",
		content,
		"",
		"Address the message above as you continue this turn.",
	}, "\n")
}

// zcodeMailboxWrappedInput renders exactly what the runtime's drain builds from
// an envelope, escapes included. The size admission and the event correlation
// both read this shape, so it stays byte-faithful to the bundle's formatter
// rather than approximately like it.
func zcodeMailboxWrappedInput(envelope zcodeMailboxEnvelope) string {
	return strings.Join([]string{
		`<session-message source="mailbox" message_id="` + zcodeEscapeMailboxAttr(envelope.MessageID) +
			`" from_session="` + zcodeEscapeMailboxAttr(envelope.FromSessionID) +
			`" created_at="` + zcodeEscapeMailboxAttr(envelope.CreatedAt) + `">`,
		"For reference only. Verify against raw source before acting.",
		"",
		envelope.Content,
		"</session-message>",
	}, "\n")
}

// zcodeEscapeMailboxAttr is the runtime's attribute escape. LeapMux mints ids
// and timestamps that never need it; keeping the escape makes the wrapped
// rendering faithful for any content that someday does.
func zcodeEscapeMailboxAttr(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;")
	return replacer.Replace(value)
}

// SupportsSteering reports whether the app-server drain proved itself live.
//
// The capability is discovered, not assumed: a build without the mailbox
// feature ignores ZCODE_MESSAGE_ENABLED without a word, and steering on such a
// build would write envelopes nothing drains -- the queue would show the steer
// as delivered and the model would never see it. The proof is the first
// successful builtin.sessionMailbox.drain hook run, which every user prompt
// produces (the hook runs and reports success on an empty mailbox too), so the
// answer is true from the first prompt of the session onward.
func (a *Agent) SupportsSteering() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.mailboxActive
}

// SteerRowAtEcho keeps the queue's row until the runtime proves delivery.
//
// The app-server never repeats a steered input into its own stream, so the
// queue's own row would be the only record even if delivery failed. Waiting
// for the proof means the row lands when the model actually reads the steer:
// turn.steerDrained (or a promoted input's own turn) resolves the ledger, and
// an interrupt before either returns the input to the queue for the next turn.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerInput writes the input as a mailbox envelope for the running session.
//
// The mailbox carries TEXT only: an envelope's content is one string, and the
// drain injects it as one text message. Text attachments inline into the
// content the way a normal send inlines them; anything else is refused as
// unsupported, and the worker sends that input as the NEXT turn, where
// session/send's attachment wire carries it in full. The refusal depends on the
// input alone, so it comes before the turn check.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	var blocks []string
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != agent.AttachmentKindText {
			return fmt.Errorf("%w: a ZCode steer carries text only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
		blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
	}

	// steerMu serializes envelope writes against the turn-end withdrawal, so an
	// envelope can never be written after the sweep that should have withdrawn it.
	a.steerMu.Lock()
	defer a.steerMu.Unlock()

	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if !a.mailboxActive || a.mailboxRoot == "" {
		a.Mu.Unlock()
		return fmt.Errorf("%w: the app-server has not shown a mailbox drain", agent.ErrSteeringUnsupported)
	}
	if !a.turnActive {
		// A compaction alone is not a turn to steer: its summary query is not
		// the user's conversation, and the queued input takes the next turn.
		a.Mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	sessionID := a.sessionID
	a.nextMailboxSteer++
	sequence := a.nextMailboxSteer
	a.Mu.Unlock()
	if !zcodeMailboxSessionIDRE.MatchString(sessionID) {
		return fmt.Errorf("%w: session %q cannot receive a mailbox steer", agent.ErrSteeringUnsupported, sessionID)
	}

	now := a.Clock().Now()
	envelope := zcodeMailboxEnvelope{
		Version:       zcodeMailboxEnvelopeVersion,
		MessageID:     fmt.Sprintf("%s%d-%06d", zcodeMailboxMessageIDPrefix, now.UnixMilli(), sequence),
		FromSessionID: "sess_leapmux_" + a.AgentID(),
		ToSessionID:   sessionID,
		Content:       zcodeMailboxSteerContent(zcodeJoinPrompt(content, blocks)),
		CreatedAt:     now.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if wrapped := zcodeMailboxWrappedInput(envelope); len(wrapped) > zcodeSteerInputMaxBytes {
		return fmt.Errorf("the steered input wraps to %d bytes; the runtime steers at most %d",
			len(wrapped), zcodeSteerInputMaxBytes)
	}

	ticket := a.steerLedger.Add(envelope.MessageID)
	name, err := a.writeZCodeMailboxEnvelope(sessionID, envelope, now.UnixMilli(), sequence)
	if err != nil {
		a.steerLedger.Forget(ticket)
		a.steerLedger.Settle(a.sink)
		return fmt.Errorf("write the ZCode steer envelope: %w", err)
	}

	// The turn (or the session) can end between the check and the write; the
	// drain cannot have taken the envelope in that window, because a drain that
	// takes it continues the turn. Withdraw it and report the turn over, so the
	// queue requeues the input for the next turn instead of trusting a delivery
	// that will not happen.
	a.Mu.Lock()
	delivered := a.turnActive && a.sessionID == sessionID
	if delivered {
		if a.mailboxSteers == nil {
			a.mailboxSteers = map[string]*zcodeMailboxSteer{}
		}
		a.mailboxSteers[envelope.MessageID] = &zcodeMailboxSteer{file: name, ticket: ticket}
	}
	a.Mu.Unlock()
	if !delivered {
		_ = os.Remove(filepath.Join(a.mailboxRoot, sessionID, zcodeMailboxUnreadDir, name))
		a.steerLedger.Forget(ticket)
		a.steerLedger.Settle(a.sink)
		return agent.ErrNoActiveTurn
	}
	return nil
}

const (
	// zcodeMailboxUnreadDir and zcodeMailboxReadDir are the drain's two
	// directories inside `<root>/<sessionId>/`.
	zcodeMailboxUnreadDir = "unread"
	zcodeMailboxReadDir   = "read"
)

// writeZCodeMailboxEnvelope places one envelope where the next drain reads it.
//
// The name sorts by write order (the drain reads the names in sort order), and
// the write is atomic: the file appears under its `.json` name only complete,
// because the drain reads whatever `.json` files it finds, and a partial read
// makes the whole hook fail on every prompt.
func (a *Agent) writeZCodeMailboxEnvelope(sessionID string, envelope zcodeMailboxEnvelope, unixMilli, sequence int64) (string, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%013d-%06d.json", unixMilli, sequence)
	unread := filepath.Join(a.mailboxRoot, sessionID, zcodeMailboxUnreadDir)
	if err := os.MkdirAll(unread, 0o700); err != nil {
		return "", err
	}
	temporary := filepath.Join(unread, name+".tmp")
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, filepath.Join(unread, name)); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	return name, nil
}

// zcodeMailboxMessageID extracts the message_id attribute from one wrapped
// mailbox input, the attribute the runtime escapes into the text both sides
// read. ok is false when the input carries no such attribute, which is every
// steer the mailbox did not carry (a task notification, a coordinator message).
func zcodeMailboxMessageID(wrapped string) (string, bool) {
	const marker = `message_id="`
	at := strings.Index(wrapped, marker)
	if at < 0 {
		return "", false
	}
	rest := wrapped[at+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// observeZCodeMailboxSteerEvent settles the mailbox's own steer events.
//
// turn.steerQueued records the runtime's pending-input id, which a discard
// event addresses. turn.steerDrained states the model read the steer, which
// resolves the ledger entry as echoed: the queue writes the row and consumes
// the input at exactly that point, so the row lands after the output that
// preceded the drain and before the reply that follows it.
//
// A steer event whose input carries no tracked message id is the runtime's own
// (a background task notification or a subagent message steered into the
// turn), and it keeps its notification row.
func (a *Agent) observeZCodeMailboxSteerEvent(event zcodeEventEnvelope) bool {
	type steerObservation struct {
		messageID    string
		pendingInput string
		drained      bool
	}
	var observations []steerObservation
	switch event.Type {
	case contracts.ZCodeEventTurnSteerQueued:
		var payload struct {
			PendingInputID string `json:"pendingInputId"`
			Input          string `json:"input"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Input != "" {
			if id, ok := zcodeMailboxMessageID(payload.Input); ok {
				observations = append(observations, steerObservation{messageID: id, pendingInput: payload.PendingInputID})
			}
		}
	case contracts.ZCodeEventTurnSteerDrained:
		var payload struct {
			DrainedInputs []struct {
				PendingInputID string `json:"pendingInputId"`
				Text           string `json:"text"`
			} `json:"drainedInputs"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil {
			for _, drained := range payload.DrainedInputs {
				if id, ok := zcodeMailboxMessageID(drained.Text); ok {
					observations = append(observations, steerObservation{
						messageID: id, pendingInput: drained.PendingInputID, drained: true,
					})
				}
			}
		}
	default:
		return false
	}

	ours := false
	a.Mu.Lock()
	for _, observation := range observations {
		entry, tracked := a.mailboxSteers[observation.messageID]
		if !tracked {
			continue
		}
		ours = true
		if observation.pendingInput != "" {
			entry.pendingInputID = observation.pendingInput
		}
		if observation.drained {
			ticket := entry.ticket
			delete(a.mailboxSteers, observation.messageID)
			a.steerLedger.Echo(ticket)
		}
	}
	a.Mu.Unlock()
	if ours {
		a.steerLedger.Settle(a.sink)
	}
	return ours
}

// observeZCodeSteerDiscard settles a queued mailbox steer the runtime dropped.
//
// `promoted` is not a loss: the runtime moved the input into a turn of its own,
// so the model reads it there and the steer counts as echoed. Any other reason
// removed the input before the model read it, so the steer counts as dropped
// and the queue requeues it for the next turn.
//
// The answer tells the session.updated handler whether the discard is one of
// LeapMux's own steers, whose requeue is the visible outcome and needs no
// notification row of its own.
func (a *Agent) observeZCodeSteerDiscard(payload zcodeSessionUpdatedPayload) bool {
	var pendingIDs []string
	if json.Unmarshal(payload.PendingInputIDs, &pendingIDs) != nil || len(pendingIDs) == 0 {
		return false
	}
	dropped := make(map[string]bool, len(pendingIDs))
	for _, pending := range pendingIDs {
		dropped[pending] = true
	}
	reason, _ := providerkit.RawString(payload.Reason)

	ours := false
	a.Mu.Lock()
	for messageID, entry := range a.mailboxSteers {
		if entry.pendingInputID == "" || !dropped[entry.pendingInputID] {
			continue
		}
		ours = true
		ticket := entry.ticket
		delete(a.mailboxSteers, messageID)
		if reason == zcodeSteerDiscardReasonPromoted {
			a.steerLedger.Echo(ticket)
		} else {
			a.steerLedger.Drop(ticket)
		}
	}
	a.Mu.Unlock()
	if ours {
		a.steerLedger.Settle(a.sink)
	}
	return ours
}

// noteZCodeMailboxDrainRun records the proof that this app-server drains the
// mailbox: one successful builtin.sessionMailbox.drain hook run.
//
// The first one re-publishes twice, because two readers must both see the flip:
// the queue's steerable flag follows the turn state, and the browser's Steer
// control follows the agent status change, whose SupportsSteering was sampled
// at startup -- before any prompt could prove the drain.
//
// A SUCCESSFUL STOP drain is also the one delivery proof the Stop path gives:
// it does not call steerTurn, so no steer event follows. It takes the envelopes
// (unread -> read), answers continue:true, and the runtime injects them into
// the model request that continues the turn -- so a tracked steer whose
// envelope just left unread was read by the model there.
func (a *Agent) noteZCodeMailboxDrainRun(payload zcodeSessionUpdatedPayload) {
	source, _ := providerkit.RawString(payload.HookSource)
	outcome, _ := providerkit.RawString(payload.Outcome)
	eventName, _ := providerkit.RawString(payload.HookEventName)
	if source != zcodeMailboxDrainHookSource || outcome != zcodeHookRunOutcomeSuccess {
		return
	}
	if eventName == zcodeStopHookEvent {
		a.settleZCodeSteersTakenByStopDrain()
	}
	a.Mu.Lock()
	known := a.mailboxActive
	a.mailboxActive = true
	sessionID := a.sessionID
	a.Mu.Unlock()
	if known {
		return
	}
	a.PublishTurnActive()
	if sessionID != "" {
		a.sink.BroadcastStatusActive(sessionID)
	}
}

// zcodeStopHookEvent is the mailbox drain's Stop trigger.
const zcodeStopHookEvent = "Stop"

// settleZCodeSteersTakenByStopDrain resolves the steers the Stop drain took.
//
// An envelope absent from unread with no steer event is one of two things: the
// Stop drain injected it into the turn's continuation (the common case -- a
// steer that arrives while the model streams its final answer), or a drain the
// runtime refused to steer and only logged. Resolving as echoed is right for
// the first and wrong for the second; the first is a first-class steering
// scenario and the second needs the runtime to call a turn unsteerable at its
// own tool boundary, so the common case wins and the rare one is accepted.
func (a *Agent) settleZCodeSteersTakenByStopDrain() {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.Mu.Lock()
	entries := make(map[string]*zcodeMailboxSteer, len(a.mailboxSteers))
	for id, entry := range a.mailboxSteers {
		entries[id] = entry
	}
	sessionID := a.sessionID
	a.Mu.Unlock()
	if len(entries) == 0 {
		return
	}
	taken := false
	for id, entry := range entries {
		if _, err := os.Stat(filepath.Join(a.mailboxRoot, sessionID, zcodeMailboxUnreadDir, entry.file)); err == nil {
			continue
		}
		a.steerLedger.Echo(entry.ticket)
		a.Mu.Lock()
		delete(a.mailboxSteers, id)
		a.Mu.Unlock()
		taken = true
	}
	if taken {
		a.steerLedger.Settle(a.sink)
	}
}

// withdrawUndeliveredZCodeSteers settles every steer the turn leaves open, and
// drops the envelopes no drain took, together with the read copies of the ones
// it did.
//
// A turn end is the last moment an envelope is still deliverable: the next
// drain that could read it is the next prompt's UserPromptSubmit, which folds
// it into that prompt as reference material from another session -- a duplicate
// of the input the queue is about to requeue as that same next turn. The sweep
// takes steerMu so it cannot race a SteerInput write; PublishTurnActive calls
// it only when a steer is actually open, so the common turn end pays nothing.
//
// An envelope still in unread never reached the model: dropped, and the queue
// requeues the input. An envelope that LEFT unread without a resolution is the
// residue the settled paths miss (a drain the runtime only logged, a refused
// stop continuation): uncertain, so the queue neither replays it
// automatically nor claims delivery.
func (a *Agent) withdrawUndeliveredZCodeSteers() {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.Mu.Lock()
	entries := a.mailboxSteers
	sessionID := a.sessionID
	a.mailboxSteers = map[string]*zcodeMailboxSteer{}
	a.Mu.Unlock()
	if len(entries) == 0 {
		return
	}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(a.mailboxRoot, sessionID, zcodeMailboxUnreadDir, entry.file)); err == nil {
			a.steerLedger.Drop(entry.ticket)
		} else {
			a.steerLedger.Uncertain(entry.ticket)
		}
	}
	a.steerLedger.Settle(a.sink)
	if a.mailboxRoot == "" || sessionID == "" {
		return
	}
	// read holds the drained copies, which the runtime keeps forever. The
	// drain re-creates both directories.
	if err := os.RemoveAll(filepath.Join(a.mailboxRoot, sessionID)); err != nil {
		slog.Warn("zcode withdraw undelivered steer envelopes", "agent_id", a.AgentID(), "error", err)
	}
}

// hasPendingZCodeSteers is the cheap guard PublishTurnActive reads.
func (a *Agent) hasPendingZCodeSteers() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return len(a.mailboxSteers) > 0
}

// cleanupZCodeMailbox removes the mailbox root. The root is per agent process:
// nothing survives the process, so an envelope a stopped turn left is not
// deliverable by any later run. The ledger is reset rather than settled: the
// process exit owns the queue's recovery of inputs still awaiting their echo.
func (a *Agent) cleanupZCodeMailbox() {
	a.mailboxOnce.Do(func() {
		a.Mu.Lock()
		root := a.mailboxRoot
		a.mailboxSteers = map[string]*zcodeMailboxSteer{}
		a.Mu.Unlock()
		a.steerLedger.Reset()
		if root != "" {
			_ = os.RemoveAll(root)
		}
	})
}
