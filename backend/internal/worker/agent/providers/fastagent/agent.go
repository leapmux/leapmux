package fastagent

import (
	"sync"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one Fast Agent ACP process and its native launch policy.
// The shared base owns transport; this provider owns its session and child state.
type Agent struct {
	acp.Base
	// bypassPermissions is the immutable --no-permissions choice for this process.
	bypassPermissions bool

	audioQueryMu    sync.Mutex
	audioQuery      *fastagentAudioQuery
	nativeCommands  bool
	workingDir      string
	rootMu          sync.Mutex
	rootSession     string
	rootFresh       bool
	rootEpoch       uint64
	rootFailureWork providerkit.CoalescingWork
	rootClosing     bool
	rootArchiveWG   sync.WaitGroup
	home            string
	clock           quartz.Clock
	childMu         sync.Mutex
	childState      map[string]*fastagentChildState
	childDone       map[string]struct{}
	childClaims     map[string]*fastagentChildState
	childStop       chan struct{}
	archiveWG       sync.WaitGroup
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)
