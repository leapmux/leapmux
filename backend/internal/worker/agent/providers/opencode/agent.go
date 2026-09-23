package opencode

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Agent manages a single OpenCode ACP process.
type Agent struct {
	FamilyBase
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Agent steers through the family base. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying InputSteerer,
// so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
