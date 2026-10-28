package fastagent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootJournalPreservesOpaqueBytesInPrivateFiles(t *testing.T) {
	journal, err := newRootJournal(t.TempDir(), "native-session", "owner", "capture")
	require.NoError(t, err)
	record := &rootRecord{kind: rootRecordMessage, source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: agent.MessageContent{
		Original: []byte{0, 1, 2, 255}, Supplemental: []byte(`{"source":0}`), Metadata: []byte(`{"tool_uses":0}`),
		AgentSessionID: "native-session", IdempotencyKey: "original-key", Completion: agent.MessageCompletionError, WriteReceipt: agent.NewTranscriptWriteReceipt(),
	}, span: agent.SpanInfo{SpanID: "tool", SpanType: "execute", Closing: true, NoSpan: true}}
	name, err := journal.Write(record)
	require.NoError(t, err)
	read, err := journal.Read(name)
	require.NoError(t, err)
	assert.Equal(t, record.content.Original, read.content.Original)
	assert.Equal(t, record.content.Supplemental, read.content.Supplemental)
	assert.Equal(t, record.content.Metadata, read.content.Metadata)
	assert.Equal(t, record.content.AgentSessionID, read.content.AgentSessionID)
	assert.Equal(t, record.content.IdempotencyKey, read.content.IdempotencyKey)
	assert.Equal(t, record.content.Completion, read.content.Completion)
	assert.Equal(t, record.span, read.span)
	assert.Nil(t, read.content.WriteReceipt, "write receipts remain process-owned")
	if runtime.GOOS != "windows" {
		// Windows file mode bits do not describe the directory's access-control list.
		for _, path := range []string{journal.Path(), filepath.Join(journal.Path(), name), filepath.Join(journal.Path(), "manifest.json")} {
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Zero(t, info.Mode().Perm()&0o077)
		}
	}
	require.NoError(t, journal.Retain())
	require.FileExists(t, filepath.Join(journal.Path(), name))
	require.NoError(t, os.RemoveAll(journal.Path()))
}

func TestRootJournalConcurrentRecordsAndRetryableCleanup(t *testing.T) {
	journal, err := newRootJournal(t.TempDir(), "native-session", "owner", "capture")
	require.NoError(t, err)
	const count = 20
	names := make(chan string, count)
	failed := make(chan error, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Go(func() {
			name, err := journal.Write(&rootRecord{kind: rootRecordMessage, source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: agent.MessageContent{Original: []byte(`{"native":true}`)}})
			if err != nil {
				failed <- err
			} else {
				names <- name
			}
		})
	}
	workers.Wait()
	close(names)
	close(failed)
	for err := range failed {
		require.NoError(t, err)
	}
	seen := make(map[string]bool)
	for name := range names {
		assert.False(t, seen[name])
		seen[name] = true
		record, err := journal.Read(name)
		require.NoError(t, err)
		assert.JSONEq(t, `{"native":true}`, string(record.content.Original))
	}
	assert.Len(t, seen, count)
	unowned := filepath.Join(journal.Path(), "unexpected-directory")
	require.NoError(t, os.Mkdir(unowned, 0o700))
	require.Error(t, journal.Cleanup())
	require.NoError(t, os.Remove(unowned))
	require.NoError(t, journal.Cleanup())
	require.NoError(t, journal.Cleanup())
	_, err = os.Stat(journal.Path())
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRootJournalRefusesInvalidAndOversizedRecoveryRecords(t *testing.T) {
	journal, err := newRootJournal(t.TempDir(), "native-session", "owner", "capture")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, journal.Cleanup()) })
	for _, raw := range []string{`{`, `[]`, `{} {}`, `{"Unknown":true}`, `{"Kind":99,"Source":1}`, `{"Kind":0,"Source":0}`} {
		name, err := journal.Write(&rootRecord{kind: rootRecordMessage, source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(journal.Path(), name), []byte(raw), 0o600))
		_, err = journal.Read(name)
		require.Error(t, err, raw)
	}
	name, err := journal.Write(&rootRecord{kind: rootRecordMessage, source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT})
	require.NoError(t, err)
	file, err := os.OpenFile(filepath.Join(journal.Path(), name), os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(rootJournalEncodedByteLimit+1))
	require.NoError(t, file.Close())
	_, err = journal.Read(name)
	require.Error(t, err)
	_, err = journal.Read("../outside.json")
	require.Error(t, err)
}

type refusedRootJournal struct {
	rootJournalStore
	refuse bool
}

func (journal *refusedRootJournal) Write(record *rootRecord) (string, error) {
	if journal.refuse {
		return "", errors.New("the private output storage is unavailable")
	}
	return journal.rootJournalStore.Write(record)
}

func TestFastagentJournalFailureStopsProductionAndRetainsAcceptedRows(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	journal := &refusedRootJournal{rootJournalStore: root.group.journal}
	root.group.journal = journal
	appendRootFixture(t, root, `{"live":"durable first row"}`)
	journal.refuse = true
	appendRootFixture(t, root, `{"live":"accepted failed-storage row"}`)
	position := root.Reserve()
	err := position.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"live":"refused further production"}`)}, agent.SpanInfo{})
	require.ErrorContains(t, err, "storage is unavailable")
	position.Complete()
	testutil.RequireEventually(t, a.IsStopped)
	a.SimulateExitForTest()
	finishRootFixture(t, a, root)
	a.rootFailureWork.CloseAndWait()
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), "durable first row")
	assert.Contains(t, string(rows[1].Content), "accepted failed-storage row")
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, "agent_error", sink.LeapMuxNotifications()[0]["type"])
	assert.DirExists(t, journal.Path())
	assert.FileExists(t, filepath.Join(journal.Path(), "manifest.json"))
	require.NoError(t, os.RemoveAll(journal.Path()))
}

func TestFastagentRecordLimitStopsProductionWithoutLosingAcceptedRows(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	root.group.maxRecords = 2
	appendRootFixture(t, root, `{"live":"first"}`)
	appendRootFixture(t, root, `{"live":"second"}`)
	position := root.Reserve()
	err := position.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"live":"over the limit"}`)}, agent.SpanInfo{})
	require.ErrorContains(t, err, "record limit")
	position.Complete()
	testutil.RequireEventually(t, a.IsStopped)
	a.SimulateExitForTest()
	finishRootFixture(t, a, root)
	a.rootFailureWork.CloseAndWait()
	require.Len(t, sink.Messages(), 2)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, "agent_error", sink.LeapMuxNotifications()[0]["type"])
	require.NoError(t, os.RemoveAll(root.group.journal.Path()))
}
