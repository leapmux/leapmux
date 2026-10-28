package qwen

import (
	"fmt"
	"strings"
	"sync"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qwenCompressCommand is the prompt that compacts the conversation. Qwen runs
// it as a turn of its own and reports its progress on the turn's messages.
const qwenCompressCommand = "/compress"

// Agent manages a single Qwen Code process.
type Agent struct {
	acp.Base

	// stateMu guards the fields below. The reader goroutine writes most fields.
	// Worker goroutines read them. Release stateMu before calling the base or sink.
	stateMu           sync.Mutex
	steer             steerQueue
	children          childState
	tools             toolInputs
	discreteMessageID uint64
	retryCount        int64
	requestHasText    bool
	retryResolution   providerkit.RetryResolution
	compressPending   *qwenCompactionRequest
	// outputStyle is the launch value of the Output Style axis. Qwen reads it
	// only at start, so a change restarts the process.
	outputStyle string
	// clock paces the readers of background transcripts. Start sets the real
	// clock, and a test sets a mock.
	clock quartz.Clock
	// workflowStopping closes process-owned workflow admission before Stop joins readers.
	workflowStopping bool
	workflowReader   *workflowSessionReader
}

// qwenCompactionRequest identifies one admitted manual compaction.
// The nonzero size gives concurrent requests distinct pointer identities.
type qwenCompactionRequest struct{ _ byte }

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// Agent compacts through Qwen's own `/compress` turn.
var _ agent.ContextCompactor = (*Agent)(nil)

// steerItem is one message that the reader sent into a running turn.
type steerItem struct {
	content     string
	attachments []*leapmuxv1.Attachment
}

// steerQueue holds the steered messages that Qwen did not pull yet. Guarded
// by Agent.stateMu.
//
// Qwen has no method that pushes input into a running turn. It PULLS instead:
// after each tool batch it asks the client for queued input, and the model
// reads what the answer carries before its next call. A turn that ends with no
// further tool batch never asks, so the base sends what is left as the next
// prompt.
type steerQueue struct {
	items []steerItem
}

// qwenMaxDrainItems is the most items Qwen reads from one drain. It reads the
// first ten and drops the rest, so the queue keeps the rest for the next drain.
const qwenMaxDrainItems = 10

// take removes and returns up to limit items, oldest first. A limit of zero or
// less takes every item.
func (q *steerQueue) take(limit int) []steerItem {
	n := len(q.items)
	if limit > 0 && n > limit {
		n = limit
	}
	taken := q.items[:n:n]
	q.items = append([]steerItem(nil), q.items[n:]...)
	return taken
}

// SteerInput queues text for the running turn. Qwen reads it after the running
// tool batch, or, when the turn ends first, as the next prompt.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	// Native drains run on the RPC reader, so they cannot query the model's
	// audio modality. Leave audio in the worker queue for a validated prompt.
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind == contracts.AttachmentMediaKindAudio {
			return agent.ErrSteeringUnsupported
		}
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	a.stateMu.Lock()
	a.steer.items = append(a.steer.items, steerItem{content: content, attachments: attachments})
	a.stateMu.Unlock()
	return nil
}

// followUpPrompt gives the base the input that the ended turn never read. A
// turn that the reader stopped takes it with it: the input was for that turn,
// and the transcript states that it never reached Qwen.
func (a *Agent) followUpPrompt(sessionID string, stopped bool) (string, []*leapmuxv1.Attachment, bool) {
	a.stateMu.Lock()
	if a.CurrentSessionID() != sessionID {
		a.stateMu.Unlock()
		return "", nil, false
	}
	items := a.steer.take(0)
	a.stateMu.Unlock()
	if len(items) == 0 {
		return "", nil, false
	}
	if stopped {
		a.reportStatus(fmt.Sprintf("%s sent during the stopped turn did not reach Qwen Code", pluralMessages(len(items))))
		return "", nil, false
	}
	return joinSteerItems(items)
}

// joinSteerItems folds the queued messages into one prompt, in order.
func joinSteerItems(items []steerItem) (string, []*leapmuxv1.Attachment, bool) {
	texts := make([]string, 0, len(items))
	var attachments []*leapmuxv1.Attachment
	for _, item := range items {
		if text := strings.TrimSpace(item.content); text != "" {
			texts = append(texts, item.content)
		}
		attachments = append(attachments, item.attachments...)
	}
	if len(texts) == 0 && len(attachments) == 0 {
		return "", nil, false
	}
	return strings.Join(texts, "\n\n"), attachments, true
}

// pluralMessages states a count of messages.
func pluralMessages(n int) string {
	if n == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", n)
}

// CompactContext asks Qwen to compact the conversation. Qwen's own
// `/compress` makes the summary turn, and states its result on the turn's
// messages.
func (a *Agent) CompactContext() error {
	request := &qwenCompactionRequest{}
	err := a.SendPreparedPrompt(qwenCompressCommand, nil, func(string) error {
		a.stateMu.Lock()
		a.compressPending = request
		a.stateMu.Unlock()
		return nil
	})
	if err == nil {
		return nil
	}
	return a.WithSessionID(func(string) error {
		a.stateMu.Lock()
		current := a.compressPending == request
		if current {
			a.compressPending = nil
		}
		a.stateMu.Unlock()
		if current && !a.PromptActive() {
			providerkit.EndCompaction(a.Sink())
		}
		return err
	})
}
