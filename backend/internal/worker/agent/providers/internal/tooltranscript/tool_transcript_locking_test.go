package tooltranscript

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The transcript makes TWO database round trips for each supplement it writes, and mu
// guards neither. The reader goroutine drains the provider's stdout and must never
// wait for a store query, because the wait becomes back-pressure on the provider's
// pipe. These tests reach the point INSIDE one write and ask what mu holds there.

// enrichHookSink runs one hook on the FIRST EnrichMessage and then delegates.
//
// The guard is an atomic flag and NOT sync.Once. Do blocks a second caller until the
// first one returns, and the hooks below write through this same sink -- so the write
// that the hook waits for would wait for the hook, and neither would ever end.
type enrichHookSink struct {
	*agenttest.Sink
	fired atomic.Bool
	hook  func()
}

func (s *enrichHookSink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if s.hook != nil && s.fired.CompareAndSwap(false, true) {
		s.hook()
	}
	return s.Sink.EnrichMessage(change)
}

// newHookedToolTranscript builds a transcript whose store pass answers one record for
// the tool call "call", and whose first write runs the caller's hook.
func newHookedToolTranscript(t *testing.T, record []byte) (*Transcript, *enrichHookSink) {
	t.Helper()
	sink := &enrichHookSink{Sink: &agenttest.Sink{}}
	source := &testToolSource{
		locateHook: func(sessionID string) Location {
			return Location{SessionKey: sessionID, Ready: true}
		},
		readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
			if len(pending) == 0 {
				return nil
			}
			return map[string][]byte{"call": record}
		},
	}
	return New(t.Context(), agent.NewProviderServices(sink), source), sink
}

// runWhileTheWriteIsInFlight runs work on another goroutine and waits for it.
//
// The caller sits inside the transcript's own store write. Work that takes mu returns
// only while the writer holds nothing, so the timeout reports a lock held across the
// write as a failure rather than as a test that never ends.
func runWhileTheWriteIsInFlight(t *testing.T, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		work()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the transcript held mu across its own store write")
	}
}

// persistClosingToolRow persists the row that CLOSES the tool call "call".
func persistClosingToolRow(t *testing.T, transcript *Transcript, original []byte) {
	t.Helper()
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", Closing: true}))
}

// The store pass writes one record at a time and holds mu for neither write.
//
// A turn with N pending rows serialized N round trips under ONE acquisition, so every
// frame the reader goroutine had waited for all of them. The out-of-band enrichment
// below is the observable: it takes mu itself, from inside the pass's own write.
func TestSupplementPassReleasesTheLockAroundItsWrite(t *testing.T) {
	t.Parallel()
	original := []byte(`{"toolCallId":"call"}`)
	transcript, sink := newHookedToolTranscript(t, []byte(`{"store":"record"}`))
	outOfBand := make(chan bool, 1)
	sink.hook = func() {
		runWhileTheWriteIsInFlight(t, func() {
			written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) {
				return []byte(`{"frame":"out-of-band"}`), nil
			})
			assert.NoError(t, err)
			outOfBand <- written
		})
	}

	persistClosingToolRow(t, transcript, original)
	// PersistMessage adds the entry AFTER it asks for a pass, so this is what starts
	// one for the row above.
	transcript.UpdateSessionID("session-1")

	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest(),
		"a pass whose write the row refused keeps its entry for the next pass")
	assert.True(t, <-outOfBand, "the out-of-band write reaches the row")
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sink.Messages()[0].SupplementalContent, &supplement))
	assert.Contains(t, supplement, "frame")
	assert.NotContains(t, supplement, "store", "the refused pass writes nothing")
	transcript.mu.Lock()
	entry := transcript.pending["call"]
	transcript.mu.Unlock()
	assert.Equal(t, int64(1), entry.revision, "the out-of-band write leaves its revision on the entry")
}

// A pass must not drop an entry that was REPLACED while its write ran.
//
// mu is free during that write, so the reader goroutine can persist a second closing
// row for the same tool call. That row enters `pending` at revision 0, which is the
// revision the first entry started at, so a compare on the revision alone takes the
// replacement for the entry the pass read -- and deletes it, with the supplement the
// next pass still owes it. The entry's epoch is what tells the two apart.
func TestSupplementPassKeepsAnEntryReplacedDuringItsWrite(t *testing.T) {
	t.Parallel()
	original := []byte(`{"toolCallId":"call"}`)
	transcript, sink := newHookedToolTranscript(t, []byte(`{"store":"record"}`))
	sink.hook = func() {
		runWhileTheWriteIsInFlight(t, func() {
			assert.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", Closing: true}))
		})
	}

	persistClosingToolRow(t, transcript, original)
	transcript.UpdateSessionID("session-1")

	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest(),
		"the replacement row still waits for a supplement of its own")
	transcript.mu.Lock()
	entry := transcript.pending["call"]
	transcript.mu.Unlock()
	assert.Equal(t, uint64(2), entry.epoch, "the entry that waits is the replacement, not the one the pass read")
}

// An out-of-band write that a racing revision bump overtakes is a NO-OP, not a
// clobber.
//
// EnrichToolSpan releases mu for its own two round trips, so the row can move between
// the read and the write. EnrichMessage refuses a write whose PreviousRevision no
// longer matches the row, and the bookkeeping must then stay exactly as the winner
// left it: a write-back here would state a revision the row does not carry, and the
// next store pass would be refused for the rest of the turn.
func TestEnrichToolSpanRefusesAWriteARacingRevisionOvertook(t *testing.T) {
	t.Parallel()
	original := []byte(`{"toolCallId":"call"}`)
	transcript, sink := newHookedToolTranscript(t, nil)
	sink.hook = func() {
		// The store pass, which reached the row first. It writes through the base
		// sink, so the row's revision moves under the call that is in flight.
		written, err := sink.Sink.EnrichMessage(agent.MessageEnrichment{
			SpanID: "call", OriginalContent: original, PreviousRevision: 0,
			SupplementalContent: []byte(`{"store":"record"}`),
		})
		assert.NoError(t, err)
		assert.True(t, written, "the racing write must land, or this test proves nothing")
	}

	persistClosingToolRow(t, transcript, original)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) {
		return []byte(`{"frame":"out-of-band"}`), nil
	})

	require.NoError(t, err)
	assert.False(t, written, "the row refused the write")
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sink.Messages()[0].SupplementalContent, &supplement))
	assert.Equal(t, map[string]json.RawMessage{"store": json.RawMessage(`"record"`)}, supplement,
		"the winner's supplement stays whole")
	transcript.mu.Lock()
	entry := transcript.pending["call"]
	transcript.mu.Unlock()
	assert.Equal(t, int64(0), entry.revision, "a refused write leaves the bookkeeping alone")
	assert.Empty(t, entry.content.Supplemental)
}
