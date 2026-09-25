package opencode

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// FamilyBase is what OpenCode and Kilo share beyond the ACP base.
//
// Both daemons run the same two transports: the Agent Client Protocol stream that
// carries the session, and the daemon's own HTTP server that carries the questions
// the ACP adapter drops. openCodeQuestions states why that second transport exists.
type FamilyBase struct {
	acp.Base
	Questions openCodeQuestions
}

// SendRawInput answers a question through the daemon's HTTP server, and sends every
// other control answer to the ACP stream unchanged.
//
// The two answers cannot share one path. An ACP control request is a JSON-RPC request
// that LeapMux answers by id on the stream it arrived on; a question never reached
// that stream, so it has no id there to answer and the daemon reads its answer from
// a route instead.
func (b *FamilyBase) SendRawInput(raw []byte) error {
	if handled, err := b.Questions.answer(b.Context(), raw); handled {
		return err
	}
	return b.Base.SendRawInput(raw)
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
	if !b.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return b.SendPromptDetached(content, attachments, func(_ json.RawMessage, err error) {
		if err == nil || b.IsStopped() {
			return
		}
		slog.Error("acp steer failed", "agent_id", b.AgentID(), "provider", b.ProviderName(), "error", err)
		b.Sink().PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
		})
	})
}
