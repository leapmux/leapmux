package junie

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Agent manages one Junie ACP process.
type Agent struct {
	acp.Base

	// goalMu guards the goal-report bookkeeping. It is separate from Base.Mu so
	// the goal fold never takes the base lock.
	goalMu sync.Mutex
	// goalStartedAt is when this process began. A goal whose createdAt
	// predates it was stored by an earlier run and its first report is a
	// restatement.
	goalStartedAt time.Time
	// goalLast is the fingerprint of the last goal report, and nil when the
	// goal is absent. It separates a restatement from a transition.
	goalLast *junieGoalFingerprint
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers by a queued prompt of its own. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying
// InputSteerer, so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SteerInput queues text for the running turn as a follow-up prompt. Junie
// advertises steering (`_meta.steering.supported`) but states no separate
// steer method, so the input rides `session/prompt`, which Junie queues for the
// turn that runs.
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
			"prompt":    acp.BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
		})
		if err != nil {
			return fmt.Errorf("marshal the Junie steer: %w", err)
		}
		if _, err := a.SendRequest(acp.MethodSessionPrompt, params, a.APITimeout()); err != nil {
			return agent.ErrNoActiveTurn
		}
		return nil
	})
}
