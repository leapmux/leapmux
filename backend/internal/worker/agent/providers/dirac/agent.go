package dirac

import (
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// diracSteerMethod is the extension notification that carries steering text
// into a running turn. Dirac advertises it as `_meta["dev.dirac/whisper"]` on
// the initialize response, and answers with `dev.dirac/steering_status`.
const diracSteerMethod = "dev.dirac/whisper"

// Agent manages one Dirac ACP process.
type Agent struct {
	acp.Base
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers through `dev.dirac/whisper`. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying
// InputSteerer, so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SteerInput sends text into the running turn as a `dev.dirac/whisper`
// notification. Dirac queues it and answers `dev.dirac/steering_status` with
// `queued` or `sent`. A steer that reaches an idle session is refused, because
// the whisper carries no turn to land in and Dirac buffers only the whispers
// that arrive while a session has a pending prompt.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if content == "" {
		return nil
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"text":      content,
		})
		if err != nil {
			return fmt.Errorf("marshal the Dirac whisper: %w", err)
		}
		if err := a.SendNotification(diracSteerMethod, params); err != nil {
			return agent.ErrNoActiveTurn
		}
		return nil
	})
}
