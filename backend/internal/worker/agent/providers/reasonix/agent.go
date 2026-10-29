package reasonix

import (
	"encoding/json"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const (
	reasonixSteerNamespace = "reasonix.io"
	reasonixSteerMethod    = "_reasonix.io/session/steer"
)

// Agent manages a Reasonix Agent Client Protocol (ACP) process.
type Agent struct {
	acp.Base
	goalStatusMu       sync.Mutex
	goalStatusRevision uint64
	// goalOperationGeneration identifies the last explicit goal replacement or clear. Guarded by goalStatusMu.
	goalOperationGeneration uint64
	// statusCursor tracks native snapshot order within one session. Guarded by goalStatusMu.
	statusCursor reasonixStatusCursor
	// goalMethods holds the versioned goal requests that initialize advertises. Guarded by Base.Mu.
	goalMethods map[agent.GoalAction]string
	// lastStatusPhase is the phase the last status update reported. Reasonix
	// restates its WHOLE status on every change, so the same phase arrives many
	// times per turn and only a move to a new one says anything.
	//
	// ACP's session notification mutex serializes dispatch on the stdout reader
	// and during ordered replay. reportReasonixPhase is the one writer, and
	// handleReasonixStatusUpdate is its one caller. The phase report runs outside
	// goalStatusMu because it writes a row and that lock protects goal synchronization.
	lastStatusPhase string
	// planDraft is the assistant text of the current turn. Reasonix raises its
	// plan approval after streaming the proposal, so the observer saves this text
	// for a possible fresh-context execution. Guarded by Base.Mu.
	planDraft strings.Builder
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	result, err := a.SteerAdvertisedResult(content, attachments)
	if err != nil {
		return err
	}
	return readReasonixSteerDisposition(result, "")
}

// readReasonixSteerDisposition reads the disposition of one steer reply. A
// steer_accepted steer reached the active turn, and a queued_followup one
// runs as the follow-up turn when the active one ends, so both deliver. Every
// rejected_* word answers ErrNoActiveTurn, so the queue keeps the input for
// the next turn. An absent or unknown disposition delivers: the reply proved
// the session took the input.
func readReasonixSteerDisposition(result json.RawMessage, override string) error {
	disposition := override
	if disposition == "" {
		var reply struct {
			Disposition string `json:"disposition"`
		}
		if json.Unmarshal(result, &reply) != nil {
			return nil
		}
		disposition = reply.Disposition
	}
	if strings.HasPrefix(disposition, "rejected_") {
		return agent.ErrNoActiveTurn
	}
	return nil
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)
