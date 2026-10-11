package kilo

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// Agent manages a single Kilo ACP process.
type Agent struct {
	opencode.FamilyBase
	steers kiloSteerState
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Kilo steers through a second ACP prompt and keeps its late answer as a turn.
// This assertion keeps the capability when the family or Kilo agent changes.
var _ agent.InputSteerer = (*Agent)(nil)
