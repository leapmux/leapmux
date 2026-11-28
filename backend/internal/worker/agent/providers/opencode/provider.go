package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// FamilyProvider supplies Kilo's ACP wire hooks and native daemon notices.
// Its tool-result metadata carries the to-do list in addition to ACP plan events.
// OpenCode uses opencodeProvider for its native protocol.
type FamilyProvider struct{ acp.Provider }

// Classify identifies native notices that share a notification thread.
func (FamilyProvider) Classify(content json.RawMessage) agent.NotificationClassification {
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
	case contracts.OpenCodeEventSessionCompacted:
		return agent.CompactionBoundaryNotification()
	case contracts.OpenCodeEventSessionStatus:
		if providerkit.StringField(frame.Properties.Status, "type") == contracts.OpenCodeStatusTypeRetry {
			return agent.APIRetryNotification()
		}
	}
	return agent.NotificationClassification{}
}

// ValidateAttachment refuses a file type that the OpenCode family cannot send
// through its model path.
func (FamilyProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		return providerkit.ValidateAudioAttachment(attachment, "the audio attachment", contracts.OpenCodeAudioMIMEMembers)
	}
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("OpenCode protocol does not support binary attachments: %s", attachment.Filename)
	}
	return nil
}

// ChildCapabilities states what a child tab offers.
//
// AcceptsInterrupt is true: the family base stops one child session through
// the daemon's own session abort (FamilyBase.InterruptChild). AcceptsMessages
// is true: the family base steers a running child session with a second prompt
// on that session (FamilyBase.SteerChildInput).
func (FamilyProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}
}

// opencodeProvider is the wire-format plugin for OpenCode.
type opencodeProvider struct{ agent.ProviderDefaults }

func (opencodeProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return validateOpenCodeAttachment(attachment)
}

func (opencodeProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}
}

func (opencodeProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var frame struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return agent.NotificationClassification{}
	}
	if slices.Contains(contracts.OpenCodeNativeMetadataEventMembers, frame.Type) ||
		slices.Contains(contracts.OpenCodeNativeMetadataMessageTypeMembers, frame.Type) {
		return agent.StatusNotification()
	}
	switch frame.Type {
	case contracts.OpenCodeNativeEventRetryScheduled:
		return agent.APIRetryNotification()
	case contracts.OpenCodeNativeEventCompactionStarted, contracts.OpenCodeNativeEventCompactionDelta:
		return agent.StatusNotification()
	case contracts.OpenCodeNativeEventCompactionEnded, contracts.OpenCodeNativeEventCompactionFailed:
		return agent.CompactionBoundaryNotification()
	case contracts.OpenCodeNativeMessageTypeCompaction:
		switch frame.Status {
		case contracts.OpenCodeNativeCompactionStatusRunning:
			return agent.StatusNotification()
		case contracts.OpenCodeNativeCompactionStatusCompleted, contracts.OpenCodeNativeCompactionStatusFailed:
			return agent.CompactionBoundaryNotification()
		}
	}
	return agent.NotificationClassification{}
}

// SupportsFork reports that OpenCode supports native conversation-history
// forking via POST /api/session/<id>/fork.
func (opencodeProvider) SupportsFork() bool { return true }

// ListStoredSessions reads OpenCode's own session store; see sessions.go.
func (opencodeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodeStoredSessions(ctx, q)
}
