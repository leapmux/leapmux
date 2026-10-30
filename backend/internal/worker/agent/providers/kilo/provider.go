package kilo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kiloProvider interprets Kilo protocol records and validates attachments.
type kiloProvider struct{ acp.Provider }

// Classify identifies native notices that share a notification thread.
func (kiloProvider) Classify(content json.RawMessage) agent.NotificationClassification {
	var frame struct {
		Type       string `json:"type"`
		Properties struct {
			Status json.RawMessage `json:"status"`
		} `json:"properties"`
	}
	if json.Unmarshal(content, &frame) != nil {
		return agent.NotificationClassification{}
	}
	switch frame.Type {
	case contracts.KiloEventSessionCompacted:
		return agent.CompactionBoundaryNotification()
	case contracts.KiloEventSessionStatus:
		if providerkit.StringField(frame.Properties.Status, "type") == contracts.KiloStatusTypeRetry {
			return agent.APIRetryNotification()
		}
	}
	return agent.NotificationClassification{}
}

// ValidateAttachment refuses a file type that Kilo cannot send
// through its model path.
func (kiloProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		return providerkit.ValidateAudioAttachment(attachment, "the audio attachment", contracts.KiloAudioMIMEMembers)
	}
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("the Kilo protocol does not support binary attachments: %s", attachment.Filename)
	}
	return nil
}

// ChildCapabilities states what a child tab offers.
//
// AcceptsInterrupt is true: the agent stops one child session through
// the daemon's own session abort (Agent.InterruptChild). AcceptsMessages
// is true: the agent steers a running child session with a second prompt
// on that session (Agent.SteerChildInput).
func (kiloProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}
}

func (kiloProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return kiloStoredSessions(ctx, q)
}
