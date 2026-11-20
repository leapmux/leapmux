package eventjournal

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func openJournalDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "events.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, database.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	return database
}

func startTestJournal(t *testing.T, database *sql.DB, clock quartz.Clock, owner string, retention retention) *Journal {
	t.Helper()
	journal, err := start(t.Context(), database, "worker", userid.MustNew(owner), clock, retention)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		assert.NoError(t, journal.Stop(ctx, false))
	})
	return journal
}

func terminalHistoryTab(id string) Tab {
	return Tab{Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, ID: id}
}

func terminalBellFact() *leapmuxv1.SemanticTabFact {
	return &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}}
}

func terminalTitleFact(title string) *leapmuxv1.SemanticTabFact {
	return &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalTitle{TerminalTitle: &leapmuxv1.TerminalTitleChanged{Title: title}}}
}

func TestJournalReplaysCommittedEventsAndWakesAnIdleReader(t *testing.T) {
	t.Parallel()
	journal := startTestJournal(t, openJournalDatabase(t), quartz.NewReal(), "owner", defaultRetention())
	_, initial, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), nil)
	require.NoError(t, err)
	require.Zero(t, initial.GetAfterSequence())

	journal.Record(terminalHistoryTab("terminal"), terminalBellFact())
	journal.Record(terminalHistoryTab("another-terminal"), terminalTitleFact("other"))
	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact("one"))
	reader, ready, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), initial.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, int64(2), ready.GetHighWaterSequence())
	assert.Equal(t, initial.GetCursor(), ready.GetCursor())
	frames, _, err := reader.Read(t.Context(), initial.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, frames, 2)
	require.NotNil(t, frames[0].GetRecord().GetFact().GetTerminalBell())
	assert.Equal(t, int64(1), frames[0].GetRecord().GetSequence())
	assert.Equal(t, "one", frames[1].GetRecord().GetFact().GetTerminalTitle().GetTitle())

	after := frames[1].GetRecord().GetSequence()
	frames, wake, err := reader.Read(t.Context(), after)
	require.NoError(t, err)
	require.Empty(t, frames)
	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact("two"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-wake:
	case <-ctx.Done():
		t.Fatal("the commit did not wake the reader")
	}
	frames, _, err = reader.Read(ctx, after)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, int64(3), frames[0].GetRecord().GetSequence())
	assert.Equal(t, "two", frames[0].GetRecord().GetFact().GetTerminalTitle().GetTitle())
}

func TestJournalRejectsForeignAndTamperedCursors(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	reader, first, err := journal.Open(t.Context(), terminalHistoryTab("first"), nil)
	require.NoError(t, err)
	_, _, err = journal.Open(t.Context(), terminalHistoryTab("second"), nil)
	require.NoError(t, err)

	tampered := bytes.Clone(first.GetCursor())
	tampered[len(tampered)-1] ^= 1
	for _, test := range []struct {
		tabID  string
		cursor []byte
	}{
		{tabID: "", cursor: first.GetCursor()},
		{tabID: "absent", cursor: first.GetCursor()},
		{tabID: "second", cursor: first.GetCursor()},
		{tabID: "first", cursor: tampered},
		{tabID: "first", cursor: tampered[:3]},
		{tabID: "first", cursor: reader.cursor(-1)},
		{tabID: "first", cursor: reader.cursor(1 << 60)},
	} {
		read, ready, err := journal.Open(t.Context(), terminalHistoryTab(test.tabID), test.cursor)
		assert.ErrorIs(t, err, ErrCursor)
		assert.Nil(t, read)
		assert.Nil(t, ready)
	}

	require.NoError(t, journal.Stop(t.Context(), true))
	reassigned := startTestJournal(t, database, quartz.NewReal(), "different-owner", defaultRetention())
	_, current, err := reassigned.Open(t.Context(), terminalHistoryTab("first"), nil)
	require.NoError(t, err)
	assert.NotEqual(t, first.GetJournalId(), current.GetJournalId())
	_, _, err = reassigned.Open(t.Context(), terminalHistoryTab("first"), first.GetCursor())
	assert.ErrorIs(t, err, ErrCursor)
}

