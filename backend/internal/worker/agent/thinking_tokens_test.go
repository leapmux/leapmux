package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThinkingTokenEstimator_AccumulatesAndDividesOnce(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	// Sub-ratio deltas must not each truncate to zero: four 1-char deltas total
	// 4 chars -> 1 token (the first three add nothing yet, so they don't ship).
	est.observe(sink, "a")
	est.observe(sink, "b")
	est.observe(sink, "c")
	assert.Equal(t, 0, sink.SessionInfoCount(), "sub-token deltas ship nothing until a whole token accrues")
	est.observe(sink, "d")
	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, int64(1), lastThinkingTokens(sink))

	// Growth is cumulative and monotonic: +8 chars -> 12 chars total -> 3 tokens.
	est.observe(sink, "efghijkl")
	assert.Equal(t, int64(3), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_CountsRunesNotBytes(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	// Eight multibyte runes (each 3 bytes in UTF-8). The estimate must use the
	// rune count (8 -> 2 tokens), not the 24-byte length (which would give 6).
	est.observe(sink, "あいうえおかきく")
	assert.Equal(t, int64(2), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_SuppressesUnchangedEstimate(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	// 8 chars -> 2 tokens (ships). A following 1-char delta is still 2 tokens
	// (9/4), so it must NOT re-broadcast -- the displayed number is unchanged and
	// the key is dedup-exempt, so shipping it would be wasted wire traffic.
	est.observe(sink, "12345678")
	require.Equal(t, 1, sink.SessionInfoCount())
	est.observe(sink, "x")
	assert.Equal(t, 1, sink.SessionInfoCount(), "an unchanged estimate is not re-broadcast")

	// Crossing the next token boundary ships again.
	est.observe(sink, "xyz") // 12 chars -> 3 tokens
	assert.Equal(t, 2, sink.SessionInfoCount())
	assert.Equal(t, int64(3), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_ResetRestartsAndReshipsUnchangedValue(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	est.observe(sink, "12345678") // -> 2, ships
	require.Equal(t, int64(2), lastThinkingTokens(sink))

	est.reset()

	// After a reset the next phase restarts from zero, AND an identical running
	// value must re-ship -- the frontend cleared on the boundary the reset
	// mirrors, so the suppression cache must not hide the restored count.
	est.observe(sink, "12345678") // -> 2 again
	assert.Equal(t, 2, sink.SessionInfoCount(), "the same value re-ships after a reset")
	assert.Equal(t, int64(2), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_EmptyAndNonPositiveDeltasShipNothing(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	est.observe(sink, "")    // empty -> no-op
	est.observe(sink, "abc") // 3 chars -> 0 tokens -> nothing to show yet
	assert.Equal(t, 0, sink.SessionInfoCount())
	assert.Equal(t, int64(-1), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_ShipDropsValueFromEndedPhase(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator

	// accumulate computes the estimate and captures the phase generation it
	// belongs to, under the lock.
	gotEst, gen, ok := est.accumulate("abcdefgh") // 8 chars -> 2 tokens
	require.True(t, ok)
	require.Equal(t, int64(2), gotEst)

	// A reset on another goroutine ends the phase between accumulate and ship --
	// the race the split exists to neutralize. ship must drop the now-stale value
	// (its generation is gone) so the forward-only odometer never ticks backward.
	est.reset()
	est.ship(sink, gotEst, gen)
	assert.Equal(t, 0, sink.SessionInfoCount(), "a broadcast from an ended phase is dropped")

	// A value from the live generation still ships.
	gotEst, gen, ok = est.accumulate("abcd") // post-reset: 4 chars -> 1 token
	require.True(t, ok)
	est.ship(sink, gotEst, gen)
	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, int64(1), lastThinkingTokens(sink))
}

func TestThinkingTokenEstimator_HasPendingTracksUnclearedChars(t *testing.T) {
	sink := &testSink{}
	var est thinkingTokenEstimator
	assert.False(t, est.hasPending(), "a fresh estimator has nothing pending")

	// Sub-token chars (below the divide threshold, so nothing ships yet) still
	// count as pending: hasPending tracks raw accumulated chars, which is what the
	// ACP hand-off needs -- assistant text that has streamed but not yet cleared.
	est.observe(sink, "ab")
	assert.Equal(t, 0, sink.SessionInfoCount(), "sub-token chars ship nothing")
	assert.True(t, est.hasPending(), "but they are pending")

	est.reset()
	assert.False(t, est.hasPending(), "reset drops pending")

	est.observe(sink, "abcd") // 4 chars -> 1 token, ships
	require.Equal(t, int64(1), lastThinkingTokens(sink))
	assert.True(t, est.hasPending())

	est.clear(sink)
	assert.Equal(t, int64(0), lastThinkingTokens(sink), "clear broadcasts an explicit zero")
	assert.False(t, est.hasPending(), "clear drops pending")
}

func TestThinkingResetSink_ResetsOnFrontendClearBoundariesOnly(t *testing.T) {
	const (
		agent   = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		user    = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
		leapmux = leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX
	)
	for _, tc := range []struct {
		name string
		// suppress makes the inner sink report PersistNotification broadcast=false,
		// mimicking the service layer collapsing a flapping notification
		// byte-identically into the existing thread tail (no frontend clear).
		suppress bool
		call     func(s OutputSink)
		reset    bool
	}{
		{name: "AGENT message (main scope) resets", call: func(s OutputSink) { _ = s.PersistMessage(agent, nil, SpanInfo{}) }, reset: true},
		// A subagent's committed item nests under a span (non-empty ParentSpanID);
		// it must not reset the primary agent's counter.
		{name: "AGENT subagent message (nested span) does NOT reset", call: func(s OutputSink) { _ = s.PersistMessage(agent, nil, SpanInfo{ParentSpanID: "collab-span"}) }, reset: false},
		{name: "AGENT notification (broadcast) resets", call: func(s OutputSink) { _, _ = s.PersistNotification(agent, nil) }, reset: true},
		// A collapsed notification produces no broadcast, so the frontend never
		// clears -- the estimate must not reset either.
		{name: "AGENT notification (collapsed, no broadcast) does NOT reset", suppress: true, call: func(s OutputSink) { _, _ = s.PersistNotification(agent, nil) }, reset: false},
		{name: "turn-end divider resets", call: func(s OutputSink) { _ = s.PersistTurnEnd(nil, SpanInfo{}) }, reset: true},
		{name: "control request resets", call: func(s OutputSink) { s.BroadcastControlRequest("id", nil, "") }, reset: true},
		{name: "USER message does NOT reset", call: func(s OutputSink) { _ = s.PersistMessage(user, nil, SpanInfo{}) }, reset: false},
		{name: "LEAPMUX notification does NOT reset", call: func(s OutputSink) { _, _ = s.PersistNotification(leapmux, nil) }, reset: false},
		{name: "stream chunk does NOT reset", call: func(s OutputSink) { s.BroadcastStreamChunk(nil, "", "m") }, reset: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var est thinkingTokenEstimator
			inner := &testSink{notifSuppressBroadcast: tc.suppress}
			sink := newThinkingResetSink(inner, &est)

			est.observe(inner, "abcdefgh") // 8 chars -> 2 tokens
			require.Equal(t, int64(2), lastThinkingTokens(inner))

			tc.call(sink)

			est.observe(inner, "abcd") // +4 chars
			// If the boundary reset, the accumulator restarted: 4 chars -> 1.
			// Otherwise it kept climbing: 12 chars -> 3.
			want := int64(3)
			if tc.reset {
				want = int64(1)
			}
			assert.Equal(t, want, lastThinkingTokens(inner))
		})
	}
}
