package gemini

import (
	"sync"
	"time"

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
	// retryMu guards longWait and userStopRequested, which the prompt's end
	// reads for the retry verdict, and which the next prompt forgets.
	retryMu sync.Mutex
	// longWait is the retry wait of Gemini's own that made LeapMux stop the
	// running turn (stopForLongRetryWait).
	longWait *geminiLongWait
	// userStopRequested records a stop that the reader asked for.
	userStopRequested bool
	// retryMu also protects the warning parser and its original prompt identity.
	retryWarnings     geminiRetryWarningReader
	retryEpoch        uint64
	retryPreparedAt   time.Time
	retryConversation *acp.Conversation
	// debugLog is the debug log of the process, which Start creates. It is nil
	// for an agent that a test builds without Start.
	debugLog *geminiDebugLog
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