func TestJournalOwnerRejectsZeroDifferentAndRevokedIdentities(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	owner := userid.MustNew("owner")
	journal, err := Start(t.Context(), database, "worker", owner, quartz.NewReal())
	require.NoError(t, err)
	defer func() {
		journal.Revoke()
		assert.ErrorContains(t, journal.Stop(t.Context(), false), "owner changed")
	}()
	assert.True(t, journal.OwnedBy(owner))
	assert.False(t, journal.OwnedBy(userid.UserID{}))
	assert.False(t, journal.OwnedBy(userid.MustNew("another-owner")))
	journal.Revoke()
	assert.False(t, journal.OwnedBy(owner))
}

func TestJournalReportsRetentionAndUnrecordableFacts(t *testing.T) {
	t.Parallel()
	policy := defaultRetention()
	policy.perTab = 2
	journal := startTestJournal(t, openJournalDatabase(t), quartz.NewReal(), "owner", policy)
	_, initial, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), nil)
	require.NoError(t, err)
	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact("pruned"))
	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact(strings.Repeat("x", maxObservationBytes)))
	journal.Record(terminalHistoryTab("terminal"), terminalBellFact())

	reader, ready, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), initial.GetCursor())
	require.NoError(t, err)
	require.Equal(t, int64(3), ready.GetHighWaterSequence())
	page, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	require.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, gap.GetReason())
	require.Equal(t, int64(1), gap.GetResumeAfterSequence())
	page, _, err = reader.Read(t.Context(), gap.GetResumeAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, page[0].GetGap().GetReason())
	assert.Equal(t, int64(2), page[0].GetGap().GetResumeAfterSequence())
	require.NotNil(t, page[1].GetRecord().GetFact().GetTerminalBell())
	assert.Equal(t, int64(3), page[1].GetRecord().GetSequence())

	usage, err := db.New(journal.store.database).GetSemanticEventJournal(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(2), usage.EntryCount)
	assert.Positive(t, usage.PayloadBytes)
}

func TestJournalRecordsOnlyUncleanRestartsAsGaps(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	clock := quartz.NewReal()
	first := startTestJournal(t, database, clock, "owner", defaultRetention())
	_, initial, err := first.Open(t.Context(), terminalHistoryTab("terminal"), nil)
	require.NoError(t, err)
	first.Record(terminalHistoryTab("terminal"), terminalTitleFact("before interruption"))
	require.NoError(t, first.Stop(t.Context(), false))

	second := startTestJournal(t, database, clock, "owner", defaultRetention())
	second.Record(terminalHistoryTab("terminal"), terminalBellFact())
	reader, ready, err := second.Open(t.Context(), terminalHistoryTab("terminal"), initial.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, initial.GetJournalId(), ready.GetJournalId())
	assert.Equal(t, initial.GetTabStreamId(), ready.GetTabStreamId())
	frames, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, frames, 3)
	assert.Equal(t, "before interruption", frames[0].GetRecord().GetFact().GetTerminalTitle().GetTitle())
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP, frames[1].GetGap().GetReason())
	require.NotNil(t, frames[2].GetRecord().GetFact().GetTerminalBell())
	require.NoError(t, second.Stop(t.Context(), true))

	third := startTestJournal(t, database, clock, "owner", defaultRetention())
	reader, _, err = third.Open(t.Context(), terminalHistoryTab("terminal"), frames[2].GetRecord().GetCursor())
	require.NoError(t, err)
	frames, _, err = reader.Read(t.Context(), 3)
	require.NoError(t, err)
	require.Empty(t, frames)
}

