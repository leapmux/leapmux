package fastagent

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Agent manages one fast-agent ACP process. fast-agent speaks standard ACP
// with no vendor extensions, so the embedded base is the whole runtime.
type Agent struct {
	acp.Base
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)
