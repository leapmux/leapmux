package reasonix

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// reasonixProvider is the wire-format plugin for Reasonix. Reasonix speaks ACP,
// so the embedded Provider answers the protocol questions; this type adds
// where Reasonix keeps its sessions and its text-only attachment policy.
type reasonixProvider struct {
	acp.Provider
}

// ListStoredSessions reads Reasonix's own session store; see sessions.go.
func (reasonixProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return reasonixStoredSessions(ctx, q)
}

// ValidateAttachment enforces Reasonix's text-only policy: it advertises
// image:false/audio:false and drops any non-text content block, so reject
// everything but text up front.
func (reasonixProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind != agent.AttachmentKindText {
		return fmt.Errorf("reasonix only supports text attachments: %s", attachment.Filename)
	}
	return nil
}
