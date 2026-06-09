package agent

import (
	"sync"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// thinkingTokensCharsPerToken is the rough characters-per-token ratio used to
// turn streamed model text into a running token estimate. ~4 characters per
// token is the common rule of thumb for English/code; the thinking-token
// counter is only a coarse "the agent is generating something" signal, so an
// exact tokenizer is unnecessary.
const thinkingTokensCharsPerToken = 4

// thinkingTokenEstimator accumulates a rough running token estimate from the
// text a provider streams while the model is generating (assistant text plus
// reasoning/thinking/plan deltas), for ONE turn-phase. It exists because every
// provider except Claude Code emits no per-delta token telemetry of its own --
// their usage numbers arrive only at response/turn boundaries, too coarse to
// animate the counter -- so we estimate from the streamed characters instead.
//
// It carries its own mutex so it is uniformly safe regardless of how a provider
// drives it: Codex and Pi feed it only from their single serial stdout reader
// goroutine, but the ACP base also touches turn state from the prompt-response
// goroutine, so the estimator self-synchronizes rather than relying on each
// agent's existing lock.
//
// PRECONDITION: observe() must never run concurrently with itself for a given
// estimator -- every provider drives observe() from exactly one goroutine (its
// single stdout reader). accumulate and ship deliberately drop the lock between
// computing an estimate and broadcasting it (ship must not hold the lock across
// the blocking BroadcastSessionInfo), and the gen guard catches a concurrent
// reset()/clear() but NOT a newer accumulate; so two observe() calls racing on
// one estimator could ship out of order and spin the forward-only odometer
// backward. reset() and clear() MAY be driven from a second goroutine (the ACP
// prompt-response goroutine calls both at turn end) -- they are gen-guarded and
// safe against a concurrent observe().
type thinkingTokenEstimator struct {
	mu    sync.Mutex
	chars int64
	// reported is the last estimate observe shipped. It suppresses byte-identical
	// re-broadcasts within a phase (the running estimate only changes every
	// charsPerToken characters, so several consecutive sub-token deltas yield the
	// same number). reset() zeroes it so the next phase re-ships even an unchanged
	// value -- restoring a counter the frontend cleared on a boundary the worker
	// can't observe -- which is why the wire key stays exempt from the
	// service-layer dedup cache.
	reported int64
	// gen counts phase boundaries: every reset()/clear() bumps it. observe()
	// captures gen while computing an estimate under the lock, then re-checks it
	// before shipping; a reset that lands on another goroutine in between ends the
	// phase, so the now-stale estimate is dropped instead of being broadcast after
	// the frontend already cleared (which would spin the forward-only odometer
	// backward). We re-check rather than hold mu across BroadcastSessionInfo
	// because that call performs a blocking network write -- holding the estimator
	// lock across it would stall every observe/reset on the hot streaming path.
	gen uint64
}

// observe folds a streamed model-text delta into the running per-phase estimate
// and broadcasts the updated running total over the ephemeral agent_session_info
// channel under the shared SessionInfoKeyThinkingTokens key. Accumulation (under
// the lock) is split from the broadcast (a blocking network write, done unlocked)
// so the estimator lock is never held across I/O.
func (e *thinkingTokenEstimator) observe(sink OutputSink, text string) {
	est, gen, ok := e.accumulate(text)
	if !ok {
		return
	}
	e.ship(sink, est, gen)
}

// accumulate folds a delta into the running estimate under the lock and returns
// the running token estimate, the phase generation it belongs to, and whether it
// is worth shipping (positive AND changed since the last broadcast). The rune
// count (not the byte length, so multibyte text isn't inflated by its UTF-8
// width) is accumulated and divided ONCE at read time, so a burst of
// sub-charsPerToken deltas can't each truncate to zero and lose the count.
func (e *thinkingTokenEstimator) accumulate(text string) (est int64, gen uint64, ok bool) {
	if text == "" {
		return 0, 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.chars += int64(utf8.RuneCountInString(text))
	est = e.chars / thinkingTokensCharsPerToken
	if est <= 0 || est == e.reported {
		return 0, 0, false
	}
	e.reported = est
	return est, e.gen, true
}

// ship broadcasts est, unless a reset()/clear() bumped the phase generation since
// est's generation was computed -- in which case the phase ended and est belongs
// to a counter the frontend was already told to drop, so shipping it would spin
// the odometer backward. The frontend forwards only positive estimates and
// derives <= 0 as a reset; accumulate never returns a non-positive est, so the
// only non-positive value ship sends is the explicit zero clear() hands it to
// drop the counter (reusing this same generation guard).
func (e *thinkingTokenEstimator) ship(sink OutputSink, est int64, gen uint64) {
	e.mu.Lock()
	stale := e.gen != gen
	e.mu.Unlock()
	if stale {
		return
	}
	sink.BroadcastSessionInfo(map[string]interface{}{
		SessionInfoKeyThinkingTokens: est,
	})
}

// reset zeroes the accumulator at a phase/turn boundary so the next phase's
// first delta restarts near zero instead of re-broadcasting a stale cumulative
// total that would fight the frontend's clear. Most resets are driven centrally
// by thinkingResetSink (every committed main-scope AGENT-source message, a
// broadcast AGENT notification, the turn-end divider, each control-request
// prompt) so a provider can't forget one; the few silent boundaries that commit
// no message yet DO produce a frontend clear of their own (a context clear, a
// provider's turn-start marker) reset explicitly. The reset is in-memory only
// (no broadcast): the frontend already clears on those events. The boundaries
// the frontend can NOT observe on its own -- the ACP assistant->reasoning
// hand-off and a nil-result (aborted) turn end, neither of which persists a
// message -- use clear() instead, which broadcasts the zero itself. reset clears
// `reported` so the next phase re-ships even an unchanged running value, and
// bumps `gen` so an in-flight observe from the ending phase drops its broadcast.
func (e *thinkingTokenEstimator) reset() {
	e.mu.Lock()
	e.chars = 0
	e.reported = 0
	e.gen++
	e.mu.Unlock()
}

// hasPending reports whether streamed text has accumulated in the current phase
// with no intervening reset()/clear(). The ACP hand-off uses it to gate the
// assistant->reasoning clear on the estimator's own state rather than on the
// turn-scoped turnAssistantText builder (which stays non-empty for the whole turn
// to accumulate the reply for end-of-turn persistence): a tool call committed
// between assistant text and a later reasoning segment resets the estimator and
// clears the frontend, so hasPending then reports false and no redundant clear is
// sent. It tracks raw accumulated chars, not the divided estimate, so assistant
// text that has streamed but not yet crossed a whole-token boundary still counts
// as pending.
func (e *thinkingTokenEstimator) hasPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chars > 0
}

// clear resets the accumulator and broadcasts an explicit zero -- which the
// frontend reads as "drop the counter". Unlike reset(), which is silent because
// the frontend already cleared on the triggering event, clear() supplies the
// clear itself, for the phase boundaries the frontend cannot observe on its own:
// the ACP assistant->reasoning hand-off (the assistant text was only buffered,
// never committed, so it triggered no frontend clear) and a nil-result (aborted)
// turn end (no result divider is persisted, so the frontend gets no turn-end
// clear). The zero broadcast goes through ship's generation guard, so clear() is
// safe to drive from a goroutine other than the streaming observe() -- the
// nil-result turn end runs on the ACP prompt-response goroutine while reasoning
// chunks may still arrive on the reader goroutine -- and a later reset()/clear()
// drops this now-stale zero rather than letting it land out of order.
func (e *thinkingTokenEstimator) clear(sink OutputSink) {
	e.mu.Lock()
	e.chars = 0
	e.reported = 0
	e.gen++
	gen := e.gen
	e.mu.Unlock()
	e.ship(sink, 0, gen)
}

// thinkingResetSink wraps an OutputSink and resets a thinkingTokenEstimator at
// exactly the points the frontend clears its thinking-token counter: every
// committed main-scope AGENT-source message (the frontend clears on any main-agent
// AGENT entry it adds to the timeline), every AGENT notification that actually
// reaches the frontend, the turn-end divider, and each control-request prompt.
// Centralizing the reset here keeps the backend estimate in lockstep with the
// frontend without each provider handler having to remember a reset at every
// commit site -- the dominant source of the per-phase counter drifting. The
// streaming/accumulation path is untouched: BroadcastStreamChunk and
// BroadcastSessionInfo (which observe() uses to ship the running estimate) are
// inherited unchanged and never trigger a reset.
type thinkingResetSink struct {
	OutputSink
	est *thinkingTokenEstimator
}

func newThinkingResetSink(inner OutputSink, est *thinkingTokenEstimator) *thinkingResetSink {
	return &thinkingResetSink{OutputSink: inner, est: est}
}

func (s *thinkingResetSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	// Reset only for MAIN-scope AGENT commits. A collab subagent's committed item
	// carries a non-empty ParentSpanID (it nests under the spawning span); resetting
	// on it would zero the primary agent's counter on activity the user attributes
	// to a subagent -- whose streamed text the estimator already excludes via the
	// provider's main-thread gate. SpanInfo is the only thread-derived signal the
	// decorator sees: the originating thread is collapsed into ParentSpanID before
	// reaching the sink. The frontend applies the same ParentSpanID==="" gate, so
	// both sides keep/drop the counter on exactly the same commits.
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && span.ParentSpanID == "" {
		s.est.reset()
	}
	return s.OutputSink.PersistMessage(source, content, span)
}

func (s *thinkingResetSink) PersistNotification(source leapmuxv1.MessageSource, content []byte) (bool, error) {
	broadcast, err := s.OutputSink.PersistNotification(source, content)
	// Reset only when the notification actually reached the frontend. The service
	// layer suppresses the broadcast when a flapping notification collapses
	// byte-identically into the existing thread tail; in that case the frontend
	// never clears, so resetting here would drift the estimate ahead of the display
	// and spin the odometer backward on the next delta. Gating on the broadcast
	// signal keeps this reset in lockstep with the frontend's own clear, which is
	// why the reset moved AFTER the inner call.
	if err == nil && broadcast && source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		s.est.reset()
	}
	return broadcast, err
}

func (s *thinkingResetSink) PersistTurnEnd(content []byte, span SpanInfo) error {
	s.est.reset()
	return s.OutputSink.PersistTurnEnd(content, span)
}

func (s *thinkingResetSink) BroadcastControlRequest(requestID string, payload []byte) {
	s.est.reset()
	s.OutputSink.BroadcastControlRequest(requestID, payload)
}
