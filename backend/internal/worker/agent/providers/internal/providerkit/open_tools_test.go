package providerkit

import (
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeToolLifecycleSink struct {
	agent.TranscriptServices
	persisted   []persistedToolRow
	closedSpans []string
	progress    []agent.ProgressUpdate
	resetCount  int
	events      []string
	persistErr  error
}

type persistedToolRow struct {
	source  leapmuxv1.MessageSource
	content agent.MessageContent
	span    agent.SpanInfo
}

func (f *fakeToolLifecycleSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	f.persisted = append(f.persisted, persistedToolRow{source: source, content: content, span: span})
	f.events = append(f.events, "persist:"+span.SpanID)
	return f.persistErr
}

func (f *fakeToolLifecycleSink) PersistTurnEnd(agent.MessageContent, agent.SpanInfo) error {
	return nil
}
func (f *fakeToolLifecycleSink) PersistNotification(leapmuxv1.MessageSource, agent.MessageContent) (bool, error) {
	return false, nil
}
func (f *fakeToolLifecycleSink) PersistLeapMuxNotification(map[string]interface{}) {}
func (f *fakeToolLifecycleSink) CaptureMessage(content agent.MessageContent, _ agent.SpanInfo) agent.MessageContent {
	return content
}
func (f *fakeToolLifecycleSink) ReportProgress(update agent.ProgressUpdate) {
	f.progress = append(f.progress, update)
	f.events = append(f.events, fmt.Sprintf("progress:%d:%s", update.Operation, update.ScopeID))
}
func (f *fakeToolLifecycleSink) OpenSpan(string, string) {}
func (f *fakeToolLifecycleSink) CloseSpan(spanID string) {
	f.closedSpans = append(f.closedSpans, spanID)
	f.events = append(f.events, "close:"+spanID)
}
func (f *fakeToolLifecycleSink) ResetSpans() {
	f.resetCount++
	f.events = append(f.events, "reset_spans")
}
func (f *fakeToolLifecycleSink) SetSpanType(string, string)            {}
func (f *fakeToolLifecycleSink) GetSpanType(string) string             { return "" }
func (f *fakeToolLifecycleSink) ReserveSpanColor(string, string) int32 { return 1 }

func TestClosedSpanSet_CapReopenAndReset(t *testing.T) {
	t.Parallel()

	set := NewClosedSpanSet(0)
	assert.Equal(t, defaultClosedSpanCap, set.effectiveLimit())

	for i := 0; i < defaultClosedSpanCap+50; i++ {
		set.MarkClosed(fmt.Sprintf("span-%d", i))
	}
	assert.False(t, set.WasClosed("span-0"), "oldest closed ID past cap must evict")
	assert.False(t, set.WasClosed("span-49"), "50th oldest ID past cap must evict")
	assert.True(t, set.WasClosed("span-50"), "newest defaultClosedSpanCap IDs must survive")
	assert.True(t, set.WasClosed(fmt.Sprintf("span-%d", defaultClosedSpanCap+49)))

	// Reopen removes from closed set and allows later re-closing without stale eviction.
	set.Reopen("span-50")
	assert.False(t, set.WasClosed("span-50"))
	set.MarkClosed("span-50")
	assert.True(t, set.WasClosed("span-50"))

	// Reset clears all entries.
	set.Reset()
	assert.False(t, set.WasClosed("span-50"))
	assert.False(t, set.WasClosed(fmt.Sprintf("span-%d", defaultClosedSpanCap+49)))
}

func TestClosedSpanSet_ZeroValueAndEmptyID(t *testing.T) {
	t.Parallel()

	var set ClosedSpanSet
	set.MarkClosed("")
	assert.False(t, set.WasClosed(""))
	set.Reopen("")

	set.MarkClosed("a")
	set.MarkClosed("a")
	assert.True(t, set.WasClosed("a"))
	assert.Len(t, set.order, 1)
}

func TestToolSpanLedger_OpenUpdateDrainAndDeduplicate(t *testing.T) {
	t.Parallel()

	var ledger ToolSpanLedger[string]
	assert.False(t, ledger.HasOpen())
	assert.Nil(t, ledger.DrainAll())
	assert.False(t, ledger.Open("", "Bash", false, "ignored"))

	// Open with out-of-order IDs to confirm insertion order, not lexicographical order.
	require.True(t, ledger.Open("z-first", "Bash", false, "v1"))
	assert.False(t, ledger.Open("z-first", "Bash", false, "dup"))
	require.True(t, ledger.Open("a-second", "Read", true, "v2"))
	require.True(t, ledger.Open("m-third", "Edit", false, "v3"))
	assert.True(t, ledger.HasOpen())

	entry, ok := ledger.Get("a-second")
	require.True(t, ok)
	assert.Equal(t, "v2", entry.Data)
	assert.True(t, entry.NoSpan)

	require.True(t, ledger.Update("a-second", func(d *string) { *d = "v2-updated" }))
	assert.False(t, ledger.Update("missing", func(d *string) { *d = "nope" }))
	entry, ok = ledger.Get("a-second")
	require.True(t, ok)
	assert.Equal(t, "v2-updated", entry.Data)

	// DrainMatching removes only matching entries in insertion order.
	matched := ledger.DrainMatching(func(e OpenToolEntry[string]) bool {
		return !e.NoSpan
	})
	require.Len(t, matched, 2)
	assert.Equal(t, "z-first", matched[0].SpanID)
	assert.Equal(t, "m-third", matched[1].SpanID)
	assert.True(t, ledger.HasOpen())

	// Late CompleteNative on drained entry reports alreadyClosed.
	_, wasOpen, alreadyClosed := ledger.CompleteNative("z-first")
	assert.False(t, wasOpen)
	assert.True(t, alreadyClosed)

	// Unknown ID reports neither open nor closed.
	_, wasOpen, alreadyClosed = ledger.CompleteNative("never-seen")
	assert.False(t, wasOpen)
	assert.False(t, alreadyClosed)

	// Native completion on still-open entry removes it.
	entry, wasOpen, alreadyClosed = ledger.CompleteNative("a-second")
	assert.True(t, wasOpen)
	assert.False(t, alreadyClosed)
	assert.Equal(t, "v2-updated", entry.Data)
	assert.False(t, ledger.HasOpen())

	// Reopening a drained ID clears its closed mark.
	require.True(t, ledger.Open("z-first", "Bash", false, "reused"))
	entry, wasOpen, alreadyClosed = ledger.CompleteNative("z-first")
	assert.True(t, wasOpen)
	assert.False(t, alreadyClosed)
	assert.Equal(t, "reused", entry.Data)

	// MarkNativeClosed suppresses subsequent CompleteNative even without Open.
	ledger.MarkNativeClosed("dup-final")
	_, wasOpen, alreadyClosed = ledger.CompleteNative("dup-final")
	assert.False(t, wasOpen)
	assert.True(t, alreadyClosed)

	// Reset clears open and closed state.
	require.True(t, ledger.Open("after", "Bash", false, "v"))
	ledger.Reset()
	assert.False(t, ledger.HasOpen())
	_, wasOpen, alreadyClosed = ledger.CompleteNative("dup-final")
	assert.False(t, wasOpen)
	assert.False(t, alreadyClosed)

	// DrainAll removes all open entries in deterministic (Order, SpanID) order
	// and records each drained ID in the closed set.
	require.True(t, ledger.Open("z-all", "Bash", false, "a1"))
	require.True(t, ledger.Open("a-all", "Read", true, "a2"))
	require.True(t, ledger.Open("m-all", "Edit", false, "a3"))
	drained := ledger.DrainAll()
	require.Len(t, drained, 3)
	assert.Equal(t, []string{"z-all", "a-all", "m-all"}, []string{drained[0].SpanID, drained[1].SpanID, drained[2].SpanID})
	assert.Less(t, drained[0].Order, drained[1].Order)
	assert.Less(t, drained[1].Order, drained[2].Order)
	assert.False(t, ledger.HasOpen())
	assert.Nil(t, ledger.DrainAll())

	got, wasOpen, alreadyClosed := ledger.CompleteNative("a-all")
	assert.Equal(t, OpenToolEntry[string]{}, got)
	assert.False(t, wasOpen)
	assert.True(t, alreadyClosed)
}

func TestCloseToolSpans_OrderNoSpanBuildFalseAndPersistError(t *testing.T) {
	t.Parallel()

	entries := []OpenToolEntry[string]{
		{SpanID: "s1", SpanType: "Bash", Order: 1, NoSpan: false, Data: "emit"},
		{SpanID: "s2", SpanType: "Agent", Order: 2, NoSpan: true, Data: "emit"},
		{SpanID: "s3", SpanType: "Read", Order: 3, NoSpan: false, Data: "skip"},
	}

	sink := &fakeToolLifecycleSink{persistErr: errors.New("write failed")}
	closed := CloseToolSpans(sink, entries, agent.MessageCompletionInterrupted, func(e OpenToolEntry[string]) (agent.MessageContent, agent.SpanInfo, bool) {
		if e.Data == "skip" {
			return agent.MessageContent{}, agent.SpanInfo{}, false
		}
		return agent.MessageContent{Original: []byte(`{"id":"` + e.SpanID + `"}`)}, agent.SpanInfo{}, true
	}, true)

	assert.Equal(t, 3, closed)
	require.Len(t, sink.persisted, 2)
	assert.Equal(t, "s1", sink.persisted[0].span.SpanID)
	assert.Equal(t, "Bash", sink.persisted[0].span.SpanType)
	assert.True(t, sink.persisted[0].span.Closing)
	assert.False(t, sink.persisted[0].span.NoSpan)
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.persisted[0].content.Completion)

	assert.Equal(t, "s2", sink.persisted[1].span.SpanID)
	assert.True(t, sink.persisted[1].span.NoSpan)
	assert.True(t, sink.persisted[1].span.Closing)

	// NoSpan entry skips CloseSpan; PersistMessage error and ok == false still close span and progress.
	assert.Equal(t, []string{"s1", "s3"}, sink.closedSpans)
	assert.Equal(t, []agent.ProgressUpdate{
		agent.CompleteOutputProgress("s1"),
		agent.CompleteOutputProgress("s2"),
		agent.CompleteOutputProgress("s3"),
	}, sink.progress)
	assert.Equal(t, 1, sink.resetCount)
	assert.Equal(t, 0, CloseToolSpans[string](nil, entries, agent.MessageCompletionInterrupted, nil, true))
}

func TestResetSpansAndProgress(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		ResetSpansAndProgress(nil)
	})

	sink := &fakeToolLifecycleSink{}
	ResetSpansAndProgress(sink)
	assert.Equal(t, 1, sink.resetCount)
	assert.Equal(t, []agent.ProgressUpdate{agent.ResetProgress()}, sink.progress)
	assert.Equal(t, []string{"reset_spans", fmt.Sprintf("progress:%d:", agent.ProgressReset)}, sink.events)
}
