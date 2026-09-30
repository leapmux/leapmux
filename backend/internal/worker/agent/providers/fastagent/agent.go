package fastagent

import (
	"sync"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Agent manages one fast-agent ACP process. fast-agent speaks standard ACP
// with no vendor extensions, so the embedded base is the whole runtime.
type Agent struct {
	acp.Base
	home        string
	clock       quartz.Clock
	childMu     sync.Mutex
	childState  map[string]*fastagentChildState
	childDone   map[string]struct{}
	childClaims map[string]*fastagentChildState
	childStop   chan struct{}
	archiveWG   sync.WaitGroup
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)
