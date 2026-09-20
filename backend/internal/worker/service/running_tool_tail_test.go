package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A closed span keeps no live tail, whatever the provider reported.
//
// dropTailsLocked reaches only a provider that reports an output completion.
// Copilot reports none, so its entries lived until the turn reset the whole map
// and the worker went on re-broadcasting the tail of a call whose result the
// browser already drew. CloseSpan is the first of the two events that now drop it.
func TestAgentOutputSinkDropsATailWhenTheSpanCloses(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	publisher := svc.Output.sinkForAgent("root-1").progress

	sink.ReportProgress(agent.OutputTailProgress("span-1", "still running", false))
	sink.ReportProgress(agent.OutputTailProgress("span-2", "also running", false))
	require.Equal(t, 2, tailCount(publisher))

	sink.CloseSpan("span-1")

	assert.False(t, hasTail(publisher, "span-1"), "a closed span keeps no tail")
	assert.True(t, hasTail(publisher, "span-2"), "the other call is still running")
}

// The closing ROW drops the tail too, because one provider writes that row and
// never closes the span.
//
// Copilot's spawn path persists the result with Closing set and then skips
// CloseSpan, so the row is the only event it produces for a call that ended. Both
// hooks are needed, and together they cover every provider without one of them
// having to remember.
func TestAgentOutputSinkDropsATailWhenAClosingRowLands(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	publisher := svc.Output.sinkForAgent("root-1").progress

	sink.ReportProgress(agent.OutputTailProgress("span-1", "still running", false))
	require.True(t, hasTail(publisher, "span-1"))

	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"tool_result"}`)},
		agent.SpanInfo{SpanID: "span-1", SpanType: "shell", Closing: true},
	))

	assert.False(t, hasTail(publisher, "span-1"), "the closing row ends the call's live tail")
}

// A row that opens or continues a call keeps the tail, which is the whole point of
// having one.
func TestAgentOutputSinkKeepsATailForARowThatDoesNotClose(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	publisher := svc.Output.sinkForAgent("root-1").progress

	sink.ReportProgress(agent.OutputTailProgress("span-1", "still running", false))
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"tool_use"}`)},
		agent.SpanInfo{SpanID: "span-1", SpanType: "shell"},
	))

	assert.True(t, hasTail(publisher, "span-1"))
}

// The payload states the session the tail was CAPTURED in, not the one that is
// current when the flush sends it.
//
// The two differ by up to a whole tail interval, and ClearContext inside that
// window mints a new session. The browser keys its live entry by the span and the
// session together, so a tail stamped with the replacement reached an entry the
// call it describes never had.
func TestGenerationProgressPublisherStampsATailWithTheSessionOfItsCapture(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	current := "session-1"
	updates := make(chan map[string]interface{}, 2)
	publisher := newGenerationProgressPublisher(func(info map[string]interface{}) {
		updates <- info
	}, func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	})
	// A window no test waits through: flushTails runs below, on this goroutine, so
	// the armed timer never fires.
	publisher.tailInterval = time.Hour
	t.Cleanup(publisher.close)

	publisher.report(agent.OutputTailProgress("call-a", "partial", false))
	mu.Lock()
	current = "session-2"
	mu.Unlock()
	publisher.flushTails()

	running := awaitRunningTool(t, updates)
	assert.Equal(t, "session-1", running[contracts.RunningToolFieldAgentSessionId],
		"the tail belongs to the session that produced it")
	assert.Equal(t, "partial", running[contracts.RunningToolFieldOutputTail])
}

// A span id is unique inside ONE session, and the tail map is keyed by that id
// alone. The entry a dead session left must not stamp the new session's tail with
// the dead session's id -- and the dedup makes that the case for IDENTICAL text,
// which is exactly what a command that prints the same first line produces.
func TestGenerationProgressPublisherReplacesATailLeftByAnEndedSession(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	current := "session-1"
	updates := make(chan map[string]interface{}, 2)
	publisher := newGenerationProgressPublisher(func(info map[string]interface{}) {
		updates <- info
	}, func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	})
	publisher.tailInterval = time.Hour
	t.Cleanup(publisher.close)

	publisher.report(agent.OutputTailProgress("call-a", "building", false))
	mu.Lock()
	current = "session-2"
	mu.Unlock()
	publisher.report(agent.OutputTailProgress("call-a", "building", false))
	publisher.flushTails()

	running := awaitRunningTool(t, updates)
	assert.Equal(t, "session-2", running[contracts.RunningToolFieldAgentSessionId])
	assert.Equal(t, "building", running[contracts.RunningToolFieldOutputTail])
}

// dropTail answers for a span the publisher never saw, and for an empty id.
func TestGenerationProgressPublisherDropTailAcceptsAnUnknownSpan(t *testing.T) {
	t.Parallel()

	publisher := newGenerationProgressPublisher(func(map[string]interface{}) {}, func() string { return "session-1" })
	t.Cleanup(publisher.close)

	assert.NotPanics(t, func() { publisher.dropTail("never-seen") })
	assert.NotPanics(t, func() { publisher.dropTail("") })

	publisher.report(agent.OutputTailProgress("call-a", "partial", false))
	publisher.dropTail("")
	assert.True(t, hasTail(publisher, "call-a"), "an empty span id drops nothing")
	publisher.dropTail("call-a")
	assert.False(t, hasTail(publisher, "call-a"))
}

func awaitRunningTool(t *testing.T, updates <-chan map[string]interface{}) map[string]interface{} {
	t.Helper()
	select {
	case info := <-updates:
		running, ok := info[contracts.SessionInfoKeyRunningTool].(map[string]interface{})
		require.True(t, ok, "the tail rides the running_tool key")
		return running
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the running-tool payload")
		return nil
	}
}

func hasTail(publisher *generationProgressPublisher, spanID string) bool {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	_, present := publisher.tails[spanID]
	return present
}

func tailCount(publisher *generationProgressPublisher) int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return len(publisher.tails)
}
