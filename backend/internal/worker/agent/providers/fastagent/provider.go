package fastagent

import (
	"context"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// fastagentProvider is the wire-format plugin for fast-agent. fast-agent
// speaks plain ACP, so the embedded Provider answers every protocol question;
// this type owns Fast Agent's session store and attachment policy.
type fastagentProvider struct {
	acp.Provider
}

// ListStoredSessions reads fast-agent's own session store; see sessions.go.
func (fastagentProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return fastagentStoredSessions(ctx, q)
}

// ValidateAttachment permits the content blocks Fast Agent converts to model input.
func (fastagentProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		if len(attachment.Data) == 0 || !slices.Contains(contracts.FastagentAudioMIMEMembers, attachment.MIMEType) {
			return fmt.Errorf("cannot send Fast Agent audio attachment %q with media type %s", attachment.Filename, attachment.MIMEType)
		}
		return nil
	}
	switch attachment.Kind {
	case contracts.AttachmentMediaKindText, contracts.AttachmentMediaKindImage, contracts.AttachmentMediaKindPdf:
		return nil
	default:
		return fmt.Errorf("fast-agent cannot send the attachment %s", attachment.Filename)
	}
}
