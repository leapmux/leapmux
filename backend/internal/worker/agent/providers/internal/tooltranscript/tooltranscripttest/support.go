package tooltranscripttest

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolStoreSource is a transcript source that keeps a provider database handle.
//
// A source with no handle of its own declares neither method, and
// ReleaseToolStoreAtTestEnd then fails rather than pass over it in silence.
type toolStoreSource interface {
	// CloseToolStoreForTest releases the handle.
	CloseToolStoreForTest()
	// ToolStoreHandleOpenForTest reports whether a handle is open now.
	ToolStoreHandleOpenForTest() bool
}

// ReleaseToolStoreAtTestEnd closes the transcript's provider database handle when the
// test ends, and closes it SYNCHRONOUSLY.
//
// Production releases that handle from context.AfterFunc, which runs on a goroutine
// of its own once the agent's context ends. A test context ends just BEFORE the
// test's cleanup functions run, so that goroutine races the RemoveAll of t.TempDir.
// Unix unlinks an open file without complaint, so the race is invisible there.
// Windows refuses to remove a file that any handle holds open, and SQLite opens
// every file -- read-only included -- with FILE_SHARE_READ|FILE_SHARE_WRITE and
// never FILE_SHARE_DELETE. The race therefore failed the cleanup of the temporary
// directory on Windows alone.
//
// Call this AFTER t.TempDir, because cleanup functions run in reverse order.
func ReleaseToolStoreAtTestEnd(t *testing.T, transcript *tooltranscript.Transcript) {
	t.Helper()
	source, ok := transcript.SourceForTest().(toolStoreSource)
	require.True(t, ok, "a %T transcript source keeps no provider database handle", transcript.SourceForTest())
	t.Cleanup(source.CloseToolStoreForTest)
}

// AssertReleaseClosesTheToolStore pins that ReleaseToolStoreAtTestEnd closes a
// handle that is really open. open builds a transcript under ctx, calls
// ReleaseToolStoreAtTestEnd on it, and feeds it the messages that make its turn
// end read the provider store.
//
// TestEveryTestClosesTheProviderStoreHandleItOpens reads the call sites and cannot see
// this: a CloseToolStoreForTest wired to the wrong object satisfies that scan and
// closes nothing. The assertion INSIDE the subtest is what keeps this test honest,
// because a handle that never opened would pass the one outside it for the wrong
// reason.
func AssertReleaseClosesTheToolStore(t *testing.T, open func(t *testing.T, ctx context.Context) *tooltranscript.Transcript) {
	t.Helper()
	// A context that THIS test ends, after the assertion below. Each constructor also
	// releases its handle from context.AfterFunc when the agent context ends, and
	// t.Context() ends at the subtest's cleanup -- so a subtest built on t.Context()
	// would assert what that AfterFunc did and never what the helper did. It passed
	// with the close emptied to a no-op, which is how the vacuity was found.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var source toolStoreSource
	// A subtest, because t.Run returns only after the subtest's cleanup functions
	// finish. That is the point in time this test is about.
	t.Run("one turn", func(t *testing.T) {
		transcript := open(t, ctx)
		// The turn end reads the store, which is what opens the handle.
		require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
		isStoreSource := false
		source, isStoreSource = transcript.SourceForTest().(toolStoreSource)
		require.True(t, isStoreSource, "a %T source keeps no handle", transcript.SourceForTest())
		require.True(t, source.ToolStoreHandleOpenForTest(), "the turn end must leave a handle open to close")
	})

	require.NotNil(t, source)
	assert.False(t, source.ToolStoreHandleOpenForTest(),
		"%T still holds its database handle after the test that opened it ended", source)
}
