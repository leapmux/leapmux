package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var mergeBase = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// closedRow builds a worker record for a tab that was closed, which is the only
// kind that can be offered for resume.
func closedRow(handle, title string, at time.Time) db.ListSessionsForResumeRow {
	return db.ListSessionsForResumeRow{
		AgentSessionID: handle,
		Title:          title,
		ClosedAt:       sqltime.SQLiteNullTime{Time: at, Valid: true},
		LastActivity:   sqltime.SQLiteTime{Time: at},
	}
}

// openRow builds a worker record for a tab that is still open.
func openRow(handle, title string, at time.Time) db.ListSessionsForResumeRow {
	return db.ListSessionsForResumeRow{
		AgentSessionID: handle,
		Title:          title,
		LastActivity:   sqltime.SQLiteTime{Time: at},
	}
}

// summaryHandles reduces a response to its handles.
func summaryHandles(sessions []*leapmuxv1.AgentSessionSummary) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.GetSessionId())
	}
	return out
}

func TestMergeSessionSummaries_OrdersNewestFirstAcrossBothSources(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{
			closedRow("worker-old", "Worker old", mergeBase.Add(-3*time.Hour)),
			closedRow("worker-new", "Worker new", mergeBase.Add(-time.Hour)),
		},
		[]agent.StoredSession{
			{Handle: "store-newest", Title: "Store newest", UpdatedAt: mergeBase},
			{Handle: "store-mid", Title: "Store mid", UpdatedAt: mergeBase.Add(-2 * time.Hour)},
		},
		maxListedSessions,
	)

	assert.Equal(t,
		[]string{"store-newest", "worker-new", "store-mid", "worker-old"},
		summaryHandles(got),
		"the two sources interleave by recency; neither is grouped ahead of the other")
}

// TestMergeSessionSummaries_WorkerRecordWinsADuplicate pins the dedupe
// preference: the worker wrote that record, and its title is the tab title the
// user chose, which the provider's store does not know.
func TestMergeSessionSummaries_WorkerRecordWinsADuplicate(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{closedRow("shared", "The name I gave it", mergeBase)},
		[]agent.StoredSession{{Handle: "shared", Title: "What the CLI called it", UpdatedAt: mergeBase.Add(time.Hour)}},
		maxListedSessions,
	)

	require.Len(t, got, 1, "one handle, one entry")
	assert.Equal(t, "shared", got[0].GetSessionId())
	assert.Equal(t, "The name I gave it", got[0].GetTitle())
	// Every field comes from the worker's record, the timestamp included: a
	// merge that took the later time from one source and the title from the
	// other would be a record neither source ever held.
	assert.Equal(t, "2026-09-01T12:00:00.000Z", got[0].GetUpdatedAt())
}

// TestMergeSessionSummaries_ExcludesAnOpenSession is the safety rule: a live
// process is attached to that handle, and resuming it into a second tab would
// run two processes against one session store.
func TestMergeSessionSummaries_ExcludesAnOpenSession(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{
			openRow("live", "Open right now", mergeBase),
			closedRow("closed", "Closed earlier", mergeBase.Add(-time.Hour)),
		},
		nil,
		maxListedSessions,
	)
	assert.Equal(t, []string{"closed"}, summaryHandles(got))
}

// TestMergeSessionSummaries_ExclusionSurvivesTheProviderStore is why the
// exclusion happens after the merge rather than inside the SQL: the provider's
// own store lists the open session too, and would re-introduce it.
func TestMergeSessionSummaries_ExclusionSurvivesTheProviderStore(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{openRow("live", "Open right now", mergeBase)},
		[]agent.StoredSession{
			{Handle: "live", Title: "The CLI sees it too", UpdatedAt: mergeBase},
			{Handle: "other", Title: "Fine to resume", UpdatedAt: mergeBase.Add(-time.Hour)},
		},
		maxListedSessions,
	)
	assert.Equal(t, []string{"other"}, summaryHandles(got))
}

