package opencode

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

// FamilyProvider is the plugin half OpenCode and Kilo share: both are
// ACP providers, and both carry native tool-result metadata (a to-do list, see
// todo.go) in addition to ACP plan events. Each provider embeds it in
// a plugin type of its own, which adds where that provider keeps its sessions.
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
type opencodeProvider struct{ FamilyProvider }

// ListStoredSessions reads OpenCode's own session store; see sessions.go.
func (opencodeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodeStoredSessions(ctx, q)
}
