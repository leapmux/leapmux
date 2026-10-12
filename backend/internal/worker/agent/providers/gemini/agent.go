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
	// modeEchoes holds each session/set_mode that waits for its reply. Guarded
	// by modeMu.
	modeEchoes []*geminiModeEcho
	childMu    sync.Mutex
	children   *geminiChildTranscript
}

type geminiModeCall struct {
	value      string
	generation uint64
}

// geminiModeEcho is one session/set_mode that waits for its reply. Gemini
// sends the `[MODE_UPDATE] <mode>` text of the change before that reply, so
// the text that arrives in this window is the echo of the setter.
type geminiModeEcho struct {
	sessionID string
	mode      string
}

var _ agent.Agent = (*Agent)(nil)
