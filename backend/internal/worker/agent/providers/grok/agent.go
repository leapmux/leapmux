package grok

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

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
		case agent.AttachmentKindText:
			parts = append(parts, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
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
	return a.SendInput(grokCompactCommand, nil)
}
