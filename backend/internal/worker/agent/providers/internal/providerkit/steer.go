package providerkit

import (
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// TextOnlySteer refuses the attachments of a steer for a provider whose native
// steer route carries text alone. Kiro and Dirac share it. The worker keeps
// the input queued with its attachments for the next turn.
func TextOnlySteer(agentName string, attachments []*leapmuxv1.Attachment) error {
	if len(attachments) > 0 {
		return fmt.Errorf("%w: %s steers a running turn with text alone", agent.ErrSteeringUnsupported, agentName)
	}
	return nil
}
