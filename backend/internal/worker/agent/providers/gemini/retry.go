package gemini

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Gemini CLI 0.65.0 fails a prompt with a JSON-RPC error whose code is the
// HTTP status of the failed model request, or 500 when it states none, and
// whose message is the model's error text. It sends no data. A safety block
// and an empty response end the turn with end_turn instead (the ACP prompt
// loop of the bundle gemini-GLQ4MVLB.js, and classifyGoogleError of
// chunk-DKZARV6U.js).
var (
	// geminiStatedDelayRe reads the delay that a 429 states: the Code Assist
	// quota reset, the RetryInfo delay that Gemini CLI appends, and the delay
	// in the Gemini API message. The capture is the delay alone.
	geminiStatedDelayRe = regexp.MustCompile(`(?:Your quota will reset after|Suggested retry after|Please retry in) (\d+(?:\.\d+)?(?:ms|[smhd])(?:\s?\d+(?:\.\d+)?(?:ms|[smhd]))*)`)
	// geminiCapacityRe is the capacity wording that Gemini CLI itself treats
	// as an exhausted model capacity.
	geminiCapacityRe = regexp.MustCompile(`(?i)exhausted your capacity|no capacity available|capacity exceeded|MODEL_CAPACITY_EXHAUSTED`)
	// geminiServerBodyRe reads a Google API error body of a server failure.
	geminiServerBodyRe = regexp.MustCompile(`"code"\s*:\s*5\d\d\b|"status"\s*:\s*"(?:INTERNAL|UNAVAILABLE|DEADLINE_EXCEEDED)"`)
	// geminiNetworkRe reads a Node.js network failure, which Gemini CLI
	// reports with the code 500.
	geminiNetworkRe = regexp.MustCompile(`(?i)fetch failed|terminated|socket hang up|other side closed|ECONNRESET|ETIMEDOUT|ENOTFOUND|EAI_AGAIN|ECONNREFUSED|UND_ERR`)
)

// The JSON-RPC codes of a failed prompt that the verdict reads: HTTP statuses.
const (
	geminiCodeRateLimited      = 429
	geminiCodeClientClosed     = 499
	geminiCodeServerError      = 500
	geminiCodeFirstUpstream5xx = 502
	geminiCodeLast5xx          = 599
)

// finishGeminiPrompt ends the child transcripts that the turn held open, then
// returns the turn's retry verdict (Hooks.PromptEnded). A stop of the reader
// gives NotRetryable. A long retry wait that LeapMux stopped gives a
// usage-limit retry at the instant that Gemini stated. Any other end decides
// from the prompt's JSON-RPC error.
func (a *Agent) finishGeminiPrompt(err error, stopped bool) acp.PromptEnding {
	completion := agent.MessageCompletionComplete
	if stopped {
		completion = agent.MessageCompletionInterrupted
	}
	a.finishNativeChildren(completion)
	a.retryMu.Lock()
	userStop, wait := a.userStopRequested, a.longWait
	a.retryMu.Unlock()
	var verdict acp.TurnVerdict
	switch {
	case userStop:
	case wait != nil:
		verdict = acp.TurnVerdict{Failure: wait.failure, Payload: wait.payload}
	default:
		verdict = geminiTurnFailure(err, stopped, a.Clock().Now())
	}
	return acp.PromptEnding{Retry: &verdict}
}

// geminiLongWait is the retry wait that made LeapMux stop the turn.
type geminiLongWait struct {
	failure providerkit.TurnFailure
	// payload is the retry's source: {"message","retryDelayMs"}.
	payload json.RawMessage
}

// clearGeminiRetryState forgets the long wait and the reader's stop, when a
// prompt starts or a context clear replaces the session.
func (a *Agent) clearGeminiRetryState() {
	a.retryMu.Lock()
	a.longWait, a.userStopRequested = nil, false
	a.retryMu.Unlock()
}

// Interrupt stops the running turn for the reader. It records the stop first,
// so the turn's verdict schedules no retry, also after a long wait. LeapMux's
// own stop for a long wait calls Base.Interrupt and records nothing.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.noteUserStop()
	return a.Base.Interrupt(stop)
}

// SendRawInput forwards a raw frame. A raw session/cancel is the reader's
// stop, as Interrupt is, so it records the stop for the verdict too.
func (a *Agent) SendRawInput(raw []byte, stop agent.StopContext) error {
	if (acp.Provider{}).IsInterrupt(string(raw)) {
		a.noteUserStop()
	}
	return a.Base.SendRawInput(raw, stop)
}

func (a *Agent) noteUserStop() {
	a.retryMu.Lock()
	a.userStopRequested = true
	a.retryMu.Unlock()
}

// observeGeminiDebugLog reads one line of Gemini CLI's debug log. Gemini states
// a retry wait of its own nowhere on the ACP stream, only in the warning that
// it writes before the wait (geminiDebugLog).
func (a *Agent) observeGeminiDebugLog(line string) {
	message, delay, ok := a.retryWarnings.read(line)
	if !ok {
		return
	}
	a.stopForLongRetryWait(message, delay)
}

// closeDebugLog stops reading the debug log and removes it. An agent that a
// test builds without Start has no debug log.
func (a *Agent) closeDebugLog() {
	if a.debugLog != nil {
		a.debugLog.close()
	}
}

