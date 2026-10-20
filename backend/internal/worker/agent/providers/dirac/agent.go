package dirac

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// diracSteerMethod is the extension notification that carries steering text
// into a running turn. Dirac advertises it as `_meta["dev.dirac/whisper"]` on
// the initialize response, and answers with `dev.dirac/steering_status`.
const diracSteerMethod = "dev.dirac/whisper"

// Agent manages one Dirac ACP process.
type Agent struct {
	acp.Base
	root       string
	clock      quartz.Clock
	childMu    sync.Mutex
	childState map[string]*diracChildState
	childDone  map[string]struct{}
	childStop  chan struct{}
	archiveWG  sync.WaitGroup
	// claimedMu guards claimedCards, the tool calls whose cards the notice
	// observer consumed (see observeDiracNotice).
	claimedMu    sync.Mutex
	claimedCards map[string]struct{}
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers through `dev.dirac/whisper`. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying
// InputSteerer, so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// SteerInput sends text into the running turn as a `dev.dirac/whisper`
// notification. Dirac queues it and answers `dev.dirac/steering_status` with
// `queued` or `sent`. A steer that reaches an idle session is refused, because
// the whisper carries no turn to land in and Dirac buffers only the whispers
// that arrive while a session has a pending prompt.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if content == "" {
		return nil
	}
	// The whisper carries text alone, so the queue keeps an attachment-bearing
	// input for the next turn.
	if err := providerkit.TextOnlySteer("Dirac", attachments); err != nil {
		return err
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	err := a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"text":      content,
		})
		if err != nil {
			return fmt.Errorf("marshal the Dirac whisper: %w", err)
		}
		return a.SendNotification(diracSteerMethod, params)
	})
	// A write failure is a delivery failure, not a missing turn: the turn ran
	// while the whisper left.
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError(diracSteerMethod, err)
	}
	// The turn can end while the whisper writes; a whisper into an ended turn
	// reaches nothing.
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return nil
}
