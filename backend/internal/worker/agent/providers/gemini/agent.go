package gemini

import (
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Agent owns one Gemini CLI process and its native session.
type Agent struct {
	acp.Base
	modeMu         sync.Mutex
	modeGeneration uint64
	modeCalls      map[string]geminiModeCall
	childMu        sync.Mutex
	children       *geminiChildTranscript
}

type geminiModeCall struct {
	value      string
	generation uint64
}

var _ agent.Agent = (*Agent)(nil)
