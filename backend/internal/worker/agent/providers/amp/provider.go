package amp

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ampProvider is Amp's stateless wire-format plugin.
//
// Every question it answers with the neutral default is one Amp gives no
// provider-specific answer to: no plan mode, no interrupt frame (a signal
// interrupts Amp), no control frame on the wire, a thread id that the token rule
// accepts, and a turn-end row whose tool count rides in the worker's metadata.
type ampProvider struct {
	agent.ProviderDefaults
	// cli finds the Amp CLI that lists the stored threads. The zero value finds
	// the one on the user's PATH, as ampLocator does. A test states the fake CLI
	// by its absolute path, so the reader can never reach the user's real Amp
	// account, whatever the shell's profile puts on PATH.
	cli launch.Locator
}

// ValidateAttachment accepts text and images and refuses the rest. See
// validateAttachment for the whole policy.
func (ampProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return validateAttachment(attachment)
}

// ListStoredSessions lists Amp's threads of the working directory's workspace;
// see sessions.go.
func (p ampProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	cli := p.cli
	if !cli.Valid() {
		cli = ampLocator
	}
	return storedSessions(ctx, cli, q)
}
