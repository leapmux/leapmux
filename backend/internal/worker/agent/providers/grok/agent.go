package grok

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
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
	// chunkStreamStart names the model request that produced the last message
	// chunk, through the streamStartMs of its notification metadata. A retry
	// starts a new request, so the first chunk of a new stream makes the
	// buffered text of the previous stream the failed attempt's, even when Grok
	// flushed that text only after its retry_state frame (see
	// observeNotificationMetadata). Zero states that no chunk of this turn
	// named a stream.
	chunkStreamStart int64

	steerReplies grokSteerReplies
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

// SteerInput sends text into the running turn and returns its native receipt
// when the request supplies a queue input ID. Grok delivers the input at the
// next safe point, after the running tool call, and echoes it on
// `_x.ai/session/interjection`.
//
// A steer that reaches an idle session is not lost: Grok runs it as a turn of
// its own, which the queue notification then reports as an agent turn.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	content, attachments := request.Content, request.Attachments
	if !a.PromptActive() {
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	text, images, err := grokSteerContent(content, agent.ClassifyAttachments(attachments))
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	var receipt agent.NativeInputReceipt
	err = a.WithSessionID(func(sessionID string) error {
		nativeID := uuid.NewString()
		params := map[string]any{
			"sessionId":      sessionID,
			"text":           text,
			"interjectionId": nativeID,
		}
		// The structured blocks carry an image, which the text cannot. Grok reads
		// the first text block in place of `text`, so that block repeats it.
		if len(images) > 0 {
			params["content"] = acp.BuildPromptBlocks(text, images)
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal the Grok steer: %w", err)
		}
		var tracked *grokSteerReply
		if request.InputID != "" {
			receipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: nativeID}
			payload := grokInterjectionPayload{text: text}
			for _, image := range images {
				payload.images = append(payload.images, grokImageFingerprint(image.MIMEType, image.Filename, image.Data))
			}
			tracked = a.admitGrokSteer(receipt, payload)
		}
		if _, err := a.SendRequest(grokInterjectMethod, raw, a.APITimeout()); err != nil {
			a.forgetGrokSteer(nativeID)
			if providerkit.HasJSONRPCErrorCode(err, -32602) {
				return agent.ErrNoActiveTurn
			}
			return providerkit.ClassifyJSONRPCDeliveryError(grokInterjectMethod, err)
		}
		if receipt.InputID != "" && tracked == nil {
			a.retireGrokSteer(receipt)
		}
		return nil
	})
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}

// grokSteerContent prepares the text and images that Grok's interjection reads.
// Grok reads nothing else
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
func grokSteerContent(content string, attachments []agent.ClassifiedAttachment) (string, []agent.ClassifiedAttachment, error) {
	parts := make([]string, 0, len(attachments)+1)
	if content != "" {
		parts = append(parts, content)
	}
	var images []agent.ClassifiedAttachment
	for _, attachment := range attachments {
		switch attachment.Kind {
		case contracts.AttachmentMediaKindText:
			parts = append(parts, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case contracts.AttachmentMediaKindImage:
			images = append(images, attachment)
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
	_, err := a.SendInput(grokCompactCommand, nil)
	return err
}
