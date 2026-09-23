package reasonix

import (
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
	// lastStatusPhase is the phase the last status update reported. Reasonix
	// restates its WHOLE status on every change, so the same phase arrives many
	// times per turn and only a move to a new one says anything.
	//
	// No mutex guards it. The stdout reader goroutine owns it: handleOutput
	// dispatches every notification, reportReasonixPhase is the one writer, and
	// handleReasonixStatusUpdate is its one caller. The phase report runs outside
	// goalStatusMu on purpose, because it writes a row and the lock protects the
	// goal revision alone.
	lastStatusPhase string
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.SteerAdvertised(content, attachments)
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)