func TestMergeSessionSummaries_BreaksATieByHandle(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		nil,
		[]agent.StoredSession{
			{Handle: "b", UpdatedAt: mergeBase},
			{Handle: "a", UpdatedAt: mergeBase},
			{Handle: "c", UpdatedAt: mergeBase},
		},
		maxListedSessions,
	)
	assert.Equal(t, []string{"a", "b", "c"}, summaryHandles(got),
		"one stable order, so two calls cannot disagree")
}

func TestMergeSessionSummaries_CapsToTheLimit(t *testing.T) {
	t.Parallel()
	stored := make([]agent.StoredSession, 0, 100)
	for i := range 100 {
		stored = append(stored, agent.StoredSession{
			Handle:    string(rune('a'+i%26)) + string(rune('a'+i/26)),
			UpdatedAt: mergeBase.Add(-time.Duration(i) * time.Minute),
		})
	}

	got := mergeSessionSummaries(nil, stored, maxListedSessions)
	assert.Len(t, got, maxListedSessions)
	assert.Equal(t, "aa", got[0].GetSessionId(), "the newest survives the cap")
}

func TestMergeSessionSummaries_DropsAnEmptyHandle(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{closedRow("   ", "whitespace only", mergeBase)},
		[]agent.StoredSession{
			{Handle: "", Title: "no handle", UpdatedAt: mergeBase},
			{Handle: "real", Title: "resumable", UpdatedAt: mergeBase.Add(-time.Hour)},
		},
		maxListedSessions,
	)
	assert.Equal(t, []string{"real"}, summaryHandles(got),
		"a record this worker cannot resume is not a choice to offer")
}

// TestMergeSessionSummaries_UnknownTimeIsAnEmptyString pins the wire shape:
// Pi stores no timestamp for some sessions, and formatting the zero instant
// would show a client a session from the year one.
func TestMergeSessionSummaries_UnknownTimeIsAnEmptyString(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(nil, []agent.StoredSession{
		{Handle: "timed", Title: "t", UpdatedAt: mergeBase},
		{Handle: "untimed", Title: "t"},
	}, maxListedSessions)

	require.Len(t, got, 2)
	assert.Equal(t, "2026-09-01T12:00:00.000Z", got[0].GetUpdatedAt())
	assert.Empty(t, got[1].GetUpdatedAt())
	assert.Equal(t, []string{"timed", "untimed"}, summaryHandles(got),
		"an unknown time sorts last, not to the epoch")
}

func TestMergeSessionSummaries_EmptyInputs(t *testing.T) {
	t.Parallel()
	assert.Empty(t, mergeSessionSummaries(nil, nil, maxListedSessions))
	assert.Empty(t, mergeSessionSummaries([]db.ListSessionsForResumeRow{}, []agent.StoredSession{}, maxListedSessions))
}

// TestMergeSessionSummaries_DedupesWithinOneSource covers a store that lists
// one handle twice, which no reader should do but which must not produce two
// menu rows if one does.
func TestMergeSessionSummaries_DedupesWithinOneSource(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(nil, []agent.StoredSession{
		{Handle: "dup", Title: "first", UpdatedAt: mergeBase},
		{Handle: "dup", Title: "second", UpdatedAt: mergeBase.Add(-time.Hour)},
	}, maxListedSessions)

	require.Len(t, got, 1)
	assert.Equal(t, "first", got[0].GetTitle())
}

// TestMergeSessionSummaries_KeepsAnEmptyTitle pins that a missing title stays
// missing on the wire. Pi stores none and a fresh Claude session has none, and
// the client shows the handle in its place rather than the server inventing one.
func TestMergeSessionSummaries_KeepsAnEmptyTitle(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(nil,
		[]agent.StoredSession{{Handle: "untitled", UpdatedAt: mergeBase}}, maxListedSessions)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].GetTitle())
	assert.Equal(t, "untitled", got[0].GetSessionId())
}

