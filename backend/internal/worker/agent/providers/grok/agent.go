package grok

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/google/uuid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// grokInterjectMethod steers a running turn. Grok does not advertise it in its
// initialize response, so the provider states it.
const grokInterjectMethod = "_x.ai/interject"

// grokCompactCommand is the prompt that compacts the conversation. It runs as a
// turn of its own, which the queue expects of a compaction.
const grokCompactCommand = "/compact"

// Agent manages a single Grok Build process.
type Agent struct {
	acp.Base

	// stateMu guards every field below. The reader goroutine writes most of
	// them and the worker's goroutines read them, and none is held across a
	// call into the base or the sink.
	stateMu  sync.Mutex
	approval approvalState
	turns    turnState
	children childState
	controls controlIndex
	// retryResolution folds the native retry rows of one turn and resolves
	// them at the first genuine model output after the retry.
	retryResolution providerkit.RetryResolution
	// lastFailureType is the error_type of the last failed or exhausted
	// retry_state frame of the main session, "" for none. The end of the
	// prompt takes it, and the start of the next prompt forgets it.
	lastFailureType string
	// compactionPrompt states that the running prompt is Grok's compaction
	// (sendCompactPrompt). The end of the prompt takes it, and the start of
	// the next prompt forgets it.
	compactionPrompt bool
	// chunkStreamStart names the model request that produced the last message
	// chunk, through the streamStartMs of its notification metadata. A retry
	// starts a new request, so the first chunk of a new stream makes the
	// buffered text of the previous stream the failed attempt's, even when Grok
	// flushed that text only after its retry_state frame (see
	// observeNotificationMetadata). Zero states that no chunk of this turn
	// named a stream.
	chunkStreamStart int64
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// Agent compacts through Grok's own `/compact` turn.
var _ agent.ContextCompactor = (*Agent)(nil)

// SteerInput sends text into the running turn. Grok delivers it at the next
// safe point, after the running tool call, and echoes it on
// `_x.ai/session/interjection`.
//
// A steer that reaches an idle session is not lost: Grok runs it as a turn of
// its own, which the queue notification then reports as an agent turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	text, images, err := grokSteerContent(content, agent.ClassifyAttachments(attachments))
	if err != nil {
		return err
	}
	return a.WithSessionID(func(sessionID string) error {
		params := map[string]any{
			"sessionId":      sessionID,
			"text":           text,
			"interjectionId": uuid.NewString(),
		}
		// The structured blocks carry an image, which the text cannot. Grok reads
		// the first text block in place of `text`, so that block repeats it.
		if len(images) > 0 {
			var blocks []map[string]any
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			params["content"] = append(blocks, images...)
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal the Grok steer: %w", err)
		}
		if _, err := a.SendRequest(grokInterjectMethod, raw, a.APITimeout()); err != nil {
			if providerkit.HasJSONRPCErrorCode(err, -32602) {
				return agent.ErrNoActiveTurn
			}
			return providerkit.ClassifyJSONRPCDeliveryError(grokInterjectMethod, err)
		}
		return nil
	})
}

// grokSteerContent builds what one steer carries, in the two forms that Grok's
// interjection reads: its text, and its image blocks. Grok reads nothing else
// (`split_content` in extensions/content.rs keeps the first text block and the
// images), and it answers `queued` whatever else the steer holds. So:
//
//   - A text attachment is inlined into the text, where the model reads it.
//   - An image becomes an image block.
//   - Any other attachment (a PDF, a binary file) refuses the whole steer,
//     with a message that tells the reader to send the file as a message of
//     its own. A session/prompt carries the file as a resource, which Grok
//     reads. A steer that dropped the file would let the model answer without
//     it, and nothing would tell the reader.
func grokSteerContent(content string, attachments []agent.ClassifiedAttachment) (string, []map[string]any, error) {
	parts := make([]string, 0, len(attachments)+1)
	if content != "" {
		parts = append(parts, content)
	}
	var images []map[string]any
	for _, attachment := range attachments {
		switch attachment.Kind {
		case contracts.AttachmentMediaKindText:
			parts = append(parts, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case contracts.AttachmentMediaKindImage:
			images = append(images, acp.BuildPromptBlocks("", []agent.ClassifiedAttachment{attachment})...)
		default:
			return "", nil, fmt.Errorf("a steer to Grok Build carries text and images only, so it cannot carry %s: send it as a new message", attachment.Filename)
		}
	}
	return strings.Join(parts, "\n\n"), images, nil
}

// CompactContext asks Grok to compact the conversation. Grok's own `/compact`
// makes the summary turn, and reports its size on an `auto_compact_completed`
// notification.
func (a *Agent) CompactContext() error {
	return a.sendCompactPrompt(nil, grokCompactCommand)
}

// SendInput starts one prompt. Grok runs a typed /compact as the turn of
// CompactContext, so such a prompt starts the same compaction turn.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !grokStartsCompaction(content, attachments) {
		return a.Base.SendInput(content, attachments)
	}
	return a.sendCompactPrompt(nil, content)
}

// SendInputForSession is SendInput for the session that the caller validated.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if !grokStartsCompaction(content, attachments) {
		return a.Base.SendInputForSession(sessionID, content, attachments)
	}
	return a.sendCompactPrompt(&sessionID, content)
}

// grokStartsCompaction reports whether Grok runs a prompt of content and
// attachments as its compaction: /compact alone. The slash commands of Grok
// Build 1.0.70 state "/compact takes no arguments.", so a /compact with
// arguments runs no compaction. A message with an attachment is a
// conversation turn.
func grokStartsCompaction(content string, attachments []*leapmuxv1.Attachment) bool {
	name, args, ok := providerkit.SlashCommand(content)
	return ok && name == strings.TrimPrefix(grokCompactCommand, "/") && args == "" && len(attachments) == 0
}

// sendCompactPrompt sends content as a prompt that runs Grok's compaction
// turn. expected is the session that the input was written for, or nil for the
// current session. The base offers the prompt's preparation for the current
// session only, so the preparation checks expected against the prompt's
// session itself. The check before the send refuses another session before the
// base claims the turn, as the base's own check does. A failed native write
// leaves the mark on the prompt that never ran, and the start of the next
// prompt clears it.
func (a *Agent) sendCompactPrompt(expected *string, content string) error {
	if err := providerkit.CheckInputSession(expected, a.CurrentSessionID()); err != nil {
		return err
	}
	return a.SendPreparedPrompt(content, nil, a.prepareCompactPrompt(expected))
}

// prepareCompactPrompt returns the preparation of a compaction prompt for the
// session expected, or for any session when expected is nil. It receives the
// session of the prompt, refuses another session than expected, and otherwise
// marks the prompt as the compaction.
func (a *Agent) prepareCompactPrompt(expected *string) func(sessionID string) error {
	return func(sessionID string) error {
		if err := providerkit.CheckInputSession(expected, sessionID); err != nil {
			return err
		}
		a.stateMu.Lock()
		a.compactionPrompt = true
		a.stateMu.Unlock()
		return nil
	}
}
