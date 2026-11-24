package providerkit

import (
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

	var set ClosedSpanSet

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

func TestToolSpanLedger_OpenDeleteDrainAndDeduplicate(t *testing.T) {
	t.Parallel()

	var ledger ToolSpanLedger[string]
	assert.Nil(t, ledger.DrainAll())
	assert.False(t, ledger.Open("", "Bash", false, "ignored"))
	assert.False(t, ledger.WasClosed(""))
	ledger.Delete("")

	// Open with out-of-order IDs to confirm insertion order, not lexicographical order.
	require.True(t, ledger.Open("z-first", "Bash", false, "v1"))
	assert.False(t, ledger.Open("z-first", "Bash", false, "dup"))
	require.True(t, ledger.Open("a-second", "Read", true, "v2"))
	require.True(t, ledger.Open("m-third", "Edit", false, "v3"))
	require.True(t, ledger.Open("wf-fourth", "Workflow", false, "v4"))

	entry, ok := ledger.Get("a-second")
	require.True(t, ok)
	assert.Equal(t, "v2", entry.Data)
	assert.True(t, entry.NoSpan)
	assert.False(t, ledger.WasClosed("a-second"))

	// Delete removes an open entry without marking it closed.
	ledger.Delete("wf-fourth")
	_, ok = ledger.Get("wf-fourth")
	assert.False(t, ok)
	assert.False(t, ledger.WasClosed("wf-fourth"))

	// DrainMatching removes only matching entries in insertion order.
	matched := ledger.DrainMatching(func(e OpenToolEntry[string]) bool {
		return !e.NoSpan
	})
	require.Len(t, matched, 2)
	assert.Equal(t, "z-first", matched[0].SpanID)
	assert.Equal(t, "m-third", matched[1].SpanID)
	assert.True(t, ledger.WasClosed("z-first"))
	assert.True(t, ledger.WasClosed("m-third"))
	assert.False(t, ledger.WasClosed("a-second"))

	// Late CompleteNative on drained entry reports alreadyClosed.
	_, wasOpen, alreadyClosed := ledger.CompleteNative("z-first")
	assert.False(t, wasOpen)
	assert.True(t, alreadyClosed)

	// Unknown ID reports neither open nor closed.
	_, wasOpen, alreadyClosed = ledger.CompleteNative("never-seen")
	assert.False(t, wasOpen)
	assert.False(t, alreadyClosed)
	assert.False(t, ledger.WasClosed("never-seen"))

	// Native completion on still-open entry removes it without marking it force-closed.
	entry, wasOpen, alreadyClosed = ledger.CompleteNative("a-second")
	assert.True(t, wasOpen)
	assert.False(t, alreadyClosed)
	assert.Equal(t, "v2", entry.Data)
	assert.False(t, ledger.WasClosed("a-second"))

	// Reopening a drained ID clears its closed mark.
	require.True(t, ledger.Open("z-first", "Bash", false, "reused"))
	assert.False(t, ledger.WasClosed("z-first"))
	entry, wasOpen, alreadyClosed = ledger.CompleteNative("z-first")
	assert.True(t, wasOpen)
	assert.False(t, alreadyClosed)
	assert.Equal(t, "reused", entry.Data)

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
	assert.Nil(t, ledger.DrainAll())
	assert.True(t, ledger.WasClosed("a-all"))

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
	CloseToolSpans(sink, entries, agent.MessageCompletionInterrupted, func(e OpenToolEntry[string]) (agent.MessageContent, agent.SpanInfo, bool) {
		if e.Data == "skip" {
			return agent.MessageContent{}, agent.SpanInfo{}, false
		}
		return agent.MessageContent{Original: []byte(`{"id":"` + e.SpanID + `"}`)}, agent.SpanInfo{}, true
	})

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
	assert.Zero(t, sink.resetCount)
	assert.NotPanics(t, func() {
		CloseToolSpans[string](nil, entries, agent.MessageCompletionInterrupted, nil)
	})
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

func TestChildCompletion(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.MessageCompletionComplete, ChildCompletion(bgtask.StatusSucceeded))
	assert.Equal(t, agent.MessageCompletionError, ChildCompletion(bgtask.StatusFailed))
	assert.Equal(t, agent.MessageCompletionInterrupted, ChildCompletion(bgtask.StatusStopped))
	assert.Equal(t, agent.MessageCompletionInterrupted, ChildCompletion(bgtask.StatusInterrupted))
	assert.Equal(t, agent.MessageCompletionFinished, ChildCompletion(bgtask.StatusEndedWithUnknownOutcome))
}