// --- handler-level ---

// seedResumableAgent records a closed agent tab with a resume handle, which is
// what the worker's own half of the listing reads.
func seedResumableAgent(t *testing.T, svc *Service, id, handle, title, workingDir string, provider leapmuxv1.AgentProvider, closed bool) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            id,
		WorkingDir:    workingDir,
		HomeDir:       "/tmp",
		Title:         title,
		AgentProvider: provider,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: handle,
		ID:             id,
	}))
	if closed {
		_, err := svc.Queries.CloseAgent(ctx, id)
		require.NoError(t, err)
	}
}

// listAgentSessions drives the RPC and returns the decoded response.
func listAgentSessions(t *testing.T, d *channel.Dispatcher, w *testResponseWriter, provider leapmuxv1.AgentProvider, workingDir string) *leapmuxv1.ListAgentSessionsResponse {
	t.Helper()
	dispatch(d, "ListAgentSessions", &leapmuxv1.ListAgentSessionsRequest{
		AgentProvider: provider,
		WorkingDir:    workingDir,
	}, w)
	require.Empty(t, w.rejections(), "the RPC must succeed")
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentSessionsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp
}

func TestListAgentSessions_ReturnsClosedSessionsOfTheDirectory(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := t.TempDir()
	other := t.TempDir()

	seedResumableAgent(t, svc, "a-1", "handle-1", "First session", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)
	seedResumableAgent(t, svc, "a-2", "handle-2", "Second session", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)
	// Another directory, another provider, and a tab that never got a handle:
	// none of the three is an answer to this question.
	seedResumableAgent(t, svc, "a-3", "handle-3", "Elsewhere", other, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)
	seedResumableAgent(t, svc, "a-4", "handle-4", "Other provider", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, true)
	seedResumableAgent(t, svc, "a-5", "", "No handle", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, dir)
	assert.ElementsMatch(t, []string{"handle-1", "handle-2"}, summaryHandles(resp.GetSessions()))
}

// TestListAgentSessions_ExcludesAnOpenTab is the safety rule at the RPC level:
// a session a live tab is attached to must never be offered for resume.
func TestListAgentSessions_ExcludesAnOpenTab(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := t.TempDir()

	seedResumableAgent(t, svc, "a-open", "handle-open", "Open now", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, false)
	seedResumableAgent(t, svc, "a-closed", "handle-closed", "Closed", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, dir)
	assert.Equal(t, []string{"handle-closed"}, summaryHandles(resp.GetSessions()))
}

func TestListAgentSessions_ExcludesSubagentRows(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	ctx := context.Background()
	dir := t.TempDir()

	seedResumableAgent(t, svc, "parent", "handle-parent", "Parent", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true)
	// A virtual child row holds a subagent transcript and has no session of
	// its own to resume.
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child",
		ParentAgentID: sql.NullString{String: "parent", Valid: true},
		SpawnSpanID:   "span-1",
		WorkingDir:    dir,
		HomeDir:       "/tmp",
		Title:         "Subagent",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "handle-child",
		ID:             "child",
	}))
	_, err := svc.Queries.CloseAgent(ctx, "child")
	require.NoError(t, err)

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, dir)
	assert.Equal(t, []string{"handle-parent"}, summaryHandles(resp.GetSessions()))
}

func TestListAgentSessions_RefusesAnUnacceptableWorkingDir(t *testing.T) {
	t.Parallel()
	_, d, w := setupTestService(t)

	dispatch(d, "ListAgentSessions", &leapmuxv1.ListAgentSessionsRequest{
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    "relative/path",
	}, w)

	require.Len(t, w.rejections(), 1, "the same path gate every file and git handler applies")
	assert.Empty(t, w.responses)
}

