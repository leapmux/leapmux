package eventjournal

import (
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestWorkerFeedPreservesRecordsOnBothSidesOfADeletedPosition(t *testing.T) {
	t.Parallel()
	quiet, busy, other := terminalHistoryTab("quiet"), terminalHistoryTab("busy"), terminalHistoryTab("other")
	agent := Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "agent"}
	database := openJournalDatabase(t, quiet, busy, other, agent)
	policy := defaultRetention()
	policy.perTab = 1
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", policy)
	ctx := t.Context()
	_, start, err := journal.OpenFeed(ctx, quiet.Type, nil)
	require.NoError(t, err)
	assert.Zero(t, start.GetAfterSequence())

	journal.Record(quiet, terminalTitleFact("first survives"))
	journal.Record(busy, terminalTitleFact("pruned from this tab"))
	journal.Record(agent, &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_AgentActivity{AgentActivity: &leapmuxv1.SemanticAgentActivity{}}})
	journal.Record(other, terminalTitleFact("next survives"))
	journal.Record(busy, terminalBellFact())

	reader, ready, err := journal.OpenFeed(ctx, quiet.Type, start.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, int64(4), ready.GetHighWaterSequence())
	frames, _, err := reader.Read(ctx, ready.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, frames, 4)
	assert.Equal(t, quiet.ID, frames[0].GetRecord().GetTab().GetTabId())
	assert.Equal(t, int64(1), frames[0].GetRecord().GetSequence())
	gap := frames[1].GetGap()
	require.NotNil(t, gap)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_HISTORY_UNAVAILABLE, gap.GetReason())
	assert.Equal(t, int64(1), gap.GetAfterSequence())
	assert.Equal(t, int64(2), gap.GetResumeAfterSequence())
	assert.Equal(t, other.ID, frames[2].GetRecord().GetTab().GetTabId())
	assert.Equal(t, int64(3), frames[2].GetRecord().GetSequence())
	last := frames[3].GetRecord()
	require.NotNil(t, last.GetFact().GetTerminalBell())
	assert.Equal(t, busy.ID, last.GetTab().GetTabId())
	assert.Equal(t, int64(4), last.GetSequence())

	_, agentReady, err := journal.OpenFeed(ctx, agent.Type, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), agentReady.GetHighWaterSequence(), "terminal publications cannot advance the agent feed")
	_, _, err = journal.OpenFeed(ctx, agent.Type, start.GetCursor())
	require.ErrorIs(t, err, ErrFeedCursor)
	_, _, err = journal.OpenFeed(ctx, quiet.Type, journal.identity.signFeedPosition(quiet.Type, 5))
	require.ErrorIs(t, err, ErrFeedCursor)
	_, exact, err := journal.Open(ctx, busy, nil)
	require.NoError(t, err)
	_, _, err = journal.OpenFeed(ctx, busy.Type, exact.GetCursor())
	require.ErrorIs(t, err, ErrFeedCursor)
	_, _, err = journal.Open(ctx, busy, last.GetCursor())
	require.ErrorIs(t, err, ErrCursor)
	_, _, err = journal.OpenFeed(ctx, leapmuxv1.TabType_TAB_TYPE_FILE, nil)
	require.ErrorIs(t, err, ErrFeedCursor)
	page, _, err := reader.Read(ctx, last.GetSequence())
	require.NoError(t, err)
	assert.Empty(t, page)
}