// stopForLongRetryWait ends the running turn when Gemini waits more than
// providerkit.LongRetryWaitThreshold for its next attempt. Gemini CLI 0.65.0
// waits out an exhausted capacity inside the open turn for the delay that the
// error states, up to twice, which is about 19 hours for the Code Assist
// quota (retryWithBackoff of chunk-DKZARV6U.js), and a restart loses that
// wait. It acts once per prompt.
func (a *Agent) stopForLongRetryWait(message string, delay time.Duration) {
	if !a.PromptActive() {
		return
	}
	observedAt := a.Clock().Now()
	failure, ok := providerkit.LongRetryWaitFailure(observedAt.Add(delay), observedAt)
	if !ok {
		return
	}
	payload, err := json.Marshal(map[string]any{"message": message, "retryDelayMs": delay.Milliseconds()})
	if err != nil {
		return
	}
	a.retryMu.Lock()
	recorded := a.longWait != nil
	if !recorded {
		a.longWait = &geminiLongWait{failure: failure, payload: payload}
	}
	a.retryMu.Unlock()
	if recorded {
		return
	}
	if err := a.Base.Interrupt(agent.NewStopContext(nil)); err != nil {
		slog.Warn("stop the Gemini turn for its long retry wait", "agent_id", a.AgentID(), "reset_at", failure.ResetAt, "error", err)
	}
}

// The retry warning that Gemini CLI 0.65.0 writes before it waits for a
// stated delay: `Attempt <n> failed: <message>. Retrying after <ms>ms...`
// (retryWithBackoff of chunk-DKZARV6U.js). Its debug log opens the warning's
// first line with the time and the level, `[<ISO time>] [WARN] `. The message
// can span lines, so the warning ends at the line that holds the delay.
var (
	geminiRetryWarningStartRe = regexp.MustCompile(`^\[[^\]]*\] \[WARN\] Attempt \d+ failed: `)
	geminiRetryWarningEndRe   = regexp.MustCompile(`\. Retrying after (\d{1,12})ms\.\.\.$`)
)

// geminiRetryWarningMaxLines caps the lines of one warning that the reader
// holds while it waits for the line that holds the delay.
const geminiRetryWarningMaxLines = 64

// geminiRetryWarningReader joins the lines of one retry warning.
type geminiRetryWarningReader struct {
	// pending holds the lines of a warning whose delay has not arrived.
	pending []string
}

// read takes one line of the debug log. It returns the failure message and
// the delay of the warning that the line completes.
func (r *geminiRetryWarningReader) read(line string) (message string, delay time.Duration, ok bool) {
	switch start := geminiRetryWarningStartRe.FindStringIndex(line); {
	case start != nil:
		r.pending = append(r.pending[:0], line[start[1]:])
	case len(r.pending) == 0:
		return "", 0, false
	case len(r.pending) >= geminiRetryWarningMaxLines:
		r.pending = nil
		return "", 0, false
	default:
		r.pending = append(r.pending, line)
	}
	last := r.pending[len(r.pending)-1]
	end := geminiRetryWarningEndRe.FindStringSubmatchIndex(last)
	if end == nil {
		return "", 0, false
	}
	milliseconds, err := strconv.ParseInt(last[end[2]:end[3]], 10, 64)
	r.pending[len(r.pending)-1] = last[:end[0]]
	message = strings.Join(r.pending, "\n")
	r.pending = nil
	if err != nil {
		return "", 0, false
	}
	return message, time.Duration(milliseconds) * time.Millisecond, true
}

// geminiTurnFailure decides the retry verdict of one prompt end. The payload
// is the error response {"code","message"}.
func geminiTurnFailure(err error, stopped bool, observedAt time.Time) acp.TurnVerdict {
	if stopped || err == nil {
		return acp.TurnVerdict{}
	}
	responseErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return acp.TurnVerdict{}
	}
	return acp.TurnVerdict{
		Failure: geminiFailure(responseErr.Code, responseErr.Message, observedAt),
		Payload: responseErr.WireJSON(),
	}
}

// geminiFailure classifies a failed prompt by its code and its message.
func geminiFailure(code int, message string, observedAt time.Time) providerkit.TurnFailure {
	verdict, vendorDecided := modelvendor.ClassifyError(message, observedAt)
	if vendorDecided && verdict.Definitive {
		return verdict.Failure
	}
	switch {
	case code == geminiCodeRateLimited:
		return geminiRateLimitFailure(message, verdict, vendorDecided, observedAt)
	case code == geminiCodeClientClosed || (code >= geminiCodeFirstUpstream5xx && code <= geminiCodeLast5xx):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Gemini reported a server error"}
	case code == geminiCodeServerError && (geminiServerBodyRe.MatchString(message) || geminiNetworkRe.MatchString(message)):
		// 500 is also Gemini CLI's code for an error that states no status,
		// so only a server body or a network failure retries.
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Gemini reported a server or network failure"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// geminiRateLimitFailure classifies a 429. A stated delay makes it a usage
// limit that lifts at observedAt plus the delay, even with capacity wording:
// the Code Assist capacity message states its reset. Without a delay, the
// capacity wording is transient, and otherwise the vendor wording decides
// before the plain rate limit.
func geminiRateLimitFailure(message string, verdict modelvendor.Verdict, vendorDecided bool, observedAt time.Time) providerkit.TurnFailure {
	failure := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Gemini reported a 429"}
	delay, stated := geminiStatedDelay(message)
	if !stated {
		switch {
		case geminiCapacityRe.MatchString(message):
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Gemini reported an exhausted model capacity"}
		case vendorDecided:
			return verdict.Failure
		}
		return failure
	}
	if vendorDecided && verdict.Failure.Kind == providerkit.TurnFailureUsageLimited {
		failure = verdict.Failure
	}
	// WithResetHint keeps the rate limit when the delay is zero.
	return providerkit.WithResetHint(failure, observedAt.Add(delay), observedAt)
}

// geminiStatedDelay reads the delay that a 429 message states.
func geminiStatedDelay(message string) (time.Duration, bool) {
	match := geminiStatedDelayRe.FindStringSubmatch(message)
	if match == nil {
		return 0, false
	}
	return providerkit.ParseRetryDelay(match[1])
}
