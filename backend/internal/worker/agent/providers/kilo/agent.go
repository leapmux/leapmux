package kilo

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// Agent manages a single Kilo ACP process.
type Agent struct {
	opencode.FamilyBase
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Agent steers through the family base, which it embeds, so a fork cannot lose
// the capability by not restating it. This assertion makes that regression a
// compile error.
var _ agent.InputSteerer = (*Agent)(nil)