// TestListAgentSessions_SurvivesAProviderStoreFailure pins the degradation
// rule: a provider store is another program's data, and a failure to read it
// must not lose the records the worker itself holds.
func TestListAgentSessions_SurvivesAProviderStoreFailure(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := t.TempDir()
	seedResumableAgent(t, svc, "a-1", "handle-1", "Mine", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, true)

	// A file where OpenCode's database belongs, so opening it as SQLite fails.
	// svc.HomeDir is the temp home setupTestService assigned, and the reader
	// resolves the store beneath it.
	badStore := filepath.Join(svc.HomeDir, ".local", "share", "opencode", "opencode.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(badStore), 0o755))
	require.NoError(t, os.WriteFile(badStore, []byte("this is not a database"), 0o644))

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, dir)
	assert.Equal(t, []string{"handle-1"}, summaryHandles(resp.GetSessions()),
		"the worker's own records still answer when the provider's store cannot be read")
}

// TestListAgentSessions_MergesTheProviderStore drives the whole path with a
// real store on disk, so the handler's two halves are proven to meet.
func TestListAgentSessions_MergesTheProviderStore(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := t.TempDir()

	// One session LeapMux started, one the CLI started on its own, and one
	// they both know about.
	seedResumableAgent(t, svc, "a-1", "ses_worker", "From LeapMux", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, true)
	seedResumableAgent(t, svc, "a-2", "ses_shared", "The name I gave it", dir, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, true)

	store := filepath.Join(svc.HomeDir, ".local", "share", "opencode", "opencode.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(store), 0o755))
	sqlDB, err := sqlitedb.Open(store, sqlitedb.Config{})
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE TABLE session (
		id text PRIMARY KEY, parent_id text, directory text NOT NULL, title text NOT NULL DEFAULT '',
		time_created integer NOT NULL, time_updated integer, time_archived integer)`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES
		('ses_cli', ?, 'Started outside LeapMux', 1000, 3000),
		('ses_shared', ?, 'What the CLI called it', 1000, 2000)`, dir, dir)
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, dir)

	assert.ElementsMatch(t, []string{"ses_worker", "ses_shared", "ses_cli"},
		summaryHandles(resp.GetSessions()), "the shared handle appears once")
	for _, s := range resp.GetSessions() {
		if s.GetSessionId() == "ses_shared" {
			assert.Equal(t, "The name I gave it", s.GetTitle(),
				"the worker's record wins a duplicate, so the user sees the title they chose")
		}
	}
}

func TestListAgentSessions_EmptyStoreAndNoRecordsIsAnEmptyList(t *testing.T) {
	t.Parallel()
	_, d, w := setupTestService(t)

	resp := listAgentSessions(t, d, w, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, t.TempDir())
	assert.Empty(t, resp.GetSessions(), "nothing to resume is a successful empty answer, not an error")
}

// Resuming a session is what puts TWO worker rows on one handle: the tab that
// was closed keeps its record, and the tab that resumed it adds another. The
// second is open, so the handle must stay out of the list -- a live process is
// attached to that session store.
//
// The exclusion set is therefore built from every row before any row becomes a
// list entry. A check of "is THIS row closed?" would offer the older record and
// hand the user a session already in use, which is reachable by resuming once
// and opening the dialog again.
func TestMergeSessionSummaries_AClosedRowCannotReviveAnOpenHandle(t *testing.T) {
	t.Parallel()
	got := mergeSessionSummaries(
		[]db.ListSessionsForResumeRow{
			// The closed record comes FIRST, so a row-local check reaches it
			// before it ever sees the open one.
			closedRow("resumed", "The tab that was closed", mergeBase.Add(-time.Hour)),
			openRow("resumed", "The tab that resumed it", mergeBase),
			closedRow("other", "Free to resume", mergeBase.Add(-2*time.Hour)),
		},
		[]agent.StoredSession{{Handle: "resumed", Title: "The CLI sees it too", UpdatedAt: mergeBase}},
		maxListedSessions,
	)
	assert.Equal(t, []string{"other"}, summaryHandles(got))
}
