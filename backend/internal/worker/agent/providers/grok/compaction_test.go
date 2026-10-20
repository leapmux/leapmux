package grok

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactionNotification builds one auto-compaction session notification line.
func compactionNotification(t *testing.T, update map[string]any) []byte {
	t.Helper()
	return notification(t, grokTestSession, update)
}

// The start of an automatic compaction is live state only: no row, one live
// broadcast with its trigger.
func TestGrokCompactionStartIsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(compactionNotification(t, map[string]any{
		"sessionUpdate": "auto_compact_started", "percent_of_window": 81,
	}))

	assert.Empty(t, sink.PersistedNotifications(), "the start stores no row")
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

// Each of the three end frames stores its native frame, ends the live state,
// and classifies as a compaction boundary.
func TestGrokCompactionEndStoresTheEndAndEndsLiveState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame map[string]any
	}{
		{"completed", map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_before": 120000, "tokens_after": 30000}},
		{"failed", map[string]any{"sessionUpdate": "auto_compact_failed", "error": "the provider refused the summary"}},
		{"cancelled", map[string]any{"sessionUpdate": "auto_compact_cancelled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.SetPromptActiveForTest(true)

			a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
			// Buffered text before the end keeps its own row above the boundary.
			a.HandleOutput(chunk(t, "before the end"))
			a.HandleOutput(compactionNotification(t, tc.frame))
			a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1, "one native end row")
			assert.Contains(t, string(rows[0].Content), tc.frame["sessionUpdate"].(string))
			assert.Equal(t, agent.CompactionBoundaryNotification(), grokProvider{}.Classify(rows[0].Content))
			// The third value is the prompt end's own clear.
			assert.Equal(t, []interface{}{
				map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
				nil,
				nil,
			}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			texts := assembledTexts(t, sink)
			require.Len(t, texts, 1)
			assert.Equal(t, "before the end", texts[0])
		})
	}
}

// A cancelled compaction ends a live start even though the 1.0.62 worker knew
// no case for it: without the case the start row span forever.
func TestGrokCancelledCompactionEndsTheStart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
	a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_cancelled"}))

	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
		nil,
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	require.Len(t, sink.PersistedNotifications(), 1)
}

// The prompt end ends a live compaction that no native end frame reached, as a
// stop during a compaction does.
func TestGrokPromptEndEndsALiveCompaction(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "stopped"}[stopped], func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
			a.HooksForTest().PromptEnded(nil, stopped)
			assert.Equal(t, []interface{}{
				map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
				nil,
			}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
		})
	}
}

// A compaction of a child session is the child's own: it never reaches the
// parent's live state.
func TestGrokChildCompactionChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.AttachChildSession("child-session", "call_spawn")

	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_after": 100}))

	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Empty(t, sink.PersistedNotifications())
}

func TestGrokClassifyEveryCompactionEndIsABoundary(t *testing.T) {
	t.Parallel()
	for _, word := range []string{
		contracts.GrokNotificationAutoCompactCompleted,
		contracts.GrokNotificationAutoCompactFailed,
		contracts.GrokNotificationAutoCompactCancelled,
	} {
		raw := notification(t, grokTestSession, map[string]any{"sessionUpdate": word})
		assert.Equal(t, agent.CompactionBoundaryNotification(), grokProvider{}.Classify(raw), word)
	}
	assert.Equal(t, agent.NotificationClassification{}, grokProvider{}.Classify(
		notification(t, grokTestSession, map[string]any{"sessionUpdate": "auto_compact_started"})))
}
