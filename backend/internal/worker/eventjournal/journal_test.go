package eventjournal

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
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

func openJournalDatabase(t *testing.T, tabs ...Tab) *sql.DB {
	t.Helper()
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "events.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, database.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	queries := db.New(database)
	for _, tab := range tabs {
		require.True(t, tab.Valid())
		if tab.Type == leapmuxv1.TabType_TAB_TYPE_AGENT {
			require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: tab.ID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			}))
		} else {
			require.NoError(t, queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
				ID: tab.ID, WorkingDir: t.TempDir(), Cols: 80, Rows: 24, Screen: []byte{},
			}))
		}
	}
	return database
}

func TestLateClosedTerminalEventsCannotEvictALiveHistory(t *testing.T) {
	t.Parallel()
	closed, live := terminalHistoryTab("closed"), terminalHistoryTab("live")
	database := openJournalDatabase(t, closed, live)
	policy := defaultRetention()
	policy.tabs = 1
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", policy)
	_, checkpoint, err := journal.Open(t.Context(), live, nil)
	require.NoError(t, err)
	_, err = db.New(database).CloseTerminal(t.Context(), closed.ID)
	require.NoError(t, err)
	journal.Record(closed, terminalBellFact())
	journal.Seal([]Tab{closed}, true)
	_, _, err = journal.Open(t.Context(), live, checkpoint.GetCursor())
	require.NoError(t, err)
	tails, err := db.New(database).ListSemanticEventTails(t.Context())
	require.NoError(t, err)
	require.Len(t, tails, 1)
	assert.Equal(t, live.ID, tails[0].TabID)
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

func TestClosedTerminalHistorySealsAfterTheLastAdmittedFact(t *testing.T) {
	t.Parallel()
	tab := terminalHistoryTab("closed")
	database := openJournalDatabase(t, tab)
	clock := testutil.NewQuartzMock(t)
	policy := defaultRetention()
	journal := startTestJournal(t, database, clock, "owner", policy)
	_, initial, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)
	_, err = db.New(database).CloseTerminal(t.Context(), tab.ID)
	require.NoError(t, err)

	_, _, err = journal.Open(t.Context(), tab, nil)
	require.ErrorIs(t, err, ErrAdmission)
	reader, pending, err := journal.Open(t.Context(), tab, initial.GetCursor())
	require.NoError(t, err)
	page, changed, err := reader.Read(t.Context(), pending.GetAfterSequence())
	require.NoError(t, err)
	require.Empty(t, page, "database closure alone does not seal the stream")

	journal.Record(tab, terminalBellFact())
	journal.Seal([]Tab{tab}, true)
	journal.Record(tab, terminalTitleFact("late stale publication"))
	require.NoError(t, journal.barrier(t.Context()))
	select {
	case <-changed:
	default:
		t.Fatal("the last fact and seal did not wake the reader")
	}
	page, _, err = reader.Read(t.Context(), pending.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	last := page[0].GetRecord()
	require.NotNil(t, last.GetFact().GetTerminalBell())
	assert.Equal(t, int64(1), last.GetSequence())
	page, _, err = reader.Read(t.Context(), last.GetSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, page[0].GetRetired())
	assert.Equal(t, last.GetSequence(), page[0].GetRetired().GetFinalSequence())

	require.NoError(t, journal.Stop(t.Context(), false))
	clock.Advance(policy.age + time.Second).MustWait(t.Context())
	restarted := startTestJournal(t, database, clock, "owner", policy)
	reader, resumed, err := restarted.Open(t.Context(), tab, initial.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, last.GetSequence(), resumed.GetHighWaterSequence(), "a sealed tail gains no dirty-start gap")
	page, _, err = reader.Read(t.Context(), resumed.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, gap.GetReason())
	page, _, err = reader.Read(t.Context(), gap.GetResumeAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, page[0].GetRetired())
	assert.Equal(t, last.GetSequence(), page[0].GetRetired().GetFinalSequence())
}

func TestJournalReportsAnInterruptedRetirementBeforeItSealsTheHistory(t *testing.T) {
	t.Parallel()
	tab := terminalHistoryTab("pending")
	database := openJournalDatabase(t, tab)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	_, initial, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)
	_, err = db.New(database).CloseTerminal(t.Context(), tab.ID)
	require.NoError(t, err)
	require.NoError(t, journal.Stop(t.Context(), false))

	restarted := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	reader, resumed, err := restarted.Open(t.Context(), tab, initial.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(t.Context(), resumed.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP, gap.GetReason())
	page, _, err = reader.Read(t.Context(), gap.GetResumeAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, page[0].GetRetired())
	assert.Equal(t, gap.GetResumeAfterSequence(), page[0].GetRetired().GetFinalSequence())
}

func TestIncompletePublisherDrainReportsAGapBeforeRetirement(t *testing.T) {
	t.Parallel()
	tab := terminalHistoryTab("incomplete-drain")
	database := openJournalDatabase(t, tab)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	_, initial, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)
	_, err = db.New(database).CloseTerminal(t.Context(), tab.ID)
	require.NoError(t, err)

	journal.Seal([]Tab{tab}, false)
	journal.Record(tab, terminalBellFact())
	reader, resumed, err := journal.Open(t.Context(), tab, initial.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(t.Context(), resumed.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, gap.GetReason())
	page, _, err = reader.Read(t.Context(), gap.GetResumeAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, page[0].GetRetired())
	assert.Equal(t, gap.GetResumeAfterSequence(), page[0].GetRetired().GetFinalSequence())
}

func TestJournalReplaysCommittedEventsAndWakesAnIdleReader(t *testing.T) {
	t.Parallel()
	journal := startTestJournal(t, openJournalDatabase(t, terminalHistoryTab("terminal")), quartz.NewReal(), "owner", defaultRetention())
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
	database := openJournalDatabase(t, terminalHistoryTab("first"), terminalHistoryTab("second"))
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
	journal := startTestJournal(t, openJournalDatabase(t, terminalHistoryTab("terminal")), quartz.NewReal(), "owner", policy)
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
	database := openJournalDatabase(t, terminalHistoryTab("terminal"))
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
	journal := startTestJournal(t, openJournalDatabase(t, terminalHistoryTab("terminal")), clock, "owner", defaultRetention())
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
	journal := startTestJournal(t, openJournalDatabase(t, terminalHistoryTab("first"), terminalHistoryTab("second")), quartz.NewReal(), "owner", policy)
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
	database := openJournalDatabase(t, terminalHistoryTab("terminal"))
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

func TestJournalSealsALargeTreeWithoutConsumingThePublicationQueue(t *testing.T) {
	t.Parallel()
	closed, unrelated := terminalHistoryTab("closed"), terminalHistoryTab("unrelated")
	database := openJournalDatabase(t, closed, unrelated)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	_, closedStart, err := journal.Open(t.Context(), closed, nil)
	require.NoError(t, err)
	_, unrelatedStart, err := journal.Open(t.Context(), unrelated, nil)
	require.NoError(t, err)
	_, err = db.New(database).CloseTerminal(t.Context(), closed.ID)
	require.NoError(t, err)

	tabs := []Tab{closed}
	for i := range maxPendingObservations + maxCommitBatch + 1 {
		tabs = append(tabs, terminalHistoryTab("descendant-"+strconv.Itoa(i)))
	}
	blocker, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	journal.Seal(tabs, true)
	require.NoError(t, blocker.Rollback())
	reader, _, err := journal.Open(t.Context(), closed, closedStart.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, page[0].GetRetired())
	assert.Zero(t, page[0].GetRetired().GetFinalSequence())
	_, _, err = journal.Open(t.Context(), unrelated, unrelatedStart.GetCursor())
	require.NoError(t, err)
}

func TestJournalSeparatesAgentAndTerminalPositionsWithTheSameID(t *testing.T) {
	t.Parallel()
	terminal := terminalHistoryTab("shared-id")
	agent := Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: terminal.ID}
	journal := startTestJournal(t, openJournalDatabase(t, terminal, agent), quartz.NewReal(), "owner", defaultRetention())
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

func TestJournalEvictsSealedClosedTailBeforeQuietLiveTail(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	liveOld := terminalHistoryTab("live-old")
	closedNewer := terminalHistoryTab("closed-newer")
	incoming := terminalHistoryTab("incoming")
	database := openJournalDatabase(t, liveOld, closedNewer, incoming)
	policy := defaultRetention()
	policy.tabs = 2
	journal := startTestJournal(t, database, clock, "owner", policy)

	_, liveReady, err := journal.Open(t.Context(), liveOld, nil)
	require.NoError(t, err)
	clock.Advance(time.Minute).MustWait(t.Context())
	_, _, err = journal.Open(t.Context(), closedNewer, nil)
	require.NoError(t, err)
	journal.Record(closedNewer, terminalBellFact())
	require.NoError(t, journal.barrier(t.Context()))
	_, err = db.New(database).CloseTerminal(t.Context(), closedNewer.ID)
	require.NoError(t, err)
	journal.Seal([]Tab{closedNewer}, true)
	require.NoError(t, journal.barrier(t.Context()))

	clock.Advance(time.Minute).MustWait(t.Context())
	_, _, err = journal.Open(t.Context(), incoming, nil)
	require.NoError(t, err)

	_, _, err = journal.Open(t.Context(), liveOld, liveReady.GetCursor())
	require.NoError(t, err, "a quiet live tail must not be evicted ahead of a sealed closed tail")
	tails, err := db.New(database).ListSemanticEventTails(t.Context())
	require.NoError(t, err)
	require.Len(t, tails, 2)
	ids := []string{tails[0].TabID, tails[1].TabID}
	assert.ElementsMatch(t, []string{liveOld.ID, incoming.ID}, ids)
}

func TestJournalStopJoinsWriterBeforeReturningOnCanceledContext(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t, terminalHistoryTab("terminal"))
	journal, err := Start(t.Context(), database, "worker", userid.MustNew("owner"), quartz.NewReal())
	require.NoError(t, err)

	blocker, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()

	journal.Record(terminalHistoryTab("terminal"), terminalBellFact())
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	stopErr := journal.Stop(canceled, false)
	require.ErrorIs(t, stopErr, context.Canceled)
	select {
	case <-journal.done:
	default:
		t.Fatal("Journal.Stop returned while writePublications was still running")
	}
}

func TestJournalWakeChannelsIsolateTabsAndTypesAndOpens(t *testing.T) {
	t.Parallel()
	tabA := terminalHistoryTab("tab-a")
	tabB := terminalHistoryTab("tab-b")
	agentTab := Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "agent-a"}
	database := openJournalDatabase(t, tabA, tabB, agentTab)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())

	readerA, _, err := journal.Open(t.Context(), tabA, nil)
	require.NoError(t, err)
	feedTerm, _, err := journal.OpenFeed(t.Context(), leapmuxv1.TabType_TAB_TYPE_TERMINAL, nil)
	require.NoError(t, err)

	_, wakeA, err := readerA.Read(t.Context(), 0)
	require.NoError(t, err)
	_, wakeFeedTerm, err := feedTerm.Read(t.Context(), 0)
	require.NoError(t, err)

	// Opening another tab or feed must not wake unrelated idle watchers.
	_, _, err = journal.Open(t.Context(), tabB, nil)
	require.NoError(t, err)
	_, _, err = journal.OpenFeed(t.Context(), leapmuxv1.TabType_TAB_TYPE_AGENT, nil)
	require.NoError(t, err)
	select {
	case <-wakeA:
		t.Fatal("opening another tab woke idle reader for tabA")
	case <-wakeFeedTerm:
		t.Fatal("opening another feed woke idle terminal feed reader")
	default:
	}

	// Recording an agent event must not wake terminal tab or terminal feed watchers.
	journal.Record(agentTab, &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_AgentTurnEnd{AgentTurnEnd: &leapmuxv1.AgentTurnEnd{}}})
	require.NoError(t, journal.barrier(t.Context()))
	select {
	case <-wakeA:
		t.Fatal("agent event woke idle terminal tab reader")
	case <-wakeFeedTerm:
		t.Fatal("agent event woke idle terminal feed reader")
	default:
	}

	// Recording on tabB wakes the terminal feed reader, but NOT tabA's reader.
	journal.Record(tabB, terminalBellFact())
	require.NoError(t, journal.barrier(t.Context()))
	select {
	case <-wakeA:
		t.Fatal("tabB event woke idle reader for tabA")
	default:
	}
	select {
	case <-wakeFeedTerm:
	default:
		t.Fatal("tabB event did not wake terminal feed reader")
	}

	// Recording on tabA wakes tabA's reader.
	journal.Record(tabA, terminalBellFact())
	require.NoError(t, journal.barrier(t.Context()))
	select {
	case <-wakeA:
	default:
		t.Fatal("tabA event did not wake tabA reader")
	}
}

func TestTabLookupRejectsUnsupportedTabType(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	q := db.New(database)
	unsupported := Tab{Type: leapmuxv1.TabType_TAB_TYPE_FILE, ID: "file-1"}
	_, err := unsupported.live(t.Context(), q)
	require.ErrorContains(t, err, "unsupported semantic event tab type")
	_, err = unsupported.closed(t.Context(), q)
	require.ErrorContains(t, err, "unsupported semantic event tab type")
}
