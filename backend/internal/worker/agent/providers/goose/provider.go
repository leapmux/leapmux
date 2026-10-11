package goose

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// gooseProvider is the wire-format plugin for Goose. The embedded Provider
// answers shared ACP questions; Goose handles its own session store and tools.
type gooseProvider struct {
	acp.Provider
}

// ValidateAttachment refuses embedded blobs that Goose's ACP bridge drops.
func (gooseProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindPDF || attachment.Kind == agent.AttachmentKindBinary {
		return fmt.Errorf("goose does not support %s attachments: %s", attachment.Kind, attachment.Filename)
	}
	return nil
}

// ListStoredSessions reads Goose's own session store; see sessions.go.
func (gooseProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return gooseStoredSessions(ctx, q)
}