func TestJournalReportsExpiredHistoryOnAnIdleStream(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	journal := startTestJournal(t, openJournalDatabase(t), clock, "owner", defaultRetention())
	reader, _, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), nil)
	require.NoError(t, err)
	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact("old"))
	journal.Record(terminalHistoryTab("other"), terminalTitleFact("also old"))
	require.NoError(t, journal.barrier(t.Context()))

	clock.Advance(8 * 24 * time.Hour).MustWait(t.Context())
	page, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, page[0].GetGap().GetReason())
	require.Equal(t, int64(1), page[0].GetGap().GetResumeAfterSequence())

	journal.Record(terminalHistoryTab("terminal"), terminalTitleFact("new"))
	require.NoError(t, journal.barrier(t.Context()))
	frames, _, err := reader.Read(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, "new", frames[0].GetRecord().GetFact().GetTerminalTitle().GetTitle())
}
func TestJournalRetainsAContiguousSuffixUnderTheSharedEntryQuota(t *testing.T) {
	t.Parallel()
	policy := defaultRetention()
	policy.entries = 2
	journal := startTestJournal(t, openJournalDatabase(t), quartz.NewReal(), "owner", policy)
	first, _, err := journal.Open(t.Context(), terminalHistoryTab("first"), nil)
	require.NoError(t, err)
	second, _, err := journal.Open(t.Context(), terminalHistoryTab("second"), nil)
	require.NoError(t, err)

	journal.Record(terminalHistoryTab("first"), terminalTitleFact("pruned first"))
	journal.Record(terminalHistoryTab("second"), terminalTitleFact("pruned second"))
	journal.Record(terminalHistoryTab("first"), terminalTitleFact("kept first"))
	journal.Record(terminalHistoryTab("second"), terminalTitleFact("kept second"))
	require.NoError(t, journal.barrier(t.Context()))
	for _, item := range []struct {
		reader *Reader
		title  string
	}{
		{reader: first, title: "kept first"},
		{reader: second, title: "kept second"},
	} {
		page, _, err := item.reader.Read(t.Context(), 0)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, page[0].GetGap().GetReason())
		require.Equal(t, int64(1), page[0].GetGap().GetResumeAfterSequence())
		page, _, err = item.reader.Read(t.Context(), 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, item.title, page[0].GetRecord().GetFact().GetTerminalTitle().GetTitle())
		assert.Equal(t, int64(2), page[0].GetRecord().GetSequence())
	}
}

func TestJournalQueueOverflowLeavesARecoverableGap(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	clock := quartz.NewReal()
	owner := userid.MustNew("owner")
	journal, err := Start(t.Context(), database, "worker", owner, clock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = journal.Stop(context.Background(), false) })
	reader, checkpoint, err := journal.Open(t.Context(), terminalHistoryTab("terminal"), nil)
	require.NoError(t, err)
	_, wake, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	blocker, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	// While this transaction holds the writer lock, the journal can admit
	// at most one batch plus its waiting queue. A producer must return.
	for range maxPendingObservations + maxCommitBatch + 1 {
		journal.Record(terminalHistoryTab("terminal"), terminalBellFact())
	}
	select {
	case <-wake:
	default:
		t.Fatal("queue saturation did not close the journal to readers")
	}
	_, _, err = reader.Read(t.Context(), 0)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "publication queue is full")
	require.NoError(t, blocker.Rollback())
	require.ErrorContains(t, journal.Stop(t.Context(), false), "publication queue is full")

	recovered := startTestJournal(t, database, clock, "owner", defaultRetention())
	reader, _, err = recovered.Open(t.Context(), terminalHistoryTab("terminal"), checkpoint.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP, page[0].GetGap().GetReason())
}

func TestJournalSeparatesAgentAndTerminalPositionsWithTheSameID(t *testing.T) {
	t.Parallel()
	journal := startTestJournal(t, openJournalDatabase(t), quartz.NewReal(), "owner", defaultRetention())
	terminal := terminalHistoryTab("shared-id")
	agent := Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: terminal.ID}
	_, terminalStart, err := journal.Open(t.Context(), terminal, nil)
	require.NoError(t, err)
	_, agentStart, err := journal.Open(t.Context(), agent, nil)
	require.NoError(t, err)
	assert.NotEqual(t, terminalStart.GetTabStreamId(), agentStart.GetTabStreamId())

	journal.Record(terminal, terminalBellFact())
	journal.Record(agent, &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_AgentTurnEnd{AgentTurnEnd: &leapmuxv1.AgentTurnEnd{}}})
	_, _, err = journal.Open(t.Context(), agent, terminalStart.GetCursor())
	assert.ErrorIs(t, err, ErrCursor)
	reader, ready, err := journal.Open(t.Context(), agent, agentStart.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, ready.GetTab().GetTabType())
	assert.Equal(t, int64(1), ready.GetHighWaterSequence())
	page, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.NotNil(t, page[0].GetRecord().GetFact().GetAgentTurnEnd())
	assert.Nil(t, page[0].GetRecord().GetFact().GetTerminalBell())
}