func TestWorkerFeedReportsADirtyRestartEvenWithoutTabTails(t *testing.T) {
	t.Parallel()
	database := openJournalDatabase(t)
	ctx := t.Context()
	tabType := leapmuxv1.TabType_TAB_TYPE_TERMINAL
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	_, start, err := journal.OpenFeed(ctx, tabType, nil)
	require.NoError(t, err)
	require.NoError(t, journal.Stop(ctx, false))

	restarted := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	reader, ready, err := restarted.OpenFeed(ctx, tabType, start.GetCursor())
	require.NoError(t, err)
	assert.Equal(t, start.GetJournalId(), ready.GetJournalId())
	assert.Equal(t, int64(1), ready.GetHighWaterSequence())
	page, _, err := reader.Read(ctx, ready.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	assert.Zero(t, gap.GetAfterSequence())
	assert.Equal(t, int64(1), gap.GetResumeAfterSequence())
	require.NoError(t, restarted.Stop(ctx, true))

	replaced := startTestJournal(t, database, quartz.NewReal(), "another-owner", defaultRetention())
	_, _, err = replaced.OpenFeed(ctx, tabType, gap.GetCursor())
	require.ErrorIs(t, err, ErrFeedCursor)
	_, fresh, err := replaced.OpenFeed(ctx, tabType, nil)
	require.NoError(t, err)
	assert.Zero(t, fresh.GetHighWaterSequence())
	assert.NotEqual(t, start.GetJournalId(), fresh.GetJournalId())
}

func TestWorkerFeedKeepsItsPositionAfterExpirationAndAnUnverifiableClose(t *testing.T) {
	t.Parallel()
	tab, closed := terminalHistoryTab("will-expire"), terminalHistoryTab("already-closed")
	database := openJournalDatabase(t, tab, closed)
	clock := testutil.NewQuartzMock(t)
	policy := defaultRetention()
	journal := startTestJournal(t, database, clock, "owner", policy)
	ctx := t.Context()
	reader, start, err := journal.OpenFeed(ctx, tab.Type, nil)
	require.NoError(t, err)
	journal.Record(tab, terminalBellFact())
	require.NoError(t, journal.barrier(ctx))
	clock.Advance(policy.age + time.Second).MustWait(ctx)
	page, _, err := reader.Read(ctx, start.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	expired := page[0].GetGap()
	require.NotNil(t, expired)
	assert.Equal(t, int64(1), expired.GetResumeAfterSequence())

	_, err = db.New(database).CloseTerminal(ctx, closed.ID)
	require.NoError(t, err)
	journal.Record(closed, terminalBellFact())
	journal.Record(terminalHistoryTab("unknown"), terminalBellFact())
	require.NoError(t, journal.barrier(ctx))
	page, _, err = reader.Read(ctx, expired.GetResumeAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	unverifiable := page[0].GetGap()
	require.NotNil(t, unverifiable)
	assert.Equal(t, int64(1), unverifiable.GetAfterSequence())
	assert.Equal(t, int64(2), unverifiable.GetResumeAfterSequence())
	tails, err := db.New(database).ListSemanticEventTails(ctx)
	require.NoError(t, err)
	require.Len(t, tails, 1, "a late fact cannot add an exact history to a closed tab")
	assert.Equal(t, tab.ID, tails[0].TabID)
}

func TestWorkerFeedReportsLossForAPayloadOfTheWrongKind(t *testing.T) {
	t.Parallel()
	agent := Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "agent"}
	database := openJournalDatabase(t, agent)
	journal := startTestJournal(t, database, quartz.NewReal(), "owner", defaultRetention())
	ctx := t.Context()
	_, start, err := journal.OpenFeed(ctx, agent.Type, nil)
	require.NoError(t, err)

	journal.Record(agent, terminalBellFact())
	reader, ready, err := journal.OpenFeed(ctx, agent.Type, start.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(ctx, ready.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Nil(t, page[0].GetRecord(), "an agent feed must never serialize a terminal fact")
	gap := page[0].GetGap()
	require.NotNil(t, gap)
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, gap.GetReason())
	assert.Equal(t, int64(1), gap.GetResumeAfterSequence())
}

func TestWorkerFeedContinuesAcrossAPageBoundary(t *testing.T) {
	t.Parallel()
	tab := terminalHistoryTab("terminal")
	journal := startTestJournal(t, openJournalDatabase(t, tab), quartz.NewReal(), "owner", defaultRetention())
	ctx := t.Context()
	_, start, err := journal.OpenFeed(ctx, tab.Type, nil)
	require.NoError(t, err)
	for range readBatchSize + 1 {
		journal.Record(tab, terminalBellFact())
	}
	reader, ready, err := journal.OpenFeed(ctx, tab.Type, start.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(ctx, ready.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, readBatchSize)
	for i, event := range page {
		require.NotNil(t, event.GetRecord())
		assert.Equal(t, int64(i+1), event.GetRecord().GetSequence())
	}
	reader, resumed, err := journal.OpenFeed(ctx, tab.Type, page[readBatchSize-1].GetRecord().GetCursor())
	require.NoError(t, err)
	page, _, err = reader.Read(ctx, resumed.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, int64(readBatchSize+1), page[0].GetRecord().GetSequence())
}
